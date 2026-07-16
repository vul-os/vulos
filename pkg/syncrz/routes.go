package syncrz

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// genericMsg is the generic client-facing string used in place of raw
// err.Error() in default error branches. Sentinel branches keep their
// safe deterministic text. Mirrors the lancert/abuse pattern from
// audit-fix wave A (FIX-ADMIN-ERR-LEAK-01).
const genericMsg = "internal error"

// logServerErr emits a structured log line carrying the raw error and the
// request route. Kept dep-free so the package stays standalone.
func logServerErr(r *http.Request, kind string, err error) {
	if err == nil {
		return
	}
	log.Printf("[syncrz] route=%s kind=%s err=%v", r.URL.Path, kind, err)
}

// Handler wires the syncrz HTTP routes for box-side push/pull/blob-fetch.
// Authentication is the existing CP_SHARED_SECRET header (X-Device-Auth) —
// the same auth pattern lancert / routes_webapp use, so the OS side does
// not need new creds for sync.
type Handler struct {
	Svc *Service

	// AuthSecret is checked against r.Header.Get("X-Device-Auth"). When
	// empty, CP_SHARED_SECRET is consulted at request time.
	AuthSecret string

	// PrevAuthSecret is the OUTGOING secret during a rotation overlap window
	// (SECRET-ROTATE-01). When non-empty it is also accepted so a box can roll
	// from the old to the new CP_SHARED_SECRET without downtime. Populated by the
	// cmd/server wiring from CP_SHARED_SECRET_PREVIOUS.
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
// All routes require X-Device-Auth.
//
//	POST /api/sync/push                           body: pushBody
//	GET  /api/sync/pull?account_id=&sync_key=&box_id=&since=&limit=
//	GET  /api/sync/blob/{hash}?account_id=
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/sync/push", h.handlePush)
	mux.HandleFunc("GET /api/sync/pull", h.handlePull)
	mux.HandleFunc("GET /api/sync/blob/{hash}", h.handleBlob)
}

// pushBody is the on-wire JSON shape of POST /api/sync/push. Payload + Data
// bytes are base64-encoded so they survive JSON transport without parsing.
type pushBody struct {
	AccountID string          `json:"account_id"`
	SyncKey   string          `json:"sync_key"`
	Deltas    []pushBodyDelta `json:"deltas,omitempty"`
	Blobs     []pushBodyBlob  `json:"blobs,omitempty"`
}

type pushBodyDelta struct {
	BoxID        string   `json:"box_id"`
	CodecVersion int      `json:"codec_version"`
	PayloadB64   string   `json:"payload_b64"`
	Refs         []string `json:"refs,omitempty"`
}

type pushBodyBlob struct {
	ContentHash string `json:"content_hash"`
	DataB64     string `json:"data_b64"`
}

// pushResponse echoes WireProto so a client can sanity-check.
type pushResponse struct {
	Proto string `json:"proto"`
	PushResult
}

// pullResponse echoes WireProto + base64-encodes opaque payload bytes.
type pullResponse struct {
	Proto    string             `json:"proto"`
	Cursor   string             `json:"cursor"`
	Deltas   []pullResponseItem `json:"deltas"`
	Manifest []string           `json:"manifest"`
}

type pullResponseItem struct {
	ID            int64    `json:"id"`
	BoxID         string   `json:"box_id"`
	CodecVersion  int      `json:"codec_version"`
	PayloadB64    string   `json:"payload_b64"`
	PayloadSHA256 string   `json:"payload_sha256"`
	Refs          []string `json:"refs,omitempty"`
	PushedAt      int64    `json:"pushed_at"`
}

func (h *Handler) handlePush(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(w, r) {
		return
	}
	// Cap the request body so we don't OOM on a malicious push.
	r.Body = http.MaxBytesReader(w, r.Body, int64(MaxPayloadBytes+MaxBlobBytes+(64<<10)))
	var body pushBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.AccountID == "" || body.SyncKey == "" {
		writeJSONErr(w, http.StatusBadRequest, "account_id and sync_key are required")
		return
	}
	deltas := make([]PushInput, 0, len(body.Deltas))
	for i, d := range body.Deltas {
		raw, err := base64.StdEncoding.DecodeString(d.PayloadB64)
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "delta["+itoa(i)+"]: bad base64: "+err.Error())
			return
		}
		deltas = append(deltas, PushInput{
			BoxID:        d.BoxID,
			CodecVersion: d.CodecVersion,
			Payload:      raw,
			Refs:         d.Refs,
		})
	}
	blobs := make([]BlobInput, 0, len(body.Blobs))
	for i, b := range body.Blobs {
		raw, err := base64.StdEncoding.DecodeString(b.DataB64)
		if err != nil {
			writeJSONErr(w, http.StatusBadRequest, "blob["+itoa(i)+"]: bad base64: "+err.Error())
			return
		}
		blobs = append(blobs, BlobInput{ContentHash: b.ContentHash, Data: raw})
	}
	res, err := h.Svc.Push(r.Context(), body.AccountID, body.SyncKey, deltas, blobs)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidInput):
			writeJSONErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, ErrTooLarge):
			writeJSONErr(w, http.StatusRequestEntityTooLarge, err.Error())
		case errors.Is(err, ErrHashMismatch):
			writeJSONErr(w, http.StatusUnprocessableEntity, err.Error())
		default:
			logServerErr(r, "push", err)
			writeJSONErr(w, http.StatusInternalServerError, genericMsg)
		}
		return
	}
	writeJSON(w, http.StatusOK, pushResponse{Proto: WireProto, PushResult: res})
}

func (h *Handler) handlePull(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(w, r) {
		return
	}
	q := r.URL.Query()
	accountID := q.Get("account_id")
	syncKey := q.Get("sync_key")
	boxID := q.Get("box_id")
	cursor := q.Get("since")
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil && n > 0 {
			limit = n
		}
	}
	if accountID == "" || syncKey == "" || boxID == "" {
		writeJSONErr(w, http.StatusBadRequest, "account_id, sync_key, box_id are required")
		return
	}
	res, err := h.Svc.Pull(r.Context(), accountID, syncKey, boxID, cursor, limit)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidInput):
			writeJSONErr(w, http.StatusBadRequest, err.Error())
		default:
			logServerErr(r, "pull", err)
			writeJSONErr(w, http.StatusInternalServerError, genericMsg)
		}
		return
	}
	items := make([]pullResponseItem, 0, len(res.Deltas))
	for _, d := range res.Deltas {
		items = append(items, pullResponseItem{
			ID:            d.ID,
			BoxID:         d.BoxID,
			CodecVersion:  d.CodecVersion,
			PayloadB64:    base64.StdEncoding.EncodeToString(d.Payload),
			PayloadSHA256: d.PayloadSHA256,
			Refs:          d.Refs,
			PushedAt:      d.PushedAt,
		})
	}
	writeJSON(w, http.StatusOK, pullResponse{
		Proto:    WireProto,
		Cursor:   res.Cursor,
		Deltas:   items,
		Manifest: res.Manifest,
	})
}

func (h *Handler) handleBlob(w http.ResponseWriter, r *http.Request) {
	if !h.checkAuth(w, r) {
		return
	}
	hash := r.PathValue("hash")
	accountID := r.URL.Query().Get("account_id")
	if accountID == "" || hash == "" {
		writeJSONErr(w, http.StatusBadRequest, "account_id and hash are required")
		return
	}
	// Strip any accidental .bin suffix from URL captures.
	hash = strings.TrimSuffix(hash, ".bin")
	data, err := h.Svc.FetchBlob(r.Context(), accountID, hash)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidInput):
			writeJSONErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, ErrUnknown):
			writeJSONErr(w, http.StatusNotFound, "unknown content hash")
		case errors.Is(err, ErrHashMismatch):
			writeJSONErr(w, http.StatusInternalServerError, "stored object failed content-address verification")
		default:
			logServerErr(r, "blob", err)
			writeJSONErr(w, http.StatusInternalServerError, genericMsg)
		}
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Hash", hash)
	w.Header().Set("X-Sync-Proto", WireProto)
	_, _ = w.Write(data)
}

func (h *Handler) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	secret := h.AuthSecret
	if secret == "" {
		secret = os.Getenv("CP_SHARED_SECRET")
	}
	// SECRET-ROTATE-01: also honour the outgoing secret during a rotation
	// overlap window so a box can roll its key without downtime.
	prev := h.PrevAuthSecret
	if secret == "" && prev == "" {
		writeJSONErr(w, http.StatusServiceUnavailable, "syncrz: no shared secret configured")
		return false
	}
	got := []byte(r.Header.Get("X-Device-Auth"))
	ok := false
	if secret != "" && subtle.ConstantTimeCompare(got, []byte(secret)) == 1 {
		ok = true
	}
	if prev != "" && subtle.ConstantTimeCompare(got, []byte(prev)) == 1 {
		ok = true
	}
	if !ok {
		writeJSONErr(w, http.StatusUnauthorized, "invalid device auth")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// itoa avoids importing strconv just for one int→string in error messages.
func itoa(n int) string { return strconv.Itoa(n) }
