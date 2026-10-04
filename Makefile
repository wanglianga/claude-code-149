.PHONY: help build run test e2e reset-db tidy vet fmt docker-up docker-verify docker-down

help: ## 显示可用目标
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n",$$1,$$2}'

build: ## 编译二进制到本地
	go build -mod=vendor -o bin/server ./cmd/server

run: ## 本地运行（需已启动 PG 5433 与 Redis 6379，见 README）
	ALLOW_TEST_CLOCK=1 \
	DATABASE_URL='postgres://appuser@127.0.0.1:5433/badminton?sslmode=disable' \
	REDIS_ADDR=127.0.0.1:6379 \
	HTTP_ADDR=:8080 go run -mod=vendor ./cmd/server

vet:
	go vet -mod=vendor ./...

fmt:
	gofmt -w cmd internal

test: ## 端到端验证（需先以 verify 配置启动服务，或本地已起服务）
	bash scripts/e2e_test.sh

e2e: ## 本地一键：重置 PG/Redis -> 起服务(关闭自动任务) -> 跑 e2e
	bash scripts/local-e2e.sh

docker-up: ## Docker 一键启动（默认配置，含自动后台任务）
	docker compose up -d --build

docker-verify: ## Docker 启动验证环境（关闭自动后台任务）
	docker compose -f docker-compose.yml -f docker-compose.verify.yml up -d --build

docker-down:
	docker compose down -v
