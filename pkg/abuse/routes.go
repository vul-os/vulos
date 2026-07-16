package abuse

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
)

// genericMsg is the generic client-facing string used in place of raw
// err.Error() in JSON error bodies. The underlying error is written to the
// structured server-side log via logServerErr; this prevents leaking schema
// details / SQL / PRAGMA strings to a (possibly-compromised) admin session
// while preserving the operator's ability to diagnose. See FIX-ADMIN-ERR-LEAK-01.
const genericMsg = "internal error"

// logServerErr emits a structured server-side log line carrying the raw
// error and the request route. Kept tiny and dep-free so the package
// remains importable from cmd/server without pulling in any logging
// framework.
func logServerErr(r *http.Request, kind string, err error) {
	if err == nil {
		return
	}
	log.Printf("[abuse] route=%s kind=%s err=%v", r.URL.Path, kind, err)
}

// SessionAuth is the narrow seam routes use to gate the report-intake +
// review endpoints. It avoids a hard dependency on internal/auth so the
// abuse package stays standalone-testable. The real implementation in
// cmd/server wraps auth.Store.RequireSession.
type SessionAuth interface {
	// Authenticate returns the actor identifier (e.g. email) for r, or "" if
	// the caller is not authenticated. Implementations should write the 401
	// response themselves on miss; routes will simply return.
	Authenticate(w http.ResponseWriter, r *http.Request) (actor string, ok bool)

	// IsAdmin reports whether actor has T&S / admin privileges. Reviewer-only
	// endpoints (list + status mutate + reinstate + manual-suspend) require
	// this to return true.
	IsAdmin(ctx context.Context, actor string) bool
}

// Handler exposes the abuse HTTP API. Routes registered:
//
//	POST /api/abuse/report                    — file a new report (any authenticated user)
//	GET  /api/abuse/reports?status=open       — list reports (admin)
//	POST /api/abuse/reports/{id}/status       — update triage status (admin)
//	GET  /api/abuse/suspensions/{account_id}  — fetch suspension state (admin)
//	POST /api/abuse/suspensions/{account_id}/reinstate — reinstate (admin)
//	POST /api/abuse/suspensions/{account_id}/suspend   — manual suspend (admin)
type Handler struct {
	det          *Detector
	store        *Store
	auth         SessionAuth
	sharedSecret string // optional: callers can pass X-CP-Auth: <secret> in lieu of a session
	// prevSharedSecret is the OUTGOING secret during a rotation overlap window
	// (SECRET-ROTATE-01). When non-empty it is also accepted on X-CP-Auth so an
	// internal caller can roll its key without downtime.
	prevSharedSecret string
}

// NewHandler constructs the HTTP layer. det/store are required; auth may be
// nil in dev, in which case sharedSecret must be non-empty (otherwise every
// route 401s).
func NewHandler(det *Detector, store *Store, auth SessionAuth, sharedSecret string) *Handler {
	return &Handler{det: det, store: store, auth: auth, sharedSecret: sharedSecret}
}

// WithPreviousSecret sets the outgoing rotation secret (SECRET-ROTATE-01) and
// returns the handler for chaining. Pass "" when no rotation is in progress.
func (h *Handler) WithPreviousSecret(prev string) *Handler {
	h.prevSharedSecret = prev
	return h
}

// Register wires every route on the given mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/abuse/report", h.handleReport)
	mux.HandleFunc("GET /api/abuse/reports", h.handleListReports)
	mux.HandleFunc("POST /api/abuse/reports/{id}/status", h.handleSetStatus)
	mux.HandleFunc("GET /api/abuse/suspensions/{account_id}", h.handleGetSuspension)
	mux.HandleFunc("POST /api/abuse/suspensions/{account_id}/reinstate", h.handleReinstate)
	mux.HandleFunc("POST /api/abuse/suspensions/{account_id}/suspend", h.handleManualSuspend)
}

// ─── intake (any authenticated source) ───────────────────────────────────────

type reportRequest struct {
	SubjectID   string            `json:"subject_id"`
	Category    string            `json:"category"`
	Description string            `json:"description"`
	Evidence    map[string]string `json:"evidence"`
}

func (h *Handler) handleReport(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.authn(w, r)
	if !ok {
		return
	}
	var req reportRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		logServerErr(r, "invalid_json", err)
		writeErr(w, http.StatusBadRequest, "invalid_json", genericMsg)
		return
	}
	rep, err := h.store.FileReport(r.Context(), actor, req.SubjectID, req.Category, req.Description, req.Evidence)
	if err != nil {
		logServerErr(r, "report_failed", err)
		writeErr(w, http.StatusBadRequest, "report_failed", genericMsg)
		return
	}
	writeJSON(w, http.StatusCreated, rep)
}

// ─── review (admin) ──────────────────────────────────────────────────────────

func (h *Handler) handleListReports(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	status := r.URL.Query().Get("status")
	reps, err := h.store.ListReports(r.Context(), status, 0)
	if err != nil {
		logServerErr(r, "list_failed", err)
		writeErr(w, http.StatusBadRequest, "list_failed", genericMsg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": reps, "count": len(reps)})
}

type setStatusRequest struct {
	Status string `json:"status"`
}

func (h *Handler) handleSetStatus(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.adminActor(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing_id", "report id required")
		return
	}
	var req setStatusRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		logServerErr(r, "invalid_json", err)
		writeErr(w, http.StatusBadRequest, "invalid_json", genericMsg)
		return
	}
	if err := h.store.SetReportStatus(r.Context(), id, req.Status, actor); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not_found", "report not found")
			return
		}
		logServerErr(r, "set_status_failed", err)
		writeErr(w, http.StatusBadRequest, "set_status_failed", genericMsg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": req.Status})
}

func (h *Handler) handleGetSuspension(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	acct := r.PathValue("account_id")
	rec, err := h.store.GetSuspension(r.Context(), acct)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{"suspended": false})
			return
		}
		logServerErr(r, "lookup_failed", err)
		writeErr(w, http.StatusInternalServerError, "lookup_failed", genericMsg)
		return
	}
	active := rec.ReinstatedAt == ""
	writeJSON(w, http.StatusOK, map[string]any{"suspended": active, "record": rec})
}

func (h *Handler) handleReinstate(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.adminActor(w, r)
	if !ok {
		return
	}
	acct := r.PathValue("account_id")
	if err := h.det.ReinstateByHuman(r.Context(), acct, actor); err != nil {
		if errors.Is(err, ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not_suspended", "no active suspension")
			return
		}
		logServerErr(r, "reinstate_failed", err)
		writeErr(w, http.StatusBadRequest, "reinstate_failed", genericMsg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reinstated_by": actor})
}

type manualSuspendRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) handleManualSuspend(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.adminActor(w, r)
	if !ok {
		return
	}
	acct := r.PathValue("account_id")
	var req manualSuspendRequest
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req)
	if strings.TrimSpace(req.Reason) == "" {
		req.Reason = "manual:" + actor
	}
	if err := h.det.ManualSuspend(r.Context(), acct, req.Reason, actor); err != nil {
		logServerErr(r, "suspend_failed", err)
		writeErr(w, http.StatusBadRequest, "suspend_failed", genericMsg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "suspended_by": actor})
}

// ─── auth helpers ────────────────────────────────────────────────────────────

// authn accepts either a SessionAuth-authenticated user OR the shared-secret
// X-CP-Auth header (so internal services + boxes can file reports without a
// human session). Returns the actor identifier on success.
func (h *Handler) authn(w http.ResponseWriter, r *http.Request) (string, bool) {
	// SECRET-ROTATE-01: accept the current OR previous CP shared secret.
	if got := r.Header.Get("X-CP-Auth"); got != "" {
		gotB := []byte(got)
		matched := false
		if h.sharedSecret != "" && subtle.ConstantTimeCompare(gotB, []byte(h.sharedSecret)) == 1 {
			matched = true
		}
		if h.prevSharedSecret != "" && subtle.ConstantTimeCompare(gotB, []byte(h.prevSharedSecret)) == 1 {
			matched = true
		}
		if matched {
			actor := r.Header.Get("X-CP-Actor")
			if actor == "" {
				actor = "system:cp-shared-secret"
			}
			return actor, true
		}
	}
	if h.auth != nil {
		if actor, ok := h.auth.Authenticate(w, r); ok {
			return actor, true
		}
		return "", false
	}
	writeErr(w, http.StatusUnauthorized, "unauthenticated", "no auth configured")
	return "", false
}

// requireAdmin gates a route on admin privilege.
func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	_, ok := h.adminActor(w, r)
	return ok
}

// adminActor returns the admin actor id on success.
func (h *Handler) adminActor(w http.ResponseWriter, r *http.Request) (string, bool) {
	actor, ok := h.authn(w, r)
	if !ok {
		return "", false
	}
	// Shared-secret callers with no explicit actor are treated as system admin —
	// this is the same posture other CP→box endpoints use (X-Device-Auth /
	// X-Admin-Auth) and is documented in routes_cdn.go.
	if strings.HasPrefix(actor, "system:") {
		return actor, true
	}
	if h.auth != nil && h.auth.IsAdmin(r.Context(), actor) {
		return actor, true
	}
	writeErr(w, http.StatusForbidden, "forbidden", "admin required")
	return "", false
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, kind, msg string) {
	writeJSON(w, code, map[string]string{"error": kind, "message": msg})
}
