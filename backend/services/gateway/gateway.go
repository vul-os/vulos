package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
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

	// integrationTokenFunc, when set, mints a short-lived third-party access
	// token for (ctx, provider). Apps that declare the matching integration
	// permission have it injected as an X-Vulos-Integration-<Provider> request
	// header (INTEG-04). nil disables injection entirely.
	integrationTokenFunc func(ctx context.Context, provider string) (string, error)
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
}

// rateBucket tracks request count per window for per-app rate limiting.
type rateBucket struct {
	count    int
	windowAt time.Time
}

func New(authStore *auth.Store, netMgr *appnet.Manager, portPool *appnet.PortPool) *Gateway {
	g := &Gateway{
		authStore:  authStore,
		netMgr:     netMgr,
		portPool:   portPool,
		appSecrets: make(map[string]string),
		appHits:    make(map[string]*rateBucket),
		client: &http.Client{
			Timeout: 60 * time.Second,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
				DialContext: (&net.Dialer{
					Timeout:   5 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse // don't follow redirects — let the browser handle them
			},
		},
	}

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
func (g *Gateway) SetIntegrationTokenFunc(fn func(ctx context.Context, provider string) (string, error)) {
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
	pr.Header.Set("X-Vulos-Session", session.ID)
	pr.Header.Set("X-Vulos-App-ID", appID)
	pr.Header.Del("Cookie")
	g.injectIntegrationTokens(ctx, pr, appID)
	g.injectStorageHeaders(ctx, pr, session.UserID, appID)
}

// injectIntegrationTokens adds X-Vulos-Integration-<Provider> headers for every
// provider appID is permitted to receive. Mint failures (not connected, cloud
// unavailable) are swallowed — the app simply sees no token and degrades.
func (g *Gateway) injectIntegrationTokens(ctx context.Context, pr *http.Request, appID string) {
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
		tok, err := fn(ctx, provider)
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

// Handler returns the HTTP handler for app traffic.
// Supports two modes:
//   - Subdomain: cockpit.localhost:8080/* → namespace (app gets full path, all apps work)
//   - Path prefix: /app/cockpit/* → namespace (legacy fallback)
func (g *Gateway) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var appID, appPath string

		// Try subdomain: {appId}.lvh.me or {appId}.vula.example.com
		// With NET-02, subdomain may carry a profile prefix:
		//   {profile}--{appId}.{baseDomain}  →  profile=profile, appID=appId
		//   {appId}.{baseDomain}             →  profile=default, appID=appId
		var net02Profile string
		host := r.Host
		if idx := strings.Index(host, ":"); idx > 0 {
			host = host[:idx]
		}
		if baseDomain := os.Getenv("VULOS_DOMAIN"); baseDomain != "" && strings.HasSuffix(host, "."+baseDomain) {
			// ParseSubdomain (NET-01) handles both plain and profile-prefixed subdomains:
			//   {profile}--{appId}.{baseDomain}  →  profile=profile, appID=appId
			//   {appId}.{baseDomain}             →  profile="default", appID=appId
			if parsedApp, parsedProfile, ok := appnet.ParseSubdomain(host, baseDomain); ok {
				appID = parsedApp
				net02Profile = parsedProfile
			} else {
				// Shouldn't happen given HasSuffix check above, but degrade gracefully.
				appID = strings.TrimSuffix(host, "."+baseDomain)
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

		// --- Auth check ---
		session := g.validateSession(r)
		if session == nil {
			http.Error(w, `{"error":"unauthorized"}`, 401)
			return
		}

		// --- Rate limit per app (200 req/s per app) ---
		if g.isRateLimited(appID) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":"rate limited"}`, 429)
			return
		}

		// --- Find app namespace (scoped to user + profile) ---
		// net02Profile is "" when no profile subdomain was present; GetForProfile
		// normalises "" to "default", which keeps backwards-compat behaviour.
		ns, ok := g.netMgr.GetForProfile(appID, session.UserID, net02Profile)
		if !ok {
			http.Error(w, `{"error":"app not running"}`, 404)
			return
		}

		// --- Build upstream URL (full path forwarded) ---
		upstream := fmt.Sprintf("http://%s:%d%s", ns.NSIP, ns.AppPort, appPath)
		if r.URL.RawQuery != "" {
			upstream += "?" + r.URL.RawQuery
		}

		// --- WebSocket upgrade ---
		if isWebSocketUpgrade(r) {
			g.proxyWebSocket(w, r, ns, appPath, appID, session)
			return
		}

		// --- Proxy HTTP request ---
		proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstream, r.Body)
		if err != nil {
			http.Error(w, `{"error":"proxy error"}`, 500)
			return
		}

		for k, vv := range r.Header {
			for _, v := range vv {
				proxyReq.Header.Add(k, v)
			}
		}

		// applyVulosHeaders strips all inbound X-Vulos-* (anti-spoof, H1), stamps
		// trusted identity + integration + storage headers, and clears
		// client-controlled hop headers. Applied on every (re)build of proxyReq
		// so the retry path below doesn't drop these (previously it did).
		applyVulosHeaders := func(pr *http.Request) {
			g.applyTrustedHeaders(pr.Context(), pr, session, appID)
			pr.Header.Del("Host")
			pr.Host = fmt.Sprintf("localhost:%d", ns.AppPort)
		}
		applyVulosHeaders(proxyReq)

		// Retry up to 3 times — app may still be starting
		var resp *http.Response
		var proxyErr error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(time.Second)
				// Rebuild request (body may be consumed)
				proxyReq, _ = http.NewRequestWithContext(r.Context(), r.Method, upstream, r.Body)
				for k, vv := range r.Header {
					for _, v := range vv {
						proxyReq.Header.Add(k, v)
					}
				}
				applyVulosHeaders(proxyReq)
			}
			resp, proxyErr = g.client.Do(proxyReq)
			if proxyErr == nil {
				break
			}
		}
		if proxyErr != nil {
			log.Printf("[gateway] proxy to %s failed: %v", appID, proxyErr)
			http.Error(w, `{"error":"app unreachable"}`, 502)
			return
		}
		defer resp.Body.Close()

		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}

		// M2: strip ALL X-Vulos-* from the app's response so an app can never
		// leak (or forge) seam headers — storage creds, broker-auth, identity —
		// back to the browser. Done before the gateway sets its own X-Vulos-App.
		stripInboundVulosHeaders(w.Header())

		w.Header().Del("Set-Cookie")
		w.Header().Del("X-Powered-By")
		w.Header().Del("X-Frame-Options") // Allow embedding in Vula OS shell iframe
		w.Header().Set("X-Vulos-App", appID)

		// Security hardening headers applied to all proxied responses.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), clipboard-read=(), clipboard-write=()")

		// For HTML responses served via path prefix, inject <base> tag so
		// the app's absolute paths resolve relative to /app/{appId}/
		ct := resp.Header.Get("Content-Type")
		isPathPrefix := strings.HasPrefix(r.URL.Path, "/app/")
		if isPathPrefix && strings.Contains(ct, "text/html") {
			body, err := io.ReadAll(resp.Body)
			if err == nil {
				baseTag := fmt.Sprintf(`<base href="/app/%s/">`, appID)
				html := string(body)
				// Inject after <head> or at the start of the document
				if idx := strings.Index(strings.ToLower(html), "<head>"); idx >= 0 {
					html = html[:idx+6] + baseTag + html[idx+6:]
				} else if idx := strings.Index(strings.ToLower(html), "<html"); idx >= 0 {
					end := strings.Index(html[idx:], ">")
					if end >= 0 {
						pos := idx + end + 1
						html = html[:pos] + "<head>" + baseTag + "</head>" + html[pos:]
					}
				} else {
					html = baseTag + html
				}
				w.Header().Set("Content-Length", fmt.Sprintf("%d", len(html)))
				w.WriteHeader(resp.StatusCode)
				w.Write([]byte(html))
			} else {
				w.WriteHeader(resp.StatusCode)
			}
		} else {
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
		}
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
		http.Error(w, "app unreachable", 502)
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
