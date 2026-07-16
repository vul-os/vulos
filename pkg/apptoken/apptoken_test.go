package apptoken

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var key = []byte("apptoken-test-key-0123456789abcdef")

func TestMintVerifyRoundTrip(t *testing.T) {
	tok, err := NewMinter(key, DefaultTTL).Mint("user-1", "office")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	c, err := Verify(key, tok, "office", time.Now())
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if c.Sub != "user-1" || c.Aud != "office" || c.Iss != Issuer {
		t.Fatalf("claims wrong: %+v", c)
	}
}

// The audience binding is the whole point: a token minted for one app must be
// worthless to another. Without this, stripping the session would just relocate
// the same over-broad credential.
func TestVerify_AudienceIsBinding(t *testing.T) {
	tok, err := NewMinter(key, DefaultTTL).Mint("user-1", "office")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := Verify(key, tok, "talk", time.Now()); !errors.Is(err, ErrAudience) {
		t.Fatalf("office token accepted for talk: err=%v", err)
	}
	// An empty expected audience is never a wildcard on this path.
	if _, err := Verify(key, tok, "", time.Now()); !errors.Is(err, ErrAudience) {
		t.Fatalf("empty audience must not act as a wildcard: err=%v", err)
	}
}

func TestVerify_RejectsForeignKeyAndTamper(t *testing.T) {
	tok, err := NewMinter(key, DefaultTTL).Mint("user-1", "office")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := Verify([]byte("some-other-key-aaaaaaaaaaaaaaaaaa"), tok, "office", time.Now()); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("token verified under a foreign key: err=%v", err)
	}
	// The attack that matters: rewrite the claims (escalate to another user, or
	// to another app's audience) while replaying the original MAC.
	_, mac, _ := strings.Cut(tok, ".")
	for _, swapped := range []Claims{
		{Sub: "victim", Aud: "office", Iss: Issuer, Iat: time.Now().Unix(), Exp: time.Now().Add(time.Minute).Unix()},
		{Sub: "user-1", Aud: "mail", Iss: Issuer, Iat: time.Now().Unix(), Exp: time.Now().Add(time.Minute).Unix()},
	} {
		payload, err := json.Marshal(swapped)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		forged := base64.RawURLEncoding.EncodeToString(payload) + "." + mac
		if _, err := Verify(key, forged, swapped.Aud, time.Now()); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("claims %+v rewritten under a replayed MAC verified: err=%v", swapped, err)
		}
	}
}

func TestVerify_Expiry(t *testing.T) {
	m := NewMinter(key, DefaultTTL)
	tok, err := m.MintAt("user-1", "office", time.Now().Add(-30*time.Minute))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := Verify(key, tok, "office", time.Now()); !errors.Is(err, ErrExpired) {
		t.Fatalf("stale token not expired: err=%v", err)
	}
	// …and it WAS valid at its own issue time (so the failure above is the TTL,
	// not a broken mint).
	at := time.Now().Add(-30 * time.Minute)
	if _, err := Verify(key, tok, "office", at.Add(time.Second)); err != nil {
		t.Fatalf("token should be valid at issue time: %v", err)
	}
}

// A TTL must never be long: an app-identity token is minted fresh per request.
func TestNewMinter_ClampsTTL(t *testing.T) {
	tok, err := NewMinter(key, 30*24*time.Hour).Mint("user-1", "office")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	c, err := Verify(key, tok, "office", time.Now())
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got := time.Unix(c.Exp, 0).Sub(time.Unix(c.Iat, 0)); got > maxTTL {
		t.Fatalf("TTL not clamped: got %s, want <= %s", got, maxTTL)
	}
}

// VerifyAny spans a shared-secret rotation: a token minted under the previous
// key keeps working while both are configured.
func TestVerifyAny_RotationWindow(t *testing.T) {
	oldKey := []byte("old-key-aaaaaaaaaaaaaaaaaaaaaaaaaa")
	newKey := []byte("new-key-bbbbbbbbbbbbbbbbbbbbbbbbbb")

	tok, err := NewMinter(oldKey, DefaultTTL).Mint("user-1", "office")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := VerifyAny([][]byte{newKey, oldKey}, tok, "office", time.Now()); err != nil {
		t.Fatalf("token minted under the previous key must verify during rotation: %v", err)
	}
	// Once the old key is retired, it stops verifying.
	if _, err := VerifyAny([][]byte{newKey}, tok, "office", time.Now()); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("retired key still verifying: err=%v", err)
	}
	// No keys at all → fail closed, never "ok".
	if _, err := VerifyAny(nil, tok, "office", time.Now()); !errors.Is(err, ErrNoSecret) {
		t.Fatalf("no key must fail closed: err=%v", err)
	}
}

// VerifyAny may skip the audience check (introspection cannot know the caller),
// but must still enforce signature, issuer and expiry.
func TestVerifyAny_EmptyAudienceStillVerifiesTheRest(t *testing.T) {
	tok, err := NewMinter(key, DefaultTTL).Mint("user-1", "office")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	c, err := VerifyAny([][]byte{key}, tok, "", time.Now())
	if err != nil || c.Aud != "office" {
		t.Fatalf("audience-agnostic verify failed: %+v err=%v", c, err)
	}
	if _, err := VerifyAny([][]byte{[]byte("wrong-key-cccccccccccccccccccc")}, tok, "", time.Now()); err == nil {
		t.Fatal("audience-agnostic verify must still check the signature")
	}
}

// Looks is a CLASS check, not an authenticity check — the session gate uses it
// to reject app tokens up front, and every path that acts on one still verifies.
func TestLooks(t *testing.T) {
	tok, err := NewMinter(key, DefaultTTL).Mint("user-1", "office")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !Looks(tok) {
		t.Fatal("a minted app token must be recognised as one")
	}
	// A CP session token is 32 random bytes of base64url — an alphabet with no
	// ".", so it can never be mistaken for an app token.
	for _, session := range []string{
		"z1nEXWl0Y2hlc19hcmVfcmFuZG9tX2J5dGVzX2hlcmU",
		"",
		"not-a-token",
	} {
		if Looks(session) {
			t.Errorf("session-shaped token %q misread as an app token", session)
		}
	}
	// A token signed by an attacker still "looks" like one — and is rejected by
	// the session gate for it. That is the safe direction.
	forged, err := NewMinter([]byte("attacker-key-dddddddddddddddddddd"), DefaultTTL).Mint("victim", "office")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if !Looks(forged) {
		t.Fatal("a forged app token must still be classed as an app token")
	}
}

func TestMint_RequiresSubjectAudienceAndSecret(t *testing.T) {
	if _, err := NewMinter(key, DefaultTTL).Mint("", "office"); !errors.Is(err, ErrEmptyField) {
		t.Error("empty subject must not mint")
	}
	if _, err := NewMinter(key, DefaultTTL).Mint("user-1", ""); !errors.Is(err, ErrEmptyField) {
		t.Error("empty audience must not mint")
	}
	if _, err := NewMinter(nil, DefaultTTL).Mint("user-1", "office"); !errors.Is(err, ErrNoSecret) {
		t.Error("no secret must not mint")
	}
}

// The Minter must not alias a caller's secret slice.
func TestNewMinter_CopiesSecret(t *testing.T) {
	k := []byte("mutable-key-eeeeeeeeeeeeeeeeeeeeee")
	m := NewMinter(k, DefaultTTL)
	tok, err := m.Mint("user-1", "office")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	for i := range k {
		k[i] = 'x'
	}
	if _, err := Verify([]byte("mutable-key-eeeeeeeeeeeeeeeeeeeeee"), tok, "office", time.Now()); err != nil {
		t.Fatalf("minter aliased the caller's secret: %v", err)
	}
}
