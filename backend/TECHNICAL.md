# Voice Room Backend 技术文档

## 1. 项目简介

Voice Room Backend 是一个 **WebRTC 信令服务器**，负责在浏览器之间中转 WebRTC 信令，实现两两语音通话。后端本身不处理音视频流，仅做信令透传。

| 项目       | 值                        |
| --------- | ------------------------- |
| 语言       | Go 1.21                   |
| WebSocket | gorilla/websocket v1.5.1  |
| 用户 ID   | google/uuid v1.5.0        |
| 端口       | 8080 (可配置)              |

---

## 2. 目录结构

```
backend/
├── main.go                 # 入口: 路由注册、服务启动
├── handlers/
│   ├── index.go            # GET / 返回 HTML 状态页
│   └── websocket.go        # GET /ws 升级为 WebSocket
├── room/
│   └── room.go             # 核心: 房间管理、信令转发、连接生命周期
├── Dockerfile              # Docker 镜像构建
├── ARCHITECTURE.md         # 架构图
└── TECHNICAL.md            # 本文档
```

---

## 3. HTTP 路由

| 方法  | 路径    | 处理函数                         | 说明                       |
| ---- | ------- | -------------------------------- | -------------------------- |
| GET  | `/`     | `handlers.IndexHandler`          | 返回 HTML 状态页面（欢迎页） |
| GET  | `/ws`   | `handlers.WebSocketHandler`      | 升级 HTTP 为 WebSocket 连接 |

---

## 4. 数据结构定义

### 4.1 Message（信令消息）

前后端约定的消息格式，`Payload` 使用 `json.RawMessage` 延迟解析，后端不关心 WebRTC 具体内容。

```json
{
  "type": "join",
  "room_id": "room123",
  "user_id": "uuid-xxx",
  "payload": { ... }
}
```

| 字段      | 类型               | 说明                     |
| --------- | ------------------ | ------------------------ |
| `type`    | `string`           | 消息类型（见第 5 节）     |
| `room_id` | `string`           | 房间 ID，客户端传入       |
| `user_id` | `string`           | 用户 ID，后端生成         |
| `payload` | `json.RawMessage`  | 可变载荷，按消息类型不同   |

### 4.2 Client（客户端）

```go
type Client struct {
    ID        string           // UUID 唯一用户 ID
    RoomID    string           // 当前所在房间 ID
    Username  string           // 昵称
    Conn      *websocket.Conn  // WebSocket 连接
    Send      chan []byte      // 发送队列 (缓冲 256)
    closeOnce sync.Once        // 确保断连清理只执行一次
}
```

### 4.3 Room（房间）

```go
type Room struct {
    ID      string              // 房间 ID
    Clients map[string]*Client  // 在线客户端，key = 用户 ID
    Lock    sync.RWMutex        // 房间级别读写锁
}
```

### 4.4 全局状态

```go
var (
    allActiveRooms      map[string]*Room    // 所有房间，key = 房间 ID
    allConnectedClients map[string]*Client  // 所有客户端，key = 用户 ID
    roomLock   sync.RWMutex
    clientLock sync.RWMutex
    Upgrader   websocket.Upgrader  // 全局 WebSocket 升级器
)
```

---

## 5. 消息协议

### 5.1 客户端 → 服务端

| type     | payload                       | 说明                            |
| -------- | ----------------------------- | ------------------------------- |
| `join`   | `"我的昵称"` 或 `{"username":"xxx"}` | 加入指定房间（room_id 必传）     |
| `offer`  | WebRTC SessionDescription     | 发起方 Offer，后端透传给同伴     |
| `answer` | WebRTC SessionDescription     | 应答方 Answer，后端透传给同伴    |
| `ice`    | WebRTC ICE Candidate          | ICE 候选地址，后端透传给同伴     |
| `leave`  | 无                            | 主动离开房间                     |
| `ping`   | 无                            | 心跳检测                         |

### 5.2 服务端 → 客户端

| type          | payload                                        | 触发时机                          |
| ------------- | ---------------------------------------------- | --------------------------------- |
| `waiting`     | `null`                                         | 第一个用户加入房间，等待同伴       |
| `room_ready`  | `{"users": [...], "can_start": true}`          | 第 N 个用户加入，通知新用户房间就绪 |
| `user_joined` | `{"user_id": "xxx", "username": "xxx"}`        | 新用户加入，广播给房间现有成员     |
| `user_left`   | `{"user_id": "xxx"}`                           | 用户离开，广播给房间剩余成员       |
| `offer`       | WebRTC SessionDescription                      | 转发来自同伴的 Offer              |
| `answer`      | WebRTC SessionDescription                      | 转发来自同伴的 Answer             |
| `ice`         | WebRTC ICE Candidate                           | 转发来自同伴的 ICE Candidate      |
| `pong`        | `null`                                         | 回复心跳                          |
| `error`       | `{"message": "错误描述"}`                       | 操作出错时                        |

---

## 6. 核心流程

### 6.1 连接建立

```
浏览器 → GET /ws
  → handlers.WebSocketHandler
    → room.Upgrader.Upgrade(w, r)    升级为 WebSocket
    → room.HandleConnection(conn)
      → Client{ID: UUID}. 注册到 clients map
      → go writePump(client)         启动写协程
      → readPump(client)             阻塞在消息读取循环
```

### 6.2 房间加入 — 第 1 人

```
客户端 → { type: "join", room_id: "room1", payload: "Alice" }
  → 创建房间 room1
  → Client 绑定 roomID + Username
  → 人数 = 1 → 回复 { type: "waiting" }
  → 等待同伴加入...
```

### 6.3 房间加入 — 第 2 人

```
客户端 → { type: "join", room_id: "room1", payload: "Bob" }
  → 加入已有房间 room1
  → 人数 = 2
  → 广播 { type: "user_joined", user_id: "bob-uuid", username: "Bob" } 给 Alice
  → 回复 Bob { type: "room_ready", users: [{Alice信息}, {Bob信息}] }
  → 两人开始交换 offer/answer/ice 建立 P2P 连接
```

### 6.4 信令转发

```
发送方 → { type: "offer", payload: RTCSessionDescription }
  → handleRelay()
    → 查找发送者所在房间
    → 将消息原样写入房间内其他所有人的 Send 通道
  → 接收方 writePump 把消息写出 WebSocket
```

### 6.5 断连清理

```
WebSocket 连接断开 / 主动调用 leave
  → disconnect(client)
    → sync.Once 保证只执行一次
    → 从房间移除该客户端
    → 若房间还有人 → 广播 user_left
    → 若房间已空 → 删除房间
    → 从全局 allConnectedClients 移除
    → close(client.Send)  → 终止 writePump 协程
    → client.Conn.Close()
```

---

## 7. 并发安全设计

| 场景               | 策略                                              |
| ------------------ | ------------------------------------------------- |
| 房间读写            | `Room.Lock` (sync.RWMutex)                        |
| 全局 allActiveRooms      | `roomLock` (sync.RWMutex)，读写分离                |
| 全局 allConnectedClients | `clientLock` (sync.RWMutex)                       |
| 创建房间            | 双重检查锁：先读锁查 → 不存在则写锁创建               |
| 广播消息            | 先复制接收者列表（持读锁），释放锁后再逐个发送         |
| 断连清理            | `sync.Once` 防止重复清理                            |
| WebSocket 并发写    | 统一走 `writePump` 协程，单 goroutine 写           |
| 慢客户端            | `select + default` 非阻塞写入，慢客户端直接丢弃消息   |

---

## 8. 配置项

通过环境变量配置，均在 `main.go` 中读取：

| 变量              | 默认值  | 说明                           |
| ----------------- | ------- | ------------------------------ |
| `SERVER_ADDR`     | `:8080` | 服务监听地址                    |
| `HTTPS_ENABLED`   | `false` | 是否启用 HTTPS (设 `true` 开启) |
| `TLS_CERT_FILE`   | (无)    | HTTPS 证书文件路径              |
| `TLS_KEY_FILE`    | (无)    | HTTPS 私钥文件路径              |

---

## 9. 本地开发

### 启动

```bash
cd backend
go run .
```

### 测试 WebSocket

用 `wscat` 或浏览器控制台：

```javascript
const ws = new WebSocket("ws://localhost:8080/ws");
ws.onmessage = (e) => console.log(JSON.parse(e.data));

// 加入房间
ws.send(JSON.stringify({ type: "join", room_id: "room1", payload: "Alice" }));

// 测试心跳
ws.send(JSON.stringify({ type: "ping" }));
```

---

## 10. Docker 部署

### 构建镜像

```bash
docker build -t voice-room-backend .
```

### 运行容器

```bash
docker run -p 8080:8080 voice-room-backend
```

### Dockerfile 结构

- **构建阶段** (`golang:1.21-alpine`)：编译源码生成二进制文件
- **运行阶段** (`alpine:latest`)：仅包含二进制 + ca-certificates，镜像体积最小
- `CGO_ENABLED=0` 静态编译，不依赖系统 libc

---

## 11. 后台任务

| 任务                   | 运行方式     | 间隔      | 说明                       |
| ---------------------- | ------------ | --------- | -------------------------- |
| `cleanupIdleRooms()`   | goroutine   | 5 分钟    | 扫描并删除空房间，兜底清理 |

---

## 12. 依赖清单

| 模块                         | 版本    | 用途               |
| ---------------------------- | ------- | ------------------ |
| `github.com/gorilla/websocket` | v1.5.1 | WebSocket 协议实现  |
| `github.com/google/uuid`      | v1.5.0 | UUID 生成用户 ID    |
| `golang.org/x/net`            | v0.17.0 | gorilla/websocket 间接依赖 |
