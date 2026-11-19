package lease

import "github.com/valkey-io/valkey-go"

// redisScript keeps the writer, shared holders, waiting writers and holder
// names of one key consistent. Every op that changes ownership changes the
// holder hash in the same call; sync drops dead entries and keeps the hash
// alive exactly as long as its longest-lived holder.
var redisScript = valkey.NewLuaScript(`
local writer, shared, waiting, holders = KEYS[1], KEYS[2], KEYS[3], KEYS[4]
local op, ttl, token = ARGV[1], tonumber(ARGV[2]), ARGV[3]
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
local expires = now + ttl

local function put(key, member)
    redis.call('ZADD', key, expires, member)
    local latest = redis.call('ZREVRANGE', key, 0, 0, 'WITHSCORES')
    redis.call('PEXPIREAT', key, latest[2])
end

local function live(member)
    if redis.call('GET', writer) == member then return true end
    local score = redis.call('ZSCORE', shared, member)
    return score ~= false and tonumber(score) > now
end

local function sync()
    for _, field in ipairs(redis.call('HKEYS', holders)) do
        if not live(field) then redis.call('HDEL', holders, field) end
    end
    local latest = 0
    local remaining = redis.call('PTTL', writer)
    if remaining > 0 then latest = now + remaining end
    local top = redis.call('ZREVRANGE', shared, 0, 0, 'WITHSCORES')
    if top[2] and tonumber(top[2]) > latest then latest = tonumber(top[2]) end
    if latest > now then
        redis.call('PEXPIREAT', holders, latest)
    else
        redis.call('DEL', holders)
    end
end

if op == 'holders' then
    local result = {}
    local entries = redis.call('HGETALL', holders)
    for i = 1, #entries, 2 do
        if live(entries[i]) then table.insert(result, entries[i + 1]) end
    end
    return result
end
if op == 'release' then
    redis.call('ZREM', shared, token)
    redis.call('HDEL', holders, token)
    sync()
    return 1
end
if op == 'unwrite' then
    if redis.call('GET', writer) == token then redis.call('DEL', writer) end
    redis.call('HDEL', holders, token)
    sync()
    return 1
end
if op == 'cancel' then
    redis.call('ZREM', waiting, token)
    return 1
end
if op == 'extend' then
    if redis.call('GET', writer) ~= token then
        redis.call('HDEL', holders, token)
        sync()
        return -1
    end
    redis.call('PEXPIRE', writer, ttl)
    sync()
    return 1
end

redis.call('ZREMRANGEBYSCORE', shared, '-inf', now)
if op == 'renew' then
    if not redis.call('ZSCORE', shared, token) then
        redis.call('HDEL', holders, token)
        sync()
        return -1
    end
    put(shared, token)
    sync()
    return 1
end

redis.call('ZREMRANGEBYSCORE', waiting, '-inf', now)
if op == 'write' then
    local waiter, record = ARGV[4], ARGV[6]
    if ARGV[5] == '1' and not redis.call('ZSCORE', waiting, waiter) then return -1 end
    if redis.call('EXISTS', writer) == 1 or redis.call('ZCARD', shared) > 0 then
        if waiter ~= '' then put(waiting, waiter) end
        return 0
    end
    if waiter == '' and redis.call('ZCARD', waiting) > 0 then return 0 end
    if not redis.call('SET', writer, token, 'NX', 'PX', ttl) then return 0 end
    if waiter ~= '' then redis.call('ZREM', waiting, waiter) end
    redis.call('HSET', holders, token, record)
    sync()
    return 1
end

if op == 'share' then
    if redis.call('EXISTS', writer) == 1 or redis.call('ZCARD', waiting) > 0 then return 0 end
    local group, record = ARGV[4], ARGV[5]
    if group ~= '' then
        for _, member in ipairs(redis.call('ZRANGE', shared, 0, -1)) do
            local delimiter = string.find(member, ':', 1, true)
            if delimiter and string.sub(member, 1, delimiter - 1) ~= group then return 0 end
        end
    end
    put(shared, token)
    redis.call('HSET', holders, token, record)
    sync()
    return 1
end
return redis.error_reply('unknown lease operation')
`)
