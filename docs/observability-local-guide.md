# 本地指标查看与扩展指南

> 面向日常开发/调试：观测栈已经容器化后，怎么看指标、要看哪些、以及后续如何新增一个指标。
> 本文只讲操作；指标口径以 `backend/observability/registry.go` 的注册处为准。

---

## 一、架构一句话与数据流

应用**不暴露 HTTP `/metrics`**，统一走 OTLP 推送：应用 → Collector → Prometheus，Grafana 统一查询。

```
[Go backend]  ──OTLP gRPC :4317──┐
[Python agent] ──OTLP gRPC :4317──┤
[浏览器/前端]                      │
                                  ▼
                        [otel-collector]
                          ├─ metrics → prometheus exporter :8889
                          ├─ traces  → tempo :3200
                          └─ logs    → loki :3100
                                  │
                    [prometheus :9090] 抓取 :8889 与 node-exporter :9100
                                  │
                    [grafana :3000] ← Prometheus/Loki/Tempo 三数据源已自动 provision
```

要点：
- 指标导出周期：应用侧 15s（`metrics_export_interval`）+ Prometheus 抓取 15s（`scrape_interval`），即一个数据点从产生到可查最多约 30s 延迟
- Prometheus 本地保留 1 天（`--storage.tsdb.retention.time=1d`）
- node-exporter 采宿主机 CPU/内存/磁盘/网络，Prometheus 直抓，不经 Collector

---

## 二、启动与访问

### 2.1 启动观测栈（profile: observability）

```bash
# 原生开发（后端/agent 本机跑，只有观测栈进容器）：
docker compose -f deploy/docker-compose.base.yml -f deploy/docker-compose.dev.yml --profile observability up -d

# 全栈容器化（后端也在容器里）：
docker compose -f deploy/docker-compose.base.yml -f deploy/docker-compose.local.yml --profile observability up -d
```

### 2.2 访问入口

| 入口 | 地址 | 用途 |
|---|---|---|
| **Grafana** | http://localhost:3000 | 日常看板首选（匿名 Admin 登录） |
| Prometheus UI | http://localhost:9090 | 临时查指标、看抓取状态（Status → Targets） |
| Collector 原始指标 | http://localhost:8889/metrics | 直接 curl 看应用上报的原始 Prometheus 文本 |
| Tempo | http://localhost:3200 | trace 查询 API（一般走 Grafana 的 Explore） |
| Loki | http://localhost:3100 | 日志（一般走 Grafana 的 Explore） |
| node-exporter | http://localhost:9100/metrics | 宿主机资源指标原始文本 |

### 2.3 应用侧配置（决定数据能不能送到 Collector）

`backend/config.yaml` 的 `observability` 段：

```yaml
observability:
  enabled: true                    # 必须为 true，否则全部 instrument 绑 noop
  service_name: "echat-backend"
  otlp_endpoint: "localhost:4317"  # 原生 dev：容器端口映射到宿主机 → localhost:4317
                                   # 容器化（local/prod）：→ "otel-collector:4317"
  trace_sample_rate: 1.0           # dev 全采
  capture_content: false
  metrics_export_interval: "15s"
  logs_enabled: true
```

两种形态的差别只有 `otlp_endpoint`：后端跑在宿主机就填 `localhost:4317`（compose 已映射端口），跑在容器里就填服务名 `otel-collector:4317`（容器网络内）。

Python agent 同理，配置在 `agent/config.yaml` 的同名段。

---

## 三、怎么看各类指标

### 3.1 Grafana（推荐）

- 左侧 Dashboards → **Latency Budget**（已 provision，`deploy-config/projects/echat/grafana/dashboards/latency-budget.json`）：语音延迟七段分解、p50/p95
- Explore → Prometheus：自由写 PromQL
- Explore → Tempo：按 `echat.turn_id` 查一条 trace，看 Go↔Python 跨语言 span 树
- Explore → Loki：日志检索，日志行的 `trace_id` 可一键跳回 Tempo

### 3.2 指标速查（现有全部指标）

指标名进入 Prometheus 后点号转下划线：`echat.turn.e2e_latency` → `echat_turn_e2e_latency`（Collector 配了 `add_metric_suffixes: false`，不追加单位后缀）。

| 类别 | Prometheus 名 | 类型 | 说明 |
|---|---|---|---|
| 端到端延迟 | `echat_turn_e2e_latency` | histogram(ms) | ASR 句末 → 首块音频到达播发循环 |
| LLM 首 token | `echat_turn_llm_ttft` | histogram(ms) | ASR 句末 → 首 token 回 Go |
| TTS 首块 | `echat_turn_tts_ttfb` | histogram(ms) | 首句送入 provider → 首块音频 |
| 打断响应 | `echat_turn_interrupt_delay` | histogram(ms) | 新语音到达 → drain 完成 |
| 首帧编码 | `echat_turn_first_audio_encode` | histogram(ms) | 首块音频 → 首帧 RTP |
| 判停延迟 | `echat_turn_asr_final_delay` | histogram(ms) | 本地能量判停 → ASR 句末 |
| 轮次计数 | `echat_turns_total` | counter | label `end`=completed/interrupted/error |
| 在途轮次 | `echat_agentcli_inflight_turns` | gauge | |
| agentcli 故障 | `echat_agentcli_{retried,timedout,failed,rejected,cancelled,preempted}_total` | counter | |
| 房间/连接 | `echat_sfu_rooms_active`、`echat_sfu_peers_active` | gauge | |
| 重新协商 | `echat_sfu_renegotiations_total` | counter | |
| TTS | `echat_tts_requests_total`、`echat_tts_characters_total` | counter | label `provider`/`status` |
| ASR 用量 | `echat_asr_audio_seconds_total` | counter | |
| 媒体面故障 | `echat_sfu_{relay_write,opus_decode,ai_write}_errors_total` | counter | |
| Redis 降级 | `echat_redis_fallback_total` | counter | |
| RTCP 质量 | `echat_sfu_rtcp_jitter_microseconds`、`echat_sfu_rtcp_loss_percent` | histogram | label `kind`=relay/ai |
| RTCP 时延 | `echat_sfu_rtt_milliseconds` | histogram(ms) | 由 SR/RR 的 LSR 与 DLSR 反算 |
| TWCC 反馈 | `echat_sfu_twcc_feedback_total` | counter | 恒为 0 说明下行 transport-cc 反馈链不通 |
| 连续丢包 | `echat_sfu_burst_loss_total` | counter | label `len`=1/2/3plus |
| 下行码率 | `echat_sfu_tx_bytes_total` | counter | label `kind`=relay/ai |
| 断连 | `echat_sfu_peer_disconnects_total` | counter | label `state`=disconnected/failed |
| 进程 CPU | `echat_process_cpu_percent` | gauge | 仅 Linux 采集 |
| 客户端播放 | `echat_client_playback_delay_milliseconds` | histogram(ms) | 浏览器按入流上报，每条流一个样本 |
| 数据面水位 | `echat_dataplane_ring_backlog`、`echat_dataplane_ring_capacity` | gauge | label `queue`=聚合后的队列族 |
| 数据面丢弃 | `echat_dataplane_dropped` | gauge | label `counter`=计数/队列族 |
| Go runtime | `process_runtime_go_goroutines` 等 | gauge | OTel runtime 自动 |
| RPC RED | `rpc_server_duration` 等 | | otelgrpc 自动 |
| 宿主机资源 | `node_cpu_seconds_total`、`node_memory_*` 等 | | node-exporter，job=node |
| GenAI（agent 侧） | `gen_ai_client_*`、`echat_llm_ttft`、`echat_agent_sessions_active` 等 | | `agent/observability/metrics.py` |

### 3.3 常用 PromQL 示例

```promql
# 直方图分位数（所有 echat_turn_* 同构，改名字即可）
histogram_quantile(0.95, sum(rate(echat_turn_e2e_latency_ms_bucket[5m])) by (le))

# 注意：Prometheus 直方图桶名自动带 _bucket；本指标 unit=ms，
# 名字可能是 echat_turn_e2e_latency_bucket，以 :8889/metrics 实际输出为准

# 每分钟轮次速率，按结束方式分组
sum(rate(echat_turns_total[5m])) by (end)

# 打断占比
sum(rate(echat_turns_total{end="interrupted"}[5m])) / sum(rate(echat_turns_total[5m]))

# 数据面是否在丢弃（应长期为 0）
topk(5, echat_dataplane_dropped)

# 队列积压水位
echat_dataplane_ring_backlog / echat_dataplane_ring_capacity

# TTS 按 provider/status 的错误率
sum(rate(echat_tts_requests_total{status!="ok"}[5m])) by (provider, status)

# 宿主机 CPU 使用率
100 - avg(rate(node_cpu_seconds_total{mode="idle"}[5m])) * 100
```

### 3.4 排查：Prometheus 里查不到指标？

1. **确认观测栈在跑**：`docker compose -f deploy/docker-compose.base.yml --profile observability ps`，六个容器（otel-collector/tempo/prometheus/loki/grafana/node-exporter）应全部 Up
2. **确认抓取健康**：Prometheus UI → Status → Targets，`otel-collector` 与 `node` 都应为 UP
3. **直接看原始出口**：`curl http://localhost:8889/metrics | findstr echat`（Windows PowerShell 用 `Select-String echat`），有则说明应用→Collector→exporter 链路通，问题在 Prometheus 抓取；没有则继续
4. **确认应用观测开启**：`backend/config.yaml` 的 `observability.enabled: true`，且 `otlp_endpoint` 与运行形态匹配（原生 dev=`localhost:4317`，容器内=`otel-collector:4317`）
5. **确认数据已过导出周期**：应用 15s 导出 + 15s 抓取，最多等约 30s；指标只在有流量时产生数值（gauge 常驻，counter/histogram 需要对应事件发生）
6. **看 Collector 日志**：`docker logs echat-otel-collector-1`（容器名以 `docker ps` 为准），排查 OTLP 拒收/导出错误

---

## 四、如何新增一个指标

### 4.1 Go 后端（最常见）

指标集中注册在 `backend/observability/registry.go`，**Init 期一次性创建，热路径只 record 不 new**。三步：

1. **声明 instrument**（`registry.go` 顶部 var 块）：

```go
// MyNewCounter 某个新事件的累计次数
MyNewCounter metric.Int64Counter
```

2. **注册**（`initInstruments()` 内，紧跟现有写法）：

```go
MyNewCounter = mustInt64Counter(meter, "echat.mymodule.my_counter", "新事件累计次数")
```

带 label 的先看 §4.3 的规范；直方图用 `mustInt64Histogram(meter, "echat.xxx", "ms", "说明")`，gauge 用 `mustInt64Gauge`。

3. **在业务代码里 record**（import `echat-backend/observability`）：

```go
observability.MyNewCounter.Add(observability.RecordContext(), 1, metric.WithAttributes(
    attribute.String(observability.AttrStatus, "timeout"), // label key 用 attrs.go 常量
))
```

然后 `go build` 重启后端即可。**不需要改 Collector/Prometheus/Grafana 的任何配置**——新指标自动经 OTLP → :8889 → 被抓取，约 30s 后可在 `localhost:8889/metrics`、Prometheus 里查到。

4. （可选）要在 Grafana 面板展示：编辑配置仓的 `deploy-config/projects/echat/grafana/dashboards/latency-budget.json` 加 panel，Grafana 会自动热加载（provisioning 目录）。

### 4.2 Python agent

指标在 `agent/observability/metrics.py` 懒创建。新增同样三步：声明 getter/模块级变量 → 用 meter 创建（命名 `echat.agent.*` 或 GenAI semconv `gen_ai.*`）→ 在业务代码 record。同样无需改观测栈配置。

### 4.3 命名与 label 规范（必须遵守）

- **命名**：点分、前缀 `echat.`；时长统一毫秒、unit 经 `WithUnit("ms")` 表达；能对齐标准的用标准名（`gen_ai.*`），不自造私有含义
- **label（attribute key）只允许有限枚举值**：`provider`、`model`、`status`、`error_type`、`end`、`state` 等；key 常量一律收进 `backend/observability/attrs.go`
- **绝对禁止**把 `room_id / session_id / turn_id / user_id / client_id` 做成 metric label——一个 ID label 就是一条独立时间序列，房间一多 Prometheus 内存失控。ID 只能进 span attribute（用于 trace 关联）
- dataplane 的队列名天然带 ID（`sfu.asr.<session>`），桥接进指标前必须按「种类+前缀」聚合（现有 `RingBacklog` 等已处理，新队列照抄 `bridge.go` 的聚合逻辑）
- **热路径纪律**：`forwardRtp` 等每 20ms 一次的循环里只允许原子计数/直方图 record，零 span

### 4.4 新增一个独立的抓取目标（非应用指标）

例如要加一个数据库/中间件 exporter：

1. 容器加进 `deploy/docker-compose.base.yml` 的 observability profile（参照 node-exporter 的写法）
2. 在配置仓 `deploy-config/projects/echat/prometheus/prometheus.yml` 加一个 scrape job：

```yaml
  - job_name: my-exporter
    static_configs:
      - targets: ["my-exporter:9104"]
```

3. 重启观测栈：`docker compose ... --profile observability up -d --force-recreate prometheus`

### 4.5 新增 observability 自身的容器/改 Collector 行为

所有观测栈配置都在**独立配置仓** `d:/programProject/deploy-config/projects/echat/`（compose 经 `OBS_DIR` 环境变量挂载，默认指向上级的这个目录）：

| 改什么 | 文件 |
|---|---|
| OTLP 分流、尾采样阈值、PII 剥离规则 | `otel/collector-config.yaml` |
| 抓取目标、抓取间隔 | `prometheus/prometheus.yml` |
| 数据源、日志↔trace 跳转字段 | `grafana/provisioning/datasources/datasources.yml` |
| 面板 | `grafana/dashboards/*.json` + `grafana/provisioning/dashboards/dashboards.yml` |
| Tempo/Loki 本身 | `otel/tempo.yaml`、`otel/loki-config.yaml` |

改完对应组件需 recreate 该容器才能生效（provisioning 的 dashboard/datasource 是挂载只读文件，Grafana 会周期热加载，一般不用重启）。

---

## 五、注意项

- Prometheus 本地只留 **1 天**数据，跨天的趋势看不到；要留痕先把面板截图或导出
- `capture_content: true` 只允许本机调试；Collector 会兜底剥 `echat.pii.*` attribute，但日志正文剥不了——正文永远不放对话内容
- 修改 `collector-config.yaml` 后必须 recreate otel-collector；改尾采样阈值会影响 Tempo 里能看到的 trace 数量
- Grafana 是匿名 Admin（本地形态），不要把 3000 端口暴露到公网
