package scenario

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"echat-loadtest/internal/client"
	"echat-loadtest/internal/metrics"
)

// BroadcastParams 数据面场景参数：N 个成员先后加入同一房间
type BroadcastParams struct {
	Base       string        // backend 地址
	UserPrefix string        // 测试账号前缀（前 Users 个账号）
	Password   string        // 测试账号密码
	Users      int           // 房间最终成员数
	Room       string        // 房间号（全员同一房间）
	JoinGap    time.Duration // 相邻两次 join 的间隔（让广播排空，避免测量互相污染）
}

// RunBroadcast 数据面广播场景：成员逐个加入同一房间，测量两类时延：
//   - joinAckMs：join 发出 → 服务端应答（waiting/room_ready）
//   - broadcastMs：第 k 个成员的 join 发出 → 既有成员收到 user_joined 广播，
//     按既有成员逐个记录——广播时延随房间人数的放大关系即由此而来
func RunBroadcast(p BroadcastParams) *metrics.Report {
	runID := "broadcast-" + time.Now().Format("20060102-150405")
	rep := &metrics.Report{
		RunID:     runID,
		Scenario:  "broadcast",
		StartedAt: time.Now(),
		Params: map[string]any{
			"users": p.Users, "room": p.Room, "joinGapMs": p.JoinGap.Milliseconds(),
		},
		Metrics: map[string]any{},
	}
	start := time.Now()

	// 幂等建号
	for i := 1; i <= p.Users; i++ {
		_ = client.Register(p.Base, fmt.Sprintf("%s%04d", p.UserPrefix, i), p.Password)
	}

	// receivedJoin 一次 user_joined 广播的接收记录
	type receivedJoin struct {
		userID string
		at     time.Time
	}
	// member 房间成员：每连接唯一读循环向 ack/receipt 投递
	type member struct {
		ws      *client.WSConn
		ack     chan struct{}     // 自己的 join 应答（waiting/room_ready）
		receipt chan receivedJoin // 收到的 user_joined 广播（userID, 时刻）
	}

	// 登录并建连所有成员（先全部建连，join 才是唯一的被测动作）
	members := make([]member, 0, p.Users)
	for i := 1; i <= p.Users; i++ {
		username := fmt.Sprintf("%s%04d", p.UserPrefix, i)
		sess, err := client.Login(p.Base, username, p.Password)
		if err != nil {
			rep.Metrics["error"] = "login: " + err.Error()
			return finish(rep, start)
		}
		ws, _, err := client.DialWS(p.Base, sess.AccessToken)
		if err != nil {
			rep.Metrics["error"] = "dial: " + err.Error()
			return finish(rep, start)
		}
		defer ws.Close()
		m := member{
			ws:      ws,
			ack:     make(chan struct{}, 1),
			receipt: make(chan receivedJoin, p.Users),
		}
		// 每连接唯一读循环：应答类投 ack，广播类投 receipt，其余丢弃
		go func(m member) {
			for {
				msg, err := m.ws.Read()
				if err != nil {
					return
				}
				switch msg.Type {
				case "waiting", "room_ready":
					select {
					case m.ack <- struct{}{}:
					default:
					}
				case "user_joined":
					var pl struct {
						UserID string `json:"userId"`
					}
					if decodePayload(msg.Payload, &pl) == nil {
						m.receipt <- receivedJoin{userID: pl.UserID, at: time.Now()}
					}
				}
			}
		}(m)
		members = append(members, m)
	}

	// 逐个 join：第 k 个成员（k 从 0 计）发送 join，前 k 个成员等待 user_joined
	joinAck := metrics.NewHistogram(200000)
	broadcastMs := metrics.NewHistogram(200000)
	broadcastByMemberIndex := map[string]*metrics.Histogram{} // "memberIdx" → 该成员位置的广播时延
	var missed int64

	for k := range members {
		if err := members[k].ws.Join(p.Room, fmt.Sprintf("m%04d", k)); err != nil {
			rep.Metrics["error"] = "join: " + err.Error()
			return finish(rep, start)
		}
		sentAt := time.Now()

		// 等待自己的应答
		select {
		case <-members[k].ack:
			joinAck.Record(float64(time.Since(sentAt).Milliseconds()))
		case <-time.After(10 * time.Second):
			rep.Metrics["error"] = fmt.Sprintf("joinAck timeout at member %d", k)
			return finish(rep, start)
		}

		// 既有成员（0..k-1）等待 user_joined 广播
		var wg sync.WaitGroup
		for j := 0; j < k; j++ {
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				deadline := time.After(10 * time.Second)
				for {
					select {
					case r := <-members[j].receipt:
						if r.userID == "" {
							return
						}
						// 广播时延 = 收到时刻 - 该次 join 发出时刻
						broadcastMs.Record(float64(r.at.Sub(sentAt).Milliseconds()))
						key := fmt.Sprintf("roomSize%02d", j+1)
						h, ok := broadcastByMemberIndex[key]
						if !ok {
							h = metrics.NewHistogram(200000)
							broadcastByMemberIndex[key] = h
						}
						h.Record(float64(r.at.Sub(sentAt).Milliseconds()))
						return
					case <-deadline:
						atomic.AddInt64(&missed, 1)
						return
					}
				}
			}(j)
		}
		wg.Wait()

		if p.JoinGap > 0 {
			time.Sleep(p.JoinGap)
		}
	}

	rep.Metrics["joinAckMs"] = joinAck.Summarize()
	rep.Metrics["broadcastMs"] = broadcastMs.Summarize()
	rep.Metrics["missedBroadcasts"] = missed
	for key, h := range broadcastByMemberIndex {
		rep.Metrics[key] = h.Summarize()
	}
	return finish(rep, start)
}

// decodePayload 通用 payload 解码
func decodePayload(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return fmt.Errorf("payload 为空")
	}
	return json.Unmarshal(raw, v)
}
