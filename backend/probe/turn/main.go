// 跨语言冒烟：用生产用的 agentcli 连接真实 Python Agent，验证「发起一轮 → 收事件 → 取消/抢占」
//
// 用法（先在另一个终端起 agent/test/turn_server_smoke.py）：
//
//	go run ./probe/turn -addr 127.0.0.1:50099            # 单轮 + 取消
//	go run ./probe/turn -addr 127.0.0.1:50099 -preempt   # 同会话两轮，验证新轮抢占旧轮
package main

import (
	"context"
	"flag"
	"fmt"
	"sync"
	"time"

	"echat-backend/agentcli"
	llmpb "echat-backend/proto/llm"
)

// main 按模式发起轮次并打印两侧可对照的时延与计数
func main() {
	addr := flag.String("addr", "127.0.0.1:50099", "Python Agent 地址")
	shards := flag.Int("shards", 2, "连接分片数")
	preempt := flag.Bool("preempt", false, "发两轮验证抢占：第一轮收到事件后再发第二轮")
	flag.Parse()

	var mu sync.Mutex
	perTurn := map[string]int{} // turnID -> 已收事件数
	start := time.Now()

	c, err := agentcli.New(agentcli.Options{
		Addr:        *addr,
		Shards:      *shards,
		TurnTimeout: 30 * time.Second,
		Dispatch: func(_ context.Context, resp *llmpb.TurnResponse) {
			mu.Lock()
			perTurn[resp.TurnId]++
			first := perTurn[resp.TurnId] == 1
			mu.Unlock()
			if first {
				fmt.Printf("[%.2fs] 轮 %s 首个事件 text=%q\n", time.Since(start).Seconds(), resp.TurnId, resp.ResponseText)
			} else if resp.IsFinal {
				fmt.Printf("[%.2fs] 轮 %s 结束 text=%q\n", time.Since(start).Seconds(), resp.TurnId, resp.ResponseText)
			}
		},
	})
	if err != nil {
		panic(err)
	}
	defer c.Close()

	sessionID := "smoke-room1-client1"
	first := &llmpb.TurnRequest{
		SessionId: sessionID, RoomId: "room1", ClientId: "client1",
		TurnId: "smoke-turn-1", UserText: "你好，请说长一点", Seq: 1,
	}
	if err := c.SendTurn(context.Background(), first); err != nil {
		panic(err)
	}
	fmt.Printf("已发起轮次 %s\n", first.TurnId)
	waitEvents(&mu, perTurn, first.TurnId, 1)

	if *preempt {
		// 同会话再发一轮：agentcli 会先取消在途轮次再接手
		second := &llmpb.TurnRequest{
			SessionId: sessionID, RoomId: "room1", ClientId: "client1",
			TurnId: "smoke-turn-2", UserText: "换个话题，短一点", Seq: 2,
		}
		fmt.Printf("[%.2fs] 发起第二轮 %s（应抢占第一轮）\n", time.Since(start).Seconds(), second.TurnId)
		if err := c.SendTurn(context.Background(), second); err != nil {
			panic(err)
		}
		waitEvents(&mu, perTurn, second.TurnId, 3)
		mu.Lock()
		fmt.Printf("事件分布: turn-1=%d turn-2=%d\n", perTurn["smoke-turn-1"], perTurn["smoke-turn-2"])
		mu.Unlock()
	} else {
		cancelAt := time.Now()
		fmt.Println("发起取消 CancelTurn")
		if !c.CancelTurn(sessionID) {
			fmt.Println("取消返回 false：该会话没有在途轮次")
		}
		for i := 0; i < 200; i++ {
			if c.Inflight() == 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		fmt.Printf("取消后 %.0fms 在途轮次归零\n", time.Since(cancelAt).Seconds()*1000)
	}

	fmt.Printf("统计: %+v\n", c.Stats())
	fmt.Println("（对照 Python 侧日志：应出现「本轮生成被取消」与「轮次已取消」）")
}

// waitEvents 轮询等待某轮累计收到指定数量的事件
//
// mu: 保护计数表的锁
// perTurn: 轮次 ID 到事件数的映射
// turnID: 目标轮次 ID
// want: 期望的事件数
func waitEvents(mu *sync.Mutex, perTurn map[string]int, turnID string, want int) {
	for i := 0; i < 300; i++ {
		mu.Lock()
		got := perTurn[turnID]
		mu.Unlock()
		if got >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
