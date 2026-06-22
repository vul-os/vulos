package gateway

import (
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
			g.proxyWebSocket(w, r, ns, appPath, session)
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

		// applyVulosHeaders stamps identity + integration headers and clears
		// client-controlled hop headers. Applied on every (re)build of proxyReq
		// so the retry path below doesn't drop these (previously it did).
		applyVulosHeaders := func(pr *http.Request) {
			pr.Header.Set("X-Vulos-User-ID", session.UserID)
			pr.Header.Set("X-Vulos-Email", session.Email)
			pr.Header.Set("X-Vulos-Session", session.ID)
			pr.Header.Set("X-Vulos-App-ID", appID)
			pr.Header.Del("Cookie")
			pr.Header.Del("Host")
			pr.Host = fmt.Sprintf("localhost:%d", ns.AppPort)
			g.injectIntegrationTokens(pr.Context(), pr, appID)
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
func (g *Gateway) proxyWebSocket(w http.ResponseWriter, r *http.Request, ns *appnet.Namespace, appPath string, session *auth.Session) {
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

	// Write the original HTTP upgrade request to the upstream
	reqLine := fmt.Sprintf("%s %s HTTP/1.1\r\n", r.Method, appPath)
	upConn.Write([]byte(reqLine))
	r.Header.Set("X-Vulos-User-ID", session.UserID)
	r.Header.Set("X-Vulos-Email", session.Email)
	r.Header.Del("Cookie")
	r.Header.Write(upConn)
	upConn.Write([]byte("\r\n"))

	// Hijack the client connection
	clientConn, _, err := hj.Hijack()
	if err != nil {
		upConn.Close()
		return
	}

	// Bidirectional copy
	go func() {
		io.Copy(upConn, clientConn)
		upConn.Close()
	}()
	go func() {
		io.Copy(clientConn, upConn)
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
