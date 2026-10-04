package service

import (
	"context"
	"time"
)

// Seed 初始化演示数据（幂等）。
func (s *Service) Seed(ctx context.Context) error {
	var venueID int64
	err := s.Store.Pool.QueryRow(ctx, `SELECT id FROM venues WHERE name='城市体育馆' LIMIT 1`).Scan(&venueID)
	if err != nil {
		err = s.Store.Pool.QueryRow(ctx, `
			INSERT INTO venues(name,address,open_time,close_time)
			VALUES ('城市体育馆','市民中心体育路 1 号','08:00','22:00') RETURNING id`).Scan(&venueID)
		if err != nil {
			return err
		}
	}
	// 场地 1-8
	for i := 1; i <= 8; i++ {
		_, _ = s.Store.Pool.Exec(ctx,
			`INSERT INTO courts(venue_id,court_no) VALUES ($1,$2) ON CONFLICT (venue_id,court_no) DO NOTHING`,
			venueID, courtNo(i))
	}
	// 策略
	_, err = s.Store.Pool.Exec(ctx, `
		INSERT INTO venue_policies(venue_id) VALUES ($1) ON CONFLICT (venue_id) DO NOTHING`, venueID)
	if err != nil {
		return err
	}
	// 价格（分）：工作日非高峰 60 元/时，高峰 100，周末 90；租拍 10，购球 25
	for _, pr := range []struct {
		scope                 string
		rate, racket, shuttle int
	}{
		{"weekday_offpeak", 6000, 1000, 2500},
		{"weekday_peak", 10000, 1000, 2500},
		{"weekend", 9000, 1000, 2500},
	} {
		_, _ = s.Store.Pool.Exec(ctx, `
			INSERT INTO price_rules(venue_id,scope,hourly_rate_cents,racket_fee_cents,shuttle_fee_cents)
			VALUES ($1,$2,$3,$4,$5) ON CONFLICT (venue_id,scope) DO NOTHING`,
			venueID, pr.scope, pr.rate, pr.racket, pr.shuttle)
	}
	// 员工账号
	staff := []struct{ user, pw, name, role string }{
		{"admin", "admin123", "运营主管", "admin"},
		{"operator", "oper123", "运营员", "operator"},
		{"front", "front123", "前台小王", "frontdesk"},
		{"cs", "cs123", "客服小李", "cs"},
	}
	for _, st := range staff {
		_, _ = s.Store.Pool.Exec(ctx, `
			INSERT INTO staff_users(username,password_hash,name,role,venue_id)
			VALUES ($1,$2,$3,$4,$5) ON CONFLICT (username) DO NOTHING`,
			st.user, HashPassword(st.pw), st.name, st.role, venueID)
	}
	// 演示会员
	members := []struct{ phone, name, tier string }{
		{"13800000001", "张三（普通会员）", "normal"},
		{"13800000002", "李四（银卡）", "silver"},
		{"13800000003", "王五（金卡）", "gold"},
	}
	for _, m := range members {
		_, _ = s.Store.Pool.Exec(ctx,
			`INSERT INTO members(phone,name,member_tier) VALUES ($1,$2,$3) ON CONFLICT (phone) DO NOTHING`,
			m.phone, m.name, m.tier)
	}
	_ = time.Now
	return nil
}

func courtNo(i int) string {
	return "C" + string(rune('0'+i/10)) + string(rune('0'+i%10))
}
