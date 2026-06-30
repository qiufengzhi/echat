# 部署说明

这套部署方案使用 GitHub Actions 完成以下流程：

1. 校验前端是否可以构建、后端是否可以通过测试。
2. 分别构建前端和后端 Docker 镜像。
3. 把镜像推送到 `ghcr.io`。
4. 通过 SSH 登录服务器，上传生产环境 `compose` 文件和 Nginx HTTPS 网关配置。
5. 在服务器上执行 `docker compose pull` 和 `docker compose up -d`，同时更新网关、前端和后端。

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
  nginx/
    gateway.prod.conf
```

## GitHub Secrets 配置

需要在 GitHub 仓库的 `Settings -> Secrets and variables -> Actions` 中配置这些 Secrets：

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
- `GHCR_DEPLOY_USERNAME`
  服务器拉取 GHCR 镜像时使用的 GitHub 用户名。
- `GHCR_DEPLOY_TOKEN`
  服务器拉取 GHCR 镜像时使用的 Token，至少需要 `read:packages` 权限。

## GHCR 权限要求

工作流推送镜像使用的是 GitHub Actions 自带的 `GITHUB_TOKEN`，因此工作流文件里已经开启了：

- `packages: write`

如果服务器需要拉取私有镜像，则 `GHCR_DEPLOY_TOKEN` 至少需要：

- `read:packages`

如果你的仓库或镜像策略更严格，也可以单独创建一个专用机器人账号来拉取镜像。

## 触发方式

以下情况会自动触发部署：

- 推送到 `main`
- 推送到 `master`
- 修改了前端、后端、部署配置或工作流文件

也支持在 GitHub Actions 页面手动点击 `Run workflow` 触发。

## 镜像命名规则

工作流会自动生成两个镜像名：

- `ghcr.io/<owner>/<repo>-backend`
- `ghcr.io/<owner>/<repo>-frontend`

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

也就是说，外部访问入口默认只有 `gateway` 容器。

## 首次部署前准备

1. 上传并编辑后端配置（端口、STUN、ASR 开关等通用配置）：
```bash
# 从项目复制到服务器，按需修改
scp backend/config.yaml user@host:/srv/echat/backend/config.yaml
```

2. 复制环境变量模板并填入密钥：
```bash
cp .env.example .env
# 编辑 .env，填入 ASR 密钥（如需启用）
```

3. Let's Encrypt 证书获取（如果还没有）：

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
