# 部署说明

部署分两块：

- **CI/CD 与镜像构建** —— 本仓 `.github/workflows/ci.yml`
- **线上编排与配置** —— 独立配置仓 `deploy-config`（生产 compose / nginx / otel / grafana / 部署脚本 / 加密配置）

## CI/CD 流程（本仓 ci.yml）

1. 校验前端可构建、后端测试通过（`go test -race`）。
2. 分别构建前端 / 后端 / Agent 三个镜像。
3. 镜像推 **GHCR**（主目标），再从 GHCR 尽力同步到**阿里云 ACR**（国内拉取加速）。
4. **一次 SSH** 到服务器，执行配置仓内的 `projects/echat/deploy.sh`；该脚本在服务器本地完成：`git pull` 配置仓 → SOPS 解密 prod 配置 → 按 registry 拉镜像 → 跑内嵌迁移 → `docker compose up -d`。

> 为什么收敛成一次 SSH：旧链路要 7 次跨境建连（每次 3.5~5 分钟），是部署超时的根因。详见 `docs/deploy-config-repo.md`。

## 本仓相关文件

- `.github/workflows/ci.yml` —— 校验 / 构建 / 推镜像 / 触发部署
- `deploy/docker-compose.base.yml` —— 共享基础设施（PostgreSQL + NATS + Redis + SpiceDB），dev / local 复用
- `deploy/docker-compose.dev.yml` —— 原生开发叠加层（只暴露基础设施端口，业务进程在宿主机原生跑）
- `deploy/docker-compose.local.yml` —— 全栈容器叠加层（镜像化 backend / frontend / agent，模拟生产拓扑）
- `deploy/local-up.ps1` / `deploy/build-local.ps1` —— 本地全栈容器的便捷脚本
- 生产编排与配置**不在本仓**，在配置仓 `deploy-config`：`projects/echat/{docker-compose.prod.yml, nginx, otel, prometheus, grafana, deploy.sh, envs/prod}`

## 本地开发

基础设施共享一份 `base`，按需叠加：

```bash
# 原生开发（业务进程宿主机跑，连 127.0.0.1）
docker compose -f deploy/docker-compose.base.yml -f deploy/docker-compose.dev.yml up -d
(cd backend && go run . migrate)      # 迁移 SQL 已内嵌后端二进制
(cd backend && go run .)              # 后端
(cd agent && python -m agent.cmd.main --config agent/config.yaml)   # Agent
(cd frontend && npm run dev)          # 前端

# 全栈容器（镜像化，模拟生产）
.\deploy\local-up.ps1                 # 内部已拼好 base + local

# 附带可观测栈（profile 按需）
docker compose -f deploy/docker-compose.base.yml -f deploy/docker-compose.dev.yml --profile observability up -d
```

端口：dev 用 5433 / 4222 / 6379 / 50051；local 只暴露前端 8081、后端 8082、QUIC 4433、媒体 50000-50100。

## 服务器准备

部署机需要：

- Docker + Docker Compose v2 插件
- 一个可 SSH 登录的用户
- `git`、`age`、`sops`（由配置仓 `scripts/bootstrap.sh` 安装）

服务器目录结构（由 `bootstrap.sh` 建立）：

```text
/srv/echat/config/        # 配置仓 checkout（sparse: projects/echat），无状态、可重 clone
/etc/echat/               # 树外，chmod 600
  keys/age.key            # age 私钥，唯一根信任，由运维从离线备份放入
  backend.config.yaml     # deploy.sh 从 envs/prod 解密落到此处
  agent.config.yaml
  .env                    # 基础设施凭据 + 镜像变量
```

## GitHub Secrets 配置

本仓 `Settings -> Secrets and variables -> Actions`：

**部署相关：**

- `DEPLOY_HOST` / `DEPLOY_PORT` / `DEPLOY_USER` / `DEPLOY_PASSWORD` —— SSH 目标与凭据
- `DEPLOY_CONFIG_PATH`（可选）—— 配置仓 checkout 路径，默认 `/srv/echat/config`

**ACR 相关：**

- `ACR_USERNAME` / `ACR_PASSWORD` —— 阿里云容器镜像服务访问凭证

> ACR 仓库地址已写死在 `ci.yml`（`crpi-cqc86h0ovlhhtcpq.cn-hangzhou.personal.cr.aliyuncs.com/axjj`）。
> GHCR 用 Actions 内置 `GITHUB_TOKEN` + `github.actor`，无需额外 Secret（workflow 已声明 `packages: write`）。
> **配置仓另需自己的只读 Deploy Key**，供服务器 `git clone`（见配置仓 README）。

## 阿里云 ACR 准备

服务器**优先从 ACR 拉取**（国内快），ACR 不可用时自动 fallback GHCR。ACR 非必需但强烈推荐。

1. 开通[阿里云容器镜像服务](https://cr.console.aliyun.com/)个人版（免费）。
2. 创建命名空间 `axjj`（CI 已写死该命名空间）。
3. 设置**固定密码**，配置到本仓 Secrets 的 `ACR_USERNAME` / `ACR_PASSWORD`。

## 触发方式

- 推送到 `main`，且改动命中 `backend/**`、`frontend/**`、`agent/**` 或 workflow 自身
- GitHub Actions 页面手动 `Run workflow`

## 镜像命名规则

**GHCR：** `ghcr.io/<owner>/<repo>-{backend,frontend,agent}`
**ACR：** `crpi-.../axjj/{echat-backend,echat-frontend,echat-agent}`
每个镜像打两个 tag：`${GITHUB_SHA}` 与 `latest`；部署默认用 `${GITHUB_SHA}`。

## 部署后的访问方式

```
浏览器 ── HTTPS(443) ──► gateway(Nginx) ──► frontend(Nginx) ──► backend:8080
   │                                              │
   ├─ WebRTC UDP(50000-50100) ───────────────────► backend(SFU 媒体流)
   └─ WebTransport UDP(4433) ────────────────────► backend(QUIC 信令)
```

- **gateway**：对外 `80`/`443`，TLS 终止后 proxy_pass 到前端容器
- **frontend**：内部 Nginx，反代 `/ws`、`/api` 到 `backend:8080`
- **backend**：内网 `8080`（HTTP/WS）+ `4433/udp`（QUIC）；对外 `50000-50100/udp`（WebRTC）、`4433/udp`（WebTransport）
- **agent**：内网 `50053`（gRPC）
- **redis / spicedb / postgres / nats**：仅内网

## 线上配置与密钥

线上配置**不入本仓**，由配置仓 `projects/echat/envs/prod/` 以 SOPS 加密存放，`deploy.sh` 在服务器解密到 `/etc/echat/`：

- `backend.config.enc.yaml` → `/etc/echat/backend.config.yaml`（后端唯一配置源：连接地址 + 全部凭据）
- `agent.config.enc.yaml` → `/etc/echat/agent.config.yaml`
- `.env.enc` → `/etc/echat/.env`（基础设施凭据；`deploy.sh` 每次覆盖其中的镜像变量）

基础设施容器（postgres / redis / spicedb / grafana）自身的密码经 compose 的 `.env` 变量提供，**后端 `config.yaml` 对应字段须与之一致**，否则连不上或被拒。首次接入与密钥初始化见配置仓 README 与 `scripts/bootstrap.sh`。

## 数据库迁移

迁移 SQL **编译期内嵌进后端二进制**（`backend/migrations/*.sql` + `atlas.sum`，`//go:embed`），服务器不再需要迁移目录，也不再使用 atlas CLI：

- 后端启动期 `store.Migrate` 只做**漂移校验**：比对「二进制内嵌最新版本」与库里 `atlas_schema_revisions` 已应用版本，落后即 FATAL 退出。
- 真正 apply 由 `./server migrate` 子命令完成（`ariga.io/atlas` Go SDK 从内嵌 `embed.FS` apply），幂等；已最新时视为成功，apply 后按 `store.Migrate` 同口径复核。
- `deploy.sh` 在 `up -d` 前以一次性 `migrate` 服务（后端镜像 + `command: ["./server","migrate"]`）执行，失败即终止部署。

手工运维：

```bash
cd /srv/echat/config/projects/echat
IMAGE_TAG=<当前tag> BACKEND_IMAGE=<后端镜像> FRONTEND_IMAGE=<前端镜像> AGENT_IMAGE=<agent镜像> \
ACR_OK=true ACR_REGISTRY=<acr地址> ACR_USERNAME=<用户> ACR_PASSWORD=<密码> ./deploy.sh
```

不走完整部署、只做日常查看/拉镜像/重启时，用同目录的 `ops.sh`（自动带 `--env-file /etc/echat/.env` 与 `-p echat`；直接敲 `docker compose -f docker-compose.prod.yml ...` 会因镜像变量为空报 `invalid reference format`）：

```bash
./ops.sh ps -a            # 容器状态
./ops.sh pull             # 拉镜像（可观测性镜像 502 后重试）
./ops.sh logs -f backend  # 跟看日志
./ops.sh restart gateway  # 换证书后重启网关
```

后端日志若出现 `数据库已应用迁移 <旧> 落后于最新 <新>`，说明所部署镜像落后于库里 schema——用当前镜像 tag 重跑上面的 `deploy.sh` 即可。

## 基础设施两个易踩的点

- **NATS 必须显式 `-sd /data`**：nats-server 未配 `store_dir` 时 JetStream 默认落 `/tmp`，容器重建即丢事件流，`echat_*_natsdata` 卷变空挂（outbox 已投递事件与依赖事件重建的投影一起丢）。base / prod 的 nats 都已带 `-sd /data`。
- **头像上传要两层 nginx 同时放开请求体上限**：后端允许 5 MiB（`backend/profile/avatar.go` 的 `maxAvatarBytes`），nginx 默认 1 MiB。外层 `nginx/gateway.prod.conf`（配置仓）与前端镜像内 `frontend/nginx.conf` 任一层没设 `client_max_body_size`，请求先被挡成 413。

## Let's Encrypt 证书

```bash
certbot certonly --standalone -d echat.qxbnx.cn
```

证书被 gateway（HTTPS 终止）与 backend（WebTransport/QUIC 握手）共用，backend 需同时挂 `live/<domain>` 与 `archive/<domain>`（`live/*.pem` 是相对符号链接，只挂 `live` 会在容器内断链）。

续期替换私钥后，**容器读的是旧 inode**，必须重启让其重解析证书：

```bash
docker restart echat-gateway echat-backend
```

建议挂到续期钩子：

```bash
cat > /etc/letsencrypt/renewal-hooks/deploy/restart-echat.sh <<'EOF'
#!/bin/sh
docker restart echat-gateway echat-backend
EOF
chmod +x /etc/letsencrypt/renewal-hooks/deploy/restart-echat.sh
```

服务器需开放端口：`80/tcp`、`443/tcp`、`4433/udp`、`50000-50100/udp`。
