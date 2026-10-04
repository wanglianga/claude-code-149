package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"courtbook/internal/service"
)

func (s *Server) routes() {
	r := s.route

	// ---------- 认证 ----------
	r.Handle("POST", "/api/auth/staff/login", s.staffLogin)
	r.Handle("POST", "/api/auth/member/login", s.memberLogin)

	// ---------- 会员 ----------
	r.Handle("GET", "/api/venues", s.requireStaff()(s.listVenues))
	r.Handle("GET", "/api/catalog", s.requireMember(s.catalog))
	r.Handle("GET", "/api/members/me/credits", s.requireMember(s.myCredits))
	r.Handle("GET", "/api/members/me/bookings", s.requireMember(s.myBookings))
	r.Handle("POST", "/api/bookings/quote", s.requireMember(s.quoteBooking))
	r.Handle("POST", "/api/bookings", s.requireMember(s.createBooking))
	r.Handle("GET", "/api/bookings/{id}", s.requireMember(s.getBooking))
	r.Handle("POST", "/api/bookings/{id}/pay", s.requireMember(s.payBooking))
	r.Handle("POST", "/api/bookings/{id}/cancel", s.requireMember(s.cancelBooking))
	r.Handle("POST", "/api/bookings/{id}/refund-requests", s.requireMember(s.createRefundRequest))

	// 场地占用视图：会员/员工均可
	r.Handle("GET", "/api/venues/{id}/availability", s.requireMember(s.availability))

	// ---------- 硬件回调（闸机 / 灯控） ----------
	r.Handle("POST", "/api/devices/gate/scan", s.requireDevice(s.gateScan))
	r.Handle("POST", "/api/devices/light/warmup", s.requireDevice(s.lightWarmup))
	r.Handle("POST", "/api/devices/light/on", s.requireDevice(s.lightOn))
	r.Handle("POST", "/api/devices/light/off", s.requireDevice(s.lightOff))

	// ---------- 前台 ----------
	fd := s.requireStaff("frontdesk", "admin", "operator")
	r.Handle("GET", "/api/staff/bookings/{id}", s.requireStaff()(s.staffGetBooking))
	r.Handle("POST", "/api/staff/bookings/{id}/frontdesk-sign", fd(s.frontdeskSign))
	r.Handle("POST", "/api/staff/bookings/{id}/waiting", fd(s.markWaiting))
	r.Handle("POST", "/api/staff/bookings/{id}/comp-minutes", fd(s.grantComp))
	r.Handle("POST", "/api/staff/bookings/{id}/players", fd(s.reportPlayers))
	r.Handle("POST", "/api/staff/bookings/{id}/cancel", fd(s.staffCancelBooking))

	// ---------- 运营端 ----------
	op := s.requireStaff("admin", "operator")
	r.Handle("GET", "/api/admin/venues", s.requireStaff("admin", "operator")(s.adminListVenues))
	r.Handle("POST", "/api/admin/maintenance", op(s.createMaintenance))
	r.Handle("POST", "/api/admin/maintenance/{id}/release", op(s.releaseMaintenance))
	r.Handle("POST", "/api/admin/extra-sessions", op(s.createExtraSession))
	r.Handle("POST", "/api/admin/extra-sessions/{id}/cancel", op(s.cancelExtraSession))
	r.Handle("PUT", "/api/admin/venues/{id}/policy", op(s.updatePolicy))
	r.Handle("GET", "/api/admin/venues/{id}/policy", op(s.getPolicy))
	r.Handle("PUT", "/api/admin/venues/{id}/prices", op(s.upsertPrice))
	r.Handle("PUT", "/api/admin/courts/{id}/status", op(s.setCourtStatus))
	r.Handle("POST", "/api/admin/members/{id}/tier", op(s.setMemberTier))
	r.Handle("POST", "/api/admin/members/{id}/unban", op(s.unbanMember))
	r.Handle("POST", "/api/admin/worker/run-once", s.requireStaff("admin")(s.runWorkerOnce))
	r.Handle("POST", "/api/admin/clock/set", s.requireStaff("admin")(s.clockSet))
	r.Handle("POST", "/api/admin/clock/reset", s.requireStaff("admin")(s.clockReset))

	// ---------- 客服 ----------
	cs := s.requireStaff("cs", "admin")
	r.Handle("GET", "/api/cs/bookings", cs(s.csFindBookings))
	r.Handle("GET", "/api/cs/bookings/{id}/evidence", cs(s.csEvidence))
	r.Handle("POST", "/api/cs/bookings/{id}/classify-noshow", cs(s.csClassify))
	r.Handle("GET", "/api/cs/refund-requests", cs(s.csListRefunds))
	r.Handle("POST", "/api/cs/refund-requests/{id}/decision", cs(s.csDecideRefund))
	r.Handle("POST", "/api/cs/disputes", cs(s.csOpenDispute))
	r.Handle("POST", "/api/cs/disputes/{id}/resolve", cs(s.csResolveDispute))
}

// ---------- 认证 ----------

func (s *Server) staffLogin(w http.ResponseWriter, r *http.Request) {
	var in struct{ Username, Password string }
	if decode(r, &in) != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	sess, err := s.svc.StaffLogin(r.Context(), in.Username, in.Password)
	if err != nil {
		writeErr(w, 401, err.Error())
		return
	}
	writeJSON(w, 200, sess)
}

func (s *Server) memberLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Phone   string `json:"phone"`
		Name    string `json:"name"`
		SMSCode string `json:"sms_code"`
	}
	if decode(r, &in) != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	sess, err := s.svc.MemberLogin(r.Context(), in.Phone, in.Name, in.SMSCode)
	if err != nil {
		writeErr(w, 401, err.Error())
		return
	}
	writeJSON(w, 200, sess)
}

// ---------- 会员接口 ----------

func bookingInput(r *http.Request) (service.CreateBookingInput, error) {
	var in struct {
		VenueID       int64  `json:"venue_id"`
		CourtID       int64  `json:"court_id"`
		StartTime     string `json:"start_time"`
		EndTime       string `json:"end_time"`
		PlayerCount   int    `json:"player_count"`
		RentRacket    bool   `json:"rent_racket"`
		BuyShuttle    bool   `json:"buy_shuttle"`
		UseCreditMins int    `json:"use_credit_minutes"`
	}
	if err := decode(r, &in); err != nil {
		return service.CreateBookingInput{}, err
	}
	st, err := parseTime(in.StartTime)
	if err != nil {
		return service.CreateBookingInput{}, errors.New("start_time 需为 RFC3339")
	}
	en, err := parseTime(in.EndTime)
	if err != nil {
		return service.CreateBookingInput{}, errors.New("end_time 需为 RFC3339")
	}
	return service.CreateBookingInput{
		VenueID: in.VenueID, CourtID: in.CourtID, StartTime: st, EndTime: en,
		PlayerCount: in.PlayerCount, RentRacket: in.RentRacket, BuyShuttle: in.BuyShuttle,
		UseCreditMins: in.UseCreditMins,
	}, nil
}

func (s *Server) quoteBooking(w http.ResponseWriter, r *http.Request) {
	in, err := bookingInput(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	in.MemberID = mustMember(r).ID
	q, err := s.svc.Quote(r.Context(), in)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, q)
}

func (s *Server) createBooking(w http.ResponseWriter, r *http.Request) {
	in, err := bookingInput(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	in.MemberID = mustMember(r).ID
	b, q, err := s.svc.CreateBooking(r.Context(), in)
	if err != nil {
		writeErr(w, mapErrStatus(err), err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"booking": b, "quote": q})
}

func (s *Server) getBooking(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	b, err := s.svc.Store.GetBooking(r.Context(), id)
	if err != nil {
		writeErr(w, 404, "订单不存在")
		return
	}
	if b.MemberID != mustMember(r).ID {
		writeErr(w, 403, "无权查看他人订单")
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) payBooking(w http.ResponseWriter, r *http.Request) {
	var in struct{ Method string }
	_ = decode(r, &in)
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	b, err := s.svc.Store.GetBooking(r.Context(), id)
	if err != nil || b.MemberID != mustMember(r).ID {
		writeErr(w, 404, "订单不存在")
		return
	}
	updated, err := s.svc.Pay(r.Context(), id, defaultStr(in.Method, "wechat"))
	if err != nil {
		writeErr(w, mapErrStatus(err), err.Error())
		return
	}
	writeJSON(w, 200, updated)
}

func (s *Server) cancelBooking(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason     string `json:"reason"`
		VenueFault bool   `json:"venue_fault"`
	}
	_ = decode(r, &in)
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	b, err := s.svc.Store.GetBooking(r.Context(), id)
	if err != nil || b.MemberID != mustMember(r).ID {
		writeErr(w, 404, "订单不存在")
		return
	}
	if in.VenueFault {
		writeErr(w, 403, "会员不能以场馆原因退订，请联系前台或提交争议")
		return
	}
	res, err := s.svc.Cancel(r.Context(), id, "member", strconv.FormatInt(mustMember(r).ID, 10), defaultStr(in.Reason, "会员退订"), false)
	if err != nil {
		writeErr(w, mapErrStatus(err), err.Error())
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) createRefundRequest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Amount int    `json:"amount_cents"`
		Reason string `json:"reason"`
	}
	_ = decode(r, &in)
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	rid, err := s.svc.CreateRefundRequest(r.Context(), mustMember(r).ID, id, in.Amount, defaultStr(in.Reason, "会员申请退款"))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"refund_request_id": rid})
}

func (s *Server) myBookings(w http.ResponseWriter, r *http.Request) {
	rows, err := s.svc.Store.Pool.Query(r.Context(),
		`SELECT id,order_no,venue_id,court_id,start_time,end_time,status,arrival_status,pay_status,paid_cents,refund_cents,entry_code
		 FROM bookings WHERE member_id=$1 ORDER BY id DESC LIMIT 100`, mustMember(r).ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, venueID int64
		var courtID int64
		var st, en time.Time
		var status, arrival, pay, orderNo string
		var paid, refunded int
		var code *string
		if rows.Scan(&id, &orderNo, &venueID, &courtID, &st, &en, &status, &arrival, &pay, &paid, &refunded, &code) != nil {
			continue
		}
		out = append(out, map[string]any{
			"id": id, "order_no": orderNo, "venue_id": venueID, "court_id": courtID,
			"start_time": st, "end_time": en, "status": status, "arrival_status": arrival,
			"pay_status": pay, "paid_cents": paid, "refund_cents": refunded, "entry_code": code,
		})
	}
	writeJSON(w, 200, map[string]any{"bookings": out})
}

func (s *Server) myCredits(w http.ResponseWriter, r *http.Request) {
	mins, err := s.svc.TotalCreditMinutes(r.Context(), mustMember(r).ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"remaining_minutes": mins})
}

func (s *Server) availability(w http.ResponseWriter, r *http.Request) {
	venueID, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	date := r.URL.Query().Get("date")
	if date == "" {
		date = s.svc.Now().Format("2006-01-02")
	}
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		writeErr(w, 400, "date 需为 YYYY-MM-DD")
		return
	}
	v, err := s.svc.Availability(r.Context(), venueID, d)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, v)
}

// ---------- 硬件回调 ----------

func (s *Server) gateScan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code     string `json:"entry_code"`
		GateID   string `json:"gate_id"`
		SignalID string `json:"signal_id"`
	}
	if decode(r, &in) != nil || in.Code == "" {
		writeErr(w, 400, "需要 entry_code")
		return
	}
	b, res, err := s.svc.GateScan(r.Context(), in.Code, defaultStr(in.GateID, "G1"), in.SignalID)
	if err != nil {
		writeErr(w, 403, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"booking_id": b.ID, "result": res})
}

func (s *Server) lightWarmup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BookingID int64  `json:"booking_id"`
		SignalID  string `json:"signal_id"`
	}
	if decode(r, &in) != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	b, err := s.svc.LightWarmup(r.Context(), in.BookingID, in.SignalID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) lightOn(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BookingID int64  `json:"booking_id"`
		SignalID  string `json:"signal_id"`
	}
	if decode(r, &in) != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	b, err := s.svc.LightOn(r.Context(), in.BookingID, in.SignalID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) lightOff(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BookingID int64  `json:"booking_id"`
		OffAt     string `json:"off_at"`
		SignalID  string `json:"signal_id"`
	}
	if decode(r, &in) != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	var off time.Time
	if in.OffAt != "" {
		t, err := parseTime(in.OffAt)
		if err != nil {
			writeErr(w, 400, "off_at 需为 RFC3339")
			return
		}
		off = t
	}
	res, err := s.svc.LightOff(r.Context(), in.BookingID, off, defaultStr(in.SignalID, "auto"))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

// ---------- 前台 ----------

func (s *Server) staffGetBooking(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	b, err := s.svc.Store.GetBooking(r.Context(), id)
	if err != nil {
		writeErr(w, 404, "订单不存在")
		return
	}
	evs, _ := s.svc.Store.ListEvents(r.Context(), id)
	led, _ := s.svc.Store.ListLedger(r.Context(), id)
	writeJSON(w, 200, map[string]any{"booking": b, "events": evs, "ledger": led})
}

func (s *Server) frontdeskSign(w http.ResponseWriter, r *http.Request) {
	var in struct{ Note string }
	_ = decode(r, &in)
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	b, err := s.svc.FrontdeskSign(r.Context(), id, mustStaff(r).ID, in.Note)
	if err != nil {
		writeErr(w, mapErrStatus(err), err.Error())
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) markWaiting(w http.ResponseWriter, r *http.Request) {
	var in struct{ Note string }
	_ = decode(r, &in)
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	b, err := s.svc.MarkWaiting(r.Context(), id, mustStaff(r).ID, defaultStr(in.Note, "在场边等待"))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) grantComp(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Minutes int    `json:"minutes"`
		Note    string `json:"note"`
	}
	if decode(r, &in) != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	b, err := s.svc.GrantCompMinutes(r.Context(), id, mustStaff(r).ID, in.Minutes, defaultStr(in.Note, "前台现场补偿"))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) reportPlayers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Players int `json:"players"`
	}
	if decode(r, &in) != nil || in.Players < 1 {
		writeErr(w, 400, "需要 players（>=1）")
		return
	}
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	b, fee, err := s.svc.ReportPlayers(r.Context(), id, mustStaff(r).ID, in.Players)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"booking": b, "over_player_fee_cents": fee})
}

func (s *Server) staffCancelBooking(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason     string `json:"reason"`
		VenueFault bool   `json:"venue_fault"`
	}
	_ = decode(r, &in)
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	st := mustStaff(r)
	res, err := s.svc.Cancel(r.Context(), id, "frontdesk", strconv.FormatInt(st.ID, 10),
		defaultStr(in.Reason, "前台退订"), in.VenueFault)
	if err != nil {
		writeErr(w, mapErrStatus(err), err.Error())
		return
	}
	writeJSON(w, 200, res)
}

// ---------- 运营端 ----------

func (s *Server) listVenues(w http.ResponseWriter, r *http.Request) { s.adminListVenues(w, r) }

// catalog 会员浏览：场馆、场地、策略摘要、价格
func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}
	rows, err := s.svc.Store.Pool.Query(r.Context(), `
		SELECT id,name,address,open_time::text,close_time::text FROM venues ORDER BY id`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type v struct {
		ID                                 int64 `json:"id"`
		Name, Address, OpenTime, CloseTime string
	}
	var venues []v
	for rows.Next() {
		var x v
		if rows.Scan(&x.ID, &x.Name, &x.Address, &x.OpenTime, &x.CloseTime) == nil {
			venues = append(venues, x)
		}
	}
	rows.Close()
	crows, err := s.svc.Store.Pool.Query(r.Context(),
		`SELECT id,venue_id,court_no,status FROM courts ORDER BY venue_id,court_no`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type c struct {
		ID      int64  `json:"id"`
		VenueID int64  `json:"venue_id"`
		CourtNo string `json:"court_no"`
		Status  string `json:"status"`
	}
	var courts []c
	for crows.Next() {
		var x c
		if crows.Scan(&x.ID, &x.VenueID, &x.CourtNo, &x.Status) == nil {
			courts = append(courts, x)
		}
	}
	crows.Close()
	prows, _ := s.svc.Store.Pool.Query(r.Context(),
		`SELECT venue_id,scope,hourly_rate_cents,racket_fee_cents,shuttle_fee_cents FROM price_rules ORDER BY venue_id,scope`)
	type pr struct {
		VenueID int64  `json:"venue_id"`
		Scope   string `json:"scope"`
		Hourly  int    `json:"hourly_rate_cents"`
		Racket  int    `json:"racket_fee_cents"`
		Shuttle int    `json:"shuttle_fee_cents"`
	}
	var prices []pr
	for prows.Next() {
		var x pr
		if prows.Scan(&x.VenueID, &x.Scope, &x.Hourly, &x.Racket, &x.Shuttle) == nil {
			prices = append(prices, x)
		}
	}
	prows.Close()
	polrows, _ := s.svc.Store.Pool.Query(r.Context(), `
		SELECT venue_id,payment_timeout_min,warmup_minutes,early_entry_minutes,grace_minutes,
		       late_cutoff_minutes,peak_start::text,peak_end::text,peak_cancel_free_hours,
		       peak_cancel_fee_ratio,offpeak_cancel_free_hours,offpeak_cancel_fee_ratio,
		       max_players,tier_book_ahead_days,tier_discount_pct
		FROM venue_policies ORDER BY venue_id`)
	var policies []map[string]any
	for polrows.Next() {
		var venueID int64
		var payTo, warm, early, grace, cutoff, maxP int
		var peakStart, peakEnd string
		var peakFree, peakRatio, offFree, offRatio float64
		var ahead, disc []byte
		if polrows.Scan(&venueID, &payTo, &warm, &early, &grace, &cutoff, &peakStart, &peakEnd,
			&peakFree, &peakRatio, &offFree, &offRatio, &maxP, &ahead, &disc) == nil {
			var am, dm map[string]any
			json.Unmarshal(ahead, &am)
			json.Unmarshal(disc, &dm)
			policies = append(policies, map[string]any{
				"venue_id": venueID, "payment_timeout_min": payTo, "warmup_minutes": warm,
				"early_entry_minutes": early, "grace_minutes": grace, "late_cutoff_minutes": cutoff,
				"peak_start": peakStart, "peak_end": peakEnd,
				"peak_cancel_free_hours": peakFree, "peak_cancel_fee_ratio": peakRatio,
				"offpeak_cancel_free_hours": offFree, "offpeak_cancel_fee_ratio": offRatio,
				"max_players": maxP, "tier_book_ahead_days": am, "tier_discount_pct": dm,
			})
		}
	}
	polrows.Close()
	out["venues"] = venues
	out["courts"] = courts
	out["prices"] = prices
	out["policies"] = policies
	writeJSON(w, 200, out)
}

func (s *Server) adminListVenues(w http.ResponseWriter, r *http.Request) {
	rows, err := s.svc.Store.Pool.Query(r.Context(),
		`SELECT v.id,v.name,v.address,v.open_time::text,v.close_time::text,
		        (SELECT count(*) FROM courts c WHERE c.venue_id=v.id)
		 FROM venues v ORDER BY v.id`)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type venue struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		Address    string `json:"address"`
		OpenTime   string `json:"open_time"`
		CloseTime  string `json:"close_time"`
		CourtCount int    `json:"court_count"`
	}
	var out []venue
	for rows.Next() {
		var v venue
		if rows.Scan(&v.ID, &v.Name, &v.Address, &v.OpenTime, &v.CloseTime, &v.CourtCount) == nil {
			out = append(out, v)
		}
	}
	// 附带场地列表
	crows, _ := s.svc.Store.Pool.Query(r.Context(),
		`SELECT id,venue_id,court_no,status FROM courts ORDER BY venue_id,court_no`)
	defer crows.Close()
	type court struct {
		ID      int64  `json:"id"`
		VenueID int64  `json:"venue_id"`
		CourtNo string `json:"court_no"`
		Status  string `json:"status"`
	}
	var courts []court
	for crows.Next() {
		var c court
		if crows.Scan(&c.ID, &c.VenueID, &c.CourtNo, &c.Status) == nil {
			courts = append(courts, c)
		}
	}
	writeJSON(w, 200, map[string]any{"venues": out, "courts": courts})
}

func parseStartEnd(r *http.Request) (int64, int64, time.Time, time.Time, string, error) {
	var in struct {
		VenueID   int64  `json:"venue_id"`
		CourtID   int64  `json:"court_id"`
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
		Reason    string `json:"reason"`
		Note      string `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		return 0, 0, time.Time{}, time.Time{}, "", err
	}
	st, err := parseTime(in.StartTime)
	if err != nil {
		return 0, 0, time.Time{}, time.Time{}, "", errors.New("start_time 需为 RFC3339")
	}
	en, err := parseTime(in.EndTime)
	if err != nil {
		return 0, 0, time.Time{}, time.Time{}, "", errors.New("end_time 需为 RFC3339")
	}
	note := in.Reason
	if in.Note != "" {
		note = in.Note
	}
	return in.VenueID, in.CourtID, st, en, note, nil
}

func (s *Server) createMaintenance(w http.ResponseWriter, r *http.Request) {
	venueID, courtID, st, en, reason, err := parseStartEnd(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	res, err := s.svc.CreateMaintenance(r.Context(), venueID, courtID, mustStaff(r).ID, st, en, defaultStr(reason, "场地临时维护"))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, res)
}

func (s *Server) releaseMaintenance(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	if err := s.svc.ReleaseMaintenance(r.Context(), id, mustStaff(r).ID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "released"})
}

func (s *Server) createExtraSession(w http.ResponseWriter, r *http.Request) {
	venueID, courtID, st, en, note, err := parseStartEnd(r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	id, err := s.svc.CreateExtraSession(r.Context(), venueID, courtID, mustStaff(r).ID, st, en, defaultStr(note, "临时加场"))
	if err != nil {
		writeErr(w, mapErrStatus(err), err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"extra_session_id": id})
}

func (s *Server) cancelExtraSession(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	if err := s.svc.CancelExtraSession(r.Context(), id, mustStaff(r).ID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "cancelled"})
}

func (s *Server) updatePolicy(w http.ResponseWriter, r *http.Request) {
	venueID, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	var patch map[string]any
	if decode(r, &patch) != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	if err := s.svc.UpdatePolicy(r.Context(), venueID, mustStaff(r).ID, patch); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "updated"})
}

func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request) {
	venueID, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	rows, err := s.svc.Store.Pool.Query(r.Context(), `
		SELECT row_to_json(p) FROM venue_policies p WHERE venue_id=$1
		UNION ALL
		SELECT row_to_json(pr) FROM price_rules pr WHERE venue_id=$1`, venueID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	var raw []map[string]any
	for rows.Next() {
		var b []byte
		if rows.Scan(&b) == nil {
			var m map[string]any
			if json.Unmarshal(b, &m) == nil {
				raw = append(raw, m)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"rows": raw})
}

func (s *Server) upsertPrice(w http.ResponseWriter, r *http.Request) {
	venueID, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	var in struct {
		Scope           string `json:"scope"`
		HourlyRateCents int    `json:"hourly_rate_cents"`
		RacketFeeCents  int    `json:"racket_fee_cents"`
		ShuttleFeeCents int    `json:"shuttle_fee_cents"`
	}
	if decode(r, &in) != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	if err := s.svc.UpsertPriceRule(r.Context(), venueID, mustStaff(r).ID,
		in.Scope, in.HourlyRateCents, in.RacketFeeCents, in.ShuttleFeeCents); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "updated"})
}

func (s *Server) setCourtStatus(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	var in struct {
		Status string `json:"status"`
	}
	if decode(r, &in) != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	if err := s.svc.SetCourtStatus(r.Context(), id, in.Status, mustStaff(r).ID); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": in.Status})
}

func (s *Server) setMemberTier(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	var in struct {
		Tier string `json:"tier"`
	}
	if decode(r, &in) != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	if err := s.svc.SetMemberTier(r.Context(), id, in.Tier); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "updated"})
}

func (s *Server) unbanMember(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	if err := s.svc.UnbanMember(r.Context(), id); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "unbanned"})
}

func (s *Server) runWorkerOnce(w http.ResponseWriter, r *http.Request) {
	s.worker.RunOnce(r.Context())
	writeJSON(w, 200, map[string]string{"status": "done"})
}

// clockSet 设定虚拟时钟（演示/自动化验证用，生产不开启：受 ENABLE_TEST_CLOCK 控制）
func (s *Server) clockSet(w http.ResponseWriter, r *http.Request) {
	if !s.testClockEnabled {
		writeErr(w, 403, "虚拟时钟未启用（需 ENABLE_TEST_CLOCK=true）")
		return
	}
	var in struct {
		At string `json:"at"`
	}
	if decode(r, &in) != nil || in.At == "" {
		writeErr(w, 400, "需要 at（RFC3339）")
		return
	}
	t, err := parseTime(in.At)
	if err != nil {
		writeErr(w, 400, "at 需为 RFC3339")
		return
	}
	s.svc.SetFixedNow(&t)
	writeJSON(w, 200, map[string]any{"now": s.svc.Now()})
}

func (s *Server) clockReset(w http.ResponseWriter, r *http.Request) {
	if !s.testClockEnabled {
		writeErr(w, 403, "虚拟时钟未启用（需 ENABLE_TEST_CLOCK=true）")
		return
	}
	s.svc.SetFixedNow(nil)
	writeJSON(w, 200, map[string]any{"now": s.svc.Now()})
}

// ---------- 客服 ----------

func (s *Server) csFindBookings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var args []any
	sql := `
		SELECT b.id,b.order_no,b.venue_id,b.court_id,b.member_id,m.phone,m.name,
		       b.start_time,b.end_time,b.status,b.arrival_status,b.pay_status,b.paid_cents,b.refund_cents
		FROM bookings b JOIN members m ON m.id=b.member_id
		WHERE 1=1`
	switch {
	case q.Get("order_no") != "":
		sql += " AND b.order_no=$1"
		args = append(args, q.Get("order_no"))
	case q.Get("phone") != "":
		sql += " AND m.phone=$1"
		args = append(args, q.Get("phone"))
	case q.Get("entry_code") != "":
		sql += " AND b.entry_code=$1"
		args = append(args, q.Get("entry_code"))
	case q.Get("date") != "":
		sql += " AND to_char(b.start_time,'YYYY-MM-DD')=$1"
		args = append(args, q.Get("date"))
	}
	sql += " ORDER BY b.id DESC LIMIT 100"
	rows, err := s.svc.Store.Pool.Query(r.Context(), sql, args...)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, venueID, courtID, memberID int64
		var orderNo, phone, name, status, arrival, pay string
		var st, en time.Time
		var paid, refund int
		if rows.Scan(&id, &orderNo, &venueID, &courtID, &memberID, &phone, &name,
			&st, &en, &status, &arrival, &pay, &paid, &refund) == nil {
			out = append(out, map[string]any{
				"id": id, "order_no": orderNo, "venue_id": venueID, "court_id": courtID,
				"member_id": memberID, "phone": phone, "name": name,
				"start_time": st, "end_time": en, "status": status, "arrival_status": arrival,
				"pay_status": pay, "paid_cents": paid, "refund_cents": refund,
			})
		}
	}
	writeJSON(w, 200, map[string]any{"bookings": out})
}

func (s *Server) csEvidence(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	bundle, err := s.svc.Evidence(r.Context(), id)
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, bundle)
}

func (s *Server) csClassify(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	res, err := s.svc.ClassifyNoShow(r.Context(), id)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) csListRefunds(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	rows, err := s.svc.Store.Pool.Query(r.Context(), `
		SELECT rr.id,rr.booking_id,b.order_no,rr.applicant_id,rr.amount_cents,rr.reason,rr.status,rr.created_at
		FROM refund_requests rr JOIN bookings b ON b.id=rr.booking_id
		WHERE ($1='' OR rr.status=$1) ORDER BY rr.id DESC LIMIT 200`, status)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, bid, applicant int64
		var amount int
		var orderNo, reason, st string
		var at time.Time
		if rows.Scan(&id, &bid, &orderNo, &applicant, &amount, &reason, &st, &at) == nil {
			out = append(out, map[string]any{
				"id": id, "booking_id": bid, "order_no": orderNo, "applicant_id": applicant,
				"amount_cents": amount, "reason": reason, "status": st, "created_at": at,
			})
		}
	}
	writeJSON(w, 200, map[string]any{"refund_requests": out})
}

func (s *Server) csDecideRefund(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	var in struct {
		Approve bool   `json:"approve"`
		Note    string `json:"note"`
	}
	if decode(r, &in) != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	if err := s.svc.DecideRefundRequest(r.Context(), id, mustStaff(r).ID, in.Approve, defaultStr(in.Note, "-")); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "decided"})
}

func (s *Server) csOpenDispute(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BookingID int64  `json:"booking_id"`
		Reason    string `json:"reason"`
	}
	if decode(r, &in) != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	id, err := s.svc.OpenDispute(r.Context(), in.BookingID, mustStaff(r).ID, in.Reason)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, map[string]any{"dispute_id": id})
}

func (s *Server) csResolveDispute(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(pathValue(r, "id"), 10, 64)
	var in struct {
		Kind         string `json:"kind"`
		RefundAmount int    `json:"refund_amount_cents"`
		CompMinutes  int    `json:"comp_minutes"`
		WaiveNoShow  bool   `json:"waive_no_show"`
		Note         string `json:"note"`
	}
	if decode(r, &in) != nil {
		writeErr(w, 400, "请求体格式错误")
		return
	}
	d, err := s.svc.ResolveDispute(r.Context(), service.ResolveDisputeInput{
		DisputeID: id, StaffID: mustStaff(r).ID, Kind: in.Kind,
		RefundAmount: in.RefundAmount, CompMinutes: in.CompMinutes,
		WaiveNoShow: in.WaiveNoShow, Note: defaultStr(in.Note, "-"),
	})
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, d)
}

// ---------- 小工具 ----------

func defaultStr(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func mapErrStatus(err error) int {
	switch {
	case errors.Is(err, service.ErrSlotConflict), errors.Is(err, service.ErrMaintenance):
		return 409
	case errors.Is(err, service.ErrInvalidState):
		return 409
	case errors.Is(err, service.ErrPaymentTimeout):
		return 408
	case errors.Is(err, service.ErrBanned), errors.Is(err, service.ErrBookAhead),
		errors.Is(err, service.ErrOutsideHours), errors.Is(err, service.ErrTooManyPlayers):
		return 422
	}
	return 400
}
