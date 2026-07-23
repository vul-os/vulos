package appsplatform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// webhookScopedAdapter wraps fakeAdapter but requires a specific scope for
// "incoming_webhook" actions, so we can test scope denial on the webhook path.
type webhookScopedAdapter struct {
	fakeAdapter
	webhookScope string
}

func (w *webhookScopedAdapter) RequiredScope(actionOrKind string) string {
	if actionOrKind == "incoming_webhook" {
		return w.webhookScope
	}
	return w.fakeAdapter.RequiredScope(actionOrKind)
}

func (w *webhookScopedAdapter) Product() string { return ProductOffice }

func (w *webhookScopedAdapter) CanAccessTarget(app *App, target string) (bool, bool) {
	return w.fakeAdapter.CanAccessTarget(app, target)
}

func (w *webhookScopedAdapter) Act(ctx context.Context, app *App, req ActionRequest, emit EmitFunc) (any, error) {
	return w.fakeAdapter.Act(ctx, app, req, emit)
}

func (w *webhookScopedAdapter) Read(ctx context.Context, app *App, req ReadRequest) (any, error) {
	return w.fakeAdapter.Read(ctx, app, req)
}

// newWebhookScopedHandler builds a handler backed by webhookScopedAdapter.
func newWebhookScopedHandler(t *testing.T, webhookScope string) (*Handler, Registry, *webhookScopedAdapter) {
	t.Helper()
	reg := NewMemoryRegistry()
	ad := &webhookScopedAdapter{webhookScope: webhookScope}
	h, err := NewHandler(MountConfig{
		Adapter:    ad,
		Registry:   reg,
		Dispatcher: NewDispatcher(reg, ad.Product()),
		Admin:      headerAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, reg, ad
}

// TestIncomingWebhookPinsToDefaultTarget verifies that the body-supplied
// "target" field is completely ignored: Act is always called with the app's
// configured DefaultTarget, regardless of what the caller puts in the body.
func TestIncomingWebhookPinsToDefaultTarget(t *testing.T) {
	h, reg, ad := newTestHandler(t, ProductOffice)
	c, _ := reg.Create(CreateParams{
		Name:            "hook",
		OwnerID:         "alice",
		Products:        []string{ProductOffice},
		DefaultTarget:   "general",
		IncomingEnabled: true,
	})

	// Post with an alternate body-supplied target — must be ignored.
	w := do(h, "POST", "/api/apps/hooks/"+c.App.Incoming.ID,
		`{"target":"other-channel","text":"ci done"}`,
		map[string]string{"Content-Type": "application/json"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body)
	}
	if ad.acted != 1 {
		t.Fatalf("Act should have been called once, got acted=%d", ad.acted)
	}

	// Confirm Act received the configured DefaultTarget, not the body's target.
	var resp struct {
		Result map[string]any `json:"result"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if got, _ := resp.Result["target"].(string); got != "general" {
		t.Errorf("Act received target %q, want %q (body target must be ignored)", got, "general")
	}
}

// TestIncomingWebhookBodyTargetCannotWidenScope verifies that supplying a
// body target that bypasses the adapter's CanAccessTarget check is impossible:
// the request is always evaluated against the configured DefaultTarget, so a
// body-supplied forbidden/missing target does NOT cause a denial (the DefaultTarget
// is used), and a body-supplied allowed-but-different target does NOT widen access.
func TestIncomingWebhookBodyTargetCannotWidenScope(t *testing.T) {
	h, reg, ad := newTestHandler(t, ProductOffice)
	// App whose DefaultTarget is the accessible "general".
	c, _ := reg.Create(CreateParams{
		Name:            "hook",
		OwnerID:         "alice",
		Products:        []string{ProductOffice},
		DefaultTarget:   "general",
		IncomingEnabled: true,
	})

	// Even though body contains "secret" (which CanAccessTarget denies), the
	// request succeeds — because the body target is ignored and DefaultTarget
	// ("general") is used instead.
	w := do(h, "POST", "/api/apps/hooks/"+c.App.Incoming.ID,
		`{"target":"secret","text":"trying to widen"}`,
		map[string]string{"Content-Type": "application/json"})
	if w.Code != http.StatusCreated {
		t.Fatalf("body target 'secret' was not ignored — got %d: %s", w.Code, w.Body)
	}
	if ad.acted != 1 {
		t.Fatalf("Act should have been called once, got acted=%d", ad.acted)
	}

	// Confirm the target passed to Act is the configured DefaultTarget.
	var resp struct {
		Result map[string]any `json:"result"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if got, _ := resp.Result["target"].(string); got != "general" {
		t.Errorf("Act received target %q, want %q", got, "general")
	}
}

// TestIncomingWebhookForbiddenDefaultTargetDenied verifies that when the app's
// configured DefaultTarget is inaccessible (CanAccessTarget returns false,true),
// the incoming webhook path returns 403 — mirroring the authenticated runtime path.
func TestIncomingWebhookForbiddenDefaultTargetDenied(t *testing.T) {
	h, reg, ad := newTestHandler(t, ProductOffice)
	// DefaultTarget "secret" makes CanAccessTarget return (false, true) → 403.
	c, _ := reg.Create(CreateParams{
		Name:            "hook",
		OwnerID:         "alice",
		Products:        []string{ProductOffice},
		DefaultTarget:   "secret",
		IncomingEnabled: true,
	})

	w := do(h, "POST", "/api/apps/hooks/"+c.App.Incoming.ID,
		`{"text":"hi"}`,
		map[string]string{"Content-Type": "application/json"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("forbidden DefaultTarget should return 403, got %d: %s", w.Code, w.Body)
	}
	if ad.acted != 0 {
		t.Errorf("Act must not be called when target is forbidden, acted=%d", ad.acted)
	}
}

// TestIncomingWebhookMissingDefaultTargetDenied verifies that when the app's
// configured DefaultTarget does not exist (CanAccessTarget returns false,false),
// the incoming webhook path returns 404.
func TestIncomingWebhookMissingDefaultTargetDenied(t *testing.T) {
	h, reg, ad := newTestHandler(t, ProductOffice)
	// DefaultTarget "missing" makes CanAccessTarget return (false, false) → 404.
	c, _ := reg.Create(CreateParams{
		Name:            "hook",
		OwnerID:         "alice",
		Products:        []string{ProductOffice},
		DefaultTarget:   "missing",
		IncomingEnabled: true,
	})

	w := do(h, "POST", "/api/apps/hooks/"+c.App.Incoming.ID,
		`{"text":"hi"}`,
		map[string]string{"Content-Type": "application/json"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing DefaultTarget should return 404, got %d: %s", w.Code, w.Body)
	}
	if ad.acted != 0 {
		t.Errorf("Act must not be called when target does not exist, acted=%d", ad.acted)
	}
}

// TestIncomingWebhookOutOfScopeActionDenied verifies that when the adapter
// requires a scope for "incoming_webhook" that the app was not granted, the
// webhook path returns 403 — the same enforcement applied on the runtime path.
func TestIncomingWebhookOutOfScopeActionDenied(t *testing.T) {
	const requiredScope = ScopeAppsWrite

	h, reg, ad := newWebhookScopedHandler(t, requiredScope)

	// App that does NOT have the scope required for incoming_webhook.
	noScope, _ := reg.Create(CreateParams{
		Name:            "hook-noscope",
		OwnerID:         "alice",
		Products:        []string{ProductOffice},
		DefaultTarget:   "general",
		IncomingEnabled: true,
		// Scopes intentionally omitted — no apps:write granted.
	})

	w := do(h, "POST", "/api/apps/hooks/"+noScope.App.Incoming.ID,
		`{"text":"hi"}`,
		map[string]string{"Content-Type": "application/json"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing scope should return 403, got %d: %s", w.Code, w.Body)
	}
	if ad.fakeAdapter.acted != 0 {
		t.Errorf("Act must not be called when scope is missing, acted=%d", ad.fakeAdapter.acted)
	}

	// Same adapter, but an app WITH the required scope — must succeed.
	withScope, _ := reg.Create(CreateParams{
		Name:            "hook-scoped",
		OwnerID:         "alice",
		Products:        []string{ProductOffice},
		DefaultTarget:   "general",
		IncomingEnabled: true,
		Scopes:          []string{requiredScope},
	})

	w = do(h, "POST", "/api/apps/hooks/"+withScope.App.Incoming.ID,
		`{"text":"hi"}`,
		map[string]string{"Content-Type": "application/json"})
	if w.Code != http.StatusCreated {
		t.Fatalf("granted scope should allow webhook, got %d: %s", w.Code, w.Body)
	}
	if ad.fakeAdapter.acted != 1 {
		t.Errorf("Act should be called exactly once for granted-scope app, acted=%d", ad.fakeAdapter.acted)
	}
}

// TestIncomingWebhookActsOnConfiguredTargetOnly is an integration assertion:
// given two apps each with a different DefaultTarget, each webhook only ever
// drives Act with ITS OWN target — no cross-contamination, no body override.
func TestIncomingWebhookActsOnConfiguredTargetOnly(t *testing.T) {
	h, reg, _ := newTestHandler(t, ProductOffice)

	alpha, _ := reg.Create(CreateParams{
		Name: "alpha", OwnerID: "a", Products: []string{ProductOffice},
		DefaultTarget: "alpha-channel", IncomingEnabled: true,
	})
	beta, _ := reg.Create(CreateParams{
		Name: "beta", OwnerID: "a", Products: []string{ProductOffice},
		DefaultTarget: "beta-channel", IncomingEnabled: true,
	})

	actTarget := func(webhookID, bodyTarget string) string {
		body := `{"target":"` + bodyTarget + `","text":"x"}`
		w := do(h, "POST", "/api/apps/hooks/"+webhookID, body,
			map[string]string{"Content-Type": "application/json"})
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body)
		}
		var resp struct {
			Result map[string]any `json:"result"`
		}
		json.Unmarshal(w.Body.Bytes(), &resp)
		tgt, _ := resp.Result["target"].(string)
		return tgt
	}

	// Alpha's webhook must use alpha-channel even if body says beta-channel.
	if got := actTarget(alpha.App.Incoming.ID, "beta-channel"); got != "alpha-channel" {
		t.Errorf("alpha webhook used target %q, want %q", got, "alpha-channel")
	}
	// Beta's webhook must use beta-channel even if body says alpha-channel.
	if got := actTarget(beta.App.Incoming.ID, "alpha-channel"); got != "beta-channel" {
		t.Errorf("beta webhook used target %q, want %q", got, "beta-channel")
	}
}

// ---- Incoming webhook rate limiting ------------------------------------------

// newRateLimitedHandler builds an Office handler with an explicit limiter.
func newRateLimitedHandler(t *testing.T, limiter RateLimiter) (*Handler, Registry, *fakeAdapter) {
	t.Helper()
	reg := NewMemoryRegistry()
	ad := &fakeAdapter{}
	h, err := NewHandler(MountConfig{
		Adapter:    ad,
		Registry:   reg,
		Dispatcher: NewDispatcher(reg, ad.Product()),
		Admin:      headerAdmin,
		Limiter:    limiter,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, reg, ad
}

// postHook posts a webhook body from an explicit client IP.
func postHook(h http.Handler, webhookID, ip string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/apps/hooks/"+webhookID, strings.NewReader(`{"text":"x"}`))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = ip + ":40000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestIncomingWebhookRateLimitedPerIP verifies the unauthenticated webhook route
// is bounded per client IP: unbounded POST volume from one source must be
// throttled with 429, and the adapter must not be driven by the throttled calls.
func TestIncomingWebhookRateLimitedPerIP(t *testing.T) {
	// Per-hook generous, per-IP burst=1: the second POST from the same IP is denied.
	h, reg, ad := newRateLimitedHandler(t, NewTokenBucketLimiter(1000, 1000, 1, 1))
	c, _ := reg.Create(CreateParams{
		Name: "hook", OwnerID: "alice", Products: []string{ProductOffice},
		DefaultTarget: "general", IncomingEnabled: true,
	})

	if w := postHook(h, c.App.Incoming.ID, "203.0.113.7"); w.Code != http.StatusCreated {
		t.Fatalf("first webhook post should be 201, got %d: %s", w.Code, w.Body)
	}
	if w := postHook(h, c.App.Incoming.ID, "203.0.113.7"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second webhook post from same IP should be 429, got %d: %s", w.Code, w.Body)
	}
	if ad.acted != 1 {
		t.Errorf("Act must not run for rate-limited requests, acted=%d", ad.acted)
	}
	// A different IP has its own bucket and is still served.
	if w := postHook(h, c.App.Incoming.ID, "203.0.113.8"); w.Code != http.StatusCreated {
		t.Fatalf("different IP should not be limited, got %d: %s", w.Code, w.Body)
	}
}

// TestIncomingWebhookRateLimitedPerHook verifies a leaked webhook URL cannot be
// used for unthrottled message injection even from a rotating set of IPs: the
// hook id is itself the credential and gets its own bucket.
func TestIncomingWebhookRateLimitedPerHook(t *testing.T) {
	// Per-IP generous, per-hook burst=1: the second POST to the same hook is denied
	// even though it arrives from a different IP.
	h, reg, ad := newRateLimitedHandler(t, NewTokenBucketLimiter(1, 1, 1000, 1000))
	leaked, _ := reg.Create(CreateParams{
		Name: "leaked", OwnerID: "alice", Products: []string{ProductOffice},
		DefaultTarget: "general", IncomingEnabled: true,
	})
	other, _ := reg.Create(CreateParams{
		Name: "other", OwnerID: "alice", Products: []string{ProductOffice},
		DefaultTarget: "general", IncomingEnabled: true,
	})

	if w := postHook(h, leaked.App.Incoming.ID, "198.51.100.1"); w.Code != http.StatusCreated {
		t.Fatalf("first webhook post should be 201, got %d: %s", w.Code, w.Body)
	}
	if w := postHook(h, leaked.App.Incoming.ID, "198.51.100.2"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second post to the same hook from a new IP should be 429, got %d: %s", w.Code, w.Body)
	}
	if ad.acted != 1 {
		t.Errorf("Act must not run for rate-limited requests, acted=%d", ad.acted)
	}
	// The bucket is per hook, not global: another app's webhook still works.
	if w := postHook(h, other.App.Incoming.ID, "198.51.100.3"); w.Code != http.StatusCreated {
		t.Fatalf("a different hook should have its own bucket, got %d: %s", w.Code, w.Body)
	}
}

// TestIncomingWebhookUnknownIDRateLimited verifies the per-IP limit is applied
// BEFORE the registry lookup, so guessing at webhook ids (which are secrets) is
// throttled rather than costing one registry hit per probe.
func TestIncomingWebhookUnknownIDRateLimited(t *testing.T) {
	h, _, ad := newRateLimitedHandler(t, NewTokenBucketLimiter(1000, 1000, 1, 1))

	if w := postHook(h, "deadbeef", "192.0.2.55"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown hook should be 404, got %d: %s", w.Code, w.Body)
	}
	if w := postHook(h, "deadbee0", "192.0.2.55"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second probe from the same IP should be 429, got %d: %s", w.Code, w.Body)
	}
	if ad.acted != 0 {
		t.Errorf("Act must never run for unknown hooks, acted=%d", ad.acted)
	}
}

// TestIncomingWebhookNoopLimiterUnthrottled confirms the opt-out still works:
// with NoopRateLimiter the webhook route is not throttled at all.
func TestIncomingWebhookNoopLimiterUnthrottled(t *testing.T) {
	h, reg, ad := newRateLimitedHandler(t, NoopRateLimiter{})
	c, _ := reg.Create(CreateParams{
		Name: "hook", OwnerID: "alice", Products: []string{ProductOffice},
		DefaultTarget: "general", IncomingEnabled: true,
	})

	for i := 0; i < 50; i++ {
		if w := postHook(h, c.App.Incoming.ID, "203.0.113.9"); w.Code != http.StatusCreated {
			t.Fatalf("NoopRateLimiter must not throttle (request %d): got %d: %s", i, w.Code, w.Body)
		}
	}
	if ad.acted != 50 {
		t.Errorf("all 50 posts should have reached the adapter, acted=%d", ad.acted)
	}
}
