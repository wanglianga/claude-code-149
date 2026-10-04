# ---- 构建阶段 ----
FROM golang:1.19-alpine AS builder

WORKDIR /src

# 先拷依赖清单以利用层缓存
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- 运行阶段 ----
FROM alpine:3.18

RUN apk add --no-cache ca-certificates wget tzdata && \
    adduser -D -u 10001 appuser
ENV TZ=Asia/Shanghai

WORKDIR /app
COPY --from=builder /out/server /app/server

USER appuser
EXPOSE 8080

ENV HTTP_ADDR=":8080" \
    DATABASE_URL="postgres://postgres:postgres@db:5432/courtbook?sslmode=disable" \
    REDIS_ADDR="redis:6379" \
    MIGRATE_ON_START="true" \
    DEVICE_KEY="device-key-123" \
    ENABLE_TEST_CLOCK="false"

HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=5 \
    CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/app/server"]
