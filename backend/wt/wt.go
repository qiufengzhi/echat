// Package wt 提供 WebTransport/QUIC 信令端点，与 WebSocket 共用同一套房间域信令处理逻辑
//
// 传输框架：quic-go/webtransport-go 实现 HTTP/3 over QUIC（UDP）
// 架构：浏览器 WebTransport 建会话（CONNECT 握手携带 ?token=）→ 首条双向流承载信令帧
// 帧协议：每条 Message 前 4 字节大端长度前缀 + JSON 体，复用 room.MessageFramer 抽象
// 鉴权：握手阶段复用与 WebSocket 相同的 access token 校验，失败按 401 拒绝升级
// 可靠性：WebSocket 走 TCP 语义，WebTransport QUIC 双向流同样有序可靠，信令逻辑完全一致
package wt

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"echat-backend/authn"
	"echat-backend/config"
	"echat-backend/logging"
	"echat-backend/room"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

var logger = logging.New("wt")

// maxFrameSize 单帧最大长度上限：防止恶意客户端用超大长度头占满内存
const maxFrameSize = 1 << 20

// wtAuthTimeout 握手鉴权上限：WebSocket 端点同款策略，避免占连接不鉴权
const wtAuthTimeout = 5 * time.Second

// Server WebTransport 信令端点：封装 HTTP/3 服务、鉴权服务与会话处理
type Server struct {
	srv     *webtransport.Server // 底层 HTTP/3 WebTransport 服务
	authSvc *authn.Service       // 握手鉴权服务，与 WebSocket 端点共用
}

// NewServer 构建 WebTransport 端点，加载 TLS 证书并完成 HTTP/3 配置
// cfg 监听地址与证书路径，authSvc 握手鉴权服务（与 WS 共用），错误来自证书加载或配置
func NewServer(cfg config.WTConfig, authSvc *authn.Service) (*Server, error) {
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, err
	}
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}
	h3 := &http3.Server{
		Addr:       cfg.Addr,
		TLSConfig:  http3.ConfigureTLSConfig(tlsConf),
		QUICConfig: &quic.Config{EnableDatagrams: true}, // datagram 通道供后续可扩展的不可靠帧
	}
	webtransport.ConfigureHTTP3Server(h3)
	mux := http.NewServeMux()
	h3.Handler = mux
	s := &Server{authSvc: authSvc}
	srv := &webtransport.Server{
		H3: h3,
		// 开发期允许任意来源；生产环境通过 CheckOrigin 收紧为前端白名单
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	s.srv = srv
	// /.well-known/webtransport 为 WebTransport 事实标准路径，前端按此协商会话
	mux.HandleFunc("/.well-known/webtransport/signal", s.handleSignal)
	return s, nil
}

// handleSignal WebTransport CONNECT 握手处理：先鉴权再升级为会话
// token 从请求 URL query 读取（浏览器 WebTransport 无法自定义 Header，与 WS 同款策略）
// 鉴权失败按 401 写 JSON 错误，浏览器表现与 WS 握手失败一致
func (s *Server) handleSignal(w http.ResponseWriter, r *http.Request) {
	authCtx, cancel := context.WithTimeout(r.Context(), wtAuthTimeout)
	defer cancel()
	claims, err := s.authSvc.ValidateAccess(authCtx, r.URL.Query().Get("token"))
	if err != nil {
		writeUpgradeError(w, err)
		return
	}
	sess, err := s.srv.Upgrade(w, r)
	if err != nil {
		logger.Warnw("WebTransport 会话升级失败", "error", err)
		return
	}
	go s.serveSession(sess, room.ConnIdentity{
		UserID:       claims.SubjectUUID().String(),
		SessionID:    claims.SessionID.String(),
		TokenVersion: claims.TokenVersion,
	})
}

// serveSession 处理单个 WebTransport 会话：等待首条双向信令流并交给房间域处理
// 会话生命周期绑定信令流：会话结束流读取自然出错，readPump 退出并触发连接清理
func (s *Server) serveSession(sess *webtransport.Session, identity room.ConnIdentity) {
	defer sess.CloseWithError(0, "signal session done")
	stream, err := sess.AcceptStream(sess.Context())
	if err != nil {
		logger.Warnw("接收信令流失败", "userID", identity.UserID, "error", err)
		return
	}
	logger.Infow("WebTransport 信令流已建立", "userID", identity.UserID)
	room.HandleConnection(&streamFramer{stream: stream}, identity)
}

// ListenAndServe 启动 WebTransport UDP 监听，阻塞直到错误
func (s *Server) ListenAndServe() error {
	return s.srv.ListenAndServe()
}

// streamFramer 把 QUIC 双向流适配成 MessageFramer：4 字节大端长度前缀 + JSON 体
// 语义对齐 gorilla/websocket.Conn：ReadMessage 返回完整一帧，WriteMessage 忽略消息类型参数
type streamFramer struct {
	stream *webtransport.Stream // 会话内的双向信令流
}

// ReadMessage 读出一帧完整消息：先读 4 字节长度头，再按长度读 JSON 体
// 返回消息类型固定为 1（文本），语义与 WebSocket 文本帧一致；流结束返回 EOF 驱动 readPump 退出
func (f *streamFramer) ReadMessage() (int, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(f.stream, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrameSize {
		return 0, nil, errors.New("frame too large")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(f.stream, buf); err != nil {
		return 0, nil, err
	}
	return 1, buf, nil
}

// WriteMessage 写出一帧消息：4 字节长度头 + 消息体
// messageType 在流式传输中无意义，忽略；返回写入错误
func (f *streamFramer) WriteMessage(messageType int, data []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(data)))
	if _, err := f.stream.Write(hdr[:]); err != nil {
		return err
	}
	_, err := f.stream.Write(data)
	return err
}

// Close 关闭双向信令流，释放 QUIC 流资源
func (f *streamFramer) Close() error {
	return f.stream.Close()
}

// writeUpgradeError 在升级前把鉴权失败写成 JSON，浏览器 WebTransport 表现为握手失败
// 与 handlers 包中的 WebSocket 同款响应格式，前端降级逻辑可统一处理
func writeUpgradeError(w http.ResponseWriter, err error) {
	status := http.StatusUnauthorized
	code := "TOKEN_INVALID"
	message := "登录已失效，请重新登录"
	var ae *authn.Error
	if errors.As(err, &ae) {
		status = ae.Status
		code = ae.Code
		message = ae.Message
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}