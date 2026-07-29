# 部署说明

这套部署方案使用 GitHub Actions 完成以下流程：

1. 校验前端是否可以构建、后端是否可以通过测试。
2. 分别构建前端和后端 Docker 镜像。
3. 把镜像推送到 `ghcr.io` 和**阿里云 ACR**（国内服务器拉取用）。
4. 通过 SSH 登录服务器，上传生产环境 `compose` 文件和 Nginx HTTPS 网关配置。
5. 在服务器上从 **ACR 拉取镜像**并执行 `docker compose up -d`，同时更新网关、前端和后端。

## 仓库内相关文件

- `.github/workflows/ci.yml`
  负责 CI/CD 工作流：校验、构建镜像、推送镜像、部署服务器。
- `deploy/docker-compose.prod.yml`
  负责服务器端的网关、前端、后端容器编排。
- `deploy/nginx/gateway.prod.conf`
  负责最外层 Nginx HTTPS 入口配置。

## 服务器准备

部署机至少需要提前安装以下组件：

- Docker
- Docker Compose v2 插件；如果服务器只有旧版 `docker-compose`，部署脚本会用先停旧容器再新建的方式兼容。
- 一个可以通过 SSH 登录的用户

建议服务器目录结构如下：

```text
/srv/echat/
  docker-compose.prod.yml
  .env                       # 从 .env.example 复制并填入密钥
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

> ACR 仓库地址已写死在 `ci.yml` 中（`crpi-aixkibkv62fgkkyl.cn-heyuan.personal.cr.aliyuncs.com/echat`），无需单独配置 Secret。

**已废弃（改用 ACR 后不再需要）：**

- ~~`GHCR_DEPLOY_USERNAME`~~ — 不再需要，服务器改为从 ACR 拉取。
- ~~`GHCR_DEPLOY_TOKEN`~~ — 不再需要，服务器改为从 ACR 拉取。

## GHCR 权限要求

工作流推送镜像到 GHCR 使用的是 GitHub Actions 自带的 `GITHUB_TOKEN`，因此工作流文件里已经开启了：

- `packages: write`

服务器部署已改为从 ACR 拉取，不再需要 GHCR 拉取权限。

## 阿里云 ACR 准备

国内服务器从 ghcr.io 拉取镜像极慢，因此改为从阿里云 ACR 拉取。

1. 登录[阿里云容器镜像服务](https://cr.console.aliyun.com/)，开通**个人版**（免费）。
2. 创建命名空间 `echat`（必须叫这个，CI 里已写死 `crpi-aixkibkv62fgkkyl.cn-heyuan.personal.cr.aliyuncs.com/echat`）。
3. 在 ACR 控制台「访问凭证」中设置**固定密码**。
4. 在 GitHub Secrets 中配置 `ACR_USERNAME` 和 `ACR_PASSWORD`。
5. CI 构建时自动双推到 GHCR + ACR，服务器从 ACR 拉取即可（国内通常几秒完成）。

## 触发方式

以下情况会自动触发部署：

- 推送到 `main`
- 推送到 `master`
- 修改了前端、后端、Agent、部署配置或工作流文件

也支持在 GitHub Actions 页面手动点击 `Run workflow` 触发。

## 镜像命名规则

工作流会自动生成两套镜像名：

**GHCR（海外 / Actions 内部）：**
- `ghcr.io/<owner>/<repo>-backend`
- `ghcr.io/<owner>/<repo>-frontend`
- `ghcr.io/<owner>/<repo>-agent`

**ACR（国内服务器拉取）：**
- `crpi-aixkibkv62fgkkyl.cn-heyuan.personal.cr.aliyuncs.com/echat/<repo>-backend`
- `crpi-aixkibkv62fgkkyl.cn-heyuan.personal.cr.aliyuncs.com/echat/<repo>-frontend`
- `crpi-aixkibkv62fgkkyl.cn-heyuan.personal.cr.aliyuncs.com/echat/<repo>-agent`

并同时打两个 tag：

- `${GITHUB_SHA}`
- `latest`

实际部署时默认使用当前提交对应的 `${GITHUB_SHA}`，这样更稳，也更容易追踪回滚。

## 部署后的访问方式

当前生产 `compose` 配置默认行为是：

- 网关容器对外暴露宿主机 `80` 和 `443`
- 网关容器把所有 HTTPS 请求转发到前端容器
- 后端容器只在 Compose 内部网络暴露 `8080`
- 前端 Nginx 通过容器名 `backend:8080` 反向代理后端和 WebSocket
- 后端容器对外暴露 `50000-50100/udp`，WebRTC 媒体流浏览器直连必需

也就是说，外部 HTTP/WS 流量经 `gateway` HTTPS 进入，WebRTC 媒体流则走 UDP 端口直连后端。

## 首次部署前准备

1. 上传并编辑后端配置（端口、STUN、ASR 开关等）：
```bash
# 从项目复制到服务器，按需修改
scp backend/config.yaml user@host:/srv/echat/backend/config.yaml
```

2. 上传 Agent 配置（LLM 密钥等）：
```bash
scp agent/config.yaml user@host:/srv/echat/agent/config.yaml
```

3. 创建 `.env` 文件（仅首次部署前需初始化空文件，后续 CI 会自动写入镜像名）：
```bash
touch /srv/echat/.env
```

> ⚠️ **注意**：`.env` 文件由 CI 自动管理，每次部署都会覆盖 `BACKEND_IMAGE`、`FRONTEND_IMAGE`、`AGENT_IMAGE`、`IMAGE_TAG` 四个字段。不要手动在 `.env` 中存放持久密钥，敏感配置（ASR 密钥、LLM API Key 等）应统一放在 `backend/config.yaml` 和 `agent/config.yaml` 中。

4. Let's Encrypt 证书获取（如果还没有）：

```bash
certbot certonly --standalone -d echat.qxbnx.cn
```

4. 确认服务器端口开放：
   - 80/tcp（HTTP → HTTPS 重定向）
   - 443/tcp（HTTPS）
   - 8080/tcp（WebSocket 信令）
   - 50000-50100/udp（WebRTC 媒体流）

Nginx 网关直接挂载 Let's Encrypt 证书（域名 `echat.qxbnx.cn`），无需手动复制：

- `/etc/letsencrypt/live/echat.qxbnx.cn/fullchain.pem`
- `/etc/letsencrypt/live/echat.qxbnx.cn/privkey.pem`

首次部署前，请先用 Certbot 获取证书：

```bash
certbot certonly --standalone -d echat.qxbnx.cn
```

Let's Encrypt 会自动续期，无需额外操作。

## 为什么不用后端直接开 HTTPS

当前 `config.yaml` 中 `server.https_enabled` 默认为 `false`，由最外层 Nginx 统一处理 TLS，后端只监听内网 HTTP，更简单也更易维护。

需要在服务器上修改 `config.yaml` 即可切换：

## 手动排查命令

如果部署后要在服务器上排查，可以执行：

```bash
cd /opt/echat
docker compose --env-file .env -f docker-compose.prod.yml ps
docker compose --env-file .env -f docker-compose.prod.yml logs -f
```

如果服务器只有旧版 `docker-compose`，排查命令可以把上面的 `docker compose` 改成 `docker-compose`。部署时不要直接执行 `docker-compose up -d` 重建旧容器；旧版工具可能读取不到新版镜像元数据里的 `ContainerConfig`，更稳妥的顺序是先 `pull`，再 `down --remove-orphans`，最后 `up -d`。
