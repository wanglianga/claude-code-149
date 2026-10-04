package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"courtbook/internal/config"
	"courtbook/internal/server"
	"courtbook/internal/service"
	"courtbook/internal/store"
	"courtbook/internal/worker"
)

func main() {
	cfg := config.Load()
	ctx := context.Background()

	log.Println("连接 PostgreSQL ...")
	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("PostgreSQL 连接失败: %v", err)
	}
	if cfg.MigrateOnStart {
		if err := st.Migrate(ctx); err != nil {
			log.Fatalf("数据库迁移失败: %v", err)
		}
		log.Println("数据库迁移完成")
	}

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Printf("警告: Redis 不可用（%v），继续以无缓存模式运行", err)
	} else {
		log.Println("Redis 连接正常")
	}

	svc := service.New(st, rdb)
	if err := svc.Seed(ctx); err != nil {
		log.Printf("警告: 种子数据初始化失败: %v", err)
	}

	w := worker.New(svc)
	if os.Getenv("WORKER_AUTOSTART") == "false" {
		w.WithAutoStart(false)
	}
	w.Start()
	defer w.Stop()

	srv := server.New(svc, w)
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("HTTP 服务监听 %s（Swagger UI: http://localhost%s/swagger/）", cfg.HTTPAddr, cfg.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务失败: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("正在关闭服务 ...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("关闭超时: %v", err)
	}
}
