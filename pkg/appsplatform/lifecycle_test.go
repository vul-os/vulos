package appsplatform

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// These tests cover the app install / update / uninstall lifecycle (the registry
// state machine) and malformed-manifest rejection, both through the registry
// directly and through the HTTP management surface.

// TestLifecycleStateMachine walks an app through install → update → rotate →
// uninstall and asserts each transition's invariants, including that a deleted
// app is gone from every index.
func TestLifecycleStateMachine(t *testing.T) {
	r := NewMemoryRegistry()

	// Install.
	c, err := r.Create(CreateParams{
		Name: "ci-bot", OwnerID: "alice", Products: []string{ProductOffice},
		Scopes: []string{ScopeAppsWrite}, IncomingEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(c.App.ID); err != nil {
		t.Fatal("installed app not retrievable")
	}
	if _, err := r.GetByIncomingWebhookID(c.App.Incoming.ID); err != nil {
		t.Fatal("incoming webhook not indexed at install")
	}

	// Update: add a product and a scope.
	prods := []string{ProductOffice, ProductBoard}
	scopes := []string{ScopeAppsWrite, ScopeAppsRead}
	upd, err := r.Update(c.App.ID, UpdateParams{Products: &prods, Scopes: &scopes})
	if err != nil {
		t.Fatal(err)
	}
	if !upd.TargetsProduct(ProductBoard) || !upd.HasScope(ScopeAppsRead) {
		t.Fatalf("update did not apply: %+v", upd)
	}

	// Rotating the token must invalidate the old one and validate the new.
	newTok, err := r.RotateToken(c.App.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetByTokenHash(HashToken(c.Token)); err != ErrNotFound {
		t.Fatal("old token still valid after rotate")
	}
	if _, err := r.GetByTokenHash(HashToken(newTok)); err != nil {
		t.Fatal("new token invalid after rotate")
	}

	// Uninstall: gone from id, token, and webhook indexes.
	if err := r.Delete(c.App.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(c.App.ID); err != ErrNotFound {
		t.Fatal("app still present after delete")
	}
	if _, err := r.GetByTokenHash(HashToken(newTok)); err != ErrNotFound {
		t.Fatal("token still resolves after delete")
	}
	if _, err := r.GetByIncomingWebhookID(c.App.Incoming.ID); err != ErrNotFound {
		t.Fatal("webhook still resolves after delete")
	}
}

// TestUpdateRotateDeleteUnknownApp asserts mutations on a non-existent app are
// ErrNotFound, never a silent create.
func TestMutateUnknownAppIsNotFound(t *testing.T) {
	r := NewMemoryRegistry()
	if _, err := r.Update("ghost", UpdateParams{}); err != ErrNotFound {
		t.Errorf("Update unknown = %v, want ErrNotFound", err)
	}
	if _, err := r.RotateToken("ghost"); err != ErrNotFound {
		t.Errorf("RotateToken unknown = %v, want ErrNotFound", err)
	}
	if _, err := r.RotateSecret("ghost"); err != ErrNotFound {
		t.Errorf("RotateSecret unknown = %v, want ErrNotFound", err)
	}
	if err := r.Delete("ghost"); err != ErrNotFound {
		t.Errorf("Delete unknown = %v, want ErrNotFound", err)
	}
}

// TestIncomingWebhookEnableDisableCycle confirms the enabled flag is the gate:
// the id persists across toggles but a disabled hook is not honored at runtime.
func TestIncomingWebhookEnableDisableCycle(t *testing.T) {
	h, reg, ad := newTestHandler(t, ProductOffice)
	c, _ := reg.Create(CreateParams{Name: "hook", OwnerID: "a", Products: []string{ProductOffice}, DefaultTarget: "general", IncomingEnabled: true})
	hookPath := "/api/apps/hooks/" + c.App.Incoming.ID
	jsonH := map[string]string{"Content-Type": "application/json"}

	if w := do(h, "POST", hookPath, `{"text":"go"}`, jsonH); w.Code != http.StatusCreated {
		t.Fatalf("enabled hook should accept, got %d", w.Code)
	}
	reg.Update(c.App.ID, UpdateParams{IncomingEnabled: ptr(false)})
	if w := do(h, "POST", hookPath, `{"text":"go"}`, jsonH); w.Code != http.StatusNotFound {
		t.Fatalf("disabled hook should 404, got %d", w.Code)
	}
	// Re-enable: same id works again (id is stable across the cycle).
	reg.Update(c.App.ID, UpdateParams{IncomingEnabled: ptr(true)})
	if w := do(h, "POST", hookPath, `{"text":"go"}`, jsonH); w.Code != http.StatusCreated {
		t.Fatalf("re-enabled hook should accept, got %d", w.Code)
	}
	if ad.acted != 2 {
		t.Fatalf("adapter acted %d times, want 2 (disabled call must not reach it)", ad.acted)
	}
}

// TestMalformedManifestRejectedAtCreate covers the registry's manifest
// validation: unknown scope, unknown product, and SSRF webhook are all rejected
// before any state is written.
func TestMalformedManifestRejectedAtCreate(t *testing.T) {
	t.Setenv(AllowPrivateWebhooksEnv, "")
	r := NewMemoryRegistry()
	cases := []struct {
		name string
		p    CreateParams
	}{
		{"unknown scope", CreateParams{Name: "x", Products: []string{ProductOffice}, Scopes: []string{"bogus:delete"}}},
		{"unknown product", CreateParams{Name: "x", Products: []string{"fax"}}},
		{"ssrf webhook", CreateParams{Name: "x", Products: []string{ProductOffice}, WebhookURL: "http://169.254.169.254/"}},
		{"non-http webhook", CreateParams{Name: "x", Products: []string{ProductOffice}, WebhookURL: "file:///etc/passwd"}},
	}
	for _, tc := range cases {
		if _, err := r.Create(tc.p); err == nil {
			t.Errorf("%s: Create should have failed", tc.name)
		}
	}
	// No partial state should have leaked in.
	if apps, _ := r.List("", true); len(apps) != 0 {
		t.Fatalf("rejected creates left %d apps behind", len(apps))
	}
}

// TestMalformedManifestRejectedOverHTTP covers the HTTP create handler: missing
// name is 400, invalid JSON is 400, and a bad scope surfaces the registry error
// as 400 — never a 500.
func TestMalformedManifestRejectedOverHTTP(t *testing.T) {
	h, _, _ := newTestHandler(t, ProductOffice)
	alice := map[string]string{"X-User": "alice", "Content-Type": "application/json"}

	cases := []struct {
		name, body string
		want       int
	}{
		{"empty name", `{"scopes":["apps:write"]}`, http.StatusBadRequest},
		{"whitespace name", `{"name":"   "}`, http.StatusBadRequest},
		{"invalid json", `{"name":`, http.StatusBadRequest},
		{"unknown scope", `{"name":"x","scopes":["root:all"]}`, http.StatusBadRequest},
		{"unknown product", `{"name":"x","products":["pager"]}`, http.StatusBadRequest},
		{"truncated", `{`, http.StatusBadRequest},
		{"array not object", `[1,2,3]`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		w := do(h, "POST", "/api/apps", tc.body, alice)
		if w.Code != tc.want {
			t.Errorf("%s: got %d, want %d (%s)", tc.name, w.Code, tc.want, w.Body)
		}
		// Never a server error from malformed input.
		if w.Code >= 500 {
			t.Errorf("%s: malformed manifest produced a 5xx (%d)", tc.name, w.Code)
		}
	}
}

// TestUpdateRejectsScopeEscalationToUnknown ensures an update cannot introduce a
// scope outside the registry's scope set (a typo never silently grants access).
func TestUpdateRejectsUnknownScope(t *testing.T) {
	r := NewMemoryRegistry()
	c, _ := r.Create(CreateParams{Name: "x", Products: []string{ProductOffice}, Scopes: []string{ScopeAppsWrite}})
	bad := []string{"admin:everything"}
	if _, err := r.Update(c.App.ID, UpdateParams{Scopes: &bad}); err == nil {
		t.Fatal("Update accepted an unknown scope")
	}
	// Original scope is unchanged.
	cur, _ := r.Get(c.App.ID)
	if !cur.HasScope(ScopeAppsWrite) || cur.HasScope("admin:everything") {
		t.Fatalf("scopes mutated by a rejected update: %v", cur.Scopes)
	}
}

// TestCreateNormalizesManifest verifies dedup/trim/lowercasing of scopes,
// products, events and slash commands at install.
func TestCreateNormalizesManifest(t *testing.T) {
	r := NewMemoryRegistry()
	c, err := r.Create(CreateParams{
		Name:          "  Tidy  ",
		Products:      []string{"office", "OFFICE", " office "},
		Scopes:        []string{"apps:write", "APPS:WRITE"},
		Events:        []string{"Message.Created", "message.created"},
		SlashCommands: []SlashCommand{{Name: "/Deploy"}, {Name: "deploy"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.App.Name != "Tidy" {
		t.Errorf("name not trimmed: %q", c.App.Name)
	}
	if len(c.App.Products) != 1 || len(c.App.Scopes) != 1 {
		t.Errorf("products/scopes not de-duped: %v %v", c.App.Products, c.App.Scopes)
	}
	if len(c.App.Events) != 1 || c.App.Events[0] != "message.created" {
		t.Errorf("events not normalized: %v", c.App.Events)
	}
	if len(c.App.SlashCommands) != 1 || c.App.SlashCommands[0].Name != "deploy" {
		t.Errorf("slash commands not normalized: %v", c.App.SlashCommands)
	}
}

// TestSummaryNeverLeaksSecrets is a belt-and-braces check that the serialized
// summary (the consolidation contract) carries no token hash or signing secret.
func TestSummaryNeverLeaksSecrets(t *testing.T) {
	r := NewMemoryRegistry()
	c, _ := r.Create(CreateParams{Name: "x", Products: []string{ProductOffice}})
	app, _ := r.Get(c.App.ID)
	blob, _ := json.Marshal(app.ToSummary("/api/apps"))
	s := string(blob)
	for _, leak := range []string{app.TokenHash, app.SigningSecret, "token_hash", "signing_secret", "vas_"} {
		if leak != "" && strings.Contains(s, leak) {
			t.Fatalf("summary leaked %q: %s", leak, s)
		}
	}
}
