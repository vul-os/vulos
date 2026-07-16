package lancert

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
)

// genericMsg is the generic client-facing string used in place of raw
// err.Error() in admin/box-facing JSON error bodies. The underlying error
// is written to the structured server-side log to preserve operator
// diagnostics without leaking schema/SQL details to a compromised box or
// admin session. See FIX-ADMIN-ERR-LEAK-01.
const genericMsg = "internal error"

// logServerErr emits a structured log line carrying the raw error and the
// request route. Kept dep-free so the package stays standalone.
func logServerErr(r *http.Request, kind string, err error) {
	if err == nil {
		return
	}
	log.Printf("[lancert] route=%s kind=%s err=%v", r.URL.Path, kind, err)
}

// Handler wires the lancert HTTP routes that the box uses to report its LAN
// IP and pull its cert+key. Authentication is the existing CP_SHARED_SECRET
// header (X-Device-Auth) — the same auth the OS already presents on its
// other CP→box-side endpoints, so OS-side OFFLINE-01 does not need new creds.
type Handler struct {
	Svc *Service

	// AuthSecret is checked against r.Header.Get("X-Device-Auth"). When
	// empty, AuthFromEnv() is consulted on each request.
	AuthSecret string

	// PrevAuthSecret is the OUTGOING secret during a rotation overlap window
	// (SECRET-ROTATE-01). When non-empty it is also accepted, so a box can roll
	// from the old to the new CP_SHARED_SECRET without downtime. Populated by
	// the cmd/server wiring from CP_SHARED_SECRET_PREVIOUS.
	PrevAuthSecret string
}

// NewHandler returns a Handler for svc using the given shared secret.
func NewHandler(svc *Service, authSecret string) *Handler {
	return &Handler{Svc: svc, AuthSecret: authSecret}
}

// NewHandlerWithRotation returns a Handler that accepts EITHER authSecret (the
// current secret) or prevSecret (the outgoing secret) during a rotation overlap
// window. prevSecret may be empty (no rotation in progress).
func NewHandlerWithRotation(svc *Service, authSecret, prevSecret string) *Handler {
	return &Handler{Svc: svc, AuthSecret: authSecret, PrevAuthSecret: prevSecret}
}

// Register attaches the handler's routes to mux.
//
// Routes (both require X-Device-Auth):
//
//	POST /api/lancert/report-ip   — body: {box_id, lan_ip}      → 200 {ok:true}
//	GET  /api/lancert/cert        — query: box_id              → 200 BoxCert JSON
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/lancert/report-ip", h.handleReportIP)
	mux.HandleFunc("GET /api/lancert/cert", h.handleGetCert)
}

// handleReportIP records the box's LAN IP and publishes the A record.
//
// On success the cloud also kicks off issuance if no cert exists yet —
// the box will start polling /api/lancert/cert until it sees a cert.
func (h *Handler) handleReportIP(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(w, r) {
		return
	}
	var body struct {
		BoxID string `json:"box_id"`
		LANIP string `json:"lan_ip"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		logServerErr(r, "report_ip_decode", err)
		writeJSONErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := h.Svc.ReportLANIP(r.Context(), body.BoxID, body.LANIP); err != nil {
		switch {
		case errors.Is(err, ErrInvalidInput):
			logServerErr(r, "report_ip_invalid", err)
			writeJSONErr(w, http.StatusBadRequest, "invalid input")
		default:
			logServerErr(r, "report_ip_failed", err)
			writeJSONErr(w, http.StatusInternalServerError, genericMsg)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "fqdn": h.Svc.FQDNForBox(body.BoxID)})
}

// handleGetCert returns the stored cert+key for the box. The box writes the
// PEMs verbatim to its on-disk CertSource paths.
func (h *Handler) handleGetCert(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(w, r) {
		return
	}
	boxID := r.URL.Query().Get("box_id")
	if boxID == "" {
		writeJSONErr(w, http.StatusBadRequest, "box_id query param required")
		return
	}
	cert, err := h.Svc.GetCert(r.Context(), boxID)
	switch {
	case errors.Is(err, ErrUnknownBox):
		writeJSONErr(w, http.StatusNotFound, "unknown box_id")
		return
	case errors.Is(err, ErrNoCert):
		// 202 Accepted — registered but no cert yet (issuance pending).
		writeJSONErr(w, http.StatusAccepted, "no cert issued yet")
		return
	case err != nil:
		logServerErr(r, "get_cert_failed", err)
		writeJSONErr(w, http.StatusInternalServerError, genericMsg)
		return
	}
	writeJSON(w, http.StatusOK, cert)
}

// checkAuth enforces X-Device-Auth equals the configured shared secret.
// Uses constant-time compare. When no secret is configured, returns 503 —
// the route must not silently accept anonymous traffic in production.
//
// The secret is taken ONLY from h.AuthSecret, which the cmd/server wiring
// populates via the SecretsProvider (secretOrEnv "CP_SHARED_SECRET") so secret
// rotation covers it. We deliberately do NOT fall back to a raw os.Getenv here
// — that would bypass the provider and (if AuthSecret were ever left unset)
// read an unrotated value. Fail closed when AuthSecret is empty.
func (h *Handler) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if h.AuthSecret == "" && h.PrevAuthSecret == "" {
		writeJSONErr(w, http.StatusServiceUnavailable, "lancert: no shared secret configured")
		return false
	}
	got := []byte(r.Header.Get("X-Device-Auth"))
	// SECRET-ROTATE-01: accept the current OR previous secret (constant-time
	// against each candidate). Comparing against both unconditionally keeps the
	// total work independent of which one matched.
	ok := false
	if h.AuthSecret != "" && subtle.ConstantTimeCompare(got, []byte(h.AuthSecret)) == 1 {
		ok = true
	}
	if h.PrevAuthSecret != "" && subtle.ConstantTimeCompare(got, []byte(h.PrevAuthSecret)) == 1 {
		ok = true
	}
	if !ok {
		writeJSONErr(w, http.StatusUnauthorized, "invalid device auth")
		return false
	}
	return true
}

// Small JSON helpers — kept local so the package has no import of cmd/server.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
