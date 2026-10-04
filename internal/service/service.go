package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"courtbook/internal/domain"
	"courtbook/internal/store"
)

// Service 业务服务。Now 可被测试覆盖。
type Service struct {
	Store *store.Store
	RDB   *redis.Client
	Now   func() time.Time

	clockMu  sync.RWMutex
	fixedNow *time.Time
}

// SetFixedNow 设置虚拟当前时间（e2e/演示用）；传 nil 恢复真实时钟。
func (s *Service) SetFixedNow(t *time.Time) {
	s.clockMu.Lock()
	defer s.clockMu.Unlock()
	s.fixedNow = t
}

// FixedNow 返回当前虚拟时间指针（未设置为 nil）
func (s *Service) FixedNow() *time.Time {
	s.clockMu.RLock()
	defer s.clockMu.RUnlock()
	return s.fixedNow
}

func New(st *store.Store, rdb *redis.Client) *Service {
	s := &Service{Store: st, RDB: rdb}
	s.Now = func() time.Time {
		s.clockMu.RLock()
		f := s.fixedNow
		s.clockMu.RUnlock()
		if f != nil {
			return f.UTC().Truncate(time.Second)
		}
		return time.Now().UTC().Truncate(time.Second)
	}
	return s
}

// Policy 加载场馆策略并转为领域对象（Redis 缓存 60 秒，运营更新时主动失效）
func (s *Service) Policy(ctx context.Context, venueID int64) (domain.Policy, error) {
	cacheKey := fmt.Sprintf("policy:%d", venueID)
	if s.RDB != nil {
		if raw, err := s.RDB.Get(ctx, cacheKey).Bytes(); err == nil {
			var cached domain.Policy
			if json.Unmarshal(raw, &cached) == nil && cached.VenueID == venueID {
				return cached, nil
			}
		}
	}
	p := domain.Policy{VenueID: venueID, TierDiscountPct: map[string]int{}, TierBookAheadDays: map[string]int{}}
	var peakStart, peakEnd string
	var discountJSON, aheadJSON []byte
	err := s.Store.Pool.QueryRow(ctx, `
		SELECT payment_timeout_min,warmup_minutes,early_entry_minutes,grace_minutes,late_cutoff_minutes,
		       late_fee_ratio,peak_start::text,peak_end::text,peak_cancel_free_hours,peak_cancel_fee_ratio,
		       offpeak_cancel_free_hours,offpeak_cancel_fee_ratio,no_show_ban_threshold,max_players,per_extra_player_cents,
		       tier_book_ahead_days,tier_discount_pct
		FROM venue_policies WHERE venue_id=$1`, venueID).Scan(
		&p.PaymentTimeoutMin, &p.WarmupMinutes, &p.EarlyEntryMinutes, &p.GraceMinutes, &p.LateCutoffMinutes,
		&p.LateFeeRatio, &peakStart, &peakEnd, &p.PeakCancelFreeHours, &p.PeakCancelFeeRatio,
		&p.OffpeakCancelFreeHours, &p.OffpeakCancelFeeRatio, &p.NoShowBanThreshold, &p.MaxPlayers, &p.PerExtraPlayerCents,
		&aheadJSON, &discountJSON)
	if err != nil {
		return p, fmt.Errorf("load policy venue=%d: %w", venueID, err)
	}
	p.PeakStartMin = hhmmToMin(peakStart)
	p.PeakEndMin = hhmmToMin(peakEnd)
	_ = json.Unmarshal(discountJSON, &p.TierDiscountPct)
	_ = json.Unmarshal(aheadJSON, &p.TierBookAheadDays)
	if s.RDB != nil {
		if raw, err := json.Marshal(p); err == nil {
			_ = s.RDB.Set(ctx, cacheKey, raw, 60*time.Second).Err()
		}
	}
	return p, nil
}

// InvalidatePolicyCache 运营更新策略后失效缓存
func (s *Service) InvalidatePolicyCache(ctx context.Context, venueID int64) {
	if s.RDB != nil {
		_ = s.RDB.Del(ctx, fmt.Sprintf("policy:%d", venueID)).Err()
	}
}

func hhmmToMin(s string) int {
	if len(s) < 5 {
		return 0
	}
	h, _ := strconv.Atoi(s[:2])
	m, _ := strconv.Atoi(s[3:5])
	return h*60 + m
}

func (s *Service) priceRules(ctx context.Context, venueID int64) (map[string]domain.PriceRule, error) {
	rows, err := s.Store.Pool.Query(ctx,
		`SELECT scope,hourly_rate_cents,racket_fee_cents,shuttle_fee_cents FROM price_rules WHERE venue_id=$1`, venueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]domain.PriceRule{}
	for rows.Next() {
		var r domain.PriceRule
		if err := rows.Scan(&r.Scope, &r.HourlyRateCents, &r.RacketFeeCents, &r.ShuttleFeeCents); err != nil {
			return nil, err
		}
		out[r.Scope] = r
	}
	return out, rows.Err()
}
