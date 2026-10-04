package api

import (
	"net/http"

	"gymbadminton/internal/service"
)

func (s *Server) handleDisputes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if _, ok := s.requireStaff(w, r, "cs", "operator", "front"); !ok {
			return
		}
		status := r.URL.Query().Get("status")
		list, err := s.Svc.ListDisputes(r.Context(), status, 100)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeOK(w, map[string]any{"disputes": list})
	case http.MethodPost:
		// 人工开争议单（客服/前台都可）
		c, ok := s.requireStaff(w, r, "cs", "front", "operator")
		if !ok {
			return
		}
		var req struct {
			BookingID int64  `json:"booking_id"`
			Issue     string `json:"issue"`
		}
		if err := decode(r, &req); err != nil {
			writeErr(w, err)
			return
		}
		d, err := s.Svc.OpenDispute(r.Context(), req.BookingID, c.Sub, req.Issue)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 201, d)
	default:
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
	}
}

// /api/v1/cs/disputes/{id}/resolve
func (s *Server) dispatchDispute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	c, ok := s.requireStaff(w, r, "cs")
	if !ok {
		return
	}
	parts := segs("/api/v1/cs/disputes", r.URL.Path)
	if len(parts) != 2 || parts[1] != "resolve" {
		http.NotFound(w, r)
		return
	}
	id, err := parseID64(parts[0])
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": map[string]string{"code": "BAD_ID", "message": "非法争议单 ID"}})
		return
	}
	var req struct {
		Action      string `json:"action"`
		RefundCents int64  `json:"refund_cents"`
		CompMinutes int    `json:"comp_minutes"`
		Note        string `json:"note"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	d, err := s.Svc.ResolveDispute(r.Context(), id, service.ResolutionInput(req.Action, req.RefundCents, req.CompMinutes, req.Note, c.Sub))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, d)
}
