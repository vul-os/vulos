package consoleboot

// operational_e2e_test.go — the self-host operational E2E.
//
// f15bdb2 wired the operational route groups (device enrollment RFC-8628, the
// OS routing plane: DNS/relay-status/edge/CDN, the third-party OAuth data
// broker, the storage/files plane, and the status surfaces) into the self-host
// binary via cproutes.RegisterOperational → registerNetworkOperational. This
// test BOOTS that console-enabled OSS control plane on a real ephemeral port and
// drives every one of those newly-wired groups over a REAL HTTP client, proving
// two things at once:
//
//  1. each group is actually MOUNTED (never a 404 "route missing"), and
//  2. each is correctly GATED — the unauthenticated / unconfigured request is
//     denied (401/403/503), never a fail-open 2xx, and the authenticated happy
//     path succeeds.
//
// The overarching invariant asserted at the end: NO gated route answered a 2xx
// to an unauthenticated caller. Fail-open on any operational surface is a bug.
//
// This complements consoleboot_test.go (which drives the admin surface via the
// in-process Handler) by exercising the OPERATIONAL API over the wire exactly as
// a self-hoster's box, browser, or scaler would.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/cproutes"
	"github.com/vul-os/vulos-management/pkg/cpserver"
)

// bootConsoleServerOnPort assembles the console-enabled self-host control plane
// (same wiring as cmd/server + consoleboot's bootConsoleServer) but bound to a
// real ephemeral loopback port, runs it, and returns the base URL + a client.
// The server is shut down and closed via t.Cleanup.
func bootConsoleServerOnPort(t *testing.T) (*cpserver.Server, string, *http.Client) {
	t.Helper()
	addr := freeLoopbackPort(t)
	srv, err := cpserver.New(cpserver.Config{
		Addr:        addr,
		Environment: "local",
		Version:     "operational-e2e",
		DBDir:       mustTempDir(),
	}, cpserver.Deps{
		Routes: []cpserver.RouteRegistrar{
			func(mux *http.ServeMux, _ *cpserver.Runtime) error {
				cproutes.RegisterSPAFallback(mux)
				return nil
			},
		},
	})
	if err != nil {
		t.Fatalf("cpserver.New (console-enabled self-host, real port): %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- srv.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-runErr:
			if err != nil {
				t.Errorf("srv.Run returned %v, want clean shutdown", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("srv.Run did not shut down within 10s of ctx cancel")
		}
	})

	base := "http://" + addr
	client := &http.Client{Timeout: 4 * time.Second}
	waitReadyE2E(t, client, base+"/healthz")
	return srv, base, client
}

// TestOperationalRoutes_SelfHost_E2E boots the console-enabled OSS CP and drives
// the newly-wired operational route groups over real HTTP, asserting each is
// mounted + correctly gated, and that none is fail-open.
func TestOperationalRoutes_SelfHost_E2E(t *testing.T) {
	srv, base, client := bootConsoleServerOnPort(t)
	session := realSessionCookie(t, srv)

	// Every gated route we probe unauthenticated. If ANY returns 2xx the surface
	// is fail-open — the single most important invariant of this whole test.
	var failOpen []string
	recordGate := func(name string, code int) {
		if code >= 200 && code < 300 {
			failOpen = append(failOpen, fmt.Sprintf("%s → %d", name, code))
		}
	}

	// ── 1) Device enrollment (RFC-8628): public + rate-limited ────────────────
	t.Run("enrollment", func(t *testing.T) {
		// start is PUBLIC (the box has no session yet): a valid ed25519 pubkey
		// mints a grant → 200 with a device_code + user_code. NOT a 404 (mounted)
		// and NOT gated (public by design).
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("gen device key: %v", err)
		}
		startBody := fmt.Sprintf(`{"device_pubkey":%q}`, base64.StdEncoding.EncodeToString(pub))
		code, body := postE2E(t, client, base+"/enroll/start", "", startBody)
		if code != http.StatusOK {
			t.Fatalf("POST /enroll/start = %d, want 200 (public grant); body=%s", code, body)
		}
		var grant struct {
			DeviceCode string `json:"device_code"`
			UserCode   string `json:"user_code"`
		}
		if err := json.Unmarshal([]byte(body), &grant); err != nil || grant.DeviceCode == "" || grant.UserCode == "" {
			t.Fatalf("enroll/start body missing device_code/user_code: %s", body)
		}

		// poll is PUBLIC: an un-approved grant is authorization_pending — a 200
		// with an RFC-8628 error field (NOT a mint, NOT a 404).
		code, body = postE2E(t, client, base+"/enroll/poll", "", fmt.Sprintf(`{"device_code":%q}`, grant.DeviceCode))
		if code != http.StatusOK {
			t.Fatalf("POST /enroll/poll = %d, want 200 pending; body=%s", code, body)
		}
		if !strings.Contains(body, "authorization_pending") {
			t.Fatalf("POST /enroll/poll body = %s, want authorization_pending", body)
		}

		// approve is GATED for an unauthenticated caller: the global CSRF layer
		// rejects a POST with no browser Origin/Referer (403) before the session
		// gate would (401). Either is fail-closed — what matters is it is mounted
		// (never 404) and never a fail-open 2xx.
		code, body = postE2E(t, client, base+"/enroll/approve", "", fmt.Sprintf(`{"user_code":%q}`, grant.UserCode))
		if code == http.StatusNotFound {
			t.Fatalf("POST /enroll/approve = 404 — route not mounted; body=%s", body)
		}
		if code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Fatalf("POST /enroll/approve (no session) = %d, want 401/403 (gated); body=%s", code, body)
		}
		recordGate("POST /enroll/approve (no session)", code)
	})

	// ── 2) OS routing plane read (DNS / relay-status) ─────────────────────────
	t.Run("os_routing_read", func(t *testing.T) {
		// relay status is session-gated: no session → 401 (mounted, gated).
		code, body := getE2E(t, client, base+"/api/relay/status", nil)
		if code == http.StatusNotFound {
			t.Fatalf("GET /api/relay/status = 404 — route not mounted; body=%s", body)
		}
		if code != http.StatusUnauthorized {
			t.Fatalf("GET /api/relay/status (no session) = %d, want 401; body=%s", code, body)
		}
		recordGate("GET /api/relay/status (no session)", code)

		// authed happy path: the caller's own per-account relay view → 200.
		code, body = getE2E(t, client, base+"/api/relay/status", session)
		if code != http.StatusOK {
			t.Fatalf("GET /api/relay/status (session) = %d, want 200; body=%s", code, body)
		}

		// DNS plane zone read is session-gated + owner-scoped: no session → 401.
		const someULID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
		code, body = getE2E(t, client, base+"/api/dnsplane/zone/"+someULID, nil)
		if code == http.StatusNotFound {
			t.Fatalf("GET /api/dnsplane/zone = 404 — route not mounted; body=%s", body)
		}
		if code != http.StatusUnauthorized {
			t.Fatalf("GET /api/dnsplane/zone (no session) = %d, want 401; body=%s", code, body)
		}
		recordGate("GET /api/dnsplane/zone (no session)", code)
	})

	// ── 3) Integrations (third-party OAuth broker): fails closed ──────────────
	t.Run("integrations_fail_closed", func(t *testing.T) {
		// list is session-gated: no session → 401 (mounted, gated).
		code, body := getE2E(t, client, base+"/api/integrations", nil)
		if code == http.StatusNotFound {
			t.Fatalf("GET /api/integrations = 404 — route not mounted; body=%s", body)
		}
		if code != http.StatusUnauthorized {
			t.Fatalf("GET /api/integrations (no session) = %d, want 401; body=%s", code, body)
		}
		recordGate("GET /api/integrations (no session)", code)

		// FAIL-CLOSED: an authenticated connect start with NO OAuth/KEK config
		// refuses (503) rather than fabricating a broken consent redirect. The
		// broker never custodies tokens under a default key. Never a 2xx, never a
		// 5xx crash — an honest "not configured".
		code, body = getE2E(t, client, base+"/api/integrations/google/start", session)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("GET /api/integrations/google/start (session, no OAuth cfg) = %d, want 503 fail-closed; body=%s", code, body)
		}
	})

	// ── 4) Storage / Files plane (BYO no-op) ──────────────────────────────────
	t.Run("storage_byo", func(t *testing.T) {
		// byo status is session-gated: no session → 401 (mounted, gated).
		code, body := getE2E(t, client, base+"/api/storage/byo", nil)
		if code == http.StatusNotFound {
			t.Fatalf("GET /api/storage/byo = 404 — route not mounted; body=%s", body)
		}
		if code != http.StatusUnauthorized {
			t.Fatalf("GET /api/storage/byo (no session) = %d, want 401; body=%s", code, body)
		}
		recordGate("GET /api/storage/byo (no session)", code)

		// authed happy path: a fresh account has no BYO bucket connected → 200
		// with connected=false. This is the bring-your-own-bucket no-op posture:
		// the plane is live and self-scoped, it just provisions nothing.
		code, body = getE2E(t, client, base+"/api/storage/byo", session)
		if code != http.StatusOK {
			t.Fatalf("GET /api/storage/byo (session) = %d, want 200; body=%s", code, body)
		}
		var byo struct {
			BYO       bool `json:"byo"`
			Connected bool `json:"connected"`
		}
		if err := json.Unmarshal([]byte(body), &byo); err != nil {
			t.Fatalf("decode /api/storage/byo: %v; body=%s", err, body)
		}
		if byo.Connected || byo.BYO {
			t.Fatalf("fresh account byo status = %+v, want not-connected (BYO no-op)", byo)
		}
	})

	// ── 5) Status surfaces (public + authenticated) ───────────────────────────
	t.Run("status", func(t *testing.T) {
		// cloud status is PUBLIC → 200 (no tenant data).
		code, body := getE2E(t, client, base+"/api/cloud/status", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /api/cloud/status = %d, want 200 (public); body=%s", code, body)
		}

		// account status is AUTHENTICATED: no session → 401 (mounted, gated).
		code, body = getE2E(t, client, base+"/api/account/status", nil)
		if code == http.StatusNotFound {
			t.Fatalf("GET /api/account/status = 404 — route not mounted; body=%s", body)
		}
		if code != http.StatusUnauthorized {
			t.Fatalf("GET /api/account/status (no session) = %d, want 401; body=%s", code, body)
		}
		recordGate("GET /api/account/status (no session)", code)

		// authed happy path → 200 (the caller's own status, strictly self-scoped).
		code, body = getE2E(t, client, base+"/api/account/status", session)
		if code != http.StatusOK {
			t.Fatalf("GET /api/account/status (session) = %d, want 200; body=%s", code, body)
		}
	})

	// ── Overarching invariant: NOTHING gated was fail-open ────────────────────
	if len(failOpen) > 0 {
		t.Fatalf("FAIL-OPEN operational routes served 2xx to an unauthenticated caller: %s", strings.Join(failOpen, "; "))
	}
}

// realSessionCookie signs up + logs in a fresh account through the runtime auth
// store and returns its live main-session cookie, for use over the wire.
func realSessionCookie(t *testing.T, srv *cpserver.Server) *http.Cookie {
	t.Helper()
	rt := srv.Runtime()
	ctx := context.Background()
	email := fmt.Sprintf("e2e-%d@example.test", time.Now().UnixNano())
	const pw = "operational-e2e-password-123456"
	if _, _, err := rt.AuthStore.Signup(ctx, email, pw, "127.0.0.1", "e2e"); err != nil {
		t.Fatalf("signup e2e user: %v", err)
	}
	res, err := rt.AuthStore.Login(ctx, email, pw, "127.0.0.1", "e2e")
	if err != nil || res == nil || res.Token == "" {
		t.Fatalf("login e2e user: %v", err)
	}
	return &http.Cookie{Name: auth.SessionCookieName, Value: res.Token}
}

// freeLoopbackPort reserves an ephemeral loopback port and releases it for reuse.
func freeLoopbackPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve ephemeral port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// waitReadyE2E polls url until it answers 200 or the deadline elapses.
func waitReadyE2E(t *testing.T, c *http.Client, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := c.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("server did not become ready at %s within 5s", url)
}

func getE2E(t *testing.T, c *http.Client, url string, cookie *http.Cookie) (int, string) {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", url, err)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	resp, err := c.Do(r)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func postE2E(t *testing.T, c *http.Client, url string, cookieVal, body string) (int, string) {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build POST %s: %v", url, err)
	}
	r.Header.Set("Content-Type", "application/json")
	if cookieVal != "" {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookieVal})
	}
	resp, err := c.Do(r)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
