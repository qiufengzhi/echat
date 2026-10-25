package scenario

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"echat-loadtest/internal/client"
	"echat-loadtest/internal/metrics"
)

// ConnParams 连接层风暴参数
type ConnParams struct {
	Base       string        // backend 地址
	UserPrefix string        // 测试账号前缀（loaduser_0001..N）
	Password   string        // 测试账号密码
	Users      int           // 目标并发连接数
	Ramp       time.Duration // 建连铺开时长（0 = 同时建连）
	Hold       time.Duration // 连接保持时长
	PingGap    time.Duration // 心跳间隔
	LoginMode  string        // per-user（每人登录，压 argon2+登录链路）| shared（复用单 token，纯压 WS 层）
}

// RunConn 连接层场景：ramp 建连 → 保持（周期心跳）→ 全量断开。
// 指标口径对齐 docs/loadtest.md §2.1 维度一；服务端 goroutine/内存/FD 曲线
// 由观测栈（Grafana/node-exporter/collector）在 run 期间采集，与 run_id 关联。
func RunConn(p ConnParams) *metrics.Report {
	runID := "conn-" + time.Now().Format("20060102-150405")
	rep := &metrics.Report{
		RunID:    runID,
		Scenario: "conn",
		StartedAt: time.Now(),
		Params: map[string]any{
			"users": p.Users, "rampSec": p.Ramp.Seconds(), "holdSec": p.Hold.Seconds(),
			"pingGapSec": p.PingGap.Seconds(), "loginMode": p.LoginMode,
		},
		Metrics: map[string]any{},
	}
	start := time.Now()

	// 幂等建号（压测前一次性准备，不算压测路径）
	for i := 1; i <= p.Users; i++ {
		_ = client.Register(p.Base, fmt.Sprintf("%s%04d", p.UserPrefix, i), p.Password)
	}

	var (
		loginMs  = metrics.NewHistogram(200000) // 登录耗时
		hsMs     = metrics.NewHistogram(200000) // WS 握手耗时
		joinMs   = metrics.NewHistogram(200000) // join→应答耗时
		conns    int64                          // 当前活跃连接数（atomic）
		maxConns int64                          // 峰值连接数（atomic）
		dialOK   int64                          // 建连成功数
		joinOK   int64                          // 进房成功数
		heldOK   int64                          // 全程保持成功数
		errMu    sync.Mutex
		errs     = map[string]int{}
		wg       sync.WaitGroup
	)
	recordErr := func(kind string) {
		errMu.Lock()
		errs[kind]++
		errMu.Unlock()
	}

	// 每秒采样活跃连接数，维护峰值
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				for {
					cur := atomic.LoadInt64(&conns)
					m := atomic.LoadInt64(&maxConns)
					if cur <= m || atomic.CompareAndSwapInt64(&maxConns, m, cur) {
						break
					}
				}
			}
		}
	}()

	// shared 模式：单账号登录一次，所有 VU 复用 token（压纯 WS 层，不压 argon2）
	var sharedToken string
	if p.LoginMode == "shared" {
		sess, err := client.Login(p.Base, p.UserPrefix+"0001", p.Password)
		if err != nil {
			rep.Metrics["error"] = err.Error()
			return finish(rep, start)
		}
		sharedToken = sess.AccessToken
	}

	for i := 1; i <= p.Users; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			username := fmt.Sprintf("%s%04d", p.UserPrefix, idx)
			room := fmt.Sprintf("LOAD%04d", idx) // 每连接独立房间：纯连接层压力，不含大房间广播放大
			// 到达时刻按 ramp 线性铺开
			if p.Ramp > 0 {
				time.Sleep(time.Duration(float64(p.Ramp) * float64(idx-1) / float64(p.Users)))
			}

			token := sharedToken
			if token == "" {
				t0 := time.Now()
				sess, err := client.Login(p.Base, username, p.Password)
				if err != nil {
					recordErr("login")
					return
				}
				loginMs.Record(float64(time.Since(t0).Milliseconds()))
				token = sess.AccessToken
			}

			conn, hs, err := client.DialWS(p.Base, token)
			if err != nil {
				recordErr("dial:" + err.Error())
				return
			}
			defer conn.Close()
			atomic.AddInt64(&conns, 1)
			defer atomic.AddInt64(&conns, -1)
			atomic.AddInt64(&dialOK, 1)
			hsMs.Record(hs)

			jt := time.Now()
			if err := conn.Join(room, username); err != nil {
				recordErr("join")
				return
			}
			if err := waitJoined(conn); err != nil {
				recordErr("joinAck")
				return
			}
			joinMs.Record(float64(time.Since(jt).Milliseconds()))
			atomic.AddInt64(&joinOK, 1)

			// 保持阶段：周期心跳；任何异常退出都计入错误分类（带原因，便于定位）
			if err := holdConn(conn, room, p.Hold, p.PingGap); err != nil {
				recordErr("hold:" + err.Error())
				return
			}
			atomic.AddInt64(&heldOK, 1)
		}(i)
	}
	wg.Wait()
	close(stop)

	rep.Metrics["loginMs"] = loginMs.Summarize()
	rep.Metrics["handshakeMs"] = hsMs.Summarize()
	rep.Metrics["joinMs"] = joinMs.Summarize()
	rep.Metrics["dialSuccessRate"] = rate(dialOK, int64(p.Users))
	rep.Metrics["joinSuccessRate"] = rate(joinOK, int64(p.Users))
	rep.Metrics["heldSuccessRate"] = rate(heldOK, int64(p.Users))
	rep.Metrics["maxConcurrentConns"] = atomic.LoadInt64(&maxConns)
	if len(errs) > 0 {
		rep.Metrics["errors"] = errs
	}
	return finish(rep, start)
}

// holdConn 保持连接：周期 ping/pong 直到 hold 时长结束。
//
// gorilla/websocket 契约：一条连接上任意一次读失败（含读超时）后不得再读，
// 再次读会 panic "repeated read on failed websocket connection"。
//
// 结构说明：必须「发 ping → 等 pong → 睡到下个心跳时刻」严格配对。
// 若把 ping 检查放在 Read 之前再阻塞式 Read，Read 会一直占住循环，
// 下一个 ping 永远发不出去，读等待必然超时（教训：读操作内不能隐式等心跳）。
func holdConn(conn *client.WSConn, room string, hold, pingGap time.Duration) error {
	deadline := time.Now().Add(hold)
	for time.Now().Before(deadline) {
		// 1. 发心跳
		if err := conn.Ping(room); err != nil {
			return err
		}
		sentAt := time.Now()
		// 2. 等 pong（读窗口 = 心跳间隔 + 5s 宽限；正常 pong 毫秒级返回）
		conn.SetReadDeadline(sentAt.Add(pingGap + 5*time.Second))
		if _, err := conn.Read(); err != nil {
			return err
		}
		// 3. 停到下个心跳时刻，避免 pong 秒回造成 ping 风暴
		if d := sentAt.Add(pingGap).Sub(time.Now()); d > 0 {
			time.Sleep(d)
		}
	}
	return nil
}

// rate 计算成功率（百分比，一位小数）
func rate(ok, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(int(1000*float64(ok)/float64(total))) / 10
}
