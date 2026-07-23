package integrations

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ---------------------------------------------------------------------------
// crypto
// ---------------------------------------------------------------------------

func testKEK() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	kek := testKEK()
	for _, plain := range []string{"", "x", "a-refresh-token-value", "unicode: ☃ 🔐"} {
		enc, err := encrypt(plain, kek)
		if err != nil {
			t.Fatalf("encrypt(%q): %v", plain, err)
		}
		if plain != "" && enc == plain {
			t.Fatalf("ciphertext equals plaintext for %q", plain)
		}
		got, err := decrypt(enc, kek)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		if got != plain {
			t.Fatalf("round-trip: want %q got %q", plain, got)
		}
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	enc, err := encrypt("secret", testKEK())
	if err != nil {
		t.Fatal(err)
	}
	wrong := make([]byte, 32) // all zeros
	if _, err := decrypt(enc, wrong); err == nil {
		t.Fatal("expected decrypt with wrong key to fail")
	}
}

func TestLoadKEK(t *testing.T) {
	t.Run("missing+notdev errors", func(t *testing.T) {
		t.Setenv("INTEGRATIONS_KEK", "")
		t.Setenv("VULOS_DEV", "")
		if _, err := LoadKEK(); err == nil {
			t.Fatal("expected error when KEK unset and not dev")
		}
	})
	t.Run("dev fallback with explicit non-prod env", func(t *testing.T) {
		// WAVE37: dev fallback requires an EXPLICIT non-prod env, not merely an
		// unset one.
		t.Setenv("INTEGRATIONS_KEK", "")
		t.Setenv("VULOS_DEV", "true")
		t.Setenv("VULOS_ENV", "local")
		k, err := LoadKEK()
		if err != nil || len(k) != 32 {
			t.Fatalf("dev fallback: %v len=%d", err, len(k))
		}
	})
	t.Run("VULOS_DEV set but VULOS_ENV unset fails closed", func(t *testing.T) {
		// WAVE37 fail-safe: an unconfigured box (unset env) resolves to prod, so a
		// stray VULOS_DEV must NOT activate the zero KEK.
		t.Setenv("INTEGRATIONS_KEK", "")
		t.Setenv("VULOS_DEV", "true")
		t.Setenv("VULOS_ENV", "")
		if _, err := LoadKEK(); err == nil {
			t.Fatal("expected fail-closed when VULOS_ENV is unset even with VULOS_DEV=true")
		}
	})
	t.Run("VULOS_DEV set but VULOS_ENV=prod fails closed", func(t *testing.T) {
		// The landmine: a stray VULOS_DEV on a prod box must NOT activate the zero KEK.
		t.Setenv("INTEGRATIONS_KEK", "")
		t.Setenv("VULOS_DEV", "true")
		t.Setenv("VULOS_ENV", "prod")
		if _, err := LoadKEK(); err == nil {
			t.Fatal("expected fail-closed when VULOS_ENV=prod even with VULOS_DEV=true")
		}
	})
	t.Run("bad base64", func(t *testing.T) {
		t.Setenv("INTEGRATIONS_KEK", "!!!not-base64!!!")
		if _, err := LoadKEK(); err == nil {
			t.Fatal("expected base64 error")
		}
	})
	t.Run("wrong length", func(t *testing.T) {
		t.Setenv("INTEGRATIONS_KEK", "dG9vc2hvcnQ=") // "tooshort"
		if _, err := LoadKEK(); err == nil {
			t.Fatal("expected length error")
		}
	})
}

// ---------------------------------------------------------------------------
// scopes
// ---------------------------------------------------------------------------

func TestGoogleScopesIncludeDriveReadonly(t *testing.T) {
	scopes := GoogleScopes()
	if !HasScope(joinScopes(scopes), ScopeDriveReadonly) {
		t.Fatalf("requested scopes missing %s: %v", ScopeDriveReadonly, scopes)
	}
	// GoogleScopes must be a copy — mutating it must not affect the package set.
	scopes[0] = "mutated"
	if GoogleScopes()[0] == "mutated" {
		t.Fatal("GoogleScopes returned a shared slice, not a copy")
	}
}

func joinScopes(scopes []string) string {
	out := ""
	for i, s := range scopes {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}

func TestHasScope(t *testing.T) {
	granted := "openid email " + ScopeDriveReadonly
	if !HasScope(granted, ScopeDriveReadonly) {
		t.Fatal("expected drive.readonly to be present")
	}
	if HasScope(granted, ScopeDrive) {
		t.Fatal("did not expect full drive to be present")
	}
	if HasScope("", ScopeDriveReadonly) {
		t.Fatal("empty granted must not match")
	}
	// Substring false positive guard: a scope that is a prefix of another.
	if HasScope("https://www.googleapis.com/auth/drive.readonly.extra", ScopeDriveReadonly) {
		t.Fatal("prefix must not be treated as a match")
	}
}

func TestHasDriveReadAccess(t *testing.T) {
	// Read-only scope satisfies it.
	if !HasDriveReadAccess("openid " + ScopeDriveReadonly) {
		t.Fatal("drive.readonly must satisfy drive-read access")
	}
	// Full drive (superset) also satisfies it — pre-readonly connections.
	if !HasDriveReadAccess("openid " + ScopeDrive) {
		t.Fatal("full drive must satisfy drive-read access")
	}
	// Neither → false.
	if HasDriveReadAccess("openid email https://www.googleapis.com/auth/calendar") {
		t.Fatal("no drive scope must not satisfy drive-read access")
	}
}

// ---------------------------------------------------------------------------
// CSRF state
// ---------------------------------------------------------------------------

func TestStateRoundTrip(t *testing.T) {
	secret := []byte("state-secret")
	now := time.Unix(1_700_000_000, 0)

	w := httptest.NewRecorder()
	state, err := GenerateState(w, secret, now)
	if err != nil {
		t.Fatalf("GenerateState: %v", err)
	}
	cookie := w.Result().Cookies()[0]

	r := httptest.NewRequest(http.MethodGet, "/api/integrations/google/callback", nil)
	r.AddCookie(cookie)
	if err := VerifyState(r, state, secret, now.Add(30*time.Second)); err != nil {
		t.Fatalf("VerifyState: %v", err)
	}
}

func TestStateRejections(t *testing.T) {
	secret := []byte("state-secret")
	now := time.Unix(1_700_000_000, 0)

	mk := func() (*http.Cookie, string) {
		w := httptest.NewRecorder()
		state, _ := GenerateState(w, secret, now)
		return w.Result().Cookies()[0], state
	}

	t.Run("no cookie", func(t *testing.T) {
		_, state := mk()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		if err := VerifyState(r, state, secret, now); err != ErrStateMismatch {
			t.Fatalf("want ErrStateMismatch, got %v", err)
		}
	})
	t.Run("tampered returned state", func(t *testing.T) {
		cookie, state := mk()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(cookie)
		if err := VerifyState(r, state+"x", secret, now); err != ErrStateMismatch {
			t.Fatalf("want ErrStateMismatch, got %v", err)
		}
	})
	t.Run("wrong secret", func(t *testing.T) {
		cookie, state := mk()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(cookie)
		if err := VerifyState(r, state, []byte("other-secret"), now); err != ErrStateMismatch {
			t.Fatalf("want ErrStateMismatch, got %v", err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		cookie, state := mk()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(cookie)
		late := now.Add(time.Duration(stateMaxAgeSec+5) * time.Second)
		if err := VerifyState(r, state, secret, late); err != ErrStateMismatch {
			t.Fatalf("want ErrStateMismatch for expired, got %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Store (both implementations)
// ---------------------------------------------------------------------------

func eachStore(t *testing.T, fn func(t *testing.T, s Store)) {
	t.Helper()
	t.Run("mem", func(t *testing.T) {
		fn(t, NewMemStore())
	})
	t.Run("sql", func(t *testing.T) {
		db, err := cpdb.OpenSQLiteDSN(":memory:")
		if err != nil {
			t.Fatalf("cpdb open: %v", err)
		}
		s, err := Open(db)
		if err != nil {
			db.Close()
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		fn(t, s)
	})
}

func TestStoreCRUD(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		ctx := context.Background()

		// Get on empty → ErrNotConnected.
		if _, err := s.Get(ctx, "acct1", "google"); err != ErrNotConnected {
			t.Fatalf("want ErrNotConnected, got %v", err)
		}

		c := Connection{
			AccountID:       "acct1",
			Provider:        "google",
			RefreshTokenEnc: "rt-enc",
			AccessTokenEnc:  "at-enc",
			AccessExpiry:    time.Unix(1_700_000_900, 0).UTC(),
			Scopes:          "openid email",
			AccountEmail:    "user@gmail.com",
			AccountSub:      "sub-123",
		}
		if err := s.Upsert(ctx, c); err != nil {
			t.Fatalf("Upsert: %v", err)
		}

		got, err := s.Get(ctx, "acct1", "google")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.RefreshTokenEnc != "rt-enc" || got.AccountEmail != "user@gmail.com" || got.Scopes != "openid email" {
			t.Fatalf("Get mismatch: %+v", got)
		}
		if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
			t.Fatal("timestamps not set")
		}
		createdAt := got.CreatedAt

		// SetAccessToken.
		newExpiry := time.Unix(1_700_009_999, 0).UTC()
		if err := s.SetAccessToken(ctx, "acct1", "google", "at-enc-2", newExpiry); err != nil {
			t.Fatalf("SetAccessToken: %v", err)
		}
		got, _ = s.Get(ctx, "acct1", "google")
		if got.AccessTokenEnc != "at-enc-2" || !got.AccessExpiry.Equal(newExpiry) {
			t.Fatalf("SetAccessToken not applied: %+v", got)
		}

		// SetRefreshToken.
		if err := s.SetRefreshToken(ctx, "acct1", "google", "rt-enc-2"); err != nil {
			t.Fatalf("SetRefreshToken: %v", err)
		}
		got, _ = s.Get(ctx, "acct1", "google")
		if got.RefreshTokenEnc != "rt-enc-2" {
			t.Fatalf("SetRefreshToken not applied: %q", got.RefreshTokenEnc)
		}

		// Re-Upsert preserves created_at.
		c.RefreshTokenEnc = "rt-enc-3"
		if err := s.Upsert(ctx, c); err != nil {
			t.Fatalf("re-Upsert: %v", err)
		}
		got, _ = s.Get(ctx, "acct1", "google")
		if !got.CreatedAt.Equal(createdAt) {
			t.Fatalf("created_at not preserved: was %v now %v", createdAt, got.CreatedAt)
		}

		// SetAccessToken on unknown → ErrNotConnected.
		if err := s.SetAccessToken(ctx, "nobody", "google", "x", newExpiry); err != ErrNotConnected {
			t.Fatalf("want ErrNotConnected, got %v", err)
		}

		// List.
		_ = s.Upsert(ctx, Connection{AccountID: "acct1", Provider: "other", RefreshTokenEnc: "z"})
		list, err := s.List(ctx, "acct1")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(list) != 2 {
			t.Fatalf("List want 2, got %d", len(list))
		}

		// Delete.
		if err := s.Delete(ctx, "acct1", "google"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := s.Get(ctx, "acct1", "google"); err != ErrNotConnected {
			t.Fatalf("after Delete want ErrNotConnected, got %v", err)
		}
		// Delete is idempotent.
		if err := s.Delete(ctx, "acct1", "google"); err != nil {
			t.Fatalf("idempotent Delete: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Service (with fake exchanger)
// ---------------------------------------------------------------------------

type fakeExchanger struct {
	exchangeTok   *Token
	exchangeErr   error
	refreshTok    *Token
	refreshErr    error
	refreshCalls  int
	revokeCalls   int
	revokedTokens []string
	revokeErr     error
}

func (f *fakeExchanger) AuthCodeURL(state string) (string, error) {
	return "https://accounts.google.test/o/oauth2/auth?state=" + state, nil
}
func (f *fakeExchanger) Exchange(_ context.Context, _ string) (*Token, error) {
	return f.exchangeTok, f.exchangeErr
}
func (f *fakeExchanger) Refresh(_ context.Context, _ string) (*Token, error) {
	f.refreshCalls++
	return f.refreshTok, f.refreshErr
}
func (f *fakeExchanger) Revoke(_ context.Context, refreshToken string) error {
	f.revokeCalls++
	f.revokedTokens = append(f.revokedTokens, refreshToken)
	return f.revokeErr
}

func newTestService(ex Exchanger) *Service {
	return NewService(NewMemStore(), ex, testKEK())
}

func TestServiceConnectStoresEncrypted(t *testing.T) {
	ex := &fakeExchanger{exchangeTok: &Token{
		AccessToken:  "access-1",
		RefreshToken: "refresh-1",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       "openid email https://www.googleapis.com/auth/gmail.modify",
		Email:        "u@gmail.com",
		Sub:          "sub-1",
	}}
	svc := newTestService(ex)
	ctx := context.Background()

	conn, err := svc.Connect(ctx, "acct1", ProviderGoogle, "auth-code")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// Stored token must be encrypted (not equal to plaintext).
	if conn.RefreshTokenEnc == "refresh-1" || conn.RefreshTokenEnc == "" {
		t.Fatalf("refresh token not encrypted at rest: %q", conn.RefreshTokenEnc)
	}
	if conn.AccountEmail != "u@gmail.com" {
		t.Fatalf("email not stored: %q", conn.AccountEmail)
	}
}

func TestServiceConnectNoRefreshToken(t *testing.T) {
	// Exchanger surfaces ErrNoRefreshToken; Connect must propagate it.
	ex := &fakeExchanger{exchangeErr: ErrNoRefreshToken}
	svc := newTestService(ex)
	if _, err := svc.Connect(context.Background(), "a", ProviderGoogle, "c"); err != ErrNoRefreshToken {
		t.Fatalf("want ErrNoRefreshToken, got %v", err)
	}
}

func TestServiceMintCachedThenRefresh(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	ex := &fakeExchanger{
		exchangeTok: &Token{
			AccessToken:  "access-fresh",
			RefreshToken: "refresh-1",
			Expiry:       now.Add(30 * time.Minute), // valid well past skew
			Scopes:       "openid email",
		},
		refreshTok: &Token{
			AccessToken: "access-refreshed",
			Expiry:      now.Add(2 * time.Hour),
			Scopes:      "openid email",
		},
	}
	svc := newTestService(ex)
	svc.now = func() time.Time { return now }
	ctx := context.Background()

	if _, err := svc.Connect(ctx, "acct1", ProviderGoogle, "code"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// 1) Cached access token is served without a refresh call.
	m, err := svc.MintAccessToken(ctx, "acct1", ProviderGoogle)
	if err != nil {
		t.Fatalf("Mint (cached): %v", err)
	}
	if m.AccessToken != "access-fresh" {
		t.Fatalf("want cached access-fresh, got %q", m.AccessToken)
	}
	if ex.refreshCalls != 0 {
		t.Fatalf("expected no refresh on cached path, got %d", ex.refreshCalls)
	}

	// 2) Advance past expiry → refresh path.
	svc.now = func() time.Time { return now.Add(time.Hour) }
	m, err = svc.MintAccessToken(ctx, "acct1", ProviderGoogle)
	if err != nil {
		t.Fatalf("Mint (refresh): %v", err)
	}
	if m.AccessToken != "access-refreshed" {
		t.Fatalf("want access-refreshed, got %q", m.AccessToken)
	}
	if ex.refreshCalls != 1 {
		t.Fatalf("expected 1 refresh, got %d", ex.refreshCalls)
	}
}

func TestServiceMintNotConnected(t *testing.T) {
	svc := newTestService(&fakeExchanger{})
	if _, err := svc.MintAccessToken(context.Background(), "ghost", ProviderGoogle); err != ErrNotConnected {
		t.Fatalf("want ErrNotConnected, got %v", err)
	}
}

func TestServiceMintRotatesRefreshToken(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	ex := &fakeExchanger{
		exchangeTok: &Token{AccessToken: "a", RefreshToken: "refresh-old", Expiry: now.Add(-time.Minute)},
		refreshTok:  &Token{AccessToken: "a2", RefreshToken: "refresh-new", Expiry: now.Add(time.Hour)},
	}
	svc := newTestService(ex)
	svc.now = func() time.Time { return now }
	ctx := context.Background()
	if _, err := svc.Connect(ctx, "acct1", ProviderGoogle, "c"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.MintAccessToken(ctx, "acct1", ProviderGoogle); err != nil {
		t.Fatal(err)
	}
	// The rotated refresh token must have been persisted (encrypted) and be
	// usable for a subsequent decrypt → not equal to the old ciphertext.
	conn, _ := svc.store.Get(ctx, "acct1", ProviderGoogle)
	got, err := decrypt(conn.RefreshTokenEnc, svc.kek)
	if err != nil {
		t.Fatalf("decrypt rotated: %v", err)
	}
	if got != "refresh-new" {
		t.Fatalf("rotation not persisted: %q", got)
	}
}

func TestServiceStatusAndDisconnect(t *testing.T) {
	ex := &fakeExchanger{exchangeTok: &Token{AccessToken: "a", RefreshToken: "r", Expiry: time.Now().Add(time.Hour)}}
	svc := newTestService(ex)
	ctx := context.Background()

	connected, _, err := svc.Status(ctx, "acct1", ProviderGoogle)
	if err != nil || connected {
		t.Fatalf("expected not connected, got connected=%v err=%v", connected, err)
	}
	if _, err := svc.Connect(ctx, "acct1", ProviderGoogle, "c"); err != nil {
		t.Fatal(err)
	}
	connected, _, _ = svc.Status(ctx, "acct1", ProviderGoogle)
	if !connected {
		t.Fatal("expected connected after Connect")
	}
	if err := svc.Disconnect(ctx, "acct1", ProviderGoogle); err != nil {
		t.Fatal(err)
	}
	// Disconnect must revoke the refresh token at the provider.
	if ex.revokeCalls != 1 || len(ex.revokedTokens) != 1 || ex.revokedTokens[0] != "r" {
		t.Fatalf("expected revoke(\"r\") once, got calls=%d tokens=%v", ex.revokeCalls, ex.revokedTokens)
	}
	connected, _, _ = svc.Status(ctx, "acct1", ProviderGoogle)
	if connected {
		t.Fatal("expected disconnected after Disconnect")
	}
}

func TestServiceDisconnectDeletesEvenIfRevokeFails(t *testing.T) {
	ex := &fakeExchanger{
		exchangeTok: &Token{AccessToken: "a", RefreshToken: "r", Expiry: time.Now().Add(time.Hour)},
		revokeErr:   errors.New("google 503"),
	}
	svc := newTestService(ex)
	ctx := context.Background()
	if _, err := svc.Connect(ctx, "acct1", ProviderGoogle, "c"); err != nil {
		t.Fatal(err)
	}
	// Revoke fails, but the connection must still be deleted.
	if err := svc.Disconnect(ctx, "acct1", ProviderGoogle); err == nil {
		t.Fatal("expected non-nil revoke error surfaced")
	}
	if connected, _, _ := svc.Status(ctx, "acct1", ProviderGoogle); connected {
		t.Fatal("connection must be deleted even when revoke fails")
	}
}

func TestServiceDisconnectIdempotent(t *testing.T) {
	svc := newTestService(&fakeExchanger{})
	if err := svc.Disconnect(context.Background(), "ghost", ProviderGoogle); err != nil {
		t.Fatalf("disconnect of absent connection should be nil, got %v", err)
	}
}
