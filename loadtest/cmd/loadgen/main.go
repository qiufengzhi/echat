// loadgen 压测负载生成器入口。一键运行方式见 loadtest/README.md。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"echat-loadtest/internal/client"
	"echat-loadtest/internal/metrics"
	"echat-loadtest/internal/scenario"
)

func main() {
	var (
		base       = flag.String("base", "http://127.0.0.1:8080", "backend 地址")
		scen       = flag.String("scenario", "smoke", "场景：smoke | conn | session | interrupt | broadcast")
		users      = flag.Int("users", 50, "conn 并发连接数 / session 并发会话数 / broadcast 房间成员数")
		ramp       = flag.Duration("ramp", 30*time.Second, "conn/session 场景铺开时长")
		hold       = flag.Duration("hold", 2*time.Minute, "conn 保持时长 / session、interrupt 观察时长")
		pingGap    = flag.Duration("ping-gap", 25*time.Second, "心跳间隔")
		pingCount  = flag.Int("ping-count", 10, "smoke 场景心跳次数")
		room       = flag.String("room", "LOADTEST", "smoke 场景房间号 / broadcast 房间号")
		joinGap    = flag.Duration("join-gap", 500*time.Millisecond, "broadcast 场景相邻 join 间隔")
		asrInterval = flag.Duration("asr-interval", 4*time.Second, "session/interrupt 场景 mock ASR 产出间隔（须与 backend 配置一致）")
		burstGap   = flag.Duration("burst-gap", 600*time.Millisecond, "burst 判定阈值（须小于轮次间隔−单句播放时长的静音间隙）")
		setup      = flag.Int("setup", 0, "只建号不压测：批量注册 N 个测试账号后退出")
		userPrefix = flag.String("user-prefix", "loaduser_", "测试账号用户名前缀")
		password   = flag.String("password", "Loadtest#2026", "测试账号密码")
		loginMode  = flag.String("login-mode", "per-user", "conn 场景登录模式：per-user | shared")
		outDir     = flag.String("out", "results", "报告输出目录")
	)
	flag.Parse()

	// 建号模式：为 conn 场景准备账号（幂等，已存在仅提示不中断）
	if *setup > 0 {
		for i := 1; i <= *setup; i++ {
			u := fmt.Sprintf("%s%04d", *userPrefix, i)
			if err := client.Register(*base, u, *password); err != nil {
				fmt.Printf("register %s: %v（已存在可忽略）\n", u, err)
			}
		}
		fmt.Printf("账号准备完成：%s0001 .. %s%04d\n", *userPrefix, *userPrefix, *setup)
		return
	}

	var rep *metrics.Report
	switch *scen {
	case "smoke":
		rep = scenario.RunSmoke(scenario.SmokeParams{
			Base:      *base,
			Username:  *userPrefix + "0001",
			Password:  *password,
			Room:      *room,
			PingCount: *pingCount,
			PingGap:   time.Second,
		})
	case "conn":
		rep = scenario.RunConn(scenario.ConnParams{
			Base:       *base,
			UserPrefix: *userPrefix,
			Password:   *password,
			Users:      *users,
			Ramp:       *ramp,
			Hold:       *hold,
			PingGap:    *pingGap,
			LoginMode:  *loginMode,
		})
	case "session":
		rep = scenario.RunSession(scenario.SessionParams{
			Base:         *base,
			UserPrefix:   *userPrefix,
			Password:     *password,
			Sessions:     *users,
			Ramp:         *ramp,
			Hold:         *hold,
			ASRInterval:  *asrInterval,
			GapThreshold: *burstGap,
		})
	case "interrupt":
		rep = scenario.RunInterrupt(scenario.InterruptParams{
			SessionParams: scenario.SessionParams{
				Base:         *base,
				UserPrefix:   *userPrefix,
				Password:     *password,
				Sessions:     *users,
				Ramp:         *ramp,
				Hold:         *hold,
				ASRInterval:  *asrInterval,
				GapThreshold: *burstGap,
			},
		})
	case "broadcast":
		rep = scenario.RunBroadcast(scenario.BroadcastParams{
			Base:       *base,
			UserPrefix: *userPrefix,
			Password:   *password,
			Users:      *users,
			Room:       *room,
			JoinGap:    *joinGap,
		})
	default:
		fmt.Println("未知场景:", *scen)
		os.Exit(2)
	}

	// 报告落盘 loadtest/results/<runId>.json + 控制台摘要
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Println("创建输出目录失败:", err)
		os.Exit(1)
	}
	path := filepath.Join(*outDir, rep.RunID+".json")
	if err := rep.Save(path); err != nil {
		fmt.Println("保存报告失败:", err)
	}
	summary, _ := json.MarshalIndent(rep.Metrics, "", "  ")
	fmt.Printf("run_id=%s scenario=%s duration=%.1fs\nreport=%s\n%s\n", rep.RunID, rep.Scenario, rep.DurationS, path, summary)
}
