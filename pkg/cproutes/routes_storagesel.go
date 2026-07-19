// routes_storagesel.go — per-account storage-backend selector API.
//
// Tasks: STORE-BYO-01, CP-STORE-01
//
// Routes registered:
//
//	GET  /api/storage/backend?account_id=  — fetch backend config for account
//	PUT  /api/storage/backend              — upsert backend config (session-gated)
//
// Auth: both endpoints require a valid vc_session cookie.
// Self-check: account_id must match the session user ID.
// Audit: every PUT is logged via cpAuditLog.
package cproutes

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/fleet"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/storagesel"
)

// registerStorageSelRoutes wires the storage-backend selector endpoints.
// ownerResolver binds a device's ULID to its owning account so the device-authed
// sync-mode read cannot be used to read another account (item 8). It may be nil
// only in dev/test, in which case the device path fails closed.
func registerStorageSelRoutes(mux *http.ServeMux, sel *storagesel.Selector, authStore *auth.Store, ownerResolver fleet.OwnershipResolver) {
	h := &storageSelHandlers{sel: sel, auth: authStore, owner: ownerResolver}
	mux.HandleFunc("GET /api/storage/backend", h.getBackend)
	mux.HandleFunc("PUT /api/storage/backend", h.putBackend)
	// CP→box read: the bundle node pulls its EFFECTIVE central/local sync mode
	// (STORE-LOCAL-01) on boot / config refresh. Authenticated with the existing
	// X-Device-Auth (CP_SHARED_SECRET) header — same as lancert / syncrz, NOT a
	// session cookie (the box has no user session). This is the read side of the
	// backup-mode ↔ storagesel contract the org-admin Backup tab writes.
	mux.HandleFunc("GET /api/storage/sync-mode", h.getSyncMode)
}

type storageSelHandlers struct {
	sel   *storagesel.Selector
	auth  *auth.Store
	owner fleet.OwnershipResolver
}

// getSyncMode returns { account_id, sync_mode } for the requested account.
// Device-authed (X-Device-Auth = CP_SHARED_SECRET). The bundle node passes its
// own account_id; central/local is then used to pick the storage source of
// truth. Unknown accounts default to "central".
func (h *storageSelHandlers) getSyncMode(w http.ResponseWriter, r *http.Request) {
	// RISK-SEC-01: source via the SecretsProvider (env default; KMS-ready).
	// SECRET-ROTATE-01: accept the current OR previous CP_SHARED_SECRET.
	if !hasSharedSecret(r.Context()) {
		httpx.Err(w, http.StatusServiceUnavailable, "no shared secret configured")
		return
	}
	if !validSharedSecretEither(r.Context(), r.Header.Get("X-Device-Auth")) {
		httpx.Err(w, http.StatusUnauthorized, "invalid device auth")
		return
	}
	// CROSS-ACCOUNT FIX (item 8): the X-Device-Auth secret is fleet-global, so a
	// client-supplied account_id let ANY box read ANY account's sync mode. Bind to
	// the authenticated device's OWN account by resolving the device's ULID via
	// the ownership resolver. The optional account_id query param must match the
	// resolved account (a hint, not the authority). FAIL CLOSED when we cannot
	// resolve a device → account.
	accountID, ok := h.resolveDeviceAccount(w, r)
	if !ok {
		return
	}
	mode, err := h.sel.GetSyncMode(r.Context(), accountID)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, map[string]string{"account_id": accountID, "sync_mode": string(mode)})
}

// GET /api/storage/backend?account_id=
func (h *storageSelHandlers) getBackend(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	accountID := r.URL.Query().Get("account_id")
	if !storageSelfOnly(w, u.ID, accountID) {
		return
	}
	b, err := h.sel.Get(r.Context(), accountID)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, b)
}

// putBackendRequest is the JSON body for PUT /api/storage/backend.
type putBackendRequest struct {
	AccountID string          `json:"account_id"`
	Kind      storagesel.Kind `json:"kind"`
	Endpoint  string          `json:"endpoint"`
	Region    string          `json:"region"`
	Bucket    string          `json:"bucket"`
	CredRef   string          `json:"cred_ref"`
}

// PUT /api/storage/backend
func (h *storageSelHandlers) putBackend(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}

	var req putBackendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if !storageSelfOnly(w, u.ID, req.AccountID) {
		return
	}

	b := storagesel.Backend{
		Kind:     req.Kind,
		Endpoint: req.Endpoint,
		Region:   req.Region,
		Bucket:   req.Bucket,
		CredRef:  req.CredRef,
	}

	if err := h.sel.Set(r.Context(), req.AccountID, b); err != nil {
		switch {
		case errors.Is(err, storagesel.ErrInvalidKind):
			httpx.Err(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, storagesel.ErrInvalidEndpt):
			httpx.Err(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, storagesel.ErrBucketRequired):
			httpx.Err(w, http.StatusBadRequest, err.Error())
		default:
			httpx.Err(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	// Audit log: record actor, what changed, and for which account.
	auditRecord(r.Context(), u.Email, "storage.backend.set",
		"account:"+req.AccountID,
		map[string]string{"kind": string(req.Kind), "bucket": req.Bucket})

	got, err := h.sel.Get(r.Context(), req.AccountID)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.JSON(w, got)
}

// resolveDeviceAccount binds a device-authed request to the account that owns the
// device's ULID (from the X-Device-ULID header), via the ownership resolver. It
// FAILS CLOSED: no resolver wired, missing/unknown ULID, or a mismatching
// account_id hint all reject. Returns (accountID, true) on success.
func (h *storageSelHandlers) resolveDeviceAccount(w http.ResponseWriter, r *http.Request) (string, bool) {
	if h.owner == nil {
		httpx.Err(w, http.StatusServiceUnavailable, "ownership resolver unavailable")
		return "", false
	}
	ulid := r.Header.Get("X-Device-ULID")
	if ulid == "" {
		httpx.Err(w, http.StatusBadRequest, "X-Device-ULID is required")
		return "", false
	}
	accountID, err := h.owner.OwnerOf(r.Context(), ulid)
	if err != nil || accountID == "" {
		httpx.Err(w, http.StatusForbidden, "device not enrolled")
		return "", false
	}
	// If the caller also supplied an account_id, it must match the bound account.
	if q := r.URL.Query().Get("account_id"); q != "" && q != accountID {
		httpx.Err(w, http.StatusForbidden, "account_id does not match authenticated device")
		return "", false
	}
	return accountID, true
}

// storageSelfOnly enforces that the session user can only access their own data.
func storageSelfOnly(w http.ResponseWriter, sessionUserID, accountID string) bool {
	if accountID == "" {
		httpx.Err(w, http.StatusBadRequest, "account_id is required")
		return false
	}
	if sessionUserID != accountID {
		httpx.Err(w, http.StatusForbidden, "forbidden")
		return false
	}
	return true
}
