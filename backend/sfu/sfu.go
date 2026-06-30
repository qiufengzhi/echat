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
//	                                       │  PeerConnection   │
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
	"echat-backend/asr_cli"
	"echat-backend/config"
	asrpb "echat-backend/proto/asr"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

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

	// 停止中继转发协程的信号，在客户端离开或断连时关闭
	stopRelay chan struct{}

	connectedAt time.Time
}

// InitSFU 打印 SFU 配置信息
// 必须在 config.Load() 之后调用
func InitSFU() {
	cfg := config.Get().SFU
	log.Printf("[sfu] 媒体端口范围: %d-%d, NAT1To1IP: %q, STUN: %v",
		cfg.MediaMinPort, cfg.MediaMaxPort, cfg.NAT1To1IP, cfg.STUNServers)
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
			log.Printf("[sfu] 域名解析失败: %s, err=%v", addr, err)
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
	log.Printf("[sfu] 房间已创建: %s", roomID)
	return room
}

// RemoveRoom 删除 SFU 房间。调用方应在确认房间内无活跃 peer 后调用
func (s *SFUServer) RemoveRoom(roomID string) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if _, ok := s.rooms[roomID]; ok {
		delete(s.rooms, roomID)
		log.Printf("[sfu] 房间已删除: %s", roomID)
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
			log.Printf("[sfu] ICE 收集完成: client=%s", clientID[:8])
			return
		}
		if r.onICECandidate == nil {
			log.Printf("[sfu] ICE callback 未设置，candidate 已丢弃: client=%s", clientID[:8])
			return
		}
		candJSON := candidate.ToJSON()
		log.Printf("[sfu] 收集到 ICE candidate: client=%s candidate=%s", clientID[:8], candJSON.Candidate)
		r.onICECandidate(clientID, candJSON)
	})

	// 当 AddTrack 被调用后，pion 会触发 OnNegotiationNeeded，此时需要向客户端发送 renegotiation Offer
	pc.OnNegotiationNeeded(func() {
		if r.onRenegotiation == nil {
			log.Printf("[sfu] renegotiation 触发但无回调: client=%s", clientID[:8])
			return
		}
		offer, err := pc.CreateOffer(nil)
		if err != nil {
			log.Printf("[sfu] 创建 renegotiation Offer 失败: client=%s err=%v", clientID[:8], err)
			return
		}
		if err = pc.SetLocalDescription(offer); err != nil {
			log.Printf("[sfu] 设置 renegotiation Offer 失败: client=%s err=%v", clientID[:8], err)
			return
		}
		log.Printf("[sfu] renegotiation Offer 已创建: client=%s", clientID[:8])
		r.onRenegotiation(clientID, offer.SDP)
	})

	// ICE 连接状态日志
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("[sfu] ICE 状态: client=%s state=%s", clientID[:8], state.String())
	})

	// PeerConnection 整体状态变化
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[sfu] PC 状态: client=%s state=%s", clientID[:8], state.String())
		if state == webrtc.PeerConnectionStateDisconnected ||
			state == webrtc.PeerConnectionStateFailed {
			// 连接意外断开，清理转发资源
			peer.stopForwarding()
		}
	})

	// 当客户端开始发送音频时，SFU 接收音轨并转发给其他客户端
	pc.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if remoteTrack.Kind() != webrtc.RTPCodecTypeAudio {
			log.Printf("[sfu] 跳过非音频轨: %s", clientID[:8])
			return
		}

		log.Printf("[sfu] 收到音频轨: client=%s codec=%s",
			clientID[:8], remoteTrack.Codec().MimeType)

		peer.publishedAudioTrack = remoteTrack
		// 在锁外启动转发，避免持有锁时调用 AddTrack（AddTrack 也可能触发 ICE 回调）
		go r.startForwarding(clientID, remoteTrack)
	})

	r.peers[clientID] = peer
	log.Printf("[sfu] 对端加入: client=%s room=%s", clientID[:8], r.ID)
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

	log.Printf("[sfu] 接受 Offer 已回复 Answer: client=%s", clientID[:8])
	log.Printf("[sfu] Answer SDP 内容:\n%s", answer.SDP)
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

	log.Printf("[sfu] 对端离开: client=%s", clientID[:8])

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
		log.Printf("[sfu] 关闭 PC 失败: client=%s err=%v", clientID[:8], err)
	}

	if empty {
		log.Printf("[sfu] 房间已空: %s", r.ID)
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

	log.Printf("[sfu] renegotiation answer 已处理: client=%s", clientID[:8])
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
			log.Printf("[sfu] 创建中继轨失败: source=%s sub=%s err=%v",
				sourceID[:8], subscriberID[:8], err)
			continue
		}

		if _, err = subscriber.PC.AddTrack(relayTrack); err != nil {
			log.Printf("[sfu] 添加中继轨失败: source=%s sub=%s err=%v",
				sourceID[:8], subscriberID[:8], err)
			continue
		}

		// 将房间内其他人的音轨通过中继轨加入 source
		sourcePeer.outgoingRelays[subscriberID] = relayTrack
		log.Printf("[sfu] 中继已添加: %s -> %s", sourceID[:8], subscriberID[:8])
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
			log.Printf("[sfu] 为新对端创建中继轨失败: source=%s other=%s err=%v",
				sourceID[:8], otherID[:8], err)
			continue
		}

		if _, err = sourcePeer.PC.AddTrack(relayTrack); err != nil {
			log.Printf("[sfu] 为新对端添加中继轨失败: source=%s other=%s err=%v",
				sourceID[:8], otherID[:8], err)
			continue
		}

		// 将 other 的音频转发给 source
		otherPeer.outgoingRelays[sourceID] = relayTrack
		log.Printf("[sfu] 中继已添加(新对端): %s <- %s", sourceID[:8], otherID[:8])
	}

	r.lock.Unlock()

	// 启动 RTP 转发协程：从 remoteTrack 读取 RTP 包，写入所有中继音轨。将本人的音频转发给房间内其他客户端
	go r.forwardRtp(sourceID, remoteTrack)
}

// StartASRLogger 启动后台协程，持续从 ASR 识别器读取识别结果并打印日志
// 必须在 asr_cli.Init() 之后调用
func StartASRLogger() {
	go getAsrRes()
}

// getAsrRes 读取阿里云 ASR 返回的识别结果并打印日志
func getAsrRes() {
	for res := range asr_cli.GlobalRecognizer.AudioOut {
		log.Printf("[asr] 收到识别结果: user=%s text=%q isFinal=%v seq=%d", res.ClientId, res.Text, res.IsFinal, res.Seq)
	}
}

// forwardRtp 从 remoteTrack 读取 RTP 包，转发给房间其他客户端，同时送阿里云 ASR 做语音识别
func (r *SFURoom) forwardRtp(sourceID string, remoteTrack *webrtc.TrackRemote) {
	log.Printf("[sfu] 中继协程已启动: source=%s", sourceID[:8])
	defer log.Printf("[sfu] 中继协程已停止: source=%s", sourceID[:8])

	sessionID := fmt.Sprintf("%s-%s", sourceID, r.ID)

	for {
		rtpPacket, _, err := remoteTrack.ReadRTP()
		if err != nil {
			log.Printf("[sfu] 读取 RTP 失败: source=%s err=%v", sourceID[:8], err)
			return
		}

		// 解码 Opus → PCM，直接送阿里云 ASR
		pcm, err := decodeOpusToInt16(rtpPacket.Payload, 16000)
		if err != nil {
			log.Printf("[asr] 解码失败: source=%s err=%v", sourceID[:8], err)
		} else if len(pcm) > 0 {
			// 仅有实际音频数据才送 ASR，DTX 静音包解码后 pcm 为空，直接跳过
			asr_cli.GlobalRecognizer.AudioIn <- asrpb.AudioChunk{
				SessionId:  sessionID,
				RoomId:     r.ID,
				ClientId:   sourceID,
				Pcm:        int16ToLEBytes(pcm),
				SampleRate: 16000,
			}
		}

		// 转发 RTP 给房间内其他客户端
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
				log.Printf("[sfu] 写入 RTP 失败: source=%s dest=%s err=%v", sourceID[:8], otherClintId, err)
			}
		}
	}
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
			log.Printf("[sfu] 清理关闭 PC: client=%s err=%v", clientID[:8], err)
		}
		delete(r.peers, clientID)
	}
	r.lock.Unlock()

	s.RemoveRoom(roomID)
}
