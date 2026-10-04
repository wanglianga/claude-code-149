package domain

import (
	"testing"
	"time"
)

func testPolicy() Policy {
	return Policy{
		PaymentTimeoutMin: 15, WarmupMinutes: 10, EarlyEntryMinutes: 20,
		GraceMinutes: 10, LateCutoffMinutes: 30, LateFeeRatio: 0.2,
		PeakStartMin: 18 * 60, PeakEndMin: 22 * 60,
		PeakCancelFreeHours: 24, PeakCancelFeeRatio: 0.5,
		OffpeakCancelFreeHours: 2, OffpeakCancelFeeRatio: 0.3,
		MaxPlayers: 6, PerExtraPlayerCents: 2000, NoShowBanThreshold: 3,
		TierDiscountPct: map[string]int{"normal": 0, "silver": 10, "gold": 20},
	}
}

func testRules() map[string]PriceRule {
	return map[string]PriceRule{
		"weekday_offpeak": {Scope: "weekday_offpeak", HourlyRateCents: 6000, RacketFeeCents: 1000, ShuttleFeeCents: 2500},
		"weekday_peak":    {Scope: "weekday_peak", HourlyRateCents: 10000, RacketFeeCents: 1000, ShuttleFeeCents: 2500},
		"weekend":         {Scope: "weekend", HourlyRateCents: 9000, RacketFeeCents: 1000, ShuttleFeeCents: 2500},
	}
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestQuoteWeekdayPeakGold(t *testing.T) {
	p := testPolicy()
	// 周三 19:00-21:00 高峰
	st := mustTime("2026-10-07T19:00:00Z")
	q := Quote(p, testRules(), QuoteInput{Start: st, End: st.Add(2 * time.Hour), Tier: "gold", PlayerCount: 4})
	if q.SlotCents != 20000 {
		t.Fatalf("slot = %d, want 20000", q.SlotCents)
	}
	if q.DiscountCents != 4000 {
		t.Fatalf("discount = %d, want 4000", q.DiscountCents)
	}
	if q.PayableCents != 16000 {
		t.Fatalf("payable = %d, want 16000", q.PayableCents)
	}
}

func TestQuoteCrossPeakBoundary(t *testing.T) {
	p := testPolicy()
	// 周三 17:30-18:30：30 分钟非高峰 60 + 30 分钟高峰 100 = 3000+5000=8000
	st := mustTime("2026-10-07T17:30:00Z")
	q := Quote(p, testRules(), QuoteInput{Start: st, End: st.Add(time.Hour), Tier: "normal"})
	if q.SlotCents != 8000 {
		t.Fatalf("cross-boundary slot = %d, want 8000", q.SlotCents)
	}
}

func TestQuoteAddons(t *testing.T) {
	p := testPolicy()
	st := mustTime("2026-10-07T19:00:00Z")
	q := Quote(p, testRules(), QuoteInput{Start: st, End: st.Add(time.Hour), Tier: "normal", RentRacket: true, BuyShuttle: true})
	if q.AddonsCents != 3500 {
		t.Fatalf("addons = %d, want 3500", q.AddonsCents)
	}
	if q.PayableCents != 13500 {
		t.Fatalf("payable = %d, want 13500", q.PayableCents)
	}
}

func TestQuoteCreditMinutes(t *testing.T) {
	p := testPolicy()
	st := mustTime("2026-10-07T19:00:00Z")
	// 10000/60 分每分钟；补偿 30 分钟 => 5000 分抵扣
	q := Quote(p, testRules(), QuoteInput{Start: st, End: st.Add(time.Hour), Tier: "normal", CreditMinutesToUse: 30})
	if q.CreditMinutes != 30 || q.CreditCents != 5000 {
		t.Fatalf("credit = (%d,%d), want (30,5000)", q.CreditMinutes, q.CreditCents)
	}
	if q.PayableCents != 5000 {
		t.Fatalf("payable = %d, want 5000", q.PayableCents)
	}
	// 超出时长按全单封顶
	q2 := Quote(p, testRules(), QuoteInput{Start: st, End: st.Add(time.Hour), Tier: "normal", CreditMinutesToUse: 90})
	if q2.CreditMinutes != 60 || q2.PayableCents != 0 {
		t.Fatalf("over-credit = (%d mins, %d payable)", q2.CreditMinutes, q2.PayableCents)
	}
}

func TestCancellationPeak(t *testing.T) {
	p := testPolicy()
	start := mustTime("2026-10-07T19:00:00Z")
	// 提前 2 小时（不足 24h）高峰 => 50%
	cq := CancellationQuote(p, start, start.Add(-2*time.Hour), 10000)
	if cq.FeeCents != 5000 || cq.RefundCents != 5000 || !cq.Peak {
		t.Fatalf("peak cancel = %+v", cq)
	}
	// 提前 48 小时 => 免费全退
	cq2 := CancellationQuote(p, start, start.Add(-48*time.Hour), 10000)
	if cq2.FeeCents != 0 || cq2.RefundCents != 10000 {
		t.Fatalf("free cancel = %+v", cq2)
	}
}

func TestCancellationOffPeak(t *testing.T) {
	p := testPolicy()
	start := mustTime("2026-10-07T15:00:00Z") // 非高峰
	// 提前 1 小时（不足 2h）=> 30%
	cq := CancellationQuote(p, start, start.Add(-time.Hour), 10000)
	if cq.Peak || cq.FeeCents != 3000 || cq.RefundCents != 7000 {
		t.Fatalf("offpeak cancel = %+v", cq)
	}
}

func TestOvertimeRounded15(t *testing.T) {
	p := testPolicy()
	rules := testRules()
	start := mustTime("2026-10-07T20:00:00Z") // 高峰 100/h
	// 延时 20 分钟 => 进位 30 分钟 => 5000
	fee, billable, _ := OvertimeFee(rules, p, start, start.Add(20*time.Minute), 0)
	if billable != 30 || fee != 5000 {
		t.Fatalf("overtime = (%d mins, %d fee)", billable, fee)
	}
	// 补偿 10 分钟，20 分钟延时 => 10 分钟 => 进位 15 => 2500
	fee2, billable2, comp := OvertimeFee(rules, p, start, start.Add(20*time.Minute), 10)
	if comp != 10 || billable2 != 15 || fee2 != 2500 {
		t.Fatalf("overtime comp = (%d,%d,%d)", comp, billable2, fee2)
	}
}

func TestOverPlayerFee(t *testing.T) {
	p := testPolicy()
	over, fee := OverPlayerFee(p, 8)
	if over != 2 || fee != 4000 {
		t.Fatalf("over player = (%d,%d)", over, fee)
	}
	over, _ = OverPlayerFee(p, 6)
	if over != 0 {
		t.Fatalf("6 players should be within limit")
	}
}

func TestClassifyArrival(t *testing.T) {
	p := testPolicy()
	start := mustTime("2026-10-07T19:00:00Z")
	if got := ClassifyArrival(p, start, start.Add(-15*time.Minute)); got != ArrivalEarly {
		t.Fatalf("early got %s", got)
	}
	if got := ClassifyArrival(p, start, start.Add(5*time.Minute)); got != ArrivalOnTime {
		t.Fatalf("ontime got %s", got)
	}
	if got := ClassifyArrival(p, start, start.Add(20*time.Minute)); got != ArrivalLate {
		t.Fatalf("late got %s", got)
	}
}

func TestLateFee(t *testing.T) {
	p := testPolicy()
	if f := LateFee(p, 10000); f != 2000 {
		t.Fatalf("late fee = %d, want 2000", f)
	}
}
