package scenario

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"echat-loadtest/internal/client"
	"echat-loadtest/internal/metrics"
)

// dtxSilenceFrame Opus DTX 静音帧（3 字节）：合法 Opus 包，后端解码后无有效采样，
// 不进入识别路径——mock 模式下音频内容无关紧要，RTP 流只需保持活跃以启动 TTS 循环
var dtxSilenceFrame = []byte{0xF8, 0xFF, 0xFE}

// DefaultBurstGapThreshold burst 判定阈值的默认值：两个 RTP 包间隔超过该值视为
// 「AI 停止出声」，一次突发（burst）结束。
// 取值依据：必须小于「轮次间隔 − 单句播放时长」的真实静音间隙。以默认 mock 参数为例：
// ASR 间隔 4s、单句 26 字 × 120ms ≈ 3.12s，间隙 ≈ 0.88s——阈值 1.5s 时 burst 永不闭合
// （教训见 docs/loadtest.md §7.4），600ms 留出抖动余量且仍高于帧间隔
var DefaultBurstGapThreshold = 600 * time.Millisecond

// SessionParams 会话层场景参数
type SessionParams struct {
	Base         string        // backend 地址
	UserPrefix   string        // 测试账号前缀
	Password     string        // 测试账号密码
	Sessions     int           // 并发会话数
	Ramp         time.Duration // 会话铺开时长（0 = 同时发起）
	Hold         time.Duration // 每会话保持时长（期间 mock ASR 按间隔产出轮次）
	ASRInterval  time.Duration // mock ASR 产出间隔（仅记录进报告，须与 backend 配置一致）
	GapThreshold time.Duration // burst 判定阈值，0 取 DefaultBurstGapThreshold（须小于真实静音间隙）
	StunServers  []string      // ICE STUN 服务器，本地压测留空（纯 host 候选）
}

// RunSession 会话层场景：join → 建立 WebRTC 连接 → 发布静音轨 → ai_toggle →
// 观察 AI 下行音频的「突发（burst）」序列。一次 burst ≈ 一轮回复的播出；
// burst 间隔 ≈ 轮次间隔；burst 数 ≈ 轮次数。服务端各阶段耗时（LLM TTFT/TTS TTFB/
// 打断延迟）由观测栈按 run_id 采集，与客户端曲线交叉印证（docs/loadtest.md §2.2）。
func RunSession(p SessionParams) *metrics.Report {
	runID := "session-" + time.Now().Format("20060102-150405")
	rep := &metrics.Report{
		RunID:    runID,
		Scenario: "session",
		StartedAt: time.Now(),
		Params: map[string]any{
			"sessions": p.Sessions, "rampSec": p.Ramp.Seconds(), "holdSec": p.Hold.Seconds(),
			"asrIntervalMs": p.ASRInterval.Milliseconds(),
		},
		Metrics: map[string]any{},
	}
	start := time.Now()

	var (
		pcConnectMs = metrics.NewHistogram(200000) // offer→连接建立耗时
		aiOnlineMs  = metrics.NewHistogram(200000) // ai_toggle→ai_status online 耗时
		firstAudioMs = metrics.NewHistogram(200000) // ai_toggle→首个 AI 下行音频包耗时
		burstDurMs  = metrics.NewHistogram(500000) // burst 时长（≈单轮播出时长；被打断的 burst 更短）
		burstGapMs  = metrics.NewHistogram(500000) // burst 间隔（≈轮次间隔）
		burstTotal  int64
		pktTotal    int64
		okSessions  int64
		errMu       sync.Mutex
		errs        = map[string]int{}
		wg          sync.WaitGroup
	)
	recordErr := func(kind string) {
		errMu.Lock()
		errs[kind]++
		errMu.Unlock()
	}

	for i := 1; i <= p.Sessions; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			username := fmt.Sprintf("%s%04d", p.UserPrefix, idx)
			room := fmt.Sprintf("LOADS%04d", idx)
			if p.Ramp > 0 {
				time.Sleep(time.Duration(float64(p.Ramp) * float64(idx-1) / float64(p.Sessions)))
			}
			if err := runOneSession(p, username, room,
				pcConnectMs, aiOnlineMs, firstAudioMs, burstDurMs, burstGapMs,
				&burstTotal, &pktTotal); err != nil {
				recordErr(err.Error())
				return
			}
			atomic.AddInt64(&okSessions, 1)
		}(i)
	}
	wg.Wait()

	rep.Metrics["pcConnectMs"] = pcConnectMs.Summarize()
	rep.Metrics["aiOnlineMs"] = aiOnlineMs.Summarize()
	rep.Metrics["firstAudioMs"] = firstAudioMs.Summarize()
	rep.Metrics["burstDurationMs"] = burstDurMs.Summarize()
	rep.Metrics["burstGapMs"] = burstGapMs.Summarize()
	rep.Metrics["burstTotal"] = burstTotal
	rep.Metrics["audioPackets"] = pktTotal
	rep.Metrics["sessionSuccessRate"] = rate(okSessions, int64(p.Sessions))
	if len(errs) > 0 {
		rep.Metrics["errors"] = errs
	}
	return finish(rep, start)
}

// burstCollector 汇集单个会话的下行音频突发序列（线程安全，仅 OnTrack 协程使用则免锁）
type burstCollector struct {
	gap        time.Duration
	start      time.Time
	last       time.Time
	inBurst    bool
	dur        *metrics.Histogram
	gapH       *metrics.Histogram
	burstCount *int64
	pktCount   *int64
}

// observe 记录一个下行音频包到达时刻，维护 burst 边界
func (b *burstCollector) observe(now time.Time) {
	*b.pktCount++
	if !b.inBurst {
		if !b.start.IsZero() {
			b.gapH.Record(float64(now.Sub(b.last).Milliseconds()))
			*b.burstCount++
		}
		b.inBurst = true
		b.start = now
	} else if now.Sub(b.last) > b.gap {
		b.dur.Record(float64(b.last.Sub(b.start).Milliseconds()))
		b.gapH.Record(float64(now.Sub(b.last).Milliseconds()))
		*b.burstCount++
		b.start = now
	}
	b.last = now
}

// finishAt 收尾：把仍在进行的 burst 记入直方图
func (b *burstCollector) finishAt(now time.Time) {
	if b.inBurst {
		b.dur.Record(float64(now.Sub(b.start).Milliseconds()))
		*b.burstCount++
	}
}

// runOneSession 单会话全流程：登录 → join → PC 协商 → 发布静音轨 → ai_toggle →
// 保持 hold 时长观察下行音频突发 → 收尾
func runOneSession(
	p SessionParams, username, room string,
	pcConnectMs, aiOnlineMs, firstAudioMs, burstDurMs, burstGapMs *metrics.Histogram,
	burstTotal, pktTotal *int64,
) error {
	sess, err := client.Login(p.Base, username, p.Password)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	ws, _, err := client.DialWS(p.Base, sess.AccessToken)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer ws.Close()
	if err := ws.Join(room, username); err != nil {
		return fmt.Errorf("join: %w", err)
	}
	if err := waitJoined(ws); err != nil {
		return fmt.Errorf("joinAck: %w", err)
	}

	// ---------- PeerConnection 与信令 ----------
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: toICE(p.StunServers)})
	if err != nil {
		return fmt.Errorf("pc: %w", err)
	}
	defer pc.Close()

	publishTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "lg-"+username)
	if err != nil {
		return fmt.Errorf("track: %w", err)
	}
	if _, err = pc.AddTrack(publishTrack); err != nil {
		return fmt.Errorf("addTrack: %w", err)
	}

	var (
		mu           sync.Mutex // 保护 pendingICE 与 remoteSet
		pendingICE   []client.SFUICEPayload
		remoteSet    bool
		answerCh     = make(chan string, 1)   // sfu_answer 的 SDP
		onlineCh     = make(chan string, 1)   // ai_status online
		offerCh      = make(chan string, 8)   // sfu_renegotiation_offer 的 SDP
		firstAudioAt atomic.Int64
		toggleAt     atomic.Int64
	)

	gapThreshold := p.GapThreshold
	if gapThreshold <= 0 {
		gapThreshold = DefaultBurstGapThreshold
	}
	collector := &burstCollector{
		gap: gapThreshold, dur: burstDurMs, gapH: burstGapMs,
		burstCount: burstTotal, pktCount: pktTotal,
	}

	// AI 下行音频轨到达后持续计数，维护 burst 序列
	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		buf := make([]byte, 1500)
		for {
			n, _, err := remote.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				now := time.Now()
				firstAudioAt.CompareAndSwap(0, now.UnixMilli())
				collector.observe(now)
			}
		}
	})

	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		ice := toPayload(c.ToJSON())
		if !remoteSet {
			pendingICE = append(pendingICE, ice) // 远端描述未就绪先攒着
			return
		}
		_ = ws.SendICE(room, ice)
	})

	addRemoteICE := func(p client.SFUICEPayload) {
		mu.Lock()
		defer mu.Unlock()
		if !remoteSet {
			pendingICE = append(pendingICE, p)
			return
		}
		_ = pc.AddICECandidate(toWebRTCICE(p))
	}

	drainPendingICE := func() {
		mu.Lock()
		defer mu.Unlock()
		remoteSet = true
		for _, ice := range pendingICE {
			_ = pc.AddICECandidate(toWebRTCICE(ice))
		}
		pendingICE = nil
	}

	// 读循环：按类型分发下行信令
	go func() {
		for {
			msg, err := ws.Read()
			if err != nil {
				close(answerCh)
				close(onlineCh)
				close(offerCh)
				return
			}
			switch msg.Type {
			case "sfu_answer":
				sdp, _ := client.ParseSFUPayload(msg.Payload)
				select {
				case answerCh <- sdp:
				default:
				}
			case "sfu_ice":
				if ice, err := client.ParseICEPayload(msg.Payload); err == nil {
					addRemoteICE(ice)
				}
			case "sfu_renegotiation_offer":
				sdp, _ := client.ParseSFUPayload(msg.Payload)
				select {
				case offerCh <- sdp:
				default:
				}
			case "ai_status":
				// payload 形如 {"state":"online"}（对齐 wire.go AiToggleRes）
				var st struct {
					State string `json:"state"`
				}
				if len(msg.Payload) > 0 && json.Unmarshal(msg.Payload, &st) == nil && st.State == "online" {
					select {
					case onlineCh <- "online":
					default:
					}
				}
			}
		}
	}()

	// 本地 Offer：等待候选收集完成（无 STUN 时几乎立即完成），超时则后续走 sfu_ice 补发
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("offer: %w", err)
	}
	gatherDone := webrtc.GatheringCompletePromise(pc)
	if err = pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("setLocal: %w", err)
	}
	select {
	case <-gatherDone:
	case <-time.After(3 * time.Second):
	}
	if err = ws.SendSDP("sfu_offer", room, pc.LocalDescription().SDP); err != nil {
		return fmt.Errorf("sendOffer: %w", err)
	}

	t0 := time.Now()
	select {
	case sdp, ok := <-answerCh:
		if !ok {
			return fmt.Errorf("connClosed")
		}
		if err = pc.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer, SDP: sdp,
		}); err != nil {
			return fmt.Errorf("setRemote: %w", err)
		}
		drainPendingICE()
	case <-time.After(15 * time.Second):
		return fmt.Errorf("sfuAnswerTimeout")
	}
	pcConnectMs.Record(float64(time.Since(t0).Milliseconds()))

	// renegotiation 处理循环：AI 轨加入触发重协商，应答后下行音频才能开始流动
	// 必须先于「等待首音频」启动，否则双向等待死锁
	stopRenego := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopRenego:
				return
			case sdp, ok := <-offerCh:
				if !ok {
					return
				}
				if err := pc.SetRemoteDescription(webrtc.SessionDescription{
					Type: webrtc.SDPTypeOffer, SDP: sdp,
				}); err != nil {
					continue
				}
				ans, err := pc.CreateAnswer(nil)
				if err != nil {
					continue
				}
				if err := pc.SetLocalDescription(ans); err != nil {
					continue
				}
				_ = ws.SendSDP("sfu_renegotiation_answer", room, pc.LocalDescription().SDP)
			}
		}
	}()
	defer close(stopRenego)

	// 发布静音 RTP：保持媒体流活跃（后端 TTS 循环由首个入站 RTP 启动）
	stopPublish := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopPublish:
				return
			case <-ticker.C:
				_ = publishTrack.WriteSample(media.Sample{Data: dtxSilenceFrame, Duration: 20 * time.Millisecond})
			}
		}
	}()
	defer close(stopPublish)

	// ai_toggle 开启并等待 online 确认
	if err = ws.SendAIToggle(room, true); err != nil {
		return fmt.Errorf("aiToggle: %w", err)
	}
	toggleAt.Store(time.Now().UnixMilli())
	t1 := time.Now()
	select {
	case _, ok := <-onlineCh:
		if !ok {
			return fmt.Errorf("connClosed")
		}
	case <-time.After(10 * time.Second):
		return fmt.Errorf("aiOnlineTimeout")
	}
	onlineMs := float64(time.Since(t1).Milliseconds())
	aiOnlineMs.Record(onlineMs)

	// 等待首个 AI 下行音频包（= 首轮端到端耗时的客户端观测值）
	select {
	case fa := <-waitFirstAudio(&firstAudioAt):
		firstAudioMs.Record(float64(fa - toggleAt.Load()))
	case <-time.After(30 * time.Second):
		return fmt.Errorf("firstAudioTimeout")
	}

	// 保持观察：hold 期间 mock ASR 周期产出轮次，下行音频形成 burst 序列
	time.Sleep(p.Hold)
	collector.finishAt(time.Now())
	return nil
}

// waitFirstAudio 返回一个单元素通道，首个音频到达时投递其时刻
func waitFirstAudio(at *atomic.Int64) <-chan int64 {
	ch := make(chan int64, 1)
	go func() {
		for {
			if v := at.Load(); v != 0 {
				ch <- v
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	return ch
}

// toICE 转换 STUN 配置
func toICE(servers []string) []webrtc.ICEServer {
	out := make([]webrtc.ICEServer, 0, len(servers))
	for _, s := range servers {
		out = append(out, webrtc.ICEServer{URLs: []string{s}})
	}
	return out
}

// toPayload webrtc ICECandidateInit → 信令载荷
func toPayload(init webrtc.ICECandidateInit) client.SFUICEPayload {
	p := client.SFUICEPayload{
		Candidate:        init.Candidate,
		SDPMid:           deref(init.SDPMid),
		UsernameFragment: deref(init.UsernameFragment),
	}
	if init.SDPMLineIndex != nil {
		v := uint16(*init.SDPMLineIndex)
		p.SDPMLineIndex = &v
	}
	return p
}

// toWebRTCICE 信令载荷 → webrtc ICECandidateInit
func toWebRTCICE(p client.SFUICEPayload) webrtc.ICECandidateInit {
	init := webrtc.ICECandidateInit{
		Candidate:        p.Candidate,
		SDPMid:           strPtr(p.SDPMid),
		UsernameFragment: strPtr(p.UsernameFragment),
	}
	if p.SDPMLineIndex != nil {
		v := uint16(*p.SDPMLineIndex)
		init.SDPMLineIndex = &v
	}
	return init
}

func strPtr(s string) *string { return &s }

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
