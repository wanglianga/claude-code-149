package service

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Clock 统一时钟。生产环境走 time.Now；
// 当 ALLOW_TEST_CLOCK 开启时，可通过 Redis key clock:offset_seconds 设置偏移，
// 让后台任务与 HTTP 请求共享同一“虚拟时间”，便于演示爽约/超时等时间相关流程。
type Clock struct {
	RDB    *redis.Client
	Allow  bool
	offset time.Duration // 进程内兜底（Redis 不可用时）
}

func (c *Clock) Now(ctx context.Context) time.Time {
	if !c.Allow {
		return time.Now().UTC()
	}
	s, err := c.RDB.Get(ctx, "clock:offset_seconds").Result()
	if err == nil {
		if n, perr := strconv.Atoi(s); perr == nil {
			return time.Now().UTC().Add(time.Duration(n) * time.Second)
		}
	}
	return time.Now().UTC().Add(c.offset)
}

func (c *Clock) SetOffset(ctx context.Context, seconds int64) error {
	c.offset = time.Duration(seconds) * time.Second
	if !c.Allow {
		return nil
	}
	return c.RDB.Set(ctx, "clock:offset_seconds", seconds, 0).Err()
}

func (c *Clock) Reset(ctx context.Context) error {
	c.offset = 0
	return c.RDB.Del(ctx, "clock:offset_seconds").Err()
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
