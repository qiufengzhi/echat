package room

import (
	"sync"
)

var (
	allSignalRooms      = make(map[string]*Room)   // 信令层所有在线房间，与 SFU 层房间区分
	allConnectedClients = make(map[string]*Client) // 所有已连接的客户端
	roomLock            sync.RWMutex               // 保护 allSignalRooms 的并发读写
	clientLock          sync.RWMutex               // 保护 allConnectedClients 的并发读写
)
