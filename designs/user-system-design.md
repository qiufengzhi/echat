# eChat 用户系统设计

> **定位说明（2026-08-26 更新）**：本文件是**概念讲解**（认证≠授权、users/identities 解耦、双 token、RBAC/Zanzibar 演进思想）。**落地规格以 [user-system-spec.md](user-system-spec.md) 为准**——授权方案已由「手写 mini-RBAC」改为 **SpiceDB/Zanzibar**（见 [frontier-architecture-blueprint.md](../docs/frontier-architecture-blueprint.md) §3.5），其余概念（解耦、双 token、角色矩阵）仍成立。
> 本文档为 eChat 引入用户系统（认证 + 授权 + 账户建模）的设计蓝图。
> 每个技术点标注定位：✅ 现在落地 ／ 🔮 未来演进。

---

## 目录

1. [现状与目标](#一现状与目标)
2. [总体架构：身份 ≠ 权限](#二总体架构身份--权限)
3. [决策一：引入 PostgreSQL（✅ 现在）](#三决策一引入-postgresql)
4. [决策二：账户建模 users + identities（✅ 现在 + 🔮 未来）](#四决策二账户建模)
5. [决策三：认证与会话（✅ 现在）](#五决策三认证与会话)
6. [决策四：WebSocket 连接鉴权（✅ 现在，本项目差异点）](#六决策四websocket-连接鉴权)
7. [决策五：权限模型 RBAC（✅ 现在）](#七决策五权限模型-rbac)
8. [房间角色体系（RBAC 产品化落地）](#七之二房间角色体系rbac-的产品化落地)
9. [决策六：多租户（🔮 未来）](#八决策六多租户)
10. [决策七：对象级权限 Zanzibar / SpiceDB（🔮 未来）](#九决策七对象级权限-zanzibar--spicedb)
11. [主题总结表](#十主题总结表)
12. [落地分层（MVP → 演进）](#十一落地分层mvp--演进)

---

## 一、现状与目标

### 当前状态（关键约束）

- 纯 Go 后端，**无数据库、无任何持久化**，全部内存态。
- 身份是每连接随机生成：`room.go` 中 `userID := uuid.NewString()`。
- 用户名为进房时随手敲的字符串，无账户概念。
- 「房主」是房间内临时指定、离开时随机 `chooseNextHostID` 交接，无真正的权限模型。
- 走标准库 `net/http` + `http.HandleFunc`，WebSocket 在 `/ws`。
- README 路线图点明后续要做的：**用户认证、文字聊天、好友、录制回放**。

## 二、总体架构：身份 ≠ 权限

贯穿全文的核心原则（Keycloak、SpiceDB 等成熟系统的共同做法）：

> **认证 Authentication（你是谁）≠ 授权 Authorization（你能做什么）**

- **身份层**：管「用户是谁、怎么登录」（users / identities / 会话）。
- **权限层**：管「这个用户对某个资源能做什么」（RBAC / 未来的 Zanzibar）。

务必分两层建模，不要把权限字段耦合进用户表。否则多租户、细粒度授权时必然推倒重来。

---

## 三、决策一：引入 PostgreSQL

**定位：✅ 现在。** 没有持久化就无法谈用户系统，这是所有后续功能（聊天、好友、回放）的地基。

**访问层**：推荐 **pgx + sqlc**。
- sqlc 从 SQL 直接生成类型安全代码，契合本项目手写风格，且是当下类型安全趋势下受欢迎的简历点。

**落地**：`deploy/docker-compose.prod.yml` 增加 postgres 服务；`config.yaml` 增加 DB 连接段。

---

## 四、决策二：账户建模

**定位：表结构 ✅ 现在；多登录方式 🔮 未来。** 这是很多人没懂的点，展开讲。

> **（2026-08-31 更新）** 已落地「本地账号」：用户名+密码注册，邮箱可选、注册后再绑定——正是解耦模型的价值（登录标识在 identities，密码是账户级 credentials）。

### 核心概念：「你是谁」≠「你怎么证明你是你」

现在 echat 只有一种登录方式（邮箱+密码），所以「用户」和「登录凭据」看起来是一回事，一张表就能存。

但**未来接入 GitHub / 手机号等第三方登录后**，同一个人（同一个 echat 账号）会拥有**多种登录凭据**：
- 邮箱 + 密码 能登录
- GitHub 授权 能登录
- 手机号 + 验证码 能登录

这三者指向**同一个用户**，却是三种不同的「凭据」。

**如果当初把密码硬编码进 users 表**（只有 email + password 两列），接 GitHub 就得改表、加列，每接一个登录源加一列，越来越乱。

### 正确做法：users 与 identities 拆表

```
users       (id, username, display_name, avatar, status, metadata JSONB)
                 ↑ 只管「你是谁」这个身份本体

identities  (id, user_id, provider, provider_uid, ...)
                        ↑          ↑           ↑
                    属于哪个用户   来源       该来源下的唯一标识
                    (github/email/phone)
```

**「接 GitHub 登录」的本质**：`identities` 表新增**一条记录**——`{ user_id: 3, provider: 'github', provider_uid: 'octocat123' }`。**users 表一行都不用改。**

一个字概括：**账户系统解耦**（Keycloak / Auth0 都这么做）。现在不接 GitHub 也没关系，但表结构现在就按解耦设计，**未来接只是加数据，不是改表**——这正是「未来场景能讲得明白」的价值。

> 设计落点：现在**只建 users 表**，`identities` 预留为代码里的 `IdentityProvider` 接口 + 未来开表，不提前堆无用表。

### 密码存储

- 用 **bcrypt**（或 argon2），严禁明文 / MD5 / SHA。
- 邮箱唯一、用户名唯一，靠唯一索引约束。

---

## 五、决策三：认证与会话

**定位：✅ 现在。** 这是相比「只会贴 JWT」高出一档的硬功夫，也是真实工程需要。

### 为什么不只是 JWT

纯 JWT 无状态、无法吊销，**用户被封禁或改密码后，旧 token 依然有效直到过期**。对实时应用来说这是安全漏洞。

### 双 Token 会话模型（Ory / Keycloak 推荐）

```
login ──► 返回
   ├── access_token   (JWT，短时效，如 15 分钟，无状态)
   └── refresh_token  (随机不透明字符串，存表、哈希、可吊销、可轮换)
```

- **access_token**：短命，跨服务传递，无需查库。
- **refresh_token**：存 `sessions` 表哈希，**可吊销**（封禁/改密码即时生效）、**可轮换**（每次刷新换新，旧 token 作废）。
- 敏感操作（改密码、改邮箱）强制重新认证。

```
sessions(id, user_id, refresh_token_hash, expires_at, revoked_at, device, ip, user_agent)
```

---

## 六、决策四：WebSocket 连接鉴权

**定位：✅ 现在，本项目差异点（简历杀手锏）。**

普通 HTTP 的 JWT 中间件人人会写，但「**HTTP 拿到 token → 升级为 WebSocket → 全程维护身份**」是真实痛点，也是本架构的独特素材。

### 流程

```
1. POST /api/v1/auth/login ──► { access_token, refresh_token }
2. 前端 WebSocket 握手携带 access_token
   (浏览器 WS API 不能设自定义 Header，常用 ?token= 或 Sec-WebSocket-Protocol 子协议携带)
3. 服务端握手 / 首条 join 时校验 token，把 userID 绑定到连接
4. 后续所有消息的 userID 用服务端权威值
```

### 与现有代码的榫卯

`room/model.go` 的 `Message` 已有 `UserID` 字段，现在每连接随机生成。
改造方向：**把「连接随机 UUID」替换为「token 里的用户 UUID」**，其余信令逻辑基本不变。这正是「贴地落地」——不是另起炉灶，而是把现有结构的占位符替换成权威身份。

---

## 七、决策五：权限模型 RBAC

**定位：✅ 现在。** 本场景权限核心是「房间内授权」，是 RBAC 的经典应用。

### 需要授权的能力（现状）

- 谁能说话、谁是房主、谁能踢人、谁能开关 AI 助手。

### 手写 mini-RBAC，不引 Casbin 黑盒

自己实现更能展示设计能力，且无外部依赖。核心表（**角色挂命名空间 scope，为多租户预留**）：

```
roles(id, scope, name)                    -- scope: 现在为 room，未来可为 org
permissions(id, code, resource, action)   -- 如 speak / kick / manage_ai
role_assignments(user_id, role_id, scope_id)
```

权限粒度示例：`speak room:abc`、`kick room:abc`、`manage_ai room:abc`。

### 关键改造：把「房主」升级为授权实体

现状的 `chooseNextHostID` 随机选主是临时逻辑，改造后：
**「房主 = 拥有 room:admin 角色」**。房主交接 = 角色转移，踢人、禁言都基于权限判断。
这是对现有 ad-hoc 逻辑最「榫卯」的替换——**真正贴地的改进，而非另起炉灶**。

### 注册表式权限判断

权限判断收敛到一个授权服务（`Can(user, action, resource)` 接口），不在业务代码（`room.go`）里到处 `if user.role == ...`。

---

## 七之二、房间角色体系（RBAC 的产品化落地）

RBAC 不是抽象建模就完了，需要落到具体房间角色上。本项目的产品形态类似「语音厅」（Clubhouse / 全民K歌 / YY 语音房）——熟人 + 陌生人临时聚一起说话。

### 前端现状

前端已把角色建模为四类（`VoiceRoomPage.tsx`）：`'host' | 'member' | 'ai' | 'empty'`。但真实授权只有 **host / 非 host 两档**：
- 房主独享：`ai_toggle`（开关 AI）、`host_changed`（交接房主）
- 成员只剩：说话、静音、离开

**权限缺口已经存在**：房主连「静音捣乱者 / 踢人出房」这种最基本的管理能力都没有。角色体系要补的正是这块。

### 方法论：角色 = 原子权限的组合

不是「再加几个角色名」，而是**定义一组原子权限，每个角色 = 若干权限的组合**。新角色 = 现成权限重新排列，不改核心逻辑。直接对上 `roles` / `permissions` / `role_assignments`：

```
原子权限：
speak         能说话
listen        能收听
hand_raise    能举手请求上麦
mod_mic       能开关/静音别人的麦克风
kick          能把人踢出房间
manage_ai     能开关 AI 助手
invite        能邀请人进房
transfer_host 能交接/任命房主
edit_room     能改房间标题/封面/简介
```

### 原子权限 → 角色矩阵

| 权限 | host 房主 | co-host 副房主 | speaker 嘉宾 | listener 听众 |
|------|:---:|:---:|:---:|:---:|
| `speak` 说话 | ✅ | ✅ | ✅ | ❌（需上麦） |
| `listen` 收听 | ✅ | ✅ | ✅ | ✅ |
| `hand_raise` 举手 | ✅ | ✅ | ✅ | ✅ |
| `manage_ai` 开关AI | ✅ | ✅ | ❌ | ❌ |
| `mod_mic` 静音别人 | ✅ | ✅ | ❌ | ❌ |
| `kick` 踢人 | ✅ | ✅ | ❌ | ❌ |
| `invite` 邀请 | ✅ | ✅ | ✅ | ✅ |
| `transfer_host` 交接/任命 | ✅ | ❌ | ❌ | ❌ |

**Co-host = host 减去「夺权」权限**：能管人（静音/踢/开关 AI），但不能把房主交给自己或任命新房主。这演示了「用权限差 表达角色差别」。

### 角色详述

**房主 Host（保留，权限最全）**：全权限，含 `transfer_host`。现有 `chooseNextHostID` 随机选主逻辑应被「host 角色转移」取代。

**副房主 Co-host（新增）**：房主临时离开或房间大时的「副手」。能维持秩序，不能夺权。房主可同时任命多个 co-host。

**嘉宾 Speaker（新增，关键）**：语音厅常见「少数人讲、多数人听」（分享 / 表演 / K 歌）。现在的模型人人平等都能说话，这正是产品缺口——补上 Speaker 就能做分享会 / 点歌台玩法。

**听众 Listener（新增，与 Speaker 互斥）**：默认状态 = 能听不能说。想说话的举手，房主「上麦」才变 Speaker。

**静音/禁言 Mut/*（状态叠加，非独立角色）**：`muted_by_mod` 是叠加在角色上的持续性状态，由房主 `mod_mic` 打上/解除。**这演示了「角色 × 持续性状态」与「一次性动作权限」分开建模**——简历可讲。

### 配套机制：角色要能「流动」

- **举手 Hand-raise**：Listener 点举手 → 房主看到排队 → 点「上麦」变 Speaker。语音厅灵魂互动。
- **指定上 / 下麦**：房主直接点某成员上麦 / 下麦（对比目前的绝对平等，这就是产品差异）。
- **房主分权**：房主可把自己降级为 Co-host，把 Host 让渡（现有 `next_host_id` 已支持交接，只是缺「同时任命多个 co-host」）。

### 落地

技术上 SFU 已能控制每个 peer 的音轨，**静音 / 下麦不涉及新基础设施，只缺权限逻辑**——纯贴地。

- **✅ 现在做**：Co-host / Speaker / Listener + `mod_mic` / `kick` + 举手上麦。
- **🔮 未来做**：全局角色（跨房间封禁账号、全局禁言）、租户级角色——等好友 / 多租户再上，`roles.scope` 已预留。

---

## 八、决策六：多租户

**定位：🔮 未来。** 现在 App 没有「组织/公司」概念，硬做组织 UI 是纯堆复杂度；但**数据模型天生按可扩展设计**。

### 多租户到底是什么

**一句话**：一套系统给多个互相独立的组织一起用，各组织数据和权限彼此隔离，但共用同一套代码和数据库。

类比：
- **单租户** = 每家各盖一栋房，隔离彻底但浪费、难维护。
- **多租户** = 一栋楼分租，每家租一层，用钥匙开自己的门，看不到别的家；楼体（代码）是共用的。

把 echat 多租户化，就是假设未来它变成一个给多家公司用的产品：A 公司员工只能进 A 公司的房间，看不到 B 公司；A、B 数据彻底隔离，但共享同一套运行中的系统。

### 技术上怎么实现隔离

核心是一行 `org_id` / 命名空间标识，贯穿三处：

1. **数据**：房间、成员关系、聊天记录全部打「属于组织 X」的标签。
2. **查询**：强制 `WHERE org_id = 当前用户所属组织`。
3. **授权**：一个用户在各组织的角色独立（在 A 是管理员、在 B 是普通成员）。

### 精髓与风险

难点不是「建组织表」，而是**所有权限判断、所有数据查询都要带 org 维度**。漏一处就跨组织越权（A 公司看到 B 公司数据）——这是多租户系统最容易出安全事故的地方，也是资深后端面试高频考点。

### 本项目做法

现在只做「**预留**」：`roles.scope`、`role_assignments.scope_id` 字段天生支持 org。未来加组织时**不用推倒重来**，简历可讲「模型是租户感知的」。不提前做组织 UI 和 org 查询过滤，避免无用的复杂度。

---

## 九、决策七：对象级权限 Zanzibar / SpiceDB

**定位：🔮 未来。** 现在不引入（纯堆复杂度），但要能讲清楚它解决什么问题。

### Zanzibar 是什么

Google 提出的权限模型：把权限拆成无数条极小的**关系**（谁 对 什么 有什么关系），通过**图遍历**回答「某人对某物有没有某权限」。

```
用户A 是 文件X 的 查看者
文件X 属于 文件夹F
文件夹F 的 查看者 = 群组G
群组G 包含 用户B
```
问「B 能看 X 吗」→ 沿关系走：B ∈ G → G 能看 F → F 包含 X → 所以 B 能看 X。（继承、传导）

### 和 RBAC 的本质区别

- **RBAC（角色制）**：权限取决于「你是房主 / 成员」这种**固定角色**。适合 "who you are determines what you can do"。
- **Zanzibar（关系制）**：权限取决于「对象与对象之间的**具体关系**」。适合 "the relationship between objects determines what you can do"。

### 为什么「好友」「录制回放」会用上

**好友例子**：
- 要问的是「A 能不能看 B 的主页/在线状态」。
- 这不是「B 是房主所以能看」，而是「**A 和 B 是朋友关系**」。
- RBAC 表达「朋友」要建一个叫 friend 的别扭角色；Zanzibar 一句关系 `user:A friend user:B` 就解决——**关系的本质就应该是关系，不是角色**。

**录制回放例子**：
- 录了一段房间语音，要决定「谁能回放这条录音」。
- 一次录音有多个权限来源：**所有人**（房主）、**当时在场的成员**、**房间管理员**、**被单独@的人**。
- RBAC：要挨个给人分配「可回放」角色，还要处理房主离职、成员变动，很痛苦。
- Zanzibar：权限就是一组关系——「录音 R 的所有者是 A」「录音 R 的参与者是当时全体成员」「录音 R 可回放给群组 G」，查询自动推导。

### 一句话

> 现在（语音房、房主、成员）RBAC 够用；未来（好友、按条录音授权、群聊可见范围）会大量出现对象与对象的关系型权限，那时 RBAC 撑不住，Zanzibar/SpiceDB 才是答案。现在不引入，但能讲清楚它解决什么。

---

## 十、主题总结表

| 层 | 技术 | 现在 / 未来 | 简历一句话价值 |
|----|------|:---:|------|
| 存储 | PostgreSQL + sqlc/pgx | ✅ 现在 | 行业标准 + 类型安全访问层 |
| 账户 | users + identities 解耦 | ✅ 表结构 / 🔮 多登录 | 身份本体与登录凭据解耦 |
| 会话 | Access+Refresh 双 token、可吊销 | ✅ 现在 | 比「会写 JWT」高一档 |
| WebSocket 鉴权 | 握手绑定身份 | ✅ 现在 | **本架构独特难点，能讲透** |
| 权限 | 手写 mini-RBAC（room 域） | ✅ 现在 | 房主升级为真授权实体 |
| 房间角色 | host / co-host / speaker / listener | ✅ 现在 | 原子权限组合成角色，含举手上麦玩法 |
| 全局/租户角色 | 角色作用域扩展 | 🔮 未来 | 讲「角色 scope 化」 |
| 多租户 | 模型预留 scope，不做组织 UI | 🔮 未来 | 讲「租户感知设计」 |
| 身份扩展 | IdentityProvider 接口 | 🔮 未来 | 讲「多登录方式演进」 |
| 细粒度授权 | Zanzibar / SpiceDB 概念 | 🔮 未来 | 讲对象级权限 |

---

## 十一、落地分层（MVP → 演进）

### MVP（第一步，占比最大，全✅）
1. PostgreSQL 接入 + config + docker 容器
2. `users` 表 + bcrypt 注册/登录
3. Access + Refresh 双 token 会话
4. **WebSocket 握手鉴权** + userID 绑定
5. mini-RBAC，把「房主」升级为 `room:admin` 角色，并落地房间角色（host / co-host / speaker / listener + 静音/踢人 + 举手上麦）

### 演进（后续）
6. `identities` 表 / IdentityProvider 接口 → 接 GitHub 等第三方登录（🔮）
7. `organizations` 概念 + 全链路 org 过滤 → 多租户（🔮）
8. 好友、录制回放 → 引入 Zanzibar/SpiceDB 这类关系型权限（🔮）

---

## 附录：本次讨论澄清的三个概念速查

- **多租户**：一套系统给多家组织用，数据/权限靠 `org_id` 隔离，难在「所有查询和权限都带组织维度，漏一处就跨租户越权」。
- **接 GitHub 登录意味着什么**：用户多一种登录凭据；`identities` 表加一条 `provider=github` 记录即可，users 表不用改——正因为 users/identities 一开始就解耦。
- **为什么回放/好友用 Zanzibar**：前者是「录音 与 谁能看」这类对象间关系型授权，后者是「A 与 B 是朋友」关系；RBAC 用「角色」表达关系很别扭，Zanzibar 用「关系元组 + 图遍历」天然表达并支持继承/传导。
