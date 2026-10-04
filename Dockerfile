# syntax=docker/dockerfile:1
# ---- 构建阶段 ----
FROM golang:1.22-bookworm AS build
WORKDIR /src
# 先拷依赖（已 vendor，无需联网拉模块）
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- 运行阶段 ----
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /app/server
# Swagger UI 已通过 go:embed 打进二进制，无需额外静态文件
ENV HTTP_ADDR=:8080 \
    DATABASE_URL=postgres://gym:gympass@postgres:5432/badminton?sslmode=disable \
    REDIS_ADDR=redis:6379 \
    TOKEN_SECRET=change-me-in-production \
    ALLOW_TEST_CLOCK=false
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/server"]
