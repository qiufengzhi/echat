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
	"context"
	"echat-backend/asr_cli"
	"echat-backend/config"
	"echat-backend/sfu/bwe"
	"echat-backend/dataplane"
	"echat-backend/logging"
	"echat-backend/observability"
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
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

var logger = logging.New("sfu")

// asrQueueCapacity 每个发布者的待识别 RTP 队列容量，50 包 × 20ms = 1 秒音频
//
// 队列只吸收解码与投递的瞬时抖动，不承担积压：满了丢最老，语音以最新内容为准
const asrQueueCapacity = 50

// relayQueueCapacity 每个订阅者方向的 RTP 转发队列容量，50 包 × 20ms = 1 秒音频
//
// 发布者把包丢进队列就继续读下一包（Push 永不阻塞），队列满了挤掉最老的包。
// 谁的网络慢，谁自己的队列积压、丢自己的包，与其他订阅者完全隔离
const relayQueueCapacity = 50

// renegotiation 建Offer 的有界重试参数
//
// pion 的 negotiation-needed 标志在触发 OnNegotiationNeeded 回调之前就被置位，
// 此后只有两种情况会清除：判定"不再需要协商"，或信令状态回到 stable（answer
// 应用成功）。若 CreateOffer / SetLocalDescription 失败后直接放弃，且该 PC 恰好
// 再没有 answer 回来（没有已发出的 offer 就没有 answer），标志将永久卡死——
// 之后该 PC 上所有重协商请求都会被 pion 的 4.7.3.2.5 检查吞掉，表现为新轨
// 永远协商不上（某人开麦了但对方听不见 / AI 无声），且仅留一条 warn 日志。
// 因此失败必须有界重试，给瞬时错误（如 SDP 生成抖动）自愈机会
const (
	// renegotiationMaxAttempts 建 Offer 的最大尝试次数（首次 + 2 次重试）
	renegotiationMaxAttempts = 3
	// renegotiationRetryDelay 第 n 次重试前的基础等待，实际等待 = n × 该值
	renegotiationRetryDelay = 100 * time.Millisecond
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

	// stopHealth 停止健康监控协程的信号，房间移除时关闭
	stopHealth chan struct{}
	// healthOnce 保证 stopHealth 只关闭一次
	healthOnce sync.Once
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

	// 初始协商（sfu_offer -> sfu_answer）是否已完成。
	// 在此之前 PC 上没有任何 SDP，此时 AddTrack 会触发 pion 的 OnNegotiationNeeded
	// （pion 对空描述 PC 的 checkNegotiationNeeded 判定为"需要协商"），导致
	// renegotiation_offer 抢在初始协商前发出，打乱客户端信令状态机。
	// 置位点在 CompleteInitialNegotiation（sfu_answer 已入队上线之后），
	// 所有向 PC AddTrack 的路径都必须检查此标志；
	// 未协商对端由它自己 CompleteInitialNegotiation 里的 addExistingRelays 补建，
	// 不会漏轨
	negotiated atomic.Bool

	// 该客户端作为发布者时，向房间内其他客户端转发音频的管道表
	// 键是订阅者（接收方）的 clientID，值是一条独立转发管道（有界队列 + 专属写协程），
	// 每个订阅者一条：弱网订阅者只能拖满自己的队列，不会拖慢发布者和其他订阅者
	outgoingRelays map[string]*relayPipe

	// AI TTS 合成语音输出轨，写入后客户端通过 ontrack 收到
	// 由 getAITtsTrack 首次使用时懒创建并 AddTrack 到 PC
	// 使用 TrackLocalStaticSample（pion 自动管理 seq/ts/marker），而非手动构造 RTP 包
	aiTtsTrack *webrtc.TrackLocalStaticSample

	// aiTtsTrack 绑定的 RTPSender：时间轴冻结跨越回绕周期时，
	// rebuildAITtsTracks 需要先经它 RemoveTrack 旧轨再挂新轨
	aiTtsSender *webrtc.RTPSender

	// 在途 renegotiation Offer 的 SDP：OnNegotiationNeeded 发出 offer 前登记，
	// answer 应用成功后清除，AcceptRenegotiationAnswer 据此校验 offer/answer 配对。
	// pion 对远端 answer 不做与 pending offer 的匹配校验（本地侧 SetLocal 校验
	// lastOffer，远端侧没有对应物），配对正确性原本完全依赖 per-client FIFO
	// 信令与浏览器隐式 rollback 这两个隐性约定，信令一旦乱序/重发即静默错配。
	// 由 r.lock 保护（写点在 ops 链回调与 AcceptRenegotiationAnswer，均为短临界区）
	pendingOfferSDP string

	// 停止中继转发协程的信号，在客户端离开或断连时关闭
	stopRelay chan struct{}

	// srClock 本对端出流 SR 的发送时刻表，用于从 RR 反算 RTT
	// 与拦截器链共享同一实例，生命周期随 PeerConnection
	srClock *srClock

	// bwe 本对端的带宽估计器与整形器，配置关闭时为 nil
	// 每个 PeerConnection 一份：一条 UDP 链路共用一份带宽预算
	bwe *bwe.Estimator

	// health 本对端的健康状态机，由房间级协程统一驱动，bwe 关闭时为 nil
	health *bwe.Tracker

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
		ID:         roomID,
		peers:      make(map[string]*SFUPeer),
		stopHealth: make(chan struct{}),
	}
	s.rooms[roomID] = room
	// 房间级健康监控：统一驱动房间内所有对端的状态机，避免每对端一个定时器
	go room.healthLoop()
	// 识别结果按房间分发：订阅随 SFU 房间创建建立，随房间移除关闭
	GlobalASRDispatcher.subscribe(roomID)
	observability.RecordRooms(len(s.rooms))
	logger.Infow("房间已创建", "roomID", roomID)
	return room
}

// RemoveRoom 删除 SFU 房间。调用方应在确认房间内无活跃 peer 后调用
func (s *SFUServer) RemoveRoom(roomID string) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if room, ok := s.rooms[roomID]; ok {
		delete(s.rooms, roomID)
		// 停掉房间级健康监控协程，避免房间删除后仍在轮询
		room.healthOnce.Do(func() {
			if room.stopHealth != nil {
				close(room.stopHealth)
			}
		})
		// 一并移除识别结果订阅，否则订阅者与其队列会随房间数累积
		GlobalASRDispatcher.unsubscribe(roomID)
		observability.RecordRooms(len(s.rooms))
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
	// SR 时刻表必须注册在 ConfigureRTCPReports 之前（即处于其内层）：
	// report sender 发 SR 时用的是它自己内层的 writer，注册在它外层永远看不到 SR，
	// 于是 RR 里的 LSR 无对应记录，RTT 恒为空
	clock := newSRClock()
	registry.Add(newSRClockFactory(clock))
	if err := webrtc.ConfigureRTCPReports(registry); err != nil {
		return fmt.Errorf("configure rtcp reports: %w", err)
	}
	if err := webrtc.ConfigureTWCCSender(mediaEngine, registry); err != nil {
		return fmt.Errorf("configure twcc sender: %w", err)
	}
	// 下行拥塞控制：估计器 + 有界整形器，每个 PeerConnection 一份
	// 必须注册在 transport-wide 序号拦截器之前（内层），保证序号在入队前写好
	var estimator *bwe.Estimator
	var healthTracker *bwe.Tracker
	if sfuCfg.BWE.Enabled {
		factory := bwe.NewFactory(
			bwe.Config{
				MaxPackets:       sfuCfg.BWE.PacerMaxPackets,
				MaxBytes:         sfuCfg.BWE.PacerMaxBytes,
				MaxBitrate:       sfuCfg.BWE.MaxBitrate,
				InitialBitrate:   sfuCfg.BWE.InitialBitrate,
				OverheadPermille: sfuCfg.BWE.OverheadPermille,
			},
			sfuCfg.BWE.InitialBitrate,
			sfuCfg.BWE.MinBitrate,
			sfuCfg.BWE.MaxBitrate,
			func(e *bwe.Estimator) { estimator = e },
		)
		registry.Add(factory)
		// 状态机随对端创建，初始计入 healthy
		healthTracker = bwe.NewTracker(time.Now())
		observability.AdjustSubscriberHealth(string(bwe.StateHealthy), 1)
	}
	// 出向 transport-wide 序号：ConfigureTWCCSender 只注册了「用收到的包生成反馈」，
	// 不负责给出向包打序号，缺了它浏览器无法回传可用的 TWCC 反馈。
	// 必须最后注册（链中最外层），保证序号在任何队列化之前写入
	if err := webrtc.ConfigureTWCCHeaderExtensionSender(mediaEngine, registry); err != nil {
		return fmt.Errorf("configure twcc header extension: %w", err)
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
		outgoingRelays: make(map[string]*relayPipe),
		stopRelay:      make(chan struct{}),
		srClock:        clock,
		bwe:            estimator,
		health:         healthTracker,
		connectedAt:    time.Now(),
	}

	// 压测钩子：mock 识别器在轨协商时即登记会话，不依赖音频解码路径（非 mock 模式为空操作）
	asr_cli.NotifySession(peer.SessionID, r.ID, clientID)

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
	//
	// 并发与失败语义（依据 pion v4 源码 peerconnection.go / operations.go）：
	//  1. 本回调在 pion 每个 PC 一条的 ops 操作链 goroutine 上串行执行，
	//     两个 AddTrack 近同时触发时回调不会并发交错，无需额外互斥；
	//  2. offer 在途（have-local-offer）期间新增的协商请求会被 pion 挂起，
	//     并在 answer 使状态回到 stable 时自动重触发，因此这里不需要、也
	//     不应该对非 stable 态强行建 Offer；
	//  3. 唯一需要兜底的是建 Offer 失败：negotiation-needed 标志在回调触发前
	//     已置位，若失败即放弃且此后再无 answer 回来，标志永久卡死、该 PC 的
	//     后续协商全部被吞（详见 renegotiationMaxAttempts 处注释），故有界重试
	pc.OnNegotiationNeeded(func() {
		if r.onRenegotiation == nil {
			logger.Warnw("renegotiation 触发但无回调", "clientID", clientID[:8])
			return
		}
		for attempt := 1; attempt <= renegotiationMaxAttempts; attempt++ {
			// pion 仅在 stable 态触发本回调，但重试退避期间状态可能被并发迁移
			// （如客户端主动 sfu_offer 抢先、触发隐式 rollback），非 stable 时
			// 放弃本轮，交给状态回到 stable 后 pion 的自动重触发兜底
			if state := pc.SignalingState(); state != webrtc.SignalingStateStable {
				logger.Warnw("renegotiation 状态非 stable，放弃本轮建 Offer",
					"clientID", clientID[:8], "state", state.String())
				return
			}
			offer, err := pc.CreateOffer(nil)
			if err != nil {
				logger.Warnw("创建 renegotiation Offer 失败",
					"clientID", clientID[:8], "attempt", attempt, "error", err)
			} else if err = pc.SetLocalDescription(offer); err != nil {
				logger.Warnw("设置 renegotiation Offer 失败",
					"clientID", clientID[:8], "attempt", attempt, "error", err)
			} else {
				// 在途 offer 登记必须先于信令发送：answer 只可能在客户端收到
				// offer 之后到达，先登记才能保证 AcceptRenegotiationAnswer
				// 校验时 pendingOfferSDP 必已就位。临界区只写一个字符串字段，
				// 不触碰 PC，与其他 goroutine 无锁交叉
				r.lock.Lock()
				peer.pendingOfferSDP = offer.SDP
				r.lock.Unlock()
				logger.Debugw("renegotiation Offer 已创建", "clientID", clientID[:8])
				observability.IncRenegotiation()
				r.onRenegotiation(clientID, offer.SDP)
				return
			}
			if attempt < renegotiationMaxAttempts {
				// 线性退避后重试。回调运行在 ops 链 goroutine 上，退避会短暂
				// 排队同 PC 的后续 ops（此刻 offer 未发出、无 answer 在途，
				// 排队的只有 startRTP 之类的媒体启动操作），开销可接受；
				// 不另起 goroutine 重试，是为了避免重试与下一轮协商并发执行、
				// 重新引入 pion 已串行化掉的 SetLocalDescription 交错竞态
				time.Sleep(time.Duration(attempt) * renegotiationRetryDelay)
			}
		}
		logger.Warnw("renegotiation 建 Offer 连续失败，放弃本轮（等待下次触发或 answer 回 stable 后由 pion 重试）",
			"clientID", clientID[:8], "attempts", renegotiationMaxAttempts)
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
			// 连接意外断开：记一次断连事件后清理转发资源
			observability.RecordPeerDisconnect(state.String())
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
	observability.RecordPeers(r.peerCountLocked())
	logger.Infow("对端加入", "clientID", clientID[:8], "roomID", r.ID)
	return nil
}

// peerCountLocked 在已持锁（r.lock 写锁）前提下返回对端数，供埋点使用
//
// return: 当前对端数
func (r *SFURoom) peerCountLocked() int {
	return len(r.peers)
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

	// 注意：此处故意不做 negotiated 置位、也不补建中继轨。
	// sfu_answer 尚未由信令层发回客户端，若此时 AddTrack 触发 OnNegotiationNeeded，
	// renegotiation_offer 可能在 sfu_answer 之前入队送达，客户端在 have-local-offer
	// 状态先收到 offer 会导致浏览器隐式 rollback，随后迟到的 sfu_answer 失配，
	// 初始协商被自己的补建动作打乱。两者统一收口在 CompleteInitialNegotiation，
	// 由信令层在 sfu_answer 入队之后调用
	return answer.SDP, nil
}

// CompleteInitialNegotiation 标记对端初始协商完成，并为晚加入者补建房间内
// 已有发布者的中继音轨
//
// 调用时机（关键约束）：必须在该客户端的 sfu_answer 已经 Enqueue 上线之后。
// 出站队列（Enqueue）按入队顺序 FIFO 发送，只要置位发生在 answer 入队之后，
// 客户端必然先处理完 sfu_answer（回到 stable 态），再收到任何 renegotiation_offer。
// 在此之前 negotiated 保持 false，startForwarding / getAITtsTrack 等所有
// AddTrack 路径都会跳过该对端——这就把「一切 AddTrack 都发生在 sfu_answer
// 上线之后」做成了结构性保证，而不是依赖各调用点的自觉。
//
// 若客户端一直不开麦（自己的 OnTrack 永远不触发），没有这里的补建它将
// 永远听不到先加入者，因此补建是必要路径而非优化
func (r *SFURoom) CompleteInitialNegotiation(clientID string) {
	r.lock.RLock()
	peer, ok := r.peers[clientID]
	r.lock.RUnlock()
	if !ok {
		// 对端已离开（Leave 删除 peer），无事可做
		return
	}

	// 置位后该对端对所有 AddTrack 路径可见；本方法内部的补建是第一笔
	peer.negotiated.Store(true)
	r.addExistingRelays(clientID)
}

// addExistingRelays 为刚完成初始协商的客户端补建房间内已有发布者的中继音轨
//
// 背景：startForwarding 由发布者的 OnTrack 事件驱动，且只遍历"当下"房间内的对端。
// 若客户端 C 在 A、B 已发布音频之后才加入，且 C 自己永不开麦（没有 OnTrack），
// 原实现中不会有任何事件再触发 startForwarding，C 的 PeerConnection 上将没有任何
// 下行音轨——C 成为"聋子"。本方法在 C 的初始协商完成后主动扫一遍房间，
// 把每个已有发布者的音轨以中继轨的形式 AddTrack 到 C 的 PC 上，补上这条缺失的通路。
//
// 并发与锁的考虑：
//  1. AddTrack 可能触发 ICE / negotiation 回调（见 OnTrack 处的注释），
//     因此绝不能在持有房间写锁时调用 AddTrack——先在读锁内做快照，锁外执行 AddTrack；
//  2. forwardRtp 每个包都会在读锁内遍历 outgoingRelays，所以中继轨注册必须拿写锁，
//     且注册放在 AddTrack 成功之后，保证"先有轨、再入表"，不会写入空转发的轨道；
//  3. 幂等：若目标中继已存在（例如信令层重发了 Offer），直接跳过，避免同一来源的
//     音轨被重复 AddTrack 导致客户端收到重复的媒体描述。
func (r *SFURoom) addExistingRelays(clientID string) {
	// 第一步：读锁内做快照——加入者的 peer、所有"已发布且尚未为本人建过中继"的对端
	r.lock.RLock()
	peer, ok := r.peers[clientID]
	if !ok {
		r.lock.RUnlock()
		return
	}
	type publisher struct {
		id    string
		peer  *SFUPeer
		track *webrtc.TrackRemote
	}
	var publishers []publisher
	for otherID, otherPeer := range r.peers {
		if otherID == clientID || otherPeer.publishedAudioTrack == nil {
			continue
		}
		// 该对端已经在为自己转发音频（正常发布事件路径已覆盖），无需重复建轨
		if _, exists := otherPeer.outgoingRelays[clientID]; exists {
			continue
		}
		publishers = append(publishers, publisher{id: otherID, peer: otherPeer, track: otherPeer.publishedAudioTrack})
	}
	r.lock.RUnlock()

	// 第二步：锁外为每个已有发布者创建中继轨并 AddTrack 到本人的 PC。
	// 这与 startForwarding 第二个循环的建轨方式完全一致：
	// 编码能力沿用发布者实际使用的 codec，label 保持 "audio_<userId>" 格式，
	// 前端据此识别声音来源
	for _, p := range publishers {
		relayTrack, err := webrtc.NewTrackLocalStaticRTP(
			p.track.Codec().RTPCodecCapability,
			"audio",
			"audio_"+p.id,
		)
		if err != nil {
			logger.Warnw("晚加入者补建中继轨失败", "clientID", clientID[:8], "source", p.id[:8], "error", err)
			continue
		}
		snd, err := peer.PC.AddTrack(relayTrack)
		if err != nil {
			logger.Warnw("晚加入者补建中继添加失败", "clientID", clientID[:8], "source", p.id[:8], "error", err)
			continue
		}
		// 补建轨同样要读 RTCP：否则晚加入者的这条出流永远没有抖动/丢包/RTT 观测
		go r.readSenderRTCP(clientID, "relay", peer.srClock, snd)

		// 第三步：写锁内把中继轨注册到发布者的 outgoingRelays。
		// 注册后 forwardRtp 的每包循环会自动拾取，无需额外通知
		r.lock.Lock()
		p.peer.outgoingRelays[clientID] = r.newRelayPipe(p.id, clientID, relayTrack)
		r.lock.Unlock()
		logger.Infow("晚加入者中继已补建", "from", p.id[:8], "to", clientID[:8])
	}
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
	// 对端离开时把它从当前健康状态的计数里摘掉，避免计数随进出累积漂移
	if peer.health != nil {
		observability.AdjustSubscriberHealth(string(peer.health.State()), -1)
	}
	observability.RecordPeers(r.peerCountLocked())
	// 该客户端作为发布者的转发管道全部关闭，唤醒对应写协程退出；
	// 写协程只写自己的中继轨、不取房间锁，锁内关闭无死锁风险。
	// peer 马上要从 r.peers 摘除，forwardRtp 退出时的 closeOutgoingRelays
	// 将找不到它，所以必须在这里收口
	for _, pipe := range peer.outgoingRelays {
		pipe.queue.Close()
	}
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
	observability.ClearVoice(sessionID)

	// 从其他所有客户端的中继表中移除当前客户端的音轨
	r.lock.Lock()
	for _, otherPeer := range r.peers {
		if pipe, ok := otherPeer.outgoingRelays[clientID]; ok {
			delete(otherPeer.outgoingRelays, clientID)
			// 订阅者走了，关掉这条管道，写协程随 Pop 排空 + Closed 退出
			pipe.queue.Close()
		}
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
//
// 配对校验：pion 对远端 answer 不做与 pending offer 的匹配检查（对比本地侧
// SetLocalDescription(offer) 会严格校验 SDP == lastOffer），只要信令状态是
// have-local-offer 就静默接受任何 answer，错配的 offer/answer 会被拼成
// 自相矛盾的 current 描述且无报错。此处以「mid 序列」为配对指纹拦截：
// W3C 规范保证 answer 的 m= 段顺序与 mid 沿用其所应答 offer 的布局，
// 两序列不一致即 answer 属于更早/更晚的 offer（信令乱序、重发或客户端
// 状态机错乱），拒绝应用。局限：若先后两笔 offer 的 mid 序列恰好相同
// （如仅 ICE restart 类无轨变化的再协商），指纹无法区分——但这类错配
// 功能上也等价，可安全接受
func (r *SFURoom) AcceptRenegotiationAnswer(clientID string, answerSDP string) error {
	r.lock.RLock()
	peer, ok := r.peers[clientID]
	r.lock.RUnlock()
	if !ok {
		return fmt.Errorf("peer %s not found", clientID)
	}

	// 读在途 offer 快照。此刻状态必为 have-local-offer（pion 只在该状态接受
	// answer），状态回 stable 前不可能有新 offer 覆写 pendingOfferSDP，
	// 因此快照在校验与 SetRemote 期间保持有效
	r.lock.RLock()
	pendingOfferSDP := peer.pendingOfferSDP
	r.lock.RUnlock()
	if pendingOfferSDP == "" {
		return fmt.Errorf("renegotiation answer 无对应在途 offer（信令乱序或重复）")
	}
	offerMids := midSequenceFromSDP(pendingOfferSDP)
	answerMids := midSequenceFromSDP(answerSDP)
	if !equalStringSlices(offerMids, answerMids) {
		return fmt.Errorf("renegotiation answer 与在途 offer 不匹配: offer mids=%v, answer mids=%v",
			offerMids, answerMids)
	}

	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  answerSDP,
	}
	if err := peer.PC.SetRemoteDescription(answer); err != nil {
		return fmt.Errorf("set remote description: %w", err)
	}

	// 协商闭环，摘掉在途标记。条件清除：SetRemote 成功到拿到写锁之间，
	// ops 链上可能已有一笔新 offer（stable 后自动重触发）覆写 pendingOfferSDP，
	// 此时不能误清新 offer 的在途标记
	r.lock.Lock()
	if peer.pendingOfferSDP == pendingOfferSDP {
		peer.pendingOfferSDP = ""
	}
	r.lock.Unlock()

	logger.Infow("renegotiation answer 已处理", "clientID", clientID[:8])
	return nil
}

// midSequenceFromSDP 按出现顺序提取 SDP 中的全部 a=mid 值
//
// SDP 行分隔符兼容 \r\n 与 \n（逐行 Trim 空白即可）；mid 序列即 answer 的
// 媒体段布局指纹（详见 AcceptRenegotiationAnswer 注释）
func midSequenceFromSDP(sdp string) []string {
	var mids []string
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "a=mid:"); ok {
			mids = append(mids, strings.TrimSpace(rest))
		}
	}
	return mids
}

// equalStringSlices 判断两个字符串切片长度与逐元素是否完全一致
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

// SessionIDOf 返回对端的会话标识，对端不存在时返回空串
//
// clientID: 对端标识
// return: 会话标识
func (r *SFURoom) SessionIDOf(clientID string) string {
	r.lock.RLock()
	defer r.lock.RUnlock()
	if p, ok := r.peers[clientID]; ok {
		return p.SessionID
	}
	return ""
}

// startForwarding 在收到客户端音频轨后，创建中继音轨并开始转发
//
// 转发逻辑：
//  1. source 的音频到达 SFU，通过 OnTrack 收到 remoteTrack
//  2. 为房间内每个其他客户端创建一个 TrackLocalStaticRTP（中继音轨），
//     添加到该客户端的 PeerConnection 中
//  3. 同时也为 source 创建其他已有客户端的中继音轨，让 source 能听到其他人
//  4. 启动协程持续读取 source 的 RTP 包，投入各订阅者的转发队列，
//     由每个订阅者的专属写协程写出，订阅者之间互不拖累
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
		// 初始协商尚未完成的对端必须跳过：其 PC 上还没有任何 SDP，此时 AddTrack
		// 会立即触发 OnNegotiationNeeded，renegotiation_offer 会抢在它自己的
		// sfu_offer/sfu_answer 之前到达，打乱客户端信令状态机（浏览器会隐式
		// rollback，随后迟到的 sfu_answer 与已回滚的 offer 对不上）。
		// 这些对端不会漏轨：它完成初始协商时，addExistingRelays 会把 source
		// 的音轨（此时 publishedAudioTrack 已就绪）补建给它
		if !subscriber.negotiated.Load() {
			logger.Debugw("跳过未完成初始协商的对端，待其协商完成后补建", "subscriber", subscriberID[:8])
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

		snd, err := subscriber.PC.AddTrack(relayTrack)
		if err != nil {
			logger.Warnw("添加中继轨失败", "source", sourceID[:8], "subscriber", subscriberID[:8], "error", err)
			continue
		}
		go r.readSenderRTCP(subscriberID, "relay", subscriber.srClock, snd)

		// 将房间内其他人的音轨通过中继轨加入 source
		sourcePeer.outgoingRelays[subscriberID] = r.newRelayPipe(sourceID, subscriberID, relayTrack)
		logger.Infow("中继已添加", "from", sourceID[:8], "to", subscriberID[:8])
	}

	// 为当前 source 创建其他已有客户端的中继音轨，让 source 能听到其他人
	// 注意：若 source 是"晚加入"的客户端，其下行中继轨可能已由 addExistingRelays
	// 在初始协商后补建过（甚至 first loop 也可能已覆盖），此处必须检查幂等，
	// 否则会把同一来源的音轨重复 AddTrack 到 source 的 PC 上
	for otherID, otherPeer := range r.peers {
		if otherID == sourceID || otherPeer.publishedAudioTrack == nil {
			continue
		}
		// 该对端的音频已在转发给 source，跳过，避免重复建轨
		if _, exists := otherPeer.outgoingRelays[sourceID]; exists {
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

		snd, err := sourcePeer.PC.AddTrack(relayTrack)
		if err != nil {
			logger.Warnw("为新对端添加中继轨失败", "source", sourceID[:8], "other", otherID[:8], "error", err)
			continue
		}
		go r.readSenderRTCP(sourceID, "relay", sourcePeer.srClock, snd)

		// 将 other 的音频转发给 source
		otherPeer.outgoingRelays[sourceID] = r.newRelayPipe(otherID, sourceID, relayTrack)
		logger.Infow("中继已添加(新对端)", "from", otherID[:8], "to", sourceID[:8])
	}

	r.lock.Unlock()

	// 启动 RTP 转发协程：从 remoteTrack 读取 RTP 包，写入所有中继音轨。将本人的音频转发给房间内其他客户端
	go r.forwardRtp(sourceID, sessionID, remoteTrack)
}

// getAITtsTrack 懒初始化 AI TTS 输出轨，返回该客户端的 AI 语音播放通道
// 首次调用时创建 TrackLocalStaticSample 并 AddTrack 到 PeerConnection，触发重协商
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
	// 对端初始协商未完成时不建轨：与 startForwarding 的理由相同，避免 AddTrack
	// 在空描述 PC 上触发提前的 renegotiation_offer。writeAllPeers 每帧（20ms）都会
	// 重试取轨，对端协商完成后的下一帧会自然建轨，音频只延迟不到一帧
	if !peer.negotiated.Load() {
		return nil, fmt.Errorf("getAITtsTrack: 客户端 %s 初始协商未完成", clientID[:8])
	}

	track, err := r.bindAITtsTrack(peer, clientID)
	if err != nil {
		return nil, fmt.Errorf("getAITtsTrack: %w", err)
	}
	logger.Infow("AI TTS 音轨已创建", "clientID", clientID[:8])

	// AddTrack 已自动触发 OnNegotiationNeeded 回调（见 CreateSFUPeerConn 中的注册）
	// 该回调内完成 CreateOffer + SetLocalDescription + 信令通知，此处无需重复处理

	return track, nil
}

// bindAITtsTrack 为 peer 创建一条全新的 AI TTS 音轨并 AddTrack 到其 PC，
// 替换 peer 上可能存在的旧轨引用（调用方须先自行摘除旧轨）。
// 供 getAITtsTrack（首次懒创建）与 rebuildAITtsTracks（跨回绕重建）复用：
// 两者差异只在「是否先 RemoveTrack 旧轨」，建轨与绑定流程完全一致
// 调用方必须持有 r.lock
func (r *SFURoom) bindAITtsTrack(peer *SFUPeer, clientID string) (*webrtc.TrackLocalStaticSample, error) {
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"audio",
		"audio_ai_"+clientID, // label 标示 AI 合成语音来源
	)
	if err != nil {
		return nil, fmt.Errorf("创建 AI TTS 音轨失败: %w", err)
	}
	snd, err := peer.PC.AddTrack(track)
	if err != nil {
		return nil, fmt.Errorf("添加 AI TTS 音轨到 PC 失败: %w", err)
	}
	// 读该出流的 RTCP，采集浏览器侧下行抖动、丢包与 RTT
	go r.readSenderRTCP(clientID, "ai", peer.srClock, snd)

	peer.aiTtsTrack = track
	peer.aiTtsSender = snd
	return track, nil
}

// rebuildAITtsTracks 重建房间内所有成员的 AI TTS 音轨：
// 旧轨从 PC 摘除（RemoveTrack），换上全新的 TrackLocalStaticSample——
// 新 RTPSender、新 SSRC、新时间轴，触发一次重协商
//
// 用途：AI 音轨时间轴冻结跨越 32 位 RTP 时间戳一个回绕周期（48kHz 下约 24.85h）后，
// 任何「前跳」增量取模后落点不可预测，同一 SSRC 的时间轴在数学上已无法接续，
// 接收端会持续判过期丢弃。重建是唯一出路：新 SSRC 对接收端是全新流，无播放历史
// 包袱，第一帧到达即恢复播放。前端 ontrack 对同一 streamId（audio_ai_<clientID>）
// 走「移除旧轨、挂新轨」的替换分支（useVoiceRoom.ts），客户端无需协议改动
//
// 调用方不得持有 r.lock（内部加写锁）
func (r *SFURoom) rebuildAITtsTracks() {
	r.lock.Lock()
	defer r.lock.Unlock()

	rebuilt := 0
	for clientID, peer := range r.peers {
		if peer.aiTtsTrack == nil {
			// 从未创建过 AI 轨的成员没有冻结的时间轴，无需重建
			continue
		}
		// 先摘旧轨：不摘的话旧轨永远留在 SDP 里，重协商后新旧两条 AI 轨并存
		if peer.aiTtsSender != nil {
			if err := peer.PC.RemoveTrack(peer.aiTtsSender); err != nil {
				logger.Warnw("摘除旧AI TTS音轨失败", "clientID", clientID[:8], "error", err)
			}
			peer.aiTtsSender = nil
		}
		peer.aiTtsTrack = nil
		if _, err := r.bindAITtsTrack(peer, clientID); err != nil {
			logger.Warnw("重建AI TTS音轨失败", "clientID", clientID[:8], "error", err)
			continue
		}
		rebuilt++
	}
	logger.Infow("AI TTS 音轨已重建（时间轴冻结跨越回绕周期，换新SSRC恢复）",
		"rebuilt", rebuilt, "peers", len(r.peers))
}

// relayPipe 一条「发布者 → 某个订阅者」的 RTP 转发管道
//
// 设计动机：若发布者协程串行地给每个订阅者 WriteRTP，任何一个写操作变慢
// （例如对端瞬时拥塞）都会推迟同一包写给后续订阅者的时间，并反过来拖慢
// 发布者的 ReadRTP 节奏，导致全房间音频抖动。把「写给谁」拆成独立协程后，
// 订阅者之间完全解耦：弱网订阅者只能拖满自己这条队列，满后挤掉最老的包，
// 丢的是它自己的音频，别人不受影响
type relayPipe struct {
	// queue 有界环形队列，单生产者（发布者转发协程）单消费者（本管道写协程），
	// 满时挤掉最老元素——对实时语音来说，旧包过期作废，最新的才是能听懂的
	queue *dataplane.Ring[*rtp.Packet]
	// track 该订阅者的中继轨，仅由本管道的写协程写入，无并发
	track *webrtc.TrackLocalStaticRTP
}

// relayQueueSize 返回中继管道队列容量
//
// 从配置读取，未配置或非法时回退到编译期默认值
// return: 队列容量，至少为 1
func relayQueueSize() int {
	size := config.Get().SFU.BWE.RelayQueueCapacity
	if size <= 0 {
		size = relayQueueCapacity
	}
	return size
}

// newRelayPipe 创建转发管道并启动该订阅者专属的写协程
//
// 调用点（startForwarding / addExistingRelays）都持有房间写锁；
// 启动协程本身不取房间锁，不会造成锁递归
// sourceID: 发布者（音频来源）；subscriberID: 订阅者（接收方）；track: 订阅者的中继轨
// return: 已挂上写协程的转发管道
func (r *SFURoom) newRelayPipe(sourceID, subscriberID string, track *webrtc.TrackLocalStaticRTP) *relayPipe {
	pipe := &relayPipe{
		queue: dataplane.NewRing[*rtp.Packet]("sfu.relay."+sourceID+"."+subscriberID, relayQueueSize()),
		track: track,
	}
	go pipe.pumpRelay(sourceID, subscriberID)
	return pipe
}

// pumpRelay 订阅者专属写协程：从管道取包写入中继轨，直到管道关闭
//
// 写失败（对端连接已关、PC 正在关闭等）只计数告警，不退出——
// 协程的退出统一由队列 Close 驱动，避免两套退出条件打架
// sourceID: 发布者；subscriberID: 订阅者
func (p *relayPipe) pumpRelay(sourceID, subscriberID string) {
	defer logger.Infow("中继写协程已停止", "source", sourceID[:8], "dest", subscriberID[:8])
	for {
		pkt, ok := p.queue.Pop()
		if !ok {
			if p.queue.Closed() {
				return
			}
			<-p.queue.Wait()
			continue
		}
		if err := p.track.WriteRTP(pkt); err != nil {
			observability.RelayWriteErrors.Add(context.Background(), 1)
			logger.Warnw("写入 RTP 失败", "source", sourceID[:8], "dest", subscriberID[:8], "error", err)
		} else {
			// 记录实际写出的字节数，用来区分「发少了」和「丢多了」
			observability.RecordTxBytes("relay", int64(pkt.Header.MarshalSize()+len(pkt.Payload)))
		}
	}
}

// closeOutgoingRelays 关闭发布者的全部转发管道，回收其写协程
//
// 在 forwardRtp 退出（读端结束）时调用，覆盖发布者离开、PC 断开等所有收尾路径；
// 与 Leave 中按订阅者逐条关闭并发执行，Ring.Close 幂等，重复关闭安全
// sourceID: 发布者
func (r *SFURoom) closeOutgoingRelays(sourceID string) {
	r.lock.RLock()
	var pipes []*relayPipe
	if peer, ok := r.peers[sourceID]; ok {
		for _, pipe := range peer.outgoingRelays {
			pipes = append(pipes, pipe)
		}
	}
	r.lock.RUnlock()
	for _, pipe := range pipes {
		pipe.queue.Close()
	}
}

// cloneRTPPacket 深拷贝一个 RTP 包
//
// forwardRtp 拿到的包，其 payload 底层缓冲会被下一次 ReadRTP 复用，
// 包又要交给各订阅者的写协程异步消费，必须完整复制一份才能安全跨协程
// src: 源包
// return: 深拷贝出的新包
func cloneRTPPacket(src *rtp.Packet) *rtp.Packet {
	pkt := *src
	// Extensions 是切片，值拷贝只复制了切片头，底层仍共享，须单独复制
	pkt.Header.Extensions = append([]rtp.Extension(nil), src.Header.Extensions...)
	pkt.Payload = append([]byte(nil), src.Payload...)
	return &pkt
}

// forwardRtp 接收到客户端的语音包，从 remoteTrack 读取 RTP 包，转发给房间其他客户端，同时送阿里云 ASR 做语音识别
// sessionID 由调用方从对端读取，保证同一会话在 ASR、LLM、TTS 三段用同一个标识
// 转发采用「每订阅者一条管道」：本协程只负责读包、拷贝、入队（永不阻塞），
// 实际写出由各订阅者的专属写协程完成，订阅者之间互不拖累
func (r *SFURoom) forwardRtp(sourceID string, sessionID string, remoteTrack *webrtc.TrackRemote) {
	logger.Infow("中继协程已启动", "source", sourceID[:8])
	defer logger.Infow("中继协程已停止", "source", sourceID[:8])
	// 读端结束时关闭本发布者的所有订阅者管道，唤醒对应写协程退出
	defer r.closeOutgoingRelays(sourceID)
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

		// 转发 RTP 给房间内所有客户端：
		// 读锁内只做一件事——把当前订阅者的管道快照出来，绝不在锁内写媒体；
		// 之后逐条 Push 进各自的队列（Push 永不阻塞），本协程的每包耗时
		// 与任何订阅者的网络状况彻底无关
		r.lock.RLock()
		var pipes []*relayPipe
		if peer, ok := r.peers[sourceID]; ok {
			for _, pipe := range peer.outgoingRelays {
				pipes = append(pipes, pipe)
			}
		}
		r.lock.RUnlock()

		for _, pipe := range pipes {
			// 每个订阅者独立深拷贝一份：包的底层缓冲会被下一次 ReadRTP 复用，
			// 且各写协程并发消费各自那份，互不干扰
			pipe.queue.Push(cloneRTPPacket(rtpPacket))
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
			observability.OpusDecodeErrors.Add(context.Background(), 1)
			logger.Warnw("解码失败", "source", sourceID[:8], "error", decErr)
			continue
		}
		if n == 0 {
			continue // DTX 静音包，解码后没有有效采样
		}

		// 本地能量判停：记录最后一帧有效语音的时刻，供 asr_final_delay 近似测量
		if observability.HasVoice(pcm[:n]) {
			observability.MarkVoiceFrame(sessionID)
		}

		// 非阻塞投递：识别消费端繁忙时按背压丢弃，丢弃由 asr_cli 计数
		if asr_cli.Push(&asrpb.AudioChunk{
			SessionId:  sessionID,
			RoomId:     r.ID,
			ClientId:   sourceID,
			Pcm:        int16ToLEBytes(pcm[:n]),
			SampleRate: asrSampleRate,
		}) {
			observability.AddASRAudioSeconds(int64(n) * 1000 / int64(asrSampleRate))
		}
	}
}

// queuedFrame 一帧编码完成的 Opus 数据，等待按实时速率发出
type queuedFrame struct {
	// payload 编码后的 Opus 数据
	payload []byte
	// turn 该帧编码时的有效轮次，用于播放态记录
	turn string
	// sentence 该帧编码时所在的轮内句子序号，用于播出前缀裁剪
	sentence int32
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

	var pcmBuf []int16        // PCM 采样缓冲区，用于帧对齐（TTS 输出可能不对齐 20ms 帧边界）
	var totalPCM int          // 累计收到的 TTS PCM 字节数（16kHz 原始）
	var framesEncoded int     // 累计编码发送的 Opus 帧数
	var lastTurn string       // 最近输出过音频的轮次，轮次切换即本轮首个音频块
	var curSentence int32     // 最近音频块所在的轮内句子序号，随音频事件刷新
	var pendingTurn string    // 首个编码帧尚未记录的轮次
	var turnAudioAt time.Time // 该轮首块音频到达时刻，用于 first_audio_encode

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

	// 登记播放态：pacer 每帧发出时刷新，识别结果分发侧据此判定「AI 是否在出声」
	// 会话的 TTS 循环退出时一并移除登记
	playback := registerPlayback(acr.sessionID)
	defer unregisterPlayback(acr.sessionID)

	// 空闲超时 timer：2s 无新音频到达视为句子间自然停顿，刷新累积的 rawBuf
	// 使用 time.NewTimer + Reset 而非 time.After，避免每次迭代创建新 timer 导致泄漏
	idleTimer := time.NewTimer(2 * time.Second)
	defer idleTimer.Stop()

	// 帧发送队列：解耦编码（快速）与发送（20ms/帧 实时速率）
	// TTS 合成速度远超实时播放速度，若瞬间发送所有帧，Chrome NetEq 抖动缓冲区会溢出
	// 导致中间帧被丢弃——表现为只听到句子开头和末尾
	// 队列容量 2000 帧 = 40 秒缓冲，足以容纳最长 TTS 回复
	frameQueue := make(chan queuedFrame, 2000)

	// 预编码一帧静音（960 个 0 采样 @48kHz）：句间停顿/等待下一句时由 pacer 补发
	// 若不补发，停顿期间无帧发出，RTP 时间戳（按 Duration 累加）随之冻结，
	// 真实存在的几百毫秒静默在媒体时间轴上「不存在」，接收端会把前后两句话
	// 无缝拼接在一起——听感表现为句子黏连、语速忽快
	// 用独立编码器一次性编码后复用 payload：pacer 每 20ms 就要用一次，
	// 预编码避免高频加锁访问共享编码器 enc；DTX 已在 preset 中关闭，
	// 得到的是普通静音帧而非 DTX 标记包，接收端可稳定解码为静音
	// 编码失败时置 nil，退化为旧行为（停顿期间不发包）
	var silencePayload []byte
	if silEnc, silErr := newOpusEncoderPreset(); silErr != nil {
		logger.Warnw("创建静音帧编码器失败，句间停顿将不补发静音帧", "source", acr.clientId[:8], "error", silErr)
	} else {
		silencePayload, silErr = silEnc.Encode(make([]int16, opusEncoderFrameSamples))
		_ = silEnc.Close()
		if silErr != nil {
			logger.Warnw("编码静音帧失败，句间停顿将不补发静音帧", "source", acr.clientId[:8], "error", silErr)
			silencePayload = nil
		}
	}

	// pacedSender 以实时速率从队列取帧并发送，避免 NetEq 缓冲区溢出
	senderDone := make(chan struct{})
	go func() {
		defer close(senderDone)

		// errLogCounts 按「消息|成员」累计同类错误次数，用于日志限流：
		// writeAllPeers 由 pacer 每 20ms 调用一次，成员 PC 异常（写轨失败/音轨缺失）
		// 时若无差别打日志，最坏每秒 50 条洪泛。规则：每个错误首次记明细，
		// 之后每累计 100 次（约 2s）汇总一条带 count 的日志；对应操作成功一次
		// 即清零计数，同一成员后续新发的错误仍能拿到首条明细
		// map 只被 pacer 协程读写，无需加锁
		errLogCounts := make(map[string]int)
		logTrackErr := func(destID, msg string, err error) {
			key := msg + "|" + destID
			errLogCounts[key]++
			if c := errLogCounts[key]; c == 1 || c%100 == 0 {
				logger.Warnw(msg, "dest", destID[:8], "count", c, "error", err)
			}
		}
		clearTrackErr := func(destID, msg string) {
			delete(errLogCounts, msg+"|"+destID)
		}

		// writeAllPeers 把一帧 payload 写给房间内每个当前成员各自独立的 AI 音轨
		// 每个成员音轨都是 TrackLocalStaticSample，WriteSample 由 pion packetizer/sequencer
		// 维护单调连续的 seq/ts，播放端 jitter buffer 才能正常解码
		// 若像之前那样混进说话者的麦克风中继轨，AI 帧的 seq/ts 与麦克风包各自起算，
		// 会被浏览器当成已过期的旧包丢弃，导致只有说话者本人（独立 AI 轨）能听到
		// 真实帧与静音帧走同一条写入路径，保证整个会话的 seq/ts 都在同一条时间轴上
		// dur 为该帧在媒体时间轴上占据的时长，正常帧恒为 20ms，
		// 仅恢复重锚定时用大 Duration 让时间轴前跳（见下方恢复分支注释）
		writeAllPeers := func(payload []byte, dur time.Duration) {
			r.lock.RLock()
			recipients := make([]string, 0, len(r.peers))
			for destID := range r.peers {
				recipients = append(recipients, destID)
			}
			r.lock.RUnlock()

			for _, destID := range recipients {
				destTrack, err := r.getAITtsTrack(destID)
				if err != nil {
					logTrackErr(destID, "获取AI TTS音轨失败", err)
					continue
				}
				clearTrackErr(destID, "获取AI TTS音轨失败")
				if err := destTrack.WriteSample(media.Sample{
					Data:               payload,
					Duration:           dur,
					PrevDroppedPackets: 0,
				}); err != nil {
					logTrackErr(destID, "写入AI TTS轨失败", err)
				} else {
					clearTrackErr(destID, "写入AI TTS轨失败")
					observability.RecordTxBytes("ai", int64(len(payload)))
				}
			}
		}

		sendTicker := time.NewTicker(20 * time.Millisecond)
		defer sendTicker.Stop()
		// started：是否已发出过真实音频帧。首帧之前不补静音，
		// 避免 TTS 循环刚启动就提前触发 AI 音轨的懒创建与重协商
		started := false

		// 静音补发上界：自上一真实帧起最多补 10s（500 帧 × 20ms）
		// 补发只服务「句间停顿/等待下一句」，现实中的间隔（LLM TTFB、分句间隙）
		// 远小于 10s；超时仍未等到真实帧，大概率上游已卡死（TTS WS 挂住、LLM 断流）
		// 无界补发的代价：CBR 32kbps 下每帧静音是满尺寸 payload，约 44kbps/成员
		// 的持续带宽随房间人数放大，且包一直流动会掩盖上游故障（无错误、无日志）
		// 触顶后停止补发、时间轴冻结：冻结期间黏连不会发生（接收端早已播空），
		// 但恢复播放必须先重锚定时间轴，见下方真实帧分支的「恢复重锚定」
		const maxSilenceFillFrames = 500
		// silenceRun 连续补发的静音帧计数，发真实帧时清零
		silenceRun := 0
		// lastMediaAt 最近一次向时间轴写入帧（真实或静音）的墙钟时刻，
		// 恢复重锚定时用它计算时间轴落后真实时间的差距
		var lastMediaAt time.Time
		// 房间级码率策略状态：编码器为全房间共享，降码率只能按房间做
		policy := &roomBitratePolicy{}
		// framesSinceBitrateCheck 距上次检查码率档位已发送的帧数
		framesSinceBitrateCheck := 0
		for {
			select {
			case <-sendTicker.C:
				// 每 5 秒按房间健康状态分布评估一次码率档位
				framesSinceBitrateCheck++
				if framesSinceBitrateCheck >= bitrateCheckEvery {
					framesSinceBitrateCheck = 0
					r.applyRoomBitrate(enc, policy)
				}
				select {
				case qf, ok := <-frameQueue:
					if !ok {
						// 队列已关闭且排空，发送完成
						return
					}
					started = true
					// 恢复重锚定：静音补发触顶停发后，时间轴冻结而真实时间继续流逝。
					// 此时若直接发真实帧，其时间戳只比上一帧前进 20ms——媒体上「准时」，
					// 真实时间里却迟到了冻结的整段时长。接收端 NetEq 的播放时钟在
					// PLC 扩音期间持续推进，会把这种「时间戳连续但到达大幅迟到」的包
					// 判为过期丢弃，音频无法恢复。
					// 先补写一帧大 Duration 的静音帧把时间轴前跳到墙上时钟再发真实帧：
					// 「静默期不发包、恢复时时间戳前跳」正是 DTX 的原生语义，接收端按
					// 静音间隙处理并立即恢复播放。停发时无法预知冻结时长，因此该动作
					// 只能放在恢复时刻。语音房长挂场景下连接可存活超过一天，
					// 冻结也可能跨越回绕周期，此时走重建分支换新时间轴
					if silenceRun >= maxSilenceFillFrames && !lastMediaAt.IsZero() {
						gap := time.Since(lastMediaAt) - 20*time.Millisecond
						switch {
						case gap > 0 && gap < 24*time.Hour:
							// 常规恢复：跳时帧把时间轴前跳到墙上时钟，真实帧落在 now+20ms
							writeAllPeers(silencePayload, gap+20*time.Millisecond)
						case gap >= 24*time.Hour:
							// 冻结跨越 32 位时间戳回绕周期（48kHz 下约 24.85h）：任何前跳
							// 增量取模后落点不可预测，同 SSRC 时间轴无法接续，跳时反而更糟。
							// 重建音轨换新 SSRC/新时间轴，接收端当全新流处理（语音房长挂
							// 一天后 AI 再次开口的真实场景），重建失败则退化为不补救
							logger.Warnw("AI音轨时间轴冻结超过回绕周期，重建音轨恢复",
								"source", acr.clientId[:8], "gapHours", int(gap/time.Hour))
							r.rebuildAITtsTracks()
						}
					}
					silenceRun = 0
					lastMediaAt = time.Now()
					writeAllPeers(qf.payload, 20*time.Millisecond)
					// 刷新播放态：只要 pacer 还在按 20ms 节拍发帧，AI 就视为「在出声」
					playback.markSent(qf.turn, qf.sentence)
				default:
					// 队列为空：真实音频已被实时播完，当前处于句间停顿或等待下一句
					// 补发静音帧让 RTP 时间轴照常推进（作用见 silencePayload 注释）
					// 注意：静音帧绝不调用 markSent——播放态必须只反映真实语音，
					// 否则 speaking() 恒为真，「用户打断 AI」的判定会永久失效
					if started && len(silencePayload) > 0 && silenceRun < maxSilenceFillFrames {
						writeAllPeers(silencePayload, 20*time.Millisecond)
						lastMediaAt = time.Now()
						silenceRun++
						// 触顶只记一次日志：既是限流也是信号——该会话自上一真实帧起
						// 已静默 10s 仍无新音频，可据此监控「TTS/LLM 上游疑似卡死」
						if silenceRun == maxSilenceFillFrames {
							logger.Warnw("静音补发达上限，疑似TTS上游卡死，停止补发",
								"source", acr.clientId[:8], "maxSilenceMs", maxSilenceFillFrames*20)
						}
					}
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
			frameQueue <- queuedFrame{payload: opusPayload, turn: lastTurn, sentence: curSentence}
			framesEncoded++
			// 本轮首个编码帧：记录首块音频 → 首帧 RTP 的间隔
			if pendingTurn != "" {
				observability.RecordFirstAudioEncode(time.Since(turnAudioAt).Milliseconds())
				pendingTurn = ""
			}
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
			observability.MarkInterruptDone(acr.sessionID)
			droppedFrames := drainFrameQueue()
			pcmBuf = pcmBuf[:0]
			rawBuf = rawBuf[:0]
			resetCodecState()
			logger.Infow("打断：已清空待播音频",
				"source", acr.clientId[:8], "droppedFrames", droppedFrames)
			continue
		}

		// AudioData：轮次切换判定——新一轮的首个音频块，记 e2e / ttfb 时间戳
		if ev.TurnID != lastTurn {
			lastTurn = ev.TurnID
			turnAudioAt = time.Now()
			pendingTurn = ev.TurnID
			observability.MarkFirstAudio(ev.TurnID)
		}
		// 句子游标：后续编码帧都按该序号记入播放态，回答「用户听到了第几句」
		curSentence = ev.SentenceSeq
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
			frameQueue <- queuedFrame{payload: opusPayload, turn: lastTurn, sentence: curSentence}
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
		if peer.health != nil {
			observability.AdjustSubscriberHealth(string(peer.health.State()), -1)
		}
		if err := peer.PC.Close(); err != nil {
			logger.Warnw("清理关闭 PC", "clientID", clientID[:8], "error", err)
		}
		delete(r.peers, clientID)
	}
	r.lock.Unlock()

	s.RemoveRoom(roomID)
}
