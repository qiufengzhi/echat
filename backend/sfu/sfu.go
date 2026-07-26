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
	"echat-backend/global"
	"echat-backend/llm_cli"
	"echat-backend/logging"
	asrpb "echat-backend/proto/asr"
	llmpb "echat-backend/proto/llm"
	"echat-backend/tts_cli"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gunter-q12/resample"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

var logger = logging.New("sfu")

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

	// 该客户端发布的音频远端音轨（由 SFU 的 OnTrack 收到）
	publishedAudioTrack *webrtc.TrackRemote

	// 该客户端音频被转发给其他客户端时的本地中继音轨
	// 键是订阅者（接收方）的 clientID，值是对应中继音轨，该房间内其他所有用户
	outgoingRelays map[string]*webrtc.TrackLocalStaticRTP

	// AI TTS 合成语音输出轨，写入后客户端通过 ontrack 收到
	// 由 getAITtsTrack 首次使用时懒创建并 AddTrack 到 PC
	aiTtsTrack *webrtc.TrackLocalStaticRTP

	// 停止中继转发协程的信号，在客户端离开或断连时关闭
	stopRelay chan struct{}

	connectedAt time.Time
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
	logger.Infow("房间已创建", "roomID", roomID)
	return room
}

// RemoveRoom 删除 SFU 房间。调用方应在确认房间内无活跃 peer 后调用
func (s *SFUServer) RemoveRoom(roomID string) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if _, ok := s.rooms[roomID]; ok {
		delete(s.rooms, roomID)
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

	api := webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine))

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
	sessionID := fmt.Sprintf("%s-%s", clientID, r.ID)
	asr_cli.GlobalRecognizer.AudioIn <- asrpb.AudioChunk{
		SessionId:  sessionID,
		RoomId:     r.ID,
		ClientId:   clientID,
		SampleRate: 16000,
		IsLast:     true,
	}

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
	go r.forwardRtp(sourceID, remoteTrack)
}

// getAITtsTrack 懒初始化 AI TTS 输出轨，返回该客户端的 AI 语音播放通道
// 首次调用时创建 TrackLocalStaticRTP 并 AddTrack 到 PeerConnection，触发重协商
// 后续调用直接复用已创建的 track
func (r *SFURoom) getAITtsTrack(clientID string) (*webrtc.TrackLocalStaticRTP, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	peer, ok := r.peers[clientID]
	if !ok {
		return nil, fmt.Errorf("getAITtsTrack: 客户端 %s 不在房间", clientID[:8])
	}
	if peer.aiTtsTrack != nil {
		return peer.aiTtsTrack, nil
	}

	track, err := webrtc.NewTrackLocalStaticRTP(
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

// StartASRLogger 启动后台协程，持续从 ASR 识别器读取识别结果并打印日志
// 必须在 asr_cli.Init() 之后调用
func StartASRLogger() {
	go getAsrRes()
}

// getAsrRes 读取阿里云 ASR 返回的识别结果并打印日志
func getAsrRes() {
	for res := range asr_cli.GlobalRecognizer.AudioOut {
		// todo 唤醒/屏蔽 AI助手 唤醒：提示词、按钮  打断：任何话、按钮
		// 是否开启ai语音助手
		if !global.StartAiAssistant.Load() {
			continue
		}
		logger.Infow("收到识别结果", "userID", res.ClientId, "text", res.Text, "isFinal", res.IsFinal, "seq", res.Seq)
		// 将识别结果送 LLM
		llm_cli.LLMServiceClient.In <- &llmpb.LLMRequest{
			SessionId: res.SessionId,
			RoomId:    res.RoomId,
			ClientId:  res.ClientId,
			UserText:  res.Text,
			IsLast:    res.IsFinal,
			Seq:       res.Seq,
		}
	}
}

// forwardRtp 接收到客户端的语音包，从 remoteTrack 读取 RTP 包，转发给房间其他客户端，同时送阿里云 ASR 做语音识别
func (r *SFURoom) forwardRtp(sourceID string, remoteTrack *webrtc.TrackRemote) {
	logger.Infow("中继协程已启动", "source", sourceID[:8])
	defer logger.Infow("中继协程已停止", "source", sourceID[:8])
	// sessionID 使用 clientID-roomID 格式，保持与 ASR → LLM → TTS 管道一致
	// 确保 GetAudio 和 ProcessText 使用同一 session，避免音频数据写入错误会话
	sessionID := fmt.Sprintf("%s-%s", sourceID, r.ID)
	var ttsOnce sync.Once

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

		// 开启AI语音助手
		acr := aiCallReq{
			sessionID: sessionID,
			roomId:    r.ID,
			clientId:  sourceID,
			rtpPacket: rtpPacket,
			clients:   forwardClient,
		}
		go r.aiCall(acr, &ttsOnce)
	}
}

// aiCall 调用 AI 助手。先送入ASR，总是开启，唤醒词唤醒需要ASR
func (r *SFURoom) aiCall(acr aiCallReq, ttsOnce *sync.Once) {
	// 解码 Opus → PCM，送 ASR
	go func() {
		pcm, err := decodeOpusToInt16(acr.rtpPacket.Payload, 16000)
		if err != nil {
			logger.Warnw("解码失败", "source", acr.clientId[:8], "error", err)
		} else if len(pcm) > 0 {
			// 仅有实际音频数据才送 ASR，DTX 静音包解码后 pcm 为空，直接跳过
			asr_cli.GlobalRecognizer.AudioIn <- asrpb.AudioChunk{
				SessionId:  acr.sessionID,
				RoomId:     acr.roomId,
				ClientId:   acr.clientId,
				Pcm:        int16ToLEBytes(pcm),
				SampleRate: 16000,
			}
		}
	}()

	// 从TTS获取音频，转发给房间内所有客户端，包括说话者本人
	// sync.Once 保证同一 session 的 TTS 转发协程只启动一次
	// 注意：acr.clients 是首次调用时的快照，之后加入的客户端不会收到本次 AI 回复
	ttsOnce.Do(func() {
		aiTtsTrack, err := r.getAITtsTrack(acr.clientId)
		if err != nil {
			logger.Warnw("获取AI TTS音轨失败", "source", acr.clientId[:8], "error", err)
			return
		}

		// 创建 Opus 编码器：48kHz 单声道 VoIP 模式，整个 TTS 会话复用同一个实例
		// 注意：非 CGo 编码器（ccgo 转译 libopus）在 16kHz 下有已知缺陷，故统一使用 48kHz
		// CGO_ENABLED=1 编译时使用原生 libopus，编码质量最佳
		enc, err := newOpusEncoderPreset()
		if err != nil {
			logger.Warnw("创建Opus编码器失败", "source", acr.clientId[:8], "error", err)
			return
		}
		defer enc.Close()

		var pcmBuf []int16    // PCM 采样缓冲区，用于帧对齐（TTS 输出可能不对齐 20ms 帧边界）
		var seq uint16        // RTP 包序号
		var ts uint32         // RTP 时间戳，按采样数递增
		var totalPCM int      // 调试：累计收到的 TTS PCM 字节数（16kHz 原始）
		var framesEncoded int // 调试：累计编码发送的 Opus 帧数
		var firstChunk bool = true

		lrs := llmpb.LLMResponse{
			SessionId: acr.sessionID, // 使用 forwardRtp 的一致 sessionID，保证和 LLM 返回的 SessionId 一致
			RoomId:    acr.roomId,
			ClientId:  acr.clientId,
		}

		audioCh := tts_cli.GlobalTTSService.GetAudio(&lrs)
		logger.Infow("TTS音频循环已启动", "source", acr.clientId[:8], "roomID", acr.roomId)

		// 调试：导出原始 PCM，可用 ffplay -f s16le -ar 16000 -ac 1 文件名 播放
		dumpPath := "tts_dump_" + acr.clientId[:8] + ".pcm"
		dumpFile, dumpErr := os.Create(dumpPath)
		if dumpErr != nil {
			logger.Warnw("创建TTS dump文件失败", "source", acr.clientId[:8], "error", dumpErr)
		}
		if dumpFile != nil {
			defer dumpFile.Close()
		}
		var dumpTotal int

		// 创建 16kHz→48kHz 重采样器（polyphase FIR，纯 Go，替代手写线性插值）
		var upsampledBuf bytes.Buffer
		resampler, rsErr := resample.New(&upsampledBuf, resample.FormatInt16, 16000, 48000, 1, resample.WithKaiserFastFilter())
		if rsErr != nil {
			logger.Warnw("创建重采样器失败", "source", acr.clientId[:8], "error", rsErr)
			return
		}

		// rawBuf 累积原始 16kHz PCM，达到阈值后批量重采样
		// 每次 resampler.Write 内部会新建滤波器状态，逐 chunk 调用导致边界失真
		// 批量累积后再重采样可大幅减少调用次数，消除边界失真累积
		var rawBuf []byte
		// overlapSamples 重采样历史重叠采样数，为 Kaiser 滤波器提供历史上下文
		// 64 采样 = 128 字节 @ int16，可覆盖 ~32 抽头的 Kaiser 窗滤波器左翼
		// 重采样时拼接在批次前面，输出后丢弃重叠部分，消除批次边界失真
		const overlapSamples = 64
		const overlapBytes = overlapSamples * 2 // int16 = 2 bytes per sample
		var history []byte                      // 上一批次末尾的原始采样，供下次重采样做滤波器上下文
		// rawResampleThreshold 累积 200ms 原始音频后触发批量重采样
		// 6400 字节 = 3200 samples @ 16kHz → 重采样后 9600 samples @ 48kHz = 10 个 Opus 帧
		const rawResampleThreshold = 6400
		// 上采样比例：48000 / 16000 = 3x
		const upsampleRatio = 3
		// 诊断计数器
		var totalResampledBytes int // 重采样后输出的总字节数（48kHz PCM）
		for {
			timeout := time.After(2 * time.Second)
			select {
			case rawPCM, ok := <-audioCh:
				if !ok {
					goto audioLoopDone
				}
				totalPCM += len(rawPCM)
				if dumpFile != nil {
					dumpFile.Write(rawPCM)
					dumpTotal += len(rawPCM)
				}

				// 首个 chunk：打印字节序诊断信息（仅首次）
				if firstChunk {
					firstChunk = false
					hexPreview := ""
					for i := 0; i < 20 && i < len(rawPCM); i++ {
						hexPreview += fmt.Sprintf("%02x ", rawPCM[i])
					}
					samplesLE := leBytesToInt16(rawPCM)
					lePreview := ""
					for i := 0; i < 10 && i < len(samplesLE); i++ {
						lePreview += fmt.Sprintf("%d,", samplesLE[i])
					}
					bePreview := ""
					for i := 0; i < 10 && i < len(samplesLE); i++ {
						beVal := int16(binary.BigEndian.Uint16(rawPCM[i*2:]))
						bePreview += fmt.Sprintf("%d,", beVal)
					}
					logger.Debugw("TTS首个音频块诊断",
						"source", acr.clientId[:8],
						"byteLen", len(rawPCM),
						"hex", hexPreview,
						"le", lePreview,
						"be", bePreview,
					)
				}

				// 累积原始 PCM，达到阈值后批量重采样
				rawBuf = append(rawBuf, rawPCM...)
				if len(rawBuf) >= rawResampleThreshold {
					upsampledBuf.Reset()
					// 拼接历史上下文 → 重采样 → 丢弃重叠输出，保持滤波器连续性
					input := make([]byte, len(history)+len(rawBuf))
					copy(input, history)
					copy(input[len(history):], rawBuf)
					if _, rsErr := resampler.Write(input); rsErr != nil {
						logger.Warnw("重采样失败", "source", acr.clientId[:8], "error", rsErr)
						rawBuf = rawBuf[:0]
						continue
					}
					output := upsampledBuf.Bytes()
					// 丢弃重叠输出（与上一批次末尾重叠，避免重复播放）
					overlapOutBytes := len(history) * upsampleRatio
					if overlapOutBytes < len(output) {
						output = output[overlapOutBytes:]
					} else {
						output = nil
					}
					if len(output) > 0 {
						upsampled := leBytesToInt16(output)
						pcmBuf = append(pcmBuf, upsampled...)
						totalResampledBytes += len(output)
					}
					// 保存末尾样本作为下一批次的历史上下文
					if len(rawBuf) > overlapBytes {
						history = append(history[:0], rawBuf[len(rawBuf)-overlapBytes:]...)
					} else {
						history = append(history[:0], rawBuf...)
					}
					rawBuf = rawBuf[:0]
				}
			case <-timeout:
				// 2s 空闲，刷新 rawBuf 中累积的原始 PCM
				// 重置历史上下文，因为超时意味着句子间的自然停顿
				if len(rawBuf) > 0 {
					upsampledBuf.Reset()
					input := make([]byte, len(history)+len(rawBuf))
					copy(input, history)
					copy(input[len(history):], rawBuf)
					if _, rsErr := resampler.Write(input); rsErr != nil {
						logger.Warnw("重采样失败(timeout)", "source", acr.clientId[:8], "error", rsErr)
					} else {
						output := upsampledBuf.Bytes()
						overlapOutBytes := len(history) * upsampleRatio
						if overlapOutBytes < len(output) {
							output = output[overlapOutBytes:]
						} else {
							output = nil
						}
						if len(output) > 0 {
							upsampled := leBytesToInt16(output)
							pcmBuf = append(pcmBuf, upsampled...)
							totalResampledBytes += len(output)
						}
					}
					history = history[:0] // 超时后重置历史：句子间有自然停顿
					rawBuf = rawBuf[:0]
				}
			}

			// 编码所有完整帧（960 samples = 20ms @ 48kHz）
			// 不足一帧的残采样保留在 pcmBuf，不补零避免破坏 Opus 编码器预测状态
			for len(pcmBuf) >= opusEncoderFrameSamples {
				frame := pcmBuf[:opusEncoderFrameSamples]
				pcmBuf = pcmBuf[opusEncoderFrameSamples:]

				opusPayload, err := enc.Encode(frame)
				if err != nil {
					logger.Warnw("Opus编码失败", "source", acr.clientId[:8], "error", err)
					continue
				}

				// 构建 RTP 包，Version/PayloadType/SSRC 由 WriteRTP 内部根据 track 绑定覆写
				pkt := &rtp.Packet{
					Header: rtp.Header{
						Version:        2,
						Marker:         seq == 0, // RFC 7587: talkspurt first packet
						SequenceNumber: seq,
						Timestamp:      ts,
					},
					Payload: opusPayload,
				}

				// 写入说话者本人的 AI TTS 轨，客户端通过 ontrack 收到 AI 语音
				if err = aiTtsTrack.WriteRTP(pkt); err != nil {
					logger.Warnw("写入AI TTS轨失败", "source", acr.clientId[:8], "error", err)
				}

				// 写入房间内其他客户端的 relay track，让所有人都能听到 AI 回复
				for clientID, relay := range acr.clients {
					if err = relay.WriteRTP(pkt); err != nil {
						logger.Warnw("写入AI中继轨失败", "dest", clientID[:8], "error", err)
					}
				}

				seq++
				framesEncoded++
				ts += opusEncoderFrameSamples // Opus RTP 时钟 48000Hz，每 20ms 帧 = 960 ticks
			}
		}
	audioLoopDone:

		// 调试：确认 PCM dump 已写入
		if dumpFile != nil && dumpTotal > 0 {
			logger.Infow("TTS dump 已写入",
				"path", dumpPath, "bytes", dumpTotal,
				"playback", "ffplay -f s16le -ar 16000 -ac 1 "+dumpPath,
			)
		}

		logger.Infow("TTS 音频流结束",
			"source", acr.clientId[:8],
			"totalPCM", totalPCM,
			"resampledBytes", totalResampledBytes,
			"seq", seq,
			"framesEncoded", framesEncoded,
			"tailPCM", len(pcmBuf),
		)
	})
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

// writeWav 将原始 PCM 数据写入 WAV 文件（临时调试用）
func writeWav(f *os.File, pcmData []byte, sampleRate, numChannels, bitsPerSample int) {
	dataSize := len(pcmData)
	byteRate := sampleRate * numChannels * bitsPerSample / 8
	blockAlign := numChannels * bitsPerSample / 8

	// RIFF header
	f.Write([]byte("RIFF"))
	writeU32LE(f, uint32(36+dataSize))
	f.Write([]byte("WAVE"))

	// fmt chunk
	f.Write([]byte("fmt "))
	writeU32LE(f, 16) // chunk size
	writeU16LE(f, 1)  // PCM format
	writeU16LE(f, uint16(numChannels))
	writeU32LE(f, uint32(sampleRate))
	writeU32LE(f, uint32(byteRate))
	writeU16LE(f, uint16(blockAlign))
	writeU16LE(f, uint16(bitsPerSample))

	// data chunk
	f.Write([]byte("data"))
	writeU32LE(f, uint32(dataSize))
	f.Write(pcmData)
}

func writeU32LE(f *os.File, v uint32) {
	f.Write([]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)})
}

func writeU16LE(f *os.File, v uint16) {
	f.Write([]byte{byte(v), byte(v >> 8)})
}
