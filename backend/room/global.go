package room

import (
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	allSignalRooms      = make(map[string]*Room)   // 信令层所有在线房间，与 SFU 层房间区分
	allConnectedClients = make(map[string]*Client) // 所有已连接的客户端
	roomLock            sync.RWMutex               // 保护 allSignalRooms 的并发读写
	clientLock          sync.RWMutex               // 保护 allConnectedClients 的并发读写

	pool *pgxpool.Pool // 注入的 pgx 连接池，房间事实与事件按「当前态写库」同一事务落表；nil 表示未注入，此时跳过落库
)

// SetStore 注入 pgx 连接池，房间事实写库与事件记账都经由它
// st 由 main 在启动时传入 store.Store.Pool() 的产物，与事务性 outbox relay 共用同一连接池
func SetStore(st *pgxpool.Pool) {
	pool = st
}
