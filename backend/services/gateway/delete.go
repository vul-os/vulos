package gateway

// delete.go — scoped object DELETE (PERFECTION PASS 2026-07-12).
//
// Presigned DELETE URLs are awkward (an S3 presigned URL signs exactly one
// method against exactly one key, and handing a bearer a presigned DELETE is
// a bigger blast radius than a bearer holding nothing at all). Instead the
// gateway mediates delete SERVER-SIDE: it holds the (already scoped-by-prefix
// where available) credentials itself and performs the deletion on the app's
// behalf. The app never presigns DELETE and never sees a raw credential.
//
//	POST /api/storage/delete
//	  Auth: the browser session (cookie or Bearer session token) — the SAME
//	        session the gateway validates for app traffic and for presign —
//	        PLUS an X-Vulos-App-Secret header proving the caller really is
//	        the app named in "app_id" (see DELETE-02 below, same as PRESIGN-02).
//	  Body: {"app_id":"office","key":"documents/a.docx"}
//	  204:  deleted (or already absent — deletes are idempotent, see
//	        GrantBroker.DeleteObject)
//	  400:  invalid request (missing app_id, unsafe key)
//	  401:  no valid session
//	  403:  app_id was never granted the "storage" permission (AllowStorage),
//	        OR the caller could not prove it is app_id (DELETE-02)
//	  402:  app_id requires a CP product entitlement the caller doesn't carry
//	        (ENTITLE-01, same gate as app-dispatch; no-op when gating is off)
//	  502:  delete failed
//	  503:  no grant broker configured (e.g. no object store at all)
//
// The caller-supplied "key" is a path RELATIVE to the app's own namespace,
// exactly like PresignHandler: the handler composes the full object key as
// "<userID>/<appPrefix><key>" itself, so an app can never name (and thus
// never delete) an object outside its own per-user/per-app prefix — the same
// C2/isolation boundary presign.go and the header-injection seam enforce.
//
// DELETE-02 (cross-app storage-prefix hole, fixed 2026-07-12, same root cause
// as PRESIGN-02): "app_id" alone used to be trusted outright, so any app
// could name a DIFFERENT app's id and delete objects under its prefix. The
// gateway now requires X-Vulos-App-Secret to match the secret minted for
// app_id at launch (GenerateAppSecret / VULOS_APP_SECRET) — see presign.go's
// package doc for the full rationale.

import (
	"encoding/json"
	"io"
	"net/http"
)

// maxDeleteBodyBytes bounds the request body read for a delete request.
const maxDeleteBodyBytes = 16 * 1024

// deleteRequest is the JSON body of POST /api/storage/delete.
type deleteRequest struct {
	AppID string `json:"app_id"`
	Key   string `json:"key"` // relative path within the app's own prefix
}

// DeleteHandler returns the http.HandlerFunc for POST /api/storage/delete.
// It shares AllowStorage's app whitelist and grantBroker/bucketForFunc with
// PresignHandler (set via SetGrantBroker) — there is no separate opt-in: any
// app permitted to presign objects under its prefix is also permitted to
// delete them there, matching the "storage" permission's existing scope.
func (g *Gateway) DeleteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session := g.validateSession(r)
		if session == nil {
			presignErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		var req deleteRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, maxDeleteBodyBytes)).Decode(&req); err != nil {
			presignErr(w, http.StatusBadRequest, "invalid request body")
			return
		}

		// DELETE-02: the caller must PROVE it is the app named in app_id —
		// see the package doc above. Checked before anything else app_id-
		// dependent so an unproven claim never even reaches the storage
		// registry lookup.
		if !g.AppSecretValid(req.AppID, r.Header.Get("X-Vulos-App-Secret")) {
			presignErr(w, http.StatusForbidden, "app credentials required or invalid")
			return
		}
		if hostAppID, ok := subdomainCallerAppID(r); ok && hostAppID != req.AppID {
			presignErr(w, http.StatusForbidden, "app_id does not match request host")
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
		// BUG FIX (2026-07-12, ENTITLE-01 gap — same as PresignHandler): don't
		// let a caller with no entitlement for a premium app delete objects
		// under that app's prefix just because they know its app_id. Apply
		// the same gate app-dispatch enforces (no-op when gating is off).
		if ok, reason := g.entitlementCheck(r, req.AppID); !ok {
			presignErr(w, http.StatusPaymentRequired, "entitlement required: "+reason)
			return
		}
		if broker == nil || bucketFor == nil {
			presignErr(w, http.StatusServiceUnavailable, "delete is not available")
			return
		}

		relKey, ok := sanitizePresignKey(req.Key)
		if !ok {
			presignErr(w, http.StatusBadRequest, "invalid key")
			return
		}

		// C2: compose the SAME "<userID>/<appPrefix>" root the header-injection
		// seam and PresignHandler use, so a caller can never name an object
		// outside its own per-user/per-app namespace regardless of what "key"
		// it supplies.
		fullKey := session.UserID + "/" + prefix + relKey

		bucket := bucketFor(session.UserID)
		if bucket == "" {
			presignErr(w, http.StatusServiceUnavailable, "no object store configured")
			return
		}

		if err := broker.DeleteObject(r.Context(), session.UserID, bucket, fullKey); err != nil {
			presignErr(w, http.StatusBadGateway, "failed to delete object")
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
