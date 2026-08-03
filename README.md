# eChat — 实时语音聊天平台

eChat 是一个基于 **WebRTC SFU + WebSocket 信令**的实时语音聊天系统。用户创建虚拟房间，通过 Go 后端 SFU 引擎实现多路音频转发，在浏览器内进行实时语音交流。系统内置 **AI 语音助手**，通过 VAD → ASR → LLM → TTS 流水线，以虚拟成员身份加入房间与用户对话

## 技术栈

| 层 | 技术 |
|----|------|
| 后端 | Go + gorilla/websocket + pion/webrtc/v4 + zap + gRPC |
| AI 服务 | Python（VAD: Silero ONNX / ASR: 阿里云 NLS / LLM: DeepSeek / TTS: 阿里云, iFLYTEK） |
| 前端 | React 18 + TypeScript + Vite + CSS |
| 基础设施 | Docker + Nginx + gRPC/Protobuf |

## 项目结构

```
echat/
├── agent/          # Python LLM Agent gRPC 服务
├── asr/            # Python ASR gRPC 服务[暂不可用]
├── vad/            # Python VAD gRPC 服务[暂不可用]
├── backend/        # Go 后端（信令 + SFU + gRPC 客户端）
├── proto/          # Protobuf 协议定义
├── frontend/       # React 前端
├── deploy/         # 部署配置（Docker Compose + Nginx）
├── designs/        # 设计文档
└── docs/           # 专项技术笔记
```

## 快速开始

**前置依赖**：Go ≥ 1.25、Node.js ≥ 18、Python ≥ 3.10

```bash
# 1. Python AI 服务（按需启动）
cd agent && python -m agent.cmd.main --config agent/config.yaml &      # LLM Agent

# 2. Go 后端（先复制 config.yaml 模板并编辑 gRPC 地址后）
cp backend/config.yaml.example backend/config.yaml 2>/dev/null || true
cd backend && go run .

# 3. 前端开发
cd frontend && npm install && npm run dev
```

后端 `localhost:8080`，前端 `localhost:5173` 代理至后端。生产部署详见 [deploy/](deploy/)。

## 架构概览

```
浏览器 ── WebSocket ──► Go 后端 ◄── gRPC ──► Python AI 服务
   │                      │                        │
   └─ WebRTC UDP ──────── SFU ◄── VAD→ASR→LLM→TTS
```

- **WebSocket 信令**：房间加入/离开、WebRTC SDP/ICE 协商、AI 启停
- **SFU 音频转发**：客户端发起 Offer，SFU 回 Answer 后建立连接，服务器接收并转发音频
- **AI 流水线**：SFU 从 RTP 提取音频 → ASR 识别 → LLM 生成回复 → TTS 合成语音 → SFU 注入房间

## 部署

```bash
cd deploy
docker compose --env-file .env -f docker-compose.prod.yml up -d
```

四个容器：gateway（Nginx TLS 终止）、frontend（静态站点 + 代理）、backend（信令 + SFU）、agent（LLM gRPC）。详细说明见 [`deploy/README.md`](deploy/README.md)。

## 后续

- **房间/用户状态持久化** 
- **对话记忆持久化**
- **语音唤醒、打断AI助手**
- **用户认证**
- **文字聊天**
- **录制回放**
- **视频通话**
