package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newBareGateway builds a Gateway with just the fields the injection logic
// needs (no network / auth wiring).
func newBareGateway() *Gateway {
	return &Gateway{
		appSecrets:      make(map[string]string),
		appHits:         make(map[string]*rateBucket),
		integrationApps: make(map[string]map[string]bool),
	}
}

func TestInjectIntegrationToken_PermittedApp(t *testing.T) {
	g := newBareGateway()
	// H3 fix: token func now receives userID so tokens are per-user.
	g.SetIntegrationTokenFunc(func(_ context.Context, provider, _ string) (string, error) {
		if provider == "google" {
			return "ya29.fake-access-token", nil
		}
		return "", errors.New("unknown provider")
	})
	g.AllowIntegration("browser", "google")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// H3 fix: injectIntegrationTokens now takes userID as second arg.
	g.injectIntegrationTokens(context.Background(), r, "user-alice", "browser")

	if got := r.Header.Get("X-Vulos-Integration-Google"); got != "ya29.fake-access-token" {
		t.Fatalf("expected injected token, got %q", got)
	}
}

func TestInjectIntegrationToken_UnpermittedApp(t *testing.T) {
	g := newBareGateway()
	g.SetIntegrationTokenFunc(func(context.Context, string, string) (string, error) {
		return "should-not-be-used", nil
	})
	// "notes" was never granted google.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	g.injectIntegrationTokens(context.Background(), r, "user-alice", "notes")

	if got := r.Header.Get("X-Vulos-Integration-Google"); got != "" {
		t.Fatalf("unpermitted app must not receive a token, got %q", got)
	}
}

func TestInjectIntegrationToken_StripsSpoofedHeader(t *testing.T) {
	g := newBareGateway()
	g.SetIntegrationTokenFunc(func(context.Context, string, string) (string, error) {
		// Mint fails → no legitimate token is set.
		return "", errors.New("not connected")
	})
	g.AllowIntegration("browser", "google")

	// A malicious client pre-sets the header hoping it is forwarded to the app.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Vulos-Integration-Google", "attacker-controlled")
	g.injectIntegrationTokens(context.Background(), r, "user-alice", "browser")

	if got := r.Header.Get("X-Vulos-Integration-Google"); got != "" {
		t.Fatalf("spoofed header must be stripped when no real token, got %q", got)
	}
}

func TestInjectIntegrationToken_OverridesSpoofedHeader(t *testing.T) {
	g := newBareGateway()
	g.SetIntegrationTokenFunc(func(context.Context, string, string) (string, error) {
		return "real-token", nil
	})
	g.AllowIntegration("browser", "google")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Vulos-Integration-Google", "attacker-controlled")
	g.injectIntegrationTokens(context.Background(), r, "user-alice", "browser")

	if got := r.Header.Get("X-Vulos-Integration-Google"); got != "real-token" {
		t.Fatalf("expected real token to override spoof, got %q", got)
	}
}

func TestInjectIntegrationToken_UserIsolation(t *testing.T) {
	// H3 regression test: the userID must be forwarded to the token func so
	// different users get different tokens.
	g := newBareGateway()
	tokenByUser := map[string]string{
		"alice": "token-for-alice",
		"bob":   "token-for-bob",
	}
	g.SetIntegrationTokenFunc(func(_ context.Context, _, userID string) (string, error) {
		if tok, ok := tokenByUser[userID]; ok {
			return tok, nil
		}
		return "", errors.New("unknown user")
	})
	g.AllowIntegration("browser", "google")

	for user, want := range tokenByUser {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		g.injectIntegrationTokens(context.Background(), r, user, "browser")
		if got := r.Header.Get("X-Vulos-Integration-Google"); got != want {
			t.Fatalf("user=%s: expected token %q, got %q", user, want, got)
		}
	}
}

func TestInjectIntegrationToken_NoFuncIsNoop(t *testing.T) {
	g := newBareGateway()
	g.AllowIntegration("browser", "google")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// No token func set — must not panic and must not add a header.
	g.injectIntegrationTokens(context.Background(), r, "user-alice", "browser")
	if r.Header.Get("X-Vulos-Integration-Google") != "" {
		t.Fatal("no token func should produce no header")
	}
}

func TestIntegrationHeaderSuffix(t *testing.T) {
	cases := map[string]string{"google": "Google", "dropbox": "Dropbox", "": ""}
	for in, want := range cases {
		if got := integrationHeaderSuffix(in); got != want {
			t.Fatalf("integrationHeaderSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}
