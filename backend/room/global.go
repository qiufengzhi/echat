package room

import (
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var (
	allSignalRooms      = make(map[string]*Room)   // 信令层所有在线房间，与 SFU 层房间区分
	allConnectedClients = make(map[string]*Client) // 所有已连接的客户端
	roomLock            sync.RWMutex               // 保护 allSignalRooms 的并发读写
	clientLock          sync.RWMutex               // 保护 allConnectedClients 的并发读写

	// Upgrader 将 HTTP 连接升级为 WebSocket 连接，用于浏览器和后端之间的实时通信
	// ReadBufferSize/WriteBufferSize 设置每次消息的缓冲区大小
	Upgrader = websocket.Upgrader{
		// CheckOrigin 决定是否允许当前来源发起 WebSocket 升级请求
		// 返回 true 表示允许；本地开发阶段允许任意来源，线上环境应校验请求来源
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
)
