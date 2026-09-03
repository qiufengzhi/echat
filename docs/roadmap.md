# eChat 分阶段计划（WHAT 层）

> **分层位置**：本文档只回答「**要实现什么、按什么阶段、每阶段验收什么**」。具体「怎么实现」见 HOW 层文档，本页只给链接，不复述机制。
> **状态**：计划，随实现推进维护；每阶段完成即勾选。
> **配套**：实现细节 → [frontier-architecture-blueprint.md](frontier-architecture-blueprint.md)（架构）与 [user-system-spec.md](../designs/user-system-spec.md)（用户系统）。

---

## 阶段总览

| 阶段 | 主题 | 一句话目标 | 技术定位 |
|------|------|-----------|----------|
| P1 | 最佳实践地基 | 认证闭环 + 事件骨干 + 可观测，先立骨架 | 最佳实践 |
| P2 | 授权 + 热状态 | SpiceDB 角色体系 + Redis 在线状态 | 前沿进场 |
| P3 | 前沿展示 | WebTransport 信令 + TLA+ 验证 | 标志性 + 正确性 |
| P4 | 种子（可选） | Redpanda / ClickHouse / 分布式 SQL / MFA / OAuth | 探索种子 |

**依赖关系**：P1 是地基（含用户身份，后续所有模块的外键前提）；P2 依赖 P1 的认证身份与事件骨干；P3 与 P2 可并行；P4 沿用前序一切，随时可切人。

---

## Phase 1：最佳实践地基

> **状态**：完成（可观测 OTel 项由用户搁置，其余验收项通过）

### 要实现什么
1. **持久层接入**：PostgreSQL + Ent + Atlas 迁移，`store` 包
2. **一致性骨干**：事务性 Outbox + NATS JetStream，把房间 join/leave/切房主/消息/开关 AI 改成「当前态写库 + 事件发布」
3. **用户系统第一步**：argon2id 注册（本地用户名+密码 / 邮箱，邮箱可选后绑定）+ 双 token 会话 + Redis 登录限流 + 找回/修改密码
4. **WebSocket 握手鉴权** + userID 绑定 + 封禁即踢（本项目标志能力）
5. **用户域事件**（user.registered / login.failed / suspended 等）并入事件骨干
6. **可观测性**：OTel + Prometheus + Tempo/Grafana，产出语音链路延迟预算图

### 验收标准
- 完整旅程闭环：**注册 → 邮箱验证 → 登录 → 进房 → 授权动作 → 刷新 → 找回密码 → 封禁即下线**
- 事件骨干可运行、可回放：outbox 事件进入 JetStream，下游授权/热状态投影能消费
- 全链路 trace 图可见（Go → gRPC → Python agent 的延迟分解）
- 连接身份从「每连接随机 UUID」切换为「鉴权后的用户 UUID」

### 怎么实现（HOW 链接）
- 持久层：blueprint §3.1；用户模型：user-system-spec §2
- 事件骨干/Outbox：blueprint §3.2 / §3.3
- 注册与会话：user-system-spec §3 / §4 / §5
- 找回/修改密码：user-system-spec §6
- WebSocket 鉴权：user-system-spec §9
- 可观测：blueprint §3.6

---

## Phase 2：授权 + 热状态

> **状态**：完成，验收项全部通过

### 要实现什么
1. **SpiceDB / Zanzibar**：把「房主」升级为关系元组；新增 co-host / speaker / listener 角色体系 + muted 负向覆盖；权限判断收敛到 `authz` 服务
2. **Redis 热状态**：在线成员/心跳 TTL、登录限流归位；接口预留 pub/sub 切换点

### 验收标准
- 房间角色体系走授权表达式（schema 见 blueprint 3.5.2），业务代码不再有散落 `if role == ...`
- 房主交接 = DB 当前态 + outbox `host.transferred` → SpiceDB 授权投影，无「双重房主」（后续由 TLA+ 验证）
- 在线状态走 Redis ZSET + TTL，事实（成员/角色）与瞬态（在线）彻底分层（blueprint §3.0）

### 怎么实现（HOW 链接）
- 授权原理/Schema/迁移/面试：blueprint §3.5、user-system-spec §10
- Redis 热状态：blueprint §3.4

---

## Phase 3：前沿展示

> **状态**：进行中，WebTransport 双通道信令已上线（前后端 + 生产部署），TLA+ 形式化验证待做

### 要实现什么
1. **WebTransport / QUIC 信令**（带 WebSocket 降级兜底）
2. **TLA+ 形式化验证**：房主交接不变量（No double host）与 AI 状态机

### 验收标准
- 信令链路在 WebTransport 上可用；特性不支持时自动降级回 WebSocket，两条路径共用同一套处理
- TLA+ 报告记录对房主交接/状态机的建模与 TLC 穷举结果

### 怎么实现（HOW 链接）
- WebTransport：blueprint §4
- TLA+：blueprint §3.7

---

## Phase 4：种子（可选，各有独立故事）

| 项 | 说明 | HOW 链接 |
|----|------|----------|
| Redpanda | 自建高性能事件存储，替代/旁路 JetStream | blueprint §3.8 |
| ClickHouse | 从事件流投影「房间活跃/消息高峰」实时分析 | blueprint §3.8 |
| 分布式 SQL（TiDB/CRDB）| 多 region 语音房、水平扩展探索 | blueprint §3.8 |
| MFA | TOTP → WebAuthn/Passkey（前沿） | user-system-spec §7 |
| OAuth 第三方登录 | GitHub 等，identities 表已备 | user-system-spec §8 |

---

## 阶段推进约定

- **P1 不完成不进 P2 的正门**：P1 里面有绝大多数简历点且是地基；P2/P3 可与 P1 后半段交叉研究，但正式开工以 P1 验收为准
- **P4 随时可切**：每项独立，作为闲聊/探索保持项目活力
- 每阶段完成后更新本页勾选状态，并在对应 HOW 文档维护实现记录