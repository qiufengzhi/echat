// Package sfu 实现基于 Pion WebRTC 的 SFU（Selective Forwarding Unit）音频转发引擎
//
// 架构说明（客户端发起 Offer）：
//
//	┌──────────┐      WebSocket 信令      ┌──────────────────┐
//	│ 浏览器 A  │ ────── sfu_offer ───────►│                  │
//	│          │ ◄────── sfu_answer ──────►│    SFU 引擎      │
//	│          │ ◄────── sfu_ice   ──────► │                  │
//	│ 发布音轨 ─┼─── WebRTC ──────────────►│                  │
//	└──────────┘                           │  每个客户端一个   │
//	                                       │  PeerConnection  │
//	┌──────────┐                           │                  │
//	│ 浏览器 B  │ ────── sfu_offer ───────►│  收到 A 的音轨   │
//	│          │ ◄────── sfu_answer ──────►│  → 转发给 B      │
//	│          │ ◄────── sfu_ice   ──────► │  收到 B 的音轨   │
//	│ 发布音轨 ─┼─── WebRTC ──────────────►│  → 转发给 A      │
//	└──────────┘                           └──────────────────┘
//
// 和旧版 P2P 中继的核心区别：
//   - 每个客户端只与 SFU 服务器建立一条 PeerConnection，而非与其他每个客户端建立 N-1 条
//   - 服务器收到某客户端的音频后，创建本地中继音轨并将其添加到房间内其他所有客户端的
//     PeerConnection 上
//   - 客户端只需关注与服务器的单条连接，远端音频以多路 MediaStream 形式通过 ontrack 到达
package sfu

import (
	"bytes"
	"echat-backend/asr_cli"
	"echat-backend/config"
	"echat-backend/dataplane"
	"echat-backend/logging"
	asrpb "echat-backend/proto/asr"
	"echat-backend/tts_cli"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gunter-q12/resample"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

var logger = logging.New("sfu")

// asrQueueCapacity 每个发布者的待识别 RTP 队列容量，50 包 × 20ms = 1 秒音频
//
// 队列只吸收解码与投递的瞬时抖动，不承担积压：满了丢最老，语音以最新内容为准
const asrQueueCapacity = 50

// ICECandidateCallback 是 SFU -> 信令层 的 ICE Candidate 回调
// 信令层收到后通过 WebSocket 转发给对应客户端
type ICECandidateCallback func(clientID string, candidate webrtc.ICECandidateInit)

// RenegotiationCallback 是 SFU -> 信令层 的重新协商回调
// 当 SFU 向订阅者添加中继音轨后，需要触发重新协商，由信令层通过 WebSocket 发送新 Offer 给客户端
// 客户端回复 Answer 后，信令层通过 AcceptRenegotiationAnswer 交回 SFU 处理
type RenegotiationCallback func(clientID string, offerSDP string)

// SFUServer 管理一组 SFU 房间，每个房间包含多个 SFU Peer 连接
// 调用方（room 包）通过 GetOrCreateRoom / RemoveRoom 来管理 SFU 房间生命周期
type SFUServer struct {
	rooms map[string]*SFURoom // roomID -> SFU 房间
	lock  sync.RWMutex
}

// SFURoom 对应一个语音房的 SFU 转发域，维护房间内所有客户端的 WebRTC PeerConnection
type SFURoom struct {
	ID    string
	peers map[string]*SFUPeer // clientID -> SFU 对端
	lock  sync.RWMutex

	// ICE Candidate 回调，由信令层注册，用于把 SFU 收集到的 candidate 转发给客户端
	onICECandidate ICECandidateCallback
	// 重新协商回调，当 AddTrack 后触发，由信令层向客户端发送 renegotiation Offer
	onRenegotiation RenegotiationCallback
}

// SFUPeer 包装单个客户端的 WebRTC PeerConnection，负责收发音频和中继管理
type SFUPeer struct {
	ClientID string                 // 对应 room.Client.ID
	PC       *webrtc.PeerConnection // 到客户端的 WebRTC 连接

	// SessionID 本对端的会话标识，由 SFU 在建立对端时生成一次
	// 不透明、不接受反向解析：房间 ID 与客户端 ID 在 proto 里本来就是独立字段，
	// 标识只做身份；拼接式标识会把编码格式变成跨包的隐式契约
	SessionID string

	// 该客户端发布的音频远端音轨（由 SFU 的 OnTrack 收到）
	publishedAudioTrack *webrtc.TrackRemote

	// 该客户端音频被转发给其他客户端时的本地中继音轨
	// 键是订阅者（接收方）的 clientID，值是对应中继音轨，该房间内其他所有用户
	outgoingRelays map[string]*webrtc.TrackLocalStaticRTP

	// AI TTS 合成语音输出轨，写入后客户端通过 ontrack 收到
	// 由 getAITtsTrack 首次使用时懒创建并 AddTrack 到 PC
	// 使用 TrackLocalStaticSample（pion 自动管理 seq/ts/marker），而非手动构造 RTP 包
	aiTtsTrack *webrtc.TrackLocalStaticSample

	// 停止中继转发协程的信号，在客户端离开或断连时关闭
	stopRelay chan struct{}

	connectedAt time.Time
}

// newSessionID 生成一个新的会话标识
//
// 不透明且服务端权威：调用方只当作字符串透传，不解析、不参与构造
// return: 形如 s_<32 位十六进制> 的标识
func newSessionID() string {
	return "s_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

// resolveNAT1To1IPs 解析配置中的 NAT 1:1 映射地址（支持 IP 或域名，逗号分隔）
// pion/webrtc 的 SetNAT1To1IPs 只接受 IP，域名会解析为第一个 IPv4 地址
func resolveNAT1To1IPs(raw string) []string {
	if raw == "" {
		return nil
	}
	var result []string
	for _, addr := range strings.Split(raw, ",") {
		addr = strings.TrimSpace(addr)
		if parsedIP := net.ParseIP(addr); parsedIP != nil {
			result = append(result, parsedIP.String())
			continue
		}
		// 域名 → 解析为 IP
		ips, err := net.LookupIP(addr)
		if err != nil {
			logger.Warnw("域名解析失败", "addr", addr, "error", err)
			continue
		}
		for _, resolvedIP := range ips {
			if resolvedIP.To4() != nil {
				result = append(result, resolvedIP.String())
				break
			}
		}
		if len(result) == 0 && len(ips) > 0 {
			result = append(result, ips[0].String())
		}
	}
	return result
}

// NewSFUServer 创建 SFU 引擎实例
func NewSFUServer() *SFUServer {
	return &SFUServer{
		rooms: make(map[string]*SFURoom),
	}
}

// GetOrCreateRoom 查找已有 SFU 房间，不存在时创建新房间
func (s *SFUServer) GetOrCreateRoom(roomID string) *SFURoom {
	s.lock.Lock()
	defer s.lock.Unlock()

	if room, ok := s.rooms[roomID]; ok {
		return room
	}

	room := &SFURoom{
		ID:    roomID,
		peers: make(map[string]*SFUPeer),
	}
	s.rooms[roomID] = room
	// 识别结果按房间分发：订阅随 SFU 房间创建建立，随房间移除关闭
	GlobalASRDispatcher.subscribe(roomID)
	logger.Infow("房间已创建", "roomID", roomID)
	return room
}

// RemoveRoom 删除 SFU 房间。调用方应在确认房间内无活跃 peer 后调用
func (s *SFUServer) RemoveRoom(roomID string) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if _, ok := s.rooms[roomID]; ok {
		delete(s.rooms, roomID)
		// 一并移除识别结果订阅，否则订阅者与其队列会随房间数累积
		GlobalASRDispatcher.unsubscribe(roomID)
		logger.Infow("房间已删除", "roomID", roomID)
	}
}

// GetRoom 获取指定 SFU 房间，nil 表示不存在
func (s *SFUServer) GetRoom(roomID string) *SFURoom {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return s.rooms[roomID]
}

// Join 为客户端创建到 SFU 的 WebRTC PeerConnection，但不生成 Offer
// Offer 由客户端发起，服务端通过 AcceptOffer 创建 Answer 完成协商
func (r *SFURoom) Join(clientID string) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	// 防止同一 clientID 重复加入
	if _, exists := r.peers[clientID]; exists {
		return fmt.Errorf("client %s already in SFU room", clientID)
	}

	sfuCfg := config.Get().SFU

	settingEngine := webrtc.SettingEngine{}

	// SetEphemeralUDPPortRange: 限制 WebRTC 媒体流使用的 UDP 端口范围
	// Docker 部署时必须设置，确保端口与 Docker 端口映射一致，否则客户端无法连接
	if err := settingEngine.SetEphemeralUDPPortRange(sfuCfg.MediaMinPort, sfuCfg.MediaMaxPort); err != nil {
		return fmt.Errorf("set port range: %w", err)
	}

	// SetNAT1To1IPs: 配置 NAT 1:1 映射的公网 IP
	// 当 SFU 在 Docker 容器内运行时，默认生成的 ICE candidate 使用容器内部 IP（如 172.17.0.x），
	// 客户端无法访问。设置此选项后，SFU 会将 host 类型的 candidate 地址替换为公网 IP，
	// 让客户端能正确建立 WebRTC 连接
	nat1To1IPs := resolveNAT1To1IPs(sfuCfg.NAT1To1IP)
	if len(nat1To1IPs) > 0 {
		settingEngine.SetNAT1To1IPs(nat1To1IPs, webrtc.ICECandidateTypeHost)
	}

	// 自建拦截器链代替默认链：默认链含 stats interceptor，多 PC 并发读 + 重协商时偶发 SIGSEGV
	// 本项目不消费服务端 GetStats，故按 pion 官方建议复刻默认链时略去 stats，保留 nack/report/twcc
	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		return fmt.Errorf("register default codecs: %w", err)
	}
	registry := &interceptor.Registry{}
	if err := webrtc.ConfigureNack(mediaEngine, registry); err != nil {
		return fmt.Errorf("configure nack: %w", err)
	}
	if err := webrtc.ConfigureRTCPReports(registry); err != nil {
		return fmt.Errorf("configure rtcp reports: %w", err)
	}
	if err := webrtc.ConfigureTWCCSender(mediaEngine, registry); err != nil {
		return fmt.Errorf("configure twcc sender: %w", err)
	}

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(mediaEngine),
		webrtc.WithInterceptorRegistry(registry),
		webrtc.WithSettingEngine(settingEngine),
	)

	cg := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: sfuCfg.STUNServers},
		},
	}

	pc, err := api.NewPeerConnection(cg)
	if err != nil {
		return fmt.Errorf("create peer connection: %w", err)
	}

	peer := &SFUPeer{
		ClientID:       clientID,
		SessionID:      newSessionID(),
		PC:             pc,
		outgoingRelays: make(map[string]*webrtc.TrackLocalStaticRTP),
		stopRelay:      make(chan struct{}),
		connectedAt:    time.Now(),
	}

	// ICE Candidate 收集 -> 通知信令层转发给客户端
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			logger.Debugw("ICE 收集完成", "clientID", clientID[:8])
			return
		}
		if r.onICECandidate == nil {
			logger.Warnw("ICE callback 未设置，candidate 已丢弃", "clientID", clientID[:8])
			return
		}
		candJSON := candidate.ToJSON()
		logger.Debugw("收集到 ICE candidate", "clientID", clientID[:8], "candidate", candJSON.Candidate)
		r.onICECandidate(clientID, candJSON)
	})

	// 当 AddTrack 被调用后，pion 会触发 OnNegotiationNeeded，此时需要向客户端发送 renegotiation Offer
	pc.OnNegotiationNeeded(func() {
		if r.onRenegotiation == nil {
			logger.Warnw("renegotiation 触发但无回调", "clientID", clientID[:8])
			return
		}
		offer, err := pc.CreateOffer(nil)
		if err != nil {
			logger.Warnw("创建 renegotiation Offer 失败", "clientID", clientID[:8], "error", err)
			return
		}
		if err = pc.SetLocalDescription(offer); err != nil {
			logger.Warnw("设置 renegotiation Offer 失败", "clientID", clientID[:8], "error", err)
			return
		}
		logger.Debugw("renegotiation Offer 已创建", "clientID", clientID[:8])
		r.onRenegotiation(clientID, offer.SDP)
	})

	// ICE 连接状态日志
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		logger.Infow("ICE 状态变化", "clientID", clientID[:8], "state", state.String())
	})

	// PeerConnection 整体状态变化
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		logger.Infow("PC 状态变化", "clientID", clientID[:8], "state", state.String())
		if state == webrtc.PeerConnectionStateDisconnected ||
			state == webrtc.PeerConnectionStateFailed {
			// 连接意外断开，清理转发资源
			peer.stopForwarding()
		}
	})

	// 当客户端开始发送音频时，SFU 接收音轨并转发给其他客户端
	pc.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if remoteTrack.Kind() != webrtc.RTPCodecTypeAudio {
			logger.Debugw("跳过非音频轨", "clientID", clientID[:8])
			return
		}

		logger.Infow("收到音频轨", "clientID", clientID[:8], "codec", remoteTrack.Codec().MimeType)

		peer.publishedAudioTrack = remoteTrack
		// 在锁外启动转发，避免持有锁时调用 AddTrack（AddTrack 也可能触发 ICE 回调）
		go r.startForwarding(clientID, remoteTrack)
	})

	r.peers[clientID] = peer
	logger.Infow("对端加入", "clientID", clientID[:8], "roomID", r.ID)
	return nil
}

// AcceptOffer 处理客户端发来的 SDP Offer，创建并返回 Answer SDP
//
// 调用时机：客户端创建 Offer 后通过 sfu_offer 信令发送给服务端，
// 服务端调用此方法设置远端描述、创建 Answer，再将 Answer 通过 sfu_answer 发回客户端
func (r *SFURoom) AcceptOffer(clientID string, offerSDP string) (answerSDP string, err error) {
	r.lock.RLock()
	peer, ok := r.peers[clientID]
	r.lock.RUnlock()

	if !ok {
		return "", fmt.Errorf("peer %s not found", clientID)
	}

	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offerSDP,
	}
	if err = peer.PC.SetRemoteDescription(offer); err != nil {
		return "", fmt.Errorf("set remote description: %w", err)
	}

	answer, err := peer.PC.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("create answer: %w", err)
	}
	if err = peer.PC.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("set local description: %w", err)
	}

	logger.Infow("接受 Offer 已回复 Answer", "clientID", clientID[:8])
	logger.Debugw("Answer SDP 内容", "clientID", clientID[:8], "sdp", answer.SDP)
	return answer.SDP, nil
}

// AcceptICECandidate 处理客户端发来的 ICE Candidate
func (r *SFURoom) AcceptICECandidate(clientID string, candidate webrtc.ICECandidateInit) error {
	r.lock.RLock()
	peer, ok := r.peers[clientID]
	r.lock.RUnlock()

	if !ok {
		return fmt.Errorf("peer %s not found", clientID)
	}
	if err := peer.PC.AddICECandidate(candidate); err != nil {
		return fmt.Errorf("add ICE candidate: %w", err)
	}
	return nil
}

// Leave 处理客户端离开，关闭 PeerConnection 并清理转发资源
func (r *SFURoom) Leave(clientID string) {
	r.lock.Lock()
	peer, ok := r.peers[clientID]
	if !ok {
		r.lock.Unlock()
		return
	}
	delete(r.peers, clientID)
	r.lock.Unlock()

	logger.Infow("对端离开", "clientID", clientID[:8])

	// 停止转发协程
	peer.stopForwarding()

	// 发送 ASR 结束标记，通知阿里云该客户端音频流已结束
	// 尽力投递：队列满时丢弃，云端会话由空闲超时兜底释放
	sessionID := peer.SessionID
	asr_cli.Push(&asrpb.AudioChunk{
		SessionId:  sessionID,
		RoomId:     r.ID,
		ClientId:   clientID,
		SampleRate: 16000,
		IsLast:     true,
	})

	// 会话结束：释放该会话的 TTS 端点与合成执行器，不调用会泄漏合成协程
	tts_cli.GlobalTTSService.Close(sessionID)

	// 从其他所有客户端的中继表中移除当前客户端的音轨
	r.lock.Lock()
	for _, otherPeer := range r.peers {
		delete(otherPeer.outgoingRelays, clientID)
	}
	empty := len(r.peers) == 0
	r.lock.Unlock()

	// 关闭 PeerConnection
	if err := peer.PC.Close(); err != nil {
		logger.Warnw("关闭 PC 失败", "clientID", clientID[:8], "error", err)
	}

	if empty {
		logger.Infow("房间已空", "roomID", r.ID)
	}
}

// SetOnICECandidate 注册 ICE Candidate 回调，供信令层转发 candidate 给客户端
func (r *SFURoom) SetOnICECandidate(cb ICECandidateCallback) {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.onICECandidate = cb
}

// SetOnRenegotiation 注册重新协商回调，供信令层在 AddTrack 后发送 renegotiation Offer 给客户端
func (r *SFURoom) SetOnRenegotiation(cb RenegotiationCallback) {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.onRenegotiation = cb
}

// AcceptRenegotiationAnswer 处理客户端对 renegotiation Offer 的 Answer
func (r *SFURoom) AcceptRenegotiationAnswer(clientID string, answerSDP string) error {
	r.lock.RLock()
	peer, ok := r.peers[clientID]
	r.lock.RUnlock()
	if !ok {
		return fmt.Errorf("peer %s not found", clientID)
	}

	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  answerSDP,
	}
	if err := peer.PC.SetRemoteDescription(answer); err != nil {
		return fmt.Errorf("set remote description: %w", err)
	}

	logger.Infow("renegotiation answer 已处理", "clientID", clientID[:8])
	return nil
}

// PeerCount 返回房间内 SFU 对端数量
func (r *SFURoom) PeerCount() int {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return len(r.peers)
}

// HasPeer 检查指定客户端是否在 SFU 房间内
func (r *SFURoom) HasPeer(clientID string) bool {
	r.lock.RLock()
	defer r.lock.RUnlock()
	_, ok := r.peers[clientID]
	return ok
}

// startForwarding 在收到客户端音频轨后，创建中继音轨并开始转发
//
// 转发逻辑：
//  1. source 的音频到达 SFU，通过 OnTrack 收到 remoteTrack
//  2. 为房间内每个其他客户端创建一个 TrackLocalStaticRTP（中继音轨），
//     添加到该客户端的 PeerConnection 中
//  3. 同时也为 source 创建其他已有客户端的中继音轨，让 source 能听到其他人
//  4. 启动协程持续读取 source 的 RTP 包并写入所有中继音轨
func (r *SFURoom) startForwarding(sourceID string, remoteTrack *webrtc.TrackRemote) {
	r.lock.Lock()

	// 获取发布音频的客户端，用于后续设置 outgoingRelays
	sourcePeer := r.peers[sourceID]
	if sourcePeer == nil {
		r.lock.Unlock()
		logger.Warnw("启动转发时对端已离开", "source", sourceID[:8])
		return
	}
	sessionID := sourcePeer.SessionID

	// 为房间内每个其他客户端创建中继音轨，让其他人能听到 source 的音频
	for subscriberID, subscriber := range r.peers {
		if subscriberID == sourceID {
			continue
		}

		relayTrack, err := webrtc.NewTrackLocalStaticRTP(
			remoteTrack.Codec().RTPCodecCapability,
			"audio",
			"audio_"+sourceID, // label 格式为 "audio_<userId>"，前端据此识别声音来源
		)
		if err != nil {
			logger.Warnw("创建中继轨失败", "source", sourceID[:8], "subscriber", subscriberID[:8], "error", err)
			continue
		}

		if _, err = subscriber.PC.AddTrack(relayTrack); err != nil {
			logger.Warnw("添加中继轨失败", "source", sourceID[:8], "subscriber", subscriberID[:8], "error", err)
			continue
		}

		// 将房间内其他人的音轨通过中继轨加入 source
		sourcePeer.outgoingRelays[subscriberID] = relayTrack
		logger.Infow("中继已添加", "from", sourceID[:8], "to", subscriberID[:8])
	}

	// 为当前 source 创建其他已有客户端的中继音轨，让 source 能听到其他人
	for otherID, otherPeer := range r.peers {
		if otherID == sourceID || otherPeer.publishedAudioTrack == nil {
			continue
		}

		relayTrack, err := webrtc.NewTrackLocalStaticRTP(
			otherPeer.publishedAudioTrack.Codec().RTPCodecCapability,
			"audio",
			"audio_"+otherID, // label 格式为 "audio_<userId>"，前端据此识别声音来源
		)
		if err != nil {
			logger.Warnw("为新对端创建中继轨失败", "source", sourceID[:8], "other", otherID[:8], "error", err)
			continue
		}

		if _, err = sourcePeer.PC.AddTrack(relayTrack); err != nil {
			logger.Warnw("为新对端添加中继轨失败", "source", sourceID[:8], "other", otherID[:8], "error", err)
			continue
		}

		// 将 other 的音频转发给 source
		otherPeer.outgoingRelays[sourceID] = relayTrack
		logger.Infow("中继已添加(新对端)", "from", otherID[:8], "to", sourceID[:8])
	}

	r.lock.Unlock()

	// 启动 RTP 转发协程：从 remoteTrack 读取 RTP 包，写入所有中继音轨。将本人的音频转发给房间内其他客户端
	go r.forwardRtp(sourceID, sessionID, remoteTrack)
}

// getAITtsTrack 懒初始化 AI TTS 输出轨，返回该客户端的 AI 语音播放通道
// 首次调用时创建 TrackLocalStaticRTP 并 AddTrack 到 PeerConnection，触发重协商
// 后续调用直接复用已创建的 track
func (r *SFURoom) getAITtsTrack(clientID string) (*webrtc.TrackLocalStaticSample, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	peer, ok := r.peers[clientID]
	if !ok {
		return nil, fmt.Errorf("getAITtsTrack: 客户端 %s 不在房间", clientID[:8])
	}
	if peer.aiTtsTrack != nil {
		return peer.aiTtsTrack, nil
	}

	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"audio",
		"audio_ai_"+clientID, // label 标示 AI 合成语音来源
	)
	if err != nil {
		return nil, fmt.Errorf("getAITtsTrack: 创建 AI TTS 音轨失败: %w", err)
	}
	if _, err = peer.PC.AddTrack(track); err != nil {
		return nil, fmt.Errorf("getAITtsTrack: 添加 AI TTS 音轨到 PC 失败: %w", err)
	}

	peer.aiTtsTrack = track
	logger.Infow("AI TTS 音轨已创建", "clientID", clientID[:8])

	// AddTrack 已自动触发 OnNegotiationNeeded 回调（见 CreateSFUPeerConn 中的注册）
	// 该回调内完成 CreateOffer + SetLocalDescription + 信令通知，此处无需重复处理

	return track, nil
}

// forwardRtp 接收到客户端的语音包，从 remoteTrack 读取 RTP 包，转发给房间其他客户端，同时送阿里云 ASR 做语音识别
// sessionID 由调用方从对端读取，保证同一会话在 ASR、LLM、TTS 三段用同一个标识
func (r *SFURoom) forwardRtp(sourceID string, sessionID string, remoteTrack *webrtc.TrackRemote) {
	logger.Infow("中继协程已启动", "source", sourceID[:8])
	defer logger.Infow("中继协程已停止", "source", sourceID[:8])
	var ttsStarted atomic.Bool

	// 待识别队列与投递协程：解码与送识别都在本发布者自己的协程里做
	// 放在本协程里做的话，每包一次的解码耗时直接叠加到用户之间的转发延迟上
	asrQueue := dataplane.NewRing[[]byte]("sfu.asr."+sessionID, asrQueueCapacity)
	defer asrQueue.Close()
	go r.deliverToASR(sourceID, sessionID, asrQueue)

	for {
		rtpPacket, _, err := remoteTrack.ReadRTP()
		if err != nil {
			logger.Warnw("读取 RTP 失败", "source", sourceID[:8], "error", err)
			return
		}

		// 转发 RTP 给房间内所有客户端
		r.lock.RLock()
		forwardClient := make(map[string]*webrtc.TrackLocalStaticRTP)
		if peer, ok := r.peers[sourceID]; ok {
			for otherClintId, relay := range peer.outgoingRelays {
				forwardClient[otherClintId] = relay
			}
		}
		r.lock.RUnlock()

		for otherClintId, relay := range forwardClient {
			if err = relay.WriteRTP(rtpPacket); err != nil {
				logger.Warnw("写入 RTP 失败", "source", sourceID[:8], "dest", otherClintId, "error", err)
			}
		}

		// 交给识别协程解码。payload 的底层缓冲会被下一次 ReadRTP 复用，必须拷贝后再入队
		payload := make([]byte, len(rtpPacket.Payload))
		copy(payload, rtpPacket.Payload)
		asrQueue.Push(payload)

		// TTS 音频处理循环只启动一次，避免每个 RTP 包都泄漏一个 goroutine
		if !ttsStarted.Swap(true) {
			acr := aiCallReq{
				sessionID: sessionID,
				roomId:    r.ID,
				clientId:  sourceID,
			}
			go r.runAITTSLoop(acr)
		}
	}
}

// deliverToASR 每个发布者的识别投递协程：取 RTP payload，用复用解码器解成 PCM 后送识别
//
// 与转发协程分离，使解码耗时不再进入用户间的转发路径；解码器与输出缓冲在本协程内复用，
// 构造次数从每包一次降到每个发布者一次
// sourceID: 说话客户端；sessionID: 会话标识；queue: 待解码的 RTP payload 队列
func (r *SFURoom) deliverToASR(sourceID string, sessionID string, queue *dataplane.Ring[[]byte]) {
	dec, err := newOpusDecoder(asrSampleRate)
	if err != nil {
		logger.Warnw("创建 Opus 解码器失败，该发布者不再送识别", "source", sourceID[:8], "error", err)
		return
	}
	pcm := make([]int16, opusMaxFrameSamples)

	for {
		payload, ok := queue.Pop()
		if !ok {
			if queue.Closed() {
				return
			}
			<-queue.Wait()
			continue
		}

		n, decErr := decodeOpusInto(dec, payload, pcm)
		if decErr != nil {
			logger.Warnw("解码失败", "source", sourceID[:8], "error", decErr)
			continue
		}
		if n == 0 {
			continue // DTX 静音包，解码后没有有效采样
		}

		// 非阻塞投递：识别消费端繁忙时按背压丢弃，丢弃由 asr_cli 计数
		asr_cli.Push(&asrpb.AudioChunk{
			SessionId:  sessionID,
			RoomId:     r.ID,
			ClientId:   sourceID,
			Pcm:        int16ToLEBytes(pcm[:n]),
			SampleRate: asrSampleRate,
		})
	}
}

// queuedFrame 一帧编码完成的 Opus 数据，等待按实时速率发出
type queuedFrame struct {
	// payload 编码后的 Opus 数据
	payload []byte
}

// drainFrames 丢弃队列里尚未发出的帧，返回丢弃帧数
//
// queue: 待清空的帧队列；发送协程并发消费，因此清空是尽力而为
// return: 本次实际取走并丢弃的帧数
func drainFrames(queue chan queuedFrame) int {
	dropped := 0
	for {
		select {
		case <-queue:
			dropped++
		default:
			return dropped
		}
	}
}

// runAITTSLoop TTS 音频处理主循环：订阅会话音频端点 → 重采样 → Opus 编码 → RTP 发送
// 由 forwardRtp 在首包到达时启动一次，整个 session 复用同一循环
// 收到 AudioInterrupt 时清空待播帧并重置编码状态，使打断立即生效，不必等队列播完
func (r *SFURoom) runAITTSLoop(acr aiCallReq) {
	// 创建 Opus 编码器：48kHz 单声道 VoIP 模式，整个 TTS 会话复用同一个实例
	// CGO_ENABLED=1 编译时使用原生 libopus，编码质量最佳
	enc, err := newOpusEncoderPreset()
	if err != nil {
		logger.Warnw("创建Opus编码器失败", "source", acr.clientId[:8], "error", err)
		return
	}
	// enc.Close() 推迟到 audioLoopDone 中调用，在关闭 frameQueue 之前
	// 确保编码器在帧队列排空期间仍可用

	var pcmBuf []int16    // PCM 采样缓冲区，用于帧对齐（TTS 输出可能不对齐 20ms 帧边界）
	var totalPCM int      // 累计收到的 TTS PCM 字节数（16kHz 原始）
	var framesEncoded int // 累计编码发送的 Opus 帧数

	// 订阅会话级音频端点：端点与会话同寿命，合成执行器换代（打断或轮次重建）不影响订阅，
	// 因此不会出现「执行器重建之后没人消费音频」的失联
	endpoint := tts_cli.GlobalTTSService.AudioEndpoint(acr.sessionID)
	logger.Infow("TTS音频循环已启动", "source", acr.clientId[:8], "roomID", acr.roomId)

	// 创建 16kHz→48kHz 重采样器（polyphase FIR，纯 Go，替代手写线性插值）
	var upsampledBuf bytes.Buffer
	resampler, rsErr := resample.New(&upsampledBuf, resample.FormatInt16, 16000, 48000, 1, resample.WithKaiserFastFilter())
	if rsErr != nil {
		logger.Warnw("创建重采样器失败", "source", acr.clientId[:8], "error", rsErr)
		return
	}

	// rawBuf 累积原始 16kHz PCM，达到阈值后批量重采样
	var rawBuf []byte
	const rawResampleThreshold = 6400 // 200ms @ 16kHz mono int16
	var totalResampledBytes int       // 重采样后输出的总字节数（48kHz PCM）

	// 空闲超时 timer：2s 无新音频到达视为句子间自然停顿，刷新累积的 rawBuf
	// 使用 time.NewTimer + Reset 而非 time.After，避免每次迭代创建新 timer 导致泄漏
	idleTimer := time.NewTimer(2 * time.Second)
	defer idleTimer.Stop()

	// 帧发送队列：解耦编码（快速）与发送（20ms/帧 实时速率）
	// TTS 合成速度远超实时播放速度，若瞬间发送所有帧，Chrome NetEq 抖动缓冲区会溢出
	// 导致中间帧被丢弃——表现为只听到句子开头和末尾
	// 队列容量 2000 帧 = 40 秒缓冲，足以容纳最长 TTS 回复
	frameQueue := make(chan queuedFrame, 2000)

	// pacedSender 以实时速率从队列取帧并发送，避免 NetEq 缓冲区溢出
	senderDone := make(chan struct{})
	go func() {
		defer close(senderDone)
		sendTicker := time.NewTicker(20 * time.Millisecond)
		defer sendTicker.Stop()
		for {
			select {
			case <-sendTicker.C:
				select {
				case qf, ok := <-frameQueue:
					if !ok {
						// 队列已关闭且排空，发送完成
						return
					}

					// 把 AI 音频写给房间内每个当前成员各自独立的 AI 音轨
					// 每个成员音轨都是 TrackLocalStaticSample，WriteSample 由 pion packetizer/sequencer
					// 维护单调连续的 seq/ts，播放端 jitter buffer 才能正常解码
					// 若像之前那样混进说话者的麦克风中继轨，AI 帧的 seq/ts 与麦克风包各自起算，
					// 会被浏览器当成已过期的旧包丢弃，导致只有说话者本人（独立 AI 轨）能听到
					r.lock.RLock()
					recipients := make([]string, 0, len(r.peers))
					for destID := range r.peers {
						recipients = append(recipients, destID)
					}
					r.lock.RUnlock()

					for _, destID := range recipients {
						destTrack, err := r.getAITtsTrack(destID)
						if err != nil {
							logger.Warnw("获取AI TTS音轨失败", "dest", destID[:8], "error", err)
							continue
						}
						if err := destTrack.WriteSample(media.Sample{
							Data:               qf.payload,
							Duration:           20 * time.Millisecond,
							PrevDroppedPackets: 0,
						}); err != nil {
							logger.Warnw("写入AI TTS轨失败", "dest", destID[:8], "error", err)
						}
					}
				default:
					// 队列为空，等待下一次 tick
				}
			}
		}
	}()

	// flushRawBuf 把累积的原始 PCM 重采样进 pcmBuf，并清空 rawBuf
	// reason 用于日志区分触发来源，rebuild 为真时重建重采样器以清除跨句滤波状态
	flushRawBuf := func(reason string, rebuild bool) {
		if len(rawBuf) > 0 {
			upsampledBuf.Reset()
			if _, rsErr := resampler.Write(rawBuf); rsErr != nil {
				logger.Warnw("重采样失败", "source", acr.clientId[:8], "reason", reason, "error", rsErr)
			} else if output := upsampledBuf.Bytes(); len(output) > 0 {
				pcmBuf = append(pcmBuf, leBytesToInt16(output)...)
				totalResampledBytes += len(output)
			}
			rawBuf = rawBuf[:0]
		}
		if !rebuild {
			return
		}
		newResampler, rsErr := resample.New(&upsampledBuf, resample.FormatInt16, 16000, 48000, 1, resample.WithKaiserFastFilter())
		if rsErr != nil {
			logger.Warnw("重建重采样器失败", "source", acr.clientId[:8], "error", rsErr)
			return
		}
		resampler = newResampler
	}

	// encodePendingFrames 把所有完整帧（960 samples = 20ms @ 48kHz）编码后入队
	// 不足一帧的残采样保留在 pcmBuf，不补零避免破坏 Opus 编码器预测状态
	// 帧发送由 pacedSender goroutine 以 20ms 间隔实时发送，防止 NetEq 缓冲区溢出
	encodePendingFrames := func() {
		for len(pcmBuf) >= opusEncoderFrameSamples {
			frame := pcmBuf[:opusEncoderFrameSamples]
			pcmBuf = pcmBuf[opusEncoderFrameSamples:]

			opusPayload, encErr := enc.Encode(frame)
			if encErr != nil {
				logger.Warnw("Opus编码失败", "source", acr.clientId[:8], "error", encErr)
				continue
			}
			frameQueue <- queuedFrame{payload: opusPayload}
			framesEncoded++
		}
	}

	// resetCodecState 重建编码器与重采样器，清掉上一段留下的预测与滤波残留
	resetCodecState := func() {
		if newEnc, encErr := newOpusEncoderPreset(); encErr != nil {
			logger.Warnw("重建 Opus 编码器失败，沿用现有编码器", "source", acr.clientId[:8], "error", encErr)
		} else {
			_ = enc.Close()
			enc = newEnc
		}
		newResampler, rsErr := resample.New(&upsampledBuf, resample.FormatInt16, 16000, 48000, 1, resample.WithKaiserFastFilter())
		if rsErr != nil {
			logger.Warnw("重建重采样器失败，沿用现有重采样器", "source", acr.clientId[:8], "error", rsErr)
			return
		}
		resampler = newResampler
	}

	// drainFrameQueue 丢弃帧队列里尚未播出的帧，返回丢弃帧数
	// 与 pacedSender 并发，因此是尽力而为：pacer 每个 tick 至多再补发一帧（20ms）
	drainFrameQueue := func() int {
		return drainFrames(frameQueue)
	}

	for {
		ev, ok := endpoint.Next()
		if !ok {
			if endpoint.Closed() {
				logger.Infow("音频端点已关闭，退出TTS循环", "source", acr.clientId[:8])
				break
			}
			select {
			case <-endpoint.Wait():
			case <-idleTimer.C:
				// 2s 无新音频到达，视为句子间自然停顿，刷新累积的 rawBuf
				idleTimer.Reset(2 * time.Second)
				flushRawBuf("idle", true)
			}
			encodePendingFrames()
			continue
		}

		if ev.Kind == tts_cli.AudioInterrupt {
			// 打断：丢弃尚未播出的帧与未编码的采样，并重建编码器状态
			// 不重建的话，下一轮开头会带上上一轮编码器与滤波器的残留
			droppedFrames := drainFrameQueue()
			pcmBuf = pcmBuf[:0]
			rawBuf = rawBuf[:0]
			resetCodecState()
			logger.Infow("打断：已清空待播音频",
				"source", acr.clientId[:8], "droppedFrames", droppedFrames)
			continue
		}

		// AudioData：重置空闲计时
		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleTimer.Reset(2 * time.Second)

		totalPCM += len(ev.PCM)

		// 累积原始 PCM，达到阈值后批量重采样
		rawBuf = append(rawBuf, ev.PCM...)
		if len(rawBuf) >= rawResampleThreshold {
			flushRawBuf("threshold", false)
		}
		encodePendingFrames()
	}

	// 编码并排空 pcmBuf 残帧（补零至完整帧），确保尾部音频不丢失
	if len(pcmBuf) > 0 {
		padded := make([]int16, opusEncoderFrameSamples)
		copy(padded, pcmBuf)
		if opusPayload, err := enc.Encode(padded); err == nil {
			frameQueue <- queuedFrame{payload: opusPayload}
			framesEncoded++
		} else {
			logger.Warnw("尾部残帧编码失败", "source", acr.clientId[:8], "error", err)
		}
	}

	// 关闭编码器（后续不再有新帧入队）
	enc.Close()

	// 关闭帧队列并等待 pacedSender 排空所有帧
	close(frameQueue)
	<-senderDone

	logger.Infow("TTS 音频流结束",
		"source", acr.clientId[:8],
		"totalPCM", totalPCM,
		"resampledBytes", totalResampledBytes,
		"framesEncoded", framesEncoded,
	)
}

// stopForwarding 关闭转发协程，确保协程退出
func (p *SFUPeer) stopForwarding() {
	select {
	case <-p.stopRelay:
		// 已关闭
	default:
		close(p.stopRelay)
	}
}

// CleanupRoom 关闭房间内所有 PeerConnection 并删除房间
func (s *SFUServer) CleanupRoom(roomID string) {
	r := s.GetRoom(roomID)
	if r == nil {
		return
	}

	r.lock.Lock()
	for clientID, peer := range r.peers {
		peer.stopForwarding()
		if err := peer.PC.Close(); err != nil {
			logger.Warnw("清理关闭 PC", "clientID", clientID[:8], "error", err)
		}
		delete(r.peers, clientID)
	}
	r.lock.Unlock()

	s.RemoveRoom(roomID)
}
