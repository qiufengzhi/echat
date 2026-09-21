# 构建阶段（go.mod 声明 go 1.26.0，需同步升级基础镜像）
# REG 用于本地 DaoCloud 镜像加速（deploy/docker-compose.local.yml 传入），默认官方源
ARG REG=docker.io/library
FROM ${REG}/golang:1.26-alpine AS builder

WORKDIR /app

# 安装依赖（opus-dev + 编译工具链用于 hraban/opus CGo 编码器）
RUN apk add --no-cache git opus-dev gcc musl-dev pkgconf

# 先复制依赖描述文件，利用 Docker 层缓存
COPY go.mod go.sum ./
ENV GOPROXY=https://goproxy.cn,direct
RUN go mod download

# 复制源码
COPY . .

# 构建（原生 libopus 编码器需 CGo + opus-dev，默认开启）
RUN CGO_ENABLED=1 GOOS=linux go build -o server .

# 运行阶段
FROM ${REG}/alpine:3.21

WORKDIR /app

# 安装 ca-certificates 和 libopus 运行时库
RUN apk --no-cache add ca-certificates opus

# 从构建阶段复制二进制文件
COPY --from=builder /app/server .
COPY --from=builder /app/handlers ./handlers

# 暴露端口
EXPOSE 8080

# 启动命令
CMD ["./server"]
