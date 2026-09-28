# WASM Opus 编码器内存损坏：排查追踪

> **目标**：找出本地 Windows `go run` 下随机 panic 的根因并根治
> **问题开始**：引入 `jj11hh/opus` WASM 编码器 +「人人能听 AI 说话」之后（本地反复 panic）
> **问题解决**：2026-09-21 — 移除 WASM 桥，本地与部署统一走 CGo 原生 libopus

## 证据链

- **崩溃 #1（opus 内部）**：`opus.(*Encoder).Encode` 空指针 @ `encoder.go:193`，调用源自 `runAITTSLoop`
- **崩溃 #2（TLS 建连）**：`fatal error: fault`，`context.WithCancel` 的父 context 接口数据指针为野地址 `0x10b76c4770`，栈在 gorilla websocket → 讯飞 TTS 的 `DialContext`
- **双路径**：部署 Dockerfile 恒带 `-tags opus_native`（CGo libopus，从未崩）；本地无标签走 WASM 桥（崩）
- **`jj11hh/opus` 架构**：全局单 wazero runtime + 单线性内存，全部编码器共享同一 malloc/free 堆，库内零同步，且每个 Encoder 注册了不经锁调 `Free` 的 GC finalizer；amd64 默认走 wazero 编译器引擎（JIT）

## 时间线

**2026-09-21**

1. 首次收到崩溃 #1，假设为「WASM 库无锁、多 TTS 会话并发踩共享堆」。
   改动：`audio_encoder.go` 封装层加包级 `sync.Mutex`，串行化构造/Encode/预设参数调用。构建通过。
2. 立刻收到崩溃 #2（TLS 野指针），与 opus 调用完全无关，`sp.ctx` 只写一次、纯 Go 竞态解释不了「不相干结构体字段变野地址」→ 推翻「并发踩堆」单一假设。
3. 全仓原生执行点盘点：WASM 构建路径无 CGo；唯一 JIT 原生代码执行 = `jj11hh/opus` 的 wasm 桥。野地址只能来自原生越界写 → 定位为 WASM 桥 JIT 级宿主内存损坏。
4. 验证互斥锁救不了该问题：串行化不影响 JIT 越界写；换解释器引擎则编码性能差数十倍，无法支撑 20ms 实时帧 → 弃用桥。
5. 方案对比（本地 cgo / WASM sidecar 隔离 / 本地走 Docker 拓扑），用户选定：统一走原生 libopus。
6. 落地：删 `audio_encoder.go`；`audio_encoder_cgo.go` 去 build tag 成默认；Dockerfile 去 `-tags opus_native`；`go mod tidy` 移除 `jj11hh/opus`/`wazero`/`zxcvbn-go`。
7. 本机装入 MSYS2 预编译 `mingw-w64-opus 1.6.1`（dll + import lib + 头文件到 `x86_64-w64-mingw32/include`）。
8. 验证：`go build ./...`、`go vet ./sfu/` 通过；`objdump` 确认链接 `libopus-0.dll` 四个符号；`go run .` 完整启动、无 panic。
9. 提交：`5938a7a fix(sfu): 移除 WASM opus 编码器`。文档补记于本轮。

## 结论

- **根因**：`jj11hh/opus` WASM 桥在 wazero 编译器引擎下越界写宿主 Go 堆，导致全进程任意位置随机崩溃；表现为两起互不相关的 panic（opus 内部、TLS 建连）
- **根治**：删除 WASM 桥，本地开发与部署统一使用 CGo 原生 libopus，一套编码器、行为一致、生产级
- **保留价值**：互斥锁思路对「并发踩共享堆」类问题成立，但于本根因无效；WASM sidecar 隔离（崩溃自愈）列为后续可选方向