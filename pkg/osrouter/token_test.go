package osrouter

import (
	"testing"
	"time"
)

var tokSecret = []byte("test-router-secret-32-bytes-long!")

func TestRouterToken_RoundTrip(t *testing.T) {
	m := NewTokenMinter(tokSecret, 0)
	now := time.Unix(1_700_000_000, 0).UTC()
	m.SetNow(func() time.Time { return now })
	aud := ulidA + ".os.vulos.org"
	tok, err := m.Mint(acctSolo, orgAcme, aud)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	c, err := VerifyRouterToken(tokSecret, tok, aud, now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if c.Sub != acctSolo || c.Org != orgAcme || c.Aud != toLower(aud) {
		t.Fatalf("claims = %+v", c)
	}
}

func TestRouterToken_WrongAudienceRejected(t *testing.T) {
	m := NewTokenMinter(tokSecret, 0)
	now := time.Now().UTC()
	tok, _ := m.Mint(acctSolo, orgAcme, ulidA+".os.vulos.org")
	// A token minted for box A must be useless at box B (cross-box isolation).
	if _, err := VerifyRouterToken(tokSecret, tok, ulidB+".os.vulos.org", now); err != ErrTokenAudience {
		t.Fatalf("err = %v, want ErrTokenAudience", err)
	}
}

func TestRouterToken_TamperedSignatureRejected(t *testing.T) {
	m := NewTokenMinter(tokSecret, 0)
	now := time.Now().UTC()
	aud := ulidA + ".os.vulos.org"
	tok, _ := m.Mint(acctSolo, orgAcme, aud)
	// Flip the last byte of the token.
	b := []byte(tok)
	b[len(b)-1] ^= 0x01
	if _, err := VerifyRouterToken(tokSecret, string(b), aud, now); err == nil {
		t.Fatal("tampered token verified")
	}
	// A different secret must also reject.
	if _, err := VerifyRouterToken([]byte("another-secret-of-the-right-len!"), tok, aud, now); err != ErrTokenSignature {
		t.Fatalf("wrong-secret err = %v, want ErrTokenSignature", err)
	}
}

func TestRouterToken_Expired(t *testing.T) {
	m := NewTokenMinter(tokSecret, time.Minute)
	issued := time.Unix(1_700_000_000, 0).UTC()
	aud := ulidA + ".os.vulos.org"
	tok, _ := m.MintAt(acctSolo, orgAcme, aud, issued)
	// 2 minutes later the 1-minute token is expired.
	if _, err := VerifyRouterToken(tokSecret, tok, aud, issued.Add(2*time.Minute)); err != ErrTokenExpired {
		t.Fatalf("err = %v, want ErrTokenExpired", err)
	}
}

func TestRouterToken_TTLClampedAndDefault(t *testing.T) {
	// ttl <= 0 → default; ttl above cap → clamped.
	if NewTokenMinter(tokSecret, 0).ttl != defaultRouterTTL {
		t.Fatal("zero ttl not defaulted")
	}
	if NewTokenMinter(tokSecret, time.Hour).ttl != maxRouterTTL {
		t.Fatal("oversized ttl not clamped")
	}
}

func TestRouterToken_EmptyFieldsRejected(t *testing.T) {
	m := NewTokenMinter(tokSecret, 0)
	if _, err := m.Mint("", orgAcme, "aud"); err != ErrTokenFields {
		t.Fatalf("empty sub err = %v", err)
	}
	if _, err := m.Mint("sub", "", "aud"); err != ErrTokenFields {
		t.Fatalf("empty org err = %v", err)
	}
	if _, err := m.Mint("sub", "org", ""); err != ErrTokenFields {
		t.Fatalf("empty aud err = %v", err)
	}
}

func TestRouterToken_NoSecret(t *testing.T) {
	m := NewTokenMinter(nil, 0)
	if _, err := m.Mint("s", "o", "a"); err != ErrTokenSecret {
		t.Fatalf("err = %v, want ErrTokenSecret", err)
	}
	if _, err := VerifyRouterToken(nil, "x.y", "a", time.Now()); err != ErrTokenSecret {
		t.Fatalf("verify no-secret err = %v", err)
	}
}

func TestRouterToken_Rotation(t *testing.T) {
	oldSecret := []byte("old-secret-thirty-two-bytes-long")
	newSecret := []byte("new-secret-thirty-two-bytes-long")
	now := time.Now().UTC()
	aud := ulidA + ".os.vulos.org"
	tok, _ := NewTokenMinter(oldSecret, 0).Mint(acctSolo, orgAcme, aud)
	// Verifier accepts a token signed by the OLD key during rotation.
	if _, err := VerifyRouterTokenAny([][]byte{newSecret, oldSecret}, tok, aud, now); err != nil {
		t.Fatalf("rotation verify: %v", err)
	}
	// A token signed by neither key is rejected.
	bogus, _ := NewTokenMinter([]byte("bogus-secret-thirty-two-bytes!!!"), 0).Mint(acctSolo, orgAcme, aud)
	if _, err := VerifyRouterTokenAny([][]byte{newSecret, oldSecret}, bogus, aud, now); err != ErrTokenSignature {
		t.Fatalf("bogus rotation err = %v, want ErrTokenSignature", err)
	}
}
