package appsplatform

import (
	"path/filepath"
	"testing"
)

// ---- signing ----------------------------------------------------------------

func TestSignVerifyRoundTrip(t *testing.T) {
	ts, body, secret := NowTimestamp(), []byte(`{"hello":"world"}`), "vas_secret"
	sig := Sign(ts, body, secret)
	if !Verify(ts, body, secret, sig) {
		t.Fatal("valid signature did not verify")
	}
}

func TestSignDeterministic(t *testing.T) {
	if Sign("123", []byte("x"), "s") != Sign("123", []byte("x"), "s") {
		t.Fatal("Sign is not deterministic")
	}
}

func TestVerifyWrongSecretAndTamper(t *testing.T) {
	ts, body := "123", []byte("payload")
	sig := Sign(ts, body, "right")
	if Verify(ts, body, "wrong", sig) {
		t.Fatal("verified under wrong secret")
	}
	if Verify(ts, []byte("payloaX"), "right", sig) {
		t.Fatal("verified tampered body")
	}
}

// ---- tokens -----------------------------------------------------------------

func TestHashTokenStableAndDistinct(t *testing.T) {
	tok := GenerateToken()
	if HashToken(tok) != HashToken(tok) {
		t.Fatal("hash not stable")
	}
	if HashToken(tok) == HashToken(GenerateToken()) {
		t.Fatal("distinct tokens hash equal")
	}
	if HashToken(tok) == tok {
		t.Fatal("hash equals plaintext (token stored in clear?)")
	}
}

// ---- registry ---------------------------------------------------------------

func newApp(t *testing.T, r Registry, owner string, products, scopes []string) *Created {
	t.Helper()
	c, err := r.Create(CreateParams{Name: "t", OwnerID: owner, Products: products, Scopes: scopes, IncomingEnabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return c
}

func TestCreateAndTokenHashLookup(t *testing.T) {
	r := NewMemoryRegistry()
	c := newApp(t, r, "alice", []string{ProductOffice}, []string{ScopeAppsWrite})
	if c.Token == "" || c.SigningSecret == "" {
		t.Fatal("missing one-time secrets")
	}
	got, err := r.GetByTokenHash(HashToken(c.Token))
	if err != nil || got.ID != c.App.ID {
		t.Fatalf("token lookup failed: %v", err)
	}
	if _, err := r.GetByTokenHash(HashToken("vat_nope")); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for unknown token, got %v", err)
	}
}

func TestCreateRejectsUnknownScopeAndProduct(t *testing.T) {
	r := NewMemoryRegistry()
	if _, err := r.Create(CreateParams{Name: "x", Products: []string{ProductOffice}, Scopes: []string{"bogus:scope"}}); err == nil {
		t.Fatal("expected unknown-scope error")
	}
	if _, err := r.Create(CreateParams{Name: "x", Products: []string{"fax"}}); err == nil {
		t.Fatal("expected unknown-product error")
	}
}

func TestCustomScopeSet(t *testing.T) {
	r := NewMemoryRegistry(WithScopeSet(NewScopeSet("board:draw")))
	if _, err := r.Create(CreateParams{Name: "m", Products: []string{ProductBoard}, Scopes: []string{"board:draw"}}); err != nil {
		t.Fatalf("custom scope rejected: %v", err)
	}
	if _, err := r.Create(CreateParams{Name: "m", Products: []string{ProductBoard}, Scopes: []string{ScopeAppsWrite}}); err == nil {
		t.Fatal("default scope should be rejected by custom set")
	}
}

func TestListOwnerScoping(t *testing.T) {
	r := NewMemoryRegistry()
	newApp(t, r, "alice", []string{ProductOffice}, nil)
	newApp(t, r, "bob", []string{ProductOffice}, nil)
	mine, _ := r.List("alice", false)
	if len(mine) != 1 || mine[0].OwnerID != "alice" {
		t.Fatalf("owner scoping wrong: %+v", mine)
	}
	all, _ := r.List("alice", true)
	if len(all) != 2 {
		t.Fatalf("admin should see all, got %d", len(all))
	}
}

func TestRotateTokenInvalidatesOld(t *testing.T) {
	r := NewMemoryRegistry()
	c := newApp(t, r, "a", []string{ProductOffice}, nil)
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
}

func TestUpdateProductsAndScopesAndDelete(t *testing.T) {
	r := NewMemoryRegistry()
	c := newApp(t, r, "a", []string{ProductOffice}, []string{ScopeAppsWrite})
	prods := []string{ProductBoard, ProductFiles}
	upd, err := r.Update(c.App.ID, UpdateParams{Products: &prods})
	if err != nil {
		t.Fatal(err)
	}
	if upd.TargetsProduct(ProductOffice) || !upd.TargetsProduct(ProductBoard) {
		t.Fatalf("products not updated: %v", upd.Products)
	}
	if err := r.Delete(c.App.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(c.App.ID); err != ErrNotFound {
		t.Fatal("double delete should be ErrNotFound")
	}
}

func TestResolveSlashCommandProductScoped(t *testing.T) {
	r := NewMemoryRegistry()
	r.Create(CreateParams{Name: "deployer", Products: []string{ProductOffice}, SlashCommands: []SlashCommand{{Name: "deploy"}}})
	if _, _, ok := r.ResolveSlashCommand(ProductOffice, "/deploy"); !ok {
		t.Fatal("should resolve deploy for office")
	}
	if _, _, ok := r.ResolveSlashCommand(ProductBoard, "deploy"); ok {
		t.Fatal("must not resolve for a product the app does not target")
	}
	if cmds := r.AllSlashCommands(ProductOffice); len(cmds) != 1 || cmds[0].Name != "deploy" {
		t.Fatalf("catalog wrong: %+v", cmds)
	}
}

func TestSQLitePersistsAcrossReopen(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "apps.db")
	enc := testEncryptor(t)
	r1, err := NewStandaloneRegistry(dsn, WithSigningSecretEncryptor(enc))
	if err != nil {
		t.Fatal(err)
	}
	c := newApp(t, r1, "a", []string{ProductOffice}, []string{ScopeAppsRead})
	r1.Close()

	r2, err := NewStandaloneRegistry(dsn, WithSigningSecretEncryptor(enc))
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	got, err := r2.Get(c.App.ID)
	if err != nil {
		t.Fatalf("app not persisted: %v", err)
	}
	if !got.TargetsProduct(ProductOffice) || !got.HasScope(ScopeAppsRead) || !got.Incoming.Enabled {
		t.Fatalf("fields not persisted: %+v", got)
	}
	if _, err := r2.GetByTokenHash(HashToken(c.Token)); err != nil {
		t.Fatal("token hash not persisted")
	}
}

// ---- dispatcher -------------------------------------------------------------

func TestParseSlash(t *testing.T) {
	cases := []struct {
		in        string
		name, arg string
		ok        bool
	}{
		{"/deploy now", "deploy", "now", true},
		{"/deploy", "deploy", "", true},
		{"hello", "", "", false},
		{"/", "", "", false},
	}
	for _, c := range cases {
		n, a, ok := ParseSlash(c.in)
		if ok != c.ok || n != c.name || a != c.arg {
			t.Errorf("ParseSlash(%q)=%q,%q,%v want %q,%q,%v", c.in, n, a, ok, c.name, c.arg, c.ok)
		}
	}
}

func TestMaybeHandleSlashProductScoped(t *testing.T) {
	r := NewMemoryRegistry()
	r.Create(CreateParams{Name: "d", Products: []string{ProductOffice}, SlashCommands: []SlashCommand{{Name: "deploy"}}})
	d := NewDispatcher(r, ProductOffice)
	if !d.MaybeHandleSlash("general", "u1", "/deploy now") {
		t.Fatal("registered slash not intercepted")
	}
	if d.MaybeHandleSlash("general", "u1", "/unknown") {
		t.Fatal("unknown slash should not be intercepted")
	}
	// Same command, wrong product mount → not intercepted.
	dm := NewDispatcher(r, ProductBoard)
	if dm.MaybeHandleSlash("inbox", "u1", "/deploy") {
		t.Fatal("slash leaked across product")
	}
}

func TestSSEFanoutAndSubscription(t *testing.T) {
	r := NewMemoryRegistry()
	// Subscribes only to app_mention.
	c := newApp(t, r, "a", []string{ProductOffice}, nil)
	r.Update(c.App.ID, UpdateParams{Events: ptr([]string{EventAppMention})})
	d := NewDispatcher(r, ProductOffice)

	ch, unsub := d.Subscribe(c.App.ID)
	defer unsub()

	// message.created is not subscribed → no delivery.
	d.Emit(EventMessageCreated, map[string]any{"x": 1}, nil)
	select {
	case <-ch:
		t.Fatal("delivered an unsubscribed event type")
	default:
	}
	// app_mention is subscribed → delivered.
	d.Emit(EventAppMention, map[string]any{"target": "general"}, nil)
	select {
	case <-ch:
	default:
		t.Fatal("subscribed event not delivered to SSE")
	}
}

func TestEmitProductTargetingAndRecipients(t *testing.T) {
	r := NewMemoryRegistry()
	talkApp := newApp(t, r, "a", []string{ProductOffice}, nil)
	mailApp := newApp(t, r, "a", []string{ProductBoard}, nil)
	d := NewDispatcher(r, ProductOffice)

	tch, tu := d.Subscribe(talkApp.App.ID)
	defer tu()
	mch, mu := d.Subscribe(mailApp.App.ID)
	defer mu()

	// recipients predicate restricts to a specific app.
	d.Emit(EventMessageCreated, map[string]any{}, func(a *App) bool { return a.ID == talkApp.App.ID })
	select {
	case <-tch:
	default:
		t.Fatal("office app should have received")
	}
	select {
	case <-mch:
		t.Fatal("board app must not receive an office-product event")
	default:
	}
}

func ptr[T any](v T) *T { return &v }
