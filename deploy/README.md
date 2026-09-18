# 部署说明

这套部署方案使用 GitHub Actions 完成以下流程：

1. 校验前端是否可以构建、后端是否可以通过测试。
2. 分别构建前端、后端和 Agent 三个 Docker 镜像。
3. 把镜像**推送到 GHCR**（主构建目标），再从 GHCR 同步到**阿里云 ACR**（国内服务器拉取用，尽力而为）。
4. 通过 SSH 登录服务器，上传生产环境 `compose` 文件和 Nginx HTTPS 网关配置。
5. 在服务器上**优先从 ACR 拉取镜像**并执行 `docker compose up -d`；ACR 不可用时自动 fallback 到 GHCR 拉取。

## 仓库内相关文件

- `.github/workflows/ci.yml`
  负责 CI/CD 工作流：校验、构建镜像、推送镜像、部署服务器。
- `deploy/docker-compose.prod.yml`
  负责服务器端的网关、前端、后端与基础设施（PostgreSQL / NATS / Redis / SpiceDB）容器编排。
- `deploy/nginx/gateway.prod.conf`
  负责最外层 Nginx HTTPS 入口配置。
- `deploy/postgres-init/01-spicedb.sh`
  PostgreSQL 首次初始化数据卷时自动创建 SpiceDB 关系元组存储库（`echat_spicedb`），随 CI 上传到服务器。

## 服务器准备

部署机至少需要提前安装以下组件：

- Docker
- Docker Compose v2 插件；如果服务器只有旧版 `docker-compose`，部署脚本会用先停旧容器再新建的方式兼容。
- 一个可以通过 SSH 登录的用户

建议服务器目录结构如下：

```text
/srv/echat/
  docker-compose.prod.yml
  .env                       # CI 自动写入四个镜像名变量，首次部署 touch 占位
  postgres-init/             # CI 自动上传，postgres 首次建卷时执行建库脚本
   01-spicedb.sh
  backend/
    config.yaml              # 后端配置，手动维护和更新
  agent/
    config.yaml              # Agent LLM 配置
  nginx/
    gateway.prod.conf
```

## GitHub Secrets 配置

需要在 GitHub 仓库的 `Settings -> Secrets and variables -> Actions` 中配置这些 Secrets：

**部署相关：**

- `DEPLOY_HOST`
  服务器 IP 或域名。
- `DEPLOY_PORT`
  SSH 端口，通常是 `22`。
- `DEPLOY_USER`
  SSH 登录用户名。
- `DEPLOY_PASSWORD`
  用于登录服务器的 SSH 密码。
- `DEPLOY_PATH`
  服务器上的部署目录，例如 `/opt/echat`。

**ACR（阿里云容器镜像服务）相关：**

- `ACR_USERNAME`
  ACR 登录用户名（在 ACR 控制台「访问凭证」中查看）。
- `ACR_PASSWORD`
  ACR 登录密码（在 ACR 控制台设置固定密码）。

> ACR 仓库地址已写死在 `ci.yml` 中（`crpi-cqc86h0ovlhhtcpq.cn-hangzhou.personal.cr.aliyuncs.com/axjj`），无需单独配置 Secret。

**GHCR 认证：无需额外配置**

工作流推送和拉取 GHCR 使用的是 GitHub Actions 内置的 `GITHUB_TOKEN` + `github.actor`，只要 workflow 中声明了 `packages: write` 权限即可，**不需要**手动创建 `GHCR_DEPLOY_USERNAME` / `GHCR_DEPLOY_TOKEN` 这类 Secret。

## GHCR 权限要求

工作流推送镜像到 GHCR 使用的是 GitHub Actions 自带的 `GITHUB_TOKEN`，因此工作流文件里已经开启了：

- `packages: write`

服务器部署**优先从 ACR 拉取**（国内速度快），但 ACR 不可用时仍有 GHCR fallback 兜底，因此仓库需保持 `packages: write` 权限。

## 阿里云 ACR 准备

服务器**优先从 ACR 拉取**镜像（国内速度快），ACR 不可用时自动 fallback 到 GHCR。ACR 作为加速通道非必需但强烈推荐。

1. 登录[阿里云容器镜像服务](https://cr.console.aliyun.com/)，开通**个人版**（免费）。
2. 创建命名空间 `axjj`（必须叫这个，CI 里已写死 `crpi-cqc86h0ovlhhtcpq.cn-hangzhou.personal.cr.aliyuncs.com/axjj`）。
3. 在 ACR 控制台「访问凭证」中设置**固定密码**。
4. 在 GitHub Secrets 中配置 `ACR_USERNAME` 和 `ACR_PASSWORD`。
5. CI 构建时自动推送到 GHCR，再从 GHCR 同步到 ACR（尽力而为，ACR 同步失败不影响整体流程）。

## 触发方式

以下情况会自动触发部署：

- 推送到 `main`
- 修改了前端、后端、Agent、部署配置或工作流文件

也支持在 GitHub Actions 页面手动点击 `Run workflow` 触发。

## 镜像命名规则

工作流会自动生成两套镜像名：

**GHCR（海外 / Actions 内部）：**
- `ghcr.io/<owner>/<repo>-backend`
- `ghcr.io/<owner>/<repo>-frontend`
- `ghcr.io/<owner>/<repo>-agent`

**ACR（国内服务器拉取）：**
- `crpi-cqc86h0ovlhhtcpq.cn-hangzhou.personal.cr.aliyuncs.com/axjj/<repo>-backend`
- `crpi-cqc86h0ovlhhtcpq.cn-hangzhou.personal.cr.aliyuncs.com/axjj/<repo>-frontend`
- `crpi-cqc86h0ovlhhtcpq.cn-hangzhou.personal.cr.aliyuncs.com/axjj/<repo>-agent`

并同时打两个 tag：

- `${GITHUB_SHA}`
- `latest`

部署时默认使用当前提交对应的 `${GITHUB_SHA}`

## 部署后的访问方式

当前生产 `compose` 配置请求链路为：

```
浏览器 ── HTTPS(443) ──► gateway(Nginx) ──► frontend(Nginx) ──► backend:8080
   │                                              │
   ├─ WebRTC UDP(50000-50100) ───────────────────► backend(SFU 媒体流)
   └─ WebTransport UDP(4433) ────────────────────► backend(QUIC 信令)
```

- **gateway 容器**：对外暴露 `80`/`443`，TLS 终止后 proxy_pass 到前端容器
- **frontend 容器**：内部 Nginx，反向代理 `/ws` 和 `/api` 到 `backend:8080`
- **backend 容器**：Go 后端，仅内网暴露 `8080`（HTTP + WebSocket）与 `4433/udp`（WebTransport/QUIC 信令）；对外暴露 `50000-50100/udp` 供浏览器 WebRTC 媒体流直连、`4433/udp` 供 WebTransport 直连；QUIC 握手证书与 gateway 共用一份 Let's Encrypt
- **agent 容器**：Python LLM Agent，仅内网暴露 `50053`（gRPC），Go 后端直连
- **redis 容器**：登录限流计数 + 在线成员心跳 ZSET 的共享热状态存储，仅内网暴露 `6379`
- **spicedb 容器**：对象级授权判定（Zanzibar），关系元组落 PostgreSQL 的 `echat_spicedb` 库，仅内网暴露 `50051`

## 首次部署前准备

1. 上传后端配置：以仓库内 `backend/config.yaml.prod.example` 为模板复制到服务器，替换占位符后作为 `backend/config.yaml`
   > 本文件是后端唯一配置源：连接地址（PostgreSQL / NATS / Redis / SpiceDB / WebTransport）与全部凭据（ASR/TTS 密钥、DB/Redis 密码、SpiceDB 预共享密钥、JWT 签名密钥）都写在这里，模板以 REPLACE 占位；文件不入 git，直接维护在服务器
```bash
# 从项目模板复制到服务器，替换所有 REPLACE 占位（jwt_secret / nat_1_to_1_ip / 阿里云与讯飞密钥）
# database.password / redis.password / spicedb.preshared_key 须与 .env 中给 postgres / redis / spicedb 容器配置的凭据一致
scp backend/config.yaml.prod.example user@host:/srv/echat/backend/config.yaml
```

2. 上传 Agent 配置（LLM 密钥等）：
```bash
scp agent/config.yaml user@host:/srv/echat/agent/config.yaml
```

3. 创建 `.env` 文件（仅首次部署前需初始化空文件，后续 CI 会自动写入镜像名）：
```bash
touch /srv/echat/.env
```

> ⚠️ **注意**：`.env` 文件由 CI 自动管理，每次部署都会覆盖 `BACKEND_IMAGE`、`FRONTEND_IMAGE`、`AGENT_IMAGE`、`IMAGE_TAG` 四个字段。基础设施容器（postgres / redis / spicedb）的密码经 `${VAR:-默认值}` 从 `.env` 读取（不写则用默认值）；后端 `backend/config.yaml` 中对应字段（`database.password` / `redis.password` / `spicedb.preshared_key`）须与之一致，否则后端连不上或授权校验被拒。ASR 密钥、JWT 签名密钥等其余敏感配置直接写进 `backend/config.yaml`（不入 git），Agent LLM 密钥在 `agent/config.yaml`。

`docker-compose.prod.yml` 编排基础设施（PostgreSQL + NATS + Redis + SpiceDB）与业务服务（gateway / frontend / backend / agent）；后端连接地址与全部凭据统一在挂载的 `backend/config.yaml`（生产按 `config.yaml.prod.example` 模板准备，填 compose 服务名与 REPLACE 占位），compose 不再给 backend 注入任何环境变量；基础设施容器（postgres / redis / spicedb）自身的密码仍经 compose 的 `.env` 变量提供，后端 config.yaml 对应字段须与之一致。持久卷 `echat_prod_pgdata` / `echat_prod_natsdata` / `echat_prod_redisdata` 承载数据、事件流与热状态；所有基础设施不映射宿主端口，仅容器内部网络访问。SpiceDB 关系元组复用 PostgreSQL，`echat_spicedb` 库由首次初始化卷时的 `postgres-init` 脚本自动创建。

4. Let's Encrypt 证书获取：

```bash
certbot certonly --standalone -d echat.qxbnx.cn
```

Let's Encrypt 会自动续期，无需额外操作。证书被两处共用：
- Nginx 网关挂载 `fullchain.pem` / `privkey.pem`（HTTPS 终止，Docker 会在宿主侧解析符号链接后挂真实文件）
- backend 容器挂载 `live/<domain>` 与 `archive/<domain>` 两个目录（WebTransport/QUIC 握手，前端默认走 `https://echat.qxbnx.cn:4433` 信令）
  > `live/*.pem` 是指向 `../../archive/<domain>/*.pem` 的相对符号链接，只挂 `live` 会让链接在容器内断链（报 `no such file or directory`），故 archive 必须一起挂

certbot 续期替换私钥后，nginx 经 reload 自动生效；backend 进程在启动时加载证书私钥，因此续期后需重启容器使新私钥生效：

```bash
docker restart echat-backend
```

5. 确认服务器端口开放：
   - 80/tcp（HTTP → HTTPS 重定向）
   - 443/tcp（HTTPS + WebSocket 信令，WebTransport 不可用时的降级通道）
   - 4433/udp（WebTransport / QUIC 信令，浏览器直连后端）
   - 50000-50100/udp（WebRTC SFU 媒体流）

如果服务器只有旧版 `docker-compose`，排查命令可以把上面的 `docker compose` 改成 `docker-compose`。部署时不要直接执行 `docker-compose up -d` 重建旧容器；旧版工具可能读取不到新版镜像元数据里的 `ContainerConfig`，更稳妥的顺序是先 `pull`，再 `down --remove-orphans`，最后 `up -d`。
