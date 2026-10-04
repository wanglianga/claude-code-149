package service

import (
	"context"
	"errors"
	"fmt"

	"courtbook/internal/domain"
	"courtbook/internal/store"
)

// ---------- 会员退款申请 ----------

func (s *Service) CreateRefundRequest(ctx context.Context, memberID, bookingID int64, amount int, reason string) (int64, error) {
	b, err := s.Store.GetBooking(ctx, bookingID)
	if err != nil {
		return 0, err
	}
	if b.MemberID != memberID {
		return 0, errors.New("只能为自己的订单申请退款")
	}
	if amount <= 0 {
		amount = b.PaidCents - b.RefundCents
	}
	if amount > b.PaidCents-b.RefundCents {
		return 0, errors.New("申请金额超过可退余额")
	}
	var id int64
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx, `
		INSERT INTO refund_requests(booking_id,applicant_id,amount_cents,reason)
		VALUES ($1,$2,$3,$4) RETURNING id`, bookingID, memberID, amount, reason).Scan(&id); err != nil {
		return 0, err
	}
	if err := s.Store.LogEvent(ctx, tx, bookingID, "refund.requested", "member", fmt.Sprint(memberID),
		map[string]any{"request_id": id, "amount_cents": amount, "reason": reason}); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Service) listRefundRequests(ctx context.Context, bookingID int64) ([]store.RefundRequest, error) {
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id,booking_id,applicant_id,amount_cents,reason,status,decided_by,decision_note,created_at,decided_at
		FROM refund_requests WHERE booking_id=$1 ORDER BY id`, bookingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.RefundRequest
	for rows.Next() {
		var r store.RefundRequest
		if err := rows.Scan(&r.ID, &r.BookingID, &r.ApplicantID, &r.AmountCents, &r.Reason, &r.Status,
			&r.DecidedBy, &r.DecisionNote, &r.CreatedAt, &r.DecidedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DecideRefundRequest 客服审批退款申请
func (s *Service) DecideRefundRequest(ctx context.Context, requestID, staffID int64, approve bool, note string) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var req store.RefundRequest
	if err := tx.QueryRow(ctx, `
		SELECT id,booking_id,applicant_id,amount_cents,reason,status,decided_by,decision_note,created_at,decided_at
		FROM refund_requests WHERE id=$1 FOR UPDATE`, requestID).
		Scan(&req.ID, &req.BookingID, &req.ApplicantID, &req.AmountCents, &req.Reason, &req.Status,
			&req.DecidedBy, &req.DecisionNote, &req.CreatedAt, &req.DecidedAt); err != nil {
		return err
	}
	if req.Status != "pending" {
		return errors.New("该申请已处理")
	}
	status := "rejected"
	if approve {
		status = "approved"
		b, err := lockBooking(ctx, tx, req.BookingID)
		if err != nil {
			return err
		}
		if err := s.issueRefund(ctx, tx, b, req.AmountCents, "refund_dispute", "refund_dispute",
			"退款申请批准："+note, "cs", fmt.Sprint(staffID)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE refund_requests SET status=$2,decided_by=$3,decision_note=$4,decided_at=now() WHERE id=$1`,
		requestID, status, staffID, note); err != nil {
		return err
	}
	if err := s.Store.LogEvent(ctx, tx, req.BookingID, "refund.decided", "cs", fmt.Sprint(staffID),
		map[string]any{"request_id": requestID, "approved": approve, "note": note}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------- 争议处理 ----------

func (s *Service) OpenDispute(ctx context.Context, bookingID, staffID int64, reason string) (int64, error) {
	if _, err := s.Store.GetBooking(ctx, bookingID); err != nil {
		return 0, err
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO disputes(booking_id,opened_by,reason) VALUES ($1,$2,$3) RETURNING id`,
		bookingID, staffID, reason).Scan(&id); err != nil {
		return 0, err
	}
	if err := s.Store.LogEvent(ctx, tx, bookingID, "dispute.opened", "cs", fmt.Sprint(staffID),
		map[string]any{"dispute_id": id, "reason": reason}); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return id, nil
}

// ResolveDisputeInput 争议裁决
type ResolveDisputeInput struct {
	DisputeID    int64
	StaffID      int64
	Kind         string // refund / comp_time / exonerate / mixed / no_action
	RefundAmount int
	CompMinutes  int // 补偿到会员账户（未来预约可用）
	WaiveNoShow  bool
	Note         string
}

// ResolveDispute 客服裁决：可组合退费、补偿时长、撤销爽约记录。
func (s *Service) ResolveDispute(ctx context.Context, in ResolveDisputeInput) (*store.Dispute, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var d store.Dispute
	err = tx.QueryRow(ctx, `SELECT id,booking_id,opened_by,reason,status FROM disputes WHERE id=$1 FOR UPDATE`,
		in.DisputeID).Scan(&d.ID, &d.BookingID, &d.OpenedBy, &d.Reason, &d.Status)
	if err != nil {
		return nil, err
	}
	if d.Status != "open" {
		return nil, errors.New("争议已结案")
	}
	b, err := lockBooking(ctx, tx, d.BookingID)
	if err != nil {
		return nil, err
	}
	actor := fmt.Sprintf("cs:%d", in.StaffID)

	if in.RefundAmount > 0 {
		if err := s.issueRefund(ctx, tx, b, in.RefundAmount, "refund_dispute", "refund_dispute",
			"争议裁决退款："+in.Note, "cs", fmt.Sprint(in.StaffID)); err != nil {
			return nil, err
		}
	}
	if in.CompMinutes > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO member_credits(member_id,minutes,remaining_minutes,source,booking_id,note)
			VALUES ($1,$2,$2,$3,$4,$5)`,
			b.MemberID, in.CompMinutes, fmt.Sprintf("dispute:%d", d.ID), b.ID, "争议补偿时长："+in.Note); err != nil {
			return nil, err
		}
		if err := s.Store.AddLedger(ctx, tx, b.ID, store.LedgerAdd{
			Kind: domain.LedgerCompTime, AmountCents: 0, Minutes: in.CompMinutes,
			Source: fmt.Sprintf("dispute:%d", d.ID), Reason: "争议补偿时长（入会员账户）", CreatedBy: actor,
		}); err != nil {
			return nil, err
		}
	}
	if in.WaiveNoShow && b.NoShowCounted {
		if _, err := tx.Exec(ctx, `
			UPDATE bookings SET no_show_counted=false,arrival_status=CASE WHEN arrival_status='no_show' THEN 'unknown' ELSE arrival_status END,
			  updated_at=now() WHERE id=$1`, b.ID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx,
			`UPDATE members SET no_show_count=GREATEST(no_show_count-1,0) WHERE id=$1`, b.MemberID); err != nil {
			return nil, err
		}
		// 爽约罚没随平反退回
		var forfeit int
		_ = tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(amount_cents),0) FROM billing_ledger
			WHERE booking_id=$1 AND kind='no_show_forfeit'`, b.ID).Scan(&forfeit)
		if forfeit > 0 {
			if err := s.issueRefund(ctx, tx, b, forfeit, "refund_dispute", "refund_dispute",
				"撤销爽约，退还罚没："+in.Note, "cs", fmt.Sprint(in.StaffID)); err != nil {
				return nil, err
			}
		}
	}
	// 若改判为场馆原因，订单状态归位
	if in.Kind == "refund" || in.Kind == "mixed" || in.Kind == "exonerate" {
		if b.Status == domain.StatusNoShow && in.WaiveNoShow {
			if _, err := tx.Exec(ctx,
				`UPDATE bookings SET status=CASE WHEN pay_status='refunded' THEN 'venue_closed' ELSE 'finished' END,
				   updated_at=now() WHERE id=$1`, b.ID); err != nil {
				return nil, err
			}
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE disputes SET status='resolved',resolution_kind=$2,refund_amount_cents=$3,comp_minutes=$4,
		  waive_no_show=$5,decided_by=$6,decision_note=$7,resolved_at=now()
		WHERE id=$1`,
		d.ID, in.Kind, in.RefundAmount, in.CompMinutes, in.WaiveNoShow, in.StaffID, in.Note); err != nil {
		return nil, err
	}
	if err := s.Store.LogEvent(ctx, tx, b.ID, "dispute.resolved", "cs", fmt.Sprint(in.StaffID), map[string]any{
		"dispute_id": d.ID, "kind": in.Kind, "refund_cents": in.RefundAmount,
		"comp_minutes": in.CompMinutes, "waive_no_show": in.WaiveNoShow, "note": in.Note,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	var out store.Dispute
	_ = s.Store.Pool.QueryRow(ctx, `
		SELECT id,booking_id,opened_by,reason,status,resolution_kind,refund_amount_cents,comp_minutes,
		       waive_no_show,decided_by,decision_note,created_at,resolved_at
		FROM disputes WHERE id=$1`, d.ID).Scan(
		&out.ID, &out.BookingID, &out.OpenedBy, &out.Reason, &out.Status, &out.ResolutionKind,
		&out.RefundAmountCents, &out.CompMinutes, &out.WaiveNoShow, &out.DecidedBy, &out.DecisionNote,
		&out.CreatedAt, &out.ResolvedAt)
	return &out, nil
}

func (s *Service) listDisputes(ctx context.Context, bookingID int64) ([]store.Dispute, error) {
	rows, err := s.Store.Pool.Query(ctx, `
		SELECT id,booking_id,opened_by,reason,status,resolution_kind,refund_amount_cents,comp_minutes,
		       waive_no_show,decided_by,decision_note,created_at,resolved_at
		FROM disputes WHERE booking_id=$1 ORDER BY id`, bookingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Dispute
	for rows.Next() {
		var d store.Dispute
		if err := rows.Scan(&d.ID, &d.BookingID, &d.OpenedBy, &d.Reason, &d.Status, &d.ResolutionKind,
			&d.RefundAmountCents, &d.CompMinutes, &d.WaiveNoShow, &d.DecidedBy, &d.DecisionNote,
			&d.CreatedAt, &d.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
