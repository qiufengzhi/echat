# eChat 用户系统完整规格（User System Specification）

> **分层位置**：本文档是 **HOW 层（用户系统怎么实现）**；「要实现什么 + 阶段计划」见 [docs/roadmap.md](../docs/roadmap.md)
> **日期**：2026-08-26
> **状态**：规格设计，是用户系统的权威落地依据
> **范围**：Go 后端为主；涉及与前端协作的协议（API / WebSocket 鉴权 / Token 载体）
> **与既有文档关系**：本规范是**实施级规格**，覆盖注册/登录/会话/找回密码/授权/安全等全套；`designs/user-system-design.md` 保留为**概念讲解**（认证≠授权、users/identities 解耦、双 token 原理等思想），两者以本规范为落地准绳，矛盾处以本规范为准
> **与前沿蓝图关系**：完全对齐 `docs/frontier-architecture-blueprint.md` —— 持久层 PostgreSQL + Ent + Atlas，一致性与事件走 Outbox + NATS JetStream，**授权用 SpiceDB/Zanzibar（取代原本手写 mini-RBAC 的决策）**

---

## 一、设计原则（先立规矩）

1. **认证 ≠ 授权**：认证层管"你是谁、怎么登录"，授权层管"你能对什么做什么"，两层彻底分开
2. **服务端权威**：所有身份与权限以服务端判定为准，客户端上报的 user id / 角色一律不信
3. **不信任客户端输入**：字段在服务端重新校验（格式、长度、枚举、归属）
4. **防枚举与防爆破**：登录/注册/重置统一走限流 + 泛化错误文案，不在响应里泄露"这个邮箱到底存不存在"
5. **凭证可吊销、可轮换**：access 短命、refresh 可吊销，封禁/改密即时生效
6. **最小数据采集**：只存业务必需的字段；敏感凭证只存不可逆哈希
7. **可审计**：关键动作（注册、登录成功/失败、改密、封禁）作为领域事件落 outbox，天然形成审计轨迹

---

## 二、账户模型（数据层）

### 2.1 核心概念回顾（承接 user-system-design.md）

- 「你是谁」（users，身份本体）≠「你怎么证明」(identities，登录凭据)
- 未来接 GitHub/手机号 = identities 表加**一条记录**，users 表不动
- 本规范从第一版就按解耦建表

### 2.2 账户状态机（users.status）

| 状态 | 含义 | 允许登录 | 允许刷新 | 说明 |
|------|------|:---:|:---:|------|
| `pending` | 已注册未邮箱验证 | ❌ | ❌ | 仅邮箱注册路径的默认态（本地账号不经过） |
| `active` | 正常可用 | ✅ | ✅ | 邮箱验证通过后 / 本地账号注册即 active |
| `suspended` | 被管理员封禁 | ❌ | ✅（仅用于提示）| 封禁后 access 立即失效，refresh 只能换来"封禁提示" |
| `deleted` | 软删除（注销） | ❌ | ❌ | 查档保留，对外不可见 |

### 2.3 数据模型总览（Ent Schema 草案）

> 字段标注规则：所有字段都有「含义」注释；枚举逐一列出；约束与索引显式声明。软删用 `deleted_at`。

```
users
  id            uuid          -- 主键，全局稳定身份
  email         string          -- 联系邮箱（可空，未绑定时 NULL；仅展示/找回用，登录凭据在 identities）
  username      string unique -- 用户名句柄（唯一索引，3-20 位字母数字 _ -；本地账号的登录标识）
  display_name  string        -- 展示昵称（可随时修改，允许重名）
  avatar_url    string        -- 头像地址（可为空）
  status        enum          -- 账户状态机：pending / active / suspended / deleted（见 2.2）
  token_version int           -- 令牌版本：封禁/改密/重置时 +1，使旧 access 失效的关键
  password_hash string        -- 账户主凭据的 argon2id 哈希（本地与邮箱登录共用同一份密码，禁止明文）
  created_at    timestamp     -- 创建时间
  updated_at    timestamp     -- 最近更新时间
  deleted_at    timestamp     -- 软删除时间（可空，唯一索引与软删配合注意唯一性约束做法）

identities
  id            uuid          -- 主键
  user_id       uuid FK       -- 归属用户（外键 users.id）NOT NULL
  provider      string        -- 登录来源枚举：local / email / phone / github（local=用户名+密码）
  provider_uid  string        -- 该来源下的唯一标识（邮箱本体 / 第三方 openid）
  metadata      jsonb         -- 来源附加信息（如第三方昵称、头像，可为空）
  created_at    timestamp     -- 创建时间
  唯一约束       (provider, provider_uid)      -- 一个来源标识只对应一个用户
  唯一约束       (user_id, provider)           -- 一个用户在同一来源只绑一条凭据

sessions
  id                  uuid          -- 主键
  user_id             uuid FK       -- 归属用户 NOT NULL
  refresh_token_hash  string        -- refresh_token 的 SHA-256 哈希（仅存哈希，不存原文）
  token_version       int           -- 签发时的用户 token_version（校验时比对）
  expires_at          timestamp     -- refresh 过期时间
  revoked_at          timestamp     -- 吊销时间（可空）
  ip                  inet          -- 登录时 IP（审计用）
  user_agent          string        -- 登录设备 UA（审计用）
  device_fingerprint  string        -- 设备指纹（可空，用于多设备管理/重放检测）
  created_at          timestamp     -- 创建时间
  索引                (user_id, revoked_at)

auth_tokens                          -- 一次性短命令牌（邮箱验证 / 密码重置 共用一张表）
  id            uuid          -- 主键
  user_id       uuid FK       -- 归属用户 NOT NULL
  purpose       enum          -- 用途：verify_email / reset_password
  token_hash    string        -- 一次性明文 token 的 SHA-256 哈希
  expires_at    timestamp     -- 失效时间（默认 30 分钟）
  consumed_at   timestamp     -- 消费时间（可空；已消费即失效）
  created_at    timestamp     -- 创建时间
  索引          (user_id, purpose, consumed_at)
```

### 2.4 需要落库的用户领域事件（并入蓝图 outbox 事件目录）

| 事件 | subject | 触发 | 说明 |
|------|---------|------|------|
| `user.registered` | `user.{id}.registered` | 注册成功 | 审计 |
| `user.verified` | `user.{id}.verified` | 邮箱验证通过 | 审计 |
| `user.email.bound` | `user.{id}.email.bound` | 本地账号成功绑定邮箱 | 审计（新增 provider=email 凭据） |
| `user.login.succeeded` | `user.{id}.login.succeeded` | 登录成功 | 审计/风控消费 |
| `user.login.failed` | `user.{id}.login.failed` | 登录失败 | 审计/风控消费（含 IP） |
| `user.password.changed` | `user.{id}.password.changed` | 改密/重置 | 触发"吊销他端会话"策略 |
| `user.suspended` | `user.{id}.suspended` | 管理员封禁 | 触发连接主动踢出 |

> 这些事件进 NATS JetStream 后，下游消费者模型见蓝图 3.3.1；`user.suspended` 取消会话/踢连接是个典型"跨读模型动作"，也是未来多实例时广播扇出的场景。

---

## 三、注册流程

### 3.1 主链路（邮箱 + 密码）

```
POST /api/v1/auth/register { email, username, password }
  1. 校验格式（邮箱 RFC 近似校验；username 3-20；password 见 3.3 强度）
  2. 查重 email / username —— 重复返回泛化 409，不暴露哪个字段撞了
  3. 密码 argon2id 哈希（见 3.2）
  4. 事务写入：
     users.status=pending + password_hash + identities(provider=email) + auth_tokens(purpose=verify_email)
     + outbox: user.registered
  5. 返回 201 + 提示"请查收验证邮件"
  邮件链接 → POST /api/v1/auth/verify { token }:
  6. 校验 token_hash / 未过期 / 未消费 → users.status=active
     + outbox: user.verified
```

### 3.2 密码哈希（OWASP 推荐）

- **算法：argon2id**（memory-hard KDF，抗 GPU 并行破解），参数：`m=64MiB, t=3, p=4`（可配置）
- 每条密码存 `$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>` 自描述串，未来升级参数可平滑迁移（重哈希）
- **禁用**：明文 / MD5 / SHA / 弱 bcrypt（成本过低）

### 3.3 密码强度策略

- 最低 8 位；不强制组合字符（OWASP 反对一堆强制规则又被用户绕回的闹剧）
- 集成 **zxcvbn**（熵估算库，Go 有移植）——比"必须大小写数字符号"更科学，是简历可讲的细节

### 3.4 防枚举与幂等

- 邮箱已注册 / 用户名已占用 → 统一返回 `注册信息无法完成`（泛化），避免注册接口成为邮箱嗅探器
- 注册请求整体幂等：同邮箱重复提交不产生第二用户（唯一索引兜底）
- 限流：`register` 按 IP（见第十二章）

### 3.5 本地账号注册（用户名+密码，邮箱可选；默认方式）

```
POST /api/v1/auth/register { username, password, email? }
  带 email → 走 3.1 邮箱路径（pending → 验证 → active）
  否则（默认）→ 本地账号路径：
    1. 校验 username 3-20（字母数字 _ -）+ password 强度（3.3）
    2. 查重 username（唯一索引兜底，重复返回泛化 409）
    3. 事务写入：
       users.status=active + password_hash
       + identities(provider=local, provider_uid=username)
       + outbox: user.registered
    4. 返回 201，可直接登录（无验证环节）
```

**绑定邮箱（随时补；密码是账户级 credentials，与本地登录互不冲突）**：
```
POST /api/v1/auth/email/verify-request { email }   → 生成 verify_email 令牌（auth_tokens）
POST /api/v1/auth/verify { token }                 → 校验后：
     identities 写入 (provider=email, provider_uid=email)
     users.email 填值（pending 账户顺带激活；active 保持，仅确认邮箱归属）
     outbox: user.email.bound
```
- 换绑：verify-request 传新邮箱 → 覆盖 identities 的 email 行 + users.email（旧邮箱作废）
- 唯一索引：`users.email` 可为 NULL 时唯一索引用部分索引（`WHERE email IS NOT NULL`），保证多个无邮箱本地账号共存

---

## 四、登录与会话

### 4.1 为什么是"双 Token"（不是只会 JWT）

| | 纯 JWT | 双 Token（本方案） |
|---|---|------|
| 可吊销 | ❌ 需等过期 | ✅ refresh 可吊销，封禁/改密即时生效 |
| 跨服务校验 | ✅ 无状态 | ✅ access 仍无状态，短期 |
| 安全性 | token 被偷走直到过期都有效 | refresh 轮换 + 重放检测，被偷可发现可作废 |

### 4.2 Token 方案

- **access_token**：JWT（HS256），声明 `sub=user_id, sid=session_id, ver=token_version, exp=15min`
  - 无状态、跨服务可验；`ver` 对齐 users.token_version —— 封禁/改密/重置时服务端 +1，旧 access **立即失效**（中间件查一次缓存/DB 比对 ver）
- **refresh_token**：随机 256 bit 不透明串，**只存 SHA-256 哈希**在 sessions 表
  - 有效期 30 天（可配置）；**轮换制**：每次刷新换新 refresh、作废旧 refresh
  - **重放检测**：若收到一个已作废的 refresh → 判定令牌被偷 → **吊销同一用户全部会话**

### 4.3 登录链路

```
POST /api/v1/auth/login { identifier, password }
  1. 解析 identifier → 命中的登录标识查用户：
     username 形态 → users.username 直接查
     含 @ → identities(provider=email, provider_uid=email) 反查 user
     → 校验 users.password_hash（argon2id；本地与邮箱登录共用账户级同一份密码）
  2. 检查 status：active 才放行（suspended 返回"账户异常"并提示客服）
  3. 失败计数：该账户 / 该 IP 连续失败触发锁定（见 4.5）
  4. 生成 access(JWT) + refresh(随机) → sessions 插入哈希记录
  5. 事件：user.login.succeeded（含 ip / ua / device_fingerprint）
  6. 返回：
     { access_token, token_type:"Bearer", expires_in:900,
       refresh_token,            // 或走 httpOnly cookie（见 4.4 传输）
       user: { id, username, display_name, avatar_url } }
```

### 4.4 Token 传输（浏览器 SPA 权衡）

| 载体 | 适用 | 风险与对策 |
|------|------|-----------|
| access_token 放 JS 内存 + `Authorization: Bearer` | 通用 | XSS 可读 → 靠 CSP 缓解；刷新靠 refresh 换新 |
| refresh_token 放 **httpOnly + Secure + SameSite=Strict** Cookie（Path 限定 `/api/v1/auth`） | 防 XSS 窃取 | 依赖 SameSite 防 CSRF；refresh 不直接暴露给 JS |
| WebSocket 握手 | 见第九章 | 浏览器 WS 不能设自定义 Header |

**结论**：access 走 Authorization 头；refresh 走 httpOnly cookie（`SameSite=Strict`）；未来若加纯 API 客户端再评估 refresh 明文返回通道。

### 4.5 登录保护

- **失败计数**：账户维度连续失败（默认 5 次/15 分钟）锁定 15 分钟；IP 维度登录频率限流
- **锁定用 Redis**（蓝图热状态层）：`rl:login:{userID}` 与 `rl:login:{ip}` 自增 + TTL
- 锁定期满自然恢复，不主动泄露"还剩几次"细节
- 事件 `user.login.failed` 供风控与告警消费

---

## 五、刷新、退出、吊销

### 5.1 刷新

```
POST /api/v1/auth/refresh    （Cookie 里的 refresh）
  1. 哈希比对 sessions —— 命中：
     a) 有效：轮换 → 生成新 refresh，旧记录 revoked_at 置位（保留哈希便于重放检测）
     b) 已 revoke：重放 → 吊销该用户全部会话（疑盗）
  2. 校验 user.status / token_version 一致
  3. 返回新 access + 新 refresh（继续 httpOnly cookie）
```

### 5.2 退出

| 场景 | 接口 | 效果 |
|------|------|------|
| 单设备退出 | `POST /api/v1/auth/logout` | 吊销当前 session |
| 全部设备退出 | `POST /api/v1/auth/logout-all` | 吊销该用户全部 session + user.token_version+1（旧 access 全废） |
| 改密/重置后 | 内置执行 | 除当前设备外全部吊销 + version+1 |

### 5.3 封禁（suspended）时的即时失效

1. 管理员操作 → `user.suspended` 事件
2. 服务端 tasks：`users.token_version += 1`（旧 access 立即失效）+ 吊销全部 sessions（refresh 立即失效）
3. **联动实时连接**：若该用户正挂在 WebSocket 房间，推送 `kicked` 并断开（消费 `user.suspended` 的下游，见蓝图 3.3.1 ③ 风扇模型）

---

## 六、找回密码 / 修改密码

### 6.1 找回密码

```
POST /api/v1/auth/password/reset-request { email }
  → 生成 reset_token（与 verify 同表 purpose=reset_password），邮件发送一次性链接
  → 无论邮箱存不存在都返回"若邮箱存在将收到重置邮件"（防枚举）

POST /api/v1/auth/password/reset { token, new_password }
  → 校验 token 未过期未消费 → 重置密码哈希 → version+1 → 吊销全会话
  → outbox: user.password.changed
```

> 找回依赖已绑定邮箱：未绑定邮箱的本地账号走不了邮箱找回 → 产品上引导先绑定（见 3.5）；这是「邮箱可选」接受的取舍，待手机号凭据支持后可再扩展

### 6.2 修改密码（已登录）

```
POST /api/v1/auth/password/change { current_password, new_password }
  → 校验当前密码 → 设置新哈希 → version+1 → 吊销除当前设备外会话
  → outbox: user.password.changed
```

### 6.3 一次性令牌安全约定

- 密文：随机 256 bit，存 SHA-256 哈希；30 分钟过期；用了即消费（`consumed_at` 置位），不可复用
- 重放：已消费 token 再次提交 → 拒绝（配表 `auth_tokens` 唯一 + 状态检查）

---

## 七、多因素认证 MFA（🔮 未来/探索位）

- **TOTP**（RFC 6238）：标准的一次性密码，`authenticator_app` 二维码绑定 + 挑战码校验 —— 简历可讲"时间步进 + 防重放窗口"
- **WebAuthn / Passkey**（🔮 前沿）：平台级生物识别/Passkey，无密码时代。定为"为探索引入"，面试讲 WebAuthn 挑战应答、防钓鱼属性
- MFA 只影响"敏感操作"（改密、登录高危设备、大额动作），不默认全开

---

## 八、第三方登录 / 多凭据（🔮 未来，结构已备好）

- 流程：`GET /api/v1/auth/oauth/{provider}` 拉起授权 → 回调拿 provider_uid → identities 查（provider, provider_uid）
  - 命中 → 登录（若该用户原本只有 pending 邮箱，可顺带完成验证）
  - 未命中 → 绑定：已登录用户 = link；未登录 = 建新用户或走"账号合并"
- **账户 link / unlink**：`POST /me/identities/link`、`/me/identities/unlink`（需当前密码或 MFA）
- **不需要改 users 表**——这就是 2.1 解耦的价值；接第三方 = identities 加记录，**绑定邮箱（3.5）同样是加一条 provider=email 记录**，同一机制

---

## 九、WebSocket 连接鉴权（本项目特有难点）

### 9.1 握手携带 Token（浏览器限制：WS 不能设自定义 Header）

| 方式 | 优点 | 缺点 |
|------|------|------|
| `?token=` query | 实现简单 | token 进访问日志（泄露）；换 token 要重建连接 |
| `Sec-WebSocket-Protocol: tma, {access_token}` | 不落日志、符合规范 | 子协议需服务端剥前缀；依赖 access 短命 |
| 首条信令鉴权 | token 不进 WebSocket 协议层 | 首条消息前连接未授权，需超时释放 |

**本方案组合**：握手带 `?token=` 的 access（短命 15 min 风险可控，访问日志脱敏处理）+ 约定异常时前端重新登录重连。升级成功后服务端立即解析并**绑定 user_id 到连接**。

### 9.2 从"每连接随机 UUID"到"权威用户身份"

- 现状：`room.go` 每连接 `uuid.NewString()` 当身份 → 改造为 **token 里的 user_id 即为连接身份**
- 连接上挂：`{ userID, sessionID, tokenVersion }`；**所有上行消息的 userID 以连接绑定值为准**（服务端权威，见原则 2）
- 房间成员 = 用户身份入座（成员去重、房主角色入库 room_members / SpiceDB）

### 9.3 实时吊销联动

维护 `sessionID → 活跃连接` 映射（内存/Redis）；`user.suspended` 或 `logout-all` 时，按 user_id 找到连接推送 `kicked` 强制断开——实现"封禁即下线"。

### 9.4 心跳与超时

沿用现有 `ping/pong`；增加**鉴权超时**：握手成功 5 秒内未完成身份绑定则断开（防占连接不鉴权）。

---

## 十、授权模型（对齐蓝图，SpiceDB 授权）

### 10.1 授权 vs 认证的边界（又一遍强调）

- **认证**（第九章）回答：这个连接是哪个 user
- **授权**回答：这个 user 能否对房间/资源做动作 —— 全部收敛到 `authz` 服务，业务代码不出现 `if role == ...`

### 10.2 授权对象清单（现状 + 未来）

| 资源 | 维度 | 说明 |
|------|------|------|
| `user:{id}` | 本人 | 看自己资料、改自己资料 |
| `room:{id}` | 房间域 | host/cohost/speaker/listener + muted 覆盖（SpiceDB schema 见蓝图 3.5.2）|
| `recording:{id}` | 对象域（🔮）| view = owner + participant + mentioned |

### 10.3 角色体系（与蓝图 3.5 完全一致）

原子权限 → 派生权限表达式（在 SpiceDB schema 里），业务侧不再存"角色名"判断逻辑：

| 权限 | 表达式（schema 内） |
|------|---------------------|
| `edit_room` / `transfer_host` | `host` |
| `manage_ai` / `kick` / `mod_mic` | `host + cohost` |
| `speak` | `(host + cohost + speaker) - muted` |
| `listen` / `hand_raise` / `invite` | `member` |

- **muted（静音/禁言）** 是叠加状态关系，用 SpiceDB 排除运算符 `-` 表达"负向覆盖"，不必发明新角色
- 房间角色变更 = 关系元组写（`TouchRelationships` 幂等），走 outbox `room.*` 事件 + SpiceDB 授权投影（蓝图 3.3.1 ①）

### 10.4 授权链路（Go）

```
控制面动作（kick/manage_ai/mod_mic/speak 校验/交接）
   → authz.Can(ctx, user, "kick", room)      // spicedb-go CheckPermission + LRU 缓存
媒体路径（发音频、SFU 转发）永不鉴权          // 性能红线
```

详细：见蓝图 3.5.3 ~ 3.5.7（client 封装、ZedToken 一致性、热路径/持久真相取舍、面试追问预演）。

---

## 十一、API 设计（REST，前缀 /api/v1）

### 11.1 端点一览

| 方法 | 路径 | 鉴权 | 说明 |
|------|------|:---:|------|
| POST | `/api/v1/auth/register` | 否 | 注册（3.1 邮箱 / 3.5 本地） |
| POST | `/api/v1/auth/verify` | 否 | 邮箱验证 / 绑定邮箱回调（3.1 / 3.5） |
| POST | `/api/v1/auth/email/verify-request` | access | 绑定/换绑邮箱验证请求（3.5） |
| POST | `/api/v1/auth/login` | 否 | 登录（4.3） |
| POST | `/api/v1/auth/refresh` | refresh | 刷新令牌（5.1） |
| POST | `/api/v1/auth/logout` | access | 单设备退出（5.2） |
| POST | `/api/v1/auth/logout-all` | access | 全设备退出（5.2） |
| POST | `/api/v1/auth/password/reset-request` | 否 | 找回密码请求（6.1） |
| POST | `/api/v1/auth/password/reset` | 否 | 找回密码重置（6.1） |
| POST | `/api/v1/auth/password/change` | access | 修改密码（6.2） |
| GET  | `/api/v1/users/me` | access | 我的资料（11.3） |
| POST | `/api/v1/users/me/profile` | access | 更新个人资料 |
| GET  | `/api/v1/users/{id}` | access | 查看他人资料（只读公开字段） |

### 11.2 统一响应与错误约定

```
成功响应直接放资源 / 动作结果
错误响应统一信封：
  { "error": { "code": "EMAIL_TAKEN", "message": "注册信息无法完成" } }
```

- `message` 面向用户一律**泛化**（防枚举）；`code` 供前端分支处理
- 错误码分组：`AUTH_*` / `USER_*` / `RATE_LIMITED` / `VALIDATION_*`
- HTTP 状态：400 校验 / 401 未认证 / 403 未授权 / 404 资源不存在 / 409 冲突 / 429 限流 / 5xx 服务端

### 11.3 用户响应字段（每个字段注释）

```
GET /api/v1/users/me → {
  id           uuid      -- 用户 id，全局稳定身份
  username     string    -- 唯一句柄
  display_name string    -- 展示昵称
  avatar_url   string    -- 头像地址（可空）
  status       string    -- 账户状态：pending / active / suspended / deleted
  created_at   timestamp -- 账户创建时间
}
```

> 安全提示：响应绝不含密码哈希、token、sessions 任何信息；他人资料接口只返回公开字段（username / display_name / avatar_url）

### 11.4 幂等

- **注册/验证/找回**：自然幂等（唯一索引 + token 一次性）
- 需要时给写接口加 `Idempotency-Key` 头（POST /register 重试不产生副作用是典型场景）

---

## 十二、防滥用与安全清单

| 维度 | 措施 |
|------|------|
| 限流 | register / login / reset 三接口各配 Redis 令牌桶（IP + 账户双维度） |
| 密码 | argon2id（m=64MiB,t=3,p=4）、zxcvbn 强度评估 |
| 防枚举 | 注册/找回/登录错误文案泛化 |
| Token | access 短命 + ver 版本；refresh 哈希存储 + 轮换 + 重放检测 |
| 传输 | 全站 HTTPS；refresh 走 httpOnly+Secure+SameSite=Strict Cookie；CSP 收敛 JS 面 |
| WebSocket | 握手身份绑定、鉴权超时释放、封禁即踢 |
| 审计 | 关键事件全量 outbox 入 JetStream（登录/改密/封禁/注册） |
| 数据面 | 只存登录所需；敏感字段（IP/UA/指纹）设保留期，到点清理 |

---

## 十三、阶段对应关系（WHAT 层已独立成文）

分阶段的「要实现什么 / 验收 / 依赖」已移入 **[docs/roadmap.md](../docs/roadmap.md)**（WHAT 层），本表仅列本 HOW 文档各小节落在哪个阶段：

| 本文档小节 | 内容 | 阶段 |
|-----------|------|------|
| §2 / §3 / §4 / §5 / §6 | 账户模型、注册验证、登录会话、刷新退出、找回/修改密码 | P1 |
| §9 | WebSocket 握手鉴权 + userID 绑定 + 踢连接（标志能力） | P1 |
| §11 / §12 | REST API + 防滥用安全清单 | P1 |
| §10 | SpiceDB 授权模型（对接 blueprint §3.5） | P2 |
| §7 / §8 | MFA（TOTP→WebAuthn）、OAuth 第三方登录 | P4（种子） |

**实现优先级提示**：§9（WebSocket 鉴权）是简历杀手锏——「HTTP 取 token → 升级 WebSocket → 全程维护身份 + 封禁即踢」，P1 内优先保证完成度。

---

## 十四、面试追问预演

- **"为什么不用纯 JWT？"** → 纯 JWT 无法吊销，封禁/改密后旧 token 仍有效；双 token 让吊销即时、刷新可轮换、重放可检测
- **"access 无状态怎么做到封禁立即失效？"** → JWT 带 `ver`（token_version），封禁时服务端 +1，中间件每次校验做一次廉价比对
- **"refresh 为什么存哈希？"** → 泄露数据库也只是哈希；未加盐哈希配合随机 256 bit 高熵输入足够（免加盐），且支持重放检测
- **"WebSocket 鉴权和 HTTP 有什么本质不同？"** → WS 无法自定义 Header；升级是"建立连接"，授权状态要持续贯穿连接生命周期；吊销后需主动踢连接（HTTP 每次请求自证，WS 是长连接要反向推送）
- **"注册怎么防枚举和安全两难？"** → 全程泛化文案（注册删除已存在提示、找回密码统一提示），把"可用性"让给产品、把"安全默认值"留给系统，用唯一索引保证不越界
- **"为什么授权用 SpiceDB 不用手写 RBAC？"** → 房间角色有继承（co-host 派生）、负向覆盖（muted）、未来对象级推导（回放），关系制天然表达；且缓存/一致性/审计由系统承担，见蓝图 3.5.7

---

## 附录 A：一个典型完整用户旅程（串讲所有环节）

```
1. 注册：POST register → pending + 验证邮件
2. 验证：邮件链接 → verify → active
3. 登录：POST login → access(JWT, ver=v) + refresh(httpOnly cookie) + session
4. 进房：WS 握手带 access → userID 绑定 → 房间成员以用户身份入座 → host 写入 SpiceDB
5. 房间内授权：manage_ai / kick / speak 都过 authz.Can
6. 刷新：access 过期 → POST refresh 轮换；每 30 天确认一次登录
7. 找回：忘记密码 → reset-request + reset → version+1，全会话吊销，重新登录
8. 被封：管理员封禁 → user.suspended → ver+1 + 全 session 吊销 + 实时踢下线
```

## 附录 B：对齐清单（本规范 ↔ 蓝图 ↔ 旧文档）

| 主题 | 本规范 | 蓝图 | user-system-design.md |
|------|--------|------|----------------------|
| 存储/访问层 | PostgreSQL + Ent + Atlas | 3.1 一致 | 曾推 pgx+sqlc（已被蓝图取代）|
| 一致性/事件 | Outbox + JetStream + 用户域事件 | 3.2/3.3 | 未涉及 |
| 授权 | SpiceDB 关系元组 | 3.5 | 曾推手写 mini-RBAC（已被蓝图取代）|
| 令牌 | 双 token + ver 版本 | Phase 1 | 双 token 概念（一致，本规范补 ver）|
| 认证/授权分层 | ✅ 原则 1 | —— | 核心思想（保留）|