// verify.go — vulos.org email verification for peering identities (PEER-10).
//
// Implements a self-contained email-address verification flow tied to the local
// Vula identity.  No external HTTP calls are made in v1 — in non-prod the
// one-time token is logged to stderr so operators can observe it during
// development and testing. In VULOS_ENV=prod the token is REDACTED from logs
// (it is a live credential: a log reader could hijack the email→identity
// binding by replaying it to /verify/email/confirm).
//
// Storage layout:
//
//	~/.vulos/peering/verify/email.json   — VerifyEmailState (one record per identity)
//
// Routes (register with RegisterEmailVerifyHandlers):
//
//	POST /verify/email/start           → issue 24-hour single-use token; log to stderr; 204
//	GET  /verify/email/confirm?token=  → consume token, bind email to identity; 200
//	GET  /verify/email/status          → {email, bound, last_attempt}
//
// Rate limiting: at most 1 resend per minute per service instance (in-memory).
package peering

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"vulos/backend/services/env"
)

// ─── Storage types ─────────────────────────────────────────────────────────────

// emailVerifyToken is a single pending verification token.
type emailVerifyToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used"`
}

// VerifyEmailState is the persisted state for one identity's email verification.
type VerifyEmailState struct {
	// Email is the address being (or already) verified.
	Email string `json:"email"`
	// Bound is true once a valid token has been consumed.
	Bound bool `json:"bound"`
	// LastAttempt is the RFC 3339 timestamp of the most recent start request.
	LastAttempt string `json:"last_attempt,omitempty"`
	// Pending is the outstanding (not yet consumed) token, if any.
	Pending *emailVerifyToken `json:"pending,omitempty"`
}

// ─── EmailVerifyService ────────────────────────────────────────────────────────

// EmailVerifyService manages email-verification state for a single Vula identity.
// The zero value is not usable; obtain one via NewEmailVerifyService.
type EmailVerifyService struct {
	mu      sync.Mutex
	state   VerifyEmailState
	dir     string    // directory that holds email.json
	limiter time.Time // wall-clock time after which another start is allowed
}

const (
	emailVerifyStateFile  = "email.json"
	emailVerifyTokenTTL   = 24 * time.Hour
	emailVerifyRateWindow = time.Minute
)

// NewEmailVerifyService constructs an EmailVerifyService that persists state
// under dir (typically ~/.vulos/peering/verify/).  Any pre-existing state is
// loaded on construction; errors are non-fatal (fresh state is used instead).
func NewEmailVerifyService(dir string) *EmailVerifyService {
	svc := &EmailVerifyService{dir: dir}
	_ = svc.load() // best-effort; ignore missing file
	return svc
}

// load reads the persisted state from disk.  Must be called with mu held or
// during construction (before the service is shared).
func (s *EmailVerifyService) load() error {
	data, err := os.ReadFile(filepath.Join(s.dir, emailVerifyStateFile))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &s.state)
}

// save writes the current state to disk.  Caller must hold mu.
func (s *EmailVerifyService) save() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("verify: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(&s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("verify: marshal: %w", err)
	}
	return os.WriteFile(filepath.Join(s.dir, emailVerifyStateFile), data, 0o600)
}

// Start issues a new single-use verification token for addr.  It:
//  1. Enforces the rate-limit window (1 request per minute).
//  2. Generates a UUID v4 token with a 24-hour expiry.
//  3. Logs "[peering] email verification token for <addr>: <token>" to stderr.
//  4. Persists the state and returns the token string.
//
// The caller is responsible for the HTTP response; Start returns only the
// token and any storage error.
func (s *EmailVerifyService) Start(addr string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	// Rate-limit: reject if last attempt was within the window.
	if now.Before(s.limiter) {
		return "", fmt.Errorf("verify: rate limited; retry after %s", s.limiter.UTC().Format(time.RFC3339))
	}

	token := uuid.New().String()
	s.state.Email = addr
	s.state.LastAttempt = now.UTC().Format(time.RFC3339)
	s.state.Pending = &emailVerifyToken{
		Token:     token,
		ExpiresAt: now.Add(emailVerifyTokenTTL),
		Used:      false,
	}
	// Update the rate-limit window regardless of save success so the clock
	// ticks from the moment the token was issued, not from a successful write.
	s.limiter = now.Add(emailVerifyRateWindow)

	if err := s.save(); err != nil {
		return "", err
	}

	// SECURITY: the token is a live credential — anyone who reads it can hijack
	// the email→identity binding by hitting /verify/email/confirm?token=. Never
	// log it verbatim in production. Off-prod (local/dev) we still print it so
	// operators can complete the flow without a real mail sender wired up.
	if activeEnv, _ := env.Parse(""); activeEnv.IsProd() {
		fmt.Fprintf(os.Stderr, "[peering] email verification token issued for %s (redacted; %d chars)\n", addr, len(token))
	} else {
		fmt.Fprintf(os.Stderr, "[peering] email verification token for %s: %s\n", addr, token)
	}
	return token, nil
}

// Confirm consumes token and, if valid, marks the email as bound.
// Returns a non-nil error (with a descriptive code) when the token is expired
// or already used — callers should map that to HTTP 410 Gone.
func (s *EmailVerifyService) Confirm(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	p := s.state.Pending
	if p == nil || p.Token != token {
		return errVerifyGone("token not found or already consumed")
	}
	if p.Used {
		return errVerifyGone("token already used")
	}
	if time.Now().After(p.ExpiresAt) {
		return errVerifyGone("token expired")
	}

	p.Used = true
	s.state.Bound = true
	return s.save()
}

// Status returns a snapshot of the current verification state.
func (s *EmailVerifyService) Status() VerifyEmailState {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := s.state
	// Do not expose the raw token in the status response.
	cp.Pending = nil
	return cp
}

// ─── Sentinel error ────────────────────────────────────────────────────────────

// verifyGoneError signals that the requested token is expired or already used.
// HTTP handlers should map this to 410 Gone.
type verifyGoneError struct{ msg string }

func (e *verifyGoneError) Error() string { return e.msg }

// errVerifyGone constructs a verifyGoneError.
func errVerifyGone(msg string) error { return &verifyGoneError{msg: msg} }

// IsVerifyGone reports whether err is a verifyGoneError.
func IsVerifyGone(err error) bool {
	_, ok := err.(*verifyGoneError)
	return ok
}

// ─── HTTP handlers ─────────────────────────────────────────────────────────────

// RegisterEmailVerifyHandlers wires the three email-verification endpoints onto mux:
//
//	POST /verify/email/start           → issue token; 204 No Content
//	GET  /verify/email/confirm?token=  → consume token; 200 JSON
//	GET  /verify/email/status          → state JSON; 200
func RegisterEmailVerifyHandlers(mux *http.ServeMux, svc *EmailVerifyService) {
	mux.HandleFunc("POST /verify/email/start", svc.handleStart)
	mux.HandleFunc("GET /verify/email/confirm", svc.handleConfirm)
	mux.HandleFunc("GET /verify/email/status", svc.handleStatus)
}

// ─── Backward-compatibility shims for cmd/server/main.go ──────────────────────
//
// The server wiring pre-dates PEER-10 and uses the names VerifyService,
// VerifyNewService, and RegisterVerifyHandlers.  These shims keep main.go
// compiling without modification while the real work is done by
// EmailVerifyService above.

// VerifyService is a thin wrapper that satisfies the main.go call-sites.
// It delegates to an EmailVerifyService stored under dir/../../verify/.
type VerifyService struct {
	inner *EmailVerifyService
}

// VerifyNewService constructs a VerifyService.  dir is the identity directory
// (e.g. ~/.vulos/peering/identity); the email-verify state is stored one level
// up under ~/.vulos/peering/verify/.
func VerifyNewService(dir string) (*VerifyService, error) {
	verifyDir := filepath.Join(dir, "..", "verify")
	return &VerifyService{inner: NewEmailVerifyService(verifyDir)}, nil
}

// RegisterVerifyHandlers wires the PEER-10 email-verification routes onto mux
// under the paths defined by RegisterEmailVerifyHandlers.
func RegisterVerifyHandlers(mux *http.ServeMux, svc *VerifyService) {
	RegisterEmailVerifyHandlers(mux, svc.inner)
}

// handleStart handles POST /verify/email/start.
// Expected JSON body: {"email":"user@example.com"}
func (s *EmailVerifyService) handleStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		http.Error(w, `{"error":"email is required"}`, http.StatusBadRequest)
		return
	}

	if _, err := s.Start(req.Email); err != nil {
		if IsVerifyGone(err) {
			http.Error(w, err.Error(), http.StatusGone)
			return
		}
		// Rate-limited responses use 429.
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleConfirm handles GET /verify/email/confirm?token=<uuid>.
func (s *EmailVerifyService) handleConfirm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, `{"error":"token query parameter is required"}`, http.StatusBadRequest)
		return
	}

	if err := s.Confirm(token); err != nil {
		if IsVerifyGone(err) {
			http.Error(w, err.Error(), http.StatusGone)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	st := s.Status()
	json.NewEncoder(w).Encode(st)
}

// handleStatus handles GET /verify/email/status.
func (s *EmailVerifyService) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.Status())
}
