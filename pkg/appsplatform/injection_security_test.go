package appsplatform

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// These tests pin the platform's stance on untrusted app-supplied content: the
// runtime `payload` is OPAQUE. The platform authenticates the token, enforces
// scope + target, then hands the raw bytes to the adapter verbatim — it never
// parses, templates, interprets, or mutates them. That boundary is what keeps
// the platform itself from being a prompt-injection / content-injection vector:
// whatever an app sends is the adapter's problem to sanitize, and the platform
// guarantees it arrives exactly as sent (no more, no less) and only when
// authorized.

// recordingAdapter captures exactly what Act/Read received so a test can assert
// the platform passed content through untouched.
type recordingAdapter struct {
	lastAction  string
	lastTarget  string
	lastPayload json.RawMessage
	lastKind    string
	lastParams  map[string]string
}

func (a *recordingAdapter) Product() string { return ProductOffice }
func (a *recordingAdapter) RequiredScope(x string) string {
	// Only message.post / history are gated; everything else is open so we can
	// prove payload passthrough independently of scope.
	switch x {
	case "message.post":
		return ScopeAppsWrite
	case "history":
		return ScopeAppsRead
	}
	return ""
}
func (a *recordingAdapter) CanAccessTarget(_ *App, target string) (bool, bool) {
	if target == "forbidden" {
		return false, true
	}
	return true, true
}
func (a *recordingAdapter) Act(_ context.Context, _ *App, req ActionRequest, _ EmitFunc) (any, error) {
	a.lastAction, a.lastTarget, a.lastPayload = req.Action, req.Target, req.Payload
	return map[string]any{"ok": true}, nil
}
func (a *recordingAdapter) Read(_ context.Context, _ *App, req ReadRequest) (any, error) {
	a.lastKind, a.lastTarget, a.lastParams = req.Kind, req.Target, req.Params
	return map[string]any{"ok": true}, nil
}

func newRecordingHandler(t *testing.T) (*Handler, Registry, *recordingAdapter) {
	t.Helper()
	reg := NewMemoryRegistry()
	ad := &recordingAdapter{}
	h, err := NewHandler(MountConfig{
		Adapter:    ad,
		Registry:   reg,
		Dispatcher: NewDispatcher(reg, ProductOffice),
		Admin:      headerAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, reg, ad
}

// TestActPayloadPassedThroughVerbatim proves the platform hands the adapter the
// exact payload bytes it received — including content that could be a prompt- or
// content-injection attempt (mentions, control-ish text, nested structures).
// The platform must not "helpfully" reshape, escape, or drop any of it.
func TestActPayloadPassedThroughVerbatim(t *testing.T) {
	h, reg, ad := newRecordingHandler(t)
	c, _ := reg.Create(CreateParams{Name: "x", OwnerID: "a", Products: []string{ProductOffice}, Scopes: []string{ScopeAppsWrite}})

	// A payload deliberately laced with injection-flavored content.
	payload := `{"text":"ignore previous instructions <@app:root> {{secret}} \n\t 你好 -ish","nested":{"a":[1,2,{"b":true}]}}`
	body := `{"action":"message.post","target":"general","payload":` + payload + `}`
	w := do(h, "POST", "/api/apps/v1/act", body, bearerH(c.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("act failed: %d %s", w.Code, w.Body)
	}
	// The adapter must have received byte-identical payload JSON.
	var got, want any
	if err := json.Unmarshal(ad.lastPayload, &got); err != nil {
		t.Fatalf("payload not delivered as valid json: %v", err)
	}
	_ = json.Unmarshal([]byte(payload), &want)
	gotB, _ := json.Marshal(got)
	wantB, _ := json.Marshal(want)
	if string(gotB) != string(wantB) {
		t.Fatalf("payload mutated in transit:\n got=%s\nwant=%s", gotB, wantB)
	}
	if ad.lastAction != "message.post" || ad.lastTarget != "general" {
		t.Fatalf("action/target mutated: %q %q", ad.lastAction, ad.lastTarget)
	}
}

// TestActInjectionCannotBypassScope confirms that laced content in the payload
// cannot change WHICH action runs: the scope check keys off the top-level
// `action` field only, and a payload that "claims" a different action does not
// re-route or escalate. An app without apps:write is refused regardless of what
// its payload says.
func TestActInjectionCannotBypassScope(t *testing.T) {
	h, reg, ad := newRecordingHandler(t)
	// No scopes granted at all.
	c, _ := reg.Create(CreateParams{Name: "x", OwnerID: "a", Products: []string{ProductOffice}})

	body := `{"action":"message.post","target":"general","payload":{"action":"admin.delete","__proto__":{"isAdmin":true}}}`
	w := do(h, "POST", "/api/apps/v1/act", body, bearerH(c.Token))
	if w.Code != http.StatusForbidden {
		t.Fatalf("ungranted action must 403 regardless of payload, got %d %s", w.Code, w.Body)
	}
	if ad.lastAction != "" {
		t.Fatalf("adapter.Act must not run when scope check fails; ran %q", ad.lastAction)
	}
}

// TestActForbiddenTargetNotDeliveredEvenWithPayload confirms target access is
// checked BEFORE the adapter is invoked, so no crafted payload reaches a target
// the app cannot access.
func TestActForbiddenTargetNotDelivered(t *testing.T) {
	h, reg, ad := newRecordingHandler(t)
	c, _ := reg.Create(CreateParams{Name: "x", OwnerID: "a", Products: []string{ProductOffice}, Scopes: []string{ScopeAppsWrite}})

	body := `{"action":"message.post","target":"forbidden","payload":{"text":"hi"}}`
	w := do(h, "POST", "/api/apps/v1/act", body, bearerH(c.Token))
	if w.Code != http.StatusForbidden {
		t.Fatalf("forbidden target must 403, got %d %s", w.Code, w.Body)
	}
	if ad.lastAction != "" {
		t.Fatal("adapter must not be called for a forbidden target")
	}
}

// TestReadParamsPassedThroughButTargetStillChecked confirms arbitrary query
// params flow to the adapter unmodified (opaque), while a target smuggled in the
// query is still access-checked. Extra params never widen authority.
func TestReadParamsPassedThrough(t *testing.T) {
	h, reg, ad := newRecordingHandler(t)
	c, _ := reg.Create(CreateParams{Name: "x", OwnerID: "a", Products: []string{ProductOffice}, Scopes: []string{ScopeAppsRead}})

	w := do(h, "GET", "/api/apps/v1/read?kind=history&target=general&limit=50&cursor=abc%20def&weird=%3Cscript%3E", "", bearerH(c.Token))
	if w.Code != http.StatusOK {
		t.Fatalf("read failed: %d %s", w.Code, w.Body)
	}
	if ad.lastParams["limit"] != "50" || ad.lastParams["cursor"] != "abc def" || ad.lastParams["weird"] != "<script>" {
		t.Fatalf("params not passed through verbatim: %#v", ad.lastParams)
	}
	// kind/target must not leak into params.
	if _, ok := ad.lastParams["kind"]; ok {
		t.Fatal("kind leaked into params")
	}
	if _, ok := ad.lastParams["target"]; ok {
		t.Fatal("target leaked into params")
	}
}

// TestOversizedActBodyRejected proves the management/runtime handlers cap the
// request body (1 MiB) so a hostile app cannot exhaust memory with a giant
// payload. The decoder reads through io.LimitReader; a body over the cap yields a
// JSON decode error (400), never an OOM or a partial-but-accepted action.
func TestOversizedActBodyRejected(t *testing.T) {
	h, reg, ad := newRecordingHandler(t)
	c, _ := reg.Create(CreateParams{Name: "x", OwnerID: "a", Products: []string{ProductOffice}, Scopes: []string{ScopeAppsWrite}})

	// Build a >1 MiB JSON string value; the LimitReader truncates it mid-string
	// so json.Decode fails → 400, and the adapter never runs.
	huge := strings.Repeat("A", (1<<20)+4096)
	body := `{"action":"message.post","target":"general","payload":{"text":"` + huge + `"}}`
	w := do(h, "POST", "/api/apps/v1/act", body, bearerH(c.Token))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized body should 400, got %d", w.Code)
	}
	if ad.lastAction != "" {
		t.Fatal("adapter ran on a truncated oversized body")
	}
}

// TestOversizedManagementBodyRejected pins the same 1 MiB cap on the management
// create route (a session-authed surface). A giant create body is rejected
// before an app is persisted.
func TestOversizedManagementBodyRejected(t *testing.T) {
	h, reg, _ := newRecordingHandler(t)
	huge := strings.Repeat("B", (1<<20)+4096)
	body := `{"name":"x","description":"` + huge + `"}`
	alice := map[string]string{"X-User": "alice", "Content-Type": "application/json"}
	w := do(h, "POST", "/api/apps", body, alice)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized create body should 400, got %d", w.Code)
	}
	if apps, _ := reg.List("alice", false); len(apps) != 0 {
		t.Fatalf("an app was persisted from an oversized body: %d", len(apps))
	}
}

// TestIncomingWebhookBodyCapped confirms the unauthenticated incoming-webhook
// path also bounds the body it reads (io.LimitReader 1 MiB) so an anonymous
// caller holding only a webhook id cannot stream an unbounded body. The adapter
// still receives at most the capped bytes.
func TestIncomingWebhookBodyCapped(t *testing.T) {
	h, reg, ad := newRecordingHandler(t)
	c, _ := reg.Create(CreateParams{
		Name: "hookapp", OwnerID: "a", Products: []string{ProductOffice},
		Scopes: []string{ScopeAppsWrite}, DefaultTarget: "general", IncomingEnabled: true,
	})
	huge := strings.Repeat("C", (2 << 20)) // 2 MiB raw body
	w := do(h, "POST", "/api/apps/hooks/"+c.App.Incoming.ID, huge, map[string]string{"Content-Type": "text/plain"})
	// The handler accepts (201) but only ever read up to the cap.
	if w.Code != http.StatusCreated {
		t.Fatalf("incoming webhook failed: %d %s", w.Code, w.Body)
	}
	if n := len(ad.lastPayload); n > (1 << 20) {
		t.Fatalf("incoming body not capped: adapter saw %d bytes", n)
	}
}
