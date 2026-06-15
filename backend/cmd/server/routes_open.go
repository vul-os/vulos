package main

// routes_open.go — POST /api/open: host-browser open instruction.
//
// BROWSER-01/02 (native-first re-architecture): all URL opens resolve to a
// host-browser instruction. The server-side streamed Chromium path has been
// removed (services/webbrowser package deleted in BROWSER-02).
// The response tells the frontend to open the URL in the host browser (in-shell
// web view or new window/tab). Zero stream.Session objects are created.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"vulos/backend/services/notify"
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

// hostBrowserOpenResponse is returned by POST /api/open (BROWSER-01).
// The frontend shell uses the "action" field to open the URL in the host
// browser: either as an in-shell web view (iframe/web-view window) or a
// real host OS browser tab, depending on the client environment.
// Zero stream.Session objects are created for this action.
type hostBrowserOpenResponse struct {
	Action string `json:"action"` // always "open_in_host_browser"
	URL    string `json:"url"`
}

// validateNativeWindowURL checks that the URL is safe to open in a native
// window: scheme must be http or https, and the host must not resolve to a
// restricted network address (SSRF prevention, mirrors /api/open logic).
func validateNativeWindowURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("url must use http or https scheme")
	}
	if isRestrictedHost(parsed.Hostname()) {
		return fmt.Errorf("url resolves to a restricted network address")
	}
	return nil
}

// registerOpenRoutes wires POST /api/open onto mux.
// All dependencies are passed explicitly — no globals captured.
func registerOpenRoutes(mux *http.ServeMux, notifySvc *notify.Service) {
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

		// Enforce concurrent-request cap to prevent resource exhaustion.
		if atomic.AddInt32(&openTabCount, 1) > openTabMax {
			atomic.AddInt32(&openTabCount, -1)
			writeErr(w, 429, "too many open requests")
			return
		}
		defer atomic.AddInt32(&openTabCount, -1)

		// BROWSER-01: return a host-browser open instruction — do NOT call
		// browserSvc.OpenTab() or spawn any stream.Session.
		notifySvc.Send("browser", req.URL, notify.LevelInfo, "host-browser-open")
		writeJSON(w, hostBrowserOpenResponse{
			Action: "open_in_host_browser",
			URL:    req.URL,
		})
	})
}
