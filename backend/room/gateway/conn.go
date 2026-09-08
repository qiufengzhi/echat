package gateway

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"echat-backend/logging"
)

// logger gateway 包的具名日志器
var logger = logging.New("gateway")

var (
	// allConnectedClients 全局在线连接索引，连接建立时登记、收尾时移除
	allConnectedClients = make(map[string]*Client)
	// clientLock 保护 allConnectedClients 的并发读写
	clientLock sync.RWMutex
)

// kickedCloseCode 服务端主动踢下线使用的自定义关闭码（避开 WebSocket 保留码）
const kickedCloseCode = 4001

// HandleConnection 为一条新信令连接登记会话并启动读写循环，阻塞至连接退出
// conn 传输无关帧通道，identity 握手鉴权后的身份，h 业务回调（由编排层注入）
// 读循环退出后回调 h.OnClosed 让编排层收尾；连接级资源登记也在本函数内收敛
func HandleConnection(conn MessageFramer, identity ConnIdentity, h ClientHandler) {
	client := &Client{
		ConnID:       uuid.NewString(),
		UserID:       identity.UserID,
		SessionID:    identity.SessionID,
		TokenVersion: identity.TokenVersion,
		Conn:         conn,
		// 使用缓冲队列避免短暂慢客户端立刻阻塞整房广播
		Send: make(chan []byte, 256),
	}

	registerClient(client)
	logger.Infow("客户端已连接", "userID", client.UserID, "connID", client.ConnID)

	go writePump(client)
	readPump(client, h) // 当前协程负责读取并按消息顺序回调；阻塞

	logger.Infow("客户端已断开", "userID", client.UserID, "connID", client.ConnID)
	h.OnClosed(client)
}

// registerClient 把连接登记进全局在线索引
func registerClient(c *Client) {
	clientLock.Lock()
	allConnectedClients[c.ConnID] = c
	clientLock.Unlock()
}

// RemoveClient 从全局在线索引移除连接，供编排层收尾时调用
// connID 待移除连接 id；不存在时静默成功
func RemoveClient(connID string) {
	clientLock.Lock()
	delete(allConnectedClients, connID)
	clientLock.Unlock()
}

// AllClients 返回全局在线连接的快照副本，供吊销踢下线遍历
func AllClients() []*Client {
	clientLock.RLock()
	clients := make([]*Client, 0, len(allConnectedClients))
	for _, c := range allConnectedClients {
		clients = append(clients, c)
	}
	clientLock.RUnlock()
	return clients
}

// CloseTransport 关闭连接发送队列与底层传输，作为连接收尾的最后一步
// 关闭 Send 会让 writePump 退出并最终释放传输；重复调用安全
func (c *Client) CloseTransport() {
	close(c.Send)
	_ = c.Conn.Close()
}

// KickClose 以踢下线关闭码发送 WebSocket 关闭帧（仅原生 WS 支持控制帧）
// WebTransport 无控制帧概念，由流关闭自然断连；调用后编排层再执行完整收尾
func KickClose(c *Client) {
	if wsc, ok := c.Conn.(*websocket.Conn); ok {
		// WriteControl 允许与 writePump 并发，关闭帧可安全立即发送
		_ = wsc.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(kickedCloseCode, "session revoked"),
			time.Now().Add(time.Second))
	}
}

// readPump 持续读取客户端上行帧并回调编排层，同一连接的消息串行分发
// 读错误/关闭时记录原因（区分代理断连与客户端 leave）后返回，交由 HandleConnection 收尾
func readPump(client *Client, h ClientHandler) {
	defer func() {
		if r := recover(); r != nil {
			logger.Warnw("readPump 发生 panic", "userID", client.UserID, "panic", r)
		}
	}()

	for {
		_, message, err := client.Conn.ReadMessage()
		if err != nil {
			closeCode := 0
			closeText := ""
			var closeErr *websocket.CloseError
			if errors.As(err, &closeErr) {
				closeCode = closeErr.Code
				closeText = closeErr.Text
			}
			logger.Infow("WebSocket 读取结束",
				"userID", client.UserID,
				"connID", client.ConnID,
				"roomID", client.RoomID,
				"username", client.Username,
				"closeCode", closeCode,
				"closeText", closeText,
				"error", err,
			)
			return
		}
		h.OnFrame(client, message)
	}
}

// writePump 串行消费连接发送队列，把消息写回传输
// gorilla/websocket 不适合被多个协程同时写，因此一个连接只保留一个写协程
func writePump(client *Client) {
	defer client.Conn.Close()

	for message := range client.Send {
		if err := client.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
			return
		}
	}
	_ = client.Conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
}
