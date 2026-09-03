// transport.go 信令传输帧抽象：WebSocket 与 WebTransport 共用同一套 Client 处理逻辑
//
// 目标：handleMessage 等房间域逻辑对传输无感知，只依赖统一的「完整一帧」读写语义
// WebSocket 自带消息帧；QUIC 双向流是字节流，由流帧适配器补 4 字节长度前缀还原帧边界
package room

// MessageFramer 统一信令传输的消息帧读写接口，签名与 gorilla/websocket.Conn 对齐
// ReadMessage 返回消息类型与完整负载；WriteMessage 按消息类型写出负载；Close 释放传输
// WebTransport 实现见 wt 包：双向流适配器把长度前缀还原为帧，消息类型参数忽略
type MessageFramer interface {
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	Close() error
}