# Agent 升级架构设计

> **读者对象**：假设你已经看过 `agent/cmd/main.py`，知道这是一个给语音聊天室提供 AI 回复的 gRPC 服务。本文档从零解释架构的每一层是什么、为什么这样分、它们之间怎么协作。

---

## 快速导航

如果你只有 5 分钟，先看这四节：
1. [当前代码长什么样](#1-出发点是这样一个服务) — 了解现状
2. [升级后目录结构](#3-升级后目录结构) — 一眼看清文件布局
3. [一条消息的完整路径](#42-一条消息的完整路径) — 请求从进到出经过哪些文件
4. [各层职责速查表](#5-各层详细设计) — 每个文件干什么

其余章节是详细展开，需要时再翻。

---

## 1. 出发点是这样一个服务

### 1.1 当前它在系统里的位置

```
浏览器 ──麦克风──► Go 后端 ──gRPC 双向流──► Python Agent ──HTTP──► DeepSeek API
                         ▲                                    │
                         │       流式 token 回传               │
                         └────────────────────────────────────┘
```

Go 后端把用户语音转成文字（ASR），通过 gRPC 发给 Python Agent。Agent 调 DeepSeek 生成回复，逐 token 流式回传。Go 后端拿到文字后做 TTS 合成语音，在房间里播放。

### 1.2 当前代码一瞥

```
agent/
├── cmd/main.py          # 全部 454 行代码都在这里
├── config.yaml          # 配置文件
├── requirements.txt     # 5 个依赖包
├── Dockerfile
├── pb/                  # protobuf 自动生成代码
└── tools/               # 空目录（预留但没用）
```

`main.py` 里塞了四件事：

| 类/函数 | 干什么 | 约多少行 |
|----------|--------|----------|
| `Config` | 从 YAML / 环境变量 / 命令行加载配置 | ~80 行 |
| `AgentSession` | 管一个对话：存历史（deque 最近 12 条）+ 调 OpenAI SDK 调 DeepSeek | ~60 行 |
| `LLMService` | gRPC Servicer：维护 `Dict[session_id → AgentSession]`，收请求、找 session、调 LLM、流式回传 | ~90 行 |
| `serve()` + `main` | 启动 gRPC server，信号处理 | ~90 行 |

### 1.3 当前架构的 8 个问题

| # | 问题 | 为什么是问题 |
|---|------|-------------|
| 1 | **纯对话，不会做事** | 用户问"今天天气怎样"——Agent 只能编一个答案，没法真的去查 |
| 2 | **不能打断** | AI 正在说话时用户插嘴——AI 不会停，继续把话说完。语音场景最痛的点 |
| 3 | **内存只涨不跌** | 每来一个新 session_id 就创建一个 AgentSession，从不删除。服务跑久了 OOM |
| 4 | **改一点牵动全身** | 454 行一个文件，加新功能 = 在已有的意大利面里再加一根 |
| 5 | **LLM 后端焊死** | 硬编码 `OpenAI(api_key, base_url)`。想换成 Ollama 本地模型？改代码 |
| 6 | **无健康检查** | Go 后端不知道 Agent 是不是还活着，出问题靠用户投诉 |
| 7 | **错误是内联文本** | LLM 调用失败时往流里塞 `"[LLM 调用异常]"`，Go 端分不清是网络问题还是 API key 过期 |
| 8 | **重启就失忆** | 对话历史全在内存。服务重启后用户回来，Agent 不记得刚才聊了什么 |

### 1.4 升级后要能做什么

- 换 LLM 后端只改一行配置
- 用户可以随时打断 AI
- AI 能调用工具（查天气、搜索网页）
- 会话自动过期清理，不会撑爆内存
- 有健康检查，Go 后端知道 Agent 状态
- 错误是结构化的，Go 端能区分处理
- 对话历史可以持久化（从 Phase 3 开始）
- 加新功能只需要新增文件或实现接口

---

## 2. 设计原则

### 2.1 四个硬原则

1. **一个目录 = 一层**：目录名叫什么，这一层就叫什么，不发明新名词
2. **下层不知道上层**：`core/` 不知道 `server/` 的存在；`providers/` 不知道自己在被谁调用
3. **接口隔离**：层与层之间通过抽象类打交道，`orchestrator.py` 依赖的是 `BaseLLMProvider`，不是具体的 `OpenAICompatibleProvider`
4. **向后兼容**：gRPC 协议只增字段不删字段，Go 后端不用动

### 2.2 技术约束

- Python ≥ 3.10，Docker 部署
- 继续用 gRPC（Go 端已深度集成，不换协议）
- 主并发模型从多线程改为 asyncio（gRPC aio + 流式 IO 更自然）
- 核心模块（`core/types.py`）零第三方依赖

---

## 3. 升级后目录结构

这是最重要的图。**每个目录对应一个架构层**，注释里标了"层名"：

```
agent/
│
├── cmd/
│   └── main.py                      # 【组装点】把各层拼起来，启动服务
│
├── server/                          # 【接口层】负责 gRPC 通信，不做业务
│   ├── __init__.py
│   ├── grpc_service.py              #   ChatStream RPC：收 proto → 调 core → 回 proto
│   ├── health.py                    #   HealthCheck RPC：返回服务健康状态
│   └── interceptors.py              #   拦截器：日志记录、异常转 status code、超时控制
│
├── core/                            # 【业务层】全部业务逻辑在这一层
│   ├── __init__.py
│   ├── orchestrator.py              #   编排引擎：把 LLM、工具、会话串起来
│   ├── session.py                   #   会话：一段对话的状态和生命周期
│   ├── types.py                     #   数据类型：ChatMessage、StreamEvent 等结构定义
│   └── strategies/                  #   自主循环策略（Phase 2+）
│       ├── __init__.py
│       ├── base.py                  #     BaseStrategy 抽象接口
│       ├── simple.py                #     简单模式：一问一答（当前行为）
│       ├── react.py                 #     ReAct：思考→行动→观察 循环
│       └── plan_and_solve.py        #     Plan-and-Solve：先规划再执行
│
├── providers/                       # 【能力层 - LLM】接入各种大模型
│   ├── __init__.py
│   ├── base.py                      #   BaseLLMProvider 抽象接口
│   └── openai_compatible.py         #   OpenAI 兼容实现（DeepSeek/Groq/Ollama 通用）
│
├── tools/                           # 【能力层 - 工具】让 LLM 能调用外部功能
│   ├── __init__.py
│   ├── base.py                      #   BaseTool 抽象接口
│   ├── registry.py                  #   ToolRegistry：工具注册表 + 执行器
│   └── builtin/                     #   内置工具
│       ├── __init__.py
│       └── web_search.py            #     示例：联网搜索
│
├── memory/                          # 【能力层 - 记忆】对话历史的存取
│   ├── __init__.py
│   ├── base.py                      #   BaseMemory 抽象接口
│   └── in_memory.py                 #   内存实现（当前行为）
│
├── config/                          # 【支撑层】配置加载
│   ├── __init__.py
│   └── loader.py                    #   YAML → 环境变量 → 命令行参数 三级加载
│
├── pb/                              # protobuf 自动生成代码（不改）
├── logs/                            # 日志文件
├── config.yaml                      # 默认配置文件
├── Dockerfile
└── requirements.txt
```

**对照表：目录 ↔ 层 ↔ 作用（一句话）**：

| 目录 | 层 | 一句话 |
|------|-----|--------|
| `server/` | 接口层 | 只做 proto ↔ 内部类型转换，不碰业务逻辑 |
| `core/` | 业务层 | 所有"怎么做"的决策都在这里 |
| `providers/` | 能力层 | 封装 LLM API 调用细节 |
| `tools/` | 能力层 | 封装可被 LLM 调用的外部功能 |
| `memory/` | 能力层 | 封装对话历史的存取 |
| `config/` | 支撑层 | 配置加载，被所有层使用 |
| `cmd/` | 组装点 | 创建对象、注入依赖、启动服务 |

---

## 4. 分层架构全景图

### 4.1 四层 + 三个能力模块

```
┌──────────────────────────────────────────────────────────────────┐
│                                                                  │
│   cmd/main.py  ←── 组装点：把下面各层创建出来，注入依赖，启动       │
│                                                                  │
├──────────────────────────────────────────────────────────────────┤
│                                                                  │
│                      接口层 server/                               │
│                                                                  │
│   ┌──────────────────┐ ┌──────────────┐ ┌─────────────────────┐  │
│   │ grpc_service.py  │ │  health.py   │ │  interceptors.py    │  │
│   │ ChatStream RPC   │ │ HealthCheck  │ │ 日志 · 错误 · 超时   │  │
│   │ proto ↔ StreamEvent││   RPC        │ │                     │  │
│   └────────┬─────────┘ └──────┬───────┘ └─────────────────────┘  │
│            │                  │                                   │
│            │  都只调 core/    │                                   │
│            ▼                  ▼                                   │
├──────────────────────────────────────────────────────────────────┤
│                                                                  │
│                      业务层 core/                                 │
│                                                                  │
│   ┌──────────────────────────────────────────────────────────┐   │
│   │                  orchestrator.py                          │   │
│   │                                                          │   │
│   │   这是整个 Agent 的大脑。                                   │   │
│   │   它不调 LLM API，不写 proto，不操作存储——只做编排。         │   │
│   │                                                          │   │
│   │   输入：用户文本                                           │   │
│   │   输出：StreamEvent 流（token / tool_call / error / done） │   │
│   │                                                          │   │
│   │   依赖（全是抽象接口，不是具体实现）：                         │   │
│   │   · BaseLLMProvider  ← 不管底层是 DeepSeek 还是 Ollama     │   │
│   │   · ToolRegistry     ← 不管注册了什么工具                  │   │
│   │   · BaseMemory       ← 不管数据存内存还是 Redis            │   │
│   │   · BaseStrategy     ← 不管用 Simple / ReAct / PlanSolve   │   │
│   └──────────┬──────────┬──────────┬────────────┬────────────┘   │
│              │          │          │            │                │
│   ┌──────────┼──────────┼──────────┼────────────┼────────────┐   │
│   │  session.py         │  types.py │ strategies/             │   │
│   │  · 对话历史          │  · ChatMessage  │ · simple.py       │   │
│   │  · 中断信号          │  · StreamEvent  │ · react.py        │   │
│   │  · 过期/淘汰         │  · SessionState │ · plan_and_solve  │   │
│   │  · 生命周期          │  · ToolCall     │   (Phase 2+)      │   │
│   └─────────────────────┴───────────────┴────────────────────┘   │
│                                                                  │
├──────────────────────────────────────────────────────────────────┤
│                                                                  │
│              能力层：三个独立模块，各自有抽象接口                   │
│                                                                  │
│   ┌──────────────────┐ ┌──────────────────┐ ┌────────────────┐  │
│   │   providers/     │ │    tools/        │ │   memory/      │  │
│   │                  │ │                  │ │                │  │
│   │ BaseLLMProvider  │ │ BaseTool         │ │ BaseMemory     │  │
│   │   ↑              │ │   ↑              │ │   ↑            │  │
│   │   │              │ │   │              │ │   │            │  │
│   │ OpenAICompat     │ │ WebSearch        │ │ InMemory       │  │
│   │ (DeepSeek/OpenAI │ │ GetTime          │ │ (未来: Redis   │  │
│   │  /Groq/Ollama)   │ │ RoomStatus       │ │  /SQLite)      │  │
│   └──────────────────┘ └──────────────────┘ └────────────────┘  │
│                                                                  │
├──────────────────────────────────────────────────────────────────┤
│                                                                  │
│                      支撑层 config/                               │
│                                                                  │
│   ┌──────────────────────────────────────────────────────────┐   │
│   │  loader.py: YAML → 环境变量 → CLI 参数 (优先级从低到高)    │   │
│   └──────────────────────────────────────────────────────────┘   │
│                                                                  │
│   日志 (loguru): 内建在 main.py 启动时配置，所有层直接用          │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘
```

### 4.2 一条消息的完整路径

这是理解整个架构最重要的图。跟着一条消息走一遍，就知道每个文件在链路里的位置。

用户说"你好"，经过的路径：

```
Go 后端 ──gRPC──► server/grpc_service.py
                        │
                        │ ① 收到 LLMRequest (proto 格式)
                        │    解析出 session_id + user_text
                        │
                        ▼
                  core/orchestrator.py
                        │
                        │ ② handle_message(session_id, "你好")
                        │    ├─ 从 session pool 获取/创建 Session
                        │    ├─ 更新 session.last_active_at
                        │    ├─ 从 memory 加载历史（如果有持久化）
                        │    ├─ 组装 messages = [system_prompt, history..., "你好"]
                        │    └─ 调 provider.chat_stream(messages, tools, cancel_event)
                        │
                        ▼
                  providers/openai_compatible.py
                        │
                        │ ③ 把 ChatMessage 转成 OpenAI 格式
                        │    调 self._client.chat.completions.create(stream=True)
                        │    逐 token yield 回来
                        │
                        ▼
                  core/orchestrator.py  (回到编排层)
                        │
                        │ ④ 每个 token 封装为 TokenEvent
                        │    遇到异常封装为 ErrorEvent
                        │    正常结束封装为 DoneEvent
                        │    （如果有 tool call，这里还会调 tools/registry.py）
                        │    全部 yield 给上层
                        │
                        ▼
                  server/grpc_service.py  (回到接口层)
                        │
                        │ ⑤ 把 StreamEvent 转回 LLMResponse (proto 格式)
                        │    逐个写入 gRPC stream
                        │
                        ▼
                  Go 后端收到流式回复
```

**一句话总结**：`server/` 收发包，`core/` 做决策，`providers/` 调模型，原路返回。

### 4.3 各层的依赖规则

```
server/ ──────► core/ ──────► providers/ (抽象接口)
                  │                │
                  ├──────────────► tools/    (抽象接口)
                  │                │
                  ├──────────────► memory/   (抽象接口)
                  │
                  ▼
              config/  ◄── 所有层都可以直接 import
              loguru   ◄── 所有层都可以直接使用
```

- `server/` 只知道 `core/` 的存在，不知道 `providers/`、`tools/`、`memory/`
- `core/` 依赖 `providers/`、`tools/`、`memory/` 的**抽象接口**，不依赖具体实现
- `providers/`、`tools/`、`memory/` 三者互不知晓，完全独立
- `config/` 和 `loguru` 属于基础设施，所有层都可以直接用

**为什么要这样分**：当你不需要关心某个具体实现时，它就是可替换的。比如想把 LLM 从 DeepSeek 换成 Ollama——只改 `providers/` 里一个配置指向，`core/orchestrator.py` 一行不动。

---

## 5. 各层详细设计

以下按目录逐个展开。每节结构：**这个文件属于哪一层 → 负责什么 → 它跟谁打交道 → 关键设计决策**。

---

### 5.1 `server/` — 接口层

**层定位**：系统的边界。外面是 gRPC 协议，里面是 Python 对象。这一层只做翻译，不参与任何业务决策。

#### `server/grpc_service.py`

| 项目 | 内容 |
|------|------|
| **职责** | 实现 proto 定义的 `LLMServiceServicer.ChatStream()` |
| **输入** | gRPC 双向流里的 `LLMRequest`（proto 格式） |
| **输出** | gRPC 双向流里的 `LLMResponse`（proto 格式） |
| **依赖** | 只依赖 `core/orchestrator.py` 的 `ChatOrchestrator.handle_message()` |
| **不做什么** | 不拼 messages、不调 LLM API、不管 session 生命周期 |

**为什么这个文件应该很薄**：如果以后想加一个 HTTP+SSE 的调试接口，只需要新写一个 HTTP handler，`core/` 以下的代码全部复用。

**ChatStream 方法的骨架逻辑**：

```
对每个收到的 LLMRequest:
  1. 解析: session_id, user_text, cancel 标志
  2. 如果 cancel=true → 调 orchestrator.cancel(session_id)
  3. 如果 user_text 非空 → 调 orchestrator.handle_message(session_id, user_text)
     得到 AsyncIterator[StreamEvent]
  4. 遍历 StreamEvent:
     TokenEvent    → 构造 LLMResponse(response_text=token, is_final=false)
     ToolCallEvent → 构造 LLMResponse(event_type="tool_call", ...)
     ErrorEvent    → 构造 LLMResponse(error_code=..., is_final=true)
     DoneEvent     → 构造 LLMResponse(is_final=true)
  5. 每个 LLMResponse 写入 gRPC stream
```

#### `server/health.py`

| 项目 | 内容 |
|------|------|
| **职责** | 实现新增的 `HealthCheck` RPC（unary，非流式） |
| **输入** | `HealthCheckRequest`（可选的服务名） |
| **输出** | `HealthCheckResponse`（status + active_sessions + provider_name + uptime） |
| **依赖** | `core/orchestrator.py` 的 `health()` 方法 |

**三种状态及含义**：

| 状态 | 含义 | 什么时候返回 |
|------|------|-------------|
| `SERVING` | 一切正常 | gRPC 在线 + LLM provider 最近一次调用成功 |
| `DEGRADED` | 服务在线但 LLM 不可用 | gRPC 在线但 LLM provider 最近一次调用失败（API key 过期/上游故障） |
| `NOT_SERVING` | 服务未就绪 | gRPC server 还没 start |

**谁会用到它**：Kubernetes 用 `SERVING`/`NOT_SERVING` 做 liveness probe。Go 后端可以定时 ping 来判断是否要把 Agent 从可用列表里摘掉。

#### `server/interceptors.py`

gRPC 的拦截器机制，类似 HTTP 中间件。三个拦截器按顺序执行：

| 拦截器 | 做什么 | 为什么需要 |
|--------|--------|-----------|
| `LoggingInterceptor` | 记录每个 RPC 的方法名、耗时、状态码 | 没有它，RPC 级别的日志需要每个方法自己写 |
| `ErrorHandlingInterceptor` | 捕获所有漏出去的异常，映射为 gRPC status code | 防止未处理异常让整个 stream 断开 |
| `TimeoutInterceptor` | 单次 RPC 超过 60 秒自动返回 `DEADLINE_EXCEEDED` | 防止单个慢请求占着连接不放 |

---

### 5.2 `core/` — 业务层

**层定位**：所有业务决策都在这一层。它不碰协议细节（那是 `server/` 的事），不碰 API 细节（那是 `providers/` 的事），只做编排。

#### `core/orchestrator.py` — ChatOrchestrator

这是整个 Agent 的**大脑**。类比餐厅：`server/` 是服务员（接待客人、传菜），`providers/` 是后厨（做菜），`orchestrator.py` 是主厨——决定什么时候做哪道菜、食材不够时怎么处理、菜做坏了怎么办。

**维护的数据**：

| 数据 | 类型 | 用途 |
|------|------|------|
| 会话池 | `Dict[str, Session]` | 所有活跃会话，按 session_id 索引 |
| LLM provider | `BaseLLMProvider` | 通过抽象接口持有，不关心具体实现 |
| 工具注册表 | `ToolRegistry` | 所有可调用工具 |
| 记忆后端 | `BaseMemory` | 对话历史的存取 |
| 池锁 | `asyncio.Lock` | 保护会话池的并发访问 |

**提供的方法**：

| 方法 | 做什么 |
|------|--------|
| `handle_message(session_id, user_text) → AsyncIterator[StreamEvent]` | 核心入口。接收用户文本，选择策略（simple/react/plan&solve），返回事件流 |
| `cancel(session_id)` | 中断指定会话正在进行的 LLM 生成 |
| `get_or_create_session(session_id) → Session` | 获取或惰性创建会话 |
| `evict_expired_sessions()` | 清理超过 TTL 的过期会话 |
| `health() → HealthStatus` | 聚合自身状态 + provider 状态，返回健康报告 |

**`handle_message` 内部做了什么**：

```
1. 从会话池获取/创建 Session
2. 更新 session.last_active_at
3. 如果配置了 memory，尝试 memory.load(session_id) 恢复历史
4. 从 session 取出对话历史
5. 拼装 messages = [system_prompt] + history + [user_msg]
6. 如果启用了工具，从 ToolRegistry 获取 tools schema
7. 调用 provider.chat_stream(messages, tools, cancel_event)
8. 遍历 provider 返回的事件:
   - TokenEvent: 直接转发给上层
   - ToolCallEvent: 调 ToolRegistry.execute() → 结果注入 messages → 回到步骤 7
   - ErrorEvent: 判断是否可重试 → 重试或转发
   - DoneEvent: 转发
9. 将完整的 assistant 回复写入 session.history
10. 异步调用 memory.save()（不阻塞流式回复）
```

**Tool Call 循环的安全阀**：单轮对话最多 5 轮 tool call，防止 LLM 进入"调工具 → 不满意 → 再调 → 不满意 → ..."的死循环。超过后强制终止。

**会话淘汰**：每次 `handle_message` 调用时惰性检查——遍历会话池，把 `last_active_at` 超过 30 分钟（可配置）的 session 移除。淘汰前尝试最后一次 `memory.save()`。

#### `core/session.py` — Session

**定位**：一个 Session 代表一段对话的运行时状态。注意区分——Session 是"运行时对象"，对话历史是"数据"。Session 持有对话历史，但对话历史的存储由 `memory/` 负责。

**包含的数据**：

| 字段 | 类型 | 用途 |
|------|------|------|
| `session_id` | str | 唯一标识，由 Go 后端传入 |
| `room_id` | str | 所属声聊间 |
| `state` | SessionState | 当前状态（见状态机） |
| `created_at` | float | 创建时间戳 |
| `last_active_at` | float | 最近一次活动时间戳 |
| `history` | deque[ChatMessage] | 对话历史，maxlen 可配置 |
| `cancel_event` | asyncio.Event | 中断信号 |
| `metadata` | dict | 扩展信息（如用户偏好、话题标签） |

**Session 状态机**：

```
                  创建会话
                     │
                     ▼
              ┌────────────┐
              │   ACTIVE   │ ◄──────────────────┐
              └─────┬──────┘                    │
                    │                           │
          ┌─────────┴──────────┐                │
          │                    │                │
    超过 TTL 无活动       收到 cancel 信号       │
          │                    │                │
          ▼                    ▼                │
    ┌──────────┐       ┌────────────┐           │
    │ EXPIRED  │       │ CANCELLING │           │
    └────┬─────┘       └─────┬──────┘           │
         │                   │                  │
    淘汰清理              当前生成终止           │
         │                   │                  │
         ▼                   └──────────────────┘
    ┌──────────┐
    │ REMOVED  │  (对象从池中移除，等待 GC)
    └──────────┘
```

- **ACTIVE → CANCELLING → ACTIVE**：瞬时转换，cancel 后 session 继续活跃，等待下一轮输入
- **ACTIVE → EXPIRED → REMOVED**：TTL 过期后从池中移除

**metadata 字段的规划**：预留的扩展字典，Agent 在对话中自然收集。例如用户说"我叫小明"→ `metadata["preferred_name"] = "小明"`；用户说"我在上海"→ `metadata["user_city"] = "上海"`。这些不需要显式的设置接口。

#### `core/types.py` — 共享数据类型

**定位**：项目的"通用语言"。所有层都使用这些类型来通信。这一层零第三方依赖，纯 dataclass + enum。

**核心类型**：

**ChatMessage** — 结构化消息（替代当前代码的裸 dict）：

```
ChatMessage
├── role: "system" | "user" | "assistant" | "tool"
├── content: str
├── tool_calls: list[ToolCall] | None      # assistant 发起的工具调用
├── tool_call_id: str | None               # 工具消息的关联 ID
└── name: str | None                       # 工具名称
```

**StreamEvent** — orchestrator 向上层产出的事件流（联合类型）：

```
StreamEvent
├── TokenEvent          content: str                          # 一个文本片段
├── ToolCallEvent       tool_call_id, tool_name, arguments    # LLM 要求调工具
├── ToolResultEvent     tool_call_id, result, is_error        # 工具执行结果
├── ErrorEvent          code, message, recoverable            # 异常
└── DoneEvent           cancelled, finish_reason, usage       # 本轮结束
```

**辅助类型**：
- `ToolCall`：`id + name + arguments` 三元组
- `ToolResult`：`tool_call_id + name + result + is_error`
- `HealthStatus`：`status + active_sessions + provider_name + uptime`
- `SessionState`：枚举 `ACTIVE | EXPIRED | REMOVED | CANCELLING`
- `TokenUsage`：`prompt_tokens + completion_tokens + total_tokens`

#### `core/strategies/` — 自主循环策略

**定位**：Tool Calling 让 LLM 能"用一个工具"，但真正让 Agent 变自主的，是**循环调用策略**——LLM 多次思考、多次调工具、自我纠错，直到完成任务。

这不是一个单独的工具，而是一种**编排模式**。当前代码的"收到消息 → 生成回复"是最简单的策略。ReAct、Plan-and-Solve 是更复杂的策略。

#### `core/strategies/base.py` — BaseStrategy

```
BaseStrategy（抽象基类，所有策略共享的接口）
├── name → str               # 策略名称，如 "simple"、"react"、"plan_and_solve"
├── description → str        # 策略描述
│
├── execute(                   # 执行策略，返回事件流
│     provider,                #   LLM 后端（BaseLLMProvider）
│     tool_registry,           #   工具注册表
│     messages,                #   当前对话上下文
│     cancel_event,            #   中断信号
│     config,                  #   策略相关配置（如最大轮次）
│   ) → AsyncIterator[StreamEvent]
│
└── supports_tools → bool    # 这个策略需要工具支持吗？
```

#### 三种策略对比

| 策略 | 文件 | 流程 | 什么时候用 |
|------|------|------|-----------|
| **Simple** | `simple.py` | 用户说话 → 调 LLM → 回复 | 日常闲聊，不需要查东西。这就是当前代码的行为 |
| **ReAct** | `react.py` | Thought → Action → Observation → Thought → ... → Final Answer | 需要查信息、调用工具。比如"帮我查一下今天天气，然后建议穿什么" |
| **Plan-and-Solve** | `plan_and_solve.py` | 先列计划 → 逐步执行 → 评估结果 → 最终回复 | 复杂多步骤任务。比如"帮我安排周末行程：先查天气，再找景点，最后推荐路线" |

#### ReAct 策略的详细流程

ReAct = **Re**asoning + **A**cting（推理 + 行动）。LLM 在思考和行动之间循环，直到得出结论。

```
用户: "北京今天适合户外运动吗"

  ReAct Loop:
  ┌─────────────────────────────────────────────────┐
  │ Thought: 我需要知道北京今天的天气和空气质量        │
  │ Action: 调 web_search("北京 今天 天气 空气质量")   │
  │ Observation: 北京今天晴，25°C，AQI 45（优）       │
  │                                                 │
  │ Thought: 天气晴朗温度舒适，空气质量优，适合户外    │
  │ Action: 不需要更多工具                            │
  │                                                 │
  │ Final Answer: "北京今天晴天25度，空气很好，         │
  │               非常适合户外运动！"                  │
  └─────────────────────────────────────────────────┘
```

**关键设计**：ReAct 循环的"观察结果 → 下一步思考"全部由 LLM 自身驱动。`react.py` 只负责：
1. 管理循环（最多 N 轮）
2. 检查 cancel_event（允许中断）
3. 执行 LLM 要求的工具
4. 将结果注入上下文
5. 判断是否到达终止条件

#### Plan-and-Solve 策略的详细流程

Plan-and-Solve 比 ReAct 多了一个**先规划**的步骤：

```
用户: "帮我安排周末北京两日游"

  Phase 1 — Plan（规划）:
  ┌─────────────────────────────────────────────────┐
  │ LLM 生成计划:                                    │
  │   Step 1: 查周末天气                             │
  │   Step 2: 根据天气推荐景点                        │
  │   Step 3: 查景点开放时间和门票                     │
  │   Step 4: 生成行程表                             │
  └─────────────────────────────────────────────────┘

  Phase 2 — Execute（执行，对每个 Step 用 ReAct）:
     Step 1: 查天气 → 结果: 周六晴、周日阴
     Step 2: 推荐景点 → 结果: 故宫、颐和园
     Step 3: 查信息 → 结果: 故宫 8:30-17:00，门票 60 元...
     Step 4: 生成行程

  Phase 3 — Answer（回答）:
     LLM 基于所有执行结果，生成最终回复给用户
```

**和 ReAct 的区别**：ReAct 边走边看（下一步做什么取决于上一步结果），Plan-and-Solve 先全局规划再执行。后者适合目标明确、步骤可预期的复杂任务。

#### 策略如何与 Orchestrator 配合

Orchestrator 不直接实现 ReAct 或 Plan-and-Solve 的逻辑。它根据配置选择策略，把策略需要的依赖注入，然后调用 `strategy.execute()`。

```
orchestrator.handle_message(session_id, text):
  │
  ├─ 获取 session, 拼装 messages
  ├─ 选择策略（从 config 读，默认 simple）
  │
  ├─ 如果 strategy == "simple":
  │     strategy = SimpleStrategy()
  │
  ├─ 如果 strategy == "react" 且 tools enabled:
  │     strategy = ReActStrategy(max_rounds=5)
  │
  ├─ 如果 strategy == "plan_and_solve" 且 tools enabled:
  │     strategy = PlanAndSolveStrategy(max_plan_steps=8)
  │
  └─ yield from strategy.execute(provider, tool_registry, messages, cancel_event, config)
```

**默认策略**：Phase 1 只有 simple，用户行为完全不变。Phase 2 引入工具后，默认仍是 simple（保持兼容），高级策略通过配置显式开启。

#### 策略的安全约束

| 约束 | 说明 |
|------|------|
| 最大循环轮次 | ReAct 默认 5 轮，Plan-and-Solve 默认 8 个 Step |
| 每轮超时 | 单轮 Thought→Action→Observation 不超过 30 秒 |
| 中断支持 | 每轮循环开始前检查 cancel_event |
| 策略降级 | 如果策略执行失败（如 LLM 始终不给 Final Answer），自动降级为 simple 策略回复 |

---

### 5.3 `providers/` — 能力层（LLM 后端）

**层定位**：封装不同 LLM 提供商的 API 差异，对上暴露统一接口。

#### `providers/base.py` — BaseLLMProvider

抽象接口，定义了 `core/orchestrator.py` 能对任何 LLM provider 做什么：

```
BaseLLMProvider（抽象基类，即 Python 的 `abc.ABC`，只定义接口不能直接实例化）
├── chat_stream(messages, tools, cancel_event)
│     输入: 对话上下文 + 可选工具列表 + 取消信号
│     输出: AsyncIterator[StreamEvent]
│
├── supports_tools() → bool          # 这个 LLM 支不支持 function calling
├── token_limit → int                # 上下文窗口大小（用于截断策略）
└── model_name → str                 # 当前模型名
```

**接口设计的要点**：
- `chat_stream` 接收 `cancel_event`：每个 provider 实现必须在流式循环中检查这个信号，一旦 set 就终止生成
- `tools` 参数为 None 时表示本轮不需要 function calling
- 返回的是 `StreamEvent` 而不是原始 API 响应：provider 内部做格式转换

#### `providers/openai_compatible.py` — OpenAICompatibleProvider

一套代码覆盖所有 OpenAI 兼容 API。DeepSeek、OpenAI、Groq、vLLM、Ollama——它们的 API 签名都是 `/v1/chat/completions`，区别仅在于 `base_url` 和 `model` 的值以及一些特性开关。

**与 config 的配合**：启动时读 `llm.base_url` 判断路由到哪个 provider。如果 base_url 是 `https://api.deepseek.com/v1`，就创建 `OpenAICompatibleProvider` 并标记 DeepSeek 特性（如不支持 vision）。如果 base_url 是 `http://localhost:11434/v1`（Ollama），同样用 `OpenAICompatibleProvider` 但标记可能不支持 tools。

**什么时候需要新增 provider 文件**：只有当某个 LLM 的 API 不是 OpenAI 兼容格式时（如 Claude SDK 的原生调用方式、Gemini 的原生 API），才需要新增一个 `providers/claude.py` 或 `providers/gemini.py`。

---

### 5.4 `tools/` — 能力层（工具系统）

**层定位**：让 LLM 不仅能说话，还能做事。封装一切可以被 LLM 调用的外部功能。

#### `tools/base.py` — BaseTool

```
BaseTool（抽象基类，只定义接口，子类必须实现 `execute` 方法）
├── name: str                       # 工具名（LLM 用这个来指定调哪个工具）
├── description: str                # 自然语言描述（LLM 据此判断什么时候调用）
├── parameters: dict                # JSON Schema 参数定义
│
├── execute(**kwargs) → str         # 执行工具，返回文本结果
└── to_openai_schema() → dict       # 转成 OpenAI function calling 格式
```

**为什么 execute 返回 str 而不是结构化对象**：因为工具的返回值最终会被放进 `role: "tool"` 消息里塞回 LLM。LLM 最擅长理解自然语言，所以返回纯文本是最简单且最可靠的方式。

#### `tools/registry.py` — ToolRegistry

```
ToolRegistry
├── register(tool: BaseTool)        # 注册工具
├── unregister(name: str)           # 移除工具
├── get(name: str) → BaseTool       # 按名查找
├── get_schemas() → list[dict]      # 获取所有工具的 OpenAI schema（给 LLM 用）
├── execute(name, args) → ToolResult# 执行指定工具
└── list_names() → list[str]        # 列出所有已注册工具
```

**安全约束**：
- 执行前用 JSON Schema 校验参数，不合法参数直接返回错误，不传给工具
- 单个工具执行超时 10 秒（可配置），超时后返回错误结果
- 网络请求统一设超时，防止被滥用

**工具权限分级**：

| 级别 | 说明 | 示例 | 启用策略 |
|------|------|------|----------|
| `safe` | 只读，无副作用 | `web_search`, `get_time` | Phase 2 默认启用 |
| `mutation` | 有副作用 | `set_timer`, 修改房间设置 | Phase 3+ 需要确认机制 |
| `sensitive` | 涉及外部系统 | 发邮件、操作数据库 | Phase 4+ 需要授权 |

#### `tools/builtin/` — 内置工具

Phase 2 规划的工具：

| 工具 | 做什么 | 参数 | 场景举例 |
|------|--------|------|----------|
| `web_search` | 联网搜索 | `query: str` | "帮我查一下明天北京的天气" |
| `get_time` | 获取当前时间 | 无 | "现在几点了" |
| `room_status` | 查询房间状态 | 无（从上下文取 room_id） | "房间里现在有谁在说话" |

---

### 5.5 `memory/` — 能力层（记忆持久化）

**层定位**：把对话历史从 Session 对象中分离出来，实现"服务重启后还能记得"。

#### `memory/base.py` — BaseMemory

```
BaseMemory（抽象基类，只定义接口，存储方式由子类决定）
├── load(session_id) → list[ChatMessage] | None
├── save(session_id, messages: list[ChatMessage]) → None
├── delete(session_id) → None
└── list_sessions(limit, offset) → list[str]
```

#### `memory/in_memory.py` — InMemoryMemory

一个 dict + deque，完全复现当前代码的行为：服务重启后数据丢失。是 Phase 1 的默认实现。

#### 将来的实现

| 实现 | 持久化 | 依赖 | 适用场景 |
|------|--------|------|----------|
| InMemoryMemory | ❌ 重启丢失 | 无 | 开发环境 / 不在意记忆 |
| SQLiteMemory | ✅ | 标准库 sqlite3 | 单机部署，轻量持久化 |
| RedisMemory | ✅ | redis-py | 多副本部署，共享存储 |

#### 与 orchestrator 的交互时机

```
Session 创建时:
  memory.load(session_id) → 如果有，恢复到 session.history
                          → 如果是 None，从空开始

每轮对话结束后:
  异步调用 memory.save(session_id, session.history)
  不阻塞回复流——用户不需要等存储写完才听到 AI 回复

Session 过期淘汰时:
  memory.save(...) 后从池中移除
```

---

### 5.6 `config/` — 支撑层

#### `config/loader.py`

从 `main.py` 抽离出来的配置模块。加载优先级（高到低）：**命令行参数 > 环境变量 > config.yaml**。

**完整配置结构**（新增字段用 ▶ 标记）：

```yaml
server:
  host: "0.0.0.0"
  port: 50053

llm:
  provider: "deepseek"          # ▶ 现在代码真的会读这个字段
  api_key: ""                   # 建议通过 LLM_API_KEY 环境变量设
  base_url: "https://api.deepseek.com/v1"
  model: "deepseek-v4-flash"
  system_prompt: |              # 语音助手的系统提示词
    你是用户的 AI 聊天伙伴 ...
  max_tokens: 1024
  temperature: 0.7
  request_timeout: 30.0         # ▶ 单次 LLM 请求超时秒数

session:                         # ▶ 新增配置段
  ttl_seconds: 1800             #   会话过期时间，默认 30 分钟
  max_history: 12               #   单会话最大历史消息数
  max_sessions: 10000           #   最大会话数上限

tools:                           # ▶ 新增配置段
  enabled: false                #   是否启用工具（Phase 2 打开）
  max_rounds: 5                 #   单轮 tool call 最大轮次
  tool_timeout: 10.0            #   单个工具超时秒数
  allowed_tools: []             #   允许的工具列表，空 = 全部可用

logging:
  level: "INFO"
  dir: "logs"
  retention_days: 7
```

---

## 6. 中断机制

### 6.1 为什么这很重要

语音和文字聊天的本质区别：文字是"发完等回复"，语音是"随时可能插嘴"。如果 AI 在说话时用户开口了，AI 必须尽快停下，否则就是两个人同时说话——语音交互里最差的体验。

### 6.2 中断在四个层面的协同

```
用户插嘴
  │
  ├─ ① [前端]    停止 TTS 播放 + 关掉 AI 音频轨道
  │
  ├─ ② [Go TTS]  清空待合成文本队列 + 中止当前合成
  │
  ├─ ③ [Go LLM]  通过 gRPC stream 发一条 cancel=true 的消息
  │
  └─ ④ [Python Agent] ← 本文档的范围
       │
       ├─ server/grpc_service.py: 解析出 cancel=true，调 orchestrator.cancel()
       ├─ core/orchestrator.py:   session.cancel() → cancel_event.set()
       ├─ providers/:             下一次 yield 前检测 cancel_event.is_set() → break
       └─ core/session.py:        未完成的回复不写入 history
```

### 6.3 中断后的一致性问题

| 数据 | 怎么处理 | 为什么 |
|------|----------|--------|
| 用户插嘴说的那句话 | 保留在 history 中 | 用户已经说出口了 |
| AI 被打断的那半句回复 | **不**写入 history | 没说完，下次不能当上下文 |
| AI 已生成的前几个 token（已发给 Go 端） | Go 端丢弃，TTS 不播 | 用户插嘴的瞬间 TTS 就停了 |
| LLM 的 HTTP 连接 | 关闭当前 stream | 下次请求建新连接 |

---

## 7. Tool Calling 流程

### 7.1 完整流程：从用户一句话到工具执行结果

以"上海明天天气怎么样"为例：

```
用户: "上海明天天气怎么样"

  Step 1  orchestrator 拼装上下文
         messages = [system_prompt] + history + [{"role":"user","content":"上海明天..."}]

  Step 2  orchestrator → provider.chat_stream(messages, tools=[web_search_schema])
         出: ToolCallEvent(tool_name="web_search",
                           arguments='{"query":"上海 2026-08-07 天气"}')

  Step 3  orchestrator → ToolRegistry.execute("web_search", {"query":"..."})
         出: ToolResult(result="上海 8月7日 多云 28-35°C")

  Step 4  orchestrator 把工具结果注入 messages
         messages.append({"role":"assistant","tool_calls":[...]})
         messages.append({"role":"tool","content":"上海 8月7日 多云 28-35°C"})

  Step 5  orchestrator 再次 → provider.chat_stream(messages, tools=[...])
         出: TokenEvent("上海") TokenEvent("明天") TokenEvent("多云") ...
             DoneEvent(finish_reason="stop")

  Step 6  更新 history，返回事件流
```

### 7.2 保护机制

| 保护 | 阈值 | 触发后 |
|------|------|--------|
| 最大 tool call 轮次 | 5 轮 | 强制终止，返回 DoneEvent(finish_reason="tool_loop_limit") |
| 单工具超时 | 10 秒 | 返回 ToolResultEvent(is_error=true) |
| 参数校验 | JSON Schema | 不通过不传给工具，带校验错误信息返回 |
| 工具不存在 | — | 返回 ToolResultEvent(is_error=true, "工具 X 不存在") |

---

## 8. 错误处理

### 8.1 分层处理策略

```
providers/ 层:
  API 调用失败 → 抛出 ProviderError 子类
  orchestrator 捕获 → 转成 ErrorEvent(code=..., recoverable=true/false)

tools/ 层:
  工具执行失败 → 返回 ToolResultEvent(is_error=true)
  LLM 自己决定怎么告诉用户

core/ 层 (orchestrator):
  判断 ErrorEvent.recoverable:
    true  → 重试 1 次（间隔 1 秒）
    false → 直接转发

server/ 层 (interceptor):
  所有漏出 orchestrator 的未处理异常
    → ErrorHandlingInterceptor 捕获
    → 映射为 gRPC status code
    → 记录完整堆栈
```

### 8.2 错误码速查

| 错误码 | 含义 | 可重试? | gRPC Status |
|--------|------|---------|-------------|
| `LLM_AUTH_ERROR` | API key 无效 | ❌ | `UNAUTHENTICATED` |
| `LLM_RATE_LIMITED` | 被限流 | ✅ | `RESOURCE_EXHAUSTED` |
| `LLM_TIMEOUT` | 调用超时 | ✅ | `DEADLINE_EXCEEDED` |
| `LLM_API_ERROR` | API 其他错误 | 看情况 | `INTERNAL` |
| `TOOL_NOT_FOUND` | 工具未注册 | ✅ | 走 ToolResult |
| `TOOL_TIMEOUT` | 工具执行超时 | ✅ | 走 ToolResult |
| `TOOL_ARGUMENT_ERROR` | 参数不合法 | ✅ | 走 ToolResult |
| `SESSION_NOT_FOUND` | 会话不存在 | ❌ | `NOT_FOUND` |
| `SESSION_POOL_FULL` | 会话池满 | ❌ | `RESOURCE_EXHAUSTED` |
| `INTERNAL_ERROR` | 未分类错误 | ❌ | `INTERNAL` |

---

## 9. 并发模型

### 9.1 从多线程切换到 asyncio

当前代码用的是 `grpc.server(ThreadPoolExecutor)` + `threading.Lock`。

升级后改为 `grpc.aio` + `asyncio.Lock`。原因：

- LLM API 调用是 IO 等待，asyncio 让一个线程能同时服务多个请求
- gRPC Python 有完整的 aio 支持
- asyncio 的协程模型避免了多线程的竞态条件问题

### 9.2 共享资源的保护

| 资源 | 保护机制 | 原因 |
|------|----------|------|
| orchestrator 的 sessions dict | `asyncio.Lock` | 多个协程可能同时创建/淘汰 session |
| 单个 Session 内部 | 不需要锁 | asyncio 是单线程，协程切换点可控 |
| Session.cancel_event | `asyncio.Event` | 自带线程安全 |
| ToolRegistry 的工具表 | `asyncio.Lock` | 注册是低频操作，锁开销可忽略 |
| memory 的存储 | 由具体实现负责 | 内存 dict 不需要锁；Redis/SQLite 有自己的并发机制 |

### 9.3 CPU 密集任务

如果某个工具需要做 CPU 密集计算（如图片处理），用 `asyncio.to_thread()` 丢到线程池，不阻塞事件循环。

---

## 10. Proto 变更

### 10.1 原则

- 已有 field number 不删、不改
- 新字段用靠后的 number
- 新 RPC 不影响旧 RPC

### 10.2 LLMRequest 新增

```protobuf
message LLMRequest {
  // ... 原有字段 1-6 不变 ...
  bool cancel = 7;        // 取消当前正在生成的回复
  string user_id = 8;     // 说话用户的业务 ID（预留，个性化用）
}
```

### 10.3 LLMResponse 新增

```protobuf
message LLMResponse {
  // ... 原有字段 1-6 不变 ...
  string error_code = 7;       // 错误码，空 = 正常
  string event_type = 8;       // "token" | "tool_call" | "tool_result"
  string tool_call_id = 9;     // 工具调用 ID
  string tool_name = 10;       // 工具名称
  string tool_arguments = 11;  // 工具参数 JSON
  string tool_result = 12;     // 工具结果文本
}
```

### 10.4 新增 HealthCheck RPC

```protobuf
service LLMService {
  rpc ChatStream(stream LLMRequest) returns (stream LLMResponse);
  rpc HealthCheck(HealthCheckRequest) returns (HealthCheckResponse);
}

message HealthCheckResponse {
  enum ServingStatus {
    UNKNOWN = 0;
    SERVING = 1;
    NOT_SERVING = 2;
    DEGRADED = 3;
  }
  ServingStatus status = 1;
  int32 active_sessions = 2;
  string provider_name = 3;
  double uptime_seconds = 4;
}
```

### 10.5 向后兼容

- 旧 Go 后端 → 新 Agent：不认识的字段（error_code, event_type 等）Go 端忽略，功能正常
- 新 Go 后端 → 旧 Agent：cancel、event_type 等字段旧 Agent 忽略（proto3 默认值），行为与升级前一致
- Python 和 Go 端可以独立升级，不互相阻塞

---

## 11. 与 Go 后端的配合

### 11.1 这次升级 Go 端不需要动

`ChatStream` 双向流的签名不变。Go 端 `backend/llm_cli/llm_cli.go` 一行不用改。

### 11.2 Go 端为了充分利用新能力需要做的（另行安排，不在本次范围）

| 改动 | 作用 | 优先级 |
|------|------|--------|
| 用户插嘴时发 `cancel=true` | 启用中断机制 | 高 |
| 定时 ping HealthCheck | 后端监控面板显示 Agent 状态 | 中 |
| 解析 `error_code` | 前端区分"网络问题"和"AI 暂时不可用" | 中 |
| 解析 `event_type=tool_call` | 前端显示"AI 正在搜索..." | 低（Phase 2 后） |

---

## 12. 迁移路径

从当前一个 `main.py` 拆成 8 个模块，分 5 步走。每一步都独立可测试，每一步都不破坏已有功能。

| 步骤 | 做什么 | 验证方法 |
|------|--------|----------|
| Step 1 | 把 Config 抽到 `config/loader.py` | 启动服务，配置加载行为不变 |
| Step 2 | 把 ChatMessage 类型定义到 `core/types.py` | 现有代码 import 新类型，行为不变 |
| Step 3 | 拆出 `core/session.py` + `providers/openai_compatible.py` | gRPC 双向流正常 |
| Step 4 | 拆出 `server/grpc_service.py` + `core/orchestrator.py` | 所有现有功能正常 |
| Step 5 | 加 Session TTL + HealthCheck + 错误协议 | Phase 1 完成，可上线 |

---

## 13. 实现优先级

### Phase 1 — 打地基

**目标**：代码可维护、可扩展、可观测。对外功能行为完全不变。

- 建立目录结构
- Config 模块化 + 新增 session/tools/logging 配置段
- Provider 抽象 + 当前 DeepSeek 实现
- Session 类（状态机 + cancel_event + TTL）
- ChatOrchestrator（会话池 + 编排 + 健康上报）
- gRPC service（proto ↔ 内部类型）
- HealthCheck RPC
- 拦截器（日志 + 错误处理）
- 错误结构化（ErrorEvent 替代 `"[LLM 调用异常]"`）
- 配置文件新增字段
- Dockerfile 适配新目录结构

### Phase 2 — 长能力

- Tool 抽象 + ToolRegistry
- 内置工具：`web_search`、`get_time`
- Tool Call 循环
- **ReAct 自主循环策略**（`core/strategies/react.py`）
- 中断机制
- Proto 新增字段 + Go 端配合

### Phase 3 — 提体验

- Memory 抽象 + InMemoryMemory
- SQLite / Redis 持久化
- 新增 Provider（Ollama、OpenAI）
- Metrics 暴露
- `room_status`、`set_timer` 工具

### Phase 4 — 高级场景（远期）

- 多 Agent 协作
- **Plan-and-Solve 策略**（`core/strategies/plan_and_solve.py`）
- RAG 知识库
- 结构化输出
- 流式中间状态
- Prompt 版本管理

---

## 14. 安全考量

| 方面 | 措施 |
|------|------|
| API Key | 当前 `config.yaml` 明文存了 `api_key: "sk-..."`，代码仓库里可见，是安全隐患。应该删除这行，改用 `LLM_API_KEY` 环境变量注入（`Config._load_from_env` 已支持）。当前日志没有打印 API key（`serve()` 只打 host/port/model/base_url，不打密钥），但为防以后新加日志时误打，应在日志配置里加一条过滤规则：自动把 `sk-` 开头的字符串替换为 `"***"` |
| 输入校验 | `user_text` 超长截断；`session_id` 只允许 `[a-zA-Z0-9_-]` |
| 工具安全 | Phase 2 只启用 safe 级别（只读、无副作用）。工具网络请求全部设超时 |
| 资源保护 | 单会话最大消息数上限、全局会话数上限、LLM 调用超时 |

---

## 15. 扩展点速查

想加新能力？看这里：

| 想做什么 | 动哪里 |
|----------|--------|
| 接入新 LLM（Claude、Gemini） | `providers/` 新增一个文件，实现 BaseLLMProvider |
| 让 AI 能调新工具 | `tools/builtin/` 新增一个文件，实现 BaseTool，在启动时注册 |
| 对话持久化到新存储 | `memory/` 新增一个文件，实现 BaseMemory |
| 换协议（如 HTTP+SSE） | `server/` 新增 HTTP handler，复用 `core/orchestrator.py` |
| 加新的 RPC | `server/` 新增方法，proto 加定义 |
| 加新的拦截器（如限流） | `server/interceptors.py` 新增拦截器类 |
| 加新的配置项 | `config/loader.py` 加字段 + `config.yaml` 加默认值 |
| 加新的自主循环策略 | `core/strategies/` 新增文件，实现 BaseStrategy，orchestrator 里注册 |

---

> **版本**：v2.0
> **最后更新**：2026-08-06
> **状态**：架构设计阶段，尚未实现
