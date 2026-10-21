# loadtest 压测子系统

设计文档：`docs/loadtest.md`（唯一结论性设计文档）；容量报告：`docs/loadtest-report.md`。
本目录原始结果（`results/`）已 gitignore，不入库。

## 目录结构

```
loadtest/
  scenarios/          # 场景参数与说明（场景实现在 internal/scenario，Go 代码即场景定义）
  cmd/loadgen/        # 负载生成器入口（复用 backend 的 WS/登录协议事实，见 internal/client）
  internal/           # client（协议封装）/ scenario（场景）/ metrics（指标）
  mocks/              # 真实冒烟语料（real-smoke-corpus.txt，100 句）与后续 mock 素材
  results/            # 原始结果数据（gitignore）
```

## 前置条件

1. 依赖栈可用（观测栈可选，但正式容量采集建议开启）：
   ```bash
   docker compose -f deploy/docker-compose.base.yml -f deploy/docker-compose.dev.yml --profile observability up -d
   ```
2. 本机进程：backend（默认 `http://127.0.0.1:8080`）、VAD、agent。
3. LLM mock：压 load/stress/soak 前把 `agent/config.yaml` 的 `llm.provider` 改为 `mock`
   （或环境变量 `LLM_PROVIDER=mock`），保证零真实上游调用。

## 一键运行（按分工设计：复制粘贴即可）

### 0. 编译（首次）

```bash
cd loadtest
go mod tidy
go build ./...
```

### 1. AI 跑：smoke 冒烟（验证脚本与链路可用，分钟级）

```bash
cd loadtest
go run ./cmd/loadgen -scenario smoke -base http://127.0.0.1:8080 -room LOADTEST -ping-count 10
```

预期：输出 `run_id=smoke-...`、握手耗时、进房应答、10 次 ping/pong RTT，报告写入 `results/`。

### 2. 用户跑：连接层容量采集（正式，按量级逐级加压）

```bash
# 建号（幂等，一次性）
go run ./cmd/loadgen -base http://127.0.0.1:8080 -setup 1000

# 逐级加压（每级之间观察 Grafana：goroutine/内存/FD 曲线，见 docs/loadtest.md §2.1）
go run ./cmd/loadgen -scenario conn -base http://127.0.0.1:8080 -users 100  -ramp 30s -hold 5m
go run ./cmd/loadgen -scenario conn -base http://127.0.0.1:8080 -users 500  -ramp 60s -hold 5m
go run ./cmd/loadgen -scenario conn -base http://127.0.0.1:8080 -users 1000 -ramp 90s -hold 5m -login-mode shared
```

说明：`-login-mode per-user`（默认）登录链路一起压（argon2 校验耗 CPU）；
`-login-mode shared` 复用单 token，纯压 WS 层——两者对比可分离「登录瓶颈」与「WS 承载瓶颈」。

### 3. 用户跑：真实上游冒烟（配额硬上限，可选，成本 ≤ 十元量级）

1. `agent/config.yaml`：`loadtest.real_smoke.enabled: true`（`llm.provider` 保持真实值）；
2. 重启 agent，日志确认「真实冒烟配额已启用」；
3. 运行真实冒烟场景（后续 commit 提供 `smoke-real`，单会话串行消耗 100 句语料）；
4. 结束后把 `enabled` 改回 `false`；计数文件在 `results/real_smoke_quota.json`，重启不重置；
   配额耗尽后 agent 自动降级 mock 并打 WARN，不会超支。

### 4. 采集结果交给 AI 解读

把 `results/*.json` 与 Grafana 关键面板截图（按 run_id 过滤）提供给 AI，
由 AI 解读数据并撰写 `docs/loadtest-report.md`。

## 约束提醒

- load/stress/soak 场景 **必须** `llm.provider: mock`，且不得开启 `loadtest.real_smoke`；
- 真实冒烟只允许单会话、串行、分钟级运行；
- 压测暴露的问题记入 `docs/loadtest.md` §7 backlog，不顺手改业务代码。
