package oauthclient

// providers_multi_test.go — coverage for the non-OIDC providers (GitHub, Discord)
// whose identity + verified email are fetched from a REST API rather than an
// id_token, plus the env-gated registry ordering for all four providers.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// githubServer stands in for GitHub's token + /user + /user/emails endpoints.
func githubServer(t *testing.T, id int64, primaryVerified string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "gho_test", "token_type": "bearer"})
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer gho_test" {
			t.Errorf("/user Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "login": "octocat"})
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"email": "secondary@example.com", "primary": false, "verified": true},
			{"email": primaryVerified, "primary": true, "verified": true},
			{"email": "unverified@example.com", "primary": false, "verified": false},
		})
	})
	return httptest.NewServer(mux)
}

func TestExchange_GitHub_ResolvesPrimaryVerifiedEmail(t *testing.T) {
	srv := githubServer(t, 42, "octo@example.com")
	defer srv.Close()

	reg := &Registry{providers: map[string]Provider{}, hc: srv.Client()}
	p := Provider{
		ID: "github", Kind: KindGitHub, ClientID: "cid", ClientSecret: "sec",
		TokenURL:    srv.URL + "/login/oauth/access_token",
		UserInfoURL: srv.URL + "/user",
		EmailsURL:   srv.URL + "/user/emails",
	}
	id, err := reg.Exchange(context.Background(), p, "https://vulos.org/cb", "code", "verifier")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.Provider != "github" || id.Subject != "42" {
		t.Errorf("provider/subject = %q/%q, want github/42", id.Provider, id.Subject)
	}
	if id.Email != "octo@example.com" || !id.EmailVerified {
		t.Errorf("email = %q verified=%v, want octo@example.com verified=true", id.Email, id.EmailVerified)
	}
}

func TestExchange_GitHub_NoVerifiedEmailYieldsEmpty(t *testing.T) {
	// A GitHub account with no verified email → empty email so the route layer
	// forces the user to type one (mandatory-email rule).
	srv := githubServer(t, 7, "")
	// Override /user/emails to return only unverified addresses.
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "gho_test"})
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "login": "nomail"})
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{"email": "x@example.com", "primary": true, "verified": false}})
	})
	srv.Close()
	srv2 := httptest.NewServer(mux)
	defer srv2.Close()

	reg := &Registry{providers: map[string]Provider{}, hc: srv2.Client()}
	p := Provider{
		ID: "github", Kind: KindGitHub, ClientID: "cid", ClientSecret: "sec",
		TokenURL:    srv2.URL + "/login/oauth/access_token",
		UserInfoURL: srv2.URL + "/user",
		EmailsURL:   srv2.URL + "/user/emails",
	}
	id, err := reg.Exchange(context.Background(), p, "https://vulos.org/cb", "code", "verifier")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.Email != "" || id.EmailVerified {
		t.Errorf("email = %q verified=%v, want empty/false", id.Email, id.EmailVerified)
	}
}

func TestExchange_Discord_ResolvesEmail(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "disc_test", "token_type": "Bearer"})
	})
	mux.HandleFunc("/users/@me", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer disc_test" {
			t.Errorf("/users/@me Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "9988", "email": "gamer@example.com", "verified": true})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	reg := &Registry{providers: map[string]Provider{}, hc: srv.Client()}
	p := Provider{
		ID: "discord", Kind: KindDiscord, ClientID: "cid", ClientSecret: "sec",
		TokenURL:    srv.URL + "/oauth2/token",
		UserInfoURL: srv.URL + "/users/@me",
	}
	id, err := reg.Exchange(context.Background(), p, "https://vulos.org/cb", "code", "verifier")
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if id.Subject != "9988" || id.Email != "gamer@example.com" || !id.EmailVerified {
		t.Errorf("identity = %+v, want subject 9988 / gamer@example.com / verified", id)
	}
}

func TestNewRegistryFromEnv_GatesAndOrdersAllFour(t *testing.T) {
	t.Setenv("GOOGLE_OAUTH_CLIENT_ID", "g")
	t.Setenv("GOOGLE_OAUTH_CLIENT_SECRET", "g")
	t.Setenv("MS_OAUTH_CLIENT_ID", "m")
	t.Setenv("MS_OAUTH_CLIENT_SECRET", "m")
	t.Setenv("GITHUB_OAUTH_CLIENT_ID", "gh")
	t.Setenv("GITHUB_OAUTH_CLIENT_SECRET", "gh")
	// Discord intentionally left UNconfigured — it must NOT appear.
	t.Setenv("DISCORD_OAUTH_CLIENT_ID", "")
	t.Setenv("DISCORD_OAUTH_CLIENT_SECRET", "")

	reg := NewRegistryFromEnv()
	got := reg.Configured()
	want := []string{"google", "microsoft", "github"}
	if len(got) != len(want) {
		t.Fatalf("configured = %v, want %v", got, want)
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("configured[%d] = %q, want %q", i, got[i].ID, id)
		}
	}
	if _, ok := reg.Get("discord"); ok {
		t.Error("discord should be absent (unconfigured)")
	}
	// GitHub authorize URL must NOT carry the OIDC-only access_type/prompt params.
	gh, _ := reg.Get("github")
	if u := gh.AuthCodeURL("https://vulos.org/cb", "s", "c"); contains(u, "access_type") || contains(u, "prompt=") {
		t.Errorf("github authorize URL carried OIDC-only params: %s", u)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && indexOf(s, sub) >= 0 }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
