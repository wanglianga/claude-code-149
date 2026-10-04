package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed 0001_init.sql
var migrationSQL string

// ErrConflict 排他约束冲突（场地已被锁定）
var ErrConflict = errors.New("slot conflict: court already locked for overlapping time")

// ErrNotFound 未找到
var ErrNotFound = errors.New("not found")

type Store struct {
	Pool *pgxpool.Pool
}

func New(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, err
	}
	return &Store{Pool: pool}, nil
}

// Migrate 执行内置迁移（幂等：通过 schema_migrations 跟踪）
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	if err != nil {
		return err
	}
	var exists bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version='0001_init')`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, migrationSQL); err != nil {
		return fmt.Errorf("apply migration: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES ('0001_init')`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------- 事件与台账 ----------

func (s *Store) LogEvent(ctx context.Context, tx pgx.Tx, bookingID int64, eventType, actorType, actorID string, payload any) error {
	raw := []byte("{}")
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		raw = b
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO booking_events(booking_id,event_type,actor_type,actor_id,payload) VALUES ($1,$2,$3,$4,$5)`,
		bookingID, eventType, actorType, actorID, raw)
	return err
}

type LedgerAdd struct {
	Kind        string
	AmountCents int
	Minutes     int
	Source      string
	Reason      string
	RefType     string
	RefID       int64
	CreatedBy   string
}

func (s *Store) AddLedger(ctx context.Context, tx pgx.Tx, bookingID int64, l LedgerAdd) error {
	if l.Source == "" {
		l.Source = "system"
	}
	if l.CreatedBy == "" {
		l.CreatedBy = "system"
	}
	var refID *int64
	if l.RefID != 0 {
		v := l.RefID
		refID = &v
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO billing_ledger(booking_id,kind,amount_cents,minutes,source,reason,ref_type,ref_id,created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		bookingID, l.Kind, l.AmountCents, l.Minutes, l.Source, l.Reason, l.RefType, refID, l.CreatedBy)
	return err
}

// AcquireSignal 尝试登记一个硬件信号；已存在返回 false（重复回调应忽略）。
func (s *Store) AcquireSignal(ctx context.Context, tx pgx.Tx, key string) (bool, error) {
	ct, err := tx.Exec(ctx, `INSERT INTO signal_dedup(signal_key) VALUES ($1) ON CONFLICT (signal_key) DO NOTHING`, key)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// ---------- 查询辅助 ----------

const bookingCols = `id,order_no,venue_id,court_id,member_id,member_tier_snapshot,start_time,end_time,
	player_count,rent_racket,buy_shuttle,status,arrival_status,pay_status,pay_deadline,paid_at,entry_code,
	slot_cents,discount_cents,credit_cents,credit_minutes_used,addons_cents,surcharge_cents,paid_cents,refund_cents,
	gate_opened_at,front_signed_at,checked_in_at,checkin_via,light_warmup_at,light_on_at,light_off_at,
	overtime_minutes,comp_minutes_granted,actual_start_time,actual_end_time,over_players,
	cancel_time,cancel_by,cancel_reason,no_show_classified_at,no_show_counted,created_at`

// BookingCols 订单列清单（供服务层自定义查询复用）
const BookingCols = bookingCols

// ScanBookingRow 从任意单行结果扫描订单
func ScanBookingRow(row pgx.Row) (*Booking, error) { return scanBooking(row) }

func scanBooking(row pgx.Row) (*Booking, error) {
	var b Booking
	err := row.Scan(
		&b.ID, &b.OrderNo, &b.VenueID, &b.CourtID, &b.MemberID, &b.MemberTierSnapshot, &b.StartTime, &b.EndTime,
		&b.PlayerCount, &b.RentRacket, &b.BuyShuttle, &b.Status, &b.ArrivalStatus, &b.PayStatus, &b.PayDeadline, &b.PaidAt, &b.EntryCode,
		&b.SlotCents, &b.DiscountCents, &b.CreditCents, &b.CreditMinutesUsed, &b.AddonsCents, &b.SurchargeCents, &b.PaidCents, &b.RefundCents,
		&b.GateOpenedAt, &b.FrontSignedAt, &b.CheckedInAt, &b.CheckinVia, &b.LightWarmupAt, &b.LightOnAt, &b.LightOffAt,
		&b.OvertimeMinutes, &b.CompMinutesGranted, &b.ActualStartTime, &b.ActualEndTime, &b.OverPlayers,
		&b.CancelTime, &b.CancelBy, &b.CancelReason, &b.NoShowClassifiedAt, &b.NoShowCounted, &b.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &b, err
}

func (s *Store) GetBooking(ctx context.Context, id int64) (*Booking, error) {
	return scanBooking(s.Pool.QueryRow(ctx, `SELECT `+bookingCols+` FROM bookings WHERE id=$1`, id))
}

func (s *Store) GetBookingByOrderNo(ctx context.Context, orderNo string) (*Booking, error) {
	return scanBooking(s.Pool.QueryRow(ctx, `SELECT `+bookingCols+` FROM bookings WHERE order_no=$1`, orderNo))
}

func (s *Store) GetBookingByEntryCode(ctx context.Context, code string) (*Booking, error) {
	return scanBooking(s.Pool.QueryRow(ctx, `SELECT `+bookingCols+` FROM bookings WHERE entry_code=$1`, code))
}

func (s *Store) ListEvents(ctx context.Context, bookingID int64) ([]Event, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id,booking_id,event_type,actor_type,actor_id,payload,created_at FROM booking_events WHERE booking_id=$1 ORDER BY id`, bookingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.BookingID, &e.EventType, &e.ActorType, &e.ActorID, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) ListLedger(ctx context.Context, bookingID int64) ([]LedgerEntry, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id,booking_id,kind,amount_cents,minutes,source,reason,ref_type,ref_id,created_by,created_at
		 FROM billing_ledger WHERE booking_id=$1 ORDER BY id`, bookingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LedgerEntry
	for rows.Next() {
		var l LedgerEntry
		if err := rows.Scan(&l.ID, &l.BookingID, &l.Kind, &l.AmountCents, &l.Minutes, &l.Source, &l.Reason,
			&l.RefType, &l.RefID, &l.CreatedBy, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// IsConflictError 判断是否唯一/排他约束冲突
func IsConflictError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23")
}

func (s *Store) ListPayments(ctx context.Context, bookingID int64) ([]Payment, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id,booking_id,direction,amount_cents,method,status,reason,created_by,created_at
		 FROM payments WHERE booking_id=$1 ORDER BY id`, bookingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Payment
	for rows.Next() {
		var p Payment
		if err := rows.Scan(&p.ID, &p.BookingID, &p.Direction, &p.AmountCents, &p.Method, &p.Status, &p.Reason, &p.CreatedBy, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func nowUTC() time.Time { return time.Now().UTC().Truncate(time.Second) }

func (s *Store) touchBooking(ctx context.Context, tx pgx.Tx, id int64) error {
	_, err := tx.Exec(ctx, `UPDATE bookings SET updated_at=now() WHERE id=$1`, id)
	return err
}
