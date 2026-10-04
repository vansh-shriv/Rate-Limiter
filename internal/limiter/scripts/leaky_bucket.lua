-- Leaky bucket as a shaping queue, atomic in a single Redis script.
-- Requests enter a virtual FIFO queue that drains at a constant rate (one unit every `interval` ms).
-- Unlike the token bucket there is no burst: admitted requests are SPACED OUT. The script returns the
-- delay each admitted request must observe; if the queue is full the request is rejected.
-- Only one number of state is kept: `next_free`, the time the queue will be empty again.
--
-- KEYS[1] = state key (string: next_free in ms)
-- ARGV[1] = capacity (max queued units), ARGV[2] = leak rate (units/sec), ARGV[3] = cost
-- Returns { allowed, remaining, retry_after_ms, reset_after_ms, delay_ms }

local key      = KEYS[1]
local capacity = tonumber(ARGV[1])
local rate     = tonumber(ARGV[2])
local cost     = tonumber(ARGV[3])

local interval = 1000 / rate -- ms per unit

local t   = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)

local nf = tonumber(redis.call('GET', key))
if nf == nil or nf < now then nf = now end -- queue already drained

local wait     = nf - now
local max_wait = (capacity - cost) * interval -- admit only if the request still fits in the queue

if wait <= max_wait then
  local new_nf = nf + cost * interval
  local drain  = new_nf - now
  redis.call('SET', key, string.format('%.3f', new_nf), 'PX', math.ceil(drain) + 1000)
  local remaining = math.floor(capacity - drain / interval + 1e-9)
  return { 1, math.max(0, remaining), 0, math.ceil(drain), math.ceil(wait) }
end

local remaining = math.floor(capacity - wait / interval + 1e-9)
return { 0, math.max(0, remaining), math.max(1, math.ceil(wait - max_wait)), math.ceil(wait), 0 }
