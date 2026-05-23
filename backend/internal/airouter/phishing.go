package airouter

// phishing.go — MAIL-AI: phishing classification endpoint.
//
// Adds:
//   POST /api/airouter/mail/phishing-classify
//
// Gate: same accounts.ai_mail_enabled flag as the other mail LLM endpoints.
// Returns 403 {"error":"ai_mail_disabled",...} if not opted in.
//
// Wallet: routes through h.router.Route() — same path as smart-compose/summarize.
//
// Privacy: the LLM prompt explicitly instructs no storage of message content;
// only token count + verdict are written to ai_mail_token_log.
//
// The prompt is embedded from prompts/phishing.txt via go:embed.

import (
	"context"
	"encoding/json"
	_ "embed"
	"fmt"
	"net/http"
	"strings"
	"time"
)

//go:embed prompts/phishing.txt
var phishingPrompt string

// PhishingHandler serves POST /api/airouter/mail/phishing-classify.
type PhishingHandler struct {
	router *Router
	store  *Store
}

// NewPhishingHandler creates a PhishingHandler.
func NewPhishingHandler(router *Router, store *Store) *PhishingHandler {
	return &PhishingHandler{router: router, store: store}
}

// RegisterPhishingHandlers mounts the phishing classify route onto mux.
// Call alongside RegisterMailHandlers at startup.
func RegisterPhishingHandlers(mux *http.ServeMux, router *Router, store *Store) {
	h := NewPhishingHandler(router, store)
	mux.HandleFunc("POST /api/airouter/mail/phishing-classify", h.handlePhishingClassify)
}

// phishingClassifyRequest is the body accepted by the endpoint.
type phishingClassifyRequest struct {
	MessageHeaders string   `json:"message_headers"`
	MessageBody    string   `json:"message_body"`
	URLs           []string `json:"urls"`
}

// phishingClassifyResponse is the JSON returned on success.
type phishingClassifyResponse struct {
	Verdict            string   `json:"verdict"`             // "phishing" | "suspicious" | "clean"
	Confidence         float64  `json:"confidence"`          // 0..1
	Reasons            []string `json:"reasons"`             // short classifier labels
	SuspiciousElements []string `json:"suspicious_elements"` // observed signals
}

// handlePhishingClassify serves POST /api/airouter/mail/phishing-classify.
//
// It:
//  1. Checks the opt-in gate (checkOptIn helper from mail_handler.go).
//  2. Decodes the request body.
//  3. Builds a user content string from headers + body + URLs (NO raw content
//     is stored beyond this request lifetime).
//  4. Calls the LLM via h.router.Route() (same wallet path as other endpoints).
//  5. Parses the LLM JSON response and returns the verdict.
//  6. Logs token count + verdict to ai_mail_token_log (no message content).
func (h *PhishingHandler) handlePhishingClassify(w http.ResponseWriter, r *http.Request) {
	accountID := accountKey(r)
	mh := &MailHandler{router: h.router, store: h.store}
	if !mh.checkOptIn(w, accountID) {
		return
	}

	var req phishingClassifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid_json: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Build user content: structured summary for the LLM, not raw dump.
	// We include headers + a truncated body + URL list. Max 4000 chars total
	// to keep token cost bounded.
	userContent := buildPhishingUserContent(req)

	model := mh.activeModel()
	completion, tokens, err := mh.callLLM(r.Context(), model, phishingPrompt, userContent)
	if err != nil {
		jsonError(w, "llm_error: "+err.Error(), http.StatusBadGateway)
		return
	}

	verdict, parseErr := parsePhishingVerdict(completion)
	if parseErr != nil {
		// LLM returned non-JSON; return a degraded suspicious verdict rather
		// than surfacing a 500 to the caller.
		verdict = &phishingClassifyResponse{
			Verdict:    "suspicious",
			Confidence: 0.0,
			Reasons:    []string{"llm_parse_error"},
		}
	}

	// Audit log: token count + verdict only. No message content.
	auditEntry := fmt.Sprintf("verdict=%s confidence=%.2f", verdict.Verdict, verdict.Confidence)
	go h.logPhishingUsage(accountID, model, tokens, auditEntry)

	jsonOK(w, verdict)
}

// buildPhishingUserContent assembles a compact representation of the message
// for the LLM. No raw message body is stored beyond the HTTP request lifetime.
func buildPhishingUserContent(req phishingClassifyRequest) string {
	var sb strings.Builder
	if req.MessageHeaders != "" {
		hdr := req.MessageHeaders
		if len(hdr) > 1500 {
			hdr = hdr[:1500] + "...[truncated]"
		}
		sb.WriteString("HEADERS:\n")
		sb.WriteString(hdr)
		sb.WriteString("\n\n")
	}
	if req.MessageBody != "" {
		body := req.MessageBody
		if len(body) > 2000 {
			body = body[:2000] + "...[truncated]"
		}
		sb.WriteString("BODY:\n")
		sb.WriteString(body)
		sb.WriteString("\n\n")
	}
	if len(req.URLs) > 0 {
		sb.WriteString("URLS:\n")
		for i, u := range req.URLs {
			if i >= 10 {
				sb.WriteString(fmt.Sprintf("...[%d more URLs]\n", len(req.URLs)-10))
				break
			}
			sb.WriteString(u)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// parsePhishingVerdict attempts to parse the LLM completion as a
// phishingClassifyResponse JSON object. Returns an error on parse failure.
func parsePhishingVerdict(completion string) (*phishingClassifyResponse, error) {
	// Trim whitespace and any surrounding markdown code fences.
	s := strings.TrimSpace(completion)
	if idx := strings.Index(s, "{"); idx > 0 {
		s = s[idx:]
	}
	if idx := strings.LastIndex(s, "}"); idx >= 0 && idx < len(s)-1 {
		s = s[:idx+1]
	}

	var resp phishingClassifyResponse
	if err := json.Unmarshal([]byte(s), &resp); err != nil {
		return nil, fmt.Errorf("phishing: parse verdict JSON: %w", err)
	}

	// Validate verdict field.
	switch resp.Verdict {
	case "phishing", "suspicious", "clean":
		// OK.
	default:
		return nil, fmt.Errorf("phishing: unknown verdict %q", resp.Verdict)
	}

	// Clamp confidence to [0, 1].
	if resp.Confidence < 0 {
		resp.Confidence = 0
	}
	if resp.Confidence > 1 {
		resp.Confidence = 1
	}
	if resp.Reasons == nil {
		resp.Reasons = []string{}
	}
	if resp.SuspiciousElements == nil {
		resp.SuspiciousElements = []string{}
	}

	return &resp, nil
}

// logPhishingUsage writes a non-blocking audit record. Only token count and
// verdict label are stored — no message content.
func (h *PhishingHandler) logPhishingUsage(accountID, model string, tokens int, auditNote string) {
	mh := &MailHandler{router: h.router, store: h.store}
	mh.logTokenUsage(accountID, "phishing-classify", model, tokens)

	// Additional audit row: verdict + confidence (no message content).
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = h.store.db.Exec(
		`INSERT INTO ai_mail_token_log(account_id, endpoint, model_slug, token_count, created_at)
		 VALUES(?,?,?,?,?)`,
		accountID, "phishing-classify/audit:"+auditNote, model, 0, now,
	)
}

// phishingClassifyInternalCall performs a server-side phishing classification
// call without HTTP (used by gray-zone router or internal callers). It reuses
// the MailHandler LLM path directly.
func (h *PhishingHandler) phishingClassifyInternalCall(
	ctx context.Context,
	accountID string,
	headers, body string,
	urls []string,
) (*phishingClassifyResponse, error) {
	mh := &MailHandler{router: h.router, store: h.store}

	// Check opt-in without HTTP.
	enabled, err := mh.ensureAccount(accountID)
	if err != nil {
		return nil, fmt.Errorf("phishing internal: account check: %w", err)
	}
	if !enabled {
		return nil, fmt.Errorf("phishing internal: ai_mail_disabled")
	}

	req := phishingClassifyRequest{
		MessageHeaders: headers,
		MessageBody:    body,
		URLs:           urls,
	}
	userContent := buildPhishingUserContent(req)
	model := mh.activeModel()
	completion, tokens, err := mh.callLLM(ctx, model, phishingPrompt, userContent)
	if err != nil {
		return nil, fmt.Errorf("phishing internal: llm: %w", err)
	}
	go h.logPhishingUsage(accountID, model, tokens, "internal")

	return parsePhishingVerdict(completion)
}
