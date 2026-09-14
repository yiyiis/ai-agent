# ==============================================================================
# 后端多阶段构建镜像（Go + 最小运行时 Alpine）
# ==============================================================================
FROM golang:1.26-alpine AS builder

WORKDIR /build

# 设置国内 Go 模块加速代理
ENV GOPROXY=https://goproxy.cn,direct

# 先复制依赖元数据，利用 Docker 缓存层
COPY go.mod go.sum ./
RUN go mod download

# 复制源码并执行静态编译
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /build/server .

# ------------------------------------------------------------------------------
# 运行环境
# ------------------------------------------------------------------------------
FROM alpine:3.20

# 设置国内 Alpine 镜像源加速并安装基础运行依赖：bash（Agent Shell 工具基础）、git、时区与证书
RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories && \
    apk add --no-cache \
    bash \
    ca-certificates \
    tzdata \
    git \
    && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && echo "Asia/Shanghai" > /etc/timezone

WORKDIR /app

# 从构建层拷贝二进制与基础配置文件
COPY --from=builder /build/server /app/server
COPY --from=builder /build/etc/config.yaml /app/etc/config.yaml

# 建立工作区与上传文件挂载目录
RUN mkdir -p /app/workspace /app/uploads /app/logs

EXPOSE 8888

# 启动服务（配置自动读取环境变量覆盖敏感项）
ENTRYPOINT ["/app/server", "-conf", "./etc/config.yaml"]
