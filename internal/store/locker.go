package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrLockBusy = errors.New("court slot already locked")

type Locker struct {
	RDB *redis.Client
}

func NewLocker(addr string) *Locker {
	return &Locker{RDB: redis.NewClient(&redis.Options{Addr: addr})}
}

func (l *Locker) Ping(ctx context.Context) error { return l.RDB.Ping(ctx).Err() }

// releaseLockLua 仅当 value 匹配时删除，避免误删他人的锁
var releaseLockLua = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
    return redis.call("del", KEYS[1])
else
    return 0
end`)

// CourtSlotLock 场地+时间段防重锁。key 维度为 court:开始:结束。
// 同一场地同一时段只允许一个请求进入落库临界区；跨时间段重叠由 PG GiST 约束兜底。
type CourtSlotLock struct {
	key   string
	token string
	ttl   time.Duration
}

func courtLockKey(courtID int64, start, end time.Time) string {
	return "lock:court:" + itoa(courtID) + ":" + start.UTC().Format(time.RFC3339) + ":" + end.UTC().Format(time.RFC3339)
}

func itoa(i int64) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = digits[i%10]
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// AcquireCourtLock 非阻塞抢锁；抢不到返回 ErrLockBusy
func (l *Locker) AcquireCourtLock(ctx context.Context, courtID int64, start, end time.Time, ttl time.Duration) (*CourtSlotLock, error) {
	key := courtLockKey(courtID, start, end)
	tok := newToken()
	ok, err := l.RDB.SetNX(ctx, key, tok, ttl).Result()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrLockBusy
	}
	return &CourtSlotLock{key: key, token: tok, ttl: ttl}, nil
}

func (l *Locker) Release(ctx context.Context, lk *CourtSlotLock) error {
	if lk == nil {
		return nil
	}
	return releaseLockLua.Run(ctx, l.RDB, []string{lk.key}, lk.token).Err()
}
