package lease

import "github.com/valkey-io/valkey-go"

var redisScript = valkey.NewLuaScript(`
local writer, shared, waiting = KEYS[1], KEYS[2], KEYS[3]
local op, ttl, token = ARGV[1], tonumber(ARGV[2]), ARGV[3]
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
local expires = now + ttl

local function put(key, member)
    redis.call('ZADD', key, expires, member)
    local latest = redis.call('ZREVRANGE', key, 0, 0, 'WITHSCORES')
    redis.call('PEXPIREAT', key, latest[2])
end

if op == 'release' then
    redis.call('ZREM', shared, token)
    return 1
end
if op == 'cancel' then
    redis.call('ZREM', waiting, token)
    return 1
end

redis.call('ZREMRANGEBYSCORE', shared, '-inf', now)
if op == 'renew' then
    if not redis.call('ZSCORE', shared, token) then return -1 end
    put(shared, token)
    return 1
end

redis.call('ZREMRANGEBYSCORE', waiting, '-inf', now)
if op == 'write' then
    local waiter = ARGV[4]
    if ARGV[5] == '1' and not redis.call('ZSCORE', waiting, waiter) then return -1 end
    if redis.call('EXISTS', writer) == 1 or redis.call('ZCARD', shared) > 0 then
        if waiter ~= '' then put(waiting, waiter) end
        return 0
    end
    if waiter == '' and redis.call('ZCARD', waiting) > 0 then return 0 end
    if not redis.call('SET', writer, token, 'NX', 'PX', ttl) then return 0 end
    if waiter ~= '' then redis.call('ZREM', waiting, waiter) end
    return 1
end

if op == 'share' then
    if redis.call('EXISTS', writer) == 1 or redis.call('ZCARD', waiting) > 0 then return 0 end
    local group = ARGV[4]
    if group ~= '' then
        for _, member in ipairs(redis.call('ZRANGE', shared, 0, -1)) do
            local delimiter = string.find(member, ':', 1, true)
            if delimiter and string.sub(member, 1, delimiter - 1) ~= group then return 0 end
        end
    end
    put(shared, token)
    return 1
end
return redis.error_reply('unknown lease operation')
`)
