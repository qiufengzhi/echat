package room

import (
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var (
	allActiveRooms      = make(map[string]*Room)   // 当前服务端所有在线的房间
	allConnectedClients = make(map[string]*Client) // 所有已连接的客户端
	roomLock            sync.RWMutex
	clientLock          sync.RWMutex

	// 本地开发阶段允许任意来源建立 WebSocket 连接，线上环境应收紧来源校验
	Upgrader = websocket.Upgrader{
		// CheckOrigin 决定是否允许当前来源发起 WebSocket 升级
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
)
