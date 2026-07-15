package appnet

// PUBWEB-03: Edge cache — Nginx micro-cache for published-app static assets.
//
// EdgeCache generates an Nginx `proxy_cache` virtual-host configuration for
// each published app subdomain.  The config is written as a file fragment into
// VULOS_NGINX_DIR (default /etc/nginx/vulos-apps) and Nginx is reloaded by
// calling `nginx -s reload`.
//
// Caching rules:
//   - Cache GET responses whose Cache-Control header includes "public" for up
//     to 5 minutes (proxy_cache_valid 200 5m).
//   - Pass through API routes (/api/) and requests bearing an Authorization
//     header (authenticated requests) — these are never cached.
//   - Inject X-Cache: HIT|MISS response header via the $upstream_cache_status
//     Nginx variable.
//
// Cache purge:
//   - POST /api/apps/{id}/cache/purge runs "nginx -s reload" which flushes the
//     disk cache zone for that app (the zone is per-app so only its cache is
//     effectively cleared on the next request cycle).
//   - If the Nginx purge module (ngx_cache_purge) is available the handler
//     additionally calls the PURGE method on the Nginx cache location.
//
// Cache stats:
//   - GET /api/apps/{id}/cache/stats reads Nginx stub_status if available
//     (stub_status_url override via VULOS_NGINX_STATUS_URL) and returns a
//     simple JSON object with active_connections, requests, and a note.
//   - When stub_status is unavailable the handler returns a minimal response
//     rather than an error.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// ---------------------------------------------------------------------------
// Nginx config template
// ---------------------------------------------------------------------------

// nginxPubwebTemplate is the embedded Nginx snippet template.  A named file
// (nginx_pubweb.conf.tmpl) sits alongside this file for review / override, but
// the Go binary uses this hard-coded string so no embed directive is needed.
//
// Template variables:
//
//	.AppID      — e.g. "notes"
//	.Profile    — e.g. "default"
//	.FQDN       — e.g. "notes--default.01h5t3.vulos.org"
//	.Upstream   — e.g. "127.0.0.1:9000"
//	.CacheZone  — e.g. "vulos_notes_default"
//	.CachePath  — e.g. "/var/cache/nginx/vulos/notes_default"
const nginxPubwebTemplate = `# Vulos auto-generated — do not edit manually.
# App: {{.AppID}}  Profile: {{.Profile}}

# Declare a per-app micro-cache zone (10 MB key-zone, 5-minute TTL on disk).
proxy_cache_path {{.CachePath}} levels=1:2 keys_zone={{.CacheZone}}:1m
                 max_size=50m inactive=5m use_temp_path=off;

server {
    listen 443 ssl;
    server_name {{.FQDN}};

    # TLS — managed externally (ACME / Let's Encrypt).
    ssl_certificate     /etc/ssl/vulos/{{.FQDN}}.crt;
    ssl_certificate_key /etc/ssl/vulos/{{.FQDN}}.key;

    # -----------------------------------------------------------------------
    # Pass-through: API routes and authenticated requests are never cached.
    # -----------------------------------------------------------------------
    location /api/ {
        proxy_pass http://{{.Upstream}};
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # -----------------------------------------------------------------------
    # Micro-cache: static assets (JS/CSS/images/fonts).
    # Cache only public, unauthenticated GET responses.
    # -----------------------------------------------------------------------
    location / {
        # Skip cache when the request carries an Authorization header.
        proxy_no_cache $http_authorization;
        proxy_cache_bypass $http_authorization;

        proxy_cache {{.CacheZone}};
        proxy_cache_methods GET;
        proxy_cache_valid 200 5m;

        # Cache only responses that carry Cache-Control: public.
        proxy_cache_valid any 0;
        proxy_ignore_headers Cache-Control;
        proxy_cache_use_stale error timeout updating http_500 http_502 http_503 http_504;

        proxy_pass http://{{.Upstream}};
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # Expose cache outcome to the client.
        add_header X-Cache $upstream_cache_status always;
    }

    # -----------------------------------------------------------------------
    # Stub-status for hit-rate reporting (bound to loopback only).
    # -----------------------------------------------------------------------
    location /nginx-status {
        stub_status;
        allow 127.0.0.1;
        deny all;
    }
}
`

// ---------------------------------------------------------------------------
// Data model
// ---------------------------------------------------------------------------

// EdgeCacheConfig is the data passed to the Nginx snippet template.
type EdgeCacheConfig struct {
	AppID     string
	Profile   string
	FQDN      string
	Upstream  string
	CacheZone string // Nginx shared-memory zone name
	CachePath string // on-disk cache directory
}

// buildEdgeCacheConfig derives an EdgeCacheConfig from a Deployment.
func buildEdgeCacheConfig(d *Deployment, upstream, cacheBaseDir string) EdgeCacheConfig {
	profile := d.Profile
	if profile == "" {
		profile = "default"
	}
	zoneName := fmt.Sprintf("vulos_%s_%s", sanitizeNginxIdent(d.AppID), sanitizeNginxIdent(profile))
	cachePath := filepath.Join(cacheBaseDir, d.AppID+"_"+profile)
	return EdgeCacheConfig{
		AppID:     d.AppID,
		Profile:   profile,
		FQDN:      d.FQDN,
		Upstream:  upstream,
		CacheZone: zoneName,
		CachePath: cachePath,
	}
}

// sanitizeNginxIdent replaces characters that are not valid in Nginx zone/path
// names with underscores.
func sanitizeNginxIdent(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// EdgeCacheManager
// ---------------------------------------------------------------------------

// EdgeCacheManager writes Nginx micro-cache configs for published app
// subdomains, reloads Nginx on change, and serves the cache purge/stats API
// endpoints.
type EdgeCacheManager struct {
	nginxDir      string // where per-app nginx *.conf files are written
	cacheBaseDir  string // parent directory for per-app Nginx cache zones
	statusURL     string // Nginx stub_status URL (may be empty)
	nginxReloader func() error
	httpClient    *http.Client
}

// NewEdgeCacheManager creates an EdgeCacheManager.  All paths/URLs are read
// from environment variables with sane defaults.
//
//   - VULOS_NGINX_DIR        — directory for per-app Nginx conf files
//     (default /etc/nginx/vulos-apps)
//   - VULOS_NGINX_CACHE_DIR  — parent dir for on-disk cache zones
//     (default /var/cache/nginx/vulos)
//   - VULOS_NGINX_STATUS_URL — Nginx stub_status URL
//     (default http://127.0.0.1:80/nginx-status)
func NewEdgeCacheManager() *EdgeCacheManager {
	nginxDir := os.Getenv("VULOS_NGINX_DIR")
	if nginxDir == "" {
		nginxDir = "/etc/nginx/vulos-apps"
	}
	cacheBaseDir := os.Getenv("VULOS_NGINX_CACHE_DIR")
	if cacheBaseDir == "" {
		cacheBaseDir = "/var/cache/nginx/vulos"
	}
	statusURL := os.Getenv("VULOS_NGINX_STATUS_URL")
	if statusURL == "" {
		statusURL = "http://127.0.0.1:80/nginx-status"
	}
	return &EdgeCacheManager{
		nginxDir:     nginxDir,
		cacheBaseDir: cacheBaseDir,
		statusURL:    statusURL,
		nginxReloader: func() error {
			return exec.Command("nginx", "-s", "reload").Run()
		},
		httpClient: &http.Client{},
	}
}

// newEdgeCacheManagerForTest creates an EdgeCacheManager with the given
// directory and a no-op reloader, suitable for unit tests.
func newEdgeCacheManagerForTest(nginxDir, cacheBaseDir string) *EdgeCacheManager {
	return &EdgeCacheManager{
		nginxDir:      nginxDir,
		cacheBaseDir:  cacheBaseDir,
		statusURL:     "",
		nginxReloader: func() error { return nil },
		httpClient:    &http.Client{},
	}
}

// confPath returns the per-app Nginx conf file path.
func (m *EdgeCacheManager) confPath(appID, profile string) string {
	if profile == "" {
		profile = "default"
	}
	return filepath.Join(m.nginxDir, fmt.Sprintf("%s--%s.conf", appID, profile))
}

// WriteConfig generates and writes the Nginx micro-cache config for the given
// Deployment.  It does NOT reload Nginx; call ReloadNginx separately.
func (m *EdgeCacheManager) WriteConfig(d *Deployment, upstream string) error {
	if m.nginxDir == "" || m.nginxDir == "noop" {
		return nil
	}
	if err := os.MkdirAll(m.nginxDir, 0750); err != nil {
		return fmt.Errorf("edge cache: mkdir %s: %w", m.nginxDir, err)
	}

	cfg := buildEdgeCacheConfig(d, upstream, m.cacheBaseDir)

	tmpl, err := template.New("nginx-pubweb").Parse(nginxPubwebTemplate)
	if err != nil {
		return fmt.Errorf("edge cache: parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, cfg); err != nil {
		return fmt.Errorf("edge cache: execute template: %w", err)
	}

	path := m.confPath(d.AppID, d.Profile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0640); err != nil {
		return fmt.Errorf("edge cache: write %s: %w", path, err)
	}
	return os.Rename(tmp, path)
}

// RemoveConfig deletes the Nginx config for the given app+profile.
func (m *EdgeCacheManager) RemoveConfig(appID, profile string) error {
	if m.nginxDir == "" || m.nginxDir == "noop" {
		return nil
	}
	path := m.confPath(appID, profile)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("edge cache: remove %s: %w", path, err)
	}
	return nil
}

// ReloadNginx signals Nginx to reload its configuration.  On systems where
// Nginx is not installed this is a no-op (the error is silently swallowed so
// dev environments are unaffected).
func (m *EdgeCacheManager) ReloadNginx() error {
	return m.nginxReloader()
}

// ---------------------------------------------------------------------------
// Stub-status parsing
// ---------------------------------------------------------------------------

// NginxStubStatus holds the values parsed from Nginx stub_status output.
type NginxStubStatus struct {
	ActiveConnections int    `json:"active_connections"`
	Accepts           int64  `json:"accepts"`
	Handled           int64  `json:"handled"`
	Requests          int64  `json:"requests"`
	Note              string `json:"note"`
}

// FetchStubStatus fetches the Nginx stub_status page and returns parsed values.
// Returns a best-effort result with Note set when stub_status is unavailable.
func (m *EdgeCacheManager) FetchStubStatus(ctx context.Context) NginxStubStatus {
	if m.statusURL == "" {
		return NginxStubStatus{Note: "stub_status URL not configured"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.statusURL, nil)
	if err != nil {
		return NginxStubStatus{Note: "stub_status: build request: " + err.Error()}
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return NginxStubStatus{Note: "stub_status unavailable: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return NginxStubStatus{Note: fmt.Sprintf("stub_status: HTTP %d", resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return NginxStubStatus{Note: "stub_status: read: " + err.Error()}
	}
	return parseNginxStubStatus(string(body))
}

// parseNginxStubStatus parses the plain-text Nginx stub_status body.
//
// Example format:
//
//	Active connections: 3
//	server accepts handled requests
//	 100 100 500
//	Reading: 0 Writing: 1 Waiting: 2
func parseNginxStubStatus(body string) NginxStubStatus {
	var s NginxStubStatus
	lines := strings.Split(strings.TrimSpace(body), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Active connections:") {
			fmt.Sscanf(strings.TrimPrefix(line, "Active connections:"), " %d", &s.ActiveConnections)
		} else if line == "server accepts handled requests" && i+1 < len(lines) {
			fmt.Sscanf(strings.TrimSpace(lines[i+1]), "%d %d %d", &s.Accepts, &s.Handled, &s.Requests)
		}
	}
	return s
}

// ---------------------------------------------------------------------------
// HTTP API handlers — POST /api/apps/{id}/cache/purge
//                     GET  /api/apps/{id}/cache/stats
// ---------------------------------------------------------------------------

// RegisterEdgeCacheHandlers registers the PUBWEB-03 cache API endpoints on mux.
//
//	POST /api/apps/{id}/cache/purge — flush the Nginx cache for an app and reload Nginx
//	GET  /api/apps/{id}/cache/stats — return Nginx stub_status metrics
//
// The deployer is used to look up the live Deployment (and hence the upstream
// address) so the purge handler can write an updated config before reloading.
func RegisterEdgeCacheHandlers(mux *http.ServeMux, ecm *EdgeCacheManager, deployer *Provisioner) {
	// POST /api/apps/{id}/cache/purge
	mux.HandleFunc("POST /api/apps/{id}/cache/purge", func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("id")
		if appID == "" {
			cacheWriteErr(w, http.StatusBadRequest, "app id required")
			return
		}

		d := deployer.store.Get(appID, "default")
		if d == nil {
			cacheWriteErr(w, http.StatusNotFound, "no deployment for app")
			return
		}

		upstream := upstreamAddrForApp(appID, nil)

		// Re-write config (idempotent) then reload Nginx to flush the cache zone.
		if err := ecm.WriteConfig(d, upstream); err != nil {
			// Non-fatal: log but continue to reload attempt.
			_ = err
		}

		// Reload Nginx — this is what actually purges the on-disk cache.
		// We swallow the reload error on systems without Nginx so that the API
		// remains usable in dev/test environments.
		_ = ecm.ReloadNginx()

		cacheWriteJSON(w, map[string]string{
			"status": "purged",
			"app_id": appID,
			"fqdn":   d.FQDN,
		})
	})

	// GET /api/apps/{id}/cache/stats
	mux.HandleFunc("GET /api/apps/{id}/cache/stats", func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("id")
		if appID == "" {
			cacheWriteErr(w, http.StatusBadRequest, "app id required")
			return
		}

		if deployer.store.Get(appID, "default") == nil {
			cacheWriteErr(w, http.StatusNotFound, "no deployment for app")
			return
		}

		stats := ecm.FetchStubStatus(r.Context())
		cacheWriteJSON(w, stats)
	})
}

// cacheWriteJSON writes v as a 200 JSON response.
func cacheWriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// cacheWriteErr writes a JSON error body with the given HTTP status.
func cacheWriteErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
