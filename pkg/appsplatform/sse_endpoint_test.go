package appsplatform

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// These tests harden the SSE runtime endpoint (GET /api/apps/v1/events) at the
// mounted-HTTP level (not just the dispatcher unit): the stream is authorized at
// connect, refuses a cross-product / expired / revoked token, and — end to end
// through the real handler — only ever carries the connecting app's own events.
// They complement the dispatcher-level TestSSESubscriberIsolation by proving the
// isolation holds across the actual token-authed HTTP route.

// newWiredHandler builds an Office handler and returns the dispatcher it uses so a
// test can Emit into the same fan-out the mounted /v1/events route subscribes to.
func newWiredHandler(t *testing.T) (*Handler, Registry, *Dispatcher) {
	t.Helper()
	reg := NewMemoryRegistry()
	ad := &fakeAdapter{}
	disp := NewDispatcher(reg, ad.Product())
	h, err := NewHandler(MountConfig{
		Adapter:    ad,
		Registry:   reg,
		Dispatcher: disp,
		Admin:      headerAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, reg, disp
}

// openSSE opens the /v1/events stream against a live server with the given token
// and returns a channel of the `data:` payloads it receives plus a cancel func.
// The connection is authorized at connect; the returned status is the HTTP code.
func openSSE(t *testing.T, base, token string) (int, <-chan string, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/api/apps/v1/events", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		code := resp.StatusCode
		resp.Body.Close()
		cancel()
		return code, nil, func() {}
	}
	out := make(chan string, 8)
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "data: ") {
				select {
				case out <- strings.TrimPrefix(line, "data: "):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return http.StatusOK, out, cancel
}

// TestEventsEndpointRejectsUnauthedAtConnect proves the SSE route is gated by
// token auth at connect: no token and a bogus token are both 401, and no stream
// is opened.
func TestEventsEndpointRejectsUnauthedAtConnect(t *testing.T) {
	h, _, _ := newTestHandler(t, ProductOffice)
	srv := httptest.NewServer(h)
	defer srv.Close()

	for _, tok := range []string{"", "vat_bogus"} {
		code, _, cancel := openSSE(t, srv.URL, tok)
		cancel()
		if code != http.StatusUnauthorized {
			t.Fatalf("SSE connect with token %q: got %d, want 401", tok, code)
		}
	}
}

// TestEventsEndpointExpiredTokenRejectedAtConnect proves an expired token cannot
// open the stream — expiry is enforced on the SSE route, not only on act/read.
func TestEventsEndpointExpiredTokenRejectedAtConnect(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	srv := httptest.NewServer(h)
	defer srv.Close()

	c, err := reg.Create(CreateParams{Name: "ttl", OwnerID: "a", Products: []string{ProductOffice}, TokenTTL: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)

	code, _, cancel := openSSE(t, srv.URL, c.Token)
	cancel()
	if code != http.StatusUnauthorized {
		t.Fatalf("expired token on SSE connect: got %d, want 401", code)
	}
}

// TestEventsEndpointCrossProductRejectedAtConnect proves a token whose app does
// not target the mounted product cannot open the stream (403), so it can never
// receive another product's events.
func TestEventsEndpointCrossProductRejectedAtConnect(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice) // Office mount
	srv := httptest.NewServer(h)
	defer srv.Close()

	// App targets Board only.
	c, err := reg.Create(CreateParams{Name: "boarder", OwnerID: "a", Products: []string{ProductBoard}})
	if err != nil {
		t.Fatal(err)
	}
	code, _, cancel := openSSE(t, srv.URL, c.Token)
	cancel()
	if code != http.StatusForbidden {
		t.Fatalf("cross-product token on SSE connect: got %d, want 403", code)
	}
}

// TestEventsEndpointDeliversOnlyOwnAppEvents is the end-to-end isolation proof:
// two apps each open /v1/events over the real handler; an event fanned only to
// app A reaches A's stream and never B's. This exercises the token-authed route,
// the per-app Subscribe, and the recipients predicate together.
func TestEventsEndpointDeliversOnlyOwnAppEvents(t *testing.T) {
	h, reg, disp := newWiredHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	a, _ := reg.Create(CreateParams{Name: "A", OwnerID: "o", Products: []string{ProductOffice}})
	b, _ := reg.Create(CreateParams{Name: "B", OwnerID: "o", Products: []string{ProductOffice}})

	codeA, aEvents, cancelA := openSSE(t, srv.URL, a.Token)
	defer cancelA()
	codeB, bEvents, cancelB := openSSE(t, srv.URL, b.Token)
	defer cancelB()
	if codeA != http.StatusOK || codeB != http.StatusOK {
		t.Fatalf("SSE connect codes A=%d B=%d, want 200/200", codeA, codeB)
	}

	// Re-emit (targeted at A only) until A's stream delivers — this closes the
	// connect→Subscribe race without any sleep-based flakiness.
	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	got := false
	for !got {
		select {
		case <-deadline:
			t.Fatal("app A never received its own event over the SSE HTTP route")
		case <-aEvents:
			got = true
		case <-tick.C:
			disp.Emit(EventMessageCreated, map[string]any{"to": "a"}, func(app *App) bool { return app.ID == a.App.ID })
		}
	}
	// B must not have received anything addressed to A. Give any errant delivery a
	// brief window to arrive, then assert the channel is empty.
	select {
	case ev := <-bEvents:
		t.Fatalf("app B received an event addressed only to A over the SSE route: %s", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestRevokedTokenRejectedOnAllRuntimeRoutes proves that uninstalling an app
// revokes its token everywhere on the runtime plane within one cycle: the next
// call on auth.test, act, read, and the SSE connect are all 401 — no lingering
// authority after Delete.
func TestRevokedTokenRejectedOnAllRuntimeRoutes(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	srv := httptest.NewServer(h)
	defer srv.Close()

	c, _ := reg.Create(CreateParams{Name: "doomed", OwnerID: "a", Products: []string{ProductOffice}, Scopes: []string{ScopeAppsWrite, ScopeAppsRead}})

	// Sanity: the token works before revocation.
	if w := do(h, "GET", "/api/apps/v1/auth.test", "", bearerH(c.Token)); w.Code != http.StatusOK {
		t.Fatalf("pre-delete auth.test: got %d, want 200", w.Code)
	}

	if err := reg.Delete(c.App.ID); err != nil {
		t.Fatal(err)
	}

	// Every runtime route must now reject the revoked token.
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/api/apps/v1/auth.test", ""},
		{"POST", "/api/apps/v1/act", `{"action":"message.post","target":"general"}`},
		{"GET", "/api/apps/v1/read?kind=history&target=general", ""},
	} {
		w := do(h, rt.method, rt.path, rt.body, bearerH(c.Token))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("post-delete %s %s: got %d, want 401", rt.method, rt.path, w.Code)
		}
	}
	// SSE connect over the live server too.
	code, _, cancel := openSSE(t, srv.URL, c.Token)
	cancel()
	if code != http.StatusUnauthorized {
		t.Errorf("post-delete SSE connect: got %d, want 401", code)
	}
}

// TestRotatedTokenRejectedOnRuntimeRoutes proves token rotation invalidates the
// old secret on the wire within one cycle: the previous token is 401 on act, and
// the freshly minted token works — no window where both are live.
func TestRotatedTokenRejectedOnRuntimeRoutes(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)
	c, _ := reg.Create(CreateParams{Name: "rot", OwnerID: "a", Products: []string{ProductOffice}, Scopes: []string{ScopeAppsWrite}})

	newTok, err := reg.RotateToken(c.App.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w := do(h, "POST", "/api/apps/v1/act", `{"action":"message.post","target":"general"}`, bearerH(c.Token)); w.Code != http.StatusUnauthorized {
		t.Fatalf("old token after rotate: got %d, want 401", w.Code)
	}
	if w := do(h, "POST", "/api/apps/v1/act", `{"action":"message.post","target":"general"}`, bearerH(newTok)); w.Code != http.StatusOK {
		t.Fatalf("new token after rotate: got %d, want 200: %s", w.Code, w.Body)
	}
}
