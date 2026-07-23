package telephony

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// requireOwner gates a telephony handler to the box OWNER. The single box SIM is
// the owner's line, so another authenticated account on a multi-user box must not
// read/send its SMS or place calls on it. Fail-closed: denied when the owner is
// unresolved (isOwner returns false for "").
func (s *Service) requireOwner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.isOwner(r.Header.Get("X-User-ID")) {
			http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// RegisterHandlers wires the /api/telephony/* routes onto mux. All routes are
// owner-gated. Safe to call even when no GSM is present — the handlers return
// empty/unavailable results (the phone app renders a "no modem" state).
func RegisterHandlers(mux *http.ServeMux, s *Service) {
	// Status (both the explicit path and the subtree root the app may probe).
	status := s.requireOwner(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, s.Status()) })
	mux.HandleFunc("GET /api/telephony/status", status)
	mux.HandleFunc("GET /api/telephony/", status)

	mux.HandleFunc("GET /api/telephony/sms/threads", s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.Threads())
	}))
	mux.HandleFunc("GET /api/telephony/sms/thread/{number}", s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.ThreadFor(r.PathValue("number")))
	}))
	mux.HandleFunc("POST /api/telephony/sms/send", s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			To     string `json:"to"`
			Number string `json:"number"`
			Body   string `json:"body"`
			Text   string `json:"text"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}
		if err := s.Send(firstNonEmpty(body.To, body.Number), firstNonEmpty(body.Body, body.Text)); err != nil {
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("GET /api/telephony/calls", s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.CallLog())
	}))
	mux.HandleFunc("POST /api/telephony/call", s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			To     string `json:"to"`
			Number string `json:"number"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body)
		if err := s.PlaceCall(firstNonEmpty(body.To, body.Number)); err != nil {
			writeJSON(w, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/telephony/call/hangup", s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
		_ = s.Hangup()
		writeJSON(w, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/telephony/call/decline", s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
		_ = s.Hangup()
		writeJSON(w, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("POST /api/telephony/call/answer", s.requireOwner(func(w http.ResponseWriter, r *http.Request) {
		_ = s.Accept()
		writeJSON(w, map[string]bool{"ok": true})
	}))

	mux.HandleFunc("GET /api/telephony/ws", s.serveWS)
}
