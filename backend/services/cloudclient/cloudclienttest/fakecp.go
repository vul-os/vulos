// Package cloudclienttest provides an in-process fake Vulos Cloud control
// plane for UNIFIED-SIGNIN tests. It faithfully reproduces the CP behaviours
// the OS-side client must survive:
//
//   - CSRF Origin/Referer allowlist on every POST (403 "origin not allowed");
//   - the PoW CaptchaGate on /api/auth/{login,signup} — 403 with
//     {"captcha_needed":"true"} until a VALID X-Vulos-PoW header (challenge
//     issued by GET /api/captcha/challenge, single-use, hashcash-verified) is
//     presented;
//   - POST /api/auth/login with the structured step responses
//     (email_verification_required / totp_required) + vc_session cookies;
//   - POST /api/auth/totp/verify upgrading a partial session;
//   - GET  /api/profile/broker/pubkey serving the broker Ed25519 key;
//   - POST /api/profile/login/issue minting a REAL signed login token in the
//     "base64std(payload).base64std(sig)" wire format with the unified-signin
//     claims (email, name, email_verified, jti, issued_at), enforcing session
//   - ULID ownership, mirroring the relaxed 2FA gate (fresh-session rule is
//     collapsed to "full session" here — freshness is a CP-side unit concern).
//
// NOT a mock that waves requests through: PoW is actually verified bit-for-bit
// against the CP algorithm, and tokens are actually signed.
package cloudclienttest

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Account is a fake CP account.
type Account struct {
	ID            string
	Email         string
	Password      string
	EmailVerified bool
	// TOTPCode, when non-empty, forces the totp_required step; the exact code
	// must then be presented to /api/auth/totp/verify.
	TOTPCode string
}

// FakeCP is the fake control plane. Zero value is not usable — call NewFakeCP.
type FakeCP struct {
	Server *httptest.Server

	// BrokerPriv/BrokerPub sign/verify minted login tokens.
	BrokerPriv ed25519.PrivateKey
	BrokerPub  ed25519.PublicKey

	// AllowedOrigin is the only Origin accepted on POSTs (CSRF check).
	AllowedOrigin string
	// PoWDifficulty is the hashcash difficulty (leading zero bits).
	PoWDifficulty int
	// RequirePoW toggles the CaptchaGate on /api/auth/{login,signup}.
	RequirePoW bool
	// TokenTTL is the minted token TTL (default 120s).
	TokenTTL time.Duration

	mu         sync.Mutex
	accounts   map[string]*Account // email → account
	ulidOwner  map[string]string   // ulid → account id
	challenges map[string]int      // challenge → difficulty (single-use)
	sessions   map[string]string   // session token → account id (full)
	partials   map[string]string   // session token → account id (partial)
	issued     int                 // count of minted tokens
	powSolved  int                 // count of accepted PoW headers
}

// NewFakeCP starts the fake CP. Close it via fc.Server.Close().
func NewFakeCP() *FakeCP {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	fc := &FakeCP{
		BrokerPriv:    priv,
		BrokerPub:     pub,
		AllowedOrigin: "http://localhost:5173",
		PoWDifficulty: 4,
		RequirePoW:    true,
		TokenTTL:      120 * time.Second,
		accounts:      make(map[string]*Account),
		ulidOwner:     make(map[string]string),
		challenges:    make(map[string]int),
		sessions:      make(map[string]string),
		partials:      make(map[string]string),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/captcha/challenge", fc.handleChallenge)
	mux.HandleFunc("POST /api/auth/login", fc.gate(fc.handleLogin, true))
	mux.HandleFunc("POST /api/auth/signup", fc.gate(fc.handleSignup, true))
	mux.HandleFunc("POST /api/auth/totp/verify", fc.gate(fc.handleTOTPVerify, false))
	mux.HandleFunc("GET /api/profile/broker/pubkey", fc.handleBrokerPubkey)
	mux.HandleFunc("POST /api/profile/login/issue", fc.gate(fc.handleIssue, false))
	fc.Server = httptest.NewServer(mux)
	return fc
}

// AddAccount registers an account and returns it.
func (fc *FakeCP) AddAccount(a Account) *Account {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if a.ID == "" {
		a.ID = "acc_" + randHex(8)
	}
	cp := a
	fc.accounts[a.Email] = &cp
	return &cp
}

// BindULID binds a device ULID to an account id (routing binding).
func (fc *FakeCP) BindULID(ulid, accountID string) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.ulidOwner[ulid] = accountID
}

// IssuedTokens returns how many login tokens were minted.
func (fc *FakeCP) IssuedTokens() int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.issued
}

// PoWAccepted returns how many valid PoW headers were consumed.
func (fc *FakeCP) PoWAccepted() int {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return fc.powSolved
}

// URL returns the fake CP base URL.
func (fc *FakeCP) URL() string { return fc.Server.URL }

// ─── Gate: CSRF Origin + PoW ─────────────────────────────────────────────────

func (fc *FakeCP) gate(next http.HandlerFunc, pow bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// CSRF Origin/Referer allowlist — mirrors middleware.CSRFOriginCheck:
		// require at least one of Origin/Referer to be present AND allowed.
		origin := r.Header.Get("Origin")
		referer := r.Header.Get("Referer")
		ok := (origin != "" && strings.TrimRight(origin, "/") == fc.AllowedOrigin) ||
			(referer != "" && strings.HasPrefix(referer, fc.AllowedOrigin))
		if !ok {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin not allowed"})
			return
		}
		if pow && fc.RequirePoW {
			if !fc.verifyPoW(r.Header.Get("X-Vulos-PoW")) {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"error":          "proof-of-work required",
					"captcha_needed": "true",
				})
				return
			}
		}
		next(w, r)
	}
}

func (fc *FakeCP) handleChallenge(w http.ResponseWriter, r *http.Request) {
	challenge := randHex(16)
	fc.mu.Lock()
	fc.challenges[challenge] = fc.PoWDifficulty
	fc.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"algorithm":  "sha256-hashcash",
		"challenge":  challenge,
		"difficulty": fc.PoWDifficulty,
		"route":      r.URL.Query().Get("route"),
	})
}

// verifyPoW consumes and checks an "challenge:nonce" header — the same
// algorithm as ddos.verifyHashcash.
func (fc *FakeCP) verifyPoW(header string) bool {
	parts := strings.SplitN(header, ":", 2)
	if len(parts) != 2 {
		return false
	}
	fc.mu.Lock()
	diff, ok := fc.challenges[parts[0]]
	if ok {
		delete(fc.challenges, parts[0]) // single-use
	}
	fc.mu.Unlock()
	if !ok {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + parts[1]))
	for i := 0; i < diff; i++ {
		if (sum[i/8]>>(7-(i%8)))&1 != 0 {
			return false
		}
	}
	fc.mu.Lock()
	fc.powSolved++
	fc.mu.Unlock()
	return true
}

// ─── Auth handlers ────────────────────────────────────────────────────────────

func (fc *FakeCP) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	fc.mu.Lock()
	acc := fc.accounts[body.Email]
	fc.mu.Unlock()
	if acc == nil || acc.Password != body.Password {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	if !acc.EmailVerified {
		writeJSON(w, http.StatusOK, map[string]string{"step": "email_verification_required"})
		return
	}
	if acc.TOTPCode != "" {
		tok := "partial_" + randHex(12)
		fc.mu.Lock()
		fc.partials[tok] = acc.ID
		fc.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "vc_session", Value: tok, Path: "/"})
		writeJSON(w, http.StatusOK, map[string]string{"step": "totp_required"})
		return
	}
	fc.issueFullSession(w, acc)
}

func (fc *FakeCP) handleTOTPVerify(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("vc_session")
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated or session expired"})
		return
	}
	fc.mu.Lock()
	accID, ok := fc.partials[cookie.Value]
	fc.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated or session expired"})
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	acc := fc.accountByID(accID)
	if acc == nil || body.Code == "" || body.Code != acc.TOTPCode {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid TOTP code or recovery code"})
		return
	}
	fc.mu.Lock()
	delete(fc.partials, cookie.Value)
	fc.mu.Unlock()
	fc.issueFullSession(w, acc)
}

func (fc *FakeCP) handleSignup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Handle   string `json:"handle"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Handle == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "handle is required"})
		return
	}
	if len(body.Password) < 12 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password too short"})
		return
	}
	email := body.Handle + "@vulos.test"
	fc.mu.Lock()
	if _, taken := fc.accounts[email]; taken {
		fc.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "handle already registered"})
		return
	}
	fc.mu.Unlock()
	acc := fc.AddAccount(Account{Email: email, Password: body.Password, EmailVerified: false})
	fc.issueFullSession(w, acc)
}

func (fc *FakeCP) issueFullSession(w http.ResponseWriter, acc *Account) {
	tok := "sess_" + randHex(12)
	fc.mu.Lock()
	fc.sessions[tok] = acc.ID
	fc.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "vc_session", Value: tok, Path: "/"})
	writeJSON(w, http.StatusOK, map[string]any{
		"id":             acc.ID,
		"email":          acc.Email,
		"email_verified": acc.EmailVerified,
	})
}

func (fc *FakeCP) accountByID(id string) *Account {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	for _, a := range fc.accounts {
		if a.ID == id {
			return a
		}
	}
	return nil
}

// ─── Broker handlers ─────────────────────────────────────────────────────────

func (fc *FakeCP) handleBrokerPubkey(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"active": map[string]string{
			"id":         base64.RawURLEncoding.EncodeToString(fc.BrokerPub),
			"public_key": base64.StdEncoding.EncodeToString(fc.BrokerPub),
		},
	})
}

func (fc *FakeCP) handleIssue(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("vc_session")
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}
	fc.mu.Lock()
	accID, ok := fc.sessions[cookie.Value]
	fc.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not authenticated"})
		return
	}
	acc := fc.accountByID(accID)

	var body struct {
		ULID        string `json:"ulid"`
		ProfileName string `json:"profile_name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.ULID == "" || body.ProfileName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ulid and profile_name are required"})
		return
	}
	fc.mu.Lock()
	owner, bound := fc.ulidOwner[body.ULID]
	fc.mu.Unlock()
	if !bound {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "ulid not found"})
		return
	}
	if owner != accID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "ulid not owned by authenticated account"})
		return
	}

	now := time.Now().UTC()
	nonce := randHex(16)
	payload, _ := json.Marshal(map[string]any{
		"account_id":     accID,
		"ulid":           body.ULID,
		"profile_name":   body.ProfileName,
		"expires_at":     now.Add(fc.TokenTTL).Format(time.RFC3339Nano),
		"nonce":          nonce,
		"email":          acc.Email,
		"name":           body.ProfileName,
		"issued_at":      now.Format(time.RFC3339Nano),
		"email_verified": acc.EmailVerified,
		"jti":            nonce,
	})
	sig := ed25519.Sign(fc.BrokerPriv, payload)
	wire := base64.StdEncoding.EncodeToString(payload) + "." + base64.StdEncoding.EncodeToString(sig)

	fc.mu.Lock()
	fc.issued++
	fc.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]string{
		"token":      wire,
		"expires_at": now.Add(fc.TokenTTL).Format(time.RFC3339),
	})
}

// ─── Small helpers ────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("cloudclienttest: rand: %v", err))
	}
	return hex.EncodeToString(b)
}
