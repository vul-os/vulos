package main

// routes_open.go — POST /api/open: server-side xdg-open / Chromium new-tab.
// Extracted from main.go to demonstrate the per-area routes file pattern.
// See ROUTES.md for the contract new routes should follow.

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"vulos/backend/services/notify"
	"vulos/backend/services/webbrowser"
)

// openTabCount tracks concurrent open-tab requests for rate-limiting (SEC-H, H6).
var openTabCount int32

// openTabMax caps concurrent /api/open requests across the process.
const openTabMax = 10

// isRestrictedHost resolves host to IPs and returns true if any IP is
// loopback, private, link-local, multicast, or unspecified — blocking SSRF.
// Fail-closed on resolution errors (SEC-H).
func isRestrictedHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, b := range []string{"localhost", "127.0.0.1", "0.0.0.0", "::1"} {
		if h == b {
			return true
		}
	}
	ips, err := net.LookupHost(h)
	if err != nil {
		// Fail closed: unresolvable host is blocked.
		return true
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			return true
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
			return true
		}
	}
	return false
}

// registerOpenRoutes wires POST /api/open onto mux.
// All dependencies are passed explicitly — no globals captured.
func registerOpenRoutes(mux *http.ServeMux, browserSvc *webbrowser.Service, notifySvc *notify.Service) {
	mux.HandleFunc("POST /api/open", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil || req.URL == "" {
			writeErr(w, 400, "url required")
			return
		}

		// Parse and validate URL scheme.
		parsed, err := url.Parse(req.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			writeErr(w, 400, "url must use http or https scheme")
			return
		}

		// Reject private/loopback/link-local/multicast targets (SSRF prevention).
		if isRestrictedHost(parsed.Hostname()) {
			writeErr(w, 403, "url resolves to a restricted network address")
			return
		}

		// Enforce concurrent-tab cap to prevent resource exhaustion.
		if atomic.AddInt32(&openTabCount, 1) > openTabMax {
			atomic.AddInt32(&openTabCount, -1)
			writeErr(w, 429, "too many open tabs")
			return
		}
		defer atomic.AddInt32(&openTabCount, -1)

		tab, err := browserSvc.OpenTab(req.URL)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		notifySvc.Send("browser", req.URL, notify.LevelInfo, "xdg-open")
		writeJSON(w, tab)
	})
}
