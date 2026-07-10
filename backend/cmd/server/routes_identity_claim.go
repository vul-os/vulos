package main

// routes_identity_claim.go — IDENTITY-01: thin authenticated proxies for the
// @vulos.net identity-claim flow.
//
// The OS install wizard (VulosAccountStep) needs to check handle availability
// and claim a @vulos.net address. Both operations live on the Vulos Cloud control
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
// OFFLINE FIRST-BOOT (the documented remainder): in the pure first-boot wizard the
// browser talks only to this local OS origin and may not yet hold a CP session
// cookie, so the cookie-only proxy would 401 at the CP. When the box has completed
// owner-attested cloud enrollment it therefore ALSO forwards its DEVICE CERTIFICATE
// (INTEG-SEC-01) — the same X-Device-* headers the integrations token-mint uses — so
// the CP can authenticate the enrolled device on the user's behalf and derive the
// account from the device→account binding (never from the body). The box holds the
// device cert + key; the browser never sees them.
//
// CP base URL: VULOS_CLOUD_API_URL (else https://api.vulos.org), matching the
// convention in backend/services/auth/cloudsignup.go (cloudAPIURL()).

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// identityDeviceAuther supplies this box's owner-attested device certificate
// (INTEG-SEC-01) so the claim proxy can authenticate the enrolled device to the CP
// on the offline first-boot path. cloudenroll.Identity satisfies it (DeviceCert +
// SignMint), and it carries the box ULID. nil ⇒ device-auth forwarding disabled
// (cookie-only proxy, unchanged behaviour).
type identityDeviceAuther interface {
	// DeviceCert returns (managementCASig, devicePubKey, ok). ok=false ⇒ no usable cert.
	DeviceCert() (cert, pubKey []byte, ok bool)
	// SignMint signs the purpose-bound message with the device key (ed25519).
	SignMint(message string) ([]byte, error)
}

// identityDeviceAuth is the box's enrolled device identity used to authenticate the
// offline first-boot claim, plus its ULID. Set once at startup (SetIdentityDeviceAuth)
// when cloud enrollment has loaded; nil until then.
var (
	identityDeviceAuth     identityDeviceAuther
	identityDeviceAuthULID string
)

// SetIdentityDeviceAuth installs the box's enrolled device identity for the claim
// proxy. Called from main once cloudenroll.Load succeeds. auther/ulid empty ⇒ no-op.
func SetIdentityDeviceAuth(auther identityDeviceAuther, ulid string) {
	if auther == nil || ulid == "" {
		return
	}
	identityDeviceAuth = auther
	identityDeviceAuthULID = ulid
}

// identityClaimSigMessage is the purpose-bound message the box signs to claim over
// the device-cert channel. MUST match the CP verifier
// (vulos-cloud …/cmd/server/wire_vulosaddr.go vulosAddrClaimSigMessage).
func identityClaimSigMessage(ulid string) string {
	return "identity:claim:" + ulid
}

// setIdentityDeviceAuthHeaders attaches the X-Device-* device-cert headers to req
// (mirroring the integrations token-mint client), returning true when it did. It is
// a no-op returning false when no enrolled identity is present or the cert/sig is
// unavailable — the CP then relies on the forwarded session cookie alone.
func setIdentityDeviceAuthHeaders(req *http.Request) bool {
	auther := identityDeviceAuth
	ulid := identityDeviceAuthULID
	if auther == nil || ulid == "" {
		return false
	}
	cert, pub, ok := auther.DeviceCert()
	if !ok {
		return false
	}
	sig, err := auther.SignMint(identityClaimSigMessage(ulid))
	if err != nil {
		return false
	}
	req.Header.Set("X-Device-ULID", ulid)
	req.Header.Set("X-Device-Pubkey", base64.StdEncoding.EncodeToString(pub))
	req.Header.Set("X-Device-Cert", base64.StdEncoding.EncodeToString(cert))
	req.Header.Set("X-Device-Sig", base64.StdEncoding.EncodeToString(sig))
	return true
}

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

// handleIdentityClaim proxies a @vulos.net address claim to the CP.
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
	// OFFLINE FIRST-BOOT: also present this box's owner-attested device cert so the
	// CP can authenticate the enrolled device when no CP session cookie exists yet.
	// The CP accepts EITHER a session OR a device cert; the account is derived
	// server-side from the device→account binding, never from the body we send.
	if setIdentityDeviceAuthHeaders(proxyReq) {
		log.Printf("[identity] claim: forwarding device-cert auth (ulid=%s) for offline first-boot", identityDeviceAuthULID)
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
