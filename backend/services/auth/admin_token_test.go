package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// AT10 — admin token tests.

func AT10_tmpHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Ensure db subdir exists (IssueAdminToken creates it, but tests may check paths).
	os.MkdirAll(filepath.Join(dir, ".vulos", "db"), 0700)
	return dir
}

func TestAT10_IssueAndValidate(t *testing.T) {
	home := AT10_tmpHome(t)

	tok, err := IssueAdminToken(home)
	if err != nil {
		t.Fatalf("IssueAdminToken: %v", err)
	}
	if !strings.HasPrefix(tok, AT10TokenPrefix) {
		t.Fatalf("token missing prefix %q: %s", AT10TokenPrefix, tok)
	}
	// Base part should be 32-byte base64 raw URL (43 chars) — total prefix+43
	bare := tok[len(AT10TokenPrefix):]
	if len(bare) < 40 {
		t.Fatalf("bare token too short: %d", len(bare))
	}

	ok, rotatesAt, err := ValidateAdminToken(home, tok)
	if err != nil {
		t.Fatalf("ValidateAdminToken: %v", err)
	}
	if !ok {
		t.Fatal("expected valid=true")
	}
	if time.Until(rotatesAt) < 29*24*time.Hour {
		t.Fatalf("rotatesAt too soon: %v", rotatesAt)
	}
}

func TestAT10_WrongToken(t *testing.T) {
	home := AT10_tmpHome(t)
	_, err := IssueAdminToken(home)
	if err != nil {
		t.Fatalf("IssueAdminToken: %v", err)
	}

	ok, _, err := ValidateAdminToken(home, AT10TokenPrefix+"wrongvalue1234567890123456789012")
	if err != nil {
		t.Fatalf("ValidateAdminToken: %v", err)
	}
	if ok {
		t.Fatal("expected valid=false for wrong token")
	}
}

func TestAT10_NoTokenFile(t *testing.T) {
	home := AT10_tmpHome(t)
	// Don't issue — file doesn't exist.
	ok, _, err := ValidateAdminToken(home, AT10TokenPrefix+"anything")
	if err != nil {
		t.Fatalf("ValidateAdminToken on missing file: %v", err)
	}
	if ok {
		t.Fatal("expected valid=false when no token file")
	}
}

func TestAT10_RotateInvalidatesOld(t *testing.T) {
	home := AT10_tmpHome(t)

	tok1, err := IssueAdminToken(home)
	if err != nil {
		t.Fatalf("IssueAdminToken: %v", err)
	}

	tok2, err := RotateAdminToken(home)
	if err != nil {
		t.Fatalf("RotateAdminToken: %v", err)
	}
	if tok1 == tok2 {
		t.Fatal("rotate must produce a different token")
	}

	// Old token must no longer be valid.
	ok, _, _ := ValidateAdminToken(home, tok1)
	if ok {
		t.Fatal("old token must be invalid after rotation")
	}

	// New token must be valid.
	ok, _, _ = ValidateAdminToken(home, tok2)
	if !ok {
		t.Fatal("new token must be valid")
	}
}

func TestAT10_ExpiredToken(t *testing.T) {
	home := AT10_tmpHome(t)

	tok, err := IssueAdminToken(home)
	if err != nil {
		t.Fatalf("IssueAdminToken: %v", err)
	}

	// Manually back-date the stored record so it appears expired.
	p := at10TokenPath(home)
	r := at10Read(home)
	if r == nil {
		t.Fatal("no record written")
	}
	r.IssuedAt = time.Now().Add(-31 * 24 * time.Hour)
	r.RotatesAt = time.Now().Add(-1 * time.Hour)
	if err := at10Write(home, r); err != nil {
		t.Fatalf("at10Write: %v", err)
	}
	_ = p

	ok, _, err := ValidateAdminToken(home, tok)
	if err != nil {
		t.Fatalf("ValidateAdminToken: %v", err)
	}
	if ok {
		t.Fatal("expected valid=false for expired token")
	}
}

func TestAT10_TokenPrefix8(t *testing.T) {
	tok := AT10TokenPrefix + "ABCDEFGHIJKLMNOPQRSTUVWXYZ123456"
	preview := AT10TokenPrefix8(tok)
	if !strings.HasPrefix(preview, AT10TokenPrefix) {
		t.Fatalf("prefix8 missing token prefix: %s", preview)
	}
	if strings.Contains(preview, "IJKLMNOPQRS") {
		t.Fatalf("prefix8 exposes too much: %s", preview)
	}
	if !strings.HasSuffix(preview, "...") {
		t.Fatalf("prefix8 should end with '...': %s", preview)
	}
}

func TestAT10_AtomicWrite(t *testing.T) {
	home := AT10_tmpHome(t)
	// Issue twice concurrently — no panic / corruption.
	done := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			_, err := IssueAdminToken(home)
			done <- err
		}()
	}
	for i := 0; i < 4; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent IssueAdminToken: %v", err)
		}
	}
	// File must be valid JSON after concurrent writes.
	r := at10Read(home)
	if r == nil {
		t.Fatal("record nil after concurrent writes")
	}
}
