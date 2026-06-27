// Package smsotp implements SMS OTP receive via a VoIP provider webhook (Twilio).
// It stores recent messages in ~/.vulos/auth/sms/history.json (24-hour retention),
// extracts 4–8 digit OTP codes, and fires a system notification for each message.
//
// Wire up with: smsotp.New(notifySvc).RegisterHandlers(mux)
// Do NOT edit main.go or notify.go — that wiring is a separate follow-up task (AUTH-15).
package smsotp

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"vulos/backend/services/notify"
)

// ─── Types ────────────────────────────────────────────────────────────────────

// SMSMessage is a single received SMS stored in history.
type SMSMessage struct {
	ID         string    `json:"id"`
	From       string    `json:"from"`
	To         string    `json:"to"`
	Body       string    `json:"body"`
	OTP        string    `json:"otp,omitempty"` // extracted code, empty if none found
	ReceivedAt time.Time `json:"received_at"`
}

// Settings holds number-provisioning and auto-fill preferences.
// Number provisioning is config-driven / stubbed; a real Twilio sub-account
// integration is a follow-up.
type Settings struct {
	Number         string `json:"number"`          // E.164 virtual number
	Provider       string `json:"provider"`        // e.g. "twilio"
	AutoFill       bool   `json:"auto_fill"`       // future: auto-fill OTP into waiting page
	RetentionHours int    `json:"retention_hours"` // default 24
	WebhookToken   string `json:"webhook_token"`   // shared secret for webhook validation (optional)
}

// ─── Regex ────────────────────────────────────────────────────────────────────

// otpRe matches a standalone 4–8 digit sequence.
// Word-boundary anchors prevent matching card numbers, dates, etc.
var otpRe = regexp.MustCompile(`\b(\d{4,8})\b`)

// extractOTP returns the first OTP-like code found in body, or "".
func extractOTP(body string) string {
	m := otpRe.FindStringSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// ─── Service ──────────────────────────────────────────────────────────────────

// Service handles incoming SMS webhooks and serves the recent-SMS API.
type Service struct {
	mu       sync.Mutex
	history  []SMSMessage
	settings Settings
	notifier *notify.Service
	histPath string // ~/.vulos/auth/sms/history.json
	cfgPath  string // ~/.vulos/auth/sms/config.json
}

// New creates a Service wired to the given notify.Service.
func New(n *notify.Service) *Service {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	base := filepath.Join(home, ".vulos", "auth", "sms")

	svc := &Service{
		notifier: n,
		histPath: filepath.Join(base, "history.json"),
		cfgPath:  filepath.Join(base, "config.json"),
		settings: Settings{
			Provider:       "twilio",
			RetentionHours: 24,
		},
	}
	svc.load()
	return svc
}

// RegisterHandlers attaches all SMS OTP routes to mux.
// This does NOT modify main.go — the caller decides when/whether to call this.
func (s *Service) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/sms/webhook", s.handleWebhook)
	mux.HandleFunc("GET /api/auth/sms/recent", s.handleRecent)
	mux.HandleFunc("GET /api/auth/sms/number", s.handleNumber)
	mux.HandleFunc("PUT /api/auth/sms/settings", s.handleSettings)
}

// ─── Persistence ──────────────────────────────────────────────────────────────

func (s *Service) load() {
	if err := os.MkdirAll(filepath.Dir(s.histPath), 0700); err != nil {
		log.Printf("[smsotp] mkdir: %v", err)
		return
	}

	// Load settings
	if data, err := os.ReadFile(s.cfgPath); err == nil {
		var cfg Settings
		if json.Unmarshal(data, &cfg) == nil {
			s.settings = cfg
			if s.settings.RetentionHours <= 0 {
				s.settings.RetentionHours = 24
			}
		}
	}

	// Load history
	if data, err := os.ReadFile(s.histPath); err == nil {
		var msgs []SMSMessage
		if json.Unmarshal(data, &msgs) == nil {
			s.history = msgs
		}
	}

	s.pruneOld()
}

func (s *Service) save() {
	// History
	data, err := json.MarshalIndent(s.history, "", "  ")
	if err == nil {
		if writeErr := os.WriteFile(s.histPath, data, 0600); writeErr != nil {
			log.Printf("[smsotp] save history: %v", writeErr)
		}
	}

	// Settings
	cfgData, err := json.MarshalIndent(s.settings, "", "  ")
	if err == nil {
		if writeErr := os.WriteFile(s.cfgPath, cfgData, 0600); writeErr != nil {
			log.Printf("[smsotp] save config: %v", writeErr)
		}
	}
}

// pruneOld removes messages older than RetentionHours. Caller must hold s.mu.
func (s *Service) pruneOld() {
	cutoff := time.Now().Add(-time.Duration(s.settings.RetentionHours) * time.Hour)
	kept := s.history[:0]
	for _, m := range s.history {
		if m.ReceivedAt.After(cutoff) {
			kept = append(kept, m)
		}
	}
	s.history = kept
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

// handleWebhook receives Twilio's form-encoded POST for inbound SMS.
//
// Twilio sends (among others):
//
//	From     — sender E.164 number
//	To       — recipient E.164 number (your virtual number)
//	Body     — SMS text
//	MessageSid — Twilio message SID (used as our ID)
func (s *Service) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeErr(w, 400, "cannot parse form body")
		return
	}

	from := r.FormValue("From")
	to := r.FormValue("To")
	body := r.FormValue("Body")
	sid := r.FormValue("MessageSid")

	if body == "" {
		// Twilio also sends delivery status callbacks with no Body — ignore silently.
		w.Header().Set("Content-Type", "text/xml")
		fmt.Fprint(w, "<Response/>")
		return
	}

	if sid == "" {
		sid = fmt.Sprintf("local-%d", time.Now().UnixNano())
	}

	otp := extractOTP(body)

	msg := SMSMessage{
		ID:         sid,
		From:       from,
		To:         to,
		Body:       body,
		OTP:        otp,
		ReceivedAt: time.Now(),
	}

	s.mu.Lock()
	s.pruneOld()
	s.history = append(s.history, msg)
	s.save()
	s.mu.Unlock()

	// Fire notification
	if s.notifier != nil {
		title := "SMS received"
		notifBody := body
		if otp != "" {
			title = fmt.Sprintf("OTP: %s", otp)
			notifBody = fmt.Sprintf("Code %s from %s — %s", otp, from, body)
		} else {
			notifBody = fmt.Sprintf("From %s: %s", from, body)
		}
		s.notifier.Send(title, notifBody, notify.LevelInfo, "smsotp")
	}

	log.Printf("[smsotp] received SMS from=%s otp=%q body=%q", from, otp, body)

	// Twilio expects an XML (TwiML) response; empty <Response/> means "no reply".
	w.Header().Set("Content-Type", "text/xml")
	fmt.Fprint(w, "<Response/>")
}

// handleRecent returns recent SMS messages, newest first, pruned to 24 h.
func (s *Service) handleRecent(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.pruneOld()
	// Copy to avoid holding lock during JSON encode.
	msgs := make([]SMSMessage, len(s.history))
	copy(msgs, s.history)
	s.mu.Unlock()

	// Reverse: newest first.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}

	writeJSON(w, map[string]any{"messages": msgs, "count": len(msgs)})
}

// handleNumber returns the currently configured virtual phone number and its
// real status. There is no automatic number provisioning yet — see the Twilio
// wiring notes below — so the only way a number becomes active is by setting it
// manually via PUT /api/auth/sms/settings after allocating it with the provider.
//
// Twilio wiring required for automatic provisioning (AUTH-15 follow-up):
//   - Configure TWILIO_ACCOUNT_SID / TWILIO_AUTH_TOKEN (and optionally a
//     subaccount SID) for the box.
//   - Call the Twilio IncomingPhoneNumbers API to buy/allocate an SMS-capable
//     number, then point its SMS webhook at POST /api/auth/sms/webhook
//     (secured with the WebhookToken shared secret).
//   - Persist the allocated E.164 number into Settings.Number.
//
// Until that exists, an unset number is reported as "not_configured" (a clear
// config-required state) rather than a misleading fake-success status.
func (s *Service) handleNumber(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	num := s.settings.Number
	provider := s.settings.Provider
	s.mu.Unlock()

	if num == "" {
		writeJSON(w, map[string]any{
			"number":   "",
			"provider": provider,
			"status":   "not_configured",
			"detail": "No SMS number configured. Automatic provisioning is not implemented; " +
				"allocate an SMS-capable number with your provider (e.g. Twilio), point its " +
				"SMS webhook at POST /api/auth/sms/webhook, then set it via PUT /api/auth/sms/settings.",
		})
		return
	}

	// A number is configured: inbound OTP receipt works via the webhook.
	writeJSON(w, map[string]any{
		"number":   num,
		"provider": provider,
		"status":   "active",
	})
}

// handleSettings reads/updates auto-fill preferences and retention policy.
// Number provisioning (Twilio sub-account allocation) is stubbed — use PUT to
// set the number field after provisioning it externally.
func (s *Service) handleSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Number         *string `json:"number"`
		Provider       *string `json:"provider"`
		AutoFill       *bool   `json:"auto_fill"`
		RetentionHours *int    `json:"retention_hours"`
		WebhookToken   *string `json:"webhook_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON body")
		return
	}

	s.mu.Lock()
	if req.Number != nil {
		s.settings.Number = *req.Number
	}
	if req.Provider != nil {
		s.settings.Provider = *req.Provider
	}
	if req.AutoFill != nil {
		s.settings.AutoFill = *req.AutoFill
	}
	if req.RetentionHours != nil && *req.RetentionHours > 0 {
		s.settings.RetentionHours = *req.RetentionHours
	}
	if req.WebhookToken != nil {
		s.settings.WebhookToken = *req.WebhookToken
	}
	s.save()
	cfg := s.settings
	s.mu.Unlock()

	writeJSON(w, cfg)
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
