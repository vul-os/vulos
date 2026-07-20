package integrations

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

// TestMicrosoftConfiguredEnv verifies the env gate.
func TestMicrosoftConfiguredEnv(t *testing.T) {
	t.Setenv("MICROSOFT_OAUTH_CLIENT_ID", "")
	if MicrosoftConfigured() {
		t.Fatal("expected not configured when client id unset")
	}
	t.Setenv("MICROSOFT_OAUTH_CLIENT_ID", "test-client")
	if !MicrosoftConfigured() {
		t.Fatal("expected configured when client id set")
	}
}

// TestMicrosoftAuthCodeURL checks the consent URL is built against the v2.0
// "common" endpoint and requests offline_access (refresh token) plus the three
// importer Graph scopes, with prompt=consent and the registered redirect URI.
func TestMicrosoftAuthCodeURL(t *testing.T) {
	t.Setenv("MICROSOFT_OAUTH_CLIENT_ID", "test-client")
	t.Setenv("MICROSOFT_OAUTH_CLIENT_SECRET", "test-secret")
	t.Setenv("OAUTH_REDIRECT_BASE", "https://cp.example.com")

	raw, err := MicrosoftExchanger{}.AuthCodeURL("state-xyz")
	if err != nil {
		t.Fatalf("AuthCodeURL: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if u.Host != "login.microsoftonline.com" {
		t.Fatalf("unexpected host: %s", u.Host)
	}
	if !strings.Contains(u.Path, "/common/oauth2/v2.0/authorize") {
		t.Fatalf("unexpected path: %s", u.Path)
	}
	q := u.Query()
	if q.Get("state") != "state-xyz" {
		t.Fatalf("state not propagated: %q", q.Get("state"))
	}
	if q.Get("prompt") != "consent" {
		t.Fatalf("prompt=consent required for refresh token, got %q", q.Get("prompt"))
	}
	if q.Get("redirect_uri") != "https://cp.example.com/api/integrations/microsoft/callback" {
		t.Fatalf("unexpected redirect_uri: %q", q.Get("redirect_uri"))
	}
	scope := q.Get("scope")
	for _, want := range []string{"offline_access", "openid", ScopeMSFilesRead, ScopeMSContactsRead, ScopeMSCalendarRead} {
		if !strings.Contains(scope, want) {
			t.Fatalf("scope %q missing %q", scope, want)
		}
	}
}

// TestMicrosoftAuthCodeURLNotConfigured returns ErrOAuthNotConfigured when env
// is absent.
func TestMicrosoftAuthCodeURLNotConfigured(t *testing.T) {
	t.Setenv("MICROSOFT_OAUTH_CLIENT_ID", "")
	if _, err := (MicrosoftExchanger{}).AuthCodeURL("s"); err != ErrOAuthNotConfigured {
		t.Fatalf("want ErrOAuthNotConfigured, got %v", err)
	}
}

// TestMicrosoftScopeHelpers verifies the granted-scope predicates accept both the
// resource-qualified and short forms Microsoft may echo back.
func TestMicrosoftScopeHelpers(t *testing.T) {
	full := strings.Join([]string{ScopeMSFilesRead, ScopeMSContactsRead, ScopeMSCalendarRead}, " ")
	if !HasMicrosoftFilesRead(full) || !HasMicrosoftContactsRead(full) || !HasMicrosoftCalendarRead(full) {
		t.Fatalf("qualified scopes not recognised: %q", full)
	}
	short := "Files.Read Contacts.Read Calendars.Read"
	if !HasMicrosoftFilesRead(short) || !HasMicrosoftContactsRead(short) || !HasMicrosoftCalendarRead(short) {
		t.Fatalf("short scopes not recognised: %q", short)
	}
	if HasMicrosoftFilesRead("openid email") {
		t.Fatal("no files scope must not satisfy files-read")
	}
	// MicrosoftScopes must return a copy.
	s := MicrosoftScopes()
	s[0] = "mutated"
	if MicrosoftScopes()[0] == "mutated" {
		t.Fatal("MicrosoftScopes returned a shared slice, not a copy")
	}
}

// TestMicrosoftRevokeNoop documents that Microsoft has no refresh-token revoke
// endpoint — Revoke is a best-effort no-op and Disconnect still deletes locally.
func TestMicrosoftRevokeNoop(t *testing.T) {
	if err := (MicrosoftExchanger{}).Revoke(context.Background(), "some-refresh-token"); err != nil {
		t.Fatalf("Revoke should be a no-op, got %v", err)
	}
}
