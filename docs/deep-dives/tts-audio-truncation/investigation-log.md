# TTS 语音排查追踪

> **目标**：TTS SDK 合成语音在网页端播放清晰完整
> **问题开始**：2026-07-26
> **问题解决**：2026-07-27
> **当前状态**：**✅ 已解决** — 尝试 #21 Frame Pacing（帧发送节流）确认有效，LLM 回复完整清晰可听。根因：TTS 合成后帧突发发送导致 Chrome NetEq 缓冲区溢出，中间帧被丢弃
>
> **进展**：
> - ✅ 声音清晰度已明显改善（不再模糊/失真）—— #8 WASM libopus 解决
> - ✅ 字节记账确认管线零丢失 —— #14 三级诊断日志
> - ✅ goroutine 泄漏已修复 —— #14
> - ✅ 播放不完整（开头+末尾可听，中间丢失）—— #21 Frame Pacing 解决
>
> **最终根因**：TTS 合成速度远超实时播放速度（5 秒音频在 ~1 秒内合成完成），编码循环在 tight loop 中瞬间把所有帧通过 WriteSample 发送，Chrome NetEq 抖动缓冲区（~200-500ms）溢出，中间帧被丢弃，只剩开头和末尾可听
>
> **最终修复**：引入 frameQueue + pacedSender goroutine，以 20ms ticker 定时发送帧，匹配实时播放速率。详见 [case-study.md](case-study.md)
>
> **环境**：
> - 出现问题的环境：Windows，浏览器播放 TTS 语音
> - Linux 环境：已通过 Docker 部署验证，Frame Pacing 方案在 Linux 下同样有效
> - 优先级：先在 Windows 上解决，再考虑 Linux 兼容性

---

## 已知可用模块（勿动）

> 以下模块已确认正常工作。排查期间**禁止修改**这些模块，避免引入新变量。

| 模块 | 状态 | 说明 |
|------|------|------|
| 用户麦克风 → 网页播放（真人语音） | ✅ 正常 | 清晰可听，音量正常 |
| WebRTC 信令 + 连接 | ✅ 正常 | 房间进出、ICE 协商正常 |
| ASR 语音识别 | ✅ 正常 | 能正确识别用户说话内容 |
| LLM 回复生成 | ✅ 正常 | 能正确流式返回回复文本 |
| 讯飞 TTS 合成返回音频 | ✅ 正常 | 能成功返回 PCM 16kHz 数据 |
| SFU 音频路由/转发 | ✅ 正常 | 用户间音频转发正常 |
| 浏览器 `<audio>` 播放 | ✅ 正常 | 真人语音路径播放正常 |
| Opus CBR 编码模式 | ✅ 正常 | VBR 模式会导致 ccgo 编码器损坏（已验证） |

### 问题范围（可动区域）

问题处于 TTS 音频链路中以下环节之一：

```
讯飞 TTS PCM 输出 → 字节序解析 → 16k→48k 上采样 → Opus CBR 编码 → RTP → WebRTC → 浏览器解码播放
              ↑                                                                              ↑
         能合成音频                                                                     ✅ 清晰度已改善
                                                                                         ❌ 播放不完整，丢失部分音频
```

### 排查约束

- **当前阶段**：跑通就行，不管性能优化
- **不允许改**：用户麦克风路径、ASR、LLM、VAD、前端非音频组件
- **允许改**：上采样算法、Opus 编码参数（仅 CBR 范围内）、字节序处理、PCM 帧对齐、SFU TTS track 配置、帧发送速率

---

## 管线快照

> ⚠️ 快照日期：2026-07-27 | 尝试 #21 之后
> ⚠️ 本章节描述当前代码的管线结构。若验证反馈与摘要描述矛盾，应以实际代码为准，并更新本快照

### TTS 音频到达网页的完整路径

```
TTS提供商（阿里云HTTP/讯飞WebSocket）
  → PCM 16kHz 小端序字节流
  → tts_cli 断句 + 句子级合成
  → audioOutCh（PCM 字节流, buf=256）

SFU runAITTSLoop 协程消费：
  → rawBuf 累积原始 16kHz PCM（200ms 阈值触发批量重采样）
  → resample 16k→48k（polyphase FIR, gunter-q12/resample）
  → pcmBuf（[]int16, 48kHz mono）
  → Opus CBR 编码（48kHz, 32kbps, complexity 10, VBR off, DTX off）
  → frameQueue（chan, cap=2000, 解耦编码与发送）
  → pacedSender goroutine（20ms ticker 定时发送）
    → aiTtsTrack.WriteSample（TrackLocalStaticSample, pion 管理 seq/ts/marker）
    → relay.WriteRTP（手动 RTP 包, 转发给其他客户端）

浏览器：
  → pc.ontrack 事件 → 解析 streamId
  → 按 sourceUserId 分组到 MediaStream
  → <audio> 元素 srcObject = stream → audio.play()
  → Chrome NetEq 抖动缓冲区（~200-500ms 容量）
```

### 与用户麦克风路径的关键差异

| 维度 | 用户麦克风 | TTS 合成语音 |
|------|-----------|-------------|
| 原始音频 | 浏览器 Opus 编码 | TTS 提供商 PCM 16kHz |
| 采样率转换 | 无（直通） | 16kHz → 48kHz polyphase FIR |
| Opus 编码位置 | 浏览器端 | SFU 后端 |
| 编码参数 | 浏览器默认 | 32kbps CBR complexity 10 |
| 发送速率 | 实时采集，天然 pacing | **批量合成，Frame Pacing 节流** |
| WebRTC track | 每个用户独立 | ai_<clientID> 独立 track |
| 音量控制 | 无增益处理 | 无增益处理（依赖 TTS 提供商 Volume 参数） |

### 背景参考

| 资源 | 链接 | 说明 |
|------|------|------|
| 讯飞 TTS API 文档 | https://www.xfyun.cn/doc/tts/online_tts/API.html | 当前使用的 TTS 提供商，WebSocket 协议，返回 PCM 16kHz raw |
| Opus 编解码 | https://opus-codec.org/docs/ | Opus 官方文档，包括编码参数建议和最佳实践 |
| Chrome WebRTC Internals | `chrome://webrtc-internals` | 浏览器端 WebRTC 诊断面板，可查丢包率、jitter buffer 延迟 |

### 排查假设池

以下为历史排查方向和当前状态：

1. ~~16kHz→48kHz 线性插值上采样是否引入失真~~ → 已替换为 polyphase FIR，排除
2. ~~Opus 编码参数是否适合合成语音~~ → 32kbps CBR complexity 10，已确定
3. ~~TTS 提供商输出音量是否偏低~~ → 与清晰度无关
4. ~~PCM 缓冲区帧对齐是否有边界问题~~ → 排除
5. ~~上游 ASR 或 VAD 信号是否干扰 TTS 链路~~ → 排除
6. ~~浏览器端自动播放策略或编解码兼容问题~~ → 排除
7. ~~ccgo 编码器缺陷~~ → #8 替换为 WASM libopus，已解决
8. ~~RTP 时间戳跳跃~~ → #15/#20 排除
9. ~~RTP Marker 位缺失~~ → #16 排除
10. ~~重采样边界失真~~ → #12/#13/#18 排除
11. **帧突发速率 → Chrome NetEq 缓冲区溢出** → #21 Frame Pacing，待验证

### 关键文件清单

| 文件 | 作用 |
|------|------|
| `backend/sfu/sfu.go` | `runAITTSLoop`：TTS 音频主循环，含 frameQueue + pacedSender |
| `backend/sfu/audio_encoder.go` | Opus 编码器（`jj11hh/opus` WASM 原生 libopus） |
| `backend/sfu/audio.go` | 编码参数预设（32kbps CBR）、PCM 工具函数 |
| `backend/tts_cli/service.go` | TTS 断句 + 分发 + 打断 |
| `backend/tts_cli/xfyun_synthesizer.go` | 讯飞 TTS WebSocket 合成 |
| `backend/tts_cli/aliyun_synthesizer.go` | 阿里云 TTS HTTP 流式合成 |
| `backend/tts_cli/provider.go` | Provider 接口 + SessionPipeline |
| `frontend/src/hooks/useVoiceRoom.ts` | WebRTC ontrack 处理、音轨分组 |
| `frontend/src/components/RemoteAudio.tsx` | `<audio>` 播放控制 |

---

## 尝试记录

> 按时间顺序排列。每次修改后追加一条记录。AI 应在动手前先读本章节全部历史，避免重复尝试。

### 尝试 #1 — 2026-07-26 12:50

| 字段 | 内容 |
|------|------|
| **假设/方向** | AI TTS 音轨的 `Channels: 0` 在 SDP 中非标准，WebRTC 标准 Opus 使用 `opus/48000/2`，`Channels: 0` 可能导致浏览器解码器配置异常 |
| **修改文件** | `backend/sfu/sfu.go` |
| **改动摘要** | `getAITtsTrack()` 中 RTPCodecCapability 的 `Channels: 0` 改为 `Channels: 2` |
| **验证结果** | 无效 |
| **结论/后续** | SDP 参数不是根因 |

---

### 尝试 #2 — 2026-07-26 13:00

| 字段 | 内容 |
|------|------|
| **假设/方向** | VBR 可变码率对语音编码质量更好，ccgo 编码器关 VBR 可能降低质量 |
| **修改文件** | `backend/sfu/audio.go` |
| **改动摘要** | `SetVBR(false)` 改为 `SetVBR(true)` |
| **验证结果** | **无效 — 引入严重问题** |
| **用户反馈** | "网页听到tts sdk返回的声音是尖锐爆鸣" |
| **结论/后续** | ccgo 转译 libopus 的 VBR 路径存在编码损坏，立即回退到 CBR |

---

### 尝试 #3 — 2026-07-26 13:10

| 字段 | 内容 |
|------|------|
| **假设/方向** | Dockerfile 配置了 CGo + opus-dev（原生 libopus），但代码中只有 ccgo 版编码器。缺少原生 libopus 导致编码质量差 |
| **修改文件** | 新建 `backend/sfu/audio_encoder_cgo.go`，修改 `backend/sfu/audio_encoder.go`、`backend/Dockerfile` |
| **改动摘要** | audio_encoder.go 加 `//go:build !opus_native`；新建 audio_encoder_cgo.go（CGo 原生 libopus, `//go:build opus_native`）；Dockerfile 加 `-tags opus_native`。Windows 自动走 ccgo 路径 |
| **验证结果** | Windows CGO_ENABLED=1 编译报错（缺 opus.h），CGO_ENABLED=0 通过。Docker 未验证 |
| **结论/后续** | CGo 路径在 Windows 不可行，需另寻方案 |

---

### 尝试 #4 — 2026-07-26 13:20

| 字段 | 内容 |
|------|------|
| **假设/方向** | 讯飞 TTS 返回的 PCM 字节序可能不是小端。若实际为大端而我们用小端解码，字节反转导致音频失真 |
| **修改文件** | `backend/sfu/sfu.go` |
| **改动摘要** | 首个 TTS 音频块新增：1) 原始 hex dump（前 20 字节）；2) LE 解码前 10 采样；3) BE 解码前 10 采样。PCM dump 改用 .pcm raw 格式（之前 .wav 打不开） |
| **验证结果** | 字节序确认为小端，排除该假设 |
| **结论/后续** | 确认 LE 正确，问题不在字节序 |

---

### 尝试 #5 — 2026-07-26 13:30

| 字段 | 内容 |
|------|------|
| **假设/方向** | 手写线性插值上采样不够专业，可能引入混叠失真导致"听不清"。AGENTS.md 要求用成熟库 |
| **修改文件** | `backend/sfu/audio.go`，`backend/sfu/sfu.go`，`backend/go.mod` |
| **改动摘要** | 引入 `gunter-q12/resample`（纯 Go polyphase FIR + Kaiser 窗）替换 `upsample16kTo48k`。`aiCall` 中创建 `resample.New(FormatInt16, 16000, 48000, 1, WithKaiserFastFilter())`，每个 TTS chunk 经 `resampler.Write(rawPCM)` 输出 48kHz PCM。移除 audio.go 中手写 upsampling 函数 |
| **验证结果** | 无效（用户反馈"还是听不清"，PCM 数据正常但音质未改善） |
| **用户反馈** | PCM 统计 min=-17030 max=13675 avgAbs=2685 正常，"还是听不清" |
| **结论/后续** | 重采样不是瓶颈，问题在 Opus 编码（ccgo 编码器），进入尝试 #6 |

---

### 尝试 #6 — 2026-07-26 13:40

| 字段 | 内容 |
|------|------|
| **假设/方向** | PCM 数据确认正常，问题在 Opus 编码。64kbps 远超 RFC 6716 推荐值（28-40kbps），可能触发 ccgo 编码器高码率路径 bug；complexity 5 偏低 |
| **修改文件** | `backend/sfu/audio.go` |
| **改动摘要** | `SetBitrate(64000)` → `SetBitrate(32000)`，`SetComplexity(5)` → `SetComplexity(9)`，对齐 RFC 6716 推荐值 |
| **验证结果** | 无效 |
| **结论/后续** | 编码参数调优不足以修复 ccgo 编码器缺陷 |

---

### 尝试 #7 — 2026-07-26 13:50

| 字段 | 内容 |
|------|------|
| **假设/方向** | ccgo 编码器 VoIP 模式可能有未知缺陷；更保守的参数组合（低码率+高复杂度+Audio 模式）可能避开有 bug 的代码路径 |
| **修改文件** | `backend/sfu/audio_encoder.go`，`backend/sfu/audio.go` |
| **改动摘要** | ApplicationVoIP → ApplicationAudio；SetBitrate 32000 → 24000；SetComplexity 9 → 10 |
| **验证结果** | 无效 |
| **结论/后续** | 所有 ccgo 参数组合均无效，需要替换编码器 |

---

### 尝试 #8 — 2026-07-26 13:56

| 字段 | 内容 |
|------|------|
| **假设/方向** | ccgo 转译 libopus 是根本瓶颈（16kHz 损坏、VBR 损坏、所有参数组合均无效）。替换为 jj11hh/opus（WASM 原生 libopus） |
| **修改文件** | `backend/sfu/audio_encoder.go`，`backend/sfu/audio.go`，`backend/go.mod` |
| **改动摘要** | 1) audio_encoder.go：kazzmir/opus-go(ccgo) → jj11hh/opus(WASM libopus)；2) audio.go：恢复 VBR=true + 32kbps + complexity 10；3) go mod tidy 移除 kazzmir/opus-go |
| **验证结果** | **部分有效** |
| **用户反馈** | "已经比较清楚了，至少能听到在说什么，继续优化，让它变得更清晰" |
| **结论/后续** | WASM 原生 libopus 解决了 ccgo 编码损坏的根本问题（从"完全听不清"到"能听懂"） |

---

### 尝试 #9 — 2026-07-26 14:00

| 字段 | 内容 |
|------|------|
| **假设/方向** | 32kbps VBR 对全频带（0-20kHz）编码偏保守，给编码器更多比特预算可保留更多语音谐波细节。同时显式关闭 DTX 避免静音检测误触发 |
| **修改文件** | `backend/sfu/audio.go`，`backend/sfu/audio_encoder.go`，`backend/sfu/audio_encoder_cgo.go` |
| **改动摘要** | 1) SetBitrate 32000→64000（全频带语音透明质量）；2) 接口+两套实现新增 SetDTX(false)（关非连续传输）；3) 更新注释 |
| **验证结果** | **部分有效** |
| **用户反馈** | "音质好像好了一点点，现在问题是对于llm返回的文字，网页端最终只听到了一部分" |
| **结论/后续** | 64kbps 小幅改善清晰度，但出现新问题——TTS 合成语音播放不完整。转向"数据丢失"方向排查 |

---

### 尝试 #10 — 2026-07-26 14:17

| 字段 | 内容 |
|------|------|
| **假设/方向** | 两个独立问题导致"只听到一部分"：(1) `sentenceCh` 永不关闭→`audioOutCh` 永不关闭→`pcmBuf` 尾部残帧永不发送；(2) `synthesizeSentence` 讯飞 API 错误静默丢弃整句 |
| **修改文件** | `backend/tts_cli/provider.go`，`backend/tts_cli/service.go`，`backend/sfu/sfu.go` |
| **改动摘要** | 1) provider.go：新增 `CloseSentenceCh()` 方法关闭句子通道；2) service.go：`ProcessText` 中 `IsFinal` 后调用 `CloseSentenceCh()`；3) sfu.go：`for range audioCh` 退出后刷新 `pcmBuf` 残帧（补零至 960 samples 编码发送）+ 增加 `framesEncoded` 计数器 + 诊断日志输出 `totalPCM`/`framesEncoded`/`tailSamples` |
| **验证结果** | **无效 — 引入新问题** |
| **用户反馈** | "一开始能听到语音，后面就听不到了" |
| **结论/后续** | `CloseSentenceCh` 导致旧 pipeline 销毁后新 utterance 创建新 pipeline，但 `ttsOnce` 阻止了新的消费者 goroutine。回退 CloseSentenceCh，改用超时方案 |

---

### 尝试 #11 — 2026-07-26 14:30

| 字段 | 内容 |
|------|------|
| **假设/方向** | 不关闭管道（保持复用），用空闲超时检测"一轮对话结束"来刷新 pcmBuf 尾部残帧。这样 pipeline 跨多次用户发言一直存活，audioCh 不会关闭，避免了 ttsOnce 问题 |
| **修改文件** | `backend/tts_cli/service.go`（回退 CloseSentenceCh），`backend/sfu/sfu.go`（for range → select+timeout） |
| **改动摘要** | 1) service.go：回退 CloseSentenceCh 调用，恢复原来的 `delete(ss.buffer, sessionID)`；2) sfu.go：`for rawPCM := range audioCh` → `for { select { case <-audioCh: 处理... case <-time.After(2s): 刷新 pcmBuf 残帧 } }`，2 秒无音频自动补零刷新残帧但继续等待下一轮，audioCh 关闭时 goto 到结束日志 |
| **验证结果** | 无效 |
| **结论/后续** | 管道复用方案未能解决播放不完整问题 |

---

### 尝试 #12 — 2026-07-26 17:30

| 字段 | 内容 |
|------|------|
| **本次尝试目标** | 让音频不截断完整播放 |
| **假设/方向** | 两个叠加因素：(1) resample 逐 chunk 重建滤波器→边界失真；(2) 超时补零帧破坏 Opus 编码器预测状态→后续帧严重失真/近无声。这解释了"开头听到、中间全丢、末尾又听到"的模式 |
| **修改文件** | `backend/sfu/sfu.go` |
| **改动摘要** | 1) 新增 rawBuf 累积原始 16kHz PCM，200ms（6400字节）阈值触发批量重采样；2) 编码循环提到 select 外作为公共路径，每次迭代都尝试从 pcmBuf 编码完整帧；3) 超时时只刷新 rawBuf→pcmBuf，pcmBuf 残帧**不补零**保留到下次迭代；4) 彻底消除补零帧，保护 Opus 编码器预测状态 |
| **验证结果** | **无效** |
| **用户反馈** | "仍然存在这个问题，只有开头和结尾部分语音" |
| **结论/后续** | 批量重采样和禁止补零帧不足以解决问题，说明滤波器边界失真和编码器补零不是主要根因。新假设：audioOutCh 缓冲满→讯飞 WS 读协程阻塞→WS 超时断开→中间音频丢失 |

---

### 尝试 #13 — 2026-07-26 18:15

| 字段 | 内容 |
|------|------|
| **本次尝试目标** | 让音频不截断完整播放 |
| **假设/方向** | 三个因素叠加：(1) audioOutCh 缓冲太小(32)→讯飞 WS 读协程阻塞→WS 超时断开→中间音频丢失；(2) resample.Write 每次新建滤波器→批次边界失真累积；(3) 缺少各阶段字节记账无法定位丢失点 |
| **修改文件** | `backend/tts_cli/provider.go`，`backend/sfu/sfu.go` |
| **改动摘要** | 1) provider.go：defaultAudioBufSize 32→256（7x 缓冲余量，减少阻塞概率）；2) sfu.go：新增重叠历史重采样（每批次前置 64 采样历史上下文，重采样后丢弃重叠输出，消除滤波器边界失真）；3) 移除逐 chunk PCM 幅度统计（减少日志噪音），保留首 chunk 字节序诊断；4) 新增 totalResampledBytes 计数器，结束日志输出 totalPCM/resampledBytes/framesEncoded/tailPCM 四维对比 |
| **验证结果** | **无效** |
| **用户反馈** | 完整文本"哈哈，你这是在夸谁呢？还是说你自己就是个小天使？"，网页只听到"哈哈，说你自己就是个小天使？"，中间"你这是在夸谁呢？还是"丢失 |
| **结论/后续** | 增大缓冲无效，排除"audioOutCh 满→WS阻塞"假设。重叠重采样也无效，排除"滤波器边界失真"假设。**两个主要假设均被证伪，需要全新排查思路**。下一步关键诊断：用 ffplay 播放 PCM dump 文件确认原始音频完整性 |

---

### 尝试 #14 — 2026-07-27 20:00

| 字段 | 内容 |
|------|------|
| **本次尝试目标** | 添加诊断日志定位音频丢失位置（上游讯飞/audioOutCh vs 下游重采样/编码/RTP）；修复 goroutine 泄漏 |
| **假设/方向** | 之前两个主要假设（滤波器边界失真、audioOutCh 阻塞）均被证伪。需要精确定位丢失发生在管线的哪个环节。deep-research agent 的分析报告指出：(1) aiCall 每 RTP 包泄漏一个 goroutine（sync.Once 永久阻塞）；(2) 汇总诊断日志从不输出（audioCh 永不关闭→goto audioLoopDone 永不执行）；(3) 缺乏按句子/按时间段的字节追踪 |
| **修改文件** | `backend/sfu/sfu.go`，`backend/sfu/model.go`，`backend/tts_cli/xfyun_synthesizer.go`，`backend/tts_cli/aliyun_synthesizer.go` |
| **改动摘要** | 1) **修复 goroutine 泄漏**：forwardRtp 中 ASR 解码改为独立 goroutine（每包），TTS 循环用 atomic.Bool 保证只启动一次；2) **aiCall → runAITTSLoop**：移除 sync.Once，移除内嵌 ASR goroutine；3) **三级诊断日志**：(a) 讯飞/阿里云句子级：每句标记 seq 序号+合成字节数；(b) SFU 周期诊断：每 10s ticker 输出 totalPCM/resampledBytes/framesEncoded/chunkCount/timeoutCount/缓冲区积压；(c) 超时事件日志；4) model.go：移除 aiCallReq 中不再使用的 rtpPacket 字段 |
| **验证结果** | **✅ 诊断能力建立 + 字节记账确认零丢失** |
| **结论/后续** | 日志证明管线中每个环节字节都对账——totalPCM、resampledBytes、framesEncoded 全部匹配。数据没有丢，但浏览器就是听不到。问题不在"数据对不对"，而在"数据怎么发"。这标志着排查思路从"改音频处理"转向"改发送行为" |

---

### 尝试 #15 — 2026-07-27 20:30

| 字段 | 内容 |
|------|------|
| **本次尝试目标** | 修复 RTP 时间戳停顿断层 |
| **假设/方向** | 诊断日志确认上游零丢失。根因假设在 RTP 时间戳：句子间有停顿（讯飞 WS 重连 + 2s 超时），停顿期间无帧编码→ts 冻结。但浏览器播放时钟一直在走，新帧到达时 ts 比浏览器时钟落后，jitter buffer 判定为"迟到包"丢弃。直到后续帧逐步追上播放时钟，末尾才能听到 |
| **修改文件** | `backend/sfu/sfu.go` |
| **改动摘要** | 1) 新增 `lastFrameWallTime` 变量；2) 编码每帧前检查停顿，超过 200ms 则计算停顿 RTP ticks 补齐 ts；3) 每帧编码后更新 `lastFrameWallTime` |
| **验证结果** | **无效** |
| **结论/后续** | RTP 时间戳跳跃不是根因，转向 Marker 位方向 |

---

### 尝试 #16 — 2026-07-27 21:00

| 字段 | 内容 |
|------|------|
| **本次尝试目标** | 修复 RTP Marker 位缺失导致浏览器 jitter buffer 丢弃 talkspurt |
| **假设/方向** | RFC 7587 要求每个 talkspurt 的首帧必须设 Marker=1。句子间有 300ms-13s 停顿，都是新 talkspurt。Chrome 的 NetEq 用 Marker 位识别 talkspurt 边界、重置解码器状态。没有 Marker 位时，NetEq 可能把时间戳跳跃后的新 talkspurt 帧当作"迟到包"丢弃或用 PLC 继续填充 |
| **修改文件** | `backend/sfu/sfu.go` |
| **改动摘要** | 1) 新增 `needsMarker` 标志；2) 时间戳补齐停顿后设置 `needsMarker = true`；3) RTP 包 Marker 字段改为 `needsMarker \|\| seq == 0`；4) 每帧发送后复位 `needsMarker = false`；5) `time.After(2s)` 替换为 `time.NewTimer(2s) + Reset` |
| **验证结果** | **无效** |
| **用户反馈** | "还是不行，听到的仍然不完整" |
| **结论/后续** | 手动 Marker 位修复不足以解决问题。用户建议使用成熟库而非自己实现 RTP 封装，转向 pion TrackLocalStaticSample（尝试 #17） |

---

### 尝试 #17 — 2026-07-27 21:30

| 字段 | 内容 |
|------|------|
| **本次尝试目标** | 使用 pion 成熟的 `TrackLocalStaticSample` 替代手动 RTP 封装 |
| **假设/方向** | pion 自身提供了 `TrackLocalStaticSample`，通过 `WriteSample(media.Sample{...})` 写入编码后的 Opus 帧，内部用 `rtp.Packetizer` 自动管理 seq/ts/marker/RTP 封装 |
| **修改文件** | `backend/sfu/sfu.go` |
| **改动摘要** | 1) `SFUPeer.aiTtsTrack` 类型 `*TrackLocalStaticRTP` → `*TrackLocalStaticSample`；2) `getAITtsTrack` 用 `NewTrackLocalStaticSample` 创建；3) 编码循环移除手动 `seq/ts/needsMarker/gapTicks` 和手动 `rtp.Packet` 构造，替换为 `aiTtsTrack.WriteSample(media.Sample{Data: opusPayload, Duration: 20ms, PrevDroppedPackets: 0})`；4) relay 轨保留手动 RTP；5) 新增 `github.com/pion/webrtc/v4/pkg/media` 导入 |
| **验证结果** | **无效** |
| **用户反馈** | "仍然不完整" |
| **结论/后续** | pion TrackLocalStaticSample 未解决问题。RTP 层不是根因。转向尝试 #18：问题在 resampler history/overlap 逻辑与内部状态冲突 |

---

### 尝试 #18 — 2026-07-27 22:30

| 字段 | 内容 |
|------|------|
| **本次尝试目标** | 让音频完整播放 |
| **假设/方向** | 经分析排除 RTP 层（#15/16/17 均无效）。根因在 resampler 的 history/overlap 逻辑：`gunter-q12/resample` 内部维护滤波器状态（流式处理），外部再拼接 history + 丢弃 overlap 输出造成双重补偿，每个批次边界引入相位不连续，跨多批次累积导致中间音频被 Chrome NetEq 丢弃 |
| **修改文件** | `backend/sfu/sfu.go` |
| **改动摘要** | 1) 删除 `overlapSamples`/`overlapBytes`/`history`/`upsampleRatio` 变量和常量；2) 批次重采样简化为直接 `resampler.Write(rawBuf)`；3) 超时刷新移除 history/overlap 逻辑；4) 超时刷新后用 `resample.New(...)` 重建 resampler |
| **验证结果** | **无效** |
| **用户反馈** | "仍然听不到完整的" |
| **结论/后续** | RTP 层（#15/16/17）和 resampler overlap 逻辑（#18）均已排除 |

---

### 尝试 #19 — 2026-07-27 20:00（实际时间晚于 #18）

| 字段 | 内容 |
|------|------|
| **本次尝试目标** | 让音频完整播放 |
| **假设/方向** | 截断问题首次出现在尝试 #9（码率 32→64kbps VBR）。64kbps VBR 下 Opus 帧尺寸波动更大，可能导致 Chrome NetEq 抖动缓冲区在突发帧到达时溢出触发全量丢弃。回退到已验证可用的 32kbps CBR 配置 |
| **修改文件** | `backend/sfu/audio.go` |
| **改动摘要** | `SetBitrate(64000)` → `SetBitrate(32000)`；`SetVBR(true)` → `SetVBR(false)`；更新注释说明回退原因 |
| **验证结果** | **无效** |
| **用户反馈** | 仍然不完整，中间部分仍大段听不到 |
| **结论/后续** | 截断根因不在码率/VBR，排除 #9 的码率变更嫌疑。继续排查 RTP 时间戳/NetEq 方向 |

---

### 尝试 #20 — 2026-07-27 20:15

| 字段 | 内容 |
|------|------|
| **本次尝试目标** | 排除 RTP 时间戳跳跃（PrevDroppedPackets/SkipSamples）是否为根因 |
| **假设/方向** | 停顿补齐（PrevDroppedPackets ≠ 0）会触发 pion SkipSamples 产生大时间戳跳跃，Chrome NetEq 收到后可能进入异常状态丢弃数据。移除所有停顿补偿，RTP 时间戳仅按帧递增（+960），不做任何跳跃 |
| **修改文件** | `backend/sfu/sfu.go` |
| **改动摘要** | 移除 `droppedPackets` 计算和 `lastFrameWallTime` 变量；`WriteSample.PrevDroppedPackets` 恒为 0；relay 轨不再做 relayGapTicks 补偿；relayMarker 仅首帧为 true |
| **验证结果** | **无效** |
| **用户反馈** | 还是不行 |
| **结论/后续** | 重采样（#12/13/18）、码率/VBR（#19）、RTP 时间戳跳跃（#15/16/17/20）三个方向均已排除。剩余方向：**帧突发速率与 Chrome NetEq 缓冲区匹配**——编码循环在 tight loop 中瞬间发送所有帧，讯飞合成速度可能远超实时，导致 NetEq 缓冲溢出丢弃。下一步应做帧发送节流（pacing），控制帧输出速率不超实时播放速率 |

---

### 尝试 #21 — 2026-07-27 19:45（最新）

| 字段 | 内容 |
|------|------|
| **本次尝试目标** | 让 TTS 合成语音完整播放，不再只听到开头和末尾 |
| **假设/方向** | **帧突发速率（Frame Burst）导致 Chrome NetEq 缓冲区溢出**。TTS 合成速度远超实时（例：5 秒音频在 ~1 秒内合成完成），`runAITTSLoop` 的编码循环在 tight loop 中瞬间把所有帧通过 `WriteSample`/`WriteRTP` 发送。Chrome NetEq 抖动缓冲区容量有限（~200-500ms），帧突发到达时缓冲区溢出，中间帧被丢弃，只剩开头和末尾可听。这是 WebRTC 流式播放预生成音频的经典问题 |
| **修改文件** | `backend/sfu/sfu.go` |
| **改动摘要** | 引入帧发送节流（Frame Pacing）机制，将编码与发送解耦：1) 新增 `frameQueue`（缓冲通道，容量 2000 帧 = 40s），编码后的 Opus 帧入队而非直接发送；2) 新增 `pacedSender` goroutine，以 20ms ticker 定时从队列取帧，通过 `WriteSample`/`WriteRTP` 以实时速率发送；3) `audioLoopDone` 中先排空 pcmBuf 残帧入队，再 `enc.Close()`，然后 `close(frameQueue)` 并 `<-senderDone` 等待排空；4) 移除 `defer enc.Close()` 改为显式调用，确保编码器在帧排空期间仍可用 |
| **验证结果** | **✅ 有效** — LLM 输出完整清晰可听 |
| **用户反馈** | "本次更新已经完全非常清晰且完整的听到llm输出内容了" |
| **结论/后续** | **根因确认**：帧突发速率超过 Chrome NetEq 缓冲容量是导致"开头+末尾可听、中间丢失"的根本原因。Frame Pacing 是正确的修复方向。21 次尝试中，前 20 次都在改音频处理（编码、重采样、RTP），第 21 次转向改发送行为才找到根因。后续若调整 pacing 参数：队列容量 2000 帧（40s）对最长 LLM 回复足够，20ms ticker 间隔匹配 Opus 帧时长，无需调整 |
