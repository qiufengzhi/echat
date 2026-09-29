// 跨语言冒烟：用生产用的 agentcli 连接真实 Python Agent，验证「发起一轮 → 收事件 → 取消」
//
// 用法（先在另一个终端起 agent/test/turn_server_smoke.py）：
//   go run ./probe/turn -addr 127.0.0.1:50099
package main

import (
	"flag"
	"fmt"
	"sync/atomic"
	"time"

	"echat-backend/agentcli"
	llmpb "echat-backend/proto/llm"
)

// main 发起一轮对话，收到首个事件后取消，打印两侧可对照的时延与计数
func main() {
	addr := flag.String("addr", "127.0.0.1:50099", "Python Agent 地址")
	shards := flag.Int("shards", 2, "连接分片数")
	flag.Parse()

	var firstEventAt int64
	var events int64
	start := time.Now()

	c, err := agentcli.New(agentcli.Options{
		Addr:        *addr,
		Shards:      *shards,
		TurnTimeout: 30 * time.Second,
		Dispatch: func(resp *llmpb.TurnResponse) {
			n := atomic.AddInt64(&events, 1)
			if n == 1 {
				atomic.StoreInt64(&firstEventAt, time.Since(start).Milliseconds())
				fmt.Printf("首个事件到达 %.0fms text=%q\n", time.Since(start).Seconds()*1000, resp.ResponseText)
			}
			if resp.IsFinal {
				fmt.Printf("本轮结束 is_final text=%q\n", resp.ResponseText)
			}
		},
	})
	if err != nil {
		panic(err)
	}
	defer c.Close()

	req := &llmpb.TurnRequest{
		SessionId: "smoke-room1-client1",
		RoomId:    "room1",
		ClientId:  "client1",
		TurnId:    "smoke-turn-1",
		UserText:  "你好，请说长一点",
		Seq:       1,
	}
	if err := c.SendTurn(req); err != nil {
		panic(err)
	}
	fmt.Printf("已发起轮次 %s\n", req.TurnId)

	// 等到首个事件后再取消，观察取消是否立即生效
	for i := 0; i < 100; i++ {
		if atomic.LoadInt64(&events) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancelAt := time.Now()
	fmt.Println("发起取消 CancelTurn")
	if !c.CancelTurn(req.SessionId) {
		fmt.Println("取消返回 false：该会话没有在途轮次")
	}

	// 等 Python 侧收尾并回收在途登记
	for i := 0; i < 200; i++ {
		if c.Inflight() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	fmt.Printf("取消后 %.0fms 在途轮次归零\n", time.Since(cancelAt).Seconds()*1000)
	fmt.Printf("收到事件数=%d 首个事件时延=%dms\n", atomic.LoadInt64(&events), atomic.LoadInt64(&firstEventAt))
	fmt.Printf("统计: %+v\n", c.Stats())
	fmt.Println("（对照 Python 侧日志：应出现「轮次已取消」与该轮生成被取消）")
}
