// handlers.go — HTTP handlers for the identity routes (IDENTITY-04).
//
// Registration follows the repo convention: expose a top-level
// RegisterHandlers(mux, svc, resolver) function so the calling
// integration pass (cmd/server/main.go) can wire the routes without
// importing any unrelated packages.
//
// Routes registered:
//
//	POST  /api/identity/send                  – send a mail message
//	GET   /api/identity/mailbox               – paginated mailbox list
//	GET   /api/identity/mailbox/{id}          – fetch + decrypt single message
//	PATCH /api/identity/mailbox/{id}          – mark read / archived / deleted
//	GET   /api/identity/identity              – current address + public key
//	POST  /api/identity/identity/rotate       – re-generate keypair
//
// All routes require a local OS session.  In this implementation the
// session check is a stub that accepts a non-empty X-OS-Session header;
// the real auth middleware is wired by cmd/server/main.go in a later pass.
//
// Rate limiting: POST /api/identity/send is protected by a per-account
// token-bucket rate limiter.  Defaults: 20 sends/minute, 200 sends/day.
// Configurable via IDENTITY_RATE_PER_MIN and IDENTITY_RATE_PER_DAY env vars.
// Returns 429 Too Many Requests with a Retry-After header when exceeded.
package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ─── Rate limiting ────────────────────────────────────────────────────────────

// sendBucket holds per-account token state for sendRateLimiter.
type sendBucket struct {
	tokens      [3]int64  // [minTokens, dayTokens, lastRefillNano]
	lastAccess  time.Time // wall-clock time of last allow() call
}

// sendRateLimiter implements a dual token-bucket rate limiter for the send
// endpoint: one per-minute bucket and one per-day bucket.
//
// The buckets refill at a constant rate (per-minute: capacity/60s,
// per-day: capacity/86400s).  When either bucket is empty the request is
// rejected with 429 and a Retry-After header.
//
// Defaults are read from IDENTITY_RATE_PER_MIN (default 20) and
// IDENTITY_RATE_PER_DAY (default 200) at first use; they may be overridden
// in tests by setting the fields directly.
//
// A background goroutine (started by newSendRateLimiter) sweeps the buckets
// map every 5 minutes and evicts entries idle for more than 24 hours.
// The goroutine terminates when the supplied context is cancelled.
type sendRateLimiter struct {
	mu sync.Mutex

	perMin int // token capacity per minute per account (default 20)
	perDay int // token capacity per day per account   (default 200)

	// buckets maps accountKey → per-account token state.
	buckets map[string]*sendBucket

	// now is a clock override used in tests; nil means time.Now.
	now func() time.Time

	// sweepInterval and idleThreshold can be overridden in tests.
	sweepInterval time.Duration
	idleThreshold time.Duration
}

// newSendRateLimiter builds a limiter whose capacity is read from env vars and
// starts the background eviction goroutine.  The goroutine exits when ctx is
// cancelled.
func newSendRateLimiter(ctx context.Context) *sendRateLimiter {
	perMin := envInt("IDENTITY_RATE_PER_MIN", 20)
	perDay := envInt("IDENTITY_RATE_PER_DAY", 200)
	rl := &sendRateLimiter{
		perMin:        perMin,
		perDay:        perDay,
		buckets:       make(map[string]*sendBucket),
		sweepInterval: 5 * time.Minute,
		idleThreshold: 24 * time.Hour,
	}
	go rl.sweepLoop(ctx)
	return rl
}

// defaultSendRateLimiter builds a limiter using a background context.
// Preserved for callers (RegisterHandlersWithSession) that have no context.
func defaultSendRateLimiter() *sendRateLimiter {
	return newSendRateLimiter(context.Background())
}

// sweepLoop runs in a goroutine and periodically evicts idle buckets.
func (rl *sendRateLimiter) sweepLoop(ctx context.Context) {
	nowFn := rl.now
	if nowFn == nil {
		nowFn = time.Now
	}
	ticker := time.NewTicker(rl.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rl.sweep(nowFn())
		}
	}
}

// sweep deletes bucket entries that have been idle for longer than idleThreshold.
func (rl *sendRateLimiter) sweep(now time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for key, b := range rl.buckets {
		if now.Sub(b.lastAccess) > rl.idleThreshold {
			delete(rl.buckets, key)
		}
	}
}

// allow returns (allowed, retryAfterSeconds).  It is safe for concurrent use.
func (rl *sendRateLimiter) allow(accountKey string) (bool, int) {
	nowFn := rl.now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn()
	nowNano := now.UnixNano()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[accountKey]
	if !ok {
		b = &sendBucket{
			tokens:     [3]int64{int64(rl.perMin), int64(rl.perDay), nowNano},
			lastAccess: now,
		}
		rl.buckets[accountKey] = b
	}

	b.lastAccess = now

	// Refill tokens based on elapsed time.
	elapsed := time.Duration(nowNano - b.tokens[2])
	if elapsed > 0 {
		// Per-minute bucket: refills at perMin tokens per 60s.
		minRefill := int64(float64(elapsed) / float64(time.Minute) * float64(rl.perMin))
		b.tokens[0] += minRefill
		if b.tokens[0] > int64(rl.perMin) {
			b.tokens[0] = int64(rl.perMin)
		}
		// Per-day bucket: refills at perDay tokens per 24h.
		dayRefill := int64(float64(elapsed) / float64(24*time.Hour) * float64(rl.perDay))
		b.tokens[1] += dayRefill
		if b.tokens[1] > int64(rl.perDay) {
			b.tokens[1] = int64(rl.perDay)
		}
		b.tokens[2] = nowNano
	}

	if b.tokens[0] <= 0 {
		// Minute bucket empty: compute seconds until one token refills.
		secsPerToken := int(time.Minute/time.Second) / rl.perMin
		if secsPerToken < 1 {
			secsPerToken = 1
		}
		return false, secsPerToken
	}
	if b.tokens[1] <= 0 {
		// Day bucket empty: compute seconds until one token refills.
		secsPerToken := int((24 * time.Hour) / time.Second / time.Duration(rl.perDay))
		if secsPerToken < 1 {
			secsPerToken = 1
		}
		return false, secsPerToken
	}

	b.tokens[0]--
	b.tokens[1]--
	return true, 0
}

// envInt reads an integer environment variable, returning fallback on error or absence.
func envInt(key string, fallback int) int {
	if s := os.Getenv(key); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			return v
		}
	}
	return fallback
}

// ─── Handler set ─────────────────────────────────────────────────────────────

// SessionValidator resolves a session token (the value of the X-OS-Session
// header) to a live, unrevoked user identity.  Implementations should return
// ok=false on any lookup failure so callers fail closed with HTTP 401.
//
// The identity package keeps this as a small interface so it does not have to
// import services/auth directly (which would create a layering inversion);
// cmd/server wires a thin adapter around auth.Store.ValidateToken.
type SessionValidator interface {
	ValidateOSSession(token string) (userID string, ok bool)
}

// Handlers groups the identity HTTP handler dependencies.
type Handlers struct {
	svc       *Service
	resolver  KeyResolver
	validator SessionValidator // optional; see requireSession for fail-closed semantics
	sendRL    *sendRateLimiter // rate limiter for POST /api/identity/send
}

// RegisterHandlers mounts all identity HTTP routes on mux.
//
//	mux      – the *http.ServeMux to register routes on
//	svc      – the identity Service (must have a non-nil identity)
//	resolver – KeyResolver used by the send handler to look up recipient keys
//
// This entry point is preserved for legacy callers and tests.  It registers a
// nil SessionValidator, which in VULOS_ENV=prod causes every request to be
// rejected with 401 (fail-closed).  Production callers MUST use
// RegisterHandlersWithSession and pass a real validator.
func RegisterHandlers(mux *http.ServeMux, svc *Service, resolver KeyResolver) {
	RegisterHandlersWithSession(mux, svc, resolver, nil)
}

// RegisterHandlersWithSession is the production-grade entry point.  validator
// is consulted by every identity handler before performing any work; in
// VULOS_ENV=prod a nil validator is fatal-on-request (401), so callers must
// supply one.
func RegisterHandlersWithSession(mux *http.ServeMux, svc *Service, resolver KeyResolver, validator SessionValidator) {
	if validator == nil && os.Getenv("VULOS_ENV") == "prod" {
		log.Printf("[identity] WARNING: RegisterHandlersWithSession called with a nil SessionValidator in VULOS_ENV=prod — every request will be rejected with 401")
	}
	h := &Handlers{svc: svc, resolver: resolver, validator: validator, sendRL: defaultSendRateLimiter()}

	mux.HandleFunc("GET /api/identity/check", h.handleCheckAvailability)
	mux.HandleFunc("POST /api/identity/send", h.handleSend)
	mux.HandleFunc("GET /api/identity/mailbox", h.handleListMailbox)
	mux.HandleFunc("GET /api/identity/mailbox/{id}", h.handleGetMessage)
	mux.HandleFunc("PATCH /api/identity/mailbox/{id}", h.handlePatchMessage)
	mux.HandleFunc("GET /api/identity/identity", h.handleGetIdentity)
	mux.HandleFunc("POST /api/identity/identity/rotate", h.handleRotateIdentity)
}

// ─── Session guard ────────────────────────────────────────────────────────────

// requireSession is the per-request session guard.
//
// Behaviour:
//
//	1. If the X-OS-Session header is empty, 401 immediately.
//	2. If a SessionValidator is configured, look up the token; on any failure
//	   (unknown, revoked, expired) 401 with no detail.  On success, attach the
//	   resolved user ID to the request as X-User-ID so downstream handlers can
//	   key off it without re-resolving.
//	3. If no validator is configured: in VULOS_ENV=prod fail closed (401) — the
//	   header alone is not a credential.  In dev/local accept a non-empty
//	   header so existing developer workflows and unit tests keep working.
func (h *Handlers) requireSession(w http.ResponseWriter, r *http.Request) bool {
	token := r.Header.Get("X-OS-Session")
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return false
	}
	if h.validator != nil {
		userID, ok := h.validator.ValidateOSSession(token)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
			return false
		}
		r.Header.Set("X-User-ID", userID)
		return true
	}
	// No validator: header-presence-only is unsafe in prod.
	if os.Getenv("VULOS_ENV") == "prod" {
		log.Printf("[identity] denied request: no SessionValidator wired in VULOS_ENV=prod")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return false
	}
	return true
}

// ─── GET /api/identity/check ──────────────────────────────────────────────────
//
// Proxies to the cloud control plane CheckAvailability call.
// No session is required — this is called during first-boot before any session
// exists.  The cloud is the authoritative source; if unreachable, returns
// {"offline":true} so the UI can fail-open with a soft warning.
//
// Query params:
//   handle — the username part to check (required, e.g. "alice")
//
// Response 200 {"available":true|false, "suggestions":["alice1","alice2"], "offline":false|true}
// Response 400 {"error":"handle invalid: …"} — bad charset

func (h *Handlers) handleCheckAvailability(w http.ResponseWriter, r *http.Request) {
	handle := r.URL.Query().Get("handle")
	if handle == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "handle query param is required"})
		return
	}

	ctx := r.Context()
	result, err := CheckAvailability(ctx, handle, h.svc.client)
	if err != nil {
		// ErrHandleInvalid — bad charset from the client
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// ─── POST /api/identity/send ──────────────────────────────────────────────────

type sendRequest struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func (h *Handlers) handleSend(w http.ResponseWriter, r *http.Request) {
	if !h.requireSession(w, r) {
		return
	}

	// Per-account rate limit (always on; bucket size configurable via env vars).
	accountKey := r.Header.Get("X-User-ID")
	if accountKey == "" {
		accountKey = r.Header.Get("X-OS-Session") // fallback when no validator wired
	}
	if ok, retryAfter := h.sendRL.allow(accountKey); !ok {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfter))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"error":       "rate_limit_exceeded",
			"retry_after": fmt.Sprintf("%d", retryAfter),
		})
		return
	}

	var req sendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.To == "" || req.Subject == "" || req.Body == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "to, subject, and body are required"})
		return
	}

	// Audit log: record BEFORE the mutation (Critical fix #4).
	log.Printf("[audit] actor=%q action=identity.send target=%q", accountKey, req.To)

	if err := h.svc.Send(r.Context(), req.To, req.Subject, req.Body, h.resolver); err != nil {
		log.Printf("[identity] handleSend error: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// ─── GET /api/identity/mailbox ────────────────────────────────────────────────

type mailboxItem struct {
	ID          string `json:"id"`
	FromAddress string `json:"from_address"`
	Subject     string `json:"subject"`
	ReceivedAt  string `json:"received_at"`
	Read        bool   `json:"read"`
	// BodyEncrypted is intentionally omitted from the list view;
	// callers must fetch /mailbox/:id to get the body.
}

type mailboxResponse struct {
	Messages []*mailboxItem `json:"messages"`
	Total    int            `json:"total"`
	Limit    int            `json:"limit"`
	Offset   int            `json:"offset"`
}

func (h *Handlers) handleListMailbox(w http.ResponseWriter, r *http.Request) {
	if !h.requireSession(w, r) {
		return
	}
	q := r.URL.Query()
	limit := queryInt(q.Get("limit"), 50)
	offset := queryInt(q.Get("offset"), 0)

	msgs, total, err := h.svc.store.listMailboxPaged(limit, offset)
	if err != nil {
		log.Printf("[identity] handleListMailbox error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store error"})
		return
	}

	items := make([]*mailboxItem, 0, len(msgs))
	for _, m := range msgs {
		items = append(items, &mailboxItem{
			ID:          m.ID,
			FromAddress: m.FromAddress,
			Subject:     m.Subject,
			ReceivedAt:  m.ReceivedAt.Format("2006-01-02T15:04:05Z"),
			Read:        m.Read,
		})
	}

	writeJSON(w, http.StatusOK, mailboxResponse{
		Messages: items,
		Total:    total,
		Limit:    limit,
		Offset:   offset,
	})
}

// ─── GET /api/identity/mailbox/{id} ───────────────────────────────────────────

type messageResponse struct {
	ID          string `json:"id"`
	FromAddress string `json:"from_address"`
	Subject     string `json:"subject"`
	Body        string `json:"body"`
	ReceivedAt  string `json:"received_at"`
	Read        bool   `json:"read"`
}

func (h *Handlers) handleGetMessage(w http.ResponseWriter, r *http.Request) {
	if !h.requireSession(w, r) {
		return
	}
	id := pathID(r, "/api/identity/mailbox/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing message id"})
		return
	}

	msg, err := h.svc.store.getMailMessage(id)
	if err != nil {
		log.Printf("[identity] handleGetMessage store error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store error"})
		return
	}
	if msg == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "message not found"})
		return
	}

	// The nonce is packed as the first 40 bytes of BodyEncrypted per identity.go.
	// In the Receive path the original envelope nonce was stored; use
	// DecryptMessageBody only if the identity is unlocked.
	body := ""
	if len(msg.BodyEncrypted) >= 40 {
		plain, err := h.svc.DecryptMessageBody(
			msg.BodyEncrypted[40:],
			msg.BodyEncrypted[:40],
		)
		if err != nil {
			// If we can't decrypt (locked identity), return body as empty rather
			// than a hard error — the client can retry after unlock.
			log.Printf("[identity] handleGetMessage decrypt: %v", err)
		} else {
			body = string(plain)
		}
	}

	writeJSON(w, http.StatusOK, messageResponse{
		ID:          msg.ID,
		FromAddress: msg.FromAddress,
		Subject:     msg.Subject,
		Body:        body,
		ReceivedAt:  msg.ReceivedAt.Format("2006-01-02T15:04:05Z"),
		Read:        msg.Read,
	})
}

// ─── PATCH /api/identity/mailbox/{id} ─────────────────────────────────────────

type patchMessageRequest struct {
	Read     *bool `json:"read"`
	Archived *bool `json:"archived"`
	Deleted  *bool `json:"deleted"`
}

func (h *Handlers) handlePatchMessage(w http.ResponseWriter, r *http.Request) {
	if !h.requireSession(w, r) {
		return
	}
	id := pathID(r, "/api/identity/mailbox/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing message id"})
		return
	}

	// accountID is set by requireSession via the validator; fall back to session
	// token in dev/no-validator mode (rate-limiter convention).
	accountID := r.Header.Get("X-User-ID")
	if accountID == "" {
		accountID = r.Header.Get("X-OS-Session")
	}

	var req patchMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}

	fields := map[string]interface{}{}
	if req.Read != nil {
		fields["read"] = boolToInt(*req.Read)
	}
	if req.Archived != nil {
		fields["archived"] = boolToInt(*req.Archived)
	}
	if req.Deleted != nil {
		fields["deleted"] = boolToInt(*req.Deleted)
	}
	if len(fields) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "no fields to patch"})
		return
	}

	// Audit log: record BEFORE the mutation (Critical fix #4).
	log.Printf("[audit] actor=%q action=identity.message.patch target=%q", accountID, id)

	if err := h.svc.store.patchMailMessage(id, accountID, fields); err != nil {
		log.Printf("[identity] handlePatchMessage error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ─── GET /api/identity/identity ───────────────────────────────────────────────

type identityResponse struct {
	Address      string `json:"address"`
	PublicKeyB64 string `json:"public_key_b64"`
}

func (h *Handlers) handleGetIdentity(w http.ResponseWriter, r *http.Request) {
	if !h.requireSession(w, r) {
		return
	}
	if h.svc.identity == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no identity configured"})
		return
	}
	writeJSON(w, http.StatusOK, identityResponse{
		Address:      h.svc.identity.Address,
		PublicKeyB64: h.svc.identity.PublicKeyB64,
	})
}

// ─── POST /api/identity/identity/rotate ──────────────────────────────────────

type rotateRequest struct {
	// Passphrase is used to encrypt the new private key.
	Passphrase string `json:"passphrase"`
}

func (h *Handlers) handleRotateIdentity(w http.ResponseWriter, r *http.Request) {
	if !h.requireSession(w, r) {
		return
	}
	if h.svc.identity == nil {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "no identity to rotate"})
		return
	}

	var req rotateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
		return
	}
	if req.Passphrase == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "passphrase is required"})
		return
	}

	address := h.svc.identity.Address
	newID, err := GenerateIdentity(address, req.Passphrase)
	if err != nil {
		log.Printf("[identity] handleRotateIdentity GenerateIdentity error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "key generation failed"})
		return
	}

	// Persist the new keypair.
	if err := h.svc.store.saveIdentity(newID); err != nil {
		log.Printf("[identity] handleRotateIdentity saveIdentity error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "persist failed"})
		return
	}

	// Swap active identity in the service.
	h.svc.identity = newID

	// Publish new key to relay (best-effort; failure is non-fatal — IDENTITY-05
	// implements the full relay client; here we log and continue).
	if err := publishKeyToRelay(r.Context(), h.svc.relayURL, h.svc.client, address, newID.PublicKeyB64); err != nil {
		log.Printf("[identity] rotate: publish key to relay: %v (non-fatal)", err)
	}

	log.Printf("[identity] rotated identity for %s", address)
	writeJSON(w, http.StatusOK, identityResponse{
		Address:      newID.Address,
		PublicKeyB64: newID.PublicKeyB64,
	})
}

// ─── Relay publish helper ─────────────────────────────────────────────────────

// publishKeyToRelay does a best-effort PUT to the relay key directory.
// Implements the same call as IDENTITY-05 RelayClient.PublishKey but inline
// so IDENTITY-04 has no dep on the (not-yet-implemented) relay package.
func publishKeyToRelay(ctx context.Context, relayURL string, client *http.Client, address, pubKeyB64 string) error {
	url := relayURL + "/keys/" + address
	body := strings.NewReader(`{"public_key_b64":"` + pubKeyB64 + `"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return http.ErrNotSupported // non-fatal; caller logs
	}
	return nil
}

// ─── Utility helpers ──────────────────────────────────────────────────────────

// writeJSON encodes v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[identity] writeJSON encode error: %v", err)
	}
}

// pathID extracts the trailing path component after prefix.
// e.g. pathID(r, "/api/identity/mailbox/") with URL "/api/identity/mailbox/abc123" → "abc123".
func pathID(r *http.Request, prefix string) string {
	path := r.URL.Path
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	return strings.TrimPrefix(path, prefix)
}

// queryInt parses s as an int, returning fallback on error or if s is empty.
func queryInt(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return fallback
	}
	return v
}
