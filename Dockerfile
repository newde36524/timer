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

# 安装必要的工具和 Redis
RUN apk add --no-cache redis supervisor tzdata

# 创建数据目录
RUN mkdir -p /data /var/log/supervisor

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
