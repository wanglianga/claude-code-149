package store

import "time"

type Venue struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Address   string    `json:"address"`
	OpenTime  string    `json:"open_time"`
	CloseTime string    `json:"close_time"`
	CreatedAt time.Time `json:"created_at"`
}

type Court struct {
	ID      int64  `json:"id"`
	VenueID int64  `json:"venue_id"`
	CourtNo string `json:"court_no"`
	Status  string `json:"status"`
}

type Member struct {
	ID          int64  `json:"id"`
	Phone       string `json:"phone"`
	Name        string `json:"name"`
	MemberTier  string `json:"member_tier"`
	NoShowCount int    `json:"no_show_count"`
	Banned      bool   `json:"banned"`
}

type Staff struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	VenueID  *int64 `json:"venue_id"`
}

type Booking struct {
	ID                 int64      `json:"id"`
	OrderNo            string     `json:"order_no"`
	VenueID            int64      `json:"venue_id"`
	CourtID            int64      `json:"court_id"`
	MemberID           int64      `json:"member_id"`
	MemberTierSnapshot string     `json:"member_tier_snapshot"`
	StartTime          time.Time  `json:"start_time"`
	EndTime            time.Time  `json:"end_time"`
	PlayerCount        int        `json:"player_count"`
	RentRacket         bool       `json:"rent_racket"`
	BuyShuttle         bool       `json:"buy_shuttle"`
	Status             string     `json:"status"`
	ArrivalStatus      string     `json:"arrival_status"`
	PayStatus          string     `json:"pay_status"`
	PayDeadline        *time.Time `json:"pay_deadline"`
	PaidAt             *time.Time `json:"paid_at"`
	EntryCode          *string    `json:"entry_code"`
	SlotCents          int        `json:"slot_cents"`
	DiscountCents      int        `json:"discount_cents"`
	CreditCents        int        `json:"credit_cents"`
	CreditMinutesUsed  int        `json:"credit_minutes_used"`
	AddonsCents        int        `json:"addons_cents"`
	SurchargeCents     int        `json:"surcharge_cents"`
	PaidCents          int        `json:"paid_cents"`
	RefundCents        int        `json:"refund_cents"`
	GateOpenedAt       *time.Time `json:"gate_opened_at"`
	FrontSignedAt      *time.Time `json:"front_signed_at"`
	CheckedInAt        *time.Time `json:"checked_in_at"`
	CheckinVia         *string    `json:"checkin_via"`
	LightWarmupAt      *time.Time `json:"light_warmup_at"`
	LightOnAt          *time.Time `json:"light_on_at"`
	LightOffAt         *time.Time `json:"light_off_at"`
	OvertimeMinutes    int        `json:"overtime_minutes"`
	CompMinutesGranted int        `json:"comp_minutes_granted"`
	ActualStartTime    *time.Time `json:"actual_start_time"`
	ActualEndTime      *time.Time `json:"actual_end_time"`
	OverPlayers        int        `json:"over_players"`
	CancelTime         *time.Time `json:"cancel_time"`
	CancelBy           *string    `json:"cancel_by"`
	CancelReason       *string    `json:"cancel_reason"`
	NoShowClassifiedAt *time.Time `json:"no_show_classified_at"`
	NoShowCounted      bool       `json:"no_show_counted"`
	CreatedAt          time.Time  `json:"created_at"`
}

type LedgerEntry struct {
	ID          int64     `json:"id"`
	BookingID   int64     `json:"booking_id"`
	Kind        string    `json:"kind"`
	AmountCents int       `json:"amount_cents"`
	Minutes     int       `json:"minutes"`
	Source      string    `json:"source"`
	Reason      string    `json:"reason"`
	RefType     string    `json:"ref_type"`
	RefID       *int64    `json:"ref_id"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

type Event struct {
	ID        int64     `json:"id"`
	BookingID int64     `json:"booking_id"`
	EventType string    `json:"event_type"`
	ActorType string    `json:"actor_type"`
	ActorID   string    `json:"actor_id"`
	Payload   []byte    `json:"payload"`
	CreatedAt time.Time `json:"created_at"`
}

type Payment struct {
	ID          int64     `json:"id"`
	BookingID   int64     `json:"booking_id"`
	Direction   string    `json:"direction"`
	AmountCents int       `json:"amount_cents"`
	Method      string    `json:"method"`
	Status      string    `json:"status"`
	Reason      string    `json:"reason"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

type Maintenance struct {
	ID         int64      `json:"id"`
	VenueID    int64      `json:"venue_id"`
	CourtID    int64      `json:"court_id"`
	StartTime  time.Time  `json:"start_time"`
	EndTime    time.Time  `json:"end_time"`
	Reason     string     `json:"reason"`
	CreatedBy  *int64     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ReleasedAt *time.Time `json:"released_at"`
}

type ExtraSession struct {
	ID          int64      `json:"id"`
	VenueID     int64      `json:"venue_id"`
	CourtID     int64      `json:"court_id"`
	StartTime   time.Time  `json:"start_time"`
	EndTime     time.Time  `json:"end_time"`
	Note        string     `json:"note"`
	CreatedBy   *int64     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	CancelledAt *time.Time `json:"cancelled_at"`
}

type RefundRequest struct {
	ID           int64      `json:"id"`
	BookingID    int64      `json:"booking_id"`
	ApplicantID  int64      `json:"applicant_id"`
	AmountCents  int        `json:"amount_cents"`
	Reason       string     `json:"reason"`
	Status       string     `json:"status"`
	DecidedBy    *int64     `json:"decided_by"`
	DecisionNote string     `json:"decision_note"`
	CreatedAt    time.Time  `json:"created_at"`
	DecidedAt    *time.Time `json:"decided_at"`
}

type Dispute struct {
	ID                int64      `json:"id"`
	BookingID         int64      `json:"booking_id"`
	OpenedBy          int64      `json:"opened_by"`
	Reason            string     `json:"reason"`
	Status            string     `json:"status"`
	ResolutionKind    *string    `json:"resolution_kind"`
	RefundAmountCents int        `json:"refund_amount_cents"`
	CompMinutes       int        `json:"comp_minutes"`
	WaiveNoShow       bool       `json:"waive_no_show"`
	DecidedBy         *int64     `json:"decided_by"`
	DecisionNote      string     `json:"decision_note"`
	CreatedAt         time.Time  `json:"created_at"`
	ResolvedAt        *time.Time `json:"resolved_at"`
}

type MemberCredit struct {
	ID               int64  `json:"id"`
	MemberID         int64  `json:"member_id"`
	Minutes          int    `json:"minutes"`
	RemainingMinutes int    `json:"remaining_minutes"`
	Source           string `json:"source"`
	BookingID        *int64 `json:"booking_id"`
	Note             string `json:"note"`
}

// Slot 是占用视图中的一条（预约/维护/加场）
type Slot struct {
	Kind      string    `json:"kind"` // booking / maintenance / extra_session
	RefID     int64     `json:"ref_id"`
	CourtID   int64     `json:"court_id"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`
	Status    string    `json:"status,omitempty"`
	OrderNo   string    `json:"order_no,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}
