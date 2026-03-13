# 构建阶段
FROM golang:1.23-alpine AS builder

WORKDIR /app

# 安装依赖
RUN apk add --no-cache git gcc musl-dev

# 复制 go mod 文件
COPY go.mod go.sum ./
RUN go mod download

# 复制源代码
COPY . .

# 构建
RUN CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -o timer .

# 运行阶段
FROM alpine:3.19

WORKDIR /app

# 安装必要的工具、Redis、Node.js 和 Python 以及常用 Linux 命令
RUN apk add --no-cache \
    redis \
    supervisor \
    tzdata \
    nodejs \
    python3 \
    py3-pip \
    # 网络工具
    curl \
    wget \
    bind-tools \
    iputils \
    net-tools \
    # Shell 和文本处理
    bash \
    grep \
    sed \
    gawk \
    # 压缩工具
    tar \
    gzip \
    unzip \
    # 进程和系统工具
    procps \
    htop \
    # 编辑器
    vim \
    # 其他常用工具
    jq \
    sqlite \
    openssl \
    ca-certificates \
    # 文件操作
    findutils \
    coreutils

# 创建数据目录
RUN mkdir -p /data /var/log/supervisor

# 设置时区
ENV TZ=Asia/Shanghai

# 从构建阶段复制二进制文件
COPY --from=builder /app/timer .
COPY --from=builder /app/web ./web
COPY config.yaml .
COPY supervisord.conf /etc/supervisord.conf

# 复制启动脚本
COPY start.sh .
RUN chmod +x start.sh

# 暴露端口
EXPOSE 8080

# 启动
CMD ["./start.sh"]
