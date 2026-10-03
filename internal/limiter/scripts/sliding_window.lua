-- Sliding window log, atomic in a single Redis script.
-- Exact: at most `limit` units are admitted in ANY window-length interval
-- (no fixed-window boundary burst). Cost: O(limit) memory per key.
-- KEYS[1] = sorted set; score = admit time (ms), member = "<request id>:<i>"
-- ARGV[1] = limit, ARGV[2] = window (ms), ARGV[3] = cost, ARGV[4] = unique request id
-- Returns { allowed (1|0), remaining, retry_after_ms, reset_after_ms }

local key    = KEYS[1]
local limit  = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local cost   = tonumber(ARGV[3])
local id     = ARGV[4]

local t   = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000) -- ms

-- Evict entries that have slid out of the window (score <= now - window).
redis.call('ZREMRANGEBYSCORE', key, '-inf', now - window)
local count = redis.call('ZCARD', key)

local allowed, retry = 0, 0
if count + cost <= limit then
  for i = 1, cost do
    redis.call('ZADD', key, now, id .. ':' .. i)
  end
  count   = count + cost
  allowed = 1
else
  -- The (count + cost - limit)-th oldest entry must expire before this fits.
  local need = count + cost - limit
  local old  = redis.call('ZRANGE', key, need - 1, need - 1, 'WITHSCORES')
  retry = math.max(1, tonumber(old[2]) + window - now)
end

local reset = 0
if count > 0 then
  redis.call('PEXPIRE', key, window)
  local newest = redis.call('ZRANGE', key, -1, -1, 'WITHSCORES')
  reset = math.max(0, tonumber(newest[2]) + window - now)
end

return { allowed, limit - count, retry, reset }
