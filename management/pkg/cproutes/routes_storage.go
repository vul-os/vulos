// routes_storage.go — storage HTTP routes for the vulos.cloud control-plane.
//
// Routes registered:
//
//	GET    /api/storage/config?account_id=   — fetch storage config (no secret)
//	POST   /api/storage/config               — upsert storage config
//	DELETE /api/storage/config?account_id=   — remove storage config
//	GET    /api/storage/usage?account_id=    — latest usage sample
//	POST   /api/storage/presign/get          — presigned GET URL
//	POST   /api/storage/presign/put          — presigned PUT URL
//	POST   /api/storage/delete               — scoped server-side object delete
//	GET    /api/storage/snapshot-url?account_id=&ulid= — snapshot presigned URL
//
// Auth: all endpoints require a valid session cookie (vc_session).
// Self-check: account_id must match the session user ID (self-only access).
//
// Error mapping:
//
//	ErrUnknownAccount | ErrUnknownBucket → 404
//	ErrProviderFailed                    → 502
//	Missing Tigris creds                 → 503
package cproutes

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/apptoken"
	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/billingport"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/storage"
)

// RegisterStorage wires all storage endpoints into mux.
// ent (the entitlement resolver seam) enforces per-tier storage quotas on write
// paths. With the self-host NoopResolver quota enforcement is a no-op (unlimited).
func RegisterStorage(mux *http.ServeMux, svc *storage.Service, authStore *auth.Store, ent billingport.EntitlementResolver) {
	h := &storageHandlers{svc: svc, auth: authStore, billing: ent}

	mux.HandleFunc("GET /api/storage/config", h.getConfig)
	mux.HandleFunc("POST /api/storage/config", h.putConfig)
	mux.HandleFunc("DELETE /api/storage/config", h.deleteConfig)
	mux.HandleFunc("GET /api/storage/usage", h.getUsage)
	mux.HandleFunc("POST /api/storage/presign/get", h.presignGet)
	mux.HandleFunc("POST /api/storage/presign/put", h.presignPut)
	mux.HandleFunc("POST /api/storage/delete", h.deleteObject)
	mux.HandleFunc("GET /api/storage/snapshot-url", h.snapshotURL)

	// BYO bucket (bring-your-own S3-compatible object storage). Session-gated;
	// the account is always the session user (no account_id spoofing).
	mux.HandleFunc("GET /api/storage/byo", h.byoStatus)
	mux.HandleFunc("POST /api/storage/byo", h.byoConnect)
	mux.HandleFunc("DELETE /api/storage/byo", h.byoDisconnect)

	// Self-service "Leave Vulos Cloud" / export-everything bundle. Reuses the
	// already-wired storage Service, auth store, and entitlement seam.
	RegisterAccountExport(mux, svc, authStore, ent)

	// Cloud Files (the Drive): its own index on the "files" schema, its bytes in
	// the SAME unified bucket under the "<owner>/drive/" prefix, its grants minted
	// through the storage plane wired above.
	RegisterFiles(mux, h, WireFiles(h))
}

// storageHandlers groups all storage HTTP handlers.
type storageHandlers struct {
	svc     *storage.Service
	auth    *auth.Store
	billing billingport.EntitlementResolver // optional; nil disables quota enforcement
}

// ---------------------------------------------------------------------------
// GET /api/storage/config?account_id=
// ---------------------------------------------------------------------------

func (h *storageHandlers) getConfig(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	accountID := r.URL.Query().Get("account_id")
	if !h.selfOnly(w, u.ID, accountID) {
		return
	}

	cfg, err := h.svc.GetConfig(r.Context(), accountID)
	if err != nil {
		h.mapErr(w, err)
		return
	}
	// Never expose SecretKey.
	cfg.SecretKey = ""
	httpx.JSON(w, cfg)
}

// ---------------------------------------------------------------------------
// POST /api/storage/config
// ---------------------------------------------------------------------------

type putConfigRequest struct {
	AccountID string `json:"account_id"`
	BYO       bool   `json:"byo"`
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"` // plaintext; encrypted at rest by Store
}

func (h *storageHandlers) putConfig(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	var req putConfigRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if !h.selfOnly(w, u.ID, req.AccountID) {
		return
	}
	if req.Bucket == "" {
		httpx.Err(w, http.StatusBadRequest, "bucket is required")
		return
	}
	if req.BYO && req.Endpoint != "" {
		if !strings.HasPrefix(req.Endpoint, "https://") {
			httpx.Err(w, http.StatusBadRequest, "BYO endpoint must use https scheme")
			return
		}
	}

	region := req.Region
	if region == "" {
		region = "auto"
	}

	cfg := storage.Config{
		AccountID: req.AccountID,
		BYO:       req.BYO,
		Endpoint:  req.Endpoint,
		Region:    region,
		Bucket:    req.Bucket,
		AccessKey: req.AccessKey,
		SecretKey: req.SecretKey,
	}

	if err := h.svc.PutConfig(r.Context(), cfg); err != nil {
		h.mapErr(w, err)
		return
	}
	cfg.SecretKey = ""
	httpx.JSON(w, cfg)
}

// ---------------------------------------------------------------------------
// DELETE /api/storage/config?account_id=
// ---------------------------------------------------------------------------

func (h *storageHandlers) deleteConfig(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	accountID := r.URL.Query().Get("account_id")
	if !h.selfOnly(w, u.ID, accountID) {
		return
	}

	if err := h.svc.DeleteConfig(r.Context(), accountID); err != nil {
		h.mapErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// GET /api/storage/usage?account_id=
// ---------------------------------------------------------------------------

func (h *storageHandlers) getUsage(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	accountID := r.URL.Query().Get("account_id")
	if !h.selfOnly(w, u.ID, accountID) {
		return
	}

	sample, err := h.svc.LatestUsage(r.Context(), accountID)
	if err != nil {
		h.mapErr(w, err)
		return
	}
	httpx.JSON(w, sample)
}

// ---------------------------------------------------------------------------
// POST /api/storage/presign/get
// POST /api/storage/presign/put
// ---------------------------------------------------------------------------

// Presign TTL bounds. A presigned URL is a bearer credential for one object, so
// its lifetime is clamped: absent/invalid → defaultPresignTTL; anything longer
// than maxPresignTTL is capped so a leaked URL cannot stay usable for hours.
const (
	defaultPresignTTL = 5 * time.Minute
	maxPresignTTL     = 60 * time.Minute
)

type presignRequest struct {
	AccountID  string `json:"account_id"`
	Bucket     string `json:"bucket"`
	Key        string `json:"key"`
	TTLSeconds int    `json:"ttl_seconds"`
	// AppID scopes the presign to a single Class-P app's prefix within the
	// account's unified bucket (STORAGE-SCOPING-01 — see knownStorageApps'
	// doc comment). Optional: empty means "no app scoping requested" (the
	// pre-existing, unscoped presign — still gated by callerOwnsBucket/
	// selfOnly, just not additionally prefix-restricted). Cloud callers that
	// want per-app isolation (Files, and the OS gateway on behalf of a
	// box-local app — see knownStorageApps) should always set this.
	AppID string `json:"app_id,omitempty"`
}

type presignResponse struct {
	URL string `json:"url"`
}

func (h *storageHandlers) presignGet(w http.ResponseWriter, r *http.Request) {
	h.presignOp(w, r, false)
}

func (h *storageHandlers) presignPut(w http.ResponseWriter, r *http.Request) {
	h.presignOp(w, r, true)
}

func (h *storageHandlers) presignOp(w http.ResponseWriter, r *http.Request, put bool) {
	userID, appAud, ok := storageCaller(h.auth, w, r)
	if !ok {
		return
	}
	var req presignRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if !h.selfOnly(w, userID, req.AccountID) {
		return
	}
	// SECURITY-C1: an app backend presenting an app-identity token gets its
	// app_id BOUND to the token's audience — it is no longer a self-asserted
	// body field. Office can only ever presign under <account>/office/, even if
	// it asks for another app's prefix. (Mirrors the OS-side fix that bound
	// presign/delete app_id to the calling app's own secret.)
	if appAud != "" {
		if req.AppID != "" && req.AppID != appAud {
			httpx.Err(w, http.StatusForbidden, "app_id does not match the calling app")
			return
		}
		req.AppID = appAud
	}
	if req.Bucket == "" || req.Key == "" {
		httpx.Err(w, http.StatusBadRequest, "bucket and key are required")
		return
	}
	// STORAGE-IDOR: for a vulos-MANAGED account the presigner uses a SINGLE global
	// Tigris credential that can sign ANY "vulos-*" bucket. selfOnly only checks
	// account_id == session; the client-supplied bucket was trusted verbatim, so a
	// caller could pass another tenant's bucket ("vulos-<victimULID>") and receive
	// a presigned GET (exfiltrate) or PUT (overwrite) URL for that tenant's data.
	// Pin the bucket to the caller's OWN canonical managed bucket. BYO accounts use
	// their own credentials, so any bucket they name is confined to their own S3
	// account (no cross-tenant reach) — those are left unrestricted.
	if !h.callerOwnsBucket(r.Context(), userID, req.Bucket) {
		httpx.Err(w, http.StatusForbidden, "bucket not owned by this account")
		return
	}

	// STORAGE-SCOPING-01: per-app prefix scoping within the unified bucket.
	// Cloud has no Tigris AssumeRole/STS surface (minter_tigris.go's doc
	// comment), so per-object PRESIGNED URLs are the CP-side enforcement point
	// the "never hold raw bucket creds" contract calls for — an app never sees
	// an AccessKey/Secret, only a presigned URL for the ONE object it asked
	// for, and (when it supplies app_id) that object must already live under
	// its own <accountID>/<appID>/ prefix. A request naming another app's
	// prefix is refused outright, never silently redirected — the caller
	// asked for its own presign and got the wrong one is a bug, not a 3xx.
	if req.AppID != "" {
		if !validStorageAppID(req.AppID) {
			httpx.Err(w, http.StatusBadRequest, "unknown app_id")
			return
		}
		if !keyWithinAppPrefix(req.Key, req.AccountID, req.AppID) {
			httpx.Err(w, http.StatusForbidden, "key is outside this app's storage prefix")
			return
		}
	} else if keyHasTraversal(req.Key) {
		// L1: the legacy (unscoped, user-session) presign path is already pinned to
		// the caller's OWN bucket (callerOwnsBucket), so there is no cross-tenant
		// reach — but tighten it so a key can never carry a ".." traversal segment.
		// Legitimate object keys never contain "..", so this cannot break a real
		// upload; it just refuses a malformed/abusive key on the un-prefixed path.
		httpx.Err(w, http.StatusBadRequest, "key must not contain path traversal segments")
		return
	}

	// Storage quota enforcement on write paths only (CLOUD-BILLING-EDGES edge #2).
	// BILLING-LOCATION-01: this is the ONE storage-quota gate (the second,
	// conflicting gate — storage.Service.CheckStorageQuota, which consulted a
	// stale 2 GB/10 GB allowance table instead of the real quota_table.go
	// ladder and was BYO-unaware — has been removed; see contract.go's comment
	// on Service). We check the most-recently-sampled usage (daily sample)
	// against the tier limit, tagged with the account's resolved hosting_kind
	// so a self-host/BYO account is exempt from the cap entirely (STORAGE-
	// LOCATION-$0-01). This covers BOTH uploads AND imported owned-copy files:
	// an import lands bytes in the same unified bucket, so it is sampled into
	// the same usage gauge and counts against the account's Files quota. An
	// over-quota write (upload or import) is blocked at 402 Payment Required —
	// consistent with the suite "over-cap ⇒ blocked, upgrade to raise" model
	// (see BILLING-AUDIT.md). On error reading usage we allow the write
	// through (metering is best-effort).
	if put && h.billing != nil {
		hostingKind := h.hostingKindFor(r.Context(), req.AccountID)
		if sample, err := h.svc.LatestUsage(r.Context(), req.AccountID); err == nil {
			if qerr := h.billing.CheckStorageQuota(r.Context(), req.AccountID, sample.SizeBytes, string(hostingKind)); errors.Is(qerr, billingport.ErrQuotaExceeded) {
				httpx.Err(w, http.StatusPaymentRequired, "storage quota exceeded for your current tier")
				return
			}
		}
	}

	// Clamp the presign lifetime to a sane window. A missing/zero TTL defaults to
	// 5 minutes; an over-long TTL is capped at maxPresignTTL so a caller cannot
	// mint a URL that stays live for hours/days (a long-lived, replayable
	// exfiltration/overwrite handle if it leaks). Negative values fall to the
	// default too.
	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = defaultPresignTTL
	}
	if ttl > maxPresignTTL {
		ttl = maxPresignTTL
	}

	p, err := h.svc.ProviderForAccount(r.Context(), req.AccountID)
	if err != nil {
		h.mapErr(w, err)
		return
	}

	var url string
	if put {
		url, err = p.PresignPut(r.Context(), req.Bucket, req.Key, ttl)
	} else {
		url, err = p.PresignGet(r.Context(), req.Bucket, req.Key, ttl)
	}
	if err != nil {
		h.mapErr(w, err)
		return
	}
	httpx.JSON(w, presignResponse{URL: url})
}

// ---------------------------------------------------------------------------
// POST /api/storage/delete
// ---------------------------------------------------------------------------

// deleteRequest is the CP mirror of the OS gateway's scoped-delete contract
// (PERFECTION PASS 2026-07-12, "scoped object DELETE (cloud)"). S3 presigned
// URLs sign exactly one method, so a presigned DELETE is awkward (and would
// hand an app a one-shot destructive URL it could replay against anything the
// URL's scope allows). Instead the CP mediates delete SERVER-SIDE, exactly
// like the OS gateway does for a self-hosted box: the CP holds the
// credential, the app only ever sends {account_id, bucket, app_id, key}.
//
// Key is RELATIVE to the app's own prefix — the handler composes the full
// object key as appStoragePrefix(account_id, app_id) + key, mirroring the OS
// gateway's "<accountID>/<appID>/<key>" composition. Apps never see, hold, or
// presign a raw delete URL.
type deleteRequest struct {
	AccountID string `json:"account_id"`
	Bucket    string `json:"bucket"`
	// AppID is REQUIRED (unlike presign's optional AppID, which supports the
	// pre-STORAGE-SCOPING-01 unscoped legacy path) — delete is destructive, and
	// this is a brand-new endpoint with no unscoped callers to stay compatible
	// with, so it enforces scoping unconditionally.
	AppID string `json:"app_id"`
	// Key is relative to the app's own <accountID>/<appID>/ prefix; the handler
	// composes the absolute object key server-side. A caller-supplied absolute
	// key (already containing the prefix) is rejected by the traversal/prefix
	// check below, not silently double-prefixed.
	Key string `json:"key"`
}

// deleteObject implements POST /api/storage/delete: the CP mirror of the OS
// gateway's scoped server-side delete. Same auth (session), same
// self-only/callerOwnsBucket bucket-ownership pin, and the same app_id
// whitelist + traversal rejection as presignOp — but ALWAYS requires app_id
// and composes the object key server-side instead of trusting a
// caller-supplied absolute key, so a delete can never reach outside the
// caller's own app prefix. Fail-closed: any validation failure returns an
// error status and performs no delete.
func (h *storageHandlers) deleteObject(w http.ResponseWriter, r *http.Request) {
	userID, appAud, ok := storageCaller(h.auth, w, r)
	if !ok {
		return
	}
	var req deleteRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if !h.selfOnly(w, userID, req.AccountID) {
		return
	}
	if req.Bucket == "" || req.Key == "" {
		httpx.Err(w, http.StatusBadRequest, "bucket and key are required")
		return
	}
	// SECURITY-C1: as in presignOp — an app backend's app_id is bound to its
	// token audience, so a compromised app can only delete within its own
	// prefix. Delete is destructive, so a mismatch is refused outright.
	if appAud != "" {
		if req.AppID != "" && req.AppID != appAud {
			httpx.Err(w, http.StatusForbidden, "app_id does not match the calling app")
			return
		}
		req.AppID = appAud
	}
	if req.AppID == "" {
		httpx.Err(w, http.StatusBadRequest, "app_id is required")
		return
	}
	if !validStorageAppID(req.AppID) {
		httpx.Err(w, http.StatusBadRequest, "unknown app_id")
		return
	}

	// Same cross-tenant pin as presignOp: never let a caller name another
	// tenant's bucket (see the STORAGE-IDOR comment above presignOp).
	if !h.callerOwnsBucket(r.Context(), userID, req.Bucket) {
		httpx.Err(w, http.StatusForbidden, "bucket not owned by this account")
		return
	}

	// Compose the absolute key server-side and re-validate it lands strictly
	// within the app's own prefix (rejects "..", an empty relative key, or a
	// caller trying to pass an already-absolute key that would otherwise
	// double-prefix into something outside the app's slice).
	fullKey := appStoragePrefix(req.AccountID, req.AppID) + req.Key
	if !keyWithinAppPrefix(fullKey, req.AccountID, req.AppID) {
		httpx.Err(w, http.StatusForbidden, "key is outside this app's storage prefix")
		return
	}

	p, err := h.svc.ProviderForAccount(r.Context(), req.AccountID)
	if err != nil {
		h.mapErr(w, err)
		return
	}
	if err := p.DeleteObject(r.Context(), req.Bucket, fullKey); err != nil {
		h.mapErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// GET /api/storage/snapshot-url?account_id=&ulid=
// ---------------------------------------------------------------------------

func (h *storageHandlers) snapshotURL(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	accountID := r.URL.Query().Get("account_id")
	ulid := r.URL.Query().Get("ulid")
	if !h.selfOnly(w, u.ID, accountID) {
		return
	}
	if ulid == "" {
		httpx.Err(w, http.StatusBadRequest, "ulid is required")
		return
	}
	// STORAGE-IDOR: SnapshotURL presigns "vulos-<ulid>"/latest.snap.enc with the
	// shared managed credential, so a client-supplied ulid could target another
	// tenant's box snapshot (?account_id=<self>&ulid=<victimULID>). Pin the ulid to
	// the caller's OWN canonical box ULID.
	if !strings.EqualFold(strings.TrimSpace(ulid), boxULID(u.ID)) {
		httpx.Err(w, http.StatusForbidden, "ulid not owned by this account")
		return
	}

	url, err := h.svc.SnapshotURL(r.Context(), accountID, ulid)
	if err != nil {
		h.mapErr(w, err)
		return
	}
	httpx.JSON(w, presignResponse{URL: url})
}

// ---------------------------------------------------------------------------
// BYO bucket — GET/POST/DELETE /api/storage/byo
// ---------------------------------------------------------------------------

// byoStatusResponse is the non-secret view of an account's storage backend.
type byoStatusResponse struct {
	BYO       bool   `json:"byo"`
	Connected bool   `json:"connected"`
	Endpoint  string `json:"endpoint"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
}

// byoStatus reports whether the session account has a BYO bucket connected.
func (h *storageHandlers) byoStatus(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	cfg, err := h.svc.GetConfig(r.Context(), u.ID)
	if errors.Is(err, storage.ErrUnknownAccount) {
		httpx.JSON(w, byoStatusResponse{})
		return
	}
	if err != nil {
		h.mapErr(w, err)
		return
	}
	httpx.JSON(w, byoStatusResponse{
		BYO:       cfg.BYO,
		Connected: cfg.BYO,
		Endpoint:  cfg.Endpoint,
		Bucket:    cfg.Bucket,
		Region:    cfg.Region,
	})
}

type byoConnectRequest struct {
	Endpoint  string `json:"endpoint"`
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

// byoConnect validates the user's S3-compatible bucket live (HEAD/list) and,
// on success, persists it (byo=true, secret encrypted at rest). The account is
// always the session user — no account_id is read from the body.
func (h *storageHandlers) byoConnect(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	var req byoConnectRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	cfg, err := h.svc.ConnectBYO(r.Context(), storage.BYOInput{
		AccountID: u.ID,
		Endpoint:  req.Endpoint,
		Bucket:    req.Bucket,
		Region:    req.Region,
		AccessKey: req.AccessKey,
		SecretKey: req.SecretKey,
	})
	if err != nil {
		if errors.Is(err, storage.ErrBYOValidation) {
			// Do NOT reflect the upstream/provider error text: it can echo the
			// resolved endpoint, an internal hostname, or an S3 error body back to
			// the caller (an SSRF oracle). Log the detail server-side, return a
			// generic, actionable message.
			log.Printf("[storage] BYO connect validation failed for account %s: %v", u.ID, err)
			httpx.Err(w, http.StatusBadRequest,
				"could not connect the bucket: check the endpoint (must be a public S3 host), bucket name, region and credentials")
			return
		}
		h.mapErr(w, err)
		return
	}
	httpx.JSON(w, byoStatusResponse{
		BYO:       true,
		Connected: true,
		Endpoint:  cfg.Endpoint,
		Bucket:    cfg.Bucket,
		Region:    cfg.Region,
	})
}

// byoDisconnect removes the BYO config; storage reverts to the managed bucket
// (re-provisioned lazily). Idempotent — returns 204 even if none was set.
func (h *storageHandlers) byoDisconnect(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	if err := h.svc.DisconnectBYO(r.Context(), u.ID); err != nil {
		h.mapErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// storageCaller resolves the credential on r to the account it acts for, and to
// the app audience it is bound to (SECURITY-C1).
//
// Two credential classes reach the storage gateway:
//
//   - A USER SESSION (the cockpit calling the CP directly, same-origin).
//     appAud is "" — no app scoping is imposed beyond what the caller asks for,
//     preserving the pre-existing unscoped presign path.
//
//   - An APP-IDENTITY TOKEN, which the reverse proxy minted in place of the
//     user's session (wire_apptoken.go) and which an app backend — Office is the
//     live example — forwards here to presign on the user's behalf. appAud is the
//     app slug the token is bound to, and the caller's app_id is pinned to it, so
//     a compromised app can only ever reach its OWN prefix.
//
// Accepted in the vc_session cookie (what Office forwards today) or in the
// X-Vulos-App-Auth header (the forward-looking contract). Fail-closed: an
// unverifiable token is simply not a credential, and we fall through to the
// session gate, which writes the 401/403.
func storageCaller(st *auth.Store, w http.ResponseWriter, r *http.Request) (userID, appAud string, ok bool) {
	raw := r.Header.Get(apptoken.HeaderName)
	if raw == "" {
		raw = auth.SessionFromRequest(r)
	}
	if raw != "" && apptoken.Looks(raw) {
		c, err := apptoken.VerifyAny(appTokenKeys(r.Context()), raw, "", time.Now())
		if err != nil {
			httpx.Err(w, http.StatusUnauthorized, "invalid app token")
			return "", "", false
		}
		if !validStorageAppID(c.Aud) {
			// A token for an audience with no storage prefix (e.g. "mail") has no
			// business here.
			httpx.Err(w, http.StatusForbidden, "this app may not use storage")
			return "", "", false
		}
		return c.Sub, c.Aud, true
	}
	// Not an app token → a real user session. RequireSession writes its own
	// 401/403 (including the app-token class rejection).
	u := st.RequireSession(r.Context(), w, r)
	if u == nil {
		return "", "", false
	}
	return u.ID, "", true
}

// selfOnly enforces that the session user can only access their own data.
func (h *storageHandlers) selfOnly(w http.ResponseWriter, sessionUserID, accountID string) bool {
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

// callerOwnsBucket reports whether accountID may presign against bucket.
//
// For a vulos-MANAGED account (the default — no BYO config) the presigner signs
// with a single fleet-wide Tigris credential, so the ONLY bucket the account may
// address is its own canonical managed bucket ("vulos-<lower(boxULID)>"). Any
// other name would be a cross-tenant reach and is refused.
//
// For a BYO account the presign uses the account's OWN S3 credentials, so any
// bucket it names is inherently confined to its own storage account — there is no
// cross-tenant credential to abuse — and we allow the client-supplied bucket.
//
// Fail-closed: on a config lookup error we treat the account as managed (the more
// restrictive branch) so a transient store error can never open a cross-tenant
// presign.
func (h *storageHandlers) callerOwnsBucket(ctx context.Context, accountID, bucket string) bool {
	cfg, err := h.svc.GetConfig(ctx, accountID)
	if err == nil && cfg.BYO {
		return true // BYO: confined to the account's own credentials.
	}
	// Managed (or unknown/error): pin to the canonical managed bucket. This mirrors
	// storage.managedBucketName: "vulos-" + strings.ToLower(ulid).
	want := "vulos-" + strings.ToLower(boxULID(accountID))
	return strings.EqualFold(strings.TrimSpace(bucket), want)
}

// hostingKindFor resolves the BILLING-LOCATION-01 hosting_kind for accountID
// from the SAME BYO signal resolver.StorageSource.IsSelfHost consults
// (storage.Config.BYO) — fail-closed to HostingCloud (the more heavily
// enforced kind) on a config lookup error or unknown account, mirroring
// callerOwnsBucket's fail-closed posture. A "box" (provisioned Fly box)
// hosting kind is tagged directly by box-aware call sites (routes_box.go,
// billing.BoxComputeEmitter — BOX-COMPUTE-UNIFY-01) for that box's own
// compute/storage events (which never flow through this Files/Tigris gate at
// all — see billing.ResolveHostingKind's doc comment); this gate only ever
// needs to distinguish "exempt (self-host)" from "enforced (everything
// else)", which is exactly what ResolveHostingKind computes.
func (h *storageHandlers) hostingKindFor(ctx context.Context, accountID string) billingport.HostingKind {
	cfg, err := h.svc.GetConfig(ctx, accountID)
	if err != nil {
		return billingport.HostingKindCloud
	}
	if h.billing == nil {
		return billingport.NewNoopResolver().ResolveHostingKind(cfg.BYO)
	}
	return h.billing.ResolveHostingKind(cfg.BYO)
}

// knownStorageApps is the whitelist of Class-P app IDs that may request an
// app-scoped presign (STORAGE-SCOPING-01). NOT an arbitrary caller-supplied
// string, so a typo'd or malicious app_id can never smuggle a path-traversal-
// shaped prefix through. The set is:
//
//   - "files" — Cloud Files (the Drive), served directly by the CP to the cockpit.
//   - "os"    — the OS gateway (the user's box), requesting on behalf of a
//     locally-installed app in the managed presign-broker path.
//
// BOX-FEDERATED PIVOT (2026-07): office/talk/board were REMOVED. Those apps
// retired their DEPLOY_MODE=cloud path, so no cloud-central Office/Talk/Board
// backend exists to presign here anymore. Self-hosted/OS-app instances presign
// against their OWN box (the OS gateway, "os"), never against the CP with an
// office/talk/board audience. Talk and Meet were withdrawn as products
// entirely (2026-07-15: comms are third-party) so neither ever presigns here.
// An app_id the CP no longer recognises is refused fail-closed (400 "unknown
// app_id").
var knownStorageApps = map[string]bool{
	"files": true,
	"os":    true,
}

// validStorageAppID reports whether appID is a recognised Class-P app.
func validStorageAppID(appID string) bool {
	return knownStorageApps[appID]
}

// appStoragePrefix returns the per-app prefix within an account's unified
// bucket: "<accountID>/<appID>/" — the PINNED CROSS-REPO CONTRACT's storage
// scoping layout (bucket = account's vulos-<ulid>; per-app prefix =
// <userID>/<appID>/).
func appStoragePrefix(accountID, appID string) string {
	return accountID + "/" + appID + "/"
}

// keyWithinAppPrefix reports whether key lives strictly under
// appStoragePrefix(accountID, appID) — i.e. the object this presign would
// mint a URL for cannot escape the app's own slice of the bucket via a bare
// prefix match, an empty suffix, or a ".." path segment.
func keyWithinAppPrefix(key, accountID, appID string) bool {
	prefix := appStoragePrefix(accountID, appID)
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	suffix := key[len(prefix):]
	if suffix == "" {
		return false // the prefix itself is not an object
	}
	for _, seg := range strings.Split(suffix, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

// keyHasTraversal reports whether key contains a ".." path segment. Used to
// harden the legacy (un-prefixed) presign path — see L1.
func keyHasTraversal(key string) bool {
	for _, seg := range strings.Split(key, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// mapErr maps storage sentinel errors to HTTP status codes.
func (h *storageHandlers) mapErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, storage.ErrUnknownAccount) || errors.Is(err, storage.ErrUnknownBucket):
		httpx.Err(w, http.StatusNotFound, err.Error())
	case errors.Is(err, storage.ErrProviderFailed):
		// Check if it's a missing-creds 503 vs generic 502.
		if strings.Contains(err.Error(), "S3_ACCESS_KEY_ID") ||
			strings.Contains(err.Error(), "S3_SECRET_ACCESS_KEY") ||
			strings.Contains(err.Error(), "are required") {
			httpx.Err(w, http.StatusServiceUnavailable, "storage provider not configured")
			return
		}
		httpx.Err(w, http.StatusBadGateway, err.Error())
	default:
		httpx.Err(w, http.StatusInternalServerError, err.Error())
	}
}
