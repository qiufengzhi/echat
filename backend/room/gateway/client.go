package gateway

import (
	"sync"
	"time"
)

// ConnIdentity 握手鉴权后的连接身份，取代早期「每连接随机 UUID 当身份」
// 身份（UserID）来自 access token 的 sub，服务端权威，客户端无法伪造
type ConnIdentity struct {
	// UserID 鉴权后的用户 id（权威身份，来自 access 的 sub）
	UserID string
	// SessionID 签发 access 的会话 id，吊销时按此精确踢连接
	SessionID string
	// TokenVersion 签发时的 token_version，预留吊销级联比对
	TokenVersion int
}

// MessageFramer 统一信令传输的消息帧读写接口，签名与 gorilla/websocket.Conn 对齐
// ReadMessage 返回消息类型与完整负载；WriteMessage 按消息类型写出负载；Close 释放传输
// WebTransport 实现见 wt 包：双向流适配器把长度前缀还原为帧，消息类型参数忽略
type MessageFramer interface {
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	Close() error
}

// ClientHandler 连接生命周期回调：由编排层（signaling）实现并注入
// gateway 在读到完整一帧或连接退出时回调，业务分发与房间收尾都在编排层完成
type ClientHandler interface {
	// OnFrame 读到一帧完整上行消息（原始 JSON 字节）
	OnFrame(c *Client, raw []byte)
	// OnClosed 连接读循环退出，编排层据此收尾（离开房间/广播/清理连接资源）
	OnClosed(c *Client)
}

// Client 表示一个已连接的传输会话，以及它在房间中的成员信息
// 同时实现 room/aggregate.Session，供领域层以抽象视图消费（见 Session* 方法）
type Client struct {
	// ConnID 连接级唯一 id（服务端生成），作为 SFU peer 与通道键
	ConnID string
	// UserID 鉴权后的用户 id（权威身份，所有上行身份以绑定值为准）
	UserID string
	// SessionID 签发 access 的会话 id，吊销联动时精确踢连接
	SessionID string
	// TokenVersion 签发时的 token_version，预留吊销级联比对
	TokenVersion int
	// RoomID 客户端当前所在房间 ID，尚未加入房间时为空
	RoomID string
	// Username 用户进入房间时填写的展示昵称
	Username string
	// JoinedAt 加入房间时间，用于生成稳定的成员列表排序
	JoinedAt time.Time
	// Conn 信令传输帧通道（WebSocket 连接或 WebTransport 双向流适配器）
	Conn MessageFramer
	// Send 单客户端发送队列，由 writePump 串行写入传输
	Send chan []byte
	// closeOnce 确保离开/断连清理只执行一次，避免重复关闭通道或连接
	closeOnce sync.Once
}

// SessionConnID 实现 room/aggregate.Session：返回连接级唯一 id
func (c *Client) SessionConnID() string { return c.ConnID }

// SessionUserID 实现 room/aggregate.Session：返回鉴权后的权威用户 id
func (c *Client) SessionUserID() string { return c.UserID }

// SessionUsername 实现 room/aggregate.Session：返回房间展示昵称
func (c *Client) SessionUsername() string { return c.Username }

// SessionJoinedAt 实现 room/aggregate.Session：返回加入房间时间
func (c *Client) SessionJoinedAt() time.Time { return c.JoinedAt }

// WithCloseOnce 让编排层的连接收尾只执行一次（离开/断连竞态下防重复清理）
// fn 待执行的一次性收尾函数，由 sync.Once 保证并发安全
func (c *Client) WithCloseOnce(fn func()) {
	c.closeOnce.Do(fn)
}

// Enqueue 把一帧已编码消息放入发送队列，队列满时丢弃（慢客户端保护）
// 返回是否真正入队，调用方可据此告警
func (c *Client) Enqueue(data []byte) bool {
	select {
	case c.Send <- data:
		return true
	default:
		return false
	}
}
