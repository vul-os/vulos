package credvault

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- helpers -------------------------------------------------------------

func tempVault(t *testing.T) *Vault {
	t.Helper()
	dir := t.TempDir()
	v, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return v
}

const testPassword = "correct-horse-battery-staple"

// ---- crypto round-trip ---------------------------------------------------

func TestAESGCMRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	plain := []byte("hello, credential vault!")

	ct, err := aesGCMEncrypt(key, plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if len(ct) <= len(plain) {
		t.Fatal("ciphertext not longer than plaintext (missing nonce/tag)")
	}

	got, err := aesGCMDecrypt(key, ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("round-trip mismatch: got %q, want %q", got, plain)
	}
}

func TestAESGCMWrongKey(t *testing.T) {
	key := make([]byte, 32)
	ct, err := aesGCMEncrypt(key, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	badKey := make([]byte, 32)
	badKey[0] = 0xFF
	if _, err := aesGCMDecrypt(badKey, ct); err == nil {
		t.Fatal("expected decryption error with wrong key, got nil")
	}
}

func TestDeriveKeyDeterministic(t *testing.T) {
	salt := make([]byte, saltLen)
	k1 := deriveKey("password", salt)
	k2 := deriveKey("password", salt)
	if string(k1) != string(k2) {
		t.Fatal("key derivation not deterministic")
	}
	k3 := deriveKey("wrong", salt)
	if string(k1) == string(k3) {
		t.Fatal("different passwords produced same key")
	}
}

// ---- vault file opacity --------------------------------------------------

// TestVaultFileOpaque verifies that vault.enc is not plain JSON after write.
func TestVaultFileOpaque(t *testing.T) {
	v := tempVault(t)
	if err := v.Unlock(testPassword); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	entry := Entry{
		ID:       "e1",
		URL:      "https://example.com",
		Username: "alice",
		Password: "supersecret",
	}
	if err := v.AddEntry(entry); err != nil {
		t.Fatalf("AddEntry: %v", err)
	}
	_ = v.Lock()

	// Read raw bytes of vault.enc.
	data, err := os.ReadFile(filepath.Join(v.dir, "vault.enc"))
	if err != nil {
		t.Fatalf("read vault.enc: %v", err)
	}
	// The password must NOT appear in the file.
	if strings.Contains(string(data), "supersecret") {
		t.Fatal("plaintext password found in encrypted vault file")
	}
	// It must not be valid JSON (GCM ciphertext is binary). Test the whole
	// document rather than the first byte: the file opens with a random
	// 12-byte nonce, so a leading-'{' check false-fails on 1 write in 256.
	if json.Valid(data) {
		t.Fatal("vault.enc appears to be unencrypted JSON")
	}
}

// ---- wrong master password -----------------------------------------------

func TestWrongMasterPassword(t *testing.T) {
	v := tempVault(t)
	if err := v.Unlock(testPassword); err != nil {
		t.Fatalf("first Unlock: %v", err)
	}
	if err := v.AddEntry(Entry{ID: "e1", Password: "s3cr3t"}); err != nil {
		t.Fatalf("AddEntry: %v", err)
	}
	if err := v.Lock(); err != nil {
		t.Fatalf("Lock: %v", err)
	}

	// Try to open with the wrong password.
	if err := v.Unlock("wrong-password"); err != ErrWrongPassword {
		t.Fatalf("expected ErrWrongPassword, got %v", err)
	}
	// Vault must still be locked.
	if v.IsUnlocked() {
		t.Fatal("vault reports unlocked after wrong password")
	}
	if _, err := v.GetEntry("e1"); err != ErrLocked {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
}

// ---- lock/unlock state machine -------------------------------------------

func TestLockClearsEntries(t *testing.T) {
	v := tempVault(t)
	if err := v.Unlock(testPassword); err != nil {
		t.Fatal(err)
	}
	_ = v.AddEntry(Entry{ID: "e1", Password: "abc"})

	if err := v.Lock(); err != nil {
		t.Fatal(err)
	}

	if v.IsUnlocked() {
		t.Fatal("still unlocked after Lock")
	}
	if _, err := v.GetEntry("e1"); err != ErrLocked {
		t.Fatalf("GetEntry after lock: want ErrLocked, got %v", err)
	}
	if _, err := v.ListEntries(); err != ErrLocked {
		t.Fatalf("ListEntries after lock: want ErrLocked, got %v", err)
	}
}

func TestAlreadyLocked(t *testing.T) {
	v := tempVault(t)
	if err := v.Lock(); err != ErrAlreadyLocked {
		t.Fatalf("expected ErrAlreadyLocked, got %v", err)
	}
}

func TestAlreadyUnlocked(t *testing.T) {
	v := tempVault(t)
	if err := v.Unlock(testPassword); err != nil {
		t.Fatal(err)
	}
	if err := v.Unlock(testPassword); err != ErrAlreadyUnlocked {
		t.Fatalf("expected ErrAlreadyUnlocked, got %v", err)
	}
}

func TestReUnlock(t *testing.T) {
	v := tempVault(t)
	if err := v.Unlock(testPassword); err != nil {
		t.Fatal(err)
	}
	_ = v.AddEntry(Entry{ID: "e1", URL: "https://bank.example", Username: "bob", Password: "s3cret"})
	_ = v.Lock()

	if err := v.Unlock(testPassword); err != nil {
		t.Fatalf("re-unlock: %v", err)
	}
	e, err := v.GetEntry("e1")
	if err != nil {
		t.Fatalf("GetEntry after re-unlock: %v", err)
	}
	if e.Password != "s3cret" {
		t.Fatalf("password mismatch after re-unlock: %q", e.Password)
	}
}

// ---- auto-lock timer -----------------------------------------------------

func TestAutoLock(t *testing.T) {
	v := tempVault(t)
	v.SetAutoLockDuration(80 * time.Millisecond)
	if err := v.Unlock(testPassword); err != nil {
		t.Fatal(err)
	}
	// Must be unlocked immediately after Unlock.
	if !v.IsUnlocked() {
		t.Fatal("not unlocked right after Unlock")
	}
	// Wait for the auto-lock to fire.
	time.Sleep(200 * time.Millisecond)
	if v.IsUnlocked() {
		t.Fatal("vault did not auto-lock")
	}
}

// ---- CRUD ----------------------------------------------------------------

func TestCRUD(t *testing.T) {
	v := tempVault(t)
	if err := v.Unlock(testPassword); err != nil {
		t.Fatal(err)
	}

	e := Entry{
		ID:           "entry-1",
		URL:          "https://example.com",
		Username:     "alice",
		Password:     "hunter2",
		Notes:        "main account",
		LinkedTOTPID: "totp-42",
	}
	if err := v.AddEntry(e); err != nil {
		t.Fatalf("AddEntry: %v", err)
	}

	// Duplicate ID must fail.
	if err := v.AddEntry(e); err != ErrAlreadyExists {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}

	// Get.
	got, err := v.GetEntry("entry-1")
	if err != nil {
		t.Fatalf("GetEntry: %v", err)
	}
	if got.Username != "alice" || got.Password != "hunter2" || got.LinkedTOTPID != "totp-42" {
		t.Fatalf("unexpected entry: %+v", got)
	}

	// Update.
	got.Password = "newpass"
	if err := v.UpdateEntry(got); err != nil {
		t.Fatalf("UpdateEntry: %v", err)
	}
	got2, _ := v.GetEntry("entry-1")
	if got2.Password != "newpass" {
		t.Fatalf("password not updated: %q", got2.Password)
	}

	// List.
	entries, err := v.ListEntries()
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	// Delete.
	if err := v.DeleteEntry("entry-1"); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	if _, err := v.GetEntry("entry-1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
	entries, _ = v.ListEntries()
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}

	// Update / delete on missing entry.
	if err := v.UpdateEntry(Entry{ID: "missing"}); err != ErrNotFound {
		t.Fatalf("UpdateEntry missing: want ErrNotFound, got %v", err)
	}
	if err := v.DeleteEntry("missing"); err != ErrNotFound {
		t.Fatalf("DeleteEntry missing: want ErrNotFound, got %v", err)
	}
}

// ---- persistence across unlock/lock cycles --------------------------------

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	v, _ := New(dir)
	_ = v.Unlock(testPassword)
	_ = v.AddEntry(Entry{ID: "p1", URL: "https://persist.example", Username: "carol", Password: "abc123"})
	_ = v.AddEntry(Entry{ID: "p2", URL: "https://other.example", Username: "dave", Password: "xyz789"})
	_ = v.Lock()

	// New instance, same directory.
	v2, _ := New(dir)
	if err := v2.Unlock(testPassword); err != nil {
		t.Fatalf("re-unlock: %v", err)
	}
	entries, err := v2.ListEntries()
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	byID := make(map[string]Entry, 2)
	for _, e := range entries {
		byID[e.ID] = e
	}
	if byID["p1"].Password != "abc123" {
		t.Fatalf("p1 password: %q", byID["p1"].Password)
	}
	if byID["p2"].Username != "dave" {
		t.Fatalf("p2 username: %q", byID["p2"].Username)
	}
}

// ---- generator -----------------------------------------------------------

func TestGenerateRandomDefaults(t *testing.T) {
	pw, err := Generate(GeneratorConfig{Mode: ModeRandom})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(pw) != 20 {
		t.Fatalf("expected length 20, got %d", len(pw))
	}
}

func TestGenerateRandomLength(t *testing.T) {
	pw, err := Generate(GeneratorConfig{
		Mode:   ModeRandom,
		Random: RandomConfig{Length: 32, Upper: true, Digits: true},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(pw) != 32 {
		t.Fatalf("expected 32, got %d", len(pw))
	}
}

func TestGenerateRandomGuaranteesClasses(t *testing.T) {
	for i := 0; i < 20; i++ {
		pw, err := Generate(GeneratorConfig{
			Mode: ModeRandom,
			Random: RandomConfig{
				Length:  16,
				Upper:   true,
				Lower:   true,
				Digits:  true,
				Symbols: true,
			},
		})
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		hasUpper := strings.ContainsAny(pw, upper)
		hasLower := strings.ContainsAny(pw, lower)
		hasDigit := strings.ContainsAny(pw, digits)
		hasSymbol := strings.ContainsAny(pw, symbols)
		if !hasUpper || !hasLower || !hasDigit || !hasSymbol {
			t.Fatalf("iter %d: missing character class in %q (U=%v L=%v D=%v S=%v)",
				i, pw, hasUpper, hasLower, hasDigit, hasSymbol)
		}
	}
}

func TestGenerateRandomNoAmbiguous(t *testing.T) {
	for i := 0; i < 50; i++ {
		pw, err := Generate(GeneratorConfig{
			Mode: ModeRandom,
			Random: RandomConfig{
				Length:           24,
				Upper:            true,
				Lower:            true,
				Digits:           true,
				ExcludeAmbiguous: true,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(pw, ambiguous) {
			t.Fatalf("ambiguous character found in %q", pw)
		}
	}
}

func TestGenerateRandomUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 10; i++ {
		pw, err := Generate(GeneratorConfig{
			Mode:   ModeRandom,
			Random: RandomConfig{Length: 20, Upper: true, Lower: true, Digits: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		if seen[pw] {
			t.Fatalf("duplicate password generated: %q", pw)
		}
		seen[pw] = true
	}
}

func TestGeneratePassphraseDefaults(t *testing.T) {
	pw, err := Generate(GeneratorConfig{Mode: ModePassphrase})
	if err != nil {
		t.Fatalf("Generate passphrase: %v", err)
	}
	parts := strings.Split(pw, "-")
	if len(parts) != 4 {
		t.Fatalf("expected 4 words separated by '-', got %q (parts=%d)", pw, len(parts))
	}
}

func TestGeneratePassphraseWordCount(t *testing.T) {
	pw, err := Generate(GeneratorConfig{
		Mode:       ModePassphrase,
		Passphrase: PassphraseConfig{WordCount: 6, Separator: "_"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(pw, "_")
	if len(parts) != 6 {
		t.Fatalf("expected 6 words, got %d in %q", len(parts), pw)
	}
}

func TestGeneratePassphraseCapitalize(t *testing.T) {
	for i := 0; i < 10; i++ {
		pw, err := Generate(GeneratorConfig{
			Mode:       ModePassphrase,
			Passphrase: PassphraseConfig{WordCount: 3, Separator: "-", Capitalize: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, word := range strings.Split(pw, "-") {
			if len(word) == 0 {
				continue
			}
			if word[0] < 'A' || word[0] > 'Z' {
				t.Fatalf("word %q not capitalised in %q", word, pw)
			}
		}
	}
}

func TestGeneratePassphraseAppendNumber(t *testing.T) {
	pw, err := Generate(GeneratorConfig{
		Mode:       ModePassphrase,
		Passphrase: PassphraseConfig{WordCount: 3, Separator: "-", AppendNumber: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(pw, "-")
	// 3 words + 1 number suffix = 4 parts
	if len(parts) != 4 {
		t.Fatalf("expected 4 parts (3 words + number), got %d in %q", len(parts), pw)
	}
	last := parts[len(parts)-1]
	if len(last) != 2 {
		t.Fatalf("expected 2-digit number suffix, got %q in %q", last, pw)
	}
	for _, ch := range last {
		if ch < '0' || ch > '9' {
			t.Fatalf("non-digit in number suffix %q of %q", last, pw)
		}
	}
}

func TestGeneratePassphraseUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 10; i++ {
		pw, err := Generate(GeneratorConfig{Mode: ModePassphrase})
		if err != nil {
			t.Fatal(err)
		}
		if seen[pw] {
			t.Fatalf("duplicate passphrase: %q", pw)
		}
		seen[pw] = true
	}
}

func TestGenerateUnknownMode(t *testing.T) {
	_, err := Generate(GeneratorConfig{Mode: GeneratorMode(99)})
	if err == nil {
		t.Fatal("expected error for unknown mode")
	}
}

// TestWordlistFor_ExcludesSeparatorBearingWords asserts DETERMINISTICALLY that
// no candidate word contains the separator.
//
// The pre-existing TestGeneratePassphraseCapitalize covers this only by chance:
// it draws 3 words 10 times against the 4 hyphenated entries in the EFF list
// (drop-down, felt-tip, t-shirt, yo-yo), so it catches the bug about 1.5% of
// the time — it failed in a release run while passing locally and in CI on the
// same commit. This test inspects the candidate set directly, so it cannot
// flake in either direction.
func TestWordlistFor_ExcludesSeparatorBearingWords(t *testing.T) {
	for _, sep := range []string{"-", ".", "_", " "} {
		wl := wordlistFor(sep)
		if len(wl) == 0 {
			t.Fatalf("wordlistFor(%q) returned an empty wordlist", sep)
		}
		for _, w := range wl {
			if strings.Contains(w, sep) {
				t.Errorf("wordlistFor(%q) kept %q, which contains the separator — "+
					"a passphrase built from it splits into more parts than it has words", sep, w)
			}
		}
	}

	// The hyphenated EFF entries are the concrete case that broke. Assert the
	// filter removes exactly them rather than trusting the loop above to have
	// had something to do — if the embed were ever replaced by a list with no
	// hyphens, this test would silently stop testing anything.
	full := passphraseWordlist()
	if len(full) < 7000 {
		t.Skip("EFF wordlist not embedded; the builtin fallback has no hyphenated words")
	}
	var hyphenated int
	for _, w := range full {
		if strings.Contains(w, "-") {
			hyphenated++
		}
	}
	if hyphenated == 0 {
		t.Fatal("expected the EFF wordlist to contain hyphenated words; the filter is now untested")
	}
	if got, want := len(wordlistFor("-")), len(full)-hyphenated; got != want {
		t.Errorf("wordlistFor(\"-\") kept %d words, want %d (%d hyphenated removed)", got, want, hyphenated)
	}
}

// TestGeneratePassphrase_SplitsIntoExactlyWordCount asserts the separator
// actually delimits words: splitting a generated passphrase must yield exactly
// WordCount parts, every one of them capitalised. This is the user-visible
// contract that the hyphenated words broke.
func TestGeneratePassphrase_SplitsIntoExactlyWordCount(t *testing.T) {
	const iterations = 500 // >> the ~1.5%/run odds the old test relied on
	for i := 0; i < iterations; i++ {
		pw, err := Generate(GeneratorConfig{
			Mode:       ModePassphrase,
			Passphrase: PassphraseConfig{WordCount: 3, Separator: "-", Capitalize: true},
		})
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(pw, "-")
		if len(parts) != 3 {
			t.Fatalf("passphrase %q split into %d parts, want 3 — a wordlist entry contains the separator", pw, len(parts))
		}
		for _, w := range parts {
			if w == "" || w[0] < 'A' || w[0] > 'Z' {
				t.Fatalf("part %q not capitalised in %q", w, pw)
			}
		}
	}
}
