# 压测体系设计（loadtest）

> 任务书：`docs/loadtest-task-prompt.md`。本文档是压测体系的**唯一结论性设计文档**；
> 容量结果另行输出到 `docs/loadtest-report.md`，原始数据放 `loadtest/results/`（gitignore）。
>
> 定位声明：本项目是面试导向的实时语音对话系统。压测的目的是产出可防守的容量结论与工程叙事，
> 不是把系统压挂。所有设计决策都必须能回答「为什么压这个」「为什么这么压」「数据怎么解读」。

状态标记：✅ 已定稿 / 🚧 待补（按任务书步骤逐步填充）。

---

## 目录

- [一、测试目标地图](#一测试目标地图)
- [二、指标体系](#二指标体系)
- [三、参照系调研](#三参照系调研)（✅）
- [四、压测场景分层设计](#四压测场景分层设计)（✅）
- [五、技术选型论证](#五技术选型论证)（✅）
- [六、Mock 策略与真实上游冒烟配额机制](#六mock-策略与真实上游冒烟配额机制)（✅）
- [七、落地架构与引入计划](#七落地架构与引入计划)（✅）

---

## 一、测试目标地图

### 1.1 每一发压力打在哪里（压力落点）

压测流量从客户端进入后，实际落在四条链路上。**写压测脚本之前必须确认每条链路的协议、
鉴权方式与依赖**，否则脚本写不出来或者压错了地方。

```
                       ┌────────────────────────────────────────────────────┐
 [压测客户端 loadgen]   │                    Go backend（被压主体）            │
   │                   │                                                    │
   ├─① HTTP 登录 ──────►│ POST /v1/auth/login（argon2 校验 → 签发 JWT）       │
   │                   │        backend/authn/                              │
   │                   │                                                    │
   ├─② WS 信令 ───────►│ /ws?token=<JWT>（JSON 信封 Message）                │
   │                   │        backend/handlers/websocket.go               │
   │                   │          → room/signaling（建房/加入/成员投影）      │
   │                   │          → room/projection（Redis 在线心跳）        │
   │                   │          → room/persist（ent/PG 事实落库）          │
   │                   │          → outbox → NATS JetStream                 │
   │                   │                                                    │
   ├─③ RTP 音频上行 ───►│ 自研 SFU（pion/webrtc v4，UDP 端口范围见 config）   │
   │                   │        backend/sfu/sfu.go                          │
   │                   │          → vad_cli（VAD :50052）                   │
   │                   │          → asr_cli（ASR，见 1.3）                   │
   │                   │          → agentcli（gRPC :50053, shards=4 连接池） │
   │                   │                │                                   │
   │                   │                ▼                                   │
   │                   │          [Python agent]（被压主体之二）              │
   │                   │            TurnServiceServicer（agent/server/）     │
   │                   │            orchestrator → LLM provider（见 1.3）    │
   │                   │                │流式 token 回传                     │
   │                   │          ← tts_cli（TTS，见 1.3）                   │
   │                   │          ← sfu.runAITTSLoop → Opus → RTP 下行 ─────┤
   │                   │                                                    │
   ├─④ 广播/消息 ──────►│ dataplane（ring 有界队列 + 丢弃计数）               │
   │◄──────────────────┤        backend/dataplane/                          │
   └───────────────────┴────────────────────────────────────────────────────┘
```

打断（barge-in）说明：**打断不是一条客户端消息，而是服务端从 ASR 结果推导的**
（`backend/sfu/asrdispatch.go` + `backend/sfu/playback.go` 的 playbackState）。
压打断层时必须制造「AI 出声中 + ASR 突然出结果」的真实时序，见 §4 打断层场景。

### 1.2 压测入口清单

| # | 入口 | 协议/格式 | 鉴权 | 代码入口 | 压测覆盖 |
|---|---|---|---|---|---|
| 1 | 登录换 token | HTTP JSON | 邮箱+argon2 密码 | `backend/authn/`、登录 handler | ✅ 每次场景 setup 前置调用；高频建连时注意登录限流（Redis） |
| 2 | WebSocket 信令 | WS，JSON 信封 `{type, roomId, userId, traceparent, payload}` | `?token=<JWT>`（JWT HMAC，access TTL 15m） | `backend/handlers/websocket.go` → `room/signaling/wire.go` | ✅ 连接层 + 会话层 + 数据面 |
| 3 | RTP 音频上行 | WebRTC/Opus over UDP | 信令层已鉴权后 SDP 协商 | `backend/sfu/sfu.go` | ✅ 会话层（模拟说话）/打断层 |
| 4 | gRPC Generate | protobuf 流（`proto/llm.proto`） | 无鉴权（内网 backend→agent） | `backend/agentcli/client.go`+`pool.go` | 间接压（经 SFU 驱动）；loadgen 不直连 agent |
| 5 | WebTransport :4433 | QUIC | 同 WS | `backend/wt/` | ❌ 首版不覆盖，与 WS 共用信令入口，压测增益低（见追问预判 5.2） |

关键事实：**WS 消息是 JSON 信封而非 protobuf**——这对负载工具选型影响重大（k6 原生可发 JSON；
gRPC 部分只有 backend↔agent 之间，loadgen 不需要直连）。信令消息类型清单以
`room/signaling/wire.go` 为准，写 loadgen 前逐个核对。

### 1.3 依赖拓扑与运行形态

容器化依赖（`deploy/docker-compose.base.yml`）：

| 依赖 | 端口 | 语音热路径是否经过 | 压测处理 |
|---|---|---|---|
| PostgreSQL (ent) | 5433 | ❌ 仅登录、房间事实落库、outbox | 真跑（容器，零成本） |
| Redis | 6379 | ⚠️ 在线心跳 ZSET + 登录限流 | 真跑（容器） |
| NATS JetStream | 4222 | ❌ 仅 outbox 事件（`user.>`/`room.>`） | 真跑（容器） |
| SpiceDB | 50051 | ❌ 对象级授权，登录路径 | 真跑（容器） |
| 观测栈（otel-collector/tempo/prometheus/loki/grafana/node-exporter） | — | — | 真跑，压测联动见 §2.4 |

本机进程（被压主体）：backend（内嵌 SFU）、agent :50053、VAD :50052、loadgen。

上游服务（压测核心矛盾所在）：

| 上游 | 由谁触发 | 生产实现 | 压测策略 |
|---|---|---|---|
| LLM | agent `orchestrator` → `BaseLLMProvider`（OpenAI 兼容，DeepSeek） | 真实 API，烧钱 | **mock**：agent 内 mock provider（改造 `agent/test/` 现成 `FakeProvider`/`PacedProvider`） |
| ASR | backend `asr_cli`（生产直连阿里云 NLS） | 真实云服务，烧钱 | **mock**：本地 gRPC ASR 或预录音频流回放，按 §6 定稿 |
| TTS | backend `tts_cli`（讯飞/阿里云） | 真实云服务，烧钱 | **mock**：独立 mock TTS server 或预生成音频回放 |
| VAD | backend `vad_cli` → 本地 gRPC :50052 | 本地模型 | **真跑**（本地推理，零外部成本，且是轮次尾点判定的一环） |

### 1.4 可 mock / 必须真跑清单（结论）

- **必须真跑**：Go backend（含自研 SFU）、Python agent（编排层）、VAD、PG/Redis/NATS/SpiceDB/观测栈（容器）。
- **必须 mock**：LLM、ASR、TTS 三个真实上游——压测负载场景零真实上游调用。
- **真实上游仅限冒烟**：smoke 场景（单会话、串行、分钟级）按配额跑真实 LLM/ASR/TTS 各一次，
  验证「mock 链路与真实链路的时序形态一致」（TTFT/TTFB 量级对齐），配额机制见 §6。

### 1.5 单轮次成本估算（成本红线依据）

全 mock 压测：**0 元**。真实冒烟配额（硬上限，见 §6）：

| 上游 | 配额 | 用量估算 | 计费口径 | 预估成本 |
|---|---|---|---|---|
| LLM | ≤1000 次 | 每次输入 ≤50 字短句、输出 max_tokens≤100 | 一轮一次调用，不按 token 复算 | 千次短轮次，按 DeepSeek 等国产模型单价为个位数元级；**以运行前后打印的实际计数 × 当时单价为准**（§6 强制打印） |
| ASR | ≤100 次 | 100 句 × ≤5s ≈ 8.3 分钟音频 | 按次/按时长 | 个位数元级 |
| TTS | ≤100 次 | ≈5000 字符 | 按字符 | 个位数元级 |

结论：真实冒烟总成本控制在 **十元量级以内**，且被配额机制硬性封顶（计数落盘持久化 +
超限自动降级 mock），不依赖人工自觉。

### 1.6 基线测量（所有 threshold 的参照物）

**原则：无基线的对比数据无效。** 所有场景的劣化曲线、劣化拐点、SLO threshold
均以本轮基线为参照物拟定（基线 + 预设劣化容忍度），见 §2.3。

基线环境：本机原生开发形态（观测栈容器化 + backend/agent/VAD 本机进程），
启动方式沿用 `docs/observability-local-guide.md` §2.1：

```bash
docker compose -f deploy/docker-compose.base.yml -f deploy/docker-compose.dev.yml --profile observability up -d
# 另起终端：vad、agent、backend 三个本机进程（各自 README/启动脚本）
```

基线采集项与产出表（结果记入 §2.3 基线表）：

| 项 | 场景 | 采集内容 | 采集手段 |
|---|---|---|---|
| B1 空载连接 | 建立 1 条 WS 连接，静置 10min | 单连接内存增量 / goroutine 数 / FD 数 | backend pprof + `/metrics`（otel-collector :8889） |
| B2 单会话轮次 | 1 会话串行 20 轮（mock provider） | 端到端轮次耗时 P50/P95、各阶段耗时占比（STT 尾点→LLM TTFT→TTS TTFB→播完） | Tempo 按 `echat.turn_id` 查 trace + Grafana 面板 |
| B3 agent 阶段占比 | 同 B2 | LLM TTFT、TTS TTFB 分位数（`echat_turn_llm_ttft` / `echat_turn_tts_ttfb`） | Prometheus |
| B4 真实冒烟基线 | 1 会话 3~5 轮真实 LLM/ASR/TTS（消耗配额） | 真实链路各阶段耗时，用于校准 mock provider 的 pacing 参数 | 同上 + §6 计数打印 |

基线完成后，mock provider 的时序参数（TTFT/TTFB 模拟延迟）按 B4 真实值校准，
保证 load/stress 场景下 mock 链路时序形态与真实链路同量级——这是「mock 压测数据可解释」的前提。

---

## 二、指标体系

### 2.1 指标总表

每个指标四列定位：**指标名 → 采集来源 → threshold 拟定方式 → 报告呈现形式**。
threshold 列在基线（§1.6）完成后填数值，规则见 §2.3。

**维度一：连接层（WebSocket 建连/断连风暴）——叙事核心**

| 指标 | 来源 | threshold 拟定 | 报告呈现 |
|---|---|---|---|
| 建连成功率（登录+upgrade 全程） | loadgen 客户端计时 | 基线 100%，容忍度 ≥99.9% | 表 + 结论句 |
| 建连耗时 P50/P95/P99 | loadgen 计时（含 JWT 签发） | 基线 × 容忍度 | 曲线 |
| 单机最大稳定连接数 | loadgen 阶梯加压 | 劣化拐点（成功率/内存斜率突变） | 数字结论句 |
| goroutine / 堆内存 / FD 随连接数曲线 | pprof + otel-collector | 线性度检查（超线性增长=泄漏信号） | 曲线 |
| 断连后资源回收（压前压后对比） | pprof | 压后回落到基线 ±10% | 表 |
| 登录限流触发次数 | Redis + backend 日志（Loki） | 观察项（解释建连失败） | 备注 |

**维度二：会话层（并发语音轮次）——叙事核心**

| 指标 | 来源 | threshold 拟定 | 报告呈现 |
|---|---|---|---|
| 端到端轮次耗时 P50/P95/P99（说完→AI 出声首帧） | loadgen + Tempo trace | 基线 × 劣化容忍度 | 劣化曲线 |
| LLM TTFT 分位数 | `echat_turn_llm_ttft`（Prometheus） | 与基线对齐（mock 恒定，偏离=编排层劣化） | 分层曲线 |
| TTS TTFB 分位数 | `echat_turn_tts_ttfb` | 同上 | 分层曲线 |
| 各阶段耗时占比漂移 | Tempo（按 `loadtest.run_id` 检索） | 观察项（定位劣化发生在哪一段） | 堆叠图 |
| gRPC 连接池（shards=4）排队等待 | backend agentcli 埋点/trace | 队列等待占比 >30% 判定池瓶颈 | 表 |
| agent 并发会话水位 | `echat_agent_sessions_active` | vs `max_sessions: 10000` 配置 | 曲线 |
| 轮次超时率（`turn_timeout_sec`） | backend 日志/指标 | >0.1% 判定不通过 | 表 |
| agent 侧队列堆积/会话串行锁等待 | agent trace（`turn_id` span 时长） | 观察项 | 曲线 |

**维度三：打断层（barge-in 风暴）——差异化加分项**

| 指标 | 来源 | threshold 拟定 | 报告呈现 |
|---|---|---|---|
| 打断响应延迟（ASR 出结果→TTS 停播 `drainFrameQueue`） | 新增埋点（trace attribute）+ loadgen 注入/观测时刻差 | 基线 × 容忍度 | 分位数曲线 |
| 打断成功率（gRPC CancelTurn 完成 + 播放态复位） | loadgen 校验 + backend 日志 | ≥99%（高压下降 tolerable，记录拐点） | 表 |
| 打断后下一轮历史裁剪正确性（`played_reply_prefix`） | agent 日志 + loadgen 断言 | 100% 正确 | 观察项 |

**维度四：数据面**

| 指标 | 来源 | threshold 拟定 | 报告呈现 |
|---|---|---|---|
| 房间广播时延 vs 房间人数放大曲线 | loadgen 接收端计时 | 放大系数线性（O(n) 预期） | 曲线 |
| dataplane ring 丢弃计数（DropOldest） | `backend/dataplane/counter.go` 运行快照 | 丢弃率 >0 均需解释 | 表 |

**维度五：loadgen 自观测（证明负载端不是瓶颈）**

| 指标 | 来源 | threshold 拟定 | 报告呈现 |
|---|---|---|---|
| loadgen CPU/内存/FD 预算余量 | loadgen 自身 metrics + 资源规划（§5） | 结论有效的前提：余量充足 | 每次报告必附 |
| 实际发送速率 vs 目标速率偏差（open-loop 时钟漂移） | loadgen metrics | 偏差 >5% 该 run 数据降级为参考 | 表 |
| loadgen 发送缓冲堆积 | loadgen metrics | 0 堆积 | 表 |

### 2.2 指标与可观测性体系的映射（联动机制，必须具体）

- **run id 打通**：每次压测 run 生成 `loadtest.run_id`（`日期-场景-序号`，如 `20261021-conn-01`），
  同名写入三处：trace attribute（loadgen 与被压服务间透传，经 WS 信封 `traceparent` 链路）、
  metrics label、Loki 日志字段。报告引用观测数据时一律按 run_id 过滤。
- **面板引用规范**：报告引用观测数据必须落到 `docs/observability.md` §3.4/§4.4 的具体指标名与
  Grafana 面板名（如 `echat_turn_llm_ttft` 面板），禁止「参见面板」式模糊引用。
- **采样开销声明**：观测栈遵循 `docs/observability.md` §4.5 约定（loadgen 为独立 OTel 进程
  `service_name=echat-loadbot`；正式容量采集时 `trace_sample_rate` 降至 0.01）。
  每份结果注明「是否在可观测性开启状态下采集」——采样开销本身是被测对象的一部分。
- **验证脚本**：复用 `docs/observability.md` §4.5 的综合验证脚本核对 run_id 三处打通。

### 2.3 threshold 拟定规则（拒绝拍脑袋）

```
threshold = 基线值（§1.6 实测） × 劣化容忍度
```

- 容忍度按指标类型预设并写明理由，例如：建连成功率 99.9%（可用性型）；
  轮次耗时 P99 ≤ 基线×2（体验型，实时语音可感知下限）；内存线性度 ±10%（资源型）。
- 每个 threshold 在报告中标注：基线值、容忍度、判定结果、是否触发降级解释。

---

## 三、参照系调研

### 3.1 参照系一：k6（grafana/k6）

**结构事实**（来源：k6 官方文档 Scenarios/Thresholds 章节及社区实践文章）：

- **场景组织**：`options.scenarios` 声明多个场景，每个场景独立绑定一个 JS 函数与负载执行器
  （executor）：`ramping-vus`（阶梯 VU）、`constant-arrival-rate`（恒定到达率）、
  `per-vu-iterations` 等；`setup()`/`teardown()` 做全局前置与收尾。
- **负载模型**：closed-loop（VU 循环）为主，`constant-arrival-rate` 可近似 open-loop。
- **threshold 机制**：指标上声明通过/失败阈值（如 `http_req_duration: ['p(99)<500']`），
  k6 退出码直接反映 SLO 判定，可挂 CI。
- **指标模型**：rate（比率）/ counter（计数）/ trend（分位数）三类，与 §2.1 指标表可一一映射。
- 社区已有 WS+gRPC 双向流服务压成 2.5 万连接的成例（ramping-vus + `gracefulRampDown`
  让会话优雅收尾），印证连接层风暴用 k6 完全可行。

**对本项目的适配成本**：

| 环节 | k6 适配成本 | 说明 |
|---|---|---|
| 登录换 JWT → WS `?token=` | 低 | setup() 里 HTTP 登录，token 传给 VU |
| WS JSON 信令（join/ping） | 低 | k6 原生 `ws` 模块直接发 JSON |
| gRPC backend↔agent | 不适用 | loadgen 不直连 agent（§1.2） |
| **RTP 音频上行（会话层驱动）** | **不可行** | k6 无 WebRTC/UDP 能力，无法模拟 pion 侧音频流 |
| **打断时序注入** | **不可行** | 需与音频上行耦合（§1.1），依赖 WebRTC |
| threshold → SLO 判定 | 低（可抄结构） | 自研 loadgen 的报告需自行实现等价判定 |

**结论**：k6 能覆盖连接层，但会话层/打断层被「无 WebRTC 能力」硬卡死——
这不是适配贵，而是能力空缺。此卡点即选型论据（§5.1）。

### 3.2 参照系二：livekit-cli `lk load-test`（同域容量规划视角）

**结构事实**（来源：livekit/livekit-cli 仓库）：

- `lk load-test --duration 1m --video-publishers 5 --subscribers 500` 一条命令模拟
  N 个发布者 + M 个订阅者，跑完打印逐订阅者报告（tracks/bitrate/latency/dropped%）。
- 视角是**容量规划**而非通用负载工具：按「角色」（publisher/subscriber）组织负载，
  报告直接回答「这台 SFU 带多少路」。

**借鉴点（抄结构）**：

1. **角色分离**：本项目对应 speaker/listener/AI 会话三种角色，loadgen 按角色组织 VU（自研时实现）；
2. **报告形态**：逐实例表格 + 总量结论，对齐 §2.1「报告呈现形式」列；
3. **一条命令一个结论**：参数全在命令行，run 可复现（§验收标准 6）。

**自论证部分**：livekit 压的是「媒体转发容量」，本项目核心叙事是「连接层 + 会话轮次」，
负载 profile 与 SLO 完全不同，只能抄报告与角色组织结构，不能抄负载模型。

### 3.3 对比表

| 维度 | k6 | livekit-cli load-test | 本项目自研 loadgen |
|---|---|---|---|
| 负载模型 | closed-loop VU / 可近似 open-loop | 固定角色拓扑 | 混合：连接层 closed-loop + 会话到达 open-loop |
| 场景组织 | scenarios + executors | 单命令角色配比 | Go 场景函数 + flag 参数 |
| 指标体系 | rate/counter/trend + threshold | bitrate/latency/dropped | §2.1 五维度 + JSON 报告 |
| WS JSON 协议适配 | 原生 | — | 复用 wire.go 事实（client 包） |
| WebRTC 音频 | ❌ 无能力 | ✅ | ✅（复用 pion，后续 commit） |
| SLO 判定 | threshold 内建 | 无 | 报告 JSON + §2.3 规则 |
| 性质标注 | 抄结构：executor/分级/threshold 形态 | 抄结构：角色分离 + 报告形态 | 自论证：负载 profile、SLO 数值、配额机制 |

## 四、压测场景分层设计

按面试价值排序（优先级口径与任务书一致：连接层 + 会话层是叙事核心，精力按此分配）。

### 4.1 连接层（叙事核心，已实现 ✅）

- **目的**：单机 backend 的 WebSocket 承载上限——Go 后端面试经典考题。
- **负载模型**：closed-loop 保持型（建连 → 保持心跳 → 断开），ramp 线性铺开到达。
- **参数**：users 阶梯（100/500/1000…）、ramp 30-90s、hold 5m、ping 25s（低于 WS 空闲超时）。
- **关键变体**：`per-user`（登录链路一起压，argon2 耗 CPU）vs `shared`（复用 token 纯压 WS 层），
  两者差值即登录链路的开销贡献——这是「你的数据怎么解读」的现成答案。
- **实现**：`loadtest/internal/scenario/conn.go`，指标输出对齐 §2.1 维度一。
- **面试价值**：支撑简历「单机 X 并发 WebSocket 连接，瓶颈是 goroutine/内存/FD」的数字结论句。

### 4.2 会话层（叙事核心，实现见 §7 commit C2）

- **目的**：N 个并发语音会话下端到端轮次耗时的劣化曲线。
- **负载模型**：**open-loop 会话到达**（到达率独立于系统状态，见 §5.2 论证），
  会话内 closed-loop 轮次（说完等回完再说下一句）。
- **链路**：loadgen 以 pion 发布 Opus 音频流（预录语料回放）→ SFU → VAD/ASR（mock）→
  agent（mock provider）→ TTS（mock）→ RTP 下行。
- **前置依赖**：backend 侧 ASR/TTS mock 钩子（§6.2，commit C2）。
- **面试价值**：支撑简历「Y 并发语音会话下轮次耗时 P99 保持 Z ms」+ 与 observability 面板交叉印证。

### 4.3 打断层（差异化加分，commit C3）

- **目的**：高压下打断响应延迟与成功率是否劣化。
- **关键设计**：打断不是客户端消息而是 ASR 结果触发（§1.1）。场景 = 会话层负载 +
  「TTS 出声中 1-3s 注入 ASR 识别结果」的精确时序（config 开关的测试钩子向
  `asr_cli.Results()` 注入，默认关闭）。
- **面试价值**：通用 IM 压测不覆盖的实时语音差异化叙事。

### 4.4 数据面 + soak（工程完备性，commit C4/C5）

- 数据面：大房间广播时延随人数放大曲线，dataplane ring 丢弃计数佐证。
- soak：会话层负载连续 ≥2h，观察泄漏（压前压后 goroutine/内存对比）。

## 五、技术选型论证

### 5.1 负载生成器：Go 自研 loadgen（已落地）

| 候选 | 结论 | 理由 |
|---|---|---|
| **Go 自研** | ✅ 采用 | 复用 `wire.go`/`SessionResult` 协议事实与 pion 生态；连接→会话→打断一个工具全覆盖；backend 团队语言一致 |
| k6 | 交叉验证选项 | 连接层可用（§3.1），被 WebRTC 卡死在会话层；保留作面试中「为什么不用 X」的防守答案 |
| vegeta | ❌ | HTTP 压测器，无 WS/WebRTC |
| ghz | ❌ | gRPC 压测器，loadgen 不直连 gRPC（§1.2） |

**追问预判**：
1. 「为什么不用 k6？大家压 WebSocket 都用它」→ 答：k6 覆盖连接层没问题，但本项目的会话层
   必须驱动 WebRTC 音频流（轮次由 ASR 结果触发），k6 无 WebRTC 能力是硬卡点不是成本问题；
   用两个工具不如一个自研覆盖全链路，且协议事实（JSON 信封）就在自己代码库里。
2. 「自研 loadgen 的数据可信吗？工具本身验证过吗」→ 答：smoke 场景先跑通全链路
   （登录/建连/进房/心跳），指标口径有 §2.3 的基线参照；loadgen 自身资源预算见 §5.3，
   报告必须附 loadgen 余量数据，否则结论无效。

### 5.2 负载模型：连接层 closed-loop + 会话到达 open-loop

- **连接层 closed-loop**：连接是稳态资源（用户挂着不走），固定 VU 保持是真实形态。
- **会话层 open-loop**：语音会话由用户发起，到达率与系统负载无关（饱和时用户照样来电）；
  closed-loop 会在系统变慢时自动降低到达率，**系统性低估饱和区的排队与劣化**。
  会话内轮次保持 closed-loop（说完等回完），因为单会话内这是真实行为。
- **到达模式**：泊松到达（指数间隔）近似随机用户；容量采集用恒定到达率阶梯（更易解读拐点）。

**追问预判**：
1. 「为什么连接层不用 open-loop？」→ 连接不是请求是长稳资源，恒定到达率会产生指数级
   在途连接堆积，测的是 ramp 速度不是承载上限；closed-loop 保持型才对应真实用户行为。
2. 「泊松到达参数哪来的？」→ 每会话轮次间隔取 8-20s（对话自然节奏，语料句子 2-6s +
   听回复时间），来源为语料清单实测时长 + 自论证；报告注明该假设，敏感性可在 C5 soak 验证。

### 5.3 loadgen 资源规划（证明 loadgen 不是瓶颈的前提）

- 连接层预算：单 WS 连接 ≈ 50KB（gorilla 连接 + 读写缓冲）+ 1 FD。1 万连接 ≈ 500MB 内存 +
  1 万 FD（Windows 下为 socket 句柄，建议压测机 `ulimit -n 65535`；Windows 由系统调优）。
  8C16G 单机跑到万级连接余量充足——**但报告必须附压测机实测 CPU/内存/FD 佐证**。
- 会话层预算：每 WebRTC 会话（DTLS + SRTP + Opus）≈ 4-8MB，单机模拟约 200-500 并发音频会话；
  更高并发需 loadgen 多实例分布式压测（预留：loadgen 支持以不同 user-prefix 多机并行，报告合并 JSON）。
- 自观测：loadgen 进程 CPU/内存/FD 与发送速率偏差（§2.1 维度五）每次 run 必附。

### 5.4 数据构造（可重复性）

- 测试账号：`loaduser_0001..N` / 固定密码，`-setup N` 幂等建号（已存在不中断）；
- 房间：连接层每连接独立房间（`LOAD%04d`），消除房间广播耦合，压的是纯连接层；
- 语料：`loadtest/mocks/real-smoke-corpus.txt` 固定 100 句，顺序消耗，天然可重复；
- run 复现：报告 JSON 的 params 记录全部参数 + run_id 规则（§2.2），同参数重跑即同量级 run。

## 六、Mock 策略与真实上游冒烟配额机制

### 6.1 Mock 策略四问（逐项）

**LLM — agent 内 mock provider（已落地 `agent/providers/mock.py`）**
- 动机：会话层压测必须走完整编排链路（orchestrator/会话/历史），进程内 mock 让 gRPC 全链路
  真实、只有 LLM 上游是假的；时序形态对齐真实链路（TTFT 300ms + 流式吐字）。
- 权衡：少了真实 LLM 的耗时抖动——用 pacing 参数 + B4 真实冒烟校准弥补。
- 替代方案：独立 mock gRPC server——拒绝，多一个组件多一份运维，且编排代码路径完全相同，
  收益为零（约束：同一问题域不堆叠两个工具）。
- 量化收益：负载场景上游成本归零；轮次耗时分布稳定可解读。

**ASR/TTS — backend 侧 config 开关 mock（commit C2，本 commit 未落地）**
- 动机：ASR/TTS 在 Go backend 直连云厂商（§1.3），无 provider 抽象，需显式配置开关的
  mock 实现（ASR 回放预录识别结果、TTS 回放预生成音频）。
- 权衡：改 backend 代码——以「显式配置项 + 默认关闭 + 文档说明风险」满足任务书钩子约束。

### 6.2 真实冒烟配额机制（LLM 侧已落地，成本红线强制执行）

实现文件：`agent/providers/quota.py` + `agent/config/loader.py`（`loadtest.real_smoke.*`）+ `agent/cmd/main.py`。

| 任务书要求 | 实现状态 |
|---|---|
| 开关默认关闭 + 文档说明风险 | ✅ `loadtest.real_smoke.enabled: false` 默认；`agent/config.yaml` 注释说明仅限 smoke |
| 计数持久化，重启不重置 | ✅ JSON 落盘 `counter_path`，进程启动恢复计数 |
| 超限自动降级 mock + 告警 | ✅ 先记账后调用（崩溃不超支），达上限自动切 MockProvider 并打 WARN |
| 范围限制（load/stress 真实 provider 不可达） | ✅ 双重隔离：`llm.provider: mock` 时不装配真实 provider；配额开关关闭时无 QuotaProvider——靠场景与配置隔离，不靠自觉 |
| 语料 100 句 ≤50 字 | ✅ `loadtest/mocks/real-smoke-corpus.txt`，按序消耗 |
| LLM 单轮一次计数 + 输入短句 | ✅ 一次 `chat_stream` 记 1 次；真实冒烟场景（C2）输入限定语料短句 |
| 前后计数打印 | ✅ 每次调用 INFO 打 `used/limit`，报告引用 |
| ASR/TTS 配额 | 口径预留于配置（asr/tts: 100），backend 侧钩子落地时（C2）复用同一机制设计 |

### 6.3 与可观测性的联动（对应 §2.2，机制已具备）

- `loadtest.run_id`：loadgen 报告 JSON 携带；WS join 帧的 `traceparent` 字段（wire.go 已预留）
  把 loadgen 侧 trace 与轮次 trace 关联；Prometheus/Loki 按 run 时刻窗口过滤。
- 正式容量采集遵循 `docs/observability.md` §4.5：loadgen 独立进程、`trace_sample_rate` 降 0.01。

## 七、落地架构与引入计划

### 7.1 目录与放置决策（已确认约定）

新建顶层 `loadtest/`（与 backend/agent 平级），结构见 `loadtest/README.md`。
与现状核对结论：现有 `test/`（natssrv 等）是手工实验目录而非测试体系，**不迁移**；
一次性辅助脚本沿放 `scripts/`；结论性文档只在本文件与 `docs/loadtest-report.md`。

### 7.2 引入任务清单（commit 粒度）

| # | 任务 | 改动范围 | 性质标注 | 验收方式 | 状态 |
|---|---|---|---|---|---|
| C1 | loadgen 骨架 + smoke/conn 场景 + agent mock provider + LLM 配额机制 | 新增 `loadtest/`；`agent/providers/`、`agent/config/`、`agent/cmd/main.py`、`agent/config.yaml` | 业界公认实践（配额守卫/先记账后调用）| smoke 场景跑通；`python -m pytest agent/test -q` 与 `go build ./...` 通过 | ✅ 代码就绪，smoke 待环境验证 |
| C2 | 会话层：backend ASR/TTS mock 钩子（默认关）+ loadgen pion 音频回放 + session 场景 | `backend/asr_cli`/`backend/tts_cli`（config 开关）、`loadtest/internal/scenario/session.go` | 对标 livekit 角色化压测（publisher 角色） | N 并发会话轮次耗时表产出 | ⬜ |
| C3 | 打断层：ASR 注入钩子 + interrupt 场景 + 打断延迟埋点 | `backend/sfu/asrdispatch.go` 周边钩子（config 开关） | 探索性质（无现成同类压测公开实践，自论证） | 打断延迟分位数 + 成功率表 | ⬜ |
| C4 | 数据面：大房间广播场景 | `loadtest/internal/scenario/broadcast.go` | 业界公认实践 | 放大曲线 + 丢弃计数表 | ⬜ |
| C5 | soak + 报告模板 + 容量采集（用户执行）+ `docs/loadtest-report.md` | `loadtest/`、`docs/loadtest-report.md` | 业界公认实践（soak + 泄漏对比） | 任务书 6 条验收标准逐条作答 | ⬜ |

### 7.3 运行分工与验收（与任务书一致，落地为 README 命令）

- AI 跑 smoke/小并发验证脚本正确性（README §1/§2）；
- 用户跑正式容量采集（README §2 逐级加压 + Grafana 截图，数字将写进简历）；
- AI 解读 `results/*.json` + 面板数据，撰写 `docs/loadtest-report.md`（数字结论句 + 面板名交叉印证 + loadgen 余量佐证）。

### 7.4 backlog（只压不修）

压测暴露的问题一律记入此处、不在本任务内顺手修业务代码：（待压测后填充）
