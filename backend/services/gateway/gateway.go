package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"sync"
	"time"

	storagepkg "vulos/backend/internal/storage"
	"vulos/backend/services/appnet"
	"vulos/backend/services/auth"
)

// Gateway is the auth-enforcing reverse proxy for all app traffic.
// Apps run in isolated namespaces and are NEVER exposed directly to the browser.
// All requests go through: Browser → :8080/app/{appId}/* → [auth] → namespace:port
//
// Third-party apps don't need any integration. They just serve HTTP.
// The gateway injects user identity headers so apps that want to be
// user-aware can read X-Vulos-User-ID etc, but it's optional.
type Gateway struct {
	mu         sync.RWMutex
	authStore  *auth.Store
	netMgr     *appnet.Manager
	portPool   *appnet.PortPool
	appSecrets map[string]string // appId → secret token
	appHits    map[string]*rateBucket
	client     *http.Client

	// integrationTokenFunc, when set, mints a short-lived per-user third-party access
	// token for (ctx, provider). Apps that declare the matching integration
	// permission have it injected as an X-Vulos-Integration-<Provider> request
	// header (INTEG-04). nil disables injection entirely.
	// H3 fix: userID added so tokens are scoped per user, not per box.
	integrationTokenFunc func(ctx context.Context, provider, userID string) (string, error)
	// integrationApps maps appID → set of providers that app may receive.
	integrationApps map[string]map[string]bool

	// storageResolver, when set, yields the per-user object-store binding for the
	// current request's user, scoped to the supplied prefix ("<userID>/<appID>/").
	// Apps that declare the "storage" permission have it injected as
	// X-Vulos-Storage-* request headers (the INTEG-04 pattern applied to storage).
	// nil disables injection entirely.
	storageResolver func(ctx context.Context, userID, prefix string) (storagepkg.Resolution, bool)
	// storageApps maps appID → its key prefix component within the per-user
	// namespace (e.g. "office/"). The gateway prepends "<userID>/" before
	// injecting (C2), so the effective prefix is "<userID>/<appID>/". Only listed
	// apps receive storage headers.
	storageApps map[string]string
	// storageBrokerSecret authenticates the gateway to consuming apps (H2/H3):
	// when set it is emitted as X-Vulos-Storage-Broker-Auth alongside the storage
	// headers, and apps reject seam headers unless it matches their own copy of
	// VULOS_STORAGE_BROKER_SECRET. When EMPTY, the gateway refuses to inject any
	// storage credentials (fail-closed).
	storageBrokerSecret string

	// grantBroker mints short-lived, OBJECT-scoped storage grants (presigned
	// URL or object-scoped STS) for the PRESIGN-01 endpoint (PresignHandler).
	// This is how storage-permitted apps get usable storage access in cloud
	// mode (Tigris has no bucket-wide STS/AssumeRole) WITHOUT ever holding a
	// raw AccessKey/Secret: the app requests a presign per object instead. nil
	// disables the endpoint (503).
	grantBroker *storagepkg.GrantBroker
	// bucketForFunc resolves a userID to its account bucket name, used by the
	// presign endpoint to build the (bucket, key) pair passed to grantBroker.
	bucketForFunc func(userID string) string

	// appProducts maps appID → the CP product/entitlement key required to use
	// it (e.g. "office-pro"). Empty/absent means the app requires no
	// entitlement (open to any authenticated user) — today's behavior for
	// every app that does not declare one. Populated from the app manifest's
	// "product" field.
	appProducts map[string]string
	// gateEntitlements, when true, enforces appProducts against the current
	// request's vk_-introspected products (X-Vulos-Entitlements-Products,
	// stamped by auth.Handler.Middleware) and FAILS CLOSED when a required
	// product is missing. False (self-host/standalone) leaves every app open,
	// matching today's all-open behavior. Set via SetEntitlementGating.
	gateEntitlements bool

	// sessionSalt keys the per-app session correlator (M3). It is a random,
	// per-process value so the X-Vulos-Session header injected to an app is
	// HMAC(sessionSalt, sessionID || appID) rather than the raw session id —
	// two different apps therefore see two UNLINKABLE opaque correlators for the
	// same logged-in user, so a set of colluding apps can no longer join their
	// logs on a shared session identifier, and no app ever receives the raw
	// session token (which is also a bearer credential).
	sessionSalt []byte

	// publicVisibility, when set, reports whether appID is published to the
	// public web (visibility == "public"). It gates PublicHandler: the anonymous
	// public entrypoint serves an app ONLY when this returns true, so private /
	// local apps can never be reached over the public-web path even if the edge
	// (Caddy/nginx) is pointed at the gateway. nil ⇒ nothing is public.
	publicVisibility func(appID string) bool

	// reverseProxy is the shared streaming reverse proxy for the authenticated
	// app hot path (see proxy.go). One instance, one pooled transport, one buffer
	// pool — reused across every request. All security transforms live in its
	// Director / ModifyResponse.
	reverseProxy *httputil.ReverseProxy

	// activator (LAUNCH-01), when set, starts an app that is installed but not
	// running, on the request that needs it. nil keeps the pre-LAUNCH-01
	// behaviour: a namespace miss is a flat 404. See activate.go for why the
	// launch lives here rather than in the shell.
	activator Activator
	// activeMu/activeFlights single-flight concurrent activations of the same
	// (app, user, profile). Held on its own mutex, never g.mu, so a launch in
	// progress cannot block reads of the grant maps on the request hot path.
	activeMu      sync.Mutex
	activeFlights map[string]*inFlight
}

// rateBucket tracks request count per window for per-app rate limiting.
type rateBucket struct {
	count    int
	windowAt time.Time
}

func New(authStore *auth.Store, netMgr *appnet.Manager, portPool *appnet.PortPool) *Gateway {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		// rand.Read never fails on supported platforms; if it somehow does, fall
		// back to a time-seeded value so the correlator is still non-empty (it
		// only needs to be unpredictable-per-process, not cryptographically
		// perfect — it protects correlation, not a secret).
		salt = []byte(fmt.Sprintf("vulos-session-salt-%d", time.Now().UnixNano()))
	}
	// One shared, pooled transport for BOTH the plain client (health checks,
	// public path) and the reverse proxy — keep-alive + a bounded idle pool keeps
	// upstream connections warm instead of dialing per request.
	sharedTransport := newSharedTransport()
	g := &Gateway{
		authStore:   authStore,
		netMgr:      netMgr,
		portPool:    portPool,
		appSecrets:  make(map[string]string),
		appHits:     make(map[string]*rateBucket),
		sessionSalt: salt,
		client: &http.Client{
			Timeout:   60 * time.Second,
			Transport: sharedTransport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse // don't follow redirects — let the browser handle them
			},
		},
	}
	// The reverse proxy uses a retrying round-tripper over the same pooled
	// transport so a just-launched app still serves the first request.
	g.reverseProxy = g.newReverseProxy(&retryRoundTripper{
		base:     sharedTransport,
		attempts: 3,
		delay:    time.Second,
	})

	// Periodically clean stale rate limit buckets
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			g.mu.Lock()
			now := time.Now()
			for id, b := range g.appHits {
				if now.Sub(b.windowAt) > 5*time.Second {
					delete(g.appHits, id)
				}
			}
			g.mu.Unlock()
		}
	}()

	return g
}

// SetIntegrationTokenFunc installs the third-party token minter (INTEG-04).
// Pass nil to disable integration-token injection.
// H3 fix: fn now receives userID so each user gets their own oauth token.
func (g *Gateway) SetIntegrationTokenFunc(fn func(ctx context.Context, provider, userID string) (string, error)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.integrationTokenFunc = fn
}

// AllowIntegration grants appID permission to receive the provider's access
// token (derived from the app manifest's declared integrations).
func (g *Gateway) AllowIntegration(appID, provider string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.integrationApps == nil {
		g.integrationApps = make(map[string]map[string]bool)
	}
	if g.integrationApps[appID] == nil {
		g.integrationApps[appID] = make(map[string]bool)
	}
	g.integrationApps[appID][provider] = true
}

// stripInboundVulosHeaders removes ALL client-supplied X-Vulos-* headers from h
// so a client can never spoof identity, integration, storage, session or
// broker-auth headers to an upstream app (H1). The gateway re-injects only the
// trusted values afterwards. Used on both the HTTP and WebSocket paths.
func stripInboundVulosHeaders(h http.Header) {
	for k := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "X-Vulos-") {
			h.Del(k)
		}
	}
}

// applyTrustedHeaders strips all inbound X-Vulos-* headers and re-injects the
// trusted identity, integration and storage headers. Shared by the HTTP proxy
// and WebSocket (H1) paths so neither can leak or be spoofed.
func (g *Gateway) applyTrustedHeaders(ctx context.Context, pr *http.Request, session *auth.Session, appID string) {
	stripInboundVulosHeaders(pr.Header)
	pr.Header.Set("X-Vulos-User-ID", session.UserID)
	pr.Header.Set("X-Vulos-Email", session.Email)
	// M3: never hand the raw session id (a bearer credential AND a cross-app
	// correlator) to an app. Inject a per-app, per-process derived correlator so
	// a single logged-in user is a STABLE id within one app (for the app's own
	// session bookkeeping) but an UNLINKABLE opaque value across apps.
	pr.Header.Set("X-Vulos-Session", g.perAppSession(session.ID, appID))
	pr.Header.Set("X-Vulos-App-ID", appID)
	pr.Header.Del("Cookie")
	g.injectIntegrationTokens(ctx, pr, session.UserID, appID)
	g.injectStorageHeaders(ctx, pr, session.UserID, appID)
}

// perAppSession derives the opaque per-app session correlator injected as
// X-Vulos-Session. It is HMAC-SHA256(sessionSalt, sessionID || 0x00 || appID),
// hex-encoded and truncated: deterministic for a given (session, app) pair so an
// app can key its own per-session state, but not reversible to the real session
// token and not equal across apps for the same session (M3 — cross-app
// correlation + raw-credential leak fix).
func (g *Gateway) perAppSession(sessionID, appID string) string {
	if sessionID == "" {
		return ""
	}
	mac := hmac.New(sha256.New, g.sessionSalt)
	mac.Write([]byte(sessionID))
	mac.Write([]byte{0x00})
	mac.Write([]byte(appID))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// SetPublicVisibility installs the callback that reports whether an app is
// published to the public web (visibility == "public"). It gates PublicHandler.
// Pass nil (the default) to keep every app private on the public-web path.
func (g *Gateway) SetPublicVisibility(fn func(appID string) bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.publicVisibility = fn
}

// injectIntegrationTokens adds X-Vulos-Integration-<Provider> headers for every
// provider appID is permitted to receive. Mint failures (not connected, cloud
// unavailable) are swallowed — the app simply sees no token and degrades.
// H3 fix: userID is threaded through so tokens are scoped per user.
func (g *Gateway) injectIntegrationTokens(ctx context.Context, pr *http.Request, userID, appID string) {
	g.mu.RLock()
	fn := g.integrationTokenFunc
	providers := g.integrationApps[appID]
	g.mu.RUnlock()
	if fn == nil || len(providers) == 0 {
		return
	}
	// Strip any client-supplied integration headers to prevent spoofing.
	for k := range pr.Header {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "X-Vulos-Integration-") {
			pr.Header.Del(k)
		}
	}
	for provider := range providers {
		tok, err := fn(ctx, provider, userID)
		if err != nil || tok == "" {
			continue
		}
		pr.Header.Set("X-Vulos-Integration-"+integrationHeaderSuffix(provider), tok)
	}
}

// SetStorageResolver installs the per-user storage resolver. Pass nil to
// disable X-Vulos-Storage-* injection. The gateway computes the full per-user
// prefix ("<userID>/<appID>/") and passes it so the resolver can mint
// prefix-scoped credentials (C1/C3); the returned Resolution is injected as-is.
func (g *Gateway) SetStorageResolver(fn func(ctx context.Context, userID, prefix string) (storagepkg.Resolution, bool)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.storageResolver = fn
}

// SetStorageBrokerSecret sets the shared secret emitted as
// X-Vulos-Storage-Broker-Auth (H2/H3). When empty, the gateway fails closed and
// injects no storage credentials at all.
func (g *Gateway) SetStorageBrokerSecret(secret string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.storageBrokerSecret = secret
}

// AllowStorage grants appID storage-header injection under prefix (its key
// prefix in the shared per-user bucket, e.g. "office/"). Derived from the app
// manifest's "storage" permission.
func (g *Gateway) AllowStorage(appID, prefix string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.storageApps == nil {
		g.storageApps = make(map[string]string)
	}
	g.storageApps[appID] = prefix
}

// injectStorageHeaders stamps X-Vulos-Storage-* headers for appID when it is
// permitted (declares the "storage" permission). Inbound X-Vulos-Storage-*
// headers are ALWAYS stripped first — even for unpermitted apps — so a client
// can never spoof storage credentials into an app. When the resolver reports no
// object store (empty Endpoint), the empty headers are still set so the app
// detects the signal and falls back to local/standalone storage.
func (g *Gateway) injectStorageHeaders(ctx context.Context, pr *http.Request, userID, appID string) {
	// Anti-spoof: strip any client-supplied storage / broker-auth headers
	// unconditionally — even for unpermitted apps and even when we won't inject.
	for k := range pr.Header {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "X-Vulos-Storage-") {
			pr.Header.Del(k)
		}
	}

	g.mu.RLock()
	fn := g.storageResolver
	prefix, allowed := g.storageApps[appID]
	secret := g.storageBrokerSecret
	g.mu.RUnlock()
	if fn == nil || !allowed {
		return
	}
	// H2/H3 fail-closed: without a broker secret the consuming app cannot
	// authenticate the seam, so never hand out storage credentials.
	if secret == "" {
		return
	}

	// C2: per-user prefix so isolation never depends on app behavior. The
	// effective prefix is "<userID>/<appID>/"; the resolver scopes credentials
	// to it (C1/C3) when a minter is configured.
	fullPrefix := prefix
	if userID != "" {
		fullPrefix = userID + "/" + prefix
	}

	res, ok := fn(ctx, userID, fullPrefix)
	if !ok {
		return
	}
	res = res.WithPrefix(fullPrefix)

	// SECURITY (fail-closed, per-app isolation): an object store IS configured
	// (there ARE credentials to protect) but they are NOT scoped down to this
	// app's prefix (no STS minter available, or minting failed, or — in cloud
	// mode — the resolution is inherently unscoped, e.g. Tigris has no STS).
	// Refuse to inject ANYTHING rather than hand out a static/unscoped
	// full-bucket credential. The app must use the presign endpoint
	// (PresignHandler) instead, which mints object-scoped grants regardless of
	// STS availability.
	if res.Configured() && !res.Scoped {
		log.Printf("[storage] refusing to inject unscoped credentials for app %q (user %q): no per-app scoping is available — use the presign endpoint instead", appID, userID)
		return
	}

	pr.Header.Set("X-Vulos-Storage-Endpoint", res.Endpoint)
	pr.Header.Set("X-Vulos-Storage-Bucket", res.Bucket)
	pr.Header.Set("X-Vulos-Storage-Prefix", res.Prefix)
	pr.Header.Set("X-Vulos-Storage-Region", res.Region)
	pr.Header.Set("X-Vulos-Storage-Access-Key", res.AccessKey)
	pr.Header.Set("X-Vulos-Storage-Secret-Key", res.SecretKey)
	if res.SessionToken != "" {
		pr.Header.Set("X-Vulos-Storage-Session-Token", res.SessionToken)
	}
	// H2/H3: authenticate the broker to the app.
	pr.Header.Set("X-Vulos-Storage-Broker-Auth", secret)
}

// integrationHeaderSuffix title-cases a provider id for the header name
// (e.g. "google" → "Google" → X-Vulos-Integration-Google).
func integrationHeaderSuffix(provider string) string {
	if provider == "" {
		return ""
	}
	return strings.ToUpper(provider[:1]) + provider[1:]
}

// GenerateAppSecret creates a secret for an app (injected as env var on launch).
func (g *Gateway) GenerateAppSecret(appID string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	b := make([]byte, 16)
	rand.Read(b)
	secret := hex.EncodeToString(b)
	g.appSecrets[appID] = secret
	return secret
}

// RemoveAppSecret cleans up when app stops.
func (g *Gateway) RemoveAppSecret(appID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.appSecrets, appID)
}

// AppSecretValid reports whether secret is the CURRENT credential the
// gateway issued to appID via GenerateAppSecret. That secret is injected as
// VULOS_APP_SECRET into ONLY that app's own process environment at launch
// (main.go) — it is never exposed to the browser and never exposed to any
// OTHER app. PresignHandler and DeleteHandler (PRESIGN-02/DELETE-02) use this
// to bind a storage request to the app instance that is ACTUALLY making it,
// instead of trusting the request body's "app_id" field: any app can put any
// app_id it likes in its own JSON body, but it can never produce a secret it
// was never issued, so this closes the cross-app storage-prefix hole where a
// compromised/malicious app named a DIFFERENT app's app_id to mint itself a
// grant under that other app's prefix. Uses a constant-time comparison to
// avoid leaking secret material through timing.
func (g *Gateway) AppSecretValid(appID, secret string) bool {
	if appID == "" || secret == "" {
		return false
	}
	g.mu.RLock()
	want, ok := g.appSecrets[appID]
	g.mu.RUnlock()
	if !ok || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(secret)) == 1
}

// subdomainCallerAppID derives the calling app's id from the request Host the
// SAME way Handler() does for ordinary proxied app traffic (NET-01 subdomain
// parsing) — used as a defense-in-depth cross-check by PresignHandler and
// DeleteHandler when the request happens to carry a recognisable app
// subdomain. Returns ("", false) when the host is not a recognised app
// subdomain (e.g. no VULOS_DOMAIN configured, or a plain path-prefix/base-
// domain request) — callers must treat that as "no signal available", not as
// "no app", since most self-host deployments never populate this.
func subdomainCallerAppID(r *http.Request) (string, bool) {
	host := r.Host
	if idx := strings.Index(host, ":"); idx > 0 {
		host = host[:idx]
	}
	baseDomain := os.Getenv("VULOS_DOMAIN")
	if baseDomain == "" || !strings.HasSuffix(strings.ToLower(host), "."+strings.ToLower(baseDomain)) {
		return "", false
	}
	appID, _, ok := appnet.ParseSubdomain(host, baseDomain)
	return appID, ok
}

// RemoveAppGrants clears every manifest-derived grant appID holds: its
// storage prefix (AllowStorage), integration providers (AllowIntegration),
// and required product (AllowApp). Call this on uninstall so a later
// reinstall of a DIFFERENT app under the same appID starts from a clean
// slate — without this, stale grants from the previous app would silently
// carry over (e.g. a reinstalled app that no longer declares "storage"
// would still receive storage headers, or one that no longer requires a
// product would still be entitlement-gated, until the next process
// restart).
func (g *Gateway) RemoveAppGrants(appID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.storageApps, appID)
	delete(g.integrationApps, appID)
	delete(g.appProducts, appID)
}

// Handler returns the HTTP handler for app traffic.
// Supports two modes:
//   - Subdomain: cockpit.localhost:8080/* → namespace (app gets full path, all apps work)
//   - Path prefix: /app/cockpit/* → namespace (legacy fallback)
func (g *Gateway) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var appID, appPath string

		// ORIGIN-01: per-app origins. When the deployment can serve them (a real
		// base domain whose wildcard DNS/TLS covers {app}--{profile}.{base}), an
		// app is addressed by its OWN origin and the shell's origin must never
		// serve app content. See services/appnet/origin.go for availability rules.
		baseDomain := appnet.BaseDomain()
		originsOn := appnet.Enabled(baseDomain)
		scheme := requestScheme(r)
		_, reqPort := appnet.SplitHostPort(r.Host)

		// Try subdomain: {appId}.lvh.me or {appId}.vulos.example.com
		// With NET-02, subdomain may carry a profile prefix:
		//   {profile}--{appId}.{baseDomain}  →  profile=profile, appID=appId
		//   {appId}.{baseDomain}             →  profile=default, appID=appId
		var net02Profile string
		var onAppOrigin bool
		host := r.Host
		if idx := strings.Index(host, ":"); idx > 0 {
			host = host[:idx]
		}
		if baseDomain != "" && strings.HasSuffix(strings.ToLower(host), "."+strings.ToLower(baseDomain)) {
			// ParseSubdomain (NET-01) handles both plain and profile-prefixed subdomains:
			//   {profile}--{appId}.{baseDomain}  →  profile=profile, appID=appId
			//   {appId}.{baseDomain}             →  profile="default", appID=appId
			if parsedApp, parsedProfile, ok := appnet.ParseSubdomain(host, baseDomain); ok {
				appID = parsedApp
				net02Profile = parsedProfile
				onAppOrigin = true
			} else {
				// ORIGIN-01: the host is under the base domain but its label does not
				// parse as a valid app label. Previously this "degraded gracefully" by
				// trusting the raw label as an app id — which would serve an app from a
				// host we cannot reproduce as a canonical origin (origin confusion).
				// Refuse instead: an app is only ever served from a label we ourselves
				// would mint.
				http.Error(w, `{"error":"unknown app origin"}`, http.StatusNotFound)
				return
			}
			appPath = r.URL.Path
		}

		// Fallback: /app/{appId}/path
		if appID == "" {
			path := strings.TrimPrefix(r.URL.Path, "/app/")
			slashIdx := strings.Index(path, "/")
			if slashIdx == -1 {
				appID = path
				appPath = "/"
			} else {
				appID = path[:slashIdx]
				appPath = path[slashIdx:]
			}
		}

		if appID == "" {
			http.Error(w, `{"error":"missing app id"}`, 400)
			return
		}

		// ORIGIN-01 (origin confusion, part 1 of 2): when per-app origins are
		// available, the path-prefix route on the SHELL's origin must not serve app
		// content — a document served there executes on the shell's origin and can
		// read the shell's storage and cookies directly, which is the entire hole
		// this change closes. Send the caller to the app's own origin instead.
		//
		// A redirect (not a 403) keeps every existing deep link working: bookmarks
		// and any app that hardcoded /app/{id}/ paths all continue to resolve — they
		// just land on the correct origin.
		if originsOn && !onAppOrigin {
			if origin, ok := appnet.AppOrigin(scheme, appID, appnet.DefaultProfile, baseDomain, reqPort); ok {
				target := origin + appPath
				if r.URL.RawQuery != "" {
					target += "?" + r.URL.RawQuery
				}
				// 307 preserves method and body, so a POST to /app/{id}/x is not
				// silently downgraded to a GET on the app's origin.
				http.Redirect(w, r, target, http.StatusTemporaryRedirect)
				return
			}
		}

		// --- Auth check ---
		session := g.validateSession(r)
		if session == nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		// --- Entitlement gating (ENTITLE-01) ---
		// Off by default (self-host/standalone); when enabled (cloud/os), an app
		// that declares a required product is refused to a request that doesn't
		// carry it (fail closed). See entitlement.go.
		if ok, reason := g.entitlementCheck(r, appID); !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusPaymentRequired)
			fmt.Fprintf(w, `{"error":"entitlement required","detail":%q}`, reason)
			return
		}

		// --- Rate limit per app (200 req/s per app) ---
		if g.isRateLimited(appID) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":"rate limited"}`, http.StatusTooManyRequests)
			return
		}

		// --- Find app namespace (scoped to user + profile) ---
		// net02Profile is "" when no profile subdomain was present; GetForProfile
		// normalises "" to "default", which keeps backwards-compat behaviour.
		ns, ok := g.netMgr.GetForProfile(appID, session.UserID, net02Profile)
		if !ok {
			// LAUNCH-01: nothing on the box ever started a bundled app — the shell's
			// WebApp lane opens the window without calling launch, auto_start is
			// false everywhere, and this lookup is pure. So this branch was the
			// terminal state for every app open: {"error":"app not running"}.
			//
			// Start it on the request that needs it. See activate.go for the
			// single-flight, the bound, and why this is the gateway's job.
			attempted, aErr := g.activate(r.Context(), appID, session.UserID, net02Profile)
			if !attempted {
				http.Error(w, `{"error":"app not running"}`, 404)
				return
			}
			if aErr != nil {
				log.Printf("[gateway] activate %s for %s/%s failed: %v", appID, session.UserID, net02Profile, aErr)
				w.Header().Set("Content-Type", "application/json")
				// A static app, or one the box does not have, has nothing to start
				// ever — that is a permanent 404, not a timeout a retry could clear.
				if errors.Is(aErr, ErrNoProcess) || errors.Is(aErr, ErrNotInstalled) {
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprintf(w, `{"error":"app not running","detail":%q}`, aErr.Error())
					return
				}
				w.WriteHeader(http.StatusGatewayTimeout)
				fmt.Fprintf(w, `{"error":"app failed to start","detail":%q}`, aErr.Error())
				return
			}
			// The activator reports success only once the app is reachable, so a
			// miss here means it came up and went away again in between.
			ns, ok = g.netMgr.GetForProfile(appID, session.UserID, net02Profile)
			if !ok {
				http.Error(w, `{"error":"app not running"}`, 404)
				return
			}
		}

		// --- WebSocket upgrade ---
		// Kept on the dedicated hijack path (proxyWebSocket): it already streams
		// bidirectionally and applies the same X-Vulos-* strip+inject.
		if isWebSocketUpgrade(r) {
			g.proxyWebSocket(w, r, ns, appPath, appID, session)
			return
		}

		// --- Proxy HTTP request through the shared streaming reverse proxy ---
		// Every per-request value the security transforms need rides in the
		// context (proxyCtx); nothing is re-parsed. proxyDirector applies the
		// request-side transforms (X-Vulos-* strip + trusted inject, Host rewrite,
		// Cookie strip) and proxyModifyResponse the response-side ones (X-Vulos-*
		// strip, Set-Cookie/X-Powered-By/X-Frame-Options drop, hardening headers,
		// per-app frame-ancestors CSP, HTML bridge injection). Bodies stream via a
		// pooled buffer with FlushInterval, so nothing is buffered except the HTML
		// document the bridge injection inherently needs. See proxy.go.
		pc := &proxyCtx{
			session:      session,
			appID:        appID,
			ns:           ns,
			appPath:      appPath,
			onAppOrigin:  onAppOrigin,
			isPathPrefix: strings.HasPrefix(r.URL.Path, "/app/"),
			scheme:       scheme,
			reqHost:      r.Host,
			baseDomain:   baseDomain,
			reqPort:      reqPort,
		}
		g.reverseProxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), proxyCtxKey{}, pc)))
	}
}

func (g *Gateway) validateSession(r *http.Request) *auth.Session {
	// Check session cookie
	token := ""
	if c, err := r.Cookie("vulos_session"); err == nil {
		token = c.Value
	}
	// Also check Authorization header
	if token == "" {
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
			token = strings.TrimPrefix(a, "Bearer ")
		}
	}
	if token == "" {
		return nil
	}

	sess, ok := g.authStore.ValidateToken(token)
	if !ok {
		return nil
	}
	return sess
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// proxyWebSocket handles WebSocket connections through the gateway.
func (g *Gateway) proxyWebSocket(w http.ResponseWriter, r *http.Request, ns *appnet.Namespace, appPath, appID string, session *auth.Session) {
	// Hijack the connection
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket hijack not supported", 500)
		return
	}

	upstream := net.JoinHostPort(ns.NSIP, fmt.Sprintf("%d", ns.AppPort))
	upConn, err := net.DialTimeout("tcp", upstream, 5*time.Second)
	if err != nil {
		http.Error(w, "app unreachable", http.StatusBadGateway)
		return
	}

	// Write the original HTTP upgrade request to the upstream. H1: strip ALL
	// inbound X-Vulos-* and re-inject trusted identity/integration/storage
	// headers so the WebSocket path can neither leak nor be spoofed (this
	// previously wrote the client's raw headers, including any X-Vulos-*).
	reqLine := fmt.Sprintf("%s %s HTTP/1.1\r\n", r.Method, appPath)
	upConn.Write([]byte(reqLine))
	g.applyTrustedHeaders(r.Context(), r, session, appID)
	r.Header.Write(upConn)
	upConn.Write([]byte("\r\n"))

	// Read the upstream's 101 handshake response so we can strip any X-Vulos-*
	// headers an app might echo back before relaying it to the client (M2 on the
	// WS path; the HTTP path strips response headers separately). Reads continue
	// from upReader afterwards so no bytes buffered during ReadResponse are lost.
	upReader := bufio.NewReader(upConn)
	resp, err := http.ReadResponse(upReader, r)
	if err != nil {
		upConn.Close()
		return
	}
	stripInboundVulosHeaders(resp.Header)

	// Hijack the client connection
	clientConn, _, err := hj.Hijack()
	if err != nil {
		upConn.Close()
		return
	}

	// Relay the sanitized handshake response to the client.
	var hdr bytes.Buffer
	fmt.Fprintf(&hdr, "HTTP/1.1 %s\r\n", resp.Status)
	resp.Header.Write(&hdr)
	hdr.WriteString("\r\n")
	if _, err := clientConn.Write(hdr.Bytes()); err != nil {
		upConn.Close()
		clientConn.Close()
		return
	}

	// Bidirectional copy (upstream side continues from upReader to preserve any
	// bytes buffered during ReadResponse).
	go func() {
		io.Copy(upConn, clientConn)
		upConn.Close()
	}()
	go func() {
		io.Copy(clientConn, upReader)
		clientConn.Close()
	}()
}

// isRateLimited checks if an app has exceeded 200 requests/second.
func (g *Gateway) isRateLimited(appID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now()
	b, ok := g.appHits[appID]
	if !ok || now.Sub(b.windowAt) > time.Second {
		g.appHits[appID] = &rateBucket{count: 1, windowAt: now}
		return false
	}
	b.count++
	return b.count > 200
}

// HealthCheck probes an app by hitting its root path through the namespace.
func (g *Gateway) HealthCheck(appID string) (bool, int) {
	ns, ok := g.netMgr.Get(appID)
	if !ok {
		return false, 0
	}
	url := fmt.Sprintf("http://%s:%d/", ns.NSIP, ns.AppPort)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := g.client.Do(req)
	if err != nil {
		return false, 0
	}
	resp.Body.Close()
	return resp.StatusCode < 500, resp.StatusCode
}

// HealthCheckAll probes all running apps.
func (g *Gateway) HealthCheckAll() map[string]any {
	results := make(map[string]any)
	for _, ns := range g.netMgr.List() {
		healthy, code := g.HealthCheck(ns.AppID)
		results[ns.AppID] = map[string]any{"healthy": healthy, "status_code": code}
	}
	return results
}

// URLForApp returns the gateway URL for an app (used by frontend).
func URLForApp(appID string) string {
	return "/app/" + appID + "/"
}
