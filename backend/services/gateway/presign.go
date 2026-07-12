package gateway

// presign.go — PRESIGN-01: the cloud/no-STS storage seam.
//
// Tigris (and any S3-compatible store without STS AssumeRole) cannot mint
// prefix-scoped credentials, so the header-injection seam (storage_seam in
// gateway.go) refuses to hand out static creds to storage-permitted apps when
// running against such a store (SECURITY: Resolution.Configured() &&
// !Resolution.Scoped ⇒ gateway injects nothing). Apps in that situation get
// USABLE storage access by requesting a short-lived, OBJECT-scoped grant per
// object instead — the SAME pattern the Files control plane already uses
// (internal/storage/grant.go's GrantBroker), generalised to any storage-
// permitted app via this endpoint.
//
//	POST /api/storage/presign
//	  Auth: the browser session (cookie or Bearer session token) — the SAME
//	        session the gateway itself validates for app traffic.
//	  Body: {"app_id":"office","method":"GET"|"PUT","key":"documents/a.docx"}
//	  200:  storagepkg.ObjectGrant JSON (type/method/bucket/key/url|creds/expires_at)
//	  400:  invalid request (bad method, unsafe key, missing app_id)
//	  401:  no valid session
//	  403:  app_id was never granted the "storage" permission (AllowStorage)
//	  402:  app_id requires a CP product entitlement the caller doesn't carry
//	        (ENTITLE-01, same gate as app-dispatch; no-op when gating is off)
//	  502:  grant minting failed
//	  503:  no grant broker configured (e.g. no object store at all)
//
// The caller-supplied "key" is a path RELATIVE to the app's own namespace; the
// handler composes the full object key as "<userID>/<appPrefix><key>" itself —
// the caller can never name an object outside its own per-user/per-app prefix
// (the same C2/isolation boundary the header-injection seam enforces).

import (
	"encoding/json"
	"io"
	"net/http"
	"path"
	"strings"

	storagepkg "vulos/backend/internal/storage"
)

// maxPresignBodyBytes bounds the request body read for a presign request.
const maxPresignBodyBytes = 16 * 1024

// presignRequest is the JSON body of POST /api/storage/presign.
type presignRequest struct {
	AppID  string `json:"app_id"`
	Method string `json:"method"` // "GET" (read) or "PUT" (write)
	Key    string `json:"key"`    // relative path within the app's own prefix
}

// SetGrantBroker installs the object-grant broker + bucket resolver used by
// PresignHandler. Pass (nil, nil) to disable the endpoint (503 for every
// request). broker is typically the SAME *storagepkg.GrantBroker instance the
// Files control plane uses (internal/storage/grant.go) — it is generic, not
// Files-specific.
func (g *Gateway) SetGrantBroker(broker *storagepkg.GrantBroker, bucketFor func(userID string) string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.grantBroker = broker
	g.bucketForFunc = bucketFor
}

// PresignHandler returns the http.HandlerFunc for POST /api/storage/presign.
func (g *Gateway) PresignHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session := g.validateSession(r)
		if session == nil {
			presignErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		var req presignRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, maxPresignBodyBytes)).Decode(&req); err != nil {
			presignErr(w, http.StatusBadRequest, "invalid request body")
			return
		}

		g.mu.RLock()
		prefix, allowed := g.storageApps[req.AppID]
		broker := g.grantBroker
		bucketFor := g.bucketForFunc
		g.mu.RUnlock()

		if req.AppID == "" || !allowed {
			presignErr(w, http.StatusForbidden, "app is not permitted to use storage")
			return
		}
		// BUG FIX (2026-07-12, ENTITLE-01 gap): app-dispatch (Handler(), the
		// /app/{appId}/* proxy) refuses a request for a product-gated app that
		// doesn't carry the required entitlement, but this endpoint used to
		// skip that check entirely — a user with no entitlement for a premium
		// app could still mint themselves presigned storage access under that
		// app's prefix by calling this endpoint directly with its app_id, even
		// though they could never reach the app itself. Apply the SAME
		// entitlement gate the app-dispatch path enforces (still a no-op on
		// self-host/standalone, where gating is disabled).
		if ok, reason := g.entitlementCheck(r, req.AppID); !ok {
			presignErr(w, http.StatusPaymentRequired, "entitlement required: "+reason)
			return
		}
		if broker == nil || bucketFor == nil {
			presignErr(w, http.StatusServiceUnavailable, "presign is not available")
			return
		}

		relKey, ok := sanitizePresignKey(req.Key)
		if !ok {
			presignErr(w, http.StatusBadRequest, "invalid key")
			return
		}

		// C2: compose the SAME "<userID>/<appPrefix>" root the header-injection
		// seam uses, so a caller can never name an object outside its own
		// per-user/per-app namespace regardless of what "key" it supplies.
		fullKey := session.UserID + "/" + prefix + relKey

		bucket := bucketFor(session.UserID)
		if bucket == "" {
			presignErr(w, http.StatusServiceUnavailable, "no object store configured")
			return
		}

		var (
			grant storagepkg.ObjectGrant
			err   error
		)
		switch strings.ToUpper(req.Method) {
		case http.MethodGet:
			grant, err = broker.MintRead(r.Context(), session.UserID, bucket, fullKey, 0)
		case http.MethodPut:
			grant, err = broker.MintWrite(r.Context(), session.UserID, bucket, fullKey, 0)
		default:
			presignErr(w, http.StatusBadRequest, "method must be GET or PUT")
			return
		}
		if err != nil {
			presignErr(w, http.StatusBadGateway, "failed to mint grant")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(grant)
	}
}

// sanitizePresignKey validates and normalises a caller-supplied relative
// object key: non-empty, no leading slash, no ".." traversal, bounded length.
// Returns the cleaned key and true, or ("", false) when unsafe.
func sanitizePresignKey(k string) (string, bool) {
	if k == "" || len(k) > 1024 {
		return "", false
	}
	if strings.HasPrefix(k, "/") || strings.Contains(k, "\\") || strings.Contains(k, "\x00") {
		return "", false
	}
	clean := path.Clean(k)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", false
	}
	// path.Clean never leaves a leading "/" for a relative input, but guard
	// defensively in case Clean normalises to an absolute-looking path.
	if strings.HasPrefix(clean, "/") {
		return "", false
	}
	return clean, true
}

func presignErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
