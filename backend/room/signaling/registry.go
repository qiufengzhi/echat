package signaling

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"echat-backend/config"
	"echat-backend/logging"
	"echat-backend/room/aggregate"
	"echat-backend/room/gateway"
	"echat-backend/room/persist"
	"echat-backend/room/projection"
	"echat-backend/sfu"
)

// logger signaling 包的具名日志器，沿用原 room 域名
var logger = logging.New("room")

var (
	// allSignalRooms 信令层在线房间注册表：只承载 active 房间，全员离开即删除
	allSignalRooms = make(map[string]*aggregate.Room)
	// roomsLock 保护 allSignalRooms 的并发读写
	roomsLock sync.RWMutex
	// sfuServer 全局 SFU 引擎实例，管理所有房间的 WebRTC PeerConnection 和音频转发
	sfuServer = sfu.NewSFUServer()
)

var (
	// persister 事实落库器（join/leave/交接/清空），注入前为 nil 时跳过落库
	persister aggregate.FactStore
	// recorder 孤立事件记账器（成员管理 / AI 开关），注入前为 nil 时跳过记账
	recorder aggregate.EventRecorder
	// roleProjector SpiceDB 授权投影器，内部判空降级
	roleProjector = projection.AuthzProjector{}
)

// SetStore 注入 pgx 连接池，事实落库与事件记账共用同一池
// st 由 App 启动时传入 store.Store.Pool() 的产物，与事务性 outbox relay 共用
func SetStore(st *pgxpool.Pool) {
	p := persist.New(st)
	persister = p
	recorder = p
}

// createRoom 创建房间聚合；如果房间已存在，则直接返回已有房间
// roomID 前端传入或生成的房间号，调用前应已做空值校验
func createRoom(roomID string) *aggregate.Room {
	roomsLock.Lock()
	defer roomsLock.Unlock()
	if existing, ok := allSignalRooms[roomID]; ok {
		return existing
	}
	r := aggregate.NewRoom(roomID)
	r.AggID = uuid.NewString() // 内部聚合根 id，事件骨干排列坐标；对外只暴露 roomID 短码
	allSignalRooms[roomID] = r
	logger.Infow("房间已创建", "roomID", roomID)
	return r
}

// getOrCreateRoom 先查找房间，不存在时再创建，避免调用方重复写判断逻辑
// 返回值始终是可用房间实例
func getOrCreateRoom(roomID string) *aggregate.Room {
	roomsLock.RLock()
	r, exists := allSignalRooms[roomID]
	roomsLock.RUnlock()
	if exists {
		return r
	}
	return createRoom(roomID)
}

// getRoomByID 按短码返回在线房间；不存在返回 nil
func getRoomByID(roomID string) *aggregate.Room {
	roomsLock.RLock()
	r, ok := allSignalRooms[roomID]
	roomsLock.RUnlock()
	if !ok {
		return nil
	}
	return r
}

// Exists 判断指定频道号当前是否有在线房间（信令层有 room 实例即为可加入）
// 房间是内存动态态，全部成员离开即删除，故「存在」与「当前在线」等价
func Exists(roomID string) bool {
	roomsLock.RLock()
	defer roomsLock.RUnlock()
	_, ok := allSignalRooms[roomID]
	return ok
}

// StartCleanupLoop 启动空房间清理协程与在线热状态扫刷协程
// 正常离开时房间会立即尝试删除；这里主要兜底处理异常断开后残留的空房间
func StartCleanupLoop() {
	go cleanupIdleRooms()
	projection.StartOnlineSweep()
}

// cleanupIdleRooms 定时扫描所有房间，清理空房间
// 扫描间隔来自配置文件的 room.idle_timeout
func cleanupIdleRooms() {
	interval, _ := time.ParseDuration(config.Get().Room.IdleTimeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		roomsLock.Lock()
		for id, r := range allSignalRooms {
			r.Lock.RLock()
			empty := len(r.Clients) == 0
			r.Lock.RUnlock()
			if empty {
				delete(allSignalRooms, id)
				logger.Infow("清理空闲房间", "roomID", id)
			}
		}
		roomsLock.Unlock()
	}
}

// clientInRoom 按连接 id 反查房间内真实传输连接，用于 SFU 回调与定向发送
// 成员表以抽象 Session 存储，实际值即 gateway.Client，断言失败返回 nil
func clientInRoom(roomID string, connID string) *gateway.Client {
	r := getRoomByID(roomID)
	if r == nil {
		return nil
	}
	r.Lock.RLock()
	sess, ok := r.Clients[connID]
	r.Lock.RUnlock()
	if !ok {
		return nil
	}
	if c, ok := sess.(*gateway.Client); ok {
		return c
	}
	return nil
}
