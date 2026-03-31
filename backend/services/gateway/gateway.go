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

// Handler returns the HTTP handler for /app/{appId}/*
func (g *Gateway) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Parse: /app/{appId}/rest/of/path
		path := strings.TrimPrefix(r.URL.Path, "/app/")
		slashIdx := strings.Index(path, "/")
		var appID, appPath string
		if slashIdx == -1 {
			appID = path
			appPath = "/"
		} else {
			appID = path[:slashIdx]
			appPath = path[slashIdx:]
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

		// --- Find app namespace ---
		ns, ok := g.netMgr.Get(appID)
		if !ok {
			http.Error(w, `{"error":"app not running"}`, 404)
			return
		}

		// --- Build upstream URL ---
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

		// Copy original headers
		for k, vv := range r.Header {
			for _, v := range vv {
				proxyReq.Header.Add(k, v)
			}
		}

		// Inject user identity
		proxyReq.Header.Set("X-Vulos-User-ID", session.UserID)
		proxyReq.Header.Set("X-Vulos-Email", session.Email)
		proxyReq.Header.Set("X-Vulos-Session", session.ID)
		proxyReq.Header.Set("X-Vulos-App-ID", appID)

		// Remove headers that could confuse the app
		proxyReq.Header.Del("Cookie") // don't leak vulos session cookie to apps
		proxyReq.Header.Del("Host")
		proxyReq.Host = fmt.Sprintf("%s:%d", ns.NSIP, ns.AppPort)

		// Execute
		resp, err := g.client.Do(proxyReq)
		if err != nil {
			log.Printf("[gateway] proxy to %s failed: %v", appID, err)
			http.Error(w, `{"error":"app unreachable"}`, 502)
			return
		}
		defer resp.Body.Close()

		// Copy response headers
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}

		// Strip sensitive headers from app response
		w.Header().Del("Set-Cookie")              // apps can't set cookies on vulos domain
		w.Header().Del("X-Powered-By")            // don't leak app stack
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("X-Vulos-App", appID)

		// Rewrite Location headers so redirects stay on the gateway path
		if loc := resp.Header.Get("Location"); loc != "" {
			if strings.HasPrefix(loc, "/") {
				w.Header().Set("Location", "/app/"+appID+loc)
			}
		}

		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
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
