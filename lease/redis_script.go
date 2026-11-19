package lease

import "github.com/valkey-io/valkey-go"

var redisScript = valkey.NewLuaScriptNoSha(`
local writer, shared, waiting, grouped = KEYS[1], KEYS[2], KEYS[3], KEYS[4]
local op, ttl, token = ARGV[1], tonumber(ARGV[2]), ARGV[3]
local clock = redis.call('TIME')
local now = tonumber(clock[1]) * 1000 + math.floor(tonumber(clock[2]) / 1000)
local expires = now + ttl

local function put(key, member)
    redis.call('ZADD', key, expires, member)
    local latest = redis.call('ZREVRANGE', key, 0, 0, 'WITHSCORES')
    redis.call('PEXPIREAT', key, latest[2])
    return latest[2]
end

local function groupOf(member)
    return string.match(member, '^([^:]+):')
end

local function putShared()
    local latest = put(shared, token)
    if redis.call('EXISTS', grouped) == 1 then
        if groupOf(token) then redis.call('ZADD', grouped, expires, token) end
        redis.call('PEXPIREAT', grouped, latest)
    end
end

local function currentGroup()
    if redis.call('EXISTS', grouped) == 0 then
        -- Mark the index initialized even when the shared set contains only readers.
        redis.call('ZADD', grouped, 0, '')
        -- Index records written before the grouped index was introduced.
        local members = redis.call('ZRANGE', shared, 0, -1, 'WITHSCORES')
        for i = 1, #members, 2 do
            if groupOf(members[i]) then redis.call('ZADD', grouped, members[i+1], members[i]) end
        end
        if #members > 0 then redis.call('PEXPIREAT', grouped, members[#members]) end
    end
    redis.call('ZREMRANGEBYSCORE', grouped, '(0', now)
    local latest = redis.call('ZREVRANGE', grouped, 0, 0)
    return groupOf(latest[1] or '')
end

if op == 'release' then
    redis.call('ZREM', shared, token)
    if redis.call('EXISTS', shared) == 0 then
        redis.call('DEL', grouped)
    elseif groupOf(token) then
        redis.call('ZREM', grouped, token)
    end
    return 1
end
if op == 'cancel' then
    redis.call('ZREM', waiting, token)
    return 1
end

redis.call('ZREMRANGEBYSCORE', shared, '-inf', now)
if op == 'renew' then
    if not redis.call('ZSCORE', shared, token) then return -1 end
    putShared()
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
    -- TTL expiry and Done() signal lease loss. Protected storage must enforce fencing for exclusive writes.
    if not redis.call('SET', writer, token, 'NX', 'PX', ttl) then return 0 end
    if waiter ~= '' then redis.call('ZREM', waiting, waiter) end
    return 1
end

if op == 'share' then
    if redis.call('EXISTS', writer) == 1 or redis.call('ZCARD', waiting) > 0 then return 0 end
    local group = ARGV[4]
    if group ~= '' then
        local active = currentGroup()
        if active and active ~= group then return 0 end
    end
    putShared()
    return 1
end
return redis.error_reply('unknown lease operation')
`)
