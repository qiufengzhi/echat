# eChat — 实时语音聊天平台

eChat 是一个基于 **WebRTC SFU + WebSocket 信令**的实时语音聊天系统。用户创建虚拟房间，通过 Go 后端 SFU 引擎实现多路音频转发，在浏览器内进行实时语音交流。系统内置 **AI 语音助手**，通过 VAD → ASR → LLM → TTS 流水线，以虚拟成员身份加入房间与用户对话

## 技术栈

| 层 | 技术 |
|----|------|
| 后端 | Go + gorilla/websocket + quic-go/webtransport + pion/webrtc/v4 + zap + gRPC |
| AI 服务 | Python（VAD: Silero ONNX / ASR: 阿里云 NLS / LLM: DeepSeek / TTS: 阿里云, iFLYTEK） |
| 前端 | React 18 + TypeScript + Vite + CSS |
| 数据 | PostgreSQL（ent ORM + Atlas 版本化迁移）+ Redis（限流/在线热状态）+ SpiceDB（Zanzibar 授权） |
| 基础设施 | Docker Compose + Nginx + NATS JetStream（事务性 Outbox 事件骨干）+ gRPC/Protobuf |

## 项目结构

```
echat/
├── agent/          # Python LLM Agent gRPC 服务
├── asr/            # Python ASR gRPC 服务[暂不可用]
├── vad/            # Python VAD gRPC 服务[暂不可用]
├── backend/        # Go 后端（双通道信令 + SFU + 授权/热状态接入）
├── proto/          # Protobuf 协议定义
├── frontend/       # React 前端
├── deploy/         # 部署配置（Docker Compose + Nginx + postgres-init）
├── designs/        # 设计文档
└── docs/           # 专项技术笔记
```

## 快速开始

**前置依赖**：Docker + Docker Compose、Go ≥ 1.26、Node.js ≥ 18、Python ≥ 3.10

```bash
# 1. 起基础设施（PostgreSQL + NATS + Redis + SpiceDB）
docker compose -f deploy/docker-compose.dev.yml up -d

# 2. 应用数据库迁移（首次或迁移文件变更后执行）
docker compose -f deploy/docker-compose.dev.yml run --rm migrate

# 3. Python AI 服务（按需启动）
cd agent && python -m agent.cmd.main --config agent/config.yaml &      # LLM Agent

# 4. Go 后端（配置缺省时自动用开发默认值，可按需复制 backend/config.yaml 调整）
cd backend && go run .

# 5. 前端开发
cd frontend && npm install && npm run dev
```

后端 `localhost:8080`，前端 `localhost:5173` 代理至后端。生产部署详见 [deploy/](deploy/)。

## 架构概览

```
浏览器 ── WebTransport(QUIC) 优先 / WebSocket 降级 ──► Go 后端 ◄── gRPC ──► Python AI 服务
   │                        │                          │
   └─ WebRTC UDP ───────── SFU ◄──── VAD→ASR→LLM→TTS ──┘
                                │
                 PostgreSQL / Redis / SpiceDB / NATS
```

- **双通道信令**：WebTransport（QUIC/UDP）优先，浏览器不支持或证书不可信时自动降级 WebSocket，两条路径共用同一套处理
- **SFU 音频转发**：客户端发起 Offer，SFU 回 Answer 后建立连接，服务器接收并转发音频
- **AI 流水线**：SFU 从 RTP 提取音频 → ASR 识别 → LLM 生成回复 → TTS 合成语音 → SFU 注入房间
- **基础设施**：PostgreSQL 事实层（ent + Atlas 迁移）+ 事务性 Outbox → NATS JetStream；Redis 承接登录限流与在线热状态；SpiceDB（Zanzibar）对象级授权

## 部署

生产环境由 GitHub Actions 自动构建镜像并下发配置；服务器上的后端配置以仓库内 [`backend/config.yaml.prod.example`](backend/config.yaml.prod.example) 为模板准备（连接地址 + 密钥占位），详见 [`deploy/README.md`](deploy/README.md)。如需在服务器手动拉起：

```bash
cd deploy
docker compose --env-file .env -f docker-compose.prod.yml up -d
```

`docker-compose.prod.yml` 编排业务服务（gateway Nginx TLS 终止、frontend 静态站点 + 代理、backend 双通道信令 + SFU、agent LLM gRPC）与基础设施服务（PostgreSQL 持久化、NATS 事件骨干、Redis 热状态、SpiceDB 授权）；容器内网互联，不向宿主机暴露端口。编排细节见 [`deploy/docker-compose.prod.yml`](deploy/docker-compose.prod.yml)。

## 后续

- **语音唤醒、打断AI助手**
- **文字聊天**
- **录制回放**
- **视频通话**
