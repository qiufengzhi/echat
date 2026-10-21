// Package scenario 内置压测场景：smoke（单用户链路冒烟）与 conn（连接层风暴）。
// 场景分层与指标口径见 docs/loadtest.md §2/§4。
package scenario

import (
	"fmt"
	"time"

	"echat-loadtest/internal/client"
	"echat-loadtest/internal/metrics"
)

// SmokeParams smoke 场景参数
type SmokeParams struct {
	Base      string        // backend 地址，如 http://127.0.0.1:8080
	Username  string        // 冒烟账号
	Password  string        // 冒烟密码
	Room      string        // 房间号
	PingCount int           // 心跳次数
	PingGap   time.Duration // 心跳间隔
}

// RunSmoke 单用户链路冒烟：幂等建号 → 登录 → 建连 → 进房 → 心跳 N 次 → 收尾。
// 用途：验证 loadgen 脚本与 backend 链路可用（任务书运行分工：AI 负责跑通 smoke）。
func RunSmoke(p SmokeParams) *metrics.Report {
	runID := "smoke-" + time.Now().Format("20060102-150405")
	rep := &metrics.Report{
		RunID:    runID,
		Scenario: "smoke",
		StartedAt: time.Now(),
		Params:   map[string]any{"base": p.Base, "room": p.Room, "pingCount": p.PingCount},
		Metrics:  map[string]any{},
	}
	start := time.Now()

	// 幂等建号：已存在时注册报错忽略，以登录成功为准
	_ = client.Register(p.Base, p.Username, p.Password)
	sess, err := client.Login(p.Base, p.Username, p.Password)
	if err != nil {
		rep.Metrics["error"] = err.Error()
		return finish(rep, start)
	}

	conn, hsMs, err := client.DialWS(p.Base, sess.AccessToken)
	if err != nil {
		rep.Metrics["error"] = "dial: " + err.Error()
		return finish(rep, start)
	}
	defer conn.Close()
	rep.Metrics["handshakeMs"] = hsMs

	if err := conn.Join(p.Room, "loadsmoke"); err != nil {
		rep.Metrics["error"] = "join: " + err.Error()
		return finish(rep, start)
	}

	// 等 join 应答：单人在房为 waiting，多人或就绪为 room_ready
	if err := waitJoined(conn); err != nil {
		rep.Metrics["error"] = "join ack: " + err.Error()
		return finish(rep, start)
	}

	// 心跳 RTT：连接层链路健康度的基线数据
	rtt := metrics.NewHistogram(10000)
	for i := 0; i < p.PingCount; i++ {
		t0 := time.Now()
		if err := conn.Ping(p.Room); err != nil {
			rep.Metrics["error"] = "ping: " + err.Error()
			return finish(rep, start)
		}
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		got := false
		for !got {
			msg, err := conn.Read()
			if err != nil {
				rep.Metrics["error"] = "pong wait: " + err.Error()
				return finish(rep, start)
			}
			if msg.Type == "pong" {
				got = true
			}
		}
		rtt.Record(float64(time.Since(t0).Milliseconds()))
		time.Sleep(p.PingGap)
	}
	rep.Metrics["pingRttMs"] = rtt.Summarize()
	return finish(rep, start)
}

// waitJoined 读到 join 应答（waiting/room_ready）为止，其余消息跳过
func waitJoined(conn *client.WSConn) error {
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		msg, err := conn.Read()
		if err != nil {
			return err
		}
		if msg.Type == "waiting" || msg.Type == "room_ready" {
			return nil
		}
	}
}

// finish 收尾：记录总时长并输出摘要行
func finish(rep *metrics.Report, start time.Time) *metrics.Report {
	rep.DurationS = time.Since(start).Seconds()
	fmt.Printf("[loadgen] run_id=%s scenario=%s done in %.1fs\n", rep.RunID, rep.Scenario, rep.DurationS)
	return rep
}
