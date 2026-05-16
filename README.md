# 🎙️ 语音房 Demo

两人实时语音对话的 WebRTC 演示项目

## 技术栈

- **后端**: Go + WebSocket (信令服务器)
- **前端**: React + TypeScript + Vite
- **通信**: WebRTC (P2P 直连)

## 功能

- ✅ 创建/加入语音房间
- ✅ 两人实时语音通话
- ✅ 麦克风开关控制
- ✅ 房间状态同步

## 快速开始

### 1. 启动后端

```bash
cd backend
go mod tidy
go run main.go
```

后端运行在: `http://localhost:8080`

### 2. 启动前端

```bash
cd frontend
npm install
npm run dev
```

前端运行在: `http://localhost:5173`

### 3. 测试

1. 打开两个浏览器窗口（或一台电脑 + 一部手机）
2. 都访问 `http://localhost:5173`
3. 输入相同房间号，都点击"加入房间"
4. 允许麦克风权限
5. 开始通话！

## 部署方案

### 方案 A: 云服务器 (推荐)

**需要的服务器配置:**

| 配置项 | 推荐 |
|--------|------|
| CPU | 2核 |
| 内存 | 2GB |
| 带宽 | 2Mbps+ (上行) |
| 系统 | Ubuntu 22.04 |

**部署步骤:**

1. 购买云服务器 (阿里云/腾讯云/华为云)
2. 安装 Docker:
   ```bash
   curl -fsSL https://get.docker.com | sh
   ```

3. 上传项目到服务器:
   ```bash
   scp -r voice-room-demo user@your-server:/opt/
   ```

4. 使用 Docker Compose 启动:
   ```yaml
   # docker-compose.yml
   version: '3.8'
   services:
     backend:
       build: ./backend
       ports:
         - "8080:8080"
       restart: always
     
     frontend:
       build: ./frontend
       ports:
         - "80:80"
       depends_on:
         - backend
   ```

### 方案 B: 内网穿透 (快速测试)

用于本地开发测试，无需公网服务器:

```bash
# 使用 ngrok
ngrok http 8080

# 会得到一个公网地址如: https://abc123.ngrok.io
```

修改前端 WebSocket 连接地址即可。

### 方案 C: TURN 服务器 (NAT 穿透)

解决复杂网络环境下的 P2P 连接问题:

```bash
# 使用 coturn
docker run --name coturn -p 3478:3478 -p 3478:3478/udp \
  -e TURN_SERVER_NAME=your-server \
  -e TURN_USER=username \
  -e TURN_PASSWORD=password \
  instrumentisto/coturn
```

## WebRTC 工作原理

```
1. 用户A 加入房间 → 信令服务器记录
2. 用户B 加入房间 → 信令服务器通知A
3. A 创建 RTCPeerConnection → 生成 offer
4. A 通过信令服务器发送 offer 给 B
5. B 收到 offer → 创建 answer
6. B 通过信令服务器发送 answer 给 A
7. 双方交换 ICE candidates
8. P2P 直连建立 → 开始传输音频
```

## 项目结构

```
voice-room-demo/
├── backend/
│   ├── main.go           # 入口 & HTTP 服务
│   ├── websocket.go      # WebSocket 信令
│   ├── room.go           # 房间逻辑
│   └── Dockerfile
│
└── frontend/
    ├── src/
    │   ├── App.tsx
    │   ├── components/
    │   └── hooks/
    ├── Dockerfile
    └── nginx.conf
```

## License

MIT
