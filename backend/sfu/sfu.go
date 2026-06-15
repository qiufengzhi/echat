// Package sfu 实现基于 Pion WebRTC 的 SFU（Selective Forwarding Unit）音频转发引擎。
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
//   - 每个客户端只与 SFU 服务器建立一条 PeerConnection，而非与其他每个客户端建立 N-1 条。
//   - 服务器收到某客户端的音频后，创建本地中继音轨并将其添加到房间内其他所有客户端的
//     PeerConnection 上。
//   - 客户端只需关注与服务器的单条连接，远端音频以多路 MediaStream 形式通过 ontrack 到达。
package sfu

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

// ICECandidateCallback 是 SFU -> 信令层 的 ICE Candidate 回调。
// 信令层收到后通过 WebSocket 转发给对应客户端。
type ICECandidateCallback func(clientID string, candidate webrtc.ICECandidateInit)

// SFUServer 管理一组 SFU 房间，每个房间包含多个 SFU Peer 连接。
// 调用方（room 包）通过 GetOrCreateRoom / RemoveRoom 来管理 SFU 房间生命周期。
type SFUServer struct {
	rooms map[string]*SFURoom // roomID -> SFU 房间
	lock  sync.RWMutex
}

// SFURoom 对应一个语音房的 SFU 转发域，维护房间内所有客户端的 WebRTC PeerConnection。
type SFURoom struct {
	ID    string
	peers map[string]*SFUPeer // clientID -> SFU 对端
	lock  sync.RWMutex

	// ICE Candidate 回调，由信令层注册，用于把 SFU 收集到的 candidate 转发给客户端。
	onICECandidate ICECandidateCallback
}

// SFUPeer 包装单个客户端的 WebRTC PeerConnection，负责收发音频和中继管理。
type SFUPeer struct {
	ClientID string                 // 对应 room.Client.ID
	PC       *webrtc.PeerConnection // 到客户端的 WebRTC 连接

	// 该客户端发布的音频远端音轨（由 SFU 的 OnTrack 收到）。
	incomingTrack *webrtc.TrackRemote

	// 该客户端音频被转发给其他客户端时的本地中继音轨。
	// 键是订阅者（接收方）的 clientID，值是对应中继音轨。
	outgoingRelays map[string]*webrtc.TrackLocalStaticRTP

	// 停止中继转发协程的信号，在客户端离开或断连时关闭。
	stopRelay chan struct{}

	connectedAt time.Time
}

// NewSFUServer 创建 SFU 引擎实例。
func NewSFUServer() *SFUServer {
	return &SFUServer{
		rooms: make(map[string]*SFURoom),
	}
}

// GetOrCreateRoom 查找已有 SFU 房间，不存在时创建新房间。
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

// RemoveRoom 删除 SFU 房间。调用方应在确认房间内无活跃 peer 后调用。
func (s *SFUServer) RemoveRoom(roomID string) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if _, ok := s.rooms[roomID]; ok {
		delete(s.rooms, roomID)
		log.Printf("[sfu] 房间已删除: %s", roomID)
	}
}

// GetRoom 获取指定 SFU 房间，nil 表示不存在。
func (s *SFUServer) GetRoom(roomID string) *SFURoom {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return s.rooms[roomID]
}

// Join 为客户端创建到 SFU 的 WebRTC PeerConnection，但不生成 Offer。
// Offer 由客户端发起，服务端通过 AcceptOffer 创建 Answer 完成协商。
func (r *SFURoom) Join(clientID string) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	// 防止同一 clientID 重复加入。
	if _, exists := r.peers[clientID]; exists {
		return fmt.Errorf("client %s already in SFU room", clientID)
	}

	// 创建 WebRTC PeerConnection，STUN 服务器用于 NAT 穿透。
	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	}

	pc, err := webrtc.NewPeerConnection(config)
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

	// ICE Candidate 收集 -> 通知信令层转发给客户端。
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil || r.onICECandidate == nil {
			return
		}
		// ToJSON 返回 ICECandidateInit，pion/webrtc v4 中不返回 error。
		candJSON := candidate.ToJSON()
		r.onICECandidate(clientID, candJSON)
	})

	// ICE 连接状态日志。
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("[sfu] ICE 状态: client=%s state=%s", clientID[:8], state.String())
	})

	// PeerConnection 整体状态变化。
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[sfu] PC 状态: client=%s state=%s", clientID[:8], state.String())
		if state == webrtc.PeerConnectionStateDisconnected ||
			state == webrtc.PeerConnectionStateFailed {
			// 连接意外断开，清理转发资源。
			peer.stopForwarding()
		}
	})

	// 当客户端开始发送音频时，SFU 接收音轨并转发给其他客户端。
	pc.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if remoteTrack.Kind() != webrtc.RTPCodecTypeAudio {
			log.Printf("[sfu] 跳过非音频轨: %s", clientID[:8])
			return
		}

		log.Printf("[sfu] 收到音频轨: client=%s codec=%s",
			clientID[:8], remoteTrack.Codec().MimeType)

		peer.incomingTrack = remoteTrack
		// 在锁外启动转发，避免持有锁时调用 AddTrack（AddTrack 也可能触发 ICE 回调）。
		go r.startForwarding(clientID, remoteTrack)
	})

	r.peers[clientID] = peer
	log.Printf("[sfu] 对端加入: client=%s room=%s", clientID[:8], r.ID)
	return nil
}

// AcceptOffer 处理客户端发来的 SDP Offer，创建并返回 Answer SDP。
//
// 调用时机：客户端创建 Offer 后通过 sfu_offer 信令发送给服务端，
// 服务端调用此方法设置远端描述、创建 Answer，再将 Answer 通过 sfu_answer 发回客户端。
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
	if err := peer.PC.SetRemoteDescription(offer); err != nil {
		return "", fmt.Errorf("set remote description: %w", err)
	}

	answer, err := peer.PC.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("create answer: %w", err)
	}
	if err := peer.PC.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("set local description: %w", err)
	}

	log.Printf("[sfu] 接受 Offer 已回复 Answer: client=%s", clientID[:8])
	log.Printf("[sfu] Answer SDP 内容:\n%s", answer.SDP)
	return answer.SDP, nil
}

// AcceptICECandidate 处理客户端发来的 ICE Candidate。
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

// Leave 处理客户端离开，关闭 PeerConnection 并清理转发资源。
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

	// 停止转发协程。
	peer.stopForwarding()

	// 从其他所有客户端的中继表中移除当前客户端的音轨。
	r.lock.Lock()
	for _, otherPeer := range r.peers {
		delete(otherPeer.outgoingRelays, clientID)
	}
	empty := len(r.peers) == 0
	r.lock.Unlock()

	// 关闭 PeerConnection。
	if err := peer.PC.Close(); err != nil {
		log.Printf("[sfu] 关闭 PC 失败: client=%s err=%v", clientID[:8], err)
	}

	if empty {
		log.Printf("[sfu] 房间已空: %s", r.ID)
	}
}

// SetOnICECandidate 注册 ICE Candidate 回调，供信令层转发 candidate 给客户端。
func (r *SFURoom) SetOnICECandidate(cb ICECandidateCallback) {
	r.lock.Lock()
	defer r.lock.Unlock()
	r.onICECandidate = cb
}

// PeerCount 返回房间内 SFU 对端数量。
func (r *SFURoom) PeerCount() int {
	r.lock.RLock()
	defer r.lock.RUnlock()
	return len(r.peers)
}

// HasPeer 检查指定客户端是否在 SFU 房间内。
func (r *SFURoom) HasPeer(clientID string) bool {
	r.lock.RLock()
	defer r.lock.RUnlock()
	_, ok := r.peers[clientID]
	return ok
}

// startForwarding 在收到客户端音频轨后，创建中继音轨并开始转发。
//
// 转发逻辑：
//  1. source 的音频到达 SFU，通过 OnTrack 收到 remoteTrack。
//  2. 为房间内每个其他客户端创建一个 TrackLocalStaticRTP（中继音轨），
//     添加到该客户端的 PeerConnection 中。
//  3. 同时也为 source 创建其他已有客户端的中继音轨，让 source 能听到其他人。
//  4. 启动协程持续读取 source 的 RTP 包并写入所有中继音轨。
func (r *SFURoom) startForwarding(sourceID string, remoteTrack *webrtc.TrackRemote) {
	r.lock.Lock()

	// 为房间内每个其他客户端创建中继音轨。
	for subscriberID, subscriber := range r.peers {
		if subscriberID == sourceID {
			continue
		}

		relayTrack, err := webrtc.NewTrackLocalStaticRTP(
			remoteTrack.Codec().RTPCodecCapability,
			"audio",
			sourceID, // label 为源 clientID，接收端据此识别声音来源
		)
		if err != nil {
			log.Printf("[sfu] 创建中继轨失败: source=%s sub=%s err=%v",
				sourceID[:8], subscriberID[:8], err)
			continue
		}

		if _, err := subscriber.PC.AddTrack(relayTrack); err != nil {
			log.Printf("[sfu] 添加中继轨失败: source=%s sub=%s err=%v",
				sourceID[:8], subscriberID[:8], err)
			continue
		}

		subscriber.outgoingRelays[sourceID] = relayTrack
		log.Printf("[sfu] 中继已添加: %s -> %s", sourceID[:8], subscriberID[:8])
	}

	// 为当前 source 创建其他已有客户端的中继音轨。
	sourcePeer := r.peers[sourceID]
	for otherID, otherPeer := range r.peers {
		if otherID == sourceID || otherPeer.incomingTrack == nil {
			continue
		}

		relayTrack, err := webrtc.NewTrackLocalStaticRTP(
			otherPeer.incomingTrack.Codec().RTPCodecCapability,
			"audio",
			otherID,
		)
		if err != nil {
			log.Printf("[sfu] 为新对端创建中继轨失败: source=%s other=%s err=%v",
				sourceID[:8], otherID[:8], err)
			continue
		}

		if _, err := sourcePeer.PC.AddTrack(relayTrack); err != nil {
			log.Printf("[sfu] 为新对端添加中继轨失败: source=%s other=%s err=%v",
				sourceID[:8], otherID[:8], err)
			continue
		}

		sourcePeer.outgoingRelays[otherID] = relayTrack
		log.Printf("[sfu] 中继已添加(新对端): %s <- %s", sourceID[:8], otherID[:8])
	}

	r.lock.Unlock()

	// 启动 RTP 转发协程：从 remoteTrack 读取 RTP 包，写入所有中继音轨。
	go func() {
		log.Printf("[sfu] 中继协程已启动: source=%s", sourceID[:8])
		defer log.Printf("[sfu] 中继协程已停止: source=%s", sourceID[:8])

		for {
			packet, _, err := remoteTrack.ReadRTP()
			if err != nil {
				log.Printf("[sfu] 读取 RTP 失败: source=%s err=%v", sourceID[:8], err)
				return
			}

			// 获取当前订阅者列表（可能因加入/离开而变化）。
			r.lock.RLock()
			relays := make([]*webrtc.TrackLocalStaticRTP, 0, len(r.peers))
			if peer, ok := r.peers[sourceID]; ok {
				for _, relay := range peer.outgoingRelays {
					relays = append(relays, relay)
				}
			}
			r.lock.RUnlock()

			for _, relay := range relays {
				if err := relay.WriteRTP(packet); err != nil {
					log.Printf("[sfu] 写入 RTP 失败: source=%s err=%v", sourceID[:8], err)
				}
			}
		}
	}()
}

// stopForwarding 关闭转发协程，确保协程退出。
func (p *SFUPeer) stopForwarding() {
	select {
	case <-p.stopRelay:
		// 已关闭。
	default:
		close(p.stopRelay)
	}
}

// CleanupRoom 关闭房间内所有 PeerConnection 并删除房间。
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
