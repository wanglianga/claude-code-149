package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"courtbook/internal/domain"
	"courtbook/internal/store"
)

var (
	ErrSlotConflict   = errors.New("场地该时段已被锁定")
	ErrMaintenance    = errors.New("场地该时段处于维护锁定")
	ErrOutsideHours   = errors.New("时段不在开放时间且无临时加场")
	ErrBanned         = errors.New("会员已被封禁（爽约次数超限）")
	ErrBookAhead      = errors.New("超出该会员等级可提前预约天数")
	ErrInvalidState   = errors.New("订单状态不允许该操作")
	ErrPaymentTimeout = errors.New("支付已超时")
	ErrTooManyPlayers = errors.New("人数超过上限")
)

// CreateBookingInput 创建预约入参
type CreateBookingInput struct {
	VenueID       int64     `json:"venue_id"`
	CourtID       int64     `json:"court_id"`
	MemberID      int64     `json:"member_id"`
	StartTime     time.Time `json:"start_time"`
	EndTime       time.Time `json:"end_time"`
	PlayerCount   int       `json:"player_count"`
	RentRacket    bool      `json:"rent_racket"`
	BuyShuttle    bool      `json:"buy_shuttle"`
	UseCreditMins int       `json:"use_credit_minutes"` // 使用补偿时长（分钟）
}

// QuoteResultView 报价视图
type QuoteView struct {
	domain.QuoteResult
	Peak            bool   `json:"peak"`
	PayDeadline     string `json:"pay_deadline"`
	CreditAvailable int    `json:"credit_available_minutes"`
}

// Quote 对外报价（不落库）
func (s *Service) Quote(ctx context.Context, in CreateBookingInput) (*QuoteView, error) {
	if !in.EndTime.After(in.StartTime) {
		return nil, errors.New("结束时间必须晚于开始时间")
	}
	pol, err := s.Policy(ctx, in.VenueID)
	if err != nil {
		return nil, err
	}
	rules, err := s.priceRules(ctx, in.VenueID)
	if err != nil {
		return nil, err
	}
	if in.PlayerCount < 1 {
		in.PlayerCount = 1
	}
	availCredit, _ := s.TotalCreditMinutes(ctx, in.MemberID)
	use := in.UseCreditMins
	if use > availCredit {
		use = availCredit
	}
	q := domain.Quote(pol, rules, domain.QuoteInput{
		Start: in.StartTime, End: in.EndTime, Tier: s.memberTier(ctx, in.MemberID),
		PlayerCount: in.PlayerCount, RentRacket: in.RentRacket, BuyShuttle: in.BuyShuttle,
		CreditMinutesToUse: use,
	})
	return &QuoteView{
		QuoteResult:     q,
		Peak:            domain.IsPeakAt(pol, in.StartTime),
		PayDeadline:     s.Now().Add(time.Duration(pol.PaymentTimeoutMin) * time.Minute).Format(time.RFC3339),
		CreditAvailable: availCredit,
	}, nil
}

func (s *Service) memberTier(ctx context.Context, memberID int64) string {
	var tier string
	_ = s.Store.Pool.QueryRow(ctx, `SELECT member_tier FROM members WHERE id=$1`, memberID).Scan(&tier)
	if tier == "" {
		tier = "normal"
	}
	return tier
}

// TotalCreditMinutes 会员可用补偿时长
func (s *Service) TotalCreditMinutes(ctx context.Context, memberID int64) (int, error) {
	var v int
	err := s.Store.Pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(remaining_minutes),0) FROM member_credits WHERE member_id=$1 AND remaining_minutes>0`, memberID).Scan(&v)
	return v, err
}

// checkSlotBookable 校验维护锁定 / 开放时间或加场 / 会员提前天数 / 封禁
func (s *Service) checkSlotBookable(ctx context.Context, tx pgx.Tx, in CreateBookingInput, tier string, pol domain.Policy) error {
	var banned bool
	if err := tx.QueryRow(ctx, `SELECT banned FROM members WHERE id=$1`, in.MemberID).Scan(&banned); err != nil {
		return err
	}
	if banned {
		return ErrBanned
	}
	var courtStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM courts WHERE id=$1 AND venue_id=$2`, in.CourtID, in.VenueID).Scan(&courtStatus); err != nil {
		return fmt.Errorf("场地不存在: %w", err)
	}
	if courtStatus != "active" {
		return fmt.Errorf("场地当前不可预约（%s）", courtStatus)
	}
	// 维护锁定
	var cnt int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM maintenance_windows
		WHERE court_id=$1 AND released_at IS NULL
		  AND tstzrange(start_time,end_time,'[)') && tstzrange($2::timestamptz,$3::timestamptz,'[)')`,
		in.CourtID, in.StartTime, in.EndTime).Scan(&cnt); err != nil {
		return err
	}
	if cnt > 0 {
		return ErrMaintenance
	}
	// 提前预约天数（会员优先），策略表可运营配置
	aheadDays := 2
	switch tier {
	case "silver":
		aheadDays = 4
	case "gold":
		aheadDays = 7
	}
	if d := pol.TierBookAheadDays[tier]; d > 0 {
		aheadDays = d
	}
	if s.Now().Before(in.StartTime) {
		if days := in.StartTime.Sub(s.Now()).Hours() / 24; days > float64(aheadDays) {
			return ErrBookAhead
		}
	}
	// 开放时间 / 临时加场
	var openTime, closeTime string
	if err := tx.QueryRow(ctx, `SELECT open_time::text,close_time::text FROM venues WHERE id=$1`, in.VenueID).Scan(&openTime, &closeTime); err != nil {
		return err
	}
	within := withinOpenHours(in.StartTime, in.EndTime, openTime, closeTime)
	if !within {
		var covered int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM extra_sessions
			WHERE court_id=$1 AND cancelled_at IS NULL
			  AND start_time <= $2 AND end_time >= $3`,
			in.CourtID, in.StartTime, in.EndTime).Scan(&covered); err != nil {
			return err
		}
		if covered == 0 {
			return ErrOutsideHours
		}
	}
	return nil
}

// withinOpenHours 判断 [start,end) 是否完全落在当日营业时段内（不支持跨午夜营业）
func withinOpenHours(start, end time.Time, open, close string) bool {
	st := start.UTC()
	en := end.UTC()
	if st.Format("2006-01-02") != en.Add(-time.Second).Format("2006-01-02") {
		return false
	}
	od := hhmmTime(open, st, time.UTC)
	cd := hhmmTime(close, st, time.UTC)
	return !st.Before(od) && !en.After(cd)
}

func hhmmTime(hhmm string, base time.Time, loc *time.Location) time.Time {
	var h, m int
	fmt.Sscanf(hhmm, "%d:%d", &h, &m)
	return time.Date(base.Year(), base.Month(), base.Day(), h, m, 0, 0, loc)
}

// CreateBooking 创建预约：报价 + 占用场地（GiST 排他约束兜底防并发重复锁定）+ 抵扣补偿时长
func (s *Service) CreateBooking(ctx context.Context, in CreateBookingInput) (*store.Booking, *QuoteView, error) {
	if !in.EndTime.After(in.StartTime) {
		return nil, nil, errors.New("结束时间必须晚于开始时间")
	}
	if in.PlayerCount < 1 {
		return nil, nil, errors.New("人数至少为 1")
	}
	if in.StartTime.Before(s.Now().Add(-time.Minute)) {
		return nil, nil, errors.New("不能预约过去的时段")
	}
	pol, err := s.Policy(ctx, in.VenueID)
	if err != nil {
		return nil, nil, err
	}
	if in.PlayerCount > pol.MaxPlayers {
		// 预约阶段允许超员下单（拼场灰度），但报价即计入超员费 —— 这里改为拦截，超员走前台现场修正
		return nil, nil, ErrTooManyPlayers
	}
	rules, err := s.priceRules(ctx, in.VenueID)
	if err != nil {
		return nil, nil, err
	}
	tier := s.memberTier(ctx, in.MemberID)

	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)

	if err := s.checkSlotBookable(ctx, tx, in, tier, pol); err != nil {
		return nil, nil, err
	}

	availCredit, err := s.totalCreditMinutesTx(ctx, tx, in.MemberID)
	if err != nil {
		return nil, nil, err
	}
	useCredit := in.UseCreditMins
	if useCredit > availCredit {
		useCredit = availCredit
	}
	q := domain.Quote(pol, rules, domain.QuoteInput{
		Start: in.StartTime, End: in.EndTime, Tier: tier,
		PlayerCount: in.PlayerCount, RentRacket: in.RentRacket, BuyShuttle: in.BuyShuttle,
		CreditMinutesToUse: useCredit,
	})

	orderNo := genToken("CB", 10)
	deadline := s.Now().Add(time.Duration(pol.PaymentTimeoutMin) * time.Minute)

	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO bookings(order_no,venue_id,court_id,member_id,member_tier_snapshot,
			start_time,end_time,player_count,rent_racket,buy_shuttle,
			status,arrival_status,pay_status,pay_deadline,
			slot_cents,discount_cents,credit_cents,credit_minutes_used,addons_cents)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'pending_payment','unknown','unpaid',$11,
			$12,$13,$14,$15,$16)
		RETURNING id`,
		orderNo, in.VenueID, in.CourtID, in.MemberID, tier,
		in.StartTime, in.EndTime, in.PlayerCount, in.RentRacket, in.BuyShuttle,
		deadline,
		q.SlotCents, q.DiscountCents, q.CreditCents, q.CreditMinutes, q.AddonsCents).Scan(&id)
	if err != nil {
		if store.IsConflictError(err) {
			return nil, nil, ErrSlotConflict
		}
		return nil, nil, err
	}

	// 报价条目落台账（每条注明来源 system:quote）
	for _, ln := range q.Lines {
		if err := s.Store.AddLedger(ctx, tx, id, store.LedgerAdd{
			Kind: ln.Kind, AmountCents: ln.AmountCents, Minutes: ln.Minutes,
			Source: "system:quote", Reason: ln.Reason,
		}); err != nil {
			return nil, nil, err
		}
	}
	// 预占补偿时长
	if q.CreditMinutes > 0 {
		if err := s.consumeCredits(ctx, tx, in.MemberID, id, q.CreditMinutes); err != nil {
			return nil, nil, err
		}
	}
	if err := s.Store.LogEvent(ctx, tx, id, "booking.created", "member", fmt.Sprint(in.MemberID),
		map[string]any{"order_no": orderNo, "quote": q, "pay_deadline": deadline}); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		if store.IsConflictError(err) {
			return nil, nil, ErrSlotConflict
		}
		return nil, nil, err
	}
	b, err := s.Store.GetBooking(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	view := &QuoteView{QuoteResult: q, Peak: domain.IsPeakAt(pol, in.StartTime),
		PayDeadline: deadline.Format(time.RFC3339), CreditAvailable: availCredit}
	return b, view, nil
}

func (s *Service) totalCreditMinutesTx(ctx context.Context, tx pgx.Tx, memberID int64) (int, error) {
	var v int
	err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(remaining_minutes),0) FROM member_credits WHERE member_id=$1 AND remaining_minutes>0`,
		memberID).Scan(&v)
	return v, err
}

// consumeCredits FIFO 扣减补偿时长
func (s *Service) consumeCredits(ctx context.Context, tx pgx.Tx, memberID, bookingID int64, minutes int) error {
	rows, err := tx.Query(ctx,
		`SELECT id,remaining_minutes FROM member_credits WHERE member_id=$1 AND remaining_minutes>0 ORDER BY id FOR UPDATE`,
		memberID)
	if err != nil {
		return err
	}
	type cr struct {
		id  int64
		rem int
	}
	var list []cr
	for rows.Next() {
		var c cr
		if err := rows.Scan(&c.id, &c.rem); err != nil {
			rows.Close()
			return err
		}
		list = append(list, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	left := minutes
	for _, c := range list {
		take := c.rem
		if take > left {
			take = left
		}
		if _, err := tx.Exec(ctx,
			`UPDATE member_credits SET remaining_minutes=remaining_minutes-$2 WHERE id=$1`, c.id, take); err != nil {
			return err
		}
		left -= take
		if left == 0 {
			break
		}
	}
	if left > 0 {
		return fmt.Errorf("补偿时长不足，缺少 %d 分钟", left)
	}
	return s.Store.AddLedger(ctx, tx, bookingID, store.LedgerAdd{
		Kind: "comp_time", AmountCents: 0, Minutes: minutes,
		Source: "system:credit_consume", Reason: "预约使用补偿时长",
	})
}

// restoreCredits 退订/爽约平反时归还补偿时长
func (s *Service) restoreCredits(ctx context.Context, tx pgx.Tx, memberID, bookingID int64, minutes int, source, reason string) error {
	if minutes <= 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO member_credits(member_id,minutes,remaining_minutes,source,booking_id,note)
		VALUES ($1,$2,$2,$3,$4,$5)`, memberID, minutes, source, bookingID, reason); err != nil {
		return err
	}
	return s.Store.AddLedger(ctx, tx, bookingID, store.LedgerAdd{
		Kind: "comp_time", AmountCents: 0, Minutes: minutes,
		Source: source, Reason: reason,
	})
}

// Pay 模拟支付网关回调：支付成功后发放入场码。支付超时订单拒绝支付。
func (s *Service) Pay(ctx context.Context, bookingID int64, method string) (*store.Booking, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	b, err := lockBooking(ctx, tx, bookingID)
	if err != nil {
		return nil, err
	}
	switch b.Status {
	case domain.StatusPendingPayment:
		if b.PayDeadline != nil && s.Now().After(*b.PayDeadline) {
			return nil, ErrPaymentTimeout
		}
	case domain.StatusPaidWaiting, domain.StatusWarmup, domain.StatusInPlay:
		return b, nil // 支付幂等
	default:
		return nil, ErrInvalidState
	}
	amount := b.SlotCents - b.DiscountCents - b.CreditCents + b.AddonsCents
	if amount < 0 {
		amount = 0
	}
	code := genCode(6)
	if _, err := tx.Exec(ctx, `
		UPDATE bookings SET status='paid_waiting',pay_status='paid',paid_at=now(),entry_code=$2,
		  paid_cents=$3,updated_at=now() WHERE id=$1`,
		b.ID, code, amount); err != nil {
		return nil, err
	}
	if amount > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO payments(booking_id,direction,amount_cents,method,reason,created_by)
			VALUES ($1,'pay',$2,$3,'预约支付','member')`, b.ID, amount, method); err != nil {
			return nil, err
		}
	}
	if err := s.Store.LogEvent(ctx, tx, b.ID, "payment.succeeded", "system", "gateway",
		map[string]any{"amount_cents": amount, "method": method, "entry_code": code}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Store.GetBooking(ctx, bookingID)
}

// CancelResult 退订结果
type CancelResult struct {
	Booking     *store.Booking      `json:"booking"`
	FeeCents    int                 `json:"fee_cents"`
	RefundCents int                 `json:"refund_cents"`
	Lines       []store.LedgerEntry `json:"lines"`
}

// Cancel 会员/前台退订。unpaid 直接取消；paid 按高峰退订规则扣费退款。
func (s *Service) Cancel(ctx context.Context, bookingID int64, actorType, actorID, reason string, venueFault bool) (*CancelResult, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	b, err := lockBooking(ctx, tx, bookingID)
	if err != nil {
		return nil, err
	}
	if b.Status == domain.StatusCancelled || b.Status == domain.StatusExpired ||
		b.Status == domain.StatusNoShow || b.Status == domain.StatusVenueClosed || b.Status == domain.StatusFinished {
		return nil, ErrInvalidState
	}
	now := s.Now()
	res := &CancelResult{}

	if b.Status == domain.StatusPendingPayment {
		// 未支付：无费用直接释放
		if _, err := tx.Exec(ctx, `
			UPDATE bookings SET status='cancelled',cancel_time=now(),cancel_by=$2,cancel_reason=$3,updated_at=now()
			WHERE id=$1`, b.ID, actorType+":"+actorID, reason); err != nil {
			return nil, err
		}
		if err := s.restoreCredits(ctx, tx, b.MemberID, b.ID, b.CreditMinutesUsed, "system:cancel_restore", "未支付退订，归还补偿时长"); err != nil {
			return nil, err
		}
		if err := s.Store.LogEvent(ctx, tx, b.ID, "booking.cancelled", actorType, actorID,
			map[string]any{"reason": reason, "paid": false}); err != nil {
			return nil, err
		}
	} else {
		// 已支付
		slotPaid := b.SlotCents - b.DiscountCents - b.CreditCents
		addons := b.AddonsCents
		var fee, refund int
		cancelReason := reason
		if venueFault {
			fee = 0
			refund = slotPaid + addons
			cancelReason = "场馆原因退订：" + reason
		} else {
			pol, err := s.Policy(ctx, b.VenueID)
			if err != nil {
				return nil, err
			}
			cq := domain.CancellationQuote(pol, b.StartTime, now, slotPaid)
			fee = cq.FeeCents
			// 附加费：开场前全退，开场后不退
			addonRefund := 0
			if now.Before(b.StartTime) {
				addonRefund = addons
			}
			refund = cq.RefundCents + addonRefund
			cancelReason = reason + "（" + cq.Reason + "）"
		}
		if refund > b.PaidCents-b.RefundCents {
			refund = b.PaidCents - b.RefundCents
		}
		if fee > 0 {
			if err := s.Store.AddLedger(ctx, tx, b.ID, store.LedgerAdd{
				Kind: "cancel_fee", AmountCents: fee, Source: "system:cancel",
				Reason: "退订违约金", CreatedBy: actorType + ":" + actorID,
			}); err != nil {
				return nil, err
			}
		}
		if refund > 0 {
			if err := s.issueRefund(ctx, tx, b, refund, "refund_cancel",
				venueFaultRefundKind(venueFault), "退订退款", actorType, actorID); err != nil {
				return nil, err
			}
		}
		payStatus := "paid"
		finalStatus := domain.StatusCancelled
		if venueFault {
			payStatus = "refunded"
			finalStatus = domain.StatusVenueClosed
			if err := s.restoreCredits(ctx, tx, b.MemberID, b.ID, b.CreditMinutesUsed, "system:venue_fault", "场馆原因退订，归还补偿时长"); err != nil {
				return nil, err
			}
		} else {
			switch {
			case refund == 0:
				payStatus = "forfeited"
			case refund >= b.PaidCents:
				payStatus = "refunded"
			default:
				payStatus = "partially_refunded"
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE bookings SET status=$2,pay_status=$3,cancel_time=now(),cancel_by=$4,cancel_reason=$5,
			  light_off_at=COALESCE(light_off_at,now()),updated_at=now()
			WHERE id=$1`,
			b.ID, finalStatus, payStatus, actorType+":"+actorID, cancelReason); err != nil {
			return nil, err
		}
		if err := s.Store.LogEvent(ctx, tx, b.ID, "booking.cancelled", actorType, actorID, map[string]any{
			"reason": cancelReason, "paid": true, "fee_cents": fee, "refund_cents": refund, "venue_fault": venueFault,
		}); err != nil {
			return nil, err
		}
		res.FeeCents = fee
		res.RefundCents = refund
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	res.Booking, _ = s.Store.GetBooking(ctx, bookingID)
	res.Lines, _ = s.Store.ListLedger(ctx, bookingID)
	return res, nil
}

func venueFaultRefundKind(v bool) string {
	if v {
		return "refund_venue"
	}
	return "refund_cancel"
}

// issueRefund 退款：写 payments + 台账 + 订单汇总。调用方须持有事务与行锁。
func (s *Service) issueRefund(ctx context.Context, tx pgx.Tx, b *store.Booking, amount int, ledgerKind, reasonKey, note, actorType, actorID string) error {
	if amount <= 0 {
		return nil
	}
	maxRefund := b.PaidCents - b.RefundCents
	if amount > maxRefund {
		amount = maxRefund
	}
	if amount <= 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payments(booking_id,direction,amount_cents,method,reason,created_by)
		VALUES ($1,'refund',$2,'original',$3,$4)`,
		b.ID, amount, note, actorType+":"+actorID); err != nil {
		return err
	}
	if err := s.Store.AddLedger(ctx, tx, b.ID, store.LedgerAdd{
		Kind: ledgerKind, AmountCents: -amount, Source: actorType + ":" + actorID,
		Reason: note, CreatedBy: actorType + ":" + actorID,
	}); err != nil {
		return err
	}
	newRefunded := b.RefundCents + amount
	payStatus := "partially_refunded"
	if newRefunded >= b.PaidCents {
		payStatus = "refunded"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bookings SET refund_cents=$2,pay_status=$3,updated_at=now() WHERE id=$1`,
		b.ID, newRefunded, payStatus); err != nil {
		return err
	}
	b.RefundCents = newRefunded
	b.PayStatus = payStatus
	return nil
}

// ExpireStale 支付超时释放（worker 调用）。warmup/in_play 绝不在释放范围内。
func (s *Service) ExpireStale(ctx context.Context) (int, error) {
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id FROM bookings
		WHERE status='pending_payment' AND pay_deadline < $1
		ORDER BY id FOR UPDATE SKIP LOCKED`, s.Now())
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		if err := s.expireOne(ctx, id); err != nil {
			continue
		}
		n++
	}
	return n, nil
}

func (s *Service) expireOne(ctx context.Context, id int64) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	b, err := lockBooking(ctx, tx, id)
	if err != nil {
		return err
	}
	if b.Status != domain.StatusPendingPayment {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE bookings SET status='expired',updated_at=now() WHERE id=$1`, id); err != nil {
		return err
	}
	if err := s.restoreCredits(ctx, tx, b.MemberID, id, b.CreditMinutesUsed, "system:expire_restore", "支付超时释放，归还补偿时长"); err != nil {
		return err
	}
	if err := s.Store.LogEvent(ctx, tx, id, "booking.expired", "system", "scheduler",
		map[string]any{"reason": "支付超时，场地释放"}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Availability 某日场地占用视图（预约 + 维护 + 加场）
func (s *Service) Availability(ctx context.Context, venueID int64, date time.Time) (map[string]any, error) {
	dayStart := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	dayEnd := dayStart.Add(24 * time.Hour)
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT 'booking' AS kind, id, court_id, start_time, end_time, status, order_no, '' AS reason
		FROM bookings
		WHERE venue_id=$1 AND status IN ('pending_payment','paid_waiting','warmup','in_play')
		  AND tstzrange(start_time,end_time,'[)') && tstzrange($2,$3,'[)')
		UNION ALL
		SELECT 'maintenance', id, court_id, start_time, end_time, '', '', reason
		FROM maintenance_windows
		WHERE venue_id=$1 AND released_at IS NULL
		  AND tstzrange(start_time,end_time,'[)') && tstzrange($2,$3,'[)')
		UNION ALL
		SELECT 'extra_session', id, court_id, start_time, end_time, COALESCE(cancelled_at::text,''), '', note
		FROM extra_sessions
		WHERE venue_id=$1
		  AND tstzrange(start_time,end_time,'[)') && tstzrange($2,$3,'[)')
		ORDER BY court_id, start_time`, venueID, dayStart, dayEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type slot struct {
		Kind    string    `json:"kind"`
		RefID   int64     `json:"ref_id"`
		CourtID int64     `json:"court_id"`
		Start   time.Time `json:"start_time"`
		End     time.Time `json:"end_time"`
		Status  string    `json:"status,omitempty"`
		OrderNo string    `json:"order_no,omitempty"`
		Reason  string    `json:"reason,omitempty"`
	}
	courts := map[int64][]slot{}
	for rows.Next() {
		var sl slot
		if err := rows.Scan(&sl.Kind, &sl.RefID, &sl.CourtID, &sl.Start, &sl.End, &sl.Status, &sl.OrderNo, &sl.Reason); err != nil {
			return nil, err
		}
		courts[sl.CourtID] = append(courts[sl.CourtID], sl)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	courtRows, err := s.Store.Pool.Query(ctx,
		`SELECT id,court_no,status FROM courts WHERE venue_id=$1 ORDER BY court_no`, venueID)
	if err != nil {
		return nil, err
	}
	defer courtRows.Close()
	type court struct {
		ID      int64  `json:"id"`
		CourtNo string `json:"court_no"`
		Status  string `json:"status"`
		Slots   []slot `json:"slots"`
	}
	var list []court
	for courtRows.Next() {
		var c court
		if err := courtRows.Scan(&c.ID, &c.CourtNo, &c.Status); err != nil {
			return nil, err
		}
		c.Slots = courts[c.ID]
		if c.Slots == nil {
			c.Slots = []slot{}
		}
		list = append(list, c)
	}
	return map[string]any{"venue_id": venueID, "date": dayStart.Format("2006-01-02"), "courts": list}, rows.Err()
}

// ---------- 工具 ----------

func lockBooking(ctx context.Context, tx pgx.Tx, id int64) (*store.Booking, error) {
	row := tx.QueryRow(ctx, `SELECT `+store.BookingCols+` FROM bookings WHERE id=$1 FOR UPDATE`, id)
	return store.ScanBookingRow(row)
}

func genToken(prefix string, nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return prefix + strings.ToUpper(hex.EncodeToString(b))
}

func genCode(n int) string {
	const digits = "0123456789"
	b := make([]byte, n)
	rb := make([]byte, n)
	_, _ = rand.Read(rb)
	for i := range b {
		b[i] = digits[int(rb[i])%len(digits)]
	}
	return string(b)
}
