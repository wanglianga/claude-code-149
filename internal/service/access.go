package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"courtbook/internal/domain"
	"courtbook/internal/store"
)

var ErrGateDenied = errors.New("闸机拒绝放行")

// GateScan 闸机扫码（入场码）。
// 放行条件：已支付（paid_waiting/warmup/in_play）且处于入场窗口。
// 灰度：若前一场延时未结束，会员可入场但在场边等待（arrival_status=waiting）。
func (s *Service) GateScan(ctx context.Context, code, gateID, signalID string) (*store.Booking, map[string]any, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	b, err := lockByEntryCode(ctx, tx, code)
	if err != nil {
		return nil, nil, err
	}
	if signalID != "" {
		ok, err := s.Store.AcquireSignal(ctx, tx, "gate:"+signalID)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			return b, map[string]any{"duplicate": true}, nil
		}
	}
	now := s.Now()
	switch b.Status {
	case domain.StatusPaidWaiting, domain.StatusWarmup, domain.StatusInPlay:
	default:
		return nil, nil, fmt.Errorf("%w：订单状态 %s", ErrGateDenied, b.Status)
	}
	pol, err := s.Policy(ctx, b.VenueID)
	if err != nil {
		return nil, nil, err
	}
	windowStart := b.StartTime.Add(-time.Duration(pol.EarlyEntryMinutes) * time.Minute)
	windowEnd := b.EndTime.Add(time.Duration(pol.LateCutoffMinutes) * time.Minute)
	if now.Before(windowStart) {
		return nil, nil, fmt.Errorf("%w：未到入场时间（提前 %d 分钟放行）", ErrGateDenied, pol.EarlyEntryMinutes)
	}
	if now.After(windowEnd) {
		return nil, nil, fmt.Errorf("%w：已超过入场截止", ErrGateDenied)
	}

	arrival := domain.ClassifyArrival(pol, b.StartTime, now)
	waiting := false
	if now.Before(b.StartTime.Add(time.Duration(pol.GraceMinutes) * time.Minute)) {
		// 前一场是否延时未结束（有更早的订单仍 in_play 且灯未关）
		waiting = s.courtBusy(ctx, tx, b.CourtID, now, b.ID)
	}

	updates := []string{"gate_opened_at=COALESCE(gate_opened_at,now())", "updated_at=now()"}
	args := []any{b.ID}
	if b.CheckedInAt == nil {
		updates = append(updates, "checked_in_at=now()", "checkin_via='gate'")
	}
	setArrival := arrival
	if waiting {
		setArrival = domain.ArrivalWaiting
	}
	if b.ArrivalStatus == domain.ArrivalUnknown || b.ArrivalStatus == "" {
		updates = append(updates, fmt.Sprintf("arrival_status=$%d", len(args)+1))
		args = append(args, setArrival)
	}
	q := fmt.Sprintf("UPDATE bookings SET %s WHERE id=$1", joinSets(updates))
	if _, err := tx.Exec(ctx, q, args...); err != nil {
		return nil, nil, err
	}

	// 迟到罚金（开场宽限后才入场）
	if arrival == domain.ArrivalLate && b.CheckedInAt == nil {
		fee := domain.LateFee(pol, b.PaidCents-b.AddonsCents)
		if fee > 0 {
			if err := s.Store.AddLedger(ctx, tx, b.ID, store.LedgerAdd{
				Kind: domain.LedgerLateFee, AmountCents: fee,
				Source: "system:gate_late", Reason: fmt.Sprintf("开场 %d 分钟后入场，按规则计迟到罚金", int(now.Sub(b.StartTime).Minutes())),
			}); err != nil {
				return nil, nil, err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE bookings SET surcharge_cents=surcharge_cents+$2,updated_at=now() WHERE id=$1`, b.ID, fee); err != nil {
				return nil, nil, err
			}
		}
	}

	if err := s.Store.LogEvent(ctx, tx, b.ID, "gate.scanned", "gate", gateID, map[string]any{
		"arrival": arrival, "waiting_courtside": waiting, "at": now,
	}); err != nil {
		return nil, nil, err
	}
	if waiting {
		if err := s.Store.LogEvent(ctx, tx, b.ID, "member.waiting", "gate", gateID, map[string]any{
			"note": "用户已到场，前一场延时未结束，在场边等待", "at": now,
		}); err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	fresh, _ := s.Store.GetBooking(ctx, b.ID)
	return fresh, map[string]any{"admitted": true, "arrival": arrival, "waiting_courtside": waiting}, nil
}

func joinSets(sets []string) string {
	out := ""
	for i, s := range sets {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

// courtBusy 该场地当前是否有【另一场】进行中订单且灯仍亮着（前一场延时未结束）。
// 注意：延时场的预约 end_time 可能已过，但只要灯未关、实际结束时间未确定，场地物理上仍被占用。
func (s *Service) courtBusy(ctx context.Context, tx pgx.Tx, courtID int64, at time.Time, selfID int64) bool {
	var n int
	_ = tx.QueryRow(ctx, `
		SELECT count(*) FROM bookings
		WHERE court_id=$1 AND id<>$2 AND status IN ('in_play','warmup')
		  AND light_off_at IS NULL
		  AND (actual_end_time IS NULL OR actual_end_time > $3)
		  AND start_time < $3`, courtID, selfID, at).Scan(&n)
	return n > 0
}

// FrontdeskSign 前台人工签到（晚到/手机没电等）。
func (s *Service) FrontdeskSign(ctx context.Context, bookingID int64, staffID int64, note string) (*store.Booking, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	b, err := lockBooking(ctx, tx, bookingID)
	if err != nil {
		return nil, err
	}
	if b.Status != domain.StatusPaidWaiting && b.Status != domain.StatusWarmup && b.Status != domain.StatusInPlay {
		return nil, ErrInvalidState
	}
	pol, _ := s.Policy(ctx, b.VenueID)
	now := s.Now()
	arrival := b.ArrivalStatus
	if b.CheckedInAt == nil {
		arrival = domain.ClassifyArrival(pol, b.StartTime, now)
		if _, err := tx.Exec(ctx, `
			UPDATE bookings SET front_signed_at=now(),checked_in_at=COALESCE(checked_in_at,now()),
			  checkin_via=CASE WHEN checkin_via IS NULL THEN 'frontdesk' ELSE checkin_via END,
			  arrival_status=CASE WHEN arrival_status='unknown' THEN $2 ELSE arrival_status END,
			  updated_at=now() WHERE id=$1`, b.ID, arrival); err != nil {
			return nil, err
		}
		if arrival == domain.ArrivalLate {
			fee := domain.LateFee(pol, b.PaidCents-b.AddonsCents)
			if fee > 0 {
				_ = s.Store.AddLedger(ctx, tx, b.ID, store.LedgerAdd{
					Kind: domain.LedgerLateFee, AmountCents: fee,
					Source: fmt.Sprintf("frontdesk:%d", staffID), Reason: "前台签到时判定迟到",
					CreatedBy: fmt.Sprintf("frontdesk:%d", staffID),
				})
				_, _ = tx.Exec(ctx, `UPDATE bookings SET surcharge_cents=surcharge_cents+$2 WHERE id=$1`, b.ID, fee)
			}
		}
	} else {
		_, _ = tx.Exec(ctx, `UPDATE bookings SET front_signed_at=now(),updated_at=now() WHERE id=$1`, b.ID)
	}
	if err := s.Store.LogEvent(ctx, tx, b.ID, "frontdesk.signed", "frontdesk", fmt.Sprint(staffID),
		map[string]any{"arrival": arrival, "note": note}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Store.GetBooking(ctx, bookingID)
}

// MarkWaiting 前台登记：会员已在场边等待（前一场延时）。
func (s *Service) MarkWaiting(ctx context.Context, bookingID int64, staffID int64, note string) (*store.Booking, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	b, err := lockBooking(ctx, tx, bookingID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE bookings SET arrival_status='waiting',checked_in_at=COALESCE(checked_in_at,now()),
		   front_signed_at=COALESCE(front_signed_at,now()),updated_at=now() WHERE id=$1`, b.ID); err != nil {
		return nil, err
	}
	if err := s.Store.LogEvent(ctx, tx, b.ID, "member.waiting", "frontdesk", fmt.Sprint(staffID),
		map[string]any{"note": note, "at": s.Now()}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Store.GetBooking(ctx, bookingID)
}

// LightWarmup 灯控预热（worker 到点触发或硬件回调）。
// warmup 是独立状态：支付超时释放绝不影响已进入预热的订单。
func (s *Service) LightWarmup(ctx context.Context, bookingID int64, signalID string) (*store.Booking, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	b, err := lockBooking(ctx, tx, bookingID)
	if err != nil {
		return nil, err
	}
	if signalID != "" {
		ok, err := s.Store.AcquireSignal(ctx, tx, "warmup:"+signalID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return b, nil
		}
	}
	if b.Status != domain.StatusPaidWaiting {
		return b, nil // 幂等：已预热/进行中直接返回
	}
	if _, err := tx.Exec(ctx,
		`UPDATE bookings SET status='warmup',light_warmup_at=COALESCE(light_warmup_at,now()),updated_at=now() WHERE id=$1`, b.ID); err != nil {
		return nil, err
	}
	if err := s.Store.LogEvent(ctx, tx, b.ID, "light.warmup", "light", "controller",
		map[string]any{"at": s.Now(), "note": "灯控预热，场地进入不可释放状态"}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Store.GetBooking(ctx, bookingID)
}

// LightOn 开灯（到点自动或硬件回调）。灯开 ≠ 人上场：到场以闸机/前台信号为准。
func (s *Service) LightOn(ctx context.Context, bookingID int64, signalID string) (*store.Booking, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	b, err := lockBooking(ctx, tx, bookingID)
	if err != nil {
		return nil, err
	}
	if signalID != "" {
		ok, err := s.Store.AcquireSignal(ctx, tx, "lighton:"+signalID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return b, nil
		}
	}
	switch b.Status {
	case domain.StatusPaidWaiting, domain.StatusWarmup:
	default:
		return b, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bookings SET status='in_play',light_on_at=COALESCE(light_on_at,now()),
		  actual_start_time=COALESCE(actual_start_time,now()),updated_at=now()
		WHERE id=$1`, b.ID); err != nil {
		return nil, err
	}
	present := b.GateOpenedAt != nil || b.FrontSignedAt != nil
	if err := s.Store.LogEvent(ctx, tx, b.ID, "light.on", "light", "controller", map[string]any{
		"at":                      s.Now(),
		"member_present_on_court": present,
		"note":                    "灯已开；是否上场以闸机/前台信号为准",
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Store.GetBooking(ctx, bookingID)
}

// LightOffResult 关灯结算结果
type LightOffResult struct {
	Booking          *store.Booking `json:"booking"`
	OvertimeMinutes  int            `json:"overtime_minutes"`
	CompMinutesUsed  int            `json:"comp_minutes_used"`
	OvertimeFeeCents int            `json:"overtime_fee_cents"`
}

// LightOff 关灯并结算延时。offAt 晚于 end+补偿分钟的部分按 15 分钟进位计延时费。
func (s *Service) LightOff(ctx context.Context, bookingID int64, offAt time.Time, signalID string) (*LightOffResult, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	b, err := lockBooking(ctx, tx, bookingID)
	if err != nil {
		return nil, err
	}
	if signalID != "" {
		ok, err := s.Store.AcquireSignal(ctx, tx, "lightoff:"+signalID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return &LightOffResult{Booking: b}, nil
		}
	}
	if b.Status != domain.StatusInPlay && b.Status != domain.StatusWarmup && b.Status != domain.StatusPaidWaiting {
		return &LightOffResult{Booking: b}, nil
	}
	if offAt.IsZero() {
		offAt = s.Now()
	}
	res := &LightOffResult{}
	rules, err := s.priceRules(ctx, b.VenueID)
	if err != nil {
		return nil, err
	}
	pol, err := s.Policy(ctx, b.VenueID)
	if err != nil {
		return nil, err
	}

	effectiveEnd := b.EndTime.Add(time.Duration(b.CompMinutesGranted) * time.Minute)
	status := domain.StatusFinished
	if offAt.After(effectiveEnd) {
		fee, billable, compUsed := domain.OvertimeFee(rules, pol, effectiveEnd, offAt, 0)
		res.OvertimeMinutes = int(offAt.Sub(b.EndTime).Minutes())
		res.CompMinutesUsed = compUsed
		res.OvertimeFeeCents = fee
		if fee > 0 {
			if err := s.Store.AddLedger(ctx, tx, b.ID, store.LedgerAdd{
				Kind: domain.LedgerOvertimeFee, AmountCents: fee, Minutes: billable,
				Source: "light:off_overtime",
				Reason: fmt.Sprintf("延时 %d 分钟（含补偿时长 %d 分钟），按 %d 分钟计费",
					res.OvertimeMinutes, b.CompMinutesGranted, billable),
			}); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE bookings SET surcharge_cents=surcharge_cents+$2,overtime_minutes=$3,updated_at=now() WHERE id=$1`,
				b.ID, fee, res.OvertimeMinutes); err != nil {
				return nil, err
			}
		}
		if err := s.Store.LogEvent(ctx, tx, b.ID, "play.overtime", "light", "controller", map[string]any{
			"off_at": offAt, "overtime_minutes": res.OvertimeMinutes,
			"comp_granted": b.CompMinutesGranted, "fee_cents": fee,
		}); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bookings SET status=$2,light_off_at=$3,actual_end_time=$4,actual_start_time=COALESCE(actual_start_time,light_on_at,start_time),
		  updated_at=now()
		WHERE id=$1`, b.ID, status, offAt, offAt); err != nil {
		return nil, err
	}
	if err := s.Store.LogEvent(ctx, tx, b.ID, "light.off", "light", "controller", map[string]any{"at": offAt}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	res.Booking, _ = s.Store.GetBooking(ctx, bookingID)
	return res, nil
}

// GrantCompMinutes 前台临时补偿时长（灯控故障/等待等），当场有效，抵扣延时。
func (s *Service) GrantCompMinutes(ctx context.Context, bookingID, staffID int64, minutes int, note string) (*store.Booking, error) {
	if minutes <= 0 {
		return nil, errors.New("补偿分钟数必须为正")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	b, err := lockBooking(ctx, tx, bookingID)
	if err != nil {
		return nil, err
	}
	if b.Status != domain.StatusWarmup && b.Status != domain.StatusInPlay && b.Status != domain.StatusPaidWaiting {
		return nil, ErrInvalidState
	}
	if _, err := tx.Exec(ctx,
		`UPDATE bookings SET comp_minutes_granted=comp_minutes_granted+$2,updated_at=now() WHERE id=$1`, b.ID, minutes); err != nil {
		return nil, err
	}
	if err := s.Store.AddLedger(ctx, tx, b.ID, store.LedgerAdd{
		Kind: domain.LedgerCompTime, AmountCents: 0, Minutes: minutes,
		Source:    fmt.Sprintf("frontdesk:%d", staffID),
		Reason:    "前台当场补偿时长：" + note,
		CreatedBy: fmt.Sprintf("frontdesk:%d", staffID),
	}); err != nil {
		return nil, err
	}
	if err := s.Store.LogEvent(ctx, tx, b.ID, "comp.granted", "frontdesk", fmt.Sprint(staffID),
		map[string]any{"minutes": minutes, "note": note, "scope": "current_booking"}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Store.GetBooking(ctx, bookingID)
}

// ReportPlayers 现场上报实际人数（会员带朋友拼场导致超员）。
func (s *Service) ReportPlayers(ctx context.Context, bookingID, staffID int64, players int) (*store.Booking, int, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx)
	b, err := lockBooking(ctx, tx, bookingID)
	if err != nil {
		return nil, 0, err
	}
	pol, err := s.Policy(ctx, b.VenueID)
	if err != nil {
		return nil, 0, err
	}
	over, fee := domain.OverPlayerFee(pol, players)
	_ = over
	_ = fee
	already := b.OverPlayers
	newOver := players - pol.MaxPlayers
	if newOver < 0 {
		newOver = 0
	}
	if _, err := tx.Exec(ctx,
		`UPDATE bookings SET player_count=$2,over_players=$3,updated_at=now() WHERE id=$1`, b.ID, players, newOver); err != nil {
		return nil, 0, err
	}
	charge := 0
	if newOver > already {
		charge = (newOver - already) * pol.PerExtraPlayerCents
		if err := s.Store.AddLedger(ctx, tx, b.ID, store.LedgerAdd{
			Kind: domain.LedgerOverPlayerFee, AmountCents: charge,
			Source:    fmt.Sprintf("frontdesk:%d", staffID),
			Reason:    fmt.Sprintf("现场实际 %d 人，超出上限 %d 人，按 %d 人补费", players, pol.MaxPlayers, newOver-already),
			CreatedBy: fmt.Sprintf("frontdesk:%d", staffID),
		}); err != nil {
			return nil, 0, err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE bookings SET surcharge_cents=surcharge_cents+$2,updated_at=now() WHERE id=$1`, b.ID, charge); err != nil {
			return nil, 0, err
		}
	}
	if err := s.Store.LogEvent(ctx, tx, b.ID, "players.reported", "frontdesk", fmt.Sprint(staffID),
		map[string]any{"players": players, "over": newOver, "extra_fee_cents": charge}); err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	fresh, _ := s.Store.GetBooking(ctx, bookingID)
	return fresh, charge, nil
}

func lockByEntryCode(ctx context.Context, tx pgx.Tx, code string) (*store.Booking, error) {
	row := tx.QueryRow(ctx, `SELECT `+store.BookingCols+` FROM bookings WHERE entry_code=$1 FOR UPDATE`, code)
	b, err := store.ScanBookingRow(row)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("%w：入场码无效", ErrGateDenied)
	}
	return b, err
}
