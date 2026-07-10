package auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	stdpath "path"
	"strings"
	"sync"
	"time"

	"vulos/backend/internal/apikey"
)

// Handler wires local password, PIN, and fingerprint auth routes into an http.ServeMux.
type Handler struct {
	store         *Store
	states        *stateStore
	limiter       *RateLimiter
	OnUserCreated func(username, password, role string) // called after a new user is registered
	OnUserLogin   func(username, password, role string) // called on every successful login to sync credentials
	OnRoleChanged func(username, role string)           // called when a user's role changes
	// CLOGIN-06: device-local PIN service (nil if not configured).
	DevicePIN *DevicePINService
	// CLOGIN-07: fingerprint unlock service (nil if not configured).
	Fingerprint *FingerprintService
	// VKIntrospector enables vk_ Bearer token auth on OS API endpoints.
	// Set to a non-nil value only when VULOS_CP_BASE_URL is configured;
	// when nil, vk_ tokens fall through to session-only auth (self-host unchanged).
	VKIntrospector apikey.Introspector
}

func NewHandler(store *Store) *Handler {
	return &Handler{
		store:   store,
		states:  newStateStore(),
		limiter: DefaultRateLimiter(),
	}
}

// Register adds all auth routes to the mux.
func (h *Handler) Register(mux *http.ServeMux) {
	// Local auth (primary — username + password)
	mux.HandleFunc("POST /api/auth/register", h.handleRegister)
	mux.HandleFunc("POST /api/auth/login", h.handleLocalLogin)
	mux.HandleFunc("GET /api/auth/status", h.handleAuthStatus)
	mux.HandleFunc("POST /api/auth/change-password", h.handleChangePassword)

	// WAVE2-RECOVERY: per-user master key + phrase recovery.
	mux.HandleFunc("GET /api/auth/masterkey/status", h.handleMasterKeyStatus)
	mux.HandleFunc("GET /api/auth/masterkey/envelope", h.handleMasterKeyEnvelope)
	mux.HandleFunc("POST /api/auth/masterkey/recover", h.handleMasterKeyRecover)
	// WAVE8-TIER2: active-session password reset (session-required; NOT public).
	mux.HandleFunc("POST /api/auth/masterkey/reset-active", h.handleMasterKeyResetActive)

	mux.HandleFunc("GET /api/auth/me", h.handleMe)
	mux.HandleFunc("POST /api/auth/logout", h.handleLogout)

	// Profile management
	mux.HandleFunc("GET /api/profiles", h.handleListProfiles)
	mux.HandleFunc("GET /api/profiles/{userId}", h.handleGetProfile)
	mux.HandleFunc("PUT /api/profiles/{userId}", h.handleUpdateProfile)
	mux.HandleFunc("DELETE /api/profiles/{userId}", h.handleDeleteProfile)
	mux.HandleFunc("PUT /api/profiles/{userId}/role", h.handleSetRole)
	mux.HandleFunc("POST /api/auth/pin/set", h.handleSetPIN)
	mux.HandleFunc("POST /api/auth/pin/validate", h.handleValidatePIN)

	// CLOGIN-06: device-local PIN (TPM-wrapped / argon2id fallback).
	mux.HandleFunc("POST /api/auth/pin/device/set", h.handleDevicePINSet)
	mux.HandleFunc("POST /api/auth/pin/unlock", h.handleDevicePINUnlock)
	mux.HandleFunc("GET /api/auth/pin/status", h.handleDevicePINStatus)

	// Security portal
	mux.HandleFunc("GET /api/auth/security/bans", h.handleListBans)
	mux.HandleFunc("POST /api/auth/security/unban", h.handleUnban)
	mux.HandleFunc("GET /api/auth/security/stats", h.handleSecurityStats)

	// CLOGIN-01: cloud login endpoints
	mux.HandleFunc("POST /api/auth/cloudlogin", h.handleCloudLogin)
	mux.HandleFunc("GET /api/auth/cloud/status", h.handleCloudStatus)

	// CLOGIN-04: cloud account creation proxy
	mux.HandleFunc("POST /api/auth/cloud/signup", h.handleCloudSignup)

	// CLOGIN-07: fingerprint unlock (fprintd / D-Bus).
	mux.HandleFunc("GET /api/auth/fingerprint/status", h.handleFingerprintStatus)
	mux.HandleFunc("POST /api/auth/fingerprint/enroll/start", h.handleFingerprintEnrollStart)
	mux.HandleFunc("POST /api/auth/fingerprint/enroll/stop", h.handleFingerprintEnrollStop)
	mux.HandleFunc("POST /api/auth/fingerprint/verify", h.handleFingerprintVerify)
	mux.HandleFunc("POST /api/auth/fingerprint/remove", h.handleFingerprintRemove)

	log.Printf("[auth] registered email/password auth routes")
}

// publicPaths are endpoints that don't require authentication.
var publicPaths = map[string]bool{
	"/health":                         true,
	"/healthz":                        true, // trivial liveness probe (status page) — no auth
	"/api/auth/me":                    true,
	"/api/auth/logout":                true,
	"/api/auth/register":              true,
	"/api/auth/login":                 true,
	"/api/auth/status":                true,
	"/api/setup/status":               true,
	"/api/setup/mode":                 true, // INIT-09: unauthenticated sync-mode poll (setup wizard)
	"/api/setup/apps":                 true, // BUNDLE-01: suite selection GET/POST at onboarding (pre-account)
	"/api/setup/join-code":            true, // INIT-10: unauthenticated join-code decode
	"/api/setup/join":                 true, // INIT-08: unauthenticated cluster join (setup-time)
	"/api/setup/join/status":          true, // INIT-08: unauthenticated join progress poll
	"/api/browser/status":             true,
	"/manifest.json":                  true,
	"/api/auth/cloudlogin":            true, // CLOGIN-01: unauthenticated cloud login
	"/api/auth/cloud/status":          true, // CLOGIN-01: enrollment status check (setup-time)
	"/api/auth/cloud/signup":          true, // CLOGIN-04: unauthenticated cloud account creation (setup-time)
	"/api/auth/masterkey/recover":     true, // WAVE2-RECOVERY: phrase-based password reset (user is locked out)
	"/api/auth/pin/unlock":            true, // CLOGIN-06: PIN unlock (unauthenticated — user is on lock screen)
	"/api/auth/pin/status":            true, // CLOGIN-06: lockout status (unauthenticated — shown on lock screen)
	"/api/auth/fingerprint/status":    true, // CLOGIN-07: fingerprint status (unauthenticated — shown on lock screen)
	"/api/auth/fingerprint/verify":    true, // CLOGIN-07: fingerprint verify (unauthenticated — lock screen)
	"/api/auth/passkey/login/begin":   true, // LOGINISO-01: start passkey assertion (public — user not yet logged in)
	"/api/auth/passkey/login/finish":  true, // LOGINISO-01: finish passkey assertion + issue session (public)
	"/api/auth/qr/begin":              true, // LOGINISO-02: kiosk requests a QR challenge (public)
	"/api/auth/qr/poll":               true, // LOGINISO-02: kiosk polls for approval (public)
	"/init-passphrase":                true, // managed-box vault unlock (gated by X-Burst-Secret header, not session cookie)
	"/api/files/peer/serve":           true, // FILES-2B: box-to-box capability fetch (authed by signed capability + fetch proof, not a session)
	"/api/files/internal/content-key": true, // WAVE-7: internal cell→box content-key lookup (authed by X-Vulos-Internal-Auth shared secret, not a session)
	"/mcp":                            true, // APPS/MCP: agent MCP server (authed by vat_ app token via the @vulos/apps platform, not a session)
	"/metrics":                        true, // OBS: Prometheus scrape — the /metrics handler does its OWN owner-or-scrape-token gate (VULOS_METRICS_TOKEN), so the session middleware must defer to it rather than 401 a tokened scraper. NOT actually public: no auth = 403 at the handler.

	// PEERING (box-to-box, server-to-server). These routes are reached by REMOTE
	// peers (other Vulos boxes / browser peers) that have NO OS session on this box;
	// they authenticate with their OWN peering mechanism, NOT an OS session, exactly
	// like /api/files/peer/serve above. Without these entries the OS session
	// middleware 401s every inbound peer request BEFORE it reaches its real gate, so
	// all box-to-box peering (messaging, calls, media, collab, relay, prekeys) is
	// dead. Each route below states the self-auth that gates it. Only genuinely S2S,
	// self-authenticating routes are listed — the CLIENT-facing peering routes
	// (contacts/*, conversations/*, groups create, call/initiate, media/upload,
	// identity export/import/revoke, profile PUT, collab WS /sync, discover, feeds
	// create/publish) are deliberately OMITTED so they stay OS-session-authed.
	"/.well-known/vula-id":               true, // PEER-12: public self-certifying identity (public fields only; signature-verifiable by the embedded key)
	"/api/peering/relay/deposit":         true, // PEER-38: store-and-forward deposit (Ed25519-signed request + mutual-trust contact check in the handler)
	"/api/peering/relay/pickup":          true, // PEER-38: store-and-forward pickup (Ed25519-signed "<vula_id>.<ts>" Authorization header)
	"/api/peering/relay/ack":             true, // PEER-38: store-and-forward ack (recipient-authenticated, deletes only its own blobs)
	"/api/peering/relay/attest":          true, // relay attestation evidence document (public, read-only)
	"/api/peering/prekeys/claim":         true, // X3DH forward secrecy: OPK claim (public by design — the signed prekey signature is the authorization; revoked identities fail closed)
	"/api/peering/prekeys/publish":       true, // X3DH forward secrecy: browser peer publishes its PUBLIC bundle (the signed-prekey signature IS the authorization; forged/revoked bundles fail closed)
	"/api/peering/profile/notify-change": true, // S2S cache-eviction hint from a peer (only evicts a locally-cached public profile; self-heals on next fetch)
}

// publicPrefixes are path prefixes that don't require authentication.
//
// APPS/MCP NOTE: the @vulos/apps runtime/webhook/MCP routes authenticate with a
// vat_ app token (or the webhook-id secret), NOT an OS session, so the session
// middleware must DEFER to the platform's own constant-time token auth here. Only
// these token-authed subtrees are public; the apps MANAGEMENT API (/api/apps,
// /api/apps/{id}, /api/apps/commands) is NOT listed and stays OS-session-authed.
var publicPrefixes = []string{
	"/assets/",
	"/mcp/",            // MCP subtree (vat_ app token authed by the @vulos/apps MCP layer)
	"/api/apps/v1/",    // apps runtime: act / read / events / auth.test (vat_ app token authed)
	"/api/apps/hooks/", // apps incoming webhooks (authed by the webhook-id secret in the URL)

	// PEERING inbound subtree (box-to-box). Every /api/peering/inbound/* route is
	// mounted behind peering.InboundMiddleware, which fails CLOSED: it verifies the
	// request body is an Ed25519-signed Envelope (401 on missing/invalid signature),
	// rejects revoked sender identities (403), and rejects senders who are not in the
	// approved contacts list (403) — except /api/peering/inbound/request, which is
	// intentionally open to unknown peers (still signature-verified) so a first
	// contact request can be delivered. That middleware IS the authentication for
	// this subtree, so the OS session gate must defer to it. This is the primary
	// server-to-server surface (messages, calls, media, group + collab updates,
	// presence, drop); without it every inbound peer envelope is 401'd here first.
	"/api/peering/inbound/",
}

func isPublicPath(rawPath string) bool {
	// SECURITY (path-traversal auth bypass): the middleware sees r.URL.Path, which
	// retains raw ".." / "." segments (Go's http.ServeMux only cleans the path
	// AFTER this gate runs, when it routes). Without normalising here, a request to
	// "/api/peering/inbound/../conversations/c1/send" would (a) prefix-match the
	// public "/api/peering/inbound/" and skip the session gate, then (b) be routed
	// by ServeMux to the SESSION-GATED conversations handler — an auth bypass on
	// every prefix rule below. Clean the path first so the public-prefix decision is
	// made against the SAME path ServeMux will route. path.Clean collapses "..",
	// so the example becomes "/api/peering/conversations/..." which no longer
	// matches a public prefix and stays gated.
	path := stdpath.Clean(rawPath)
	if publicPaths[path] {
		return true
	}
	for _, prefix := range publicPrefixes {
		if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// Middleware extracts the session, enforces auth on protected endpoints, and rate limits.
//
//   - AT10: `Authorization: Bearer vulos-admin-<token>` → admin identity.
//   - vk_ API-key auth: `Authorization: Bearer vk_…` → CP-introspected identity.
//     Only active when VKIntrospector is set (VULOS_CP_BASE_URL configured);
//     unset = self-host mode, session-only auth unchanged.
func (h *Handler) Middleware(next http.Handler) http.Handler {
	return h.limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// C1 (SEC-A): strip attacker-supplied identity headers before anything else.
		r.Header.Del("X-User-ID")
		r.Header.Del("X-User-Email")

		// AT10 — check admin bearer token before cookie path.
		if authHdr := r.Header.Get("Authorization"); strings.HasPrefix(authHdr, "Bearer "+AT10TokenPrefix) {
			presented := strings.TrimPrefix(authHdr, "Bearer ")
			home, _ := os.UserHomeDir()
			if ok, _, _ := ValidateAdminToken(home, presented); ok {
				if adminUser := h.store.at10FirstUser(); adminUser != nil {
					r.Header.Set("X-User-ID", adminUser.ID)
					r.Header.Set("X-User-Email", adminUser.Email)
				}
			}
		}

		// vk_ API-key auth (VK-AUTH-01): resolve vk_… Bearer tokens via the CP
		// introspection seam. Only active when VKIntrospector is non-nil
		// (VULOS_CP_BASE_URL configured). Fail-closed: CP errors and
		// missing-product always yield 401 — never fall through to session auth.
		if r.Header.Get("X-User-ID") == "" && h.VKIntrospector != nil {
			if authHdr := r.Header.Get("Authorization"); strings.HasPrefix(authHdr, "Bearer "+apikey.KeyPrefix) {
				key := strings.TrimPrefix(authHdr, "Bearer ")
				res, err := h.VKIntrospector.Introspect(r.Context(), key)
				if err != nil {
					// CP unreachable — fail closed.
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprintf(w, `{"error":"api key validation unavailable"}`)
					return
				}
				if !res.Valid || !res.HasProduct(apikey.ProductOS) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprintf(w, `{"error":"unauthorized"}`)
					return
				}
				// Resolve CP account email → local OS user.
				u := h.store.GetUserByEmail(res.Account)
				if u == nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprintf(w, `{"error":"unauthorized"}`)
					return
				}
				r.Header.Set("X-User-ID", u.ID)
				r.Header.Set("X-User-Email", u.Email)
			}
		}

		// Extract session if present (cookie / regular Bearer session token).
		// Track whether auth came from the session COOKIE so we can apply the
		// CSRF check below (M6 fix: SameSite=None requires CSRF protection).
		authedViaCookie := false
		if r.Header.Get("X-User-ID") == "" {
			token := extractToken(r)
			if token != "" {
				if sess, ok := h.store.ValidateToken(token); ok {
					r.Header.Set("X-User-ID", sess.UserID)
					r.Header.Set("X-User-Email", sess.Email)
					// Detect cookie-only auth: no Authorization header was present.
					authedViaCookie = r.Header.Get("Authorization") == ""
				}
			}
		}

		// M6 fix: CSRF protection for cookie-authenticated mutations.
		//
		// When SameSite=None is in use (required for subdomain iframes), the
		// browser sends cookies on cross-site requests. Without a CSRF check, a
		// malicious page can trigger state-mutating API calls on behalf of the
		// logged-in user.
		//
		// Mitigation: for state-changing methods (POST/PUT/PATCH/DELETE) that
		// are authenticated via session cookie, we require either:
		//   a) Content-Type: application/json  (browser form/fetch defaults
		//      cannot set this without a preflight — CORS blocks the attack), or
		//   b) Origin header that matches the request Host (same-origin check).
		//
		// GET/HEAD/OPTIONS are exempt (idempotent reads).
		// Requests already authenticated via AT10 bearer or vk_ API-key skip
		// this check (those tokens are already not cookie-bound).
		if authedViaCookie {
			method := r.Method
			if method == http.MethodPost || method == http.MethodPut ||
				method == http.MethodPatch || method == http.MethodDelete {
				ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
				// Accept application/json (or application/json;charset=utf-8 etc.)
				jsonCT := strings.HasPrefix(ct, "application/json")
				// Accept same-origin: Origin must match the Host.
				origin := r.Header.Get("Origin")
				sameOrigin := origin == "" // absent Origin is allowed (non-browser clients)
				if !sameOrigin && origin != "" {
					// Strip scheme from Origin and compare to Host.
					originHost := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
					sameOrigin = originHost == r.Host
				}
				if !jsonCT && !sameOrigin {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprintf(w, `{"error":"csrf: request rejected — use application/json content-type or same-origin request"}`)
					return
				}
			}
		}

		// Enforce auth on all non-public endpoints
		if !isPublicPath(r.URL.Path) && r.Header.Get("X-User-ID") == "" {
			// Allow only frontend static assets without auth (HTML/JS/CSS/images)
			// The React app has its own auth gate in the UI
			p := r.URL.Path
			if p == "/" || p == "/index.html" ||
				strings.HasPrefix(p, "/assets/") ||
				strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".css") ||
				strings.HasSuffix(p, ".svg") || strings.HasSuffix(p, ".png") ||
				strings.HasSuffix(p, ".ico") || strings.HasSuffix(p, ".woff2") ||
				p == "/sw-proxy.js" {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(401)
			fmt.Fprintf(w, `{"error":"unauthorized"}`)
			return
		}

		next.ServeHTTP(w, r)
	}))
}

// --- Local username/password auth ---

func (h *Handler) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"has_users": h.store.HasAnyUsers(),
	})
}

func (h *Handler) handleRegister(w http.ResponseWriter, r *http.Request) {
	ip := extractIP(r)
	if h.limiter.IsBanned(ip) {
		writeErr(w, 429, "too many attempts")
		return
	}

	// Allow unauthenticated registration ONLY when no users exist (first-time setup).
	// Otherwise require an authenticated admin.
	if h.store.HasAnyUsers() {
		reqUserID := r.Header.Get("X-User-ID")
		if reqUserID == "" {
			writeErr(w, 401, "unauthorized")
			return
		}
		reqProfile, _ := h.store.GetProfile(reqUserID)
		if reqProfile == nil || reqProfile.Role != RoleAdmin {
			writeErr(w, 403, "only admins can create users")
			return
		}
	}

	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}

	user, err := h.store.Register(req.Username, req.Password, req.DisplayName)
	if err != nil {
		h.limiter.RecordFailure(ip)
		writeErr(w, 400, err.Error())
		return
	}

	// Create corresponding Linux user with appropriate permissions
	if h.OnUserCreated != nil {
		profile, _ := h.store.GetProfile(user.ID)
		role := "user"
		if profile != nil {
			role = string(profile.Role)
		}
		h.OnUserCreated(req.Username, req.Password, role)
	}

	h.store.Flush()

	// If caller is not authenticated (first-user setup), log them in
	if r.Header.Get("X-User-ID") == "" {
		sess := h.store.CreateSession(user, "")
		h.store.Flush()
		http.SetCookie(w, sessionCookie(r, sess.Token))

		// WAVE2-RECOVERY: provision the per-user MASTER KEY and force the recovery
		// phrase. The phrase is returned ONCE for display; it is never persisted or
		// logged. Fail-closed: if provisioning fails we surface it so the account is
		// not left in a keyless state (the UI can retry / warn).
		resp := map[string]any{"user": user.Safe(), "session": sess}
		phrase, mkErr := h.store.ProvisionMasterKey(user.ID, req.Password)
		if mkErr != nil {
			log.Printf("[auth] CRITICAL: master-key provisioning failed for %q: %v", req.Username, mkErr)
			resp["master_key_error"] = "master key provisioning failed"
		} else {
			resp["master_recovery_phrase"] = phrase
		}
		writeJSON(w, resp)
		return
	}

	// Admin creating a user — just return the new user info. The master key for an
	// admin-created account is provisioned on that user's FIRST login (the admin
	// must never see the new user's recovery phrase), which is a follow-up path.
	writeJSON(w, map[string]any{"user": user.Safe()})
}

func (h *Handler) handleLocalLogin(w http.ResponseWriter, r *http.Request) {
	ip := extractIP(r)
	if h.limiter.IsBanned(ip) {
		writeErr(w, 429, "too many attempts")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}

	user, err := h.store.Login(req.Username, req.Password)
	if err != nil {
		h.limiter.RecordFailure(ip)
		writeErr(w, 401, err.Error())
		return
	}

	h.limiter.RecordSuccess(ip)

	// Sync Linux user password + role on every login
	if h.OnUserLogin != nil {
		profile, _ := h.store.GetProfile(user.ID)
		role := "user"
		if profile != nil {
			role = string(profile.Role)
		}
		h.OnUserLogin(req.Username, req.Password, role)
	}

	sess := h.store.CreateSession(user, "")
	h.store.Flush()

	http.SetCookie(w, sessionCookie(r, sess.Token))

	writeJSON(w, map[string]any{"user": user.Safe(), "session": sess})
}

// ─── CLOGIN-01: Cloud login ───────────────────────────────────────────────────

// handleCloudLogin validates a cloud-signed login token and issues an OS session.
//
// Request body (JSON):
//
//	{
//	  "token":     <canonical JSON bytes, base64-encoded>,
//	  "signature": <base64 Ed25519 signature>
//	}
//
// On success: sets session cookie + returns CloudSessionInfo JSON.
// On failure: 401 with error details.
//
// This handler does NOT make any outgoing network call — the cloud token is
// validated locally against the baked broker pubkey.
func (h *Handler) handleCloudLogin(w http.ResponseWriter, r *http.Request) {
	ip := extractIP(r)
	if h.limiter.IsBanned(ip) {
		writeErr(w, 429, "too many attempts")
		return
	}

	var req CloudLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request body")
		return
	}
	if len(req.Token) == 0 || req.Signature == "" {
		writeErr(w, 400, "token and signature are required")
		return
	}

	pub, err := LoadBrokerPubkey()
	if err != nil {
		// Device is not enrolled — cloud login not available.
		writeErr(w, 503, "cloud login not configured on this device")
		return
	}

	verifier := NewCloudLoginVerifier(h.store, pub)
	info, err := verifier.Login(req.Token, req.Signature)
	if err != nil {
		h.limiter.RecordFailure(ip)
		switch err {
		case ErrTokenExpired:
			writeErr(w, 401, "cloud token has expired")
		case ErrBadSignature:
			writeErr(w, 401, "invalid cloud token signature")
		case ErrDeviceMismatch:
			writeErr(w, 401, "cloud token is not valid for this device")
		case ErrTokenReplay:
			writeErr(w, 401, "cloud token has already been used")
		case ErrNoBrokerPubkey:
			writeErr(w, 503, "cloud login not configured on this device")
		default:
			writeErr(w, 401, err.Error())
		}
		return
	}

	h.limiter.RecordSuccess(ip)

	// Set session cookie so the browser is authenticated from here on.
	http.SetCookie(w, sessionCookie(r, info.SessionToken))

	writeJSON(w, info)
}

// handleCloudStatus reports whether this device is enrolled with Vulos Cloud.
// Used by the login screen and install wizard to decide whether to show the
// cloud account option by default.
func (h *Handler) handleCloudStatus(w http.ResponseWriter, r *http.Request) {
	enrolled := IsCloudEnrolled()
	writeJSON(w, map[string]any{
		"enrolled": enrolled,
	})
}

func (h *Handler) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "unauthorized")
		return
	}
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if err := h.store.ChangePassword(userID, req.OldPassword, req.NewPassword); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	// WAVE2-RECOVERY: keep the master-key password wrap in sync with the login
	// credential. The bcrypt password already changed above; if the master-key
	// rewrap fails we log CRITICAL but do not fail the request — the phrase wrap is
	// unaffected, so recovery via phrase still restores access.
	if err := h.store.RewrapMasterKeyOnPasswordChange(userID, req.OldPassword, req.NewPassword); err != nil {
		log.Printf("[auth] CRITICAL: master-key rewrap on password change failed for %s: %v", userID, err)
	}
	h.store.Flush()
	writeJSON(w, map[string]string{"status": "password changed"})
}

// ─── WAVE2-RECOVERY: master-key endpoints ─────────────────────────────────────

// handleMasterKeyStatus reports whether the authenticated user has a master key.
func (h *Handler) handleMasterKeyStatus(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "unauthorized")
		return
	}
	writeJSON(w, map[string]any{"provisioned": h.store.HasMasterKey(userID)})
}

// handleMasterKeyEnvelope returns the password-wrapped envelope for the
// authenticated user so the browser can unwrap the master key CLIENT-SIDE. The
// phrase wrap is never returned here. The server holds only wrapped bytes.
func (h *Handler) handleMasterKeyEnvelope(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "unauthorized")
		return
	}
	env, err := h.store.PasswordEnvelope(userID)
	if err != nil {
		writeErr(w, 404, "no master key")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(env) //nolint:errcheck
}

// handleMasterKeyRecover is the public "forgot password" endpoint: it resets the
// login password AND re-wraps the master key using the 24-word recovery phrase.
// Possession of the phrase is the authorization; a wrong phrase fails closed.
func (h *Handler) handleMasterKeyRecover(w http.ResponseWriter, r *http.Request) {
	ip := extractIP(r)
	if h.limiter.IsBanned(ip) {
		writeErr(w, 429, "too many attempts")
		return
	}
	var req struct {
		Username    string `json:"username"`
		Mnemonic    string `json:"mnemonic"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if req.Username == "" || req.Mnemonic == "" || req.NewPassword == "" {
		writeErr(w, 422, "username, mnemonic and new_password are required")
		return
	}
	if err := h.store.RecoverAccountWithPhrase(req.Username, req.Mnemonic, req.NewPassword); err != nil {
		h.limiter.RecordFailure(ip)
		// Uniform failure — never reveal whether it was the username or the phrase.
		writeErr(w, 400, "recovery failed: check your recovery phrase and try again")
		return
	}
	h.limiter.RecordSuccess(ip)
	h.store.Flush()
	writeJSON(w, map[string]string{"status": "account recovered; sign in with your new password"})
}

// handleMasterKeyResetActive is the Tier-2 (active-session / trusted-device)
// password reset. It is SESSION-REQUIRED and deliberately NOT in publicPaths:
// authorization is the caller's existing logged-in session PLUS the client's
// possession of the in-memory master key. The client re-wraps THE SAME master key
// under the new password entirely client-side and posts only the resulting wrapped
// slot; this handler updates the password wrap + the login credential and revokes
// the user's OTHER sessions.
//
// It does NOT accept the OLD password (the whole point — the user forgot it) and
// NEVER sees the master key or the phrase; the phrase slot is untouched. If the
// session holds no master key (legacy account/login), it returns 409 so the client
// falls back to the recovery phrase (Tier-1) instead of silently failing.
//
// HONEST BOUNDARY: this authorizes a reset via an active session + the in-memory
// key, so a hijacked LIVE session could reset the password — the same risk surface
// as any logged-in session, which is acceptable and standard. It never weakens the
// zero-access property (the server still never holds the master key or phrase).
func (h *Handler) handleMasterKeyResetActive(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "unauthorized")
		return
	}
	var req struct {
		NewPassword  string          `json:"new_password"`
		PasswordSlot json.RawMessage `json:"password_slot"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	if req.NewPassword == "" || len(req.PasswordSlot) == 0 {
		writeErr(w, 422, "new_password and password_slot are required")
		return
	}
	keepToken := extractToken(r)
	if err := h.store.ResetPasswordWithSessionKey(userID, keepToken, req.PasswordSlot, req.NewPassword); err != nil {
		if errors.Is(err, ErrNoMasterKey) {
			// Legacy/keyless session — the active session alone cannot authorize a
			// zero-access reset. Tell the client to fall back to the recovery phrase.
			writeErr(w, 409, "no master key held by this session; use your recovery phrase")
			return
		}
		writeErr(w, 400, err.Error())
		return
	}
	h.store.Flush()
	// Keep the caller signed in: refresh their (unchanged) session cookie.
	if keepToken != "" {
		http.SetCookie(w, sessionCookie(r, keepToken))
	}
	writeJSON(w, map[string]string{"status": "password reset"})
}

func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	token := extractToken(r)
	if token == "" {
		writeErr(w, 401, "not authenticated")
		return
	}

	sess, ok := h.store.ValidateToken(token)
	if !ok {
		writeErr(w, 401, "invalid or expired session")
		return
	}

	user, ok := h.store.GetUser(sess.UserID)
	if !ok {
		writeErr(w, 404, "user not found")
		return
	}

	profile, ok := h.store.GetProfile(sess.UserID)
	var safeProfile any
	if ok && profile != nil {
		// Never leak the lock-screen PIN hash or the cleartext AI API key in /me.
		safeProfile = sanitizeProfile(profile)
	}
	writeJSON(w, map[string]any{
		"user":    user.Safe(),
		"session": map[string]any{"id": sess.ID, "user_id": sess.UserID, "expires_at": sess.ExpiresAt},
		"profile": safeProfile,
	})
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := extractToken(r)
	if token != "" {
		// Optional "sign out everywhere": ?all=true revokes every session for
		// the user, not just the current one.
		if r.URL.Query().Get("all") == "true" {
			if sess, ok := h.store.ValidateToken(token); ok {
				h.store.RevokeAllSessions(sess.UserID)
			} else {
				h.store.RevokeSession(token)
			}
		} else {
			h.store.RevokeSession(token)
		}
		h.store.Flush()
	}

	// Build the clearing cookie from the SAME attributes used when the cookie
	// was set (HttpOnly, Secure, SameSite, Domain, Path). A clearing cookie
	// that drops these may not match/replace the original in the browser,
	// leaving the session cookie in place. Only the value + MaxAge change.
	clear := sessionCookie(r, "")
	clear.MaxAge = -1
	http.SetCookie(w, clear)

	writeJSON(w, map[string]string{"status": "logged out"})
}

func extractToken(r *http.Request) string {
	// Check Authorization header first
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	// Fall back to cookie
	if c, err := r.Cookie("vulos_session"); err == nil {
		return c.Value
	}
	return ""
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}

// SetSessionCookie writes the vulos_session cookie to w using the token.
// It delegates to sessionCookie for cookie construction so all auth endpoints
// produce identical cookies. Exported so route files in cmd/server can use it.
func SetSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, sessionCookie(r, token))
}

// sessionCookie creates a consistent session cookie for all auth endpoints.
// Uses SameSite=None + Secure on HTTPS (required for subdomain iframes).
// Uses SameSite=Lax on HTTP (localhost dev without mkcert).
func sessionCookie(r *http.Request, token string) *http.Cookie {
	// Only trust X-Forwarded-Proto from loopback (reverse proxy on same host),
	// mirroring the loopback gate used in extractIP for X-Forwarded-For.
	remoteHost, _, _ := net.SplitHostPort(r.RemoteAddr)
	fromTrustedProxy := remoteHost == "127.0.0.1" || remoteHost == "::1"
	isSecure := r.TLS != nil || (fromTrustedProxy && r.Header.Get("X-Forwarded-Proto") == "https")
	sameSite := http.SameSiteLaxMode
	if isSecure {
		sameSite = http.SameSiteNoneMode
	}
	return &http.Cookie{
		Name:     "vulos_session",
		Value:    token,
		Path:     "/",
		Domain:   cookieDomain(r),
		MaxAge:   90 * 24 * 3600,
		HttpOnly: true,
		SameSite: sameSite,
		Secure:   isSecure,
	}
}

// cookieDomain returns the domain for session cookies.
//
// When VULOS_DOMAIN is set it contains the per-instance base domain
// (e.g. "01h5t3e8k2qj7r9xmvn4p.vulos.org" in production or "lvh.me" in
// dev). The returned value is prefixed with "." so the cookie is shared
// across all {app}--{profile} subdomains of that instance but not across
// different instances.
//
// When VULOS_DOMAIN is not set the function derives the cookie scope from the
// request Host. Subdomains that follow the {app}--{profile}.{base} pattern
// (identified by the "--" separator in the leading label) are stripped down
// to their per-instance base: browser--work.abc.vulos.org → .abc.vulos.org.
// Hosts with fewer than two DNS labels (e.g. "localhost") and bare IP
// addresses receive an empty domain so the cookie is scoped to that exact
// origin only.
func cookieDomain(r *http.Request) string {
	if d := os.Getenv("VULOS_DOMAIN"); d != "" {
		// Ensure the returned value is a parent-domain cookie scope.
		if !strings.HasPrefix(d, ".") {
			return "." + d
		}
		return d
	}

	host := r.Host
	// Strip port if present.
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		// Make sure we are not confusing an IPv6 address — a bare ":" after
		// the closing "]" is the port separator.
		if !strings.Contains(host[:idx], "]") || host[idx-1] == ']' {
			host = host[:idx]
		}
	}

	// Bare IP addresses (v4 or v6) — no domain scoping.
	if net.ParseIP(host) != nil {
		return ""
	}

	// "localhost" or any single-label name — no domain scoping.
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return ""
	}

	// If the first (leftmost) label contains "--" it is an {app}--{profile}
	// label. Strip it so the cookie covers all apps of this instance.
	// e.g. browser--work.abc123.vulos.org → .abc123.vulos.org
	if strings.Contains(parts[0], "--") {
		base := strings.Join(parts[1:], ".")
		if base == "" {
			return ""
		}
		return "." + base
	}

	// Plain subdomain (no "--"): scope cookie to the parent domain so that
	// simple {sub}.{domain} setups still work.
	// e.g. cockpit.lvh.me → .lvh.me
	return "." + strings.Join(parts[1:], ".")
}

// stateStore manages CSRF state tokens with expiry.
type stateStore struct {
	mu     sync.Mutex
	states map[string]*stateEntry
}

type stateEntry struct {
	createdAt time.Time
	meta      map[string]string
}

func newStateStore() *stateStore {
	return &stateStore{states: make(map[string]*stateEntry)}
}

func (s *stateStore) create() string {
	b := make([]byte, 16)
	rand.Read(b)
	state := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	s.states[state] = &stateEntry{createdAt: time.Now(), meta: map[string]string{}}
	s.mu.Unlock()
	return state
}

func (s *stateStore) validate(state string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.states[state]
	if !ok {
		return false
	}
	return time.Since(entry.createdAt) < 10*time.Minute
}

func (s *stateStore) setMeta(state, key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.states[state]; ok {
		e.meta[key] = value
	}
}

func (s *stateStore) getMeta(state, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.states[state]; ok {
		return e.meta[key]
	}
	return ""
}

func (s *stateStore) delete(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.states, state)
}

// --- Profile management handlers ---

// sanitizeProfile returns a COPY of p safe to serialize in an HTTP response. It
// strips two secrets that must never leave the box in a profile payload:
//
//   - PinHash — the lock-screen PIN hash is a single unstretched SHA-256 over a
//     short numeric PIN with the salt disclosed inline (see hashPIN), so leaking
//     it is effectively leaking the PIN. It is ALWAYS cleared.
//   - AIAPIKey — the user's model API key. It is masked (not cleared) when set so
//     the UI can show "a key is configured" without ever revealing it.
//
// It copies by value first so the live in-memory *Profile in the store is never
// mutated (GetProfile hands back the stored pointer). Callers must serialize the
// returned value, never the original pointer.
func sanitizeProfile(p *Profile) Profile {
	cp := *p
	cp.PinHash = ""
	if cp.AIAPIKey != "" {
		cp.AIAPIKey = "••••••"
	}
	return cp
}

func (h *Handler) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	// C2: Require an authenticated admin session.
	reqUserID := r.Header.Get("X-User-ID")
	if reqUserID == "" {
		writeErr(w, 401, "unauthorized")
		return
	}
	reqProfile, _ := h.store.GetProfile(reqUserID)
	if reqProfile == nil || reqProfile.Role != RoleAdmin {
		writeErr(w, 403, "admin only")
		return
	}

	// C2 + PIN-LEAK: scrub AIAPIKey AND the lock-screen PinHash from every
	// profile before returning (an admin listing all profiles must not receive
	// every user's PIN hash / API key).
	raw := h.store.ListProfiles()
	scrubbed := make([]Profile, 0, len(raw))
	for _, p := range raw {
		scrubbed = append(scrubbed, sanitizeProfile(p))
	}
	writeJSON(w, scrubbed)
}

func (h *Handler) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userId")

	// Only the profile owner or an admin may read a profile.
	reqUserID := r.Header.Get("X-User-ID")
	if reqUserID == "" {
		writeErr(w, 401, "not authenticated")
		return
	}
	if reqUserID != userID {
		reqProfile, _ := h.store.GetProfile(reqUserID)
		if reqProfile == nil || reqProfile.Role != RoleAdmin {
			writeErr(w, 403, "can only read your own profile")
			return
		}
	}

	profile, ok := h.store.GetProfile(userID)
	if !ok {
		writeErr(w, 404, "profile not found")
		return
	}
	// Scrub API key AND lock-screen PIN hash from the response.
	writeJSON(w, sanitizeProfile(profile))
}

func (h *Handler) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userId")

	// Only the user or an admin can update a profile
	reqUserID := r.Header.Get("X-User-ID")
	if reqUserID == "" {
		writeErr(w, 401, "not authenticated")
		return
	}
	if reqUserID != userID {
		reqProfile, _ := h.store.GetProfile(reqUserID)
		if reqProfile == nil || reqProfile.Role != RoleAdmin {
			writeErr(w, 403, "can only update your own profile")
			return
		}
	}

	existing, ok := h.store.GetProfile(userID)
	if !ok {
		writeErr(w, 404, "profile not found")
		return
	}

	var update struct {
		DisplayName *string `json:"display_name"`
		Theme       *string `json:"theme"`
		Locale      *string `json:"locale"`
		Timezone    *string `json:"timezone"`
		AIProvider  *string `json:"ai_provider"`
		AIModel     *string `json:"ai_model"`
		AIAPIKey    *string `json:"ai_api_key"`
		Initiative  *string `json:"initiative"`
	}
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}

	if update.DisplayName != nil {
		existing.DisplayName = *update.DisplayName
	}
	if update.Theme != nil {
		existing.Theme = *update.Theme
	}
	if update.Locale != nil {
		existing.Locale = *update.Locale
	}
	if update.Timezone != nil {
		existing.Timezone = *update.Timezone
	}
	if update.AIProvider != nil {
		existing.AIProvider = *update.AIProvider
	}
	if update.AIModel != nil {
		existing.AIModel = *update.AIModel
	}
	if update.AIAPIKey != nil {
		existing.AIAPIKey = *update.AIAPIKey
	}
	if update.Initiative != nil {
		existing.Initiative = *update.Initiative
	}

	h.store.SetProfile(existing)
	h.store.Flush()
	// Return a scrubbed copy: never echo the PIN hash or the cleartext API key
	// back in the response (existing is the live stored profile).
	writeJSON(w, sanitizeProfile(existing))
}

func (h *Handler) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	reqUserID := r.Header.Get("X-User-ID")
	reqProfile, _ := h.store.GetProfile(reqUserID)
	if reqProfile == nil || reqProfile.Role != RoleAdmin {
		writeErr(w, 403, "admin only")
		return
	}

	userID := r.PathValue("userId")
	if userID == reqUserID {
		writeErr(w, 400, "cannot delete your own profile")
		return
	}

	if err := h.store.DeleteProfile(userID); err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	h.store.Flush()
	writeJSON(w, map[string]string{"status": "deleted"})
}

func (h *Handler) handleSetRole(w http.ResponseWriter, r *http.Request) {
	reqUserID := r.Header.Get("X-User-ID")
	reqProfile, _ := h.store.GetProfile(reqUserID)
	if reqProfile == nil || reqProfile.Role != RoleAdmin {
		writeErr(w, 403, "admin only")
		return
	}

	userID := r.PathValue("userId")
	var req struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}

	role := Role(req.Role)
	if role != RoleAdmin && role != RoleUser && role != RoleGuest {
		writeErr(w, 400, "invalid role: admin, user, or guest")
		return
	}

	if err := h.store.SetRole(userID, role); err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	h.store.Flush()

	// Sync Linux group membership
	if h.OnRoleChanged != nil {
		if user, ok := h.store.GetUser(userID); ok && user != nil {
			h.OnRoleChanged(user.Username, string(role))
		}
	}

	writeJSON(w, map[string]string{"status": "role updated"})
}

// --- Security portal handlers ---

func (h *Handler) handleListBans(w http.ResponseWriter, r *http.Request) {
	reqProfile, _ := h.store.GetProfile(r.Header.Get("X-User-ID"))
	if reqProfile == nil || reqProfile.Role != RoleAdmin {
		writeErr(w, 403, "admin only")
		return
	}
	writeJSON(w, h.limiter.BannedIPs())
}

func (h *Handler) handleUnban(w http.ResponseWriter, r *http.Request) {
	reqProfile, _ := h.store.GetProfile(r.Header.Get("X-User-ID"))
	if reqProfile == nil || reqProfile.Role != RoleAdmin {
		writeErr(w, 403, "admin only")
		return
	}
	var req struct {
		IP string `json:"ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	h.limiter.Unban(req.IP)
	writeJSON(w, map[string]string{"status": "unbanned"})
}

func (h *Handler) handleSecurityStats(w http.ResponseWriter, r *http.Request) {
	reqProfile, _ := h.store.GetProfile(r.Header.Get("X-User-ID"))
	if reqProfile == nil || reqProfile.Role != RoleAdmin {
		writeErr(w, 403, "admin only")
		return
	}
	writeJSON(w, h.limiter.Stats())
}

// --- PIN handlers ---

func (h *Handler) handleSetPIN(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "not authenticated")
		return
	}
	var req struct {
		PIN string `json:"pin"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if err := h.store.SetPIN(userID, req.PIN); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	h.store.Flush()
	writeJSON(w, map[string]string{"status": "pin set"})
}

func (h *Handler) handleValidatePIN(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "not authenticated")
		return
	}
	// Brute-force protection — same pattern as handleDevicePINUnlock and
	// handleFingerprintVerify; a PIN is typically 4-6 digits so a silent
	// per-IP retry loop can exhaust the space quickly without this guard.
	ip := extractIP(r)
	if h.limiter.IsBanned(ip) {
		writeErr(w, 429, "too many failed attempts")
		return
	}
	var req struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	valid := h.store.ValidatePIN(userID, req.PIN)
	if !valid {
		h.limiter.RecordFailure(ip)
	} else {
		h.limiter.RecordSuccess(ip)
	}
	writeJSON(w, map[string]any{"valid": valid, "has_pin": h.store.HasPIN(userID)})
}

func generateRandomPassword() string {
	b := make([]byte, 24)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ─── CLOGIN-06: device-local PIN handlers ────────────────────────────────────

// handleDevicePINSet sets or clears the device-local PIN.
// Requires a full-auth session (X-User-ID must be set).
// Body: { "pin": "1234" }  — empty string removes the PIN.
func (h *Handler) handleDevicePINSet(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "full-auth session required to set PIN")
		return
	}

	if h.DevicePIN == nil {
		writeErr(w, 503, "device PIN service not configured")
		return
	}

	var req struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}

	// Obtain the current session token so it can be wrapped by the PIN.
	token := extractToken(r)
	if req.PIN != "" && token == "" {
		writeErr(w, 401, "no active session to wrap")
		return
	}

	if err := h.DevicePIN.SetPIN(req.PIN, token); err != nil {
		writeErr(w, 400, err.Error())
		return
	}

	log.Printf("[devicepin] PIN %s for user %s", map[bool]string{true: "cleared", false: "set"}[req.PIN == ""], userID)
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleDevicePINUnlock attempts to unlock using a device PIN.
// This endpoint is PUBLIC — the user is on the lock screen and has no session.
// On success it sets the session cookie and returns { "ok": true }.
// On failure it returns a lockout sub-object describing the current state.
func (h *Handler) handleDevicePINUnlock(w http.ResponseWriter, r *http.Request) {
	ip := extractIP(r)
	if h.limiter.IsBanned(ip) {
		writeErr(w, 429, "too many attempts")
		return
	}

	if h.DevicePIN == nil {
		writeErr(w, 503, "device PIN service not configured")
		return
	}

	var req struct {
		PIN string `json:"pin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}

	sessionToken, err := h.DevicePIN.ValidatePIN(req.PIN)
	if err != nil {
		h.limiter.RecordFailure(ip)
		st := h.DevicePIN.Status()
		code := 401
		switch err {
		case ErrPINPermanentLocked:
			code = 403
		case ErrPINLocked:
			code = 423
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		enc, _ := json.Marshal(map[string]any{
			"error":   err.Error(),
			"lockout": st,
		})
		w.Write(enc) //nolint:errcheck
		return
	}

	// Verify the wrapped session token is still valid.
	sess, ok := h.store.ValidateToken(sessionToken)
	if !ok {
		// Session expired — PIN is stale; force full re-auth.
		writeErr(w, 401, "PIN session expired — please sign in with your password")
		return
	}

	h.limiter.RecordSuccess(ip)
	http.SetCookie(w, sessionCookie(r, sess.Token))
	writeJSON(w, map[string]any{"ok": true})
}

// handleDevicePINStatus returns the current lockout status without modifying state.
// PUBLIC — shown on the lock screen before the user has a session.
func (h *Handler) handleDevicePINStatus(w http.ResponseWriter, r *http.Request) {
	if h.DevicePIN == nil {
		writeJSON(w, map[string]any{"has_pin": false})
		return
	}
	st := h.DevicePIN.Status()
	writeJSON(w, map[string]any{
		"has_pin":        h.DevicePIN.HasPIN(),
		"locked":         st.Locked,
		"permanent_lock": st.PermanentLock,
		"attempts_left":  st.AttemptsLeft,
		"locked_until":   st.LockedUntil,
		"lockout_count":  st.LockoutCount,
	})
}

// ─── CLOGIN-07: fingerprint handlers ─────────────────────────────────────────

// handleFingerprintStatus returns the fingerprint capability/enrollment status.
// PUBLIC — shown on the lock screen and in Settings before the user authenticates.
func (h *Handler) handleFingerprintStatus(w http.ResponseWriter, r *http.Request) {
	if h.Fingerprint == nil {
		writeJSON(w, FingerprintStatus{Available: false})
		return
	}
	profileID := r.URL.Query().Get("profile")
	writeJSON(w, h.Fingerprint.Status(profileID))
}

// handleFingerprintEnrollStart initiates fprintd enrollment for the authenticated user.
// Requires a full-auth session.
func (h *Handler) handleFingerprintEnrollStart(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "full-auth session required to enroll fingerprint")
		return
	}
	if h.Fingerprint == nil {
		writeErr(w, 503, "fingerprint service not configured")
		return
	}
	if err := h.Fingerprint.StartEnroll(userID); err != nil {
		switch err {
		case ErrFingerprintUnavailable:
			writeErr(w, 503, err.Error())
		default:
			writeErr(w, 500, err.Error())
		}
		return
	}
	writeJSON(w, map[string]string{"status": "enroll_started"})
}

// handleFingerprintEnrollStop stops an ongoing enrollment and commits the result.
// Requires a full-auth session.
func (h *Handler) handleFingerprintEnrollStop(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "full-auth session required")
		return
	}
	if h.Fingerprint == nil {
		writeErr(w, 503, "fingerprint service not configured")
		return
	}
	if err := h.Fingerprint.StopEnroll(userID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, h.Fingerprint.Status(userID))
}

// handleFingerprintVerify performs a fingerprint verification for the given profile.
// PUBLIC — the user is on the lock screen.  On success, sets the session cookie.
func (h *Handler) handleFingerprintVerify(w http.ResponseWriter, r *http.Request) {
	ip := extractIP(r)
	if h.limiter.IsBanned(ip) {
		writeErr(w, 429, "too many attempts")
		return
	}
	if h.Fingerprint == nil {
		writeErr(w, 503, "fingerprint service not configured")
		return
	}

	var req struct {
		ProfileID string `json:"profile_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request")
		return
	}

	sessionToken, err := h.Fingerprint.Verify(req.ProfileID)
	if err != nil {
		h.limiter.RecordFailure(ip)
		code := 401
		switch err {
		case ErrFingerprintUnavailable:
			code = 503
		case ErrFingerprintMaxFails:
			code = 423
		case ErrFingerprintNotEnrolled:
			code = 404
		case ErrFingerprintTimeout:
			code = 504
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		enc, _ := json.Marshal(map[string]any{
			"error":  err.Error(),
			"status": h.Fingerprint.Status(req.ProfileID),
		})
		w.Write(enc) //nolint:errcheck
		return
	}

	// Validate the wrapped session token.
	sess, ok := h.store.ValidateToken(sessionToken)
	if !ok {
		writeErr(w, 401, "fingerprint session expired — please sign in with your password")
		return
	}

	h.limiter.RecordSuccess(ip)
	http.SetCookie(w, sessionCookie(r, sess.Token))
	writeJSON(w, map[string]any{"ok": true})
}

// handleFingerprintRemove disables fingerprint unlock for the authenticated user.
// Requires a full-auth session.
func (h *Handler) handleFingerprintRemove(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeErr(w, 401, "full-auth session required to remove fingerprint")
		return
	}
	if h.Fingerprint == nil {
		writeErr(w, 503, "fingerprint service not configured")
		return
	}
	if err := h.Fingerprint.RemoveEnrollment(userID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	log.Printf("[fingerprint] enrollment removed for user %s", userID)
	writeJSON(w, map[string]string{"status": "removed"})
}
