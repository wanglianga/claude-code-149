package store

import "time"

// Venue 场馆
type Venue struct {
	ID                         int64   `json:"id"`
	Name                       string  `json:"name"`
	Address                    string  `json:"address"`
	OpensAt                    string  `json:"opens_at"`
	ClosesAt                   string  `json:"closes_at"`
	PriceCentsPerHour          int64   `json:"price_cents_per_hour"`
	PeakMultiplier             float64 `json:"peak_multiplier"`
	OvertimeMultiplier         float64 `json:"overtime_multiplier"`
	RacketFeeCents             int64   `json:"racket_fee_cents"`
	ShuttleFeeCents            int64   `json:"shuttle_fee_cents"`
	DefaultCourtCapacity       int     `json:"default_court_capacity"`
	OverlimitFeeCents          int64   `json:"overlimit_fee_cents"`
	PaymentTimeoutSeconds      int     `json:"payment_timeout_seconds"`
	LightWarmupMinutes         int     `json:"light_warmup_minutes"`
	CheckinWindowMinutes       int     `json:"checkin_window_minutes"`
	LateGraceMinutes           int     `json:"late_grace_minutes"`
	NoShowJudgeMinutes         int     `json:"no_show_judge_minutes"`
	IdleLightOffMinutes        int     `json:"idle_light_off_minutes"`
	MaxAutoOvertimeMinutes     int     `json:"max_auto_overtime_minutes"`
	MemberOnlyPeakReleaseHours int     `json:"member_only_peak_release_hours"`
	NoShowPenaltyCount         int     `json:"noshow_penalty_count"`
}

type Court struct {
	ID       int64  `json:"id"`
	VenueID  int64  `json:"venue_id"`
	CourtNo  string `json:"court_no"`
	Capacity int    `json:"capacity"`
	Status   string `json:"status"`
}

type Member struct {
	ID           int64  `json:"id"`
	Phone        string `json:"phone"`
	Name         string `json:"name"`
	PasswordHash string `json:"-"`
	MemberType   string `json:"member_type"`
	NoShowCount  int    `json:"no_show_count"`
	Status       string `json:"status"`
}

type MemberPolicy struct {
	VenueID           int64   `json:"venue_id"`
	MemberType        string  `json:"member_type"`
	DiscountPercent   int     `json:"discount_percent"`
	FreeHoursPerMonth float64 `json:"free_hours_per_month"`
	MaxAdvanceHours   int     `json:"max_advance_hours"`
}

type PeakWindow struct {
	ID        int64  `json:"id"`
	VenueID   int64  `json:"venue_id"`
	Weekday   int    `json:"weekday"`
	StartTime string `json:"start_time"`
	EndTime   string `json:"end_time"`
}

type CancelRule struct {
	ID             int64   `json:"id"`
	VenueID        int64   `json:"venue_id"`
	IsPeak         bool    `json:"is_peak"`
	MinHoursBefore float64 `json:"min_hours_before"`
	RefundPercent  int     `json:"refund_percent"`
}

type ExtraSlot struct {
	ID       int64  `json:"id"`
	VenueID  int64  `json:"venue_id"`
	CourtID  *int64 `json:"court_id"`
	SlotDate string `json:"slot_date"`
	OpensAt  string `json:"opens_at"`
	ClosesAt string `json:"closes_at"`
	Note     string `json:"note"`
}

type Maintenance struct {
	ID         int64      `json:"id"`
	VenueID    int64      `json:"venue_id"`
	CourtID    int64      `json:"court_id"`
	StartTime  time.Time  `json:"start_time"`
	EndTime    time.Time  `json:"end_time"`
	Reason     string     `json:"reason"`
	Status     string     `json:"status"`
	CreatedBy  *int64     `json:"created_by"`
	ReleasedBy *int64     `json:"released_by"`
	ReleasedAt *time.Time `json:"released_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Booking struct {
	ID                    int64      `json:"id"`
	BookingNo             string     `json:"booking_no"`
	VenueID               int64      `json:"venue_id"`
	CourtID               int64      `json:"court_id"`
	MemberID              int64      `json:"member_id"`
	MemberTypeSnap        string     `json:"member_type_snap"`
	MemberNameSnap        string     `json:"member_name_snap"`
	PartySize             int        `json:"party_size"`
	CapacitySnapshot      int        `json:"capacity_snapshot"`
	StartTime             time.Time  `json:"start_time"`
	EndTime               time.Time  `json:"end_time"`
	RentRacket            bool       `json:"rent_racket"`
	RacketCount           int        `json:"racket_count"`
	BuyShuttle            bool       `json:"buy_shuttle"`
	ShuttleCount          int        `json:"shuttle_count"`
	CourtFeeCents         int64      `json:"court_fee_cents"`
	AddonCents            int64      `json:"addon_cents"`
	DiscountCents         int64      `json:"discount_cents"`
	BenefitCents          int64      `json:"benefit_cents"`
	BenefitMinutes        int        `json:"benefit_minutes"`
	BenefitMonthlyMinutes int        `json:"benefit_monthly_minutes"`
	BenefitGrantMinutes   int        `json:"benefit_grant_minutes"`
	OverlimitCents        int64      `json:"overlimit_cents"`
	PayableCents          int64      `json:"payable_cents"`
	PaidCents             int64      `json:"paid_cents"`
	RefundedCents         int64      `json:"refunded_cents"`
	OvertimeCents         int64      `json:"overtime_cents"`
	LateAdjustCents       int64      `json:"late_adjust_cents"`
	Status                string     `json:"status"`
	EntryCode             string     `json:"entry_code"`
	ExpiresAt             *time.Time `json:"expires_at"`
	PaidAt                *time.Time `json:"paid_at"`
	FirstGateAt           *time.Time `json:"first_gate_at"`
	GateWaitingAt         *time.Time `json:"gate_waiting_at"`
	FrontCheckinAt        *time.Time `json:"front_checkin_at"`
	FirstPresenceAt       *time.Time `json:"first_presence_at"`
	WarmupAt              *time.Time `json:"warmup_at"`
	LightOnAt             *time.Time `json:"light_on_at"`
	LightOffAt            *time.Time `json:"light_off_at"`
	EffectiveStart        *time.Time `json:"effective_start"`
	ActualEnd             *time.Time `json:"actual_end"`
	OvertimeMinutes       int        `json:"overtime_minutes"`
	LateStartMinutes      int        `json:"late_start_minutes"`
	CompBonusMinutes      int        `json:"comp_bonus_minutes"`
	OverlimitPersons      int        `json:"overlimit_persons"`
	Verdict               *string    `json:"verdict"`
	JudgedAt              *time.Time `json:"judged_at"`
	JudgeSource           *string    `json:"judge_source"`
	Note                  string     `json:"note"`
	CreatedSource         string     `json:"created_source"`
	Version               int64      `json:"version"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

type Participant struct {
	ID          int64      `json:"id"`
	BookingID   int64      `json:"booking_id"`
	MemberID    *int64     `json:"member_id"`
	Name        string     `json:"name"`
	Phone       string     `json:"phone"`
	CheckedIn   bool       `json:"checked_in"`
	CheckedInAt *time.Time `json:"checked_in_at"`
}

type Payment struct {
	ID            int64     `json:"id"`
	BookingID     int64     `json:"booking_id"`
	Kind          string    `json:"kind"`
	AmountCents   int64     `json:"amount_cents"`
	Method        string    `json:"method"`
	Status        string    `json:"status"`
	TransactionNo string    `json:"transaction_no"`
	Reason        string    `json:"reason"`
	CreatedSource string    `json:"created_source"`
	CreatedBy     *int64    `json:"created_by"`
	CreatedAt     time.Time `json:"created_at"`
}

type RefundRequest struct {
	ID          int64      `json:"id"`
	BookingID   int64      `json:"booking_id"`
	MemberID    int64      `json:"member_id"`
	AmountCents int64      `json:"amount_cents"`
	Reason      string     `json:"reason"`
	Status      string     `json:"status"`
	ReviewedBy  *int64     `json:"reviewed_by"`
	ReviewedAt  *time.Time `json:"reviewed_at"`
	ReviewNote  string     `json:"review_note"`
	CreatedAt   time.Time  `json:"created_at"`
}

type GateRecord struct {
	ID        int64     `json:"id"`
	BookingID *int64    `json:"booking_id"`
	EntryCode string    `json:"entry_code"`
	DeviceID  string    `json:"device_id"`
	Result    string    `json:"result"`
	Reason    string    `json:"reason"`
	ScannedAt time.Time `json:"scanned_at"`
}

type LightRecord struct {
	ID            int64     `json:"id"`
	BookingID     int64     `json:"booking_id"`
	CourtID       int64     `json:"court_id"`
	Action        string    `json:"action"`
	CommandSource string    `json:"command_source"`
	Success       bool      `json:"success"`
	Detail        string    `json:"detail"`
	OccurredAt    time.Time `json:"occurred_at"`
}

type FrontRecord struct {
	ID        int64     `json:"id"`
	BookingID int64     `json:"booking_id"`
	StaffID   *int64    `json:"staff_id"`
	Action    string    `json:"action"`
	Detail    []byte    `json:"detail"`
	CreatedAt time.Time `json:"created_at"`
}

type Event struct {
	ID        int64     `json:"id"`
	BookingID *int64    `json:"booking_id"`
	VenueID   *int64    `json:"venue_id"`
	CourtID   *int64    `json:"court_id"`
	EventType string    `json:"event_type"`
	Source    string    `json:"source"`
	ActorID   *int64    `json:"actor_id"`
	Payload   []byte    `json:"payload"`
	CreatedAt time.Time `json:"created_at"`
}

type Dispute struct {
	ID             int64      `json:"id"`
	BookingID      int64      `json:"booking_id"`
	OpenedBy       *int64     `json:"opened_by"`
	AutoOpened     bool       `json:"auto_opened"`
	Status         string     `json:"status"`
	Issue          string     `json:"issue"`
	ResolutionType *string    `json:"resolution_type"`
	RefundCents    int64      `json:"refund_cents"`
	CompMinutes    int        `json:"comp_minutes"`
	ResolutionNote string     `json:"resolution_note"`
	ResolvedBy     *int64     `json:"resolved_by"`
	ResolvedAt     *time.Time `json:"resolved_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

type Staff struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"-"`
	Name         string `json:"name"`
	Role         string `json:"role"`
	VenueID      *int64 `json:"venue_id"`
}

type BenefitGrant struct {
	ID            int64      `json:"id"`
	MemberID      int64      `json:"member_id"`
	VenueID       int64      `json:"venue_id"`
	Minutes       int        `json:"minutes"`
	Reason        string     `json:"reason"`
	GrantedSource string     `json:"granted_source"`
	GrantedBy     *int64     `json:"granted_by"`
	UsedBookingID *int64     `json:"used_booking_id"`
	UsedAt        *time.Time `json:"used_at"`
	CreatedAt     time.Time  `json:"created_at"`
}
