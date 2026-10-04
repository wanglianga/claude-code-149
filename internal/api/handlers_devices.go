package api

import "net/http"

// ---------- 设备端：闸机 ----------

func (s *Server) handleGateScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	var req struct {
		EntryCode string `json:"entry_code"`
		DeviceID  string `json:"device_id"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	res, err := s.Svc.GateScan(r.Context(), req.EntryCode, req.DeviceID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, res)
}

// ---------- 设备端：灯控（按预约 ID 的直连指令，鉴权与 /bookings/{id}/light 相同逻辑） ----------

// /api/v1/devices/light/{bookingId}
func (s *Server) dispatchDeviceLight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	parts := segs("/api/v1/devices/light", r.URL.Path)
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	id, err := parseID64(parts[0])
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": map[string]string{"code": "BAD_ID", "message": "非法预约 ID"}})
		return
	}
	var req struct {
		Action string `json:"action"`
		Source string `json:"source"`
		Detail string `json:"detail"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	src := req.Source
	if src == "" {
		src = "device"
	}
	b, err := s.Svc.LightCommand(r.Context(), id, req.Action, src, nil, req.Detail)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, b)
}
