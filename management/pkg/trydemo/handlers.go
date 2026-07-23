package trydemo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"

	"github.com/vul-os/vulos-management/pkg/httpx"
)

// remoteIP extracts the client IP from the request.
func remoteIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// HandleStatus — GET /api/try/status
// Returns the current Snapshot, personalised for the requesting token if
// supplied via the X-Try-Token header or "token" query param.
func (m *Manager) HandleStatus(w http.ResponseWriter, r *http.Request) {
	token := tokenFromRequest(r)

	snap := m.queue.Snapshot()
	snap.MachineWarm = m.MachineWarm()

	engaged, _ := m.cg.Engaged(r.Context())
	snap.CostCapEngaged = engaged

	if token != "" {
		snap.YourRole = m.queue.RoleOf(token)
		snap.YourPosition = positionOfToken(m.queue, token)
		if snap.YourRole == RoleDriver {
			snap.DriverSecsLeft = m.queue.SecsLeft()
		}
	}

	httpx.JSON(w, snap)
}

// HandleJoin — POST /api/try/join
// Body: {} (optional). Returns {token, role, position, eta_secs}.
func (m *Manager) HandleJoin(w http.ResponseWriter, r *http.Request) {
	ip := remoteIP(r)
	ctx := r.Context()

	// Start machine if cold.
	m.ensureMachineStarted(ctx)

	token, role, pos, err := m.queue.Join(ctx, ip)
	if err != nil {
		if err == ErrPerIPLimitReached {
			httpx.Err(w, http.StatusTooManyRequests, "per-IP session limit reached")
			return
		}
		log.Printf("[trydemo] join: %v", err)
		httpx.Err(w, http.StatusInternalServerError, "internal error")
		return
	}

	etaSecs := 0
	if pos > 0 {
		etaSecs = pos * m.cfg.DriverSessionSecs / 2
	}

	httpx.JSON(w, map[string]any{
		"token":    token,
		"role":     roleString(role),
		"position": pos,
		"eta_secs": etaSecs,
	})
}

// HandleHeartbeat — POST /api/try/heartbeat
// Body: {token}. Returns {ok, role, secs_left}.
func (m *Manager) HandleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if err := m.queue.Heartbeat(r.Context(), req.Token); err != nil {
		if err == ErrUnknownToken {
			httpx.Err(w, http.StatusUnauthorized, "unknown token")
			return
		}
		httpx.Err(w, http.StatusInternalServerError, "internal error")
		return
	}
	role := m.queue.RoleOf(req.Token)
	secsLeft := 0
	if role == RoleDriver {
		secsLeft = m.queue.SecsLeft()
	}
	httpx.JSON(w, map[string]any{
		"ok":        true,
		"role":      roleString(role),
		"secs_left": secsLeft,
	})
}

// HandleInput — POST /api/try/input
// Body: {token, kind, x?, y?, key?, ...}. Driver-only; resets idle timer.
// Forwards to the demo VM's /api/input endpoint server-side.
func (m *Manager) HandleInput(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8192))
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, "read body failed")
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		httpx.Err(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if err := m.queue.RecordInput(r.Context(), req.Token); err != nil {
		if err == ErrUnknownToken {
			httpx.Err(w, http.StatusUnauthorized, "unknown token")
			return
		}
		if err == ErrNotDriver {
			httpx.Err(w, http.StatusForbidden, "only the driver may send input")
			return
		}
		httpx.Err(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Forward to the demo VM's /api/input endpoint.
	vmURL := vmInputURL()
	if vmURL == "" {
		httpx.JSON(w, map[string]any{"ok": true, "forwarded": false})
		return
	}

	fwdResp, err := forwardJSONBody(r.Context(), vmURL, body)
	if err != nil {
		log.Printf("[trydemo] input forward: %v", err)
		httpx.Err(w, http.StatusBadGateway, "vm unavailable")
		return
	}
	defer fwdResp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(fwdResp.StatusCode)
	_, _ = io.Copy(w, fwdResp.Body)
}

// HandleLeave — POST /api/try/leave
// Body: {token}. Returns {ok}.
func (m *Manager) HandleLeave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	_ = m.queue.Leave(r.Context(), req.Token)
	httpx.JSON(w, map[string]any{"ok": true})
}

// HandleSDPProxy — GET /api/try/stream/sdp
// Proxies the WebRTC SDP signalling to the demo VM's /api/stream endpoint.
// Media flows direct from the demo instance to the client (DTLS/SRTP).
//
// AUTH: requires a valid trydemo session token (X-Try-Token header or ?token=)
// whose role is not RoleNone — i.e. a participant currently in the queue
// (driver or spectator). Without this, the proxy was open to anyone and would
// forward arbitrary client headers to the demo VM. We now (1) reject unknown/
// missing tokens with 401 and (2) forward only a minimal, safe header
// allowlist rather than r.Header.Clone() (which would leak the caller's
// cookies / Authorization to the VM and let a client smuggle hop-by-hop or
// Host-spoofing headers).
func (m *Manager) HandleSDPProxy(w http.ResponseWriter, r *http.Request) {
	// Token gate — same token surface as the other trydemo handlers.
	token := tokenFromRequest(r)
	if token == "" {
		httpx.Err(w, http.StatusUnauthorized, "missing session token")
		return
	}
	if m.queue.RoleOf(token) == RoleNone {
		httpx.Err(w, http.StatusUnauthorized, "unknown or expired session token")
		return
	}

	vmBase := os.Getenv("TRY_DEMO_VM_URL")
	if vmBase == "" {
		httpx.Err(w, http.StatusServiceUnavailable, "demo VM URL not configured")
		return
	}

	targetURL := vmBase + "/api/stream/sdp"
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	fwdReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, "proxy setup failed")
		return
	}
	// Forward only a safe allowlist — never the caller's cookies / Authorization
	// / arbitrary client headers. The SDP body itself carries the WebRTC offer.
	for _, h := range []string{"Content-Type", "Accept"} {
		if v := r.Header.Get(h); v != "" {
			fwdReq.Header.Set(h, v)
		}
	}

	fwdResp, err := http.DefaultClient.Do(fwdReq)
	if err != nil {
		log.Printf("[trydemo] sdp proxy: %v", err)
		httpx.Err(w, http.StatusBadGateway, "demo VM unreachable")
		return
	}
	defer fwdResp.Body.Close()

	for k, vs := range fwdResp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(fwdResp.StatusCode)
	_, _ = io.Copy(w, fwdResp.Body)
}

// --- helpers ---

func tokenFromRequest(r *http.Request) string {
	if t := r.Header.Get("X-Try-Token"); t != "" {
		return t
	}
	return r.URL.Query().Get("token")
}

func positionOfToken(q Queue, token string) int {
	if iq, ok := q.(*inMemQueue); ok {
		iq.mu.Lock()
		defer iq.mu.Unlock()
		return iq.positionOf(token)
	}
	return 0
}

func roleString(r Role) string {
	switch r {
	case RoleDriver:
		return "driver"
	case RoleSpectator:
		return "spectator"
	case RoleQueued:
		return "queued"
	default:
		return "none"
	}
}

func vmInputURL() string {
	base := os.Getenv("TRY_DEMO_VM_URL")
	if base == "" {
		return ""
	}
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	u.Path = "/api/input"
	return u.String()
}

func forwardJSONBody(ctx context.Context, targetURL string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return http.DefaultClient.Do(req)
}
