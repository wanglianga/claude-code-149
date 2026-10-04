// Package domain 定义领域模型与纯计费规则（不依赖数据库，便于单测）。
package domain

import (
	"math"
	"time"
)

// 订单状态
const (
	StatusPendingPayment = "pending_payment"
	StatusPaidWaiting    = "paid_waiting"
	StatusWarmup         = "warmup"
	StatusInPlay         = "in_play"
	StatusFinished       = "finished"
	StatusCancelled      = "cancelled"
	StatusExpired        = "expired"
	StatusNoShow         = "no_show"
	StatusVenueClosed    = "venue_closed"
)

// 到场判定
const (
	ArrivalUnknown    = "unknown"
	ArrivalEarly      = "early"
	ArrivalOnTime     = "ontime"
	ArrivalLate       = "late"
	ArrivalWaiting    = "waiting" // 已到场但前一场延时，在场边等待
	ArrivalNoShow     = "no_show"
	ArrivalVenueFault = "venue_fault"
)

// 台账条目类型
const (
	LedgerSlotFee        = "slot_fee"
	LedgerRacketFee      = "racket_fee"
	LedgerShuttleFee     = "shuttle_fee"
	LedgerMemberDiscount = "member_discount"
	LedgerOvertimeFee    = "overtime_fee"
	LedgerOverPlayerFee  = "over_player_fee"
	LedgerLateFee        = "late_fee"
	LedgerCancelFee      = "cancel_fee"
	LedgerNoShowForfeit  = "no_show_forfeit"
	LedgerRefundCancel   = "refund_cancel"
	LedgerRefundVenue    = "refund_venue"
	LedgerRefundDispute  = "refund_dispute"
	LedgerCompTime       = "comp_time"
)

// Policy 场馆运营规则（对应 venue_policies 行）
type Policy struct {
	VenueID                int64
	PaymentTimeoutMin      int
	WarmupMinutes          int
	EarlyEntryMinutes      int
	GraceMinutes           int
	LateCutoffMinutes      int
	LateFeeRatio           float64
	PeakStartMin           int // 一天中的分钟数
	PeakEndMin             int
	PeakCancelFreeHours    float64
	PeakCancelFeeRatio     float64
	OffpeakCancelFreeHours float64
	OffpeakCancelFeeRatio  float64
	MaxPlayers             int
	PerExtraPlayerCents    int
	NoShowBanThreshold     int
	TierBookAheadDays      map[string]int // normal/silver/gold
	TierDiscountPct        map[string]int // normal/silver/gold
}

type PriceRule struct {
	Scope           string
	HourlyRateCents int
	RacketFeeCents  int
	ShuttleFeeCents int
}

// QuoteInput 报价输入
type QuoteInput struct {
	Start, End         time.Time
	Tier               string
	PlayerCount        int
	RentRacket         bool
	BuyShuttle         bool
	CreditMinutesToUse int // 会员选择使用的补偿时长
}

// QuoteLine 报价中的一条费用
type QuoteLine struct {
	Kind        string `json:"kind"`
	AmountCents int    `json:"amount_cents"`
	Minutes     int    `json:"minutes,omitempty"`
	Reason      string `json:"reason"`
}

// QuoteResult 报价结果
type QuoteResult struct {
	Lines         []QuoteLine `json:"lines"`
	SlotCents     int         `json:"slot_cents"`
	DiscountCents int         `json:"discount_cents"`
	CreditCents   int         `json:"credit_cents"`
	CreditMinutes int         `json:"credit_minutes"`
	AddonsCents   int         `json:"addons_cents"`
	PayableCents  int         `json:"payable_cents"`
}

// IsPeak 判断某时刻是否落在高峰时段（只看时分与星期几由调用方结合价格 scope 处理）
func IsPeakAt(p Policy, t time.Time) bool {
	m := t.Hour()*60 + t.Minute()
	if p.PeakStartMin <= p.PeakEndMin {
		return m >= p.PeakStartMin && m < p.PeakEndMin
	}
	return m >= p.PeakStartMin || m < p.PeakEndMin // 跨午夜
}

// scopeAt 周末全天按 weekend；工作日区分高峰
func ScopeAt(p Policy, t time.Time) string {
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		return "weekend"
	}
	if IsPeakAt(p, t) {
		return "weekday_peak"
	}
	return "weekday_offpeak"
}

// roundUp15 按 15 分钟向上取整（延时计费单位）
func roundUp15(d time.Duration) int {
	m := int(math.Ceil(d.Minutes()))
	if m <= 0 {
		return 0
	}
	return ((m + 14) / 15) * 15
}

// Quote 纯报价逻辑：按时段分片计价（跨高峰时按落在各 scope 的分钟数加权）。
func Quote(p Policy, rules map[string]PriceRule, in QuoteInput) QuoteResult {
	res := QuoteResult{}
	totalMin := int(in.End.Sub(in.Start).Minutes())

	// 按时段 scope 分片累计分钟
	step := time.Minute
	scopeMin := map[string]int{}
	for t := in.Start; t.Before(in.End); t = t.Add(step) {
		scopeMin[ScopeAt(p, t)]++
	}
	slot := 0
	for scope, mins := range scopeMin {
		r, ok := rules[scope]
		if !ok {
			continue
		}
		slot += int(math.Round(float64(r.HourlyRateCents) * float64(mins) / 60.0))
	}
	res.SlotCents = slot
	res.Lines = append(res.Lines, QuoteLine{LedgerSlotFee, slot, totalMin, "场地费按时段计价"})

	// 会员等级折扣
	pct := p.TierDiscountPct[in.Tier]
	discount := 0
	if pct > 0 {
		discount = int(math.Round(float64(slot) * float64(pct) / 100.0))
	}
	res.DiscountCents = discount
	if discount > 0 {
		res.Lines = append(res.Lines, QuoteLine{LedgerMemberDiscount, -discount, 0, "会员折扣"})
	}

	// 补偿时长抵扣（按场地费每分钟均价折算，作用于折后场地费）
	creditCents := 0
	if in.CreditMinutesToUse > 0 && totalMin > 0 {
		usable := in.CreditMinutesToUse
		if usable > totalMin {
			usable = totalMin
		}
		avgPerMin := float64(slot-discount) / float64(totalMin)
		creditCents = int(math.Round(avgPerMin * float64(usable)))
		if creditCents > slot-discount {
			creditCents = slot - discount
		}
		res.CreditMinutes = usable
	}
	res.CreditCents = creditCents
	if creditCents > 0 {
		res.Lines = append(res.Lines, QuoteLine{LedgerCompTime, -creditCents, res.CreditMinutes, "补偿时长/权益抵扣"})
	}

	// 租拍 / 购球：固定服务费，按开场时刻所在时段的规则确定性收取
	addons := 0
	if in.RentRacket {
		if r, ok := rules[ScopeAt(p, in.Start)]; ok && r.RacketFeeCents > 0 {
			addons += r.RacketFeeCents
			res.Lines = append(res.Lines, QuoteLine{LedgerRacketFee, r.RacketFeeCents, 0, "租拍"})
		}
	}
	if in.BuyShuttle {
		if r, ok := rules[ScopeAt(p, in.Start)]; ok && r.ShuttleFeeCents > 0 {
			addons += r.ShuttleFeeCents
			res.Lines = append(res.Lines, QuoteLine{LedgerShuttleFee, r.ShuttleFeeCents, 0, "购球"})
		}
	}
	res.AddonsCents = addons

	payable := slot - discount - creditCents + addons
	if payable < 0 {
		payable = 0
	}
	res.PayableCents = payable
	return res
}

// CancellationQuote 退订费用计算：依据是否高峰、距开场小时数。
// 返回违约金与应退金额（仅基于传入的已付场地净额 + 附加费）。
type CancelQuote struct {
	FeeCents    int
	RefundCents int
	Peak        bool
	Reason      string
}

func CancellationQuote(p Policy, start time.Time, now time.Time, paidCents int) CancelQuote {
	peak := IsPeakAt(p, start)
	hours := start.Sub(now).Hours()
	var freeHours float64
	var ratio float64
	if peak {
		freeHours, ratio = p.PeakCancelFreeHours, p.PeakCancelFeeRatio
	} else {
		freeHours, ratio = p.OffpeakCancelFreeHours, p.OffpeakCancelFeeRatio
	}
	cq := CancelQuote{Peak: peak}
	switch {
	case hours < 0:
		// 开场后退订：不退
		cq.FeeCents = paidCents
		cq.Reason = "开场后退订，不予退款"
	case hours >= freeHours:
		cq.FeeCents = 0
		cq.Reason = "免费退订窗口内，全额退还"
	default:
		cq.FeeCents = int(math.Round(float64(paidCents) * ratio))
		cq.Reason = "高峰退订规则：不足免费窗口，按比例收取违约金"
		if !peak {
			cq.Reason = "非高峰退订规则：不足免费窗口，按比例收取违约金"
		}
	}
	cq.RefundCents = paidCents - cq.FeeCents
	if cq.RefundCents < 0 {
		cq.RefundCents = 0
	}
	return cq
}

// OvertimeFee 延时费用：按 scope 费率，15 分钟向上取整。
// compFreeMinutes 为前台补偿的免费分钟数（优先抵扣）。
func OvertimeFee(rules map[string]PriceRule, p Policy, overStart, overEnd time.Time, compFreeMinutes int) (feeCents, billableMins, compUsedMins int) {
	if !overEnd.After(overStart) {
		return 0, 0, 0
	}
	totalMins := int(math.Ceil(overEnd.Sub(overStart).Minutes()))
	compUsedMins = compFreeMinutes
	if compUsedMins > totalMins {
		compUsedMins = totalMins
	}
	billableMins = roundUp15(time.Duration(totalMins-compUsedMins) * time.Minute)
	if billableMins <= 0 {
		return 0, 0, compUsedMins
	}
	// 用 overStart 所在 scope 的费率（简化且可解释）
	scope := ScopeAt(p, overStart)
	r := rules[scope]
	feeCents = int(math.Round(float64(r.HourlyRateCents) * float64(billableMins) / 60.0))
	return feeCents, billableMins, compUsedMins
}

// OverPlayerFee 超员费用
func OverPlayerFee(p Policy, players int) (overCount, fee int) {
	if players <= p.MaxPlayers {
		return 0, 0
	}
	overCount = players - p.MaxPlayers
	return overCount, overCount * p.PerExtraPlayerCents
}

// LateFee 迟到罚金（按已付金额比例），封顶不超过已付
func LateFee(p Policy, paidCents int) int {
	f := int(math.Round(float64(paidCents) * p.LateFeeRatio))
	if f > paidCents {
		f = paidCents
	}
	return f
}

// ClassifyArrival 根据首个入场信号时间判定 early/ontime/late
func ClassifyArrival(p Policy, start time.Time, signalAt time.Time) string {
	if !signalAt.Before(start.Add(time.Duration(p.GraceMinutes) * time.Minute)) {
		return ArrivalLate
	}
	if signalAt.Before(start) {
		return ArrivalEarly
	}
	return ArrivalOnTime
}
