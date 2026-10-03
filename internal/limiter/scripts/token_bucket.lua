-- Token bucket, atomic in a single Redis script.
-- KEYS[1] = bucket key (hash: tokens, ts)
-- ARGV[1] = capacity (max tokens / burst)
-- ARGV[2] = refill rate (tokens per second, may be fractional)
-- ARGV[3] = cost (tokens requested)
-- Returns { allowed (1|0), remaining (floor), retry_after_ms, reset_after_ms }
-- Time comes from Redis TIME so gateway clock skew is irrelevant.

local key      = KEYS[1]
local capacity = tonumber(ARGV[1])
local rate     = tonumber(ARGV[2])
local cost     = tonumber(ARGV[3])

local t   = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000) -- ms

local data   = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts     = tonumber(data[2])

if tokens == nil or ts == nil then
  tokens = capacity -- new bucket starts full
  ts = now
end

-- Lazy refill: no background job, just math on read.
local elapsed = math.max(0, now - ts)
tokens = math.min(capacity, tokens + elapsed * rate / 1000)

local allowed = 0
local retry   = 0
if tokens >= cost then
  tokens  = tokens - cost
  allowed = 1
else
  retry = math.ceil((cost - tokens) * 1000 / rate)
end

redis.call('HSET', key, 'tokens', tokens, 'ts', now)
-- Idle buckets are equivalent to full ones, so expire after a full refill (+1s slack).
redis.call('PEXPIRE', key, math.ceil(capacity * 1000 / rate) + 1000)

local reset = math.ceil((capacity - tokens) * 1000 / rate)
return { allowed, math.floor(tokens), retry, reset }
