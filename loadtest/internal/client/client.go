// Package client 封装 loadgen 与 echat backend 的交互协议：
// HTTP 注册/登录（对齐 backend/authn）与 WS 信令（对齐 backend/room/signaling/wire.go）。
// 协议事实来源：登录响应见 backend/authn/login.go SessionResult，信令信封见 wire.go Message。
package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// SessionResult 登录响应体（字段名对齐 SessionResult 的 json tag）
type SessionResult struct {
	AccessToken  string `json:"accessToken"`
	TokenType    string `json:"tokenType"`
	ExpiresIn    int64  `json:"expiresIn"`
	RefreshToken string `json:"refreshToken"`
}

// Message WS 信令信封（字段对齐 signaling.Message）
type Message struct {
	Type    string          `json:"type"`
	RoomID  string          `json:"roomId"`
	UserID  string          `json:"userId"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Register 本地账号注册；账号已存在时返回错误（调用方决定是否视为幂等成功）
func Register(base, username, password string) error {
	body := fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
	resp, err := http.Post(base+"/api/v1/auth/register", "application/json", strings.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("register %s: http %d", username, resp.StatusCode)
	}
	return nil
}

// Login 登录换取 access token（JWT，TTL 15m，足够单场景使用）
func Login(base, identifier, password string) (SessionResult, error) {
	var out SessionResult
	body := fmt.Sprintf(`{"identifier":%q,"password":%q}`, identifier, password)
	resp, err := http.Post(base+"/api/v1/auth/login", "application/json", strings.NewReader(body))
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("login %s: http %d", identifier, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, err
	}
	if out.AccessToken == "" {
		return out, fmt.Errorf("login %s: 响应缺少 accessToken", identifier)
	}
	return out, nil
}

// WSConn WS 信令连接的薄封装
type WSConn struct {
	conn *websocket.Conn
}

// DialWS 建立 WS 连接（/ws?token=<JWT>），返回连接与握手耗时毫秒
func DialWS(base, token string) (*WSConn, float64, error) {
	wsURL := strings.Replace(base, "http", "ws", 1) + "/ws?token=" + token
	start := time.Now()
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		// 带上 HTTP 状态与响应体片段，便于区分 401（token 无效）与 403（Origin 校验）等
		if resp != nil {
			body := make([]byte, 256)
			n, _ := resp.Body.Read(body)
			return nil, 0, fmt.Errorf("websocket: bad handshake: http %d: %s", resp.StatusCode, string(body[:n]))
		}
		return nil, 0, err
	}
	return &WSConn{conn: conn}, float64(time.Since(start).Milliseconds()), nil
}

// Send 发送一条信令消息（每连接内串行调用，gorilla 不支持并发写）
func (c *WSConn) Send(msg Message) error { return c.conn.WriteJSON(msg) }

// Join 加入房间，payload 为昵称字符串（对齐 MsgTypeJoin 约定）
func (c *WSConn) Join(roomID, nickname string) error {
	return c.Send(Message{Type: "join", RoomID: roomID, Payload: json.RawMessage(`"` + nickname + `"`)})
}

// Ping 发送心跳（服务端回 pong）
func (c *WSConn) Ping(roomID string) error {
	return c.Send(Message{Type: "ping", RoomID: roomID})
}

// Read 读一条下行消息
func (c *WSConn) Read() (Message, error) {
	var msg Message
	if err := c.conn.ReadJSON(&msg); err != nil {
		return Message{}, err
	}
	return msg, nil
}

// SetReadDeadline 设置读超时
func (c *WSConn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// Close 关闭底层连接
func (c *WSConn) Close() error { return c.conn.Close() }
