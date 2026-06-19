# 构建阶段
FROM golang:1.24-alpine AS builder

WORKDIR /app

# 安装依赖
RUN apk add --no-cache git

# 复制源码
COPY . .

# 下载依赖
ENV GOPROXY=https://goproxy.cn,direct
ENV GOSUMDB=off
RUN go mod tidy

# 构建
RUN CGO_ENABLED=0 GOOS=linux go build -o server .

# 运行阶段
FROM alpine:latest

WORKDIR /app

# 安装 ca-certificates 用于 HTTPS
RUN apk --no-cache add ca-certificates

# 从构建阶段复制二进制文件
COPY --from=builder /app/server .
COPY --from=builder /app/handlers ./handlers

# 暴露端口
EXPOSE 8080

# 启动命令
CMD ["./server"]
