package oauthprovider

import (
	"context"
	"testing"
)

// subViaFlow runs a full authorize→exchange for client c and returns the `sub`
// claim from the id_token plus the userinfo `sub`, which must match.
func subViaFlow(t *testing.T, svc *Service, c Client, secret, userID string) (idSub, uiSub string) {
	t.Helper()
	ctx := context.Background()
	verifier, challenge := pkcePair()
	v, aerr := svc.ValidateAuthorize(ctx, AuthorizeRequest{
		ResponseType: "code", ClientID: c.ClientID, RedirectURI: c.RedirectURIs[0],
		Scope: "openid", CodeChallenge: challenge, CodeChallengeMethod: "S256",
	})
	if aerr != nil {
		t.Fatalf("ValidateAuthorize: %v", aerr)
	}
	code, err := svc.IssueCode(ctx, v, userID)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	resp, err := svc.ExchangeCode(ctx, ExchangeCodeParams{
		Code: code, RedirectURI: c.RedirectURIs[0], ClientID: c.ClientID,
		ClientSecret: secret, CodeVerifier: verifier,
	})
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	claims, err := VerifyIDToken(resp.IDToken, &svc.SigningKey().Private.PublicKey)
	if err != nil {
		t.Fatalf("VerifyIDToken: %v", err)
	}
	sub, _ := claims["sub"].(string)
	ui, err := svc.UserInfoForToken(ctx, resp.AccessToken)
	if err != nil {
		t.Fatalf("UserInfoForToken: %v", err)
	}
	return sub, ui.Subject
}

// TestPublicSubjectIsRawIDUnchanged proves the DEFAULT (public) client still
// gets the raw user id as `sub` — the non-breaking guarantee for existing SSO.
func TestPublicSubjectIsRawIDUnchanged(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const userID = "user-raw-1"
	c, secret, err := svc.RegisterClient(ctx, "owner", "PublicApp",
		[]string{"https://pub.example.com/cb"}, []string{ScopeOpenID}, false)
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	if c.SubjectType != SubjectTypePublic {
		t.Fatalf("default subject_type = %q, want public", c.SubjectType)
	}
	idSub, uiSub := subViaFlow(t, svc, c, secret, userID)
	if idSub != userID {
		t.Fatalf("public id_token sub = %q, want raw id %q", idSub, userID)
	}
	if uiSub != userID {
		t.Fatalf("public userinfo sub = %q, want raw id %q", uiSub, userID)
	}
}

// TestPairwiseSubjectStableDistinctNonReversible proves a pairwise client gets:
//   - a sub that is NOT the raw id (no internal-id leak / cross-RP correlation);
//   - a STABLE sub across repeated sign-ins;
//   - a sub that matches between id_token and userinfo;
//   - a DIFFERENT sub from another pairwise client for the SAME user.
func TestPairwiseSubjectStableDistinctNonReversible(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const userID = "user-pw-1"

	pw1, sec1, err := svc.RegisterClientTyped(ctx, "owner", "PairApp1",
		[]string{"https://pw1.example.com/cb"}, []string{ScopeOpenID}, false, SubjectTypePairwise, "")
	if err != nil {
		t.Fatalf("RegisterClientTyped pw1: %v", err)
	}
	if pw1.SubjectType != SubjectTypePairwise {
		t.Fatalf("pw1 subject_type = %q, want pairwise", pw1.SubjectType)
	}
	pw2, sec2, err := svc.RegisterClientTyped(ctx, "owner", "PairApp2",
		[]string{"https://pw2.example.com/cb"}, []string{ScopeOpenID}, false, SubjectTypePairwise, "")
	if err != nil {
		t.Fatalf("RegisterClientTyped pw2: %v", err)
	}

	id1a, ui1a := subViaFlow(t, svc, pw1, sec1, userID)
	id1b, _ := subViaFlow(t, svc, pw1, sec1, userID)
	id2, _ := subViaFlow(t, svc, pw2, sec2, userID)

	// Not the raw id.
	if id1a == userID {
		t.Fatalf("pairwise sub leaked the raw id: %q", id1a)
	}
	// Stable across sign-ins.
	if id1a != id1b {
		t.Fatalf("pairwise sub not stable: %q != %q", id1a, id1b)
	}
	// id_token sub == userinfo sub.
	if id1a != ui1a {
		t.Fatalf("pairwise id_token sub (%q) != userinfo sub (%q)", id1a, ui1a)
	}
	// Different pairwise clients ⇒ different subs for the same user.
	if id1a == id2 {
		t.Fatalf("two pairwise clients produced the same sub %q for user %q (correlation leak)", id1a, userID)
	}
	// Non-reversible base64url digest (43 chars for sha256), not the plaintext id.
	if len(id1a) != 43 {
		t.Fatalf("pairwise sub not a base64url sha256 digest: %q (len %d)", id1a, len(id1a))
	}
}

// TestPairwiseSharedSectorGroupsClients proves an explicit shared
// sector_identifier makes two pairwise clients see the SAME sub (client-family
// grouping) — the OIDC sector semantics — while still differing from the raw id.
func TestPairwiseSharedSectorGroupsClients(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const userID = "user-pw-2"
	const sector = "https://shared.example.com"

	a, secA, err := svc.RegisterClientTyped(ctx, "owner", "FamA",
		[]string{"https://a.example.com/cb"}, []string{ScopeOpenID}, false, SubjectTypePairwise, sector)
	if err != nil {
		t.Fatalf("register FamA: %v", err)
	}
	b, secB, err := svc.RegisterClientTyped(ctx, "owner", "FamB",
		[]string{"https://b.example.com/cb"}, []string{ScopeOpenID}, false, SubjectTypePairwise, sector)
	if err != nil {
		t.Fatalf("register FamB: %v", err)
	}
	subA, _ := subViaFlow(t, svc, a, secA, userID)
	subB, _ := subViaFlow(t, svc, b, secB, userID)
	if subA != subB {
		t.Fatalf("shared-sector pairwise clients got different subs: %q != %q", subA, subB)
	}
	if subA == userID {
		t.Fatalf("shared-sector pairwise sub leaked raw id")
	}
}

// TestRegisterRejectsUnknownSubjectType proves an invalid subject_type is
// rejected (closed set).
func TestRegisterRejectsUnknownSubjectType(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	if _, _, err := svc.RegisterClientTyped(ctx, "owner", "Bad",
		[]string{"https://bad.example.com/cb"}, []string{ScopeOpenID}, false, "sectorwise", ""); err == nil {
		t.Fatal("expected error for unknown subject_type, got nil")
	}
}
