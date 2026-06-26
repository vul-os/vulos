package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vulos/backend/services/appnet"
	"vulos/backend/services/auth"
)

// H1: the shared strip+inject helper must drop ALL inbound X-Vulos-* headers
// (identity, storage, integration, session, broker-auth) and re-inject only the
// trusted identity headers. This is the logic now shared by the HTTP and
// WebSocket paths.
func TestApplyTrustedHeaders_StripsAllInboundVulos(t *testing.T) {
	g := newStorageGateway()
	g.storageBrokerSecret = "" // keep storage injection out of this identity test

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// Attacker-supplied spoofs across every seam namespace.
	r.Header.Set("X-Vulos-User-ID", "attacker")
	r.Header.Set("X-Vulos-Email", "attacker@evil.test")
	r.Header.Set("X-Vulos-Session", "forged-session")
	r.Header.Set("X-Vulos-Storage-Access-Key", "leak")
	r.Header.Set("X-Vulos-Storage-Broker-Auth", "forged-broker")
	r.Header.Set("X-Vulos-Integration-Google", "forged-token")
	r.Header.Set("X-Vulos-App-ID", "other-app")

	session := &auth.Session{ID: "sess-1", UserID: "real-user", Email: "real@example.com"}
	g.applyTrustedHeaders(context.Background(), r, session, "office")

	if got := r.Header.Get("X-Vulos-User-ID"); got != "real-user" {
		t.Fatalf("user-id = %q, want real-user (spoof must be replaced)", got)
	}
	if got := r.Header.Get("X-Vulos-Session"); got != "sess-1" {
		t.Fatalf("session = %q, want sess-1", got)
	}
	if got := r.Header.Get("X-Vulos-App-ID"); got != "office" {
		t.Fatalf("app-id = %q, want office", got)
	}
	// No storage resolver wired + no broker secret → storage headers gone.
	if got := r.Header.Get("X-Vulos-Storage-Access-Key"); got != "" {
		t.Fatalf("spoofed storage key survived: %q", got)
	}
	if got := r.Header.Get("X-Vulos-Storage-Broker-Auth"); got != "" {
		t.Fatalf("spoofed broker-auth survived: %q", got)
	}
	// No integration func wired → integration spoof gone.
	if got := r.Header.Get("X-Vulos-Integration-Google"); got != "" {
		t.Fatalf("spoofed integration token survived: %q", got)
	}
}

func TestStripInboundVulosHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("X-Vulos-User-ID", "x")
	h.Set("X-Vulos-Storage-Bucket", "b")
	h.Set("Content-Type", "text/plain")
	h.Set("Authorization", "Bearer t")
	stripInboundVulosHeaders(h)
	if h.Get("X-Vulos-User-ID") != "" || h.Get("X-Vulos-Storage-Bucket") != "" {
		t.Fatal("X-Vulos-* headers must be stripped")
	}
	if h.Get("Content-Type") != "text/plain" || h.Get("Authorization") != "Bearer t" {
		t.Fatal("non X-Vulos headers must be preserved")
	}
}

// M2: X-Vulos-* headers in an app RESPONSE must be stripped before reaching the
// browser so an app cannot leak/forge seam headers (storage creds, identity).
func TestResponse_StripsVulosHeaders(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	token, userID := seedSession(t, store)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Malicious/buggy app tries to send seam headers back to the browser.
		w.Header().Set("X-Vulos-Storage-Secret-Key", "leaked-secret")
		w.Header().Set("X-Vulos-User-ID", "spoofed")
		w.Header().Set("X-Custom-OK", "keep-me")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	addr := upstream.Listener.Addr().String()
	colon := strings.LastIndex(addr, ":")
	ip := addr[:colon]
	var port int
	fmt.Sscanf(addr[colon+1:], "%d", &port)
	ns := &appnet.Namespace{
		Name:    "vulos_respapp",
		AppID:   "respapp",
		OwnerID: userID,
		NSIP:    ip,
		AppPort: port,
		Active:  true,
	}
	mgr.AddNamespace(userID+"-respapp", ns)

	gw := New(store, mgr, pool)
	req := httptest.NewRequest("GET", "/app/respapp/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Vulos-Storage-Secret-Key"); got != "" {
		t.Fatalf("response leaked storage secret: %q", got)
	}
	if got := rec.Header().Get("X-Vulos-User-ID"); got != "" {
		t.Fatalf("response leaked identity header: %q", got)
	}
	// The gateway's own X-Vulos-App is set after stripping.
	if got := rec.Header().Get("X-Vulos-App"); got != "respapp" {
		t.Fatalf("expected gateway X-Vulos-App=respapp, got %q", got)
	}
	if got := rec.Header().Get("X-Custom-OK"); got != "keep-me" {
		t.Fatalf("non-seam response header must survive, got %q", got)
	}
}
