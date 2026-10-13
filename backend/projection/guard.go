package projection

import "github.com/redis/go-redis/v9"

// 读模型版本守卫键空间：与 room:members:* / active_rooms 隔离的独立命名空间
// 房间短码由客户端输入、为任意字符串，独立前缀可避免守卫键与任何读模型键互相歧义
const (
	// rosterGuardPrefix 成员花名册水位哈希键前缀，完整键 room:guard:members:{room_code}
	// 结构：HASH，field = user_id，value = 已应用到该成员的单调业务序号
	rosterGuardPrefix = "room:guard:members:"

	// indexGuardKey 活跃房间索引水位哈希键（全局单键），field = room_code，value = 已应用到该房间的业务序号
	indexGuardKey = "room:guard:index"
)

// rosterApplyScript 成员花名册条件应用脚本（LWW 版本守卫）
// 仅当事件业务序号大于该成员已应用水位时才执行 join/left，并把水位推进到该序号
// 抵御至少一次投递下的乱序重投：较早的 room.left 晚到会被拒绝，不误删已重新加入的成员
// KEYS[1] 花名册 HASH，KEYS[2] 水位 HASH；ARGV[1] user_id，ARGV[2] 业务序号，ARGV[3] 操作（join/left），ARGV[4] username
var rosterApplyScript = redis.NewScript(`
local cur = redis.call('HGET', KEYS[2], ARGV[1])
if cur and tonumber(ARGV[2]) <= tonumber(cur) then
  return 0
end
if ARGV[3] == 'join' then
  redis.call('HSET', KEYS[1], ARGV[1], ARGV[4])
else
  redis.call('HDEL', KEYS[1], ARGV[1])
end
redis.call('HSET', KEYS[2], ARGV[1], ARGV[2])
return 1
`)

// indexApplyScript 活跃房间索引条件应用脚本（LWW 版本守卫）
// 仅当事件业务序号大于该房间已应用水位时才执行 created/closed，并把水位推进到该序号
// 抵御乱序重投：较早的 room.created / room.closed 晚到不会把已关闭房间复活、不会误清复开状态
// KEYS[1] 活跃房间 SET，KEYS[2] 水位 HASH；ARGV[1] room_code，ARGV[2] 业务序号，ARGV[3] 操作（created/closed）
var indexApplyScript = redis.NewScript(`
local cur = redis.call('HGET', KEYS[2], ARGV[1])
if cur and tonumber(ARGV[2]) <= tonumber(cur) then
  return 0
end
if ARGV[3] == 'created' then
  redis.call('SADD', KEYS[1], ARGV[1])
else
  redis.call('SREM', KEYS[1], ARGV[1])
end
redis.call('HSET', KEYS[2], ARGV[1], ARGV[2])
return 1
`)
