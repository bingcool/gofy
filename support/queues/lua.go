package queues

import goredis "github.com/redis/go-redis/v9"

// promoteRetryScript atomically moves due messages from the retry ZSET back to the
// main queue.
// KEYS[1] retry ZSET, KEYS[2] main queue; ARGV[1] current time, ARGV[2] maximum
// number promoted in one call.
// promoteRetryScript 把重试 ZSET 中已到期的消息原子地回流到主队列。
// KEYS[1] 重试 ZSET，KEYS[2] 主队列；ARGV[1] 当前时间，ARGV[2] 单次最多回流条数。
var promoteRetryScript = goredis.NewScript(`
local retryKey = KEYS[1]
local queueKey = KEYS[2]
local now = ARGV[1]
local limit = tonumber(ARGV[2])

local members = redis.call('ZRANGEBYSCORE', retryKey, '-inf', now, 'LIMIT', 0, limit)
if #members == 0 then
    return 0
end

for _, member in ipairs(members) do
    redis.call('LPUSH', queueKey, member)
end
redis.call('ZREM', retryKey, unpack(members))

return #members
`)

// popDueScript takes and deletes due messages from the delayed ZSET, which
// guarantees one message is consumed only once.
// KEYS[1] delayed ZSET; ARGV[1] current time, ARGV[2] maximum number taken in one
// call.
// popDueScript 取出并删除延迟 ZSET 中已到期的消息，保证同一消息只会被消费一次。
// KEYS[1] 延迟 ZSET；ARGV[1] 当前时间，ARGV[2] 单次最多取出条数。
var popDueScript = goredis.NewScript(`
local key = KEYS[1]
local now = ARGV[1]
local limit = tonumber(ARGV[2])

local members = redis.call('ZRANGEBYSCORE', key, '-inf', now, 'LIMIT', 0, limit)
if #members > 0 then
    redis.call('ZREM', key, unpack(members))
end

return members
`)
