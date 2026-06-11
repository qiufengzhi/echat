# eChat

eChat 是一个面向真实网页使用场景的轻量语音房项目。探索“AI 深度参与个人软件开发”的产品级练手项目：前端关注可上线的页面体验，后端提供 WebSocket 信令服务，实时通信使用 WebRTC 音频连接。


## 当前能力

- 创建或加入语音房。
- 通过邀请链接预填房间号。
- 请求麦克风权限并建立 WebRTC 音频连接。
- 展示多人席位、在线成员、静音状态和连接状态。
- 房主属性由后端维护，首位进入者自动成为房主。
- 房主离开前可以指定下一任房主，也可以让服务端自动选择。
- 房间内成员加入、离开、房主变化会通过 WebSocket 同步到前端。
- 前端提供首页、声聊间、离开页，以及邀请、设置、房主交接弹窗。
- 生产部署支持前端 Nginx、后端信令服务和外层 HTTPS 网关。

## 技术栈

- 前端：React 18、TypeScript、Vite、Nginx。
- 后端：Go 1.21、Gorilla WebSocket。
- 实时通信：WebRTC 音频、STUN、WebSocket 信令。
- 部署：Docker、Docker Compose、GitHub Actions、Nginx HTTPS 网关。

## 项目结构

```text
echat/
├── backend/
│   ├── main.go                  # 后端入口，注册 / 和 /ws
│   ├── handlers/                # HTTP 与 WebSocket 入口
│   ├── room/                    # 房间、成员、房主、信令转发逻辑
│   ├── ARCHITECTURE.md          # 后端架构说明
│   └── TECHNICAL.md             # 后端技术说明
├── frontend/
│   ├── src/
│   │   ├── App.tsx              # 页面流转和弹窗编排
│   │   ├── hooks/useVoiceRoom.ts
│   │   ├── services/            # 信令客户端和 WebRTC 信令处理
│   │   ├── pages/               # 首页、声聊间、离开页
│   │   ├── components/          # 房间组件和弹窗组件
│   │   └── types/               # 前端 UI 与信令协议类型
│   ├── vite.config.ts           # 本地开发代理与 HTTPS 配置
│   └── nginx.conf               # 前端容器内 Nginx 配置
├── designs/                     # 产品、页面和设计说明
├── deploy/                      # 生产 Docker Compose 与网关配置
└── AGENTS.md                    # AI 协作规则和项目上下文
```

## 本地开发

### 环境要求

- Go 1.21+
- Node.js 20+
- npm

### 启动后端

```bash
cd backend
go mod tidy
go run .
```

默认监听地址：

```text
http://localhost:8080
ws://localhost:8080/ws
```

后端可用环境变量：

```text
SERVER_ADDR=:8080
HTTPS_ENABLED=false
TLS_CERT_FILE=/path/to/fullchain.pem
TLS_KEY_FILE=/path/to/privkey.pem
```

通常本地和生产都不需要让后端直接开 HTTPS；本地由 Vite 代理，生产由 Nginx 网关处理 HTTPS。

### 启动前端

```bash
cd frontend
npm install
npm run dev
```

默认访问：

```text
http://localhost:5173
```

Vite 会把 `/ws` 代理到后端。相关配置在 `frontend/vite.config.ts`，可通过环境变量调整：

```text
VITE_DEV_PORT=5173
VITE_BACKEND_PROTOCOL=http
VITE_BACKEND_HOST=localhost
VITE_BACKEND_PORT=8080
```

如果需要在手机或非 localhost 环境测试麦克风，浏览器通常要求 HTTPS。可以参考 `frontend/.env.example` 使用 mkcert 证书开启本地 HTTPS：

```text
VITE_DEV_HTTPS=true
VITE_DEV_TLS_CERT_FILE=D:/program/ssl/localhost.pem
VITE_DEV_TLS_KEY_FILE=D:/program/ssl/localhost-key.pem
```

## 本地验证

前端构建：

```bash
cd frontend
npm run build
```

后端测试：

```bash
cd backend
go test ./...
```

手动体验建议：

1. 启动后端和前端。
2. 打开两个浏览器窗口，或一台电脑加一部手机。
3. 输入昵称，创建房间或加入同一个房间号。
4. 允许麦克风权限。
5. 观察成员席位、房主标记、邀请弹窗、静音和离开交接流程。

## 信令流程

前端和后端通过 `/ws` 交换统一信令消息。主要消息类型在 `backend/room/const.go` 和 `frontend/src/types/signaling.ts` 中维护。

常用消息：

- `join`：客户端请求加入房间。
- `waiting`：房间只有当前用户，服务端返回 `host_id`。
- `room_ready`：新加入者收到完整成员快照，可以开始创建 WebRTC offer。
- `user_joined`：房间已有成员收到新成员加入通知。
- `offer` / `answer` / `ice`：WebRTC 协商消息，后端只负责转发。
- `user_left`：成员离开，剩余成员移除对应席位并同步房主状态。
- `host_changed`：房主已变化，前端刷新房主标记和交接权限。
- `leave`：客户端主动离开，房主可携带 `next_host_id`。
- `ping` / `pong`：业务心跳，避免 WebSocket 空闲断开。

当前 WebRTC 协商约定：

1. 首位用户加入房间后收到 `waiting`。
2. 后加入者收到 `room_ready`，根据成员快照更新 UI，并主动创建 offer。
3. 已在房间内的成员收到 `user_joined`，只增量更新成员列表，等待后加入者发 offer。
4. 双方继续通过信令服务交换 `offer`、`answer` 和 `ice`。
5. 音频流到达后，前端隐藏的 `RemoteAudio` 组件负责播放远端声音。

## 当前实现边界

- UI 已按多人语音房组织，但音频连接仍处在以 WebRTC 点对点直连为核心的阶段。
- 当前前端只有一个 `RTCPeerConnection` 和一个远端音频流入口，真正多人稳定通话还需要继续设计多连接模型或引入 SFU。
- 默认只配置 STUN，没有内置 TURN；复杂 NAT 网络下可能需要自建 TURN 服务。
- 设置弹窗中的设备选择、降噪和自动增益已有 UI 表达，真实设备枚举和运行时切换仍可继续补全。
- 房间数据目前存在内存中，服务重启后房间状态会丢失。

## 生产部署

生产部署说明集中在 `deploy/README.md`。

当前生产编排使用：

- `gateway`：外层 Nginx，监听 `80` 和 `443`，负责 HTTPS 入口。
- `frontend`：前端静态站点容器，同时反向代理后端和 WebSocket。
- `backend`：Go 信令服务，只在 Compose 内部网络暴露 `8080`。

核心文件：

```text
deploy/docker-compose.prod.yml
deploy/nginx/gateway.prod.conf
frontend/Dockerfile
backend/Dockerfile
```

生产环境默认由外层 Nginx 处理 TLS，后端环境变量保持：

```text
HTTPS_ENABLED=false
```

## 相关文档

- `AGENTS.md`：项目级 AI 协作规则和产品定位。
- `designs/core-pages.md`：核心页面产品逻辑和状态含义。
- `designs/design-guidelines.md`：设计稿和前端实现规则。
- `designs/core-pages-design.html`：核心页面视觉设计稿。
- `designs/ai-development-blueprint.html`：AI 参与开发的协作蓝图。
- `backend/ARCHITECTURE.md`：后端结构说明。
- `backend/TECHNICAL.md`：后端技术细节。
- `deploy/README.md`：部署流程和服务器配置。

## 排查提示

- 麦克风无法打开：检查浏览器权限、设备占用、是否 HTTPS 或 localhost。
- 手机访问本地服务：需要前端 `server.host=true`，并确保手机和电脑在同一网络；非 localhost 通常还需要 HTTPS。
- WebSocket 连接失败：确认后端已启动，前端 `/ws` 代理目标是否正确。
- 能进房但听不到声音：检查浏览器麦克风权限、扬声器开关、NAT 网络环境和 ICE candidate 日志。
- 房主状态不对：优先查看 `waiting`、`room_ready`、`user_joined`、`user_left`、`host_changed` 的 payload。
