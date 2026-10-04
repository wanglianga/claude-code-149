package service

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"courtbook/internal/domain"
	"courtbook/internal/store"
)

// NoShowResult 爽约判定结果
type NoShowResult struct {
	BookingID    int64          `json:"booking_id"`
	Decision     string         `json:"decision"` // no_show / arrived / venue_fault / skipped
	Evidence     map[string]any `json:"evidence"`
	ForfeitCents int            `json:"forfeit_cents,omitempty"`
	Banned       bool           `json:"banned,omitempty"`
}

// EvidenceBundle 客服争议处理所需的完整证据
type EvidenceBundle struct {
	Booking     *store.Booking        `json:"booking"`
	Events      []store.Event         `json:"events"`
	Ledger      []store.LedgerEntry   `json:"ledger"`
	Payments    []store.Payment       `json:"payments"`
	Maintenance []store.Maintenance   `json:"maintenance"`
	RefundReqs  []store.RefundRequest `json:"refund_requests"`
	Disputes    []store.Dispute       `json:"disputes"`
}

func (s *Service) Evidence(ctx context.Context, bookingID int64) (*EvidenceBundle, error) {
	b, err := s.Store.GetBooking(ctx, bookingID)
	if err != nil {
		return nil, err
	}
	evs, _ := s.Store.ListEvents(ctx, bookingID)
	led, _ := s.Store.ListLedger(ctx, bookingID)
	pays, _ := s.Store.ListPayments(ctx, bookingID)
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id,venue_id,court_id,start_time,end_time,reason,created_by,created_at,released_at
		FROM maintenance_windows WHERE court_id=$1
		  AND tstzrange(start_time,end_time,'[)') && tstzrange($2,$3,'[)')
		ORDER BY start_time`, b.CourtID, b.StartTime.Add(-2*time.Hour), b.EndTime.Add(2*time.Hour))
	var maints []store.Maintenance
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var m store.Maintenance
			if rows.Scan(&m.ID, &m.VenueID, &m.CourtID, &m.StartTime, &m.EndTime, &m.Reason, &m.CreatedBy, &m.CreatedAt, &m.ReleasedAt) == nil {
				maints = append(maints, m)
			}
		}
	}
	reqs, _ := s.listRefundRequests(ctx, bookingID)
	disps, _ := s.listDisputes(ctx, bookingID)
	return &EvidenceBundle{Booking: b, Events: evs, Ledger: led, Payments: pays,
		Maintenance: maints, RefundReqs: reqs, Disputes: disps}, nil
}

// ClassifyNoShow 对单张订单做爽约判定（worker 到 late_cutoff 后调用，亦可人工触发）。
func (s *Service) ClassifyNoShow(ctx context.Context, bookingID int64) (*NoShowResult, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	b, err := lockBooking(ctx, tx, bookingID)
	if err != nil {
		return nil, err
	}
	res := &NoShowResult{BookingID: bookingID, Evidence: map[string]any{}}

	if b.Status != domain.StatusPaidWaiting && b.Status != domain.StatusWarmup && b.Status != domain.StatusInPlay {
		res.Decision = "skipped"
		res.Evidence["reason"] = "订单状态无需判定：" + b.Status
		return res, nil
	}
	pol, err := s.Policy(ctx, b.VenueID)
	if err != nil {
		return nil, err
	}
	cutoff := b.StartTime.Add(time.Duration(pol.LateCutoffMinutes) * time.Minute)
	if s.Now().Before(cutoff) {
		res.Decision = "skipped"
		res.Evidence["reason"] = fmt.Sprintf("未到判定截止（开场后 %d 分钟）", pol.LateCutoffMinutes)
		return res, nil
	}

	// 证据汇总（全部进入事件流，客服无需猜订单表）
	res.Evidence["gate_opened_at"] = b.GateOpenedAt
	res.Evidence["front_signed_at"] = b.FrontSignedAt
	res.Evidence["checked_in_at"] = b.CheckedInAt
	res.Evidence["light_warmup_at"] = b.LightWarmupAt
	res.Evidence["light_on_at"] = b.LightOnAt
	res.Evidence["arrival_status"] = b.ArrivalStatus

	// 1) 场馆原因：开场窗口内存在未解除的维护锁定
	var activeMaint int
	var maintReason string
	if err := tx.QueryRow(ctx, `
		SELECT count(*), COALESCE(max(reason),'') FROM maintenance_windows
		WHERE court_id=$1 AND released_at IS NULL
		  AND tstzrange(start_time,end_time,'[)') && tstzrange($2,$3,'[)')`,
		b.CourtID, b.StartTime, cutoff).Scan(&activeMaint, &maintReason); err != nil {
		return nil, err
	}
	res.Evidence["active_maintenance"] = activeMaint
	if activeMaint > 0 {
		return s.decideVenueFaultTx(ctx, tx, b, res, "爽约判定时发现场地维护锁定："+maintReason)
	}

	// 2) 有任何到场信号（闸机或前台）→ 非爽约
	present := b.GateOpenedAt != nil || b.FrontSignedAt != nil || b.CheckedInAt != nil
	var pendingRefund int
	_ = tx.QueryRow(ctx,
		`SELECT count(*) FROM refund_requests WHERE booking_id=$1 AND status='pending'`, b.ID).Scan(&pendingRefund)
	res.Evidence["pending_refund_request"] = pendingRefund

	if present {
		res.Decision = "arrived"
		// 灯开但人在等待且最终没上场：不算爽约；是否补偿由客服/前台处理
		res.Evidence["note"] = "存在闸机/前台到场信号，判定为已到场（可能迟到或在场边等待）"
		// 已过结束时间仍处于活动状态（如只签到未走灯控）的订单正常结案
		if !s.Now().Before(b.EndTime) {
			effectiveEnd := b.EndTime.Add(time.Duration(b.CompMinutesGranted) * time.Minute)
			if _, err := tx.Exec(ctx, `
				UPDATE bookings SET status='finished',
				  light_off_at=COALESCE(light_off_at,$2),
				  actual_end_time=COALESCE(actual_end_time,$2),
				  actual_start_time=COALESCE(actual_start_time,checked_in_at,light_on_at,start_time),
				  updated_at=now()
				WHERE id=$1 AND status IN ('paid_waiting','warmup','in_play')`,
				b.ID, effectiveEnd); err != nil {
				return nil, err
			}
			res.Evidence["finalized"] = "已到场订单正常结案"
		}
		if err := s.Store.LogEvent(ctx, tx, b.ID, "noshow.cleared", "system", "scheduler", res.Evidence); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return res, nil
	}

	// 3) 无到场信号：灯控自动开启不等于用户上场 → 爽约
	lightNote := "灯控未开启"
	if b.LightOnAt != nil {
		lightNote = "灯控自动开启但无闸机/前台到场信号：灯开不代表上场，按爽约处理（可申诉）"
	}
	res.Evidence["light_note"] = lightNote
	res.Decision = "no_show"

	forfeit := b.PaidCents - b.RefundCents
	if forfeit > 0 {
		if err := s.Store.AddLedger(ctx, tx, b.ID, store.LedgerAdd{
			Kind: domain.LedgerNoShowForfeit, AmountCents: forfeit,
			Source: "system:noshow", Reason: "爽约罚没已付金额",
		}); err != nil {
			return nil, err
		}
		res.ForfeitCents = forfeit
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bookings SET status='no_show',arrival_status='no_show',pay_status=CASE WHEN $2>0 THEN 'forfeited' ELSE pay_status END,
		  no_show_classified_at=now(),no_show_counted=true,
		  light_off_at=COALESCE(light_off_at,now()),actual_end_time=COALESCE(actual_end_time,now()),
		  updated_at=now()
		WHERE id=$1`, b.ID, forfeit); err != nil {
		return nil, err
	}
	// 补偿时长不退（爽约方责任）——仅记录
	if _, err := tx.Exec(ctx,
		`UPDATE members SET no_show_count=no_show_count+1 WHERE id=$1`, b.MemberID); err != nil {
		return nil, err
	}
	var total int
	var wasBanned bool
	if err := tx.QueryRow(ctx, `SELECT no_show_count,banned FROM members WHERE id=$1`, b.MemberID).Scan(&total, &wasBanned); err != nil {
		return nil, err
	}
	if total >= pol.NoShowBanThreshold && !wasBanned {
		if _, err := tx.Exec(ctx, `UPDATE members SET banned=true WHERE id=$1`, b.MemberID); err != nil {
			return nil, err
		}
		res.Banned = true
	}
	res.Evidence["member_no_show_count"] = total
	res.Evidence["ban_threshold"] = pol.NoShowBanThreshold
	if err := s.Store.LogEvent(ctx, tx, b.ID, "noshow.classified", "system", "scheduler", res.Evidence); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

// decideVenueFaultTx 场馆原因：订单关闭、全额退款、归还补偿时长，不计爽约
func (s *Service) decideVenueFaultTx(ctx context.Context, tx pgx.Tx, b *store.Booking, res *NoShowResult, reason string) (*NoShowResult, error) {
	res.Decision = "venue_fault"
	res.Evidence["note"] = reason
	refund := b.PaidCents - b.RefundCents
	if refund > 0 {
		if err := s.issueRefund(ctx, tx, b, refund, "refund_venue", "refund_venue", "场馆原因全额退款", "system", "nosweep"); err != nil {
			return nil, err
		}
	}
	if err := s.restoreCredits(ctx, tx, b.MemberID, b.ID, b.CreditMinutesUsed, "system:venue_fault", "场馆原因关闭，归还补偿时长"); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bookings SET status='venue_closed',arrival_status='venue_fault',updated_at=now() WHERE id=$1`, b.ID); err != nil {
		return nil, err
	}
	if err := s.Store.LogEvent(ctx, tx, b.ID, "booking.venue_closed", "system", "scheduler", res.Evidence); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

// SweepNoShow 批量扫描到达判定截止的订单（worker 调用）
func (s *Service) SweepNoShow(ctx context.Context) ([]NoShowResult, error) {
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT b.id FROM bookings b JOIN venue_policies p ON p.venue_id=b.venue_id
		WHERE b.status IN ('paid_waiting','warmup','in_play')
		  AND b.start_time + make_interval(mins => p.late_cutoff_minutes) < $1
		ORDER BY b.id`, s.Now())
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []NoShowResult
	for _, id := range ids {
		r, err := s.ClassifyNoShow(ctx, id)
		if err != nil {
			continue
		}
		if r.Decision != "skipped" {
			out = append(out, *r)
		}
	}
	return out, nil
}
