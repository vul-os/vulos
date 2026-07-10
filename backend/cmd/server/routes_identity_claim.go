package main

// routes_identity_claim.go — IDENTITY-01: thin authenticated proxies for the
// @vulos.to identity-claim flow.
//
// The OS install wizard (VulosAccountStep) needs to check handle availability
// and claim a @vulos.to address. Both operations live on the Vulos Cloud control
// plane (CP); the OS server only proxies to it so the wizard can talk to a
// same-origin local endpoint (and so any CP session cookie flows through).
//
//	GET  /api/identity/check?handle=<h>&domain=<d>  — proxy CP GET /api/identity/check
//	                                                   (public/setup-time; CP is
//	                                                   rate-limited). On CP unreachable
//	                                                   returns {"offline":true} (200)
//	                                                   which the wizard treats as a soft
//	                                                   state.
//	POST /api/identity/claim                          — proxy CP POST /api/identity/claim
//	                                                   (state-changing; OS-session gated by
//	                                                   the auth middleware since it is NOT
//	                                                   in publicPaths). The CP derives the
//	                                                   account from ITS session cookie — the
//	                                                   OS handler never supplies an account id.
//
// CP base URL: VULOS_CLOUD_API_URL (else https://api.vulos.org), matching the
// convention in backend/services/auth/cloudsignup.go (cloudAPIURL()).

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// identityCloudAPIURL returns the Vulos Cloud API base, honouring VULOS_CLOUD_API_URL
// (default https://api.vulos.org), matching auth.cloudAPIURL(). Trailing slash trimmed.
func identityCloudAPIURL() string {
	if u := os.Getenv("VULOS_CLOUD_API_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "https://api.vulos.org"
}

// registerIdentityClaimRoutes wires the two CP proxy handlers into mux. It shares
// the same mux as registerIdentityRoutes.
func registerIdentityClaimRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/identity/check", handleIdentityCheck)
	mux.HandleFunc("POST /api/identity/claim", handleIdentityClaim)
}

// handleIdentityCheck proxies handle-availability checks to the CP.
//
// Forwards the handle (+ optional domain) query params and the incoming Cookie
// header (so any CP session cookie flows). On CP unreachable it returns
// {"offline":true} with 200 — the wizard reads data.offline as a soft state.
func handleIdentityCheck(w http.ResponseWriter, r *http.Request) {
	handle := r.URL.Query().Get("handle")
	if handle == "" {
		writeErr(w, http.StatusBadRequest, "handle is required")
		return
	}

	q := url.Values{}
	q.Set("handle", handle)
	if domain := r.URL.Query().Get("domain"); domain != "" {
		q.Set("domain", domain)
	}
	targetURL := identityCloudAPIURL() + "/api/identity/check?" + q.Encode()

	client := &http.Client{Timeout: 10 * time.Second}
	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, targetURL, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	proxyReq.Header.Set("Accept", "application/json")
	proxyReq.Header.Set("User-Agent", "vulos-os/1.0 identity-check")
	// Forward the CP session cookie (if any) so the check reflects the caller.
	if cookie := r.Header.Get("Cookie"); cookie != "" {
		proxyReq.Header.Set("Cookie", cookie)
	}

	resp, err := client.Do(proxyReq)
	if err != nil {
		// Soft offline: the wizard treats {"offline":true} as "check later on submit".
		log.Printf("[identity] check: CP unreachable (%v) — returning offline", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"offline":true}`))
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		log.Printf("[identity] check: error reading CP response — returning offline")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"offline":true}`))
		return
	}

	// Relay CP status + body verbatim.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

// handleIdentityClaim proxies a @vulos.to address claim to the CP.
//
// SECURITY: this is a state-changing, authenticated call.
//   - The OS auth middleware gates it (it is NOT in publicPaths), so the caller
//     must already hold a valid LOCAL OS session.
//   - The OS handler does NOT trust or forward any client-supplied account id; the
//     CP derives the account from ITS session cookie, which we forward via the
//     incoming Cookie header. Only handle + domain are relayed.
//
// CP status + body are relayed verbatim (including 401/409/429/400).
func handleIdentityClaim(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Handle string `json:"handle"`
		Domain string `json:"domain"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8*1024)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Handle == "" {
		writeErr(w, http.StatusBadRequest, "handle is required")
		return
	}

	// SECURITY: forward ONLY handle + domain. Never an account id — the CP derives
	// the account from its session cookie (forwarded below).
	body, err := json.Marshal(map[string]string{
		"handle": req.Handle,
		"domain": req.Domain,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	targetURL := identityCloudAPIURL() + "/api/identity/claim"
	log.Printf("[identity] claim: forwarding handle=%q domain=%q to %s", req.Handle, req.Domain, targetURL)

	client := &http.Client{Timeout: 15 * time.Second}
	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("Accept", "application/json")
	proxyReq.Header.Set("User-Agent", "vulos-os/1.0 identity-claim")
	// Forward the CP session cookie so the CP can authenticate the claim.
	if cookie := r.Header.Get("Cookie"); cookie != "" {
		proxyReq.Header.Set("Cookie", cookie)
	}

	resp, err := client.Do(proxyReq)
	if err != nil {
		log.Printf("[identity] claim: CP request failed: %v", err)
		writeErr(w, http.StatusBadGateway, "could not reach Vulos Cloud — check your network connection")
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "error reading cloud response")
		return
	}

	// Relay CP status + body verbatim (including 401/409/429/400).
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
	log.Printf("[identity] claim: relayed CP status=%d for handle=%q", resp.StatusCode, req.Handle)
}
