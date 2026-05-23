// handlers.go — HTTP handlers for the vumail routes (VUMAIL-04).
//
// Registration follows the repo convention: expose a top-level
// RegisterHandlers(mux, svc, resolver) function so the calling
// integration pass (cmd/server/main.go) can wire the routes without
// importing any unrelated packages.
//
// Routes registered:
//
//	POST  /api/vumail/send                  – send a mail message
//	GET   /api/vumail/mailbox               – paginated mailbox list
//	GET   /api/vumail/mailbox/{id}          – fetch + decrypt single message
//	PATCH /api/vumail/mailbox/{id}          – mark read / archived / deleted
//	GET   /api/vumail/identity              – current address + public key
//	POST  /api/vumail/identity/rotate       – re-generate keypair
//
// All routes require a local OS session.  In this implementation the
// session check is a stub that accepts a non-empty X-OS-Session header;
// the real auth middleware is wired by cmd/server/main.go in a later pass.
package vumail

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// ─── Handler set ─────────────────────────────────────────────────────────────

// Handlers groups the vumail HTTP handler dependencies.
type Handlers struct {
	svc      *Service
	resolver KeyResolver
}

// RegisterHandlers mounts all vumail HTTP routes on mux.
//
//	mux      – the *http.ServeMux to register routes on
//	svc      – the vumail Service (must have a non-nil identity)
//	resolver – KeyResolver used by the send handler to look up recipient keys
func RegisterHandlers(mux *http.ServeMux, svc *Service, resolver KeyResolver) {
	h := &Handlers{svc: svc, resolver: resolver}

	mux.HandleFunc("POST /api/vumail/send", h.handleSend)
	mux.HandleFunc("GET /api/vumail/mailbox", h.handleListMailbox)
	mux.HandleFunc("GET /api/vumail/mailbox/{id}", h.handleGetMessage)
	mux.HandleFunc("PATCH /api/vumail/mailbox/{id}", h.handlePatchMessage)
	mux.HandleFunc("GET /api/vumail/identity", h.handleGetIdentity)
	mux.HandleFunc("POST /api/vumail/identity/rotate", h.handleRotateIdentity)
}

// ─── Session guard ────────────────────────────────────────────────────────────

// requireSession is a thin session guard.  It returns false and writes a 401
// if the request lacks an X-OS-Session header.  The full auth middleware is
// provided by the cmd/server integration pass.
func requireSession(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-OS-Session") == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return false
	}
	return true
}

// ─── POST /api/vumail/send ────────────────────────────────────────────────────

type sendRequest struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func (h *Handlers) handleSend(w http.ResponseWriter, r *http.Request) {
	if !requireSession(w, r) {
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

	if err := h.svc.Send(r.Context(), req.To, req.Subject, req.Body, h.resolver); err != nil {
		log.Printf("[vumail] handleSend error: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// ─── GET /api/vumail/mailbox ──────────────────────────────────────────────────

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
	if !requireSession(w, r) {
		return
	}
	q := r.URL.Query()
	limit := queryInt(q.Get("limit"), 50)
	offset := queryInt(q.Get("offset"), 0)

	msgs, total, err := h.svc.store.listMailboxPaged(limit, offset)
	if err != nil {
		log.Printf("[vumail] handleListMailbox error: %v", err)
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

// ─── GET /api/vumail/mailbox/{id} ─────────────────────────────────────────────

type messageResponse struct {
	ID          string `json:"id"`
	FromAddress string `json:"from_address"`
	Subject     string `json:"subject"`
	Body        string `json:"body"`
	ReceivedAt  string `json:"received_at"`
	Read        bool   `json:"read"`
}

func (h *Handlers) handleGetMessage(w http.ResponseWriter, r *http.Request) {
	if !requireSession(w, r) {
		return
	}
	id := pathID(r, "/api/vumail/mailbox/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing message id"})
		return
	}

	msg, err := h.svc.store.getMailMessage(id)
	if err != nil {
		log.Printf("[vumail] handleGetMessage store error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store error"})
		return
	}
	if msg == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "message not found"})
		return
	}

	// The nonce is packed as the first 40 bytes of BodyEncrypted per vumail.go.
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
			log.Printf("[vumail] handleGetMessage decrypt: %v", err)
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

// ─── PATCH /api/vumail/mailbox/{id} ───────────────────────────────────────────

type patchMessageRequest struct {
	Read     *bool `json:"read"`
	Archived *bool `json:"archived"`
	Deleted  *bool `json:"deleted"`
}

func (h *Handlers) handlePatchMessage(w http.ResponseWriter, r *http.Request) {
	if !requireSession(w, r) {
		return
	}
	id := pathID(r, "/api/vumail/mailbox/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing message id"})
		return
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

	if err := h.svc.store.patchMailMessage(id, fields); err != nil {
		log.Printf("[vumail] handlePatchMessage error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ─── GET /api/vumail/identity ─────────────────────────────────────────────────

type identityResponse struct {
	Address      string `json:"address"`
	PublicKeyB64 string `json:"public_key_b64"`
}

func (h *Handlers) handleGetIdentity(w http.ResponseWriter, r *http.Request) {
	if !requireSession(w, r) {
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

// ─── POST /api/vumail/identity/rotate ────────────────────────────────────────

type rotateRequest struct {
	// Passphrase is used to encrypt the new private key.
	Passphrase string `json:"passphrase"`
}

func (h *Handlers) handleRotateIdentity(w http.ResponseWriter, r *http.Request) {
	if !requireSession(w, r) {
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
		log.Printf("[vumail] handleRotateIdentity GenerateIdentity error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "key generation failed"})
		return
	}

	// Persist the new keypair.
	if err := h.svc.store.saveIdentity(newID); err != nil {
		log.Printf("[vumail] handleRotateIdentity saveIdentity error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "persist failed"})
		return
	}

	// Swap active identity in the service.
	h.svc.identity = newID

	// Publish new key to relay (best-effort; failure is non-fatal — VUMAIL-05
	// implements the full relay client; here we log and continue).
	if err := publishKeyToRelay(r.Context(), h.svc.relayURL, h.svc.client, address, newID.PublicKeyB64); err != nil {
		log.Printf("[vumail] rotate: publish key to relay: %v (non-fatal)", err)
	}

	log.Printf("[vumail] rotated identity for %s", address)
	writeJSON(w, http.StatusOK, identityResponse{
		Address:      newID.Address,
		PublicKeyB64: newID.PublicKeyB64,
	})
}

// ─── Relay publish helper ─────────────────────────────────────────────────────

// publishKeyToRelay does a best-effort PUT to the relay key directory.
// Implements the same call as VUMAIL-05 RelayClient.PublishKey but inline
// so VUMAIL-04 has no dep on the (not-yet-implemented) relay package.
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
		log.Printf("[vumail] writeJSON encode error: %v", err)
	}
}

// pathID extracts the trailing path component after prefix.
// e.g. pathID(r, "/api/vumail/mailbox/") with URL "/api/vumail/mailbox/abc123" → "abc123".
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
