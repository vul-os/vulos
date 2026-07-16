// routes_integrations_token.go — INTEG-02: device-authenticated token mint.
//
// A Vulos OS box mints a short-lived Google access token over the existing
// device-HMAC channel (the same DEVICE_SHARED_SECRET used by fleet heartbeats).
// The box presents:
//
//	X-Device-ULID:    <canonical box ULID>
//	X-Integration-Sig: hex(HMAC-SHA256(DEVICE_SHARED_SECRET, "integrations:token:" + provider + ":" + ulid))
//
// The CP resolves the ULID → owning account via the routing binding, mints (or
// serves a cached) access token by refreshing the stored refresh token, and
// returns ONLY the short-lived access token — never the refresh token or the
// client secret. The signed message is purpose-bound ("integrations:token:…")
// so a captured fleet-heartbeat signature cannot be replayed here.
//
// Drive (Vulos Files external stores): the minted Google access token already
// carries every scope the connected account granted (Gmail/Calendar/Drive/GCS),
// so the SAME token works against the Drive API — no separate provider or mint
// path. A caller passes an optional scope hint and the CP verifies the connection
// actually authorized it, returning 403 (reconnect-to-grant) instead of handing
// back a token that would 403 at the provider API:
//
//	?scope=drive        — Drive READ  (drive.readonly or full Drive)
//	?scope=drive.file   — Drive WRITE (drive.file or full Drive)
//	?scope=gcs          — Google Cloud Storage read/write (devstorage.read_write)
//
// EXT-PROVIDERS — three token-mint providers, all over the same INTEG-SEC-01
// device auth (X-Device-ULID + X-Device-Sig / X-Integration-Sig), short-lived
// minted token only, refresh token + client secret never leave the CP:
//
//	provider=google  — Gmail/Calendar/Drive/GCS from the Google connection.
//	provider=dropbox — Dropbox file content from the Dropbox connection.
//	provider=gcs     — ALIAS: brokered from the Google connection with an implicit
//	                   devstorage scope check. There is no separate "gcs"
//	                   connection — the OS supplies the bucket name; the CP only
//	                   brokers the token. Chosen as the simplest correct path: a
//	                   connected Google account that granted devstorage.read_write
//	                   can read/write its own GCS objects, reusing the existing
//	                   Google OAuth client (no extra app registration).
package cproutes

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/integrations"
	"github.com/vul-os/vulos-management/pkg/routing"
)

// mintRateLimiter is a small per-key fixed-window limiter for the token mint.
//
// SECURITY NOTE (INTEG-SEC-01): the token mint now requires a PER-DEVICE
// signature (a pinned device key or an owner-attested cert) — the fleet-wide
// DEVICE_SHARED_SECRET is no longer accepted for minting, closing the
// cross-tenant token-theft path. This limiter additionally bounds abuse by a
// single compromised (but correctly enrolled) box.
type mintRateLimiter struct {
	mu     sync.Mutex
	hits   map[string]*mintWindow
	limit  int
	window time.Duration
}

type mintWindow struct {
	count   int
	resetAt time.Time
}

func newMintRateLimiter(limit int, window time.Duration) *mintRateLimiter {
	return &mintRateLimiter{hits: make(map[string]*mintWindow), limit: limit, window: window}
}

// allow reports whether key may proceed at time now, counting this attempt.
func (l *mintRateLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.hits[key]
	if !ok || now.After(w.resetAt) {
		l.hits[key] = &mintWindow{count: 1, resetAt: now.Add(l.window)}
		// Opportunistic cleanup of expired windows so the map can't grow without
		// bound across many distinct ULIDs.
		if len(l.hits) > 1024 {
			for k, ww := range l.hits {
				if now.After(ww.resetAt) {
					delete(l.hits, k)
				}
			}
		}
		return true
	}
	if w.count >= l.limit {
		return false
	}
	w.count++
	return true
}

// integrationTokenSigMessage is the purpose-bound message the box signs.
func integrationTokenSigMessage(provider, ulid string) string {
	return "integrations:token:" + provider + ":" + ulid
}

// scopeRequirement resolves the optional ?scope= mint hint into the canonical
// scope label to echo back and a predicate that reports whether a connection's
// granted scopes satisfy it. ok is false for an unrecognized hint (→ 400).
//
// Google / GCS hints:
//   - "drive" / the read-only Drive URL → satisfied by drive.readonly OR full Drive.
//   - "drive.file" / "drive.write" / the drive.file URL → WRITE: satisfied by
//     drive.file OR full Drive (NOT by read-only).
//   - the full-Drive URL → satisfied only by full Drive.
//   - "gcs" / the devstorage URL → satisfied by devstorage.read_write.
//   - any other https://www.googleapis.com/auth/* URL → exact-match requirement.
//
// Dropbox hints:
//   - "files.read" / the read scope → satisfied by files.content.read.
//   - "files.write" / the write scope → satisfied by files.content.write.
//
// Hints are globally unique across providers, so a single resolver suffices.
func scopeRequirement(hint string) (label string, satisfied func(granted string) bool, ok bool) {
	switch hint {
	case "drive", integrations.ScopeDriveReadonly:
		return integrations.ScopeDriveReadonly, integrations.HasDriveReadAccess, true
	case "drive.file", "drive.write", integrations.ScopeDriveFile:
		return integrations.ScopeDriveFile, integrations.HasDriveWriteAccess, true
	case integrations.ScopeDrive:
		return integrations.ScopeDrive, func(g string) bool { return integrations.HasScope(g, integrations.ScopeDrive) }, true
	case "gcs", integrations.ScopeDevstorageReadWrite:
		return integrations.ScopeDevstorageReadWrite, integrations.HasGCSAccess, true
	case "files.read", integrations.ScopeDropboxContentRead:
		return integrations.ScopeDropboxContentRead, integrations.HasDropboxReadAccess, true
	case "files.write", integrations.ScopeDropboxContentWrite:
		return integrations.ScopeDropboxContentWrite, integrations.HasDropboxWriteAccess, true
	// Microsoft (Graph) importer scopes. Hints are namespaced (ms.*) so they stay
	// globally unique against the Dropbox "files.read" hint above.
	case "ms.files", "ms.files.read", integrations.ScopeMSFilesRead:
		return integrations.ScopeMSFilesRead, integrations.HasMicrosoftFilesRead, true
	case "ms.contacts", "ms.contacts.read", integrations.ScopeMSContactsRead:
		return integrations.ScopeMSContactsRead, integrations.HasMicrosoftContactsRead, true
	case "ms.calendar", "ms.calendar.read", integrations.ScopeMSCalendarRead:
		return integrations.ScopeMSCalendarRead, integrations.HasMicrosoftCalendarRead, true
	}
	if strings.HasPrefix(hint, "https://www.googleapis.com/auth/") ||
		strings.HasPrefix(hint, "https://graph.microsoft.com/") {
		return hint, func(g string) bool { return integrations.HasScope(g, hint) }, true
	}
	return "", nil, false
}

// mintProviderInfo maps a token-mint {provider} to the stored-connection provider
// it draws credentials from, plus a default scope hint applied when the caller
// supplies none. ok is false for an unknown provider (→ 400).
//
//   - google  → google connection, no default scope.
//   - dropbox → dropbox connection, no default scope.
//   - gcs     → google connection (GCS is brokered via the Google grant), with a
//     default "gcs" scope so the mint ALWAYS verifies devstorage was granted.
func mintProviderInfo(provider string) (connProvider, defaultScopeHint string, ok bool) {
	switch provider {
	case integrations.ProviderGoogle:
		return integrations.ProviderGoogle, "", true
	case integrations.ProviderDropbox:
		return integrations.ProviderDropbox, "", true
	case integrations.ProviderGCS:
		return integrations.ProviderGoogle, "gcs", true
	case integrations.ProviderMicrosoft:
		return integrations.ProviderMicrosoft, "", true
	}
	return "", "", false
}

// verifyFleetHMAC checks sig == hex(HMAC-SHA256(DEVICE_SHARED_SECRET, msg)) for
// any current/previous fleet secret (rotation-aware). This is the FLEET-WIDE
// bootstrap channel — it proves "a fleet box", not "this box" (see INTEG-SEC-01).
func (h *integrationsHandlers) verifyFleetHMAC(ctx context.Context, msg, sig string) bool {
	if sig == "" {
		return false
	}
	// COORDINATOR: needs secretCandidates — defined in wire_secrets.go (NOT in my subsystem)
	for _, secret := range secretCandidates(ctx, "DEVICE_SHARED_SECRET") {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(msg))
		expected := hex.EncodeToString(mac.Sum(nil))
		if hmac.Equal([]byte(sig), []byte(expected)) {
			return true
		}
	}
	return false
}

// authenticateMint authenticates a token-mint caller as the owner-device of canon.
//
// INTEG-SEC-01 (hardened) — a mint requires PER-DEVICE proof of possession. The
// fleet-wide DEVICE_SHARED_SECRET is NOT accepted for minting: it proves only
// "a fleet box", so a single compromised box (or any holder of the shared secret)
// could otherwise mint OTHER tenants' OAuth tokens — cross-tenant token theft.
// The shared secret remains the bootstrap channel for device-key ENROLLMENT
// (registerDeviceKey), and the enrollment self-sig proves possession of the key
// being pinned. So the flow is register-device-key-THEN-mint; there is no
// shared-secret mint path.
//
// Two accepted methods, strongest first:
//  1. Owner-attested device CERT (X-Device-Cert + X-Device-Pubkey + ed25519
//     X-Device-Sig): a management-CA-signed cert binding pubkey↔account↔ULID.
//     No pre-registration required because the cert itself is the per-device,
//     per-account binding.
//  2. Pinned device KEY (registered via /device/register): ECDSA X-Device-Sig
//     verified against the pinned key.
//
// A device that is neither cert-attested nor pinned is rejected (401) — it must
// register a per-device key first. Returns true when authenticated; otherwise
// writes the error and returns false.
func (h *integrationsHandlers) authenticateMint(ctx context.Context, w http.ResponseWriter, r *http.Request, provider, canon string) bool {
	msg := integrationTokenSigMessage(provider, canon)

	// (1) Owner-attested CA cert — strongest, preferred when presented.
	if len(h.caCertPubKey) > 0 && r.Header.Get("X-Device-Cert") != "" {
		return h.authenticateMintCert(ctx, w, r, msg, canon)
	}

	// (2) Pinned per-device key — the only other accepted mint credential.
	if h.deviceKeys == nil {
		httpx.Err(w, http.StatusServiceUnavailable, "device key registry unavailable")
		return false
	}
	dk, err := h.deviceKeys.Get(ctx, canon)
	switch {
	case err == nil:
		deviceSig := r.Header.Get("X-Device-Sig")
		raw, derr := base64.StdEncoding.DecodeString(deviceSig)
		if deviceSig == "" || derr != nil || !integrations.VerifyDeviceSig(dk.PubKeyDER, msg, raw) {
			httpx.Err(w, http.StatusUnauthorized, "invalid device signature")
			return false
		}
		return true
	case errors.Is(err, integrations.ErrNoDeviceKey):
		// No per-device key and no cert: the fleet-wide HMAC is NOT a mint
		// credential. Require enrollment first. (FLEET_REQUIRE_DEVICE_KEY is gone:
		// per-device keys are now ALWAYS required to mint.)
		httpx.Err(w, http.StatusUnauthorized, "device key not registered; register a per-device key before minting")
		return false
	default:
		httpx.Err(w, http.StatusServiceUnavailable, "device key lookup failed")
		return false
	}
}

// authenticateMintCert verifies an owner-attested device certificate (INTEG-SEC-01
// method 1). The box presents:
//
//	X-Device-Cert:   base64( management-CA ed25519 signature over the cert payload )
//	X-Device-Pubkey: base64( device ed25519 public key )
//	X-Device-Sig:    base64( device ed25519 signature over the mint message )
//
// The cert binds the pubkey to (account, ULID); account is resolved from the
// ULID's routing ownership, so a cert issued for one account cannot mint for
// another even if its ULID is presented.
func (h *integrationsHandlers) authenticateMintCert(ctx context.Context, w http.ResponseWriter, r *http.Request, msg, canon string) bool {
	pubKey, e1 := base64.StdEncoding.DecodeString(r.Header.Get("X-Device-Pubkey"))
	certSig, e2 := base64.StdEncoding.DecodeString(r.Header.Get("X-Device-Cert"))
	devSig, e3 := base64.StdEncoding.DecodeString(r.Header.Get("X-Device-Sig"))
	if e1 != nil || e2 != nil || e3 != nil || len(pubKey) == 0 || len(devSig) == 0 {
		httpx.Err(w, http.StatusUnauthorized, "invalid device certificate")
		return false
	}
	if h.ownerResolver == nil {
		httpx.Err(w, http.StatusServiceUnavailable, "ownership resolution unavailable")
		return false
	}
	accountID, err := h.ownerResolver.OwnerOf(ctx, canon)
	if err != nil || accountID == "" {
		httpx.Err(w, http.StatusForbidden, "device not enrolled")
		return false
	}
	if !integrations.VerifyDeviceCert(h.caCertPubKey, pubKey, accountID, canon, certSig) {
		httpx.Err(w, http.StatusUnauthorized, "invalid device certificate")
		return false
	}
	if !integrations.VerifyEd25519Sig(pubKey, msg, devSig) {
		httpx.Err(w, http.StatusUnauthorized, "invalid device signature")
		return false
	}
	return true
}

// deviceRegisterRequest is the body of POST /api/integrations/device/register.
type deviceRegisterRequest struct {
	ULID      string `json:"ulid"`
	PubKeyDER string `json:"pubkey_der"` // base64 PKIX DER
	Algo      string `json:"algo"`       // "ecdsa-p256"
	SelfSig   string `json:"self_sig"`   // base64 ASN.1 ECDSA over "integrations:register:"+ulid
}

// registerDeviceKey pins a device's public key (INTEG-SEC-01), trust-on-first-use.
//
// Auth is two-factor at this bootstrap boundary:
//   - X-Integration-Sig: fleet-wide HMAC over "integrations:register:"+ulid
//     (proves the caller is a fleet box — the bootstrap channel), AND
//   - self_sig: a signature over the same message by the key being registered
//     (proves the caller actually holds that device private key).
//
// First registration wins; presenting a DIFFERENT key for an already-pinned ULID
// returns 409 — a tamper signal that is logged.
func (h *integrationsHandlers) registerDeviceKey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if h.deviceKeys == nil {
		httpx.Err(w, http.StatusServiceUnavailable, "device key registry unavailable")
		return
	}

	var req deviceRegisterRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid request body")
		return
	}
	canon, err := routing.CanonULID(req.ULID)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid ULID")
		return
	}
	pubDER, err := base64.StdEncoding.DecodeString(req.PubKeyDER)
	if err != nil || len(pubDER) == 0 {
		httpx.Err(w, http.StatusBadRequest, "invalid pubkey")
		return
	}
	selfSig, err := base64.StdEncoding.DecodeString(req.SelfSig)
	if err != nil || len(selfSig) == 0 {
		httpx.Err(w, http.StatusBadRequest, "invalid self signature")
		return
	}
	if req.Algo != "" && req.Algo != integrations.AlgoECDSAP256 {
		httpx.Err(w, http.StatusBadRequest, "unsupported key algorithm")
		return
	}

	regMsg := integrations.RegisterSigMessage(canon)

	// (1) Bootstrap: prove the caller is a fleet box.
	if len(secretCandidates(ctx, "DEVICE_SHARED_SECRET")) == 0 {
		httpx.Err(w, http.StatusServiceUnavailable, "registration not configured")
		return
	}
	if !h.verifyFleetHMAC(ctx, regMsg, r.Header.Get("X-Integration-Sig")) {
		httpx.Err(w, http.StatusUnauthorized, "invalid device signature")
		return
	}

	// (2) Possession: prove the caller holds the private key being registered.
	if !integrations.VerifyDeviceSig(pubDER, regMsg, selfSig) {
		httpx.Err(w, http.StatusBadRequest, "self signature does not match pubkey")
		return
	}

	// Rate-limit registration per ULID AFTER the bootstrap + possession proofs,
	// so an unauthenticated caller cannot use a victim's ULID to exhaust its
	// registration window (per-IP flood protection lives in the gateway).
	if h.mintLimiter != nil && !h.mintLimiter.allow("register:"+canon, h.now()) {
		w.Header().Set("Retry-After", "60")
		httpx.Err(w, http.StatusTooManyRequests, "rate limited")
		return
	}

	pinnedNew, err := h.deviceKeys.Pin(ctx, canon, pubDER, req.Algo)
	if err != nil {
		switch {
		case errors.Is(err, integrations.ErrDeviceKeyConflict):
			log.Printf("[integrations] SECURITY: device key conflict for ulid=%s — re-pin attempt rejected", canon)
			httpx.Err(w, http.StatusConflict, "a different device key is already registered for this ULID")
		case errors.Is(err, integrations.ErrBadDeviceKey):
			httpx.Err(w, http.StatusBadRequest, "malformed device key")
		default:
			httpx.Err(w, http.StatusInternalServerError, "could not register device key")
		}
		return
	}
	if pinnedNew {
		log.Printf("[integrations] device key pinned: ulid=%s algo=%s", canon, req.Algo)
	}
	httpx.JSON(w, map[string]any{"ulid": canon, "pinned": pinnedNew})
}

// mintToken: device-authenticated short-lived access-token mint.
func (h *integrationsHandlers) mintToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	provider := r.PathValue("provider")
	// Resolve provider → stored-connection provider (+ any default scope). gcs is
	// brokered via the Google connection; an unknown provider fails fast with 400.
	connProvider, defaultScopeHint, ok := mintProviderInfo(provider)
	if !ok {
		httpx.Err(w, http.StatusBadRequest, "unknown integration provider")
		return
	}

	rawULID := r.Header.Get("X-Device-ULID")
	// Authentication requires a PER-DEVICE signature (X-Device-Sig) — either backed
	// by a pinned device key or an owner-attested device cert. The fleet-wide HMAC
	// (X-Integration-Sig) is NOT accepted for minting (cross-tenant risk); it is
	// only the enrollment bootstrap channel for /device/register.
	if rawULID == "" || r.Header.Get("X-Device-Sig") == "" {
		httpx.Err(w, http.StatusUnauthorized, "missing device authentication")
		return
	}
	canon, err := routing.CanonULID(rawULID)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid ULID")
		return
	}

	// Optional scope hint (e.g. a Vulos Files external Drive write mount passes
	// ?scope=drive.file). For the gcs alias a default hint is applied so the mint
	// ALWAYS verifies devstorage was granted. Validated up-front so an unknown
	// hint fails fast with 400; the granted-scope coverage check runs after the
	// token is minted.
	hint := r.URL.Query().Get("scope")
	if hint == "" {
		hint = defaultScopeHint
	}
	var requiredScope string
	var scopeSatisfied func(string) bool
	if hint != "" {
		var known bool
		requiredScope, scopeSatisfied, known = scopeRequirement(hint)
		if !known {
			httpx.Err(w, http.StatusBadRequest, "unknown scope hint")
			return
		}
	}

	// Authenticate the caller as THIS device (INTEG-SEC-01): a per-device key or an
	// owner-attested cert. The signed message is purpose-bound to {provider}. There
	// is no shared-secret fallback — an unenrolled device is rejected.
	if !h.authenticateMint(ctx, w, r, provider, canon) {
		return // authenticateMint already wrote the error response
	}

	// Per-ULID rate limit, applied AFTER authentication so an unauthenticated
	// caller who merely knows a victim's ULID cannot exhaust that ULID's mint
	// budget (the gateway's global per-IP limiter throttles unauthenticated
	// floods). This now bounds abuse by an authenticated/compromised box.
	if h.mintLimiter != nil && !h.mintLimiter.allow(canon, h.now()) {
		w.Header().Set("Retry-After", "60")
		httpx.Err(w, http.StatusTooManyRequests, "rate limited")
		return
	}

	// Resolve the device → owning account.
	if h.ownerResolver == nil {
		httpx.Err(w, http.StatusServiceUnavailable, "ownership resolution unavailable")
		return
	}
	accountID, err := h.ownerResolver.OwnerOf(ctx, canon)
	if err != nil || accountID == "" {
		httpx.Err(w, http.StatusForbidden, "device not enrolled")
		return
	}

	minted, err := h.svc.MintAccessToken(ctx, accountID, connProvider)
	if err != nil {
		if errors.Is(err, integrations.ErrNotConnected) {
			httpx.Err(w, http.StatusNotFound, "no "+connProvider+" connection for this account")
			return
		}
		// Refresh failure (revoked grant, provider error) — the box should treat
		// this as "needs reconnect".
		httpx.Err(w, http.StatusBadGateway, "could not mint access token")
		return
	}

	// If a scope was requested, confirm the connection actually authorized it.
	// A token without the scope would 403 at the provider API — surface a clear
	// "reconnect to grant" instead (e.g. a Drive read for an account connected
	// before Drive consent, or one that declined the Drive scope).
	if scopeSatisfied != nil && !scopeSatisfied(minted.Scopes) {
		log.Printf("[integrations] mint scope %q not granted: ulid=%s account=%s", requiredScope, canon, accountID)
		httpx.Err(w, http.StatusForbidden, "connected Google account has not authorized "+requiredScope+"; reconnect to grant access")
		return
	}

	expiresIn := int(time.Until(minted.Expiry).Seconds())
	if expiresIn < 0 {
		expiresIn = 0
	}
	log.Printf("[integrations] minted %s token: ulid=%s account=%s", provider, canon, accountID)
	resp := map[string]any{
		"access_token": minted.AccessToken,
		"expires_at":   minted.Expiry.UTC().Format(time.RFC3339),
		"expires_in":   expiresIn,
		"scopes":       minted.Scopes,
		"provider":     provider,
	}
	if requiredScope != "" {
		resp["required_scope"] = requiredScope
	}
	httpx.JSON(w, resp)
}
