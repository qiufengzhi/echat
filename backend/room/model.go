package room

import (
	"encoding/json"
	"sync"

	"github.com/gorilla/websocket"
)

// Client 表示一个已连接的浏览器客户端，以及它的发送队列。
type Client struct {
	ID        string          // 客户端唯一标识，由服务端在连接建立时生成（UUID）。
	RoomID    string          // 客户端当前所在的房间 ID，未加入房间时为空串。
	Username  string          // 客户端显示名称，由用户加入房间时自定义或自动生成。
	Conn      *websocket.Conn // 与客户端的 WebSocket 连接实例。
	Send      chan []byte     // 发送缓冲通道，写协程从该通道读取消息并写入 WebSocket。
	closeOnce sync.Once       // 确保断连清理逻辑只执行一次，防止重复关闭。
}

// Room 表示一个信令房间，里面维护当前在线的客户端。
type Room struct {
	ID      string             // 房间唯一标识，由客户端在加入时指定。
	Clients map[string]*Client // 房间内当前在线的所有客户端，以用户 ID 为键。  client.ID
	Lock    sync.RWMutex       // 读写锁，保护 Clients 的并发读写安全。
}

// Message 是前后端之间约定的信令消息结构。
type Message struct {
	Type    string          `json:"type"`              // 消息类型，如 join / offer / answer / ice / leave / ping / pong / error 等。
	RoomID  string          `json:"room_id"`           // 目标房间 ID，用于标识消息所属房间。
	UserID  string          `json:"user_id"`           // 发送者用户 ID，用于接收方区分消息来源。
	Payload json.RawMessage `json:"payload,omitempty"` // 消息负载，JSON 原始字节，具体结构由 Type 决定。
}
