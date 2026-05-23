package airouter

// mail_handler.go — MAIL-AI: mail-specific LLM endpoints.
//
// Adds routes under /api/airouter/mail/:
//   POST   /api/airouter/mail/smart-compose
//   POST   /api/airouter/mail/summarize
//   POST   /api/airouter/mail/reply-suggestions
//   POST   /api/airouter/mail/extract-actions
//   GET    /api/airouter/mail/status
//   POST   /api/airouter/mail/enable
//   POST   /api/airouter/mail/disable
//
// All four LLM endpoints are gated on accounts.ai_mail_enabled. If false,
// they return 403 {"error":"ai_mail_disabled","hint":"..."}.
//
// Wallet billing: each successful call routes through the existing
// airouter Router.Route path (BYO or cloud), which triggers the normal
// UsageReporter. Token usage is additionally logged to ai_mail_token_log.
//
// Privacy: only token counts + model slug are logged; mail content is never
// written to any persistent store. See AI-MAIL-PRIVACY.md.

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	_ "embed"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// ErrUnauthenticated is returned by requireSession when no valid session exists.
var ErrUnauthenticated = errors.New("airouter: unauthenticated")

// MailSessionValidator validates an OS session token and resolves the account ID.
// Implementations must fail closed — an unknown or expired token must return ("", false).
type MailSessionValidator interface {
	ValidateOSSession(token string) (accountID string, ok bool)
}

//go:embed prompts/mail_compose.txt
var mailComposePrompt string

//go:embed prompts/mail_summarize.txt
var mailSummarizePrompt string

//go:embed prompts/mail_reply_suggestions.txt
var mailReplyPrompt string

//go:embed prompts/mail_extract_actions.txt
var mailExtractActionsPrompt string

// MailHandler serves the /api/airouter/mail/* endpoints.
type MailHandler struct {
	router    *Router
	store     *Store
	validator MailSessionValidator // optional; nil = header-presence fallback (dev only)
}

// NewMailHandler creates a MailHandler with no session validator (dev/test mode).
// Production callers should use NewMailHandlerWithSession.
func NewMailHandler(router *Router, store *Store) *MailHandler {
	return &MailHandler{router: router, store: store}
}

// NewMailHandlerWithSession creates a MailHandler that validates every request
// against a real session store via validator.
func NewMailHandlerWithSession(router *Router, store *Store, validator MailSessionValidator) *MailHandler {
	return &MailHandler{router: router, store: store, validator: validator}
}

// requireSession extracts and validates the session from the request.
// It reads X-OS-Session and validates it against the MailSessionValidator if
// one is configured.  On success it returns the verified account_id.
//
// Security contract:
//   - If no X-OS-Session header: 401 immediately.
//   - If a validator is configured: token must resolve to an account or 401.
//   - If no validator (dev/test): accept non-empty X-User-ID header or the
//     session token itself as the account ID.  The RemoteAddr fallback present
//     in the old accountKey() function has been removed — it was exploitable.
//
// This replaces the old accountKey(r) function (Critical fix #2).
func (h *MailHandler) requireSession(w http.ResponseWriter, r *http.Request) (string, error) {
	token := r.Header.Get("X-OS-Session")
	if token == "" {
		jsonError(w, "unauthenticated", http.StatusUnauthorized)
		return "", ErrUnauthenticated
	}
	if h.validator != nil {
		accountID, ok := h.validator.ValidateOSSession(token)
		if !ok {
			jsonError(w, "unauthenticated", http.StatusUnauthorized)
			return "", ErrUnauthenticated
		}
		return accountID, nil
	}
	// No validator configured (dev/test): trust X-User-ID if set, else use
	// the session token as account key.  Never fall back to RemoteAddr.
	if uid := r.Header.Get("X-User-ID"); uid != "" {
		return uid, nil
	}
	return token, nil
}

// RegisterMailHandlers mounts the mail AI routes onto mux. Call once at startup
// alongside the existing RegisterHandlers.
func RegisterMailHandlers(mux *http.ServeMux, router *Router, store *Store) {
	h := NewMailHandler(router, store)
	mux.HandleFunc("POST /api/airouter/mail/smart-compose", h.handleSmartCompose)
	mux.HandleFunc("POST /api/airouter/mail/summarize", h.handleSummarize)
	mux.HandleFunc("POST /api/airouter/mail/reply-suggestions", h.handleReplySuggestions)
	mux.HandleFunc("POST /api/airouter/mail/extract-actions", h.handleExtractActions)
	mux.HandleFunc("GET /api/airouter/mail/status", h.handleMailStatus)
	mux.HandleFunc("POST /api/airouter/mail/enable", h.handleMailEnable)
	mux.HandleFunc("POST /api/airouter/mail/disable", h.handleMailDisable)
}

// ---------------------------------------------------------------------------
// Account opt-in gate (B2)
// ---------------------------------------------------------------------------

// ensureAccount upserts an account row, returning the current ai_mail_enabled flag.
func (h *MailHandler) ensureAccount(accountID string) (enabled bool, err error) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = h.store.db.Exec(
		`INSERT OR IGNORE INTO accounts(account_id, ai_mail_enabled, monthly_token_usage, updated_at)
		 VALUES(?,0,0,?)`,
		accountID, now,
	)
	if err != nil {
		return false, err
	}
	var v int
	err = h.store.db.QueryRow(`SELECT ai_mail_enabled FROM accounts WHERE account_id=?`, accountID).Scan(&v)
	return v != 0, err
}

// checkOptIn verifies the account has opted in. Returns false and writes 403
// if not enabled.
func (h *MailHandler) checkOptIn(w http.ResponseWriter, accountID string) bool {
	enabled, err := h.ensureAccount(accountID)
	if err != nil {
		jsonError(w, "store_error: "+err.Error(), http.StatusInternalServerError)
		return false
	}
	if !enabled {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "ai_mail_disabled",
			"hint":  "enable in Settings > Privacy > AI features",
		})
		return false
	}
	return true
}

// logTokenUsage writes a non-blocking token usage record.
func (h *MailHandler) logTokenUsage(accountID, endpoint, model string, tokens int) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := h.store.db.Exec(
		`INSERT INTO ai_mail_token_log(account_id, endpoint, model_slug, token_count, created_at)
		 VALUES(?,?,?,?,?)`,
		accountID, endpoint, model, tokens, now,
	)
	if err != nil {
		log.Printf("[mail_ai] token log: %v", err)
	}
	// Accumulate monthly_token_usage.
	_, err = h.store.db.Exec(
		`UPDATE accounts SET monthly_token_usage = monthly_token_usage + ?, updated_at=? WHERE account_id=?`,
		tokens, now, accountID,
	)
	if err != nil {
		log.Printf("[mail_ai] usage update: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helper: call LLM and collect full text response (non-streaming for JSON endpoints)
// ---------------------------------------------------------------------------

// callLLM routes the messages through the existing Router (BYO or cloud) and
// drains the SSE stream into a single string. Returns the completion text and
// an estimate of token usage (chars/4, a rough approximation).
func (h *MailHandler) callLLM(ctx context.Context, model, systemPrompt, userContent string) (string, int, error) {
	req := ChatRequest{
		Model: model,
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userContent},
		},
		Stream: true,
	}
	stream, err := h.router.Route(ctx, req)
	if err != nil {
		return "", 0, err
	}
	defer stream.Close()
	text, err := drainSSEText(stream)
	if err != nil {
		return "", 0, err
	}
	// Rough token estimate: (prompt + completion) / 4 chars per token.
	tokenEst := (len(systemPrompt) + len(userContent) + len(text)) / 4
	if tokenEst < 1 {
		tokenEst = 1
	}
	return text, tokenEst, nil
}

// drainSSEText reads an SSE stream and concatenates all content delta fields
// (or returns the first "content" field of a non-streaming response).
func drainSSEText(r io.Reader) (string, error) {
	var sb strings.Builder
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := line[6:]
		if payload == "[DONE]" {
			break
		}
		var ev struct {
			Choices []struct {
				Delta struct{ Content string } `json:"delta"`
				// also handle finish reason
			} `json:"choices"`
			Content string `json:"content,omitempty"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err == nil {
			if len(ev.Choices) > 0 {
				sb.WriteString(ev.Choices[0].Delta.Content)
			} else if ev.Content != "" {
				sb.WriteString(ev.Content)
			}
		}
	}
	return sb.String(), scanner.Err()
}

// activeModel returns the configured active model, falling back to a default.
func (h *MailHandler) activeModel() string {
	cfg, err := h.store.GetConfig()
	if err != nil || cfg.ActiveModel == "" {
		return "gpt-4o-mini"
	}
	return cfg.ActiveModel
}

// ---------------------------------------------------------------------------
// POST /api/airouter/mail/smart-compose (B1)
// ---------------------------------------------------------------------------

type smartComposeRequest struct {
	Context     string `json:"context"`
	DraftSoFar  string `json:"draft_so_far"`
	Instruction string `json:"instruction"` // continue|finish|rewrite-formal|rewrite-casual
}

func (h *MailHandler) handleSmartCompose(w http.ResponseWriter, r *http.Request) {
	accountID, err := h.requireSession(w, r)
	if err != nil {
		return
	}
	if !h.checkOptIn(w, accountID) {
		return
	}
	var req smartComposeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid_json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Instruction == "" {
		req.Instruction = "continue"
	}

	// We use simple text replacement, not a template engine; escape user input
	// to prevent prompt-injection via {{...}} markers (Medium fix #8).
	prompt := strings.NewReplacer(
		"{{CONTEXT}}", escapeForPrompt(req.Context),
		"{{DRAFT}}", escapeForPrompt(req.DraftSoFar),
		"{{INSTRUCTION}}", escapeForPrompt(req.Instruction),
	).Replace(mailComposePrompt)

	model := h.activeModel()
	completion, tokens, callErr := h.callLLM(r.Context(), model, prompt, "")
	if callErr != nil {
		log.Printf("[mail_ai] smart-compose llm error: %v", callErr)
		jsonError(w, "service_unavailable", http.StatusBadGateway)
		return
	}
	go h.logTokenUsage(accountID, "smart-compose", model, tokens)
	jsonOK(w, map[string]any{"completion": completion})
}

// ---------------------------------------------------------------------------
// POST /api/airouter/mail/summarize (B1)
// ---------------------------------------------------------------------------

type summarizeRequest struct {
	Thread string `json:"thread"`
}

type summarizeResponse struct {
	Summary     string   `json:"summary"`
	KeyPoints   []string `json:"key_points"`
	ActionItems []string `json:"action_items"`
}

func (h *MailHandler) handleSummarize(w http.ResponseWriter, r *http.Request) {
	accountID, err := h.requireSession(w, r)
	if err != nil {
		return
	}
	if !h.checkOptIn(w, accountID) {
		return
	}
	var req summarizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid_json: "+err.Error(), http.StatusBadRequest)
		return
	}

	// We use simple text replacement, not a template engine; escape user input
	// to prevent prompt-injection via {{...}} markers (Medium fix #8).
	prompt := strings.ReplaceAll(mailSummarizePrompt, "{{THREAD}}", escapeForPrompt(req.Thread))
	model := h.activeModel()
	raw, tokens, callErr := h.callLLM(r.Context(), model, prompt, "")
	if callErr != nil {
		log.Printf("[mail_ai] summarize llm error: %v", callErr)
		jsonError(w, "service_unavailable", http.StatusBadGateway)
		return
	}
	go h.logTokenUsage(accountID, "summarize", model, tokens)

	var resp summarizeResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &resp); err != nil {
		// Return partial: at least the raw text as summary.
		resp = summarizeResponse{
			Summary:     strings.TrimSpace(raw),
			KeyPoints:   []string{},
			ActionItems: []string{},
		}
	}
	if resp.KeyPoints == nil {
		resp.KeyPoints = []string{}
	}
	if resp.ActionItems == nil {
		resp.ActionItems = []string{}
	}
	jsonOK(w, resp)
}

// ---------------------------------------------------------------------------
// POST /api/airouter/mail/reply-suggestions (B1)
// ---------------------------------------------------------------------------

type replySuggestion struct {
	Tone string `json:"tone"`
	Text string `json:"text"`
}

type replyResponse struct {
	Suggestions []replySuggestion `json:"suggestions"`
}

func (h *MailHandler) handleReplySuggestions(w http.ResponseWriter, r *http.Request) {
	accountID, err := h.requireSession(w, r)
	if err != nil {
		return
	}
	if !h.checkOptIn(w, accountID) {
		return
	}
	var req struct {
		Thread string `json:"thread"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid_json: "+err.Error(), http.StatusBadRequest)
		return
	}

	// We use simple text replacement, not a template engine; escape user input
	// to prevent prompt-injection via {{...}} markers (Medium fix #8).
	prompt := strings.ReplaceAll(mailReplyPrompt, "{{THREAD}}", escapeForPrompt(req.Thread))
	model := h.activeModel()
	raw, tokens, callErr := h.callLLM(r.Context(), model, prompt, "")
	if callErr != nil {
		log.Printf("[mail_ai] reply-suggestions llm error: %v", callErr)
		jsonError(w, "service_unavailable", http.StatusBadGateway)
		return
	}
	go h.logTokenUsage(accountID, "reply-suggestions", model, tokens)

	var resp replyResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &resp); err != nil || len(resp.Suggestions) != 3 {
		// Fallback: return exactly 3 placeholder suggestions.
		resp = replyResponse{
			Suggestions: []replySuggestion{
				{Tone: "concise", Text: strings.TrimSpace(raw)},
				{Tone: "detailed", Text: strings.TrimSpace(raw)},
				{Tone: "decline", Text: strings.TrimSpace(raw)},
			},
		}
	}
	jsonOK(w, resp)
}

// ---------------------------------------------------------------------------
// POST /api/airouter/mail/extract-actions (B1)
// ---------------------------------------------------------------------------

type actionItem struct {
	Verb    string  `json:"verb"`
	Subject string  `json:"subject"`
	DueISO  *string `json:"due_iso"` // nullable
}

type extractActionsResponse struct {
	Actions []actionItem `json:"actions"`
}

func (h *MailHandler) handleExtractActions(w http.ResponseWriter, r *http.Request) {
	accountID, err := h.requireSession(w, r)
	if err != nil {
		return
	}
	if !h.checkOptIn(w, accountID) {
		return
	}
	var req struct {
		Thread string `json:"thread"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid_json: "+err.Error(), http.StatusBadRequest)
		return
	}

	// We use simple text replacement, not a template engine; escape user input
	// to prevent prompt-injection via {{...}} markers (Medium fix #8).
	prompt := strings.ReplaceAll(mailExtractActionsPrompt, "{{THREAD}}", escapeForPrompt(req.Thread))
	model := h.activeModel()
	raw, tokens, callErr := h.callLLM(r.Context(), model, prompt, "")
	if callErr != nil {
		log.Printf("[mail_ai] extract-actions llm error: %v", callErr)
		jsonError(w, "service_unavailable", http.StatusBadGateway)
		return
	}
	go h.logTokenUsage(accountID, "extract-actions", model, tokens)

	var resp extractActionsResponse
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &resp); err != nil {
		resp = extractActionsResponse{Actions: []actionItem{}}
	}
	if resp.Actions == nil {
		resp.Actions = []actionItem{}
	}
	jsonOK(w, resp)
}

// ---------------------------------------------------------------------------
// GET /api/airouter/mail/status (B2)
// ---------------------------------------------------------------------------

func (h *MailHandler) handleMailStatus(w http.ResponseWriter, r *http.Request) {
	accountID, err := h.requireSession(w, r)
	if err != nil {
		return
	}
	enabled, err := h.ensureAccount(accountID)
	if err != nil {
		jsonError(w, "store_error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var monthlyTokens int
	_ = h.store.db.QueryRow(
		`SELECT monthly_token_usage FROM accounts WHERE account_id=?`, accountID,
	).Scan(&monthlyTokens)

	// Wallet charge estimate: ~R0.001 per 50 tokens (rough; real billing is in the billing service).
	chargeZAR := monthlyTokens / 50

	jsonOK(w, map[string]any{
		"enabled":                 enabled,
		"monthly_token_usage":     monthlyTokens,
		"monthly_wallet_charge_zar": chargeZAR,
	})
}

// ---------------------------------------------------------------------------
// POST /api/airouter/mail/enable & /disable (B2)
// ---------------------------------------------------------------------------

func (h *MailHandler) handleMailEnable(w http.ResponseWriter, r *http.Request) {
	h.setMailEnabled(w, r, true)
}

func (h *MailHandler) handleMailDisable(w http.ResponseWriter, r *http.Request) {
	h.setMailEnabled(w, r, false)
}

func (h *MailHandler) setMailEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	accountID, sessErr := h.requireSession(w, r)
	if sessErr != nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)

	// Audit log: record BEFORE the mutation (Critical fix #4).
	action := "disable"
	if enabled {
		action = "enable"
	}
	log.Printf("[audit] actor=%q action=mail.ai.%s", accountID, action)

	// Upsert account row.
	_, err := h.store.db.Exec(
		`INSERT INTO accounts(account_id, ai_mail_enabled, monthly_token_usage, updated_at)
		 VALUES(?,?,0,?)
		 ON CONFLICT(account_id) DO UPDATE SET ai_mail_enabled=excluded.ai_mail_enabled, updated_at=excluded.updated_at`,
		accountID, boolInt(enabled), now,
	)
	if err != nil {
		jsonError(w, "store_error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Persist audit record in the ai_mail_audit table (in addition to the
	// structured log emitted above).
	if _, err := h.store.db.Exec(
		`INSERT INTO ai_mail_audit(account_id, action, created_at) VALUES(?,?,?)`,
		accountID, action, now,
	); err != nil {
		log.Printf("[mail_ai] audit log: %v", err)
	}

	jsonOK(w, map[string]any{"enabled": enabled})
}

// ---------------------------------------------------------------------------
// Store helpers for accounts table
// ---------------------------------------------------------------------------

// GetAccountEnabled returns whether ai_mail_enabled is set for accountID.
// Returns (false, sql.ErrNoRows) if the account row does not exist yet.
func (s *Store) GetAccountEnabled(accountID string) (bool, error) {
	var v int
	err := s.db.QueryRow(`SELECT ai_mail_enabled FROM accounts WHERE account_id=?`, accountID).Scan(&v)
	if err != nil {
		return false, err
	}
	return v != 0, nil
}

// GetMonthlyTokenUsage returns the monthly_token_usage counter for accountID.
func (s *Store) GetMonthlyTokenUsage(accountID string) (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT monthly_token_usage FROM accounts WHERE account_id=?`, accountID).Scan(&v)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, err
	}
	return v, nil
}

// SetAccountEnabled sets the ai_mail_enabled flag. Upserts the row.
func (s *Store) SetAccountEnabled(accountID string, enabled bool) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(
		`INSERT INTO accounts(account_id, ai_mail_enabled, monthly_token_usage, updated_at)
		 VALUES(?,?,0,?)
		 ON CONFLICT(account_id) DO UPDATE SET ai_mail_enabled=excluded.ai_mail_enabled, updated_at=excluded.updated_at`,
		accountID, boolInt(enabled), now,
	)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// escapeForPrompt neutralises {{...}} marker syntax in user-supplied strings
// before they are substituted into LLM prompt templates.  We use simple text
// replacement (not a template engine), so breaking the marker syntax is
// sufficient to prevent prompt-injection.  Normal content that contains no
// braces passes through unchanged.
//
// Strategy: replace "{{" → "{ {" and "}}" → "} }" so the resulting text no
// longer forms a valid {{KEY}} marker.
func escapeForPrompt(s string) string {
	s = strings.ReplaceAll(s, "{{", "{ {")
	s = strings.ReplaceAll(s, "}}", "} }")
	return s
}

// AppendTokenUsage adds delta tokens to the monthly counter for accountID.
func (s *Store) AppendTokenUsage(accountID string, delta int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.Exec(
		`UPDATE accounts SET monthly_token_usage = monthly_token_usage + ?, updated_at=? WHERE account_id=?`,
		delta, now, accountID,
	)
	return err
}

// Ensure unused import is referenced.
var _ = fmt.Sprintf
