package store

import (
	"context"
	"embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type DB struct {
	Pool *pgxpool.Pool
}

func New(ctx context.Context, databaseURL string) (*DB, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	// 等待数据库就绪（compose 中 PostgreSQL 可能稍后才可用）
	deadline := time.Now().Add(60 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if lastErr = pool.Ping(ctx); lastErr == nil {
			return &DB{Pool: pool}, nil
		}
		time.Sleep(2 * time.Second)
	}
	pool.Close()
	return nil, fmt.Errorf("数据库在等待期内不可用: %w", lastErr)
}

func (db *DB) Close() { db.Pool.Close() }

// Migrate 执行内嵌 SQL 迁移（以文件名为版本号，已执行的跳过）
func (db *DB) Migrate(ctx context.Context) error {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	if _, err := db.Pool.Exec(ctx,
		`create table if not exists schema_migrations (version text primary key, applied_at timestamptz not null default now())`); err != nil {
		return err
	}
	for _, e := range entries {
		var exists bool
		if err := db.Pool.QueryRow(ctx, `select exists(select 1 from schema_migrations where version=$1)`, e.Name()).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sqlBytes, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		tx, err := db.Pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migrate %s: %w", e.Name(), err)
		}
		if _, err := tx.Exec(ctx, `insert into schema_migrations(version) values($1)`, e.Name()); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
