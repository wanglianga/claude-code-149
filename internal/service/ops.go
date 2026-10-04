package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"courtbook/internal/domain"
	"courtbook/internal/store"
)

type pgxTx = pgx.Tx

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func (s *Service) logOpsEvent(ctx context.Context, tx pgxTx, venueID int64, refType string, refID int64,
	eventType string, actorID any, payload map[string]any) error {
	vid := venueID
	id := int64(0)
	switch v := actorID.(type) {
	case int64:
		id = v
	case int:
		id = int64(v)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO ops_events(venue_id,ref_type,ref_id,event_type,actor_type,actor_id,payload)
		VALUES ($1,$2,$3,$4,'operator',$5,$6)`,
		vid, refType, refID, eventType, fmt.Sprint(id), mustJSON(payload))
	return err
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	if b == nil {
		return []byte("{}")
	}
	return b
}

// ---------- 场地维护 ----------

// CreateMaintenance 建立维护锁定；自动妥善处置时段内已生效的订单（场馆原因全额退款）。
func (s *Service) CreateMaintenance(ctx context.Context, venueID, courtID, staffID int64, start, end time.Time, reason string) (map[string]any, error) {
	if !end.After(start) {
		return nil, errors.New("维护结束时间必须晚于开始时间")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var mID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO maintenance_windows(venue_id,court_id,start_time,end_time,reason,created_by)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		venueID, courtID, start, end, reason, staffID).Scan(&mID); err != nil {
		return nil, err
	}
	if err := s.logOpsEvent(ctx, tx, venueID, "maintenance", mID, "maintenance.created", staffID,
		map[string]any{"court_id": courtID, "start": start, "end": end, "reason": reason}); err != nil {
		return nil, err
	}

	// 处置受影响订单
	rows, err := tx.Query(ctx, `
		SELECT id FROM bookings WHERE court_id=$1
		  AND status IN ('pending_payment','paid_waiting','warmup','in_play')
		  AND tstzrange(start_time,end_time,'[)') && tstzrange($2,$3,'[)')
		ORDER BY id FOR UPDATE`, courtID, start, end)
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
	for _, id := range ids {
		b, err := scanLocked(ctx, tx, id)
		if err != nil {
			continue
		}
		if b.Status == domain.StatusPendingPayment {
			_, _ = tx.Exec(ctx, `UPDATE bookings SET status='expired',updated_at=now() WHERE id=$1`, id)
			if err := s.restoreCredits(ctx, tx, b.MemberID, id, b.CreditMinutesUsed,
				fmt.Sprintf("maintenance:%d", mID), "维护锁定释放未支付订单，归还补偿时长"); err != nil {
				return nil, err
			}
			if err := s.Store.LogEvent(ctx, tx, id, "booking.expired", "operator", fmt.Sprint(staffID),
				map[string]any{"maintenance_id": mID, "reason": reason, "note": "未支付订单因维护锁定释放"}); err != nil {
				return nil, err
			}
		} else {
			refund := b.PaidCents - b.RefundCents
			if refund > 0 {
				if err := s.issueRefund(ctx, tx, b, refund, "refund_venue", "refund_venue",
					"场地维护锁定，场馆原因全额退款", "operator", fmt.Sprint(staffID)); err != nil {
					return nil, err
				}
			}
			if err := s.restoreCredits(ctx, tx, b.MemberID, id, b.CreditMinutesUsed,
				fmt.Sprintf("maintenance:%d", mID), "维护锁定，归还补偿时长"); err != nil {
				return nil, err
			}
			_, _ = tx.Exec(ctx, `
				UPDATE bookings SET status='venue_closed',arrival_status='venue_fault',
				  light_off_at=COALESCE(light_off_at,now()),updated_at=now() WHERE id=$1`, id)
		}
		if err := s.Store.LogEvent(ctx, tx, id, "booking.venue_closed", "operator", fmt.Sprint(staffID),
			map[string]any{"maintenance_id": mID, "reason": reason, "refund_cents": b.PaidCents - b.RefundCents}); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"maintenance_id": mID, "affected_bookings": ids}, nil
}

func scanLocked(ctx context.Context, tx pgxTx, id int64) (*store.Booking, error) {
	row := tx.QueryRow(ctx, `SELECT `+store.BookingCols+` FROM bookings WHERE id=$1`, id)
	return store.ScanBookingRow(row)
}

// ReleaseMaintenance 提前解除维护
func (s *Service) ReleaseMaintenance(ctx context.Context, maintenanceID, staffID int64) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var courtID, venueID int64
	if err := tx.QueryRow(ctx,
		`UPDATE maintenance_windows SET released_at=now() WHERE id=$1 AND released_at IS NULL RETURNING court_id,venue_id`,
		maintenanceID).Scan(&courtID, &venueID); err != nil {
		return err
	}
	if err := s.logOpsEvent(ctx, tx, venueID, "maintenance", maintenanceID, "maintenance.released", staffID,
		map[string]any{"court_id": courtID}); err != nil {
		return err
	}
	// 通知该场地未开始订单（仅事件留痕）
	rows, _ := tx.Query(ctx,
		`SELECT id FROM bookings WHERE court_id=$1 AND status IN ('paid_waiting','warmup') AND start_time>now()`, courtID)
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		_ = s.Store.LogEvent(ctx, tx, id, "maintenance.released", "operator", fmt.Sprint(staffID),
			map[string]any{"maintenance_id": maintenanceID})
	}
	return tx.Commit(ctx)
}

// ---------- 临时加场 ----------

func (s *Service) CreateExtraSession(ctx context.Context, venueID, courtID, staffID int64, start, end time.Time, note string) (int64, error) {
	if !end.After(start) {
		return 0, errors.New("加场结束时间必须晚于开始时间")
	}
	// 与维护冲突检查
	var n int
	if err := s.Store.Pool.QueryRow(ctx, `
		SELECT count(*) FROM maintenance_windows WHERE court_id=$1 AND released_at IS NULL
		  AND tstzrange(start_time,end_time,'[)') && tstzrange($2,$3,'[)')`, courtID, start, end).Scan(&n); err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, ErrMaintenance
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO extra_sessions(venue_id,court_id,start_time,end_time,note,created_by)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		venueID, courtID, start, end, note, staffID).Scan(&id); err != nil {
		return 0, err
	}
	if err := s.logOpsEvent(ctx, tx, venueID, "extra_session", id, "extra_session.created", staffID,
		map[string]any{"court_id": courtID, "start": start, "end": end, "note": note}); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Service) CancelExtraSession(ctx context.Context, id, staffID int64) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var venueID int64
	if err := tx.QueryRow(ctx,
		`UPDATE extra_sessions SET cancelled_at=now() WHERE id=$1 AND cancelled_at IS NULL RETURNING venue_id`,
		id).Scan(&venueID); err != nil {
		return err
	}
	if err := s.logOpsEvent(ctx, tx, venueID, "extra_session", id, "extra_session.cancelled", staffID, map[string]any{}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) recordOpsEvent(ctx context.Context, venueID int64, refType string, refID int64, eventType string, staffID int64, payload map[string]any) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.logOpsEvent(ctx, tx, venueID, refType, refID, eventType, staffID, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------- 场馆策略 ----------

var editablePolicyFields = map[string]string{
	"payment_timeout_min":       "payment_timeout_min",
	"warmup_minutes":            "warmup_minutes",
	"early_entry_minutes":       "early_entry_minutes",
	"grace_minutes":             "grace_minutes",
	"late_cutoff_minutes":       "late_cutoff_minutes",
	"late_fee_ratio":            "late_fee_ratio",
	"peak_start":                "peak_start",
	"peak_end":                  "peak_end",
	"peak_cancel_free_hours":    "peak_cancel_free_hours",
	"peak_cancel_fee_ratio":     "peak_cancel_fee_ratio",
	"offpeak_cancel_free_hours": "offpeak_cancel_free_hours",
	"offpeak_cancel_fee_ratio":  "offpeak_cancel_fee_ratio",
	"no_show_ban_threshold":     "no_show_ban_threshold",
	"max_players":               "max_players",
	"per_extra_player_cents":    "per_extra_player_cents",
}

// UpdatePolicy 白名单更新运营策略；支持会员优先预约天数与等级折扣（JSON）。
func (s *Service) UpdatePolicy(ctx context.Context, venueID, staffID int64, patch map[string]any) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	i := 2
	var sets []string
	var args []any
	args = append(args, venueID)
	for k, v := range patch {
		if col, ok := editablePolicyFields[k]; ok {
			sets = append(sets, fmt.Sprintf("%s=$%d", col, i))
			args = append(args, v)
			i++
		}
	}
	if ahead, ok := patch["tier_book_ahead_days"]; ok {
		b, _ := jsonMarshal(ahead)
		sets = append(sets, fmt.Sprintf("tier_book_ahead_days=$%d", i))
		args = append(args, b)
		i++
	}
	if disc, ok := patch["tier_discount_pct"]; ok {
		b, _ := jsonMarshal(disc)
		sets = append(sets, fmt.Sprintf("tier_discount_pct=$%d", i))
		args = append(args, b)
		i++
	}
	if len(sets) == 0 {
		return errors.New("没有可更新的策略字段")
	}
	sets = append(sets, "updated_by=$"+fmt.Sprint(i), "updated_at=now()")
	args = append(args, staffID)
	q := fmt.Sprintf("UPDATE venue_policies SET %s WHERE venue_id=$1", joinSets(sets))
	ct, err := tx.Exec(ctx, q, args...)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	if err := s.logOpsEvent(ctx, tx, venueID, "policy", venueID, "policy.updated", staffID, patch); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.InvalidatePolicyCache(ctx, venueID)
	return nil
}

// UpsertPriceRule 更新某 scope 价格
func (s *Service) UpsertPriceRule(ctx context.Context, venueID, staffID int64, scope string, hourly, racket, shuttle int) error {
	_, err := s.Store.Pool.Exec(ctx, `
		INSERT INTO price_rules(venue_id,scope,hourly_rate_cents,racket_fee_cents,shuttle_fee_cents,updated_by)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (venue_id,scope) DO UPDATE SET
		  hourly_rate_cents=EXCLUDED.hourly_rate_cents,
		  racket_fee_cents=EXCLUDED.racket_fee_cents,
		  shuttle_fee_cents=EXCLUDED.shuttle_fee_cents,
		  updated_by=EXCLUDED.updated_by, updated_at=now()`,
		venueID, scope, hourly, racket, shuttle, staffID)
	if err == nil {
		s.InvalidatePolicyCache(ctx, venueID)
	}
	return err
}

// SetCourtStatus 场地状态（active/maintenance/disabled）
func (s *Service) SetCourtStatus(ctx context.Context, courtID int64, status string, staffID int64) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var venueID int64
	if err := tx.QueryRow(ctx, `UPDATE courts SET status=$2 WHERE id=$1 RETURNING venue_id`, courtID, status).Scan(&venueID); err != nil {
		return err
	}
	if err := s.logOpsEvent(ctx, tx, venueID, "court", courtID, "court.status_changed", staffID,
		map[string]any{"status": status}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
