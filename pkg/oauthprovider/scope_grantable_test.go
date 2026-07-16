package oauthprovider

import (
	"context"
	"errors"
	"testing"
)

// The mail.* scopes are UN-GRANTABLE while hosted mail is dormant
// (VULOS_HOSTED_MAIL unset/false). They stay KNOWN — this is a pure config gate.
// The suite defaults VULOS_HOSTED_MAIL=1 (see TestMain); every test here flips it
// OFF with t.Setenv to exercise the dormant path.

func TestIsGrantableScope_MailGatedWhenDormant(t *testing.T) {
	t.Setenv("VULOS_HOSTED_MAIL", "")

	// Core identity scopes are always grantable.
	for _, s := range []string{ScopeOpenID, ScopeEmail} {
		if !IsGrantableScope(s) {
			t.Fatalf("%q must be grantable even while hosted mail is dormant", s)
		}
	}
	// mail.* is known but NOT grantable while dormant.
	for _, s := range []string{ScopeMailRead, ScopeMailSend} {
		if !IsKnownScope(s) {
			t.Fatalf("%q must remain KNOWN (registered) while dormant", s)
		}
		if IsGrantableScope(s) {
			t.Fatalf("%q must be ungrantable while hosted mail is dormant", s)
		}
	}
	// Unknown scope is neither.
	if IsGrantableScope("totally.bogus") {
		t.Fatal("unknown scope must never be grantable")
	}
}

func TestGrantableScopes_OmitsMailWhenDormant(t *testing.T) {
	t.Setenv("VULOS_HOSTED_MAIL", "")
	got := GrantableScopes()
	for _, s := range got {
		if s == ScopeMailRead || s == ScopeMailSend {
			t.Fatalf("GrantableScopes leaked %q while dormant: %v", s, got)
		}
	}
	if len(got) != 2 { // openid, email
		t.Fatalf("expected exactly [openid email] while dormant, got %v", got)
	}

	// Flag flip → mail.* becomes grantable with NO other change.
	t.Setenv("VULOS_HOSTED_MAIL", "1")
	if len(GrantableScopes()) != 4 {
		t.Fatalf("expected all 4 scopes grantable once hosted mail is enabled, got %v", GrantableScopes())
	}
}

func TestFilterGrantable_DistinguishesUnknownFromGated(t *testing.T) {
	t.Setenv("VULOS_HOSTED_MAIL", "")

	kept, hadUngrantable := FilterGrantable([]string{ScopeOpenID, ScopeMailSend})
	if len(kept) != 1 || kept[0] != ScopeOpenID {
		t.Fatalf("expected only openid to survive, got %v", kept)
	}
	if !hadUngrantable {
		t.Fatal("a known-but-gated scope (mail.send) must set hadUngrantable")
	}

	// Unknown scopes are dropped WITHOUT hadUngrantable (that's FilterKnown's job).
	kept, hadUngrantable = FilterGrantable([]string{ScopeOpenID, "totally.bogus"})
	if hadUngrantable {
		t.Fatal("an unknown scope must NOT set hadUngrantable")
	}
	if len(kept) != 1 || kept[0] != ScopeOpenID {
		t.Fatalf("unknown scope should be dropped, got %v", kept)
	}
}

func TestRegisterClient_RejectsUngrantableMailScope(t *testing.T) {
	t.Setenv("VULOS_HOSTED_MAIL", "")
	svc := newTestService(t)
	ctx := context.Background()

	_, _, err := svc.RegisterClient(ctx, "user-1", "MailApp",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID, ScopeMailRead}, false)
	if !errors.Is(err, ErrScopeNotGrantable) {
		t.Fatalf("registering for mail.read while dormant must fail with ErrScopeNotGrantable, got %v", err)
	}

	// Same registration succeeds once hosted mail is enabled — flag flip only.
	t.Setenv("VULOS_HOSTED_MAIL", "1")
	if _, _, err := svc.RegisterClient(ctx, "user-1", "MailApp",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID, ScopeMailRead}, false); err != nil {
		t.Fatalf("registration should succeed once hosted mail is enabled: %v", err)
	}
}

func TestAuthorize_RejectsUngrantableMailScope(t *testing.T) {
	// Register the client WHILE hosted mail is enabled so its stored scope list
	// legitimately contains mail.read (proving the gate is at request time, not
	// merely registration time, and that no schema change is needed later).
	svc := newTestService(t) // TestMain default: VULOS_HOSTED_MAIL=1
	ctx := context.Background()
	_, challenge := pkcePair()
	c, _, err := svc.RegisterClient(ctx, "user-1", "App",
		[]string{"https://app.example.com/cb"}, []string{ScopeOpenID, ScopeEmail, ScopeMailRead}, false)
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}

	req := AuthorizeRequest{
		ResponseType: "code", ClientID: c.ClientID, RedirectURI: "https://app.example.com/cb",
		Scope: "openid mail.read", State: "st", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	}

	// Enabled: the request validates.
	if _, aerr := svc.ValidateAuthorize(ctx, req); aerr != nil {
		t.Fatalf("mail.read authorize should pass while hosted mail is enabled: %v", aerr)
	}

	// Dormant: the SAME request (same registered client) is rejected as
	// invalid_scope, redirectable, without leaking the client's scope list.
	t.Setenv("VULOS_HOSTED_MAIL", "")
	_, aerr := svc.ValidateAuthorize(ctx, req)
	if aerr == nil {
		t.Fatal("mail.read authorize must be rejected while hosted mail is dormant")
	}
	if aerr.Code != "invalid_scope" {
		t.Fatalf("expected invalid_scope, got %q", aerr.Code)
	}
	if !aerr.RedirectOK {
		t.Fatal("a post-redirect-validated scope error should be redirectable")
	}

	// A request WITHOUT mail scopes still works while dormant.
	req.Scope = "openid email"
	if _, aerr := svc.ValidateAuthorize(ctx, req); aerr != nil {
		t.Fatalf("openid+email authorize should still work while dormant: %v", aerr)
	}
}
