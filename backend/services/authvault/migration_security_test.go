package authvault

// migration_security_test.go — adversarial input + step-up tests for the TOTP
// import/export HTTP surface.
//
// The import side hand-decodes a protobuf that arrives inside a URI the user
// pasted from a QR code they scanned SOMEWHERE. That is untrusted input in the
// most literal sense, and the decoder is hand-written. The contract pinned here:
//
//   - a hostile otpauth-migration:// payload cannot panic, hang or OOM the box;
//   - export requires an account-password step-up and FAILS CLOSED if the
//     step-up is not wired;
//   - the export is decryptable WITHOUT this box (else it is not a backup);
//   - import reports exactly what landed and what did not.

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─── The proto decoder must survive hostile lengths ──────────────────────────

// TestProtoRejectsOverflowingLengthPrefix is the regression test for a real
// remote-crash / memory-exhaustion hole in the hand-rolled decoder.
//
// readBytes takes a length from an attacker-controlled varint (any uint64) and
// used to bounds-check it as:
//
//	if p.pos+int(n) > len(p.buf) { ... }
//
// For n ≥ 2^63 the int() conversion goes NEGATIVE, so `p.pos + int(n)` is
// negative, the check passes, and the very next line — make([]byte, n) — asks the
// runtime for eight exabytes. A ~40-byte URI is enough to panic or OOM the box.
//
// The fix compares in uint64 against the bytes actually remaining. These payloads
// must all come back as clean errors.
func TestProtoRejectsOverflowingLengthPrefix(t *testing.T) {
	lengths := []uint64{
		1 << 63,          // int() → negative: the overflow case
		(1 << 64) - 1,    // max uint64
		(1 << 63) + 1024, // just past the sign boundary
		1 << 62,          // huge but still positive as an int
		1 << 40,          // 1 TiB — plausible-looking, still absurd
	}

	for _, n := range lengths {
		t.Run(fmt.Sprintf("len_%d", n), func(t *testing.T) {
			buf := make([]byte, 0, 16)
			buf = append(buf, 0x0A) // field 1 (otp_parameters), wire type 2 (LEN)
			var v [binary.MaxVarintLen64]byte
			buf = append(buf, v[:binary.PutUvarint(v[:], n)]...)
			// …and then nothing. The declared length is a lie.

			uri := "otpauth-migration://offline?data=" +
				base64.StdEncoding.EncodeToString(buf)

			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("PANIC decoding length-prefix %d: %v — hostile QR payload crashes the box", n, r)
					}
				}()
				if _, err := ParseMigrationURI(uri); err == nil {
					t.Errorf("length-prefix %d was ACCEPTED — want a decode error", n)
				}
			}()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Fatalf("HANG decoding length-prefix %d", n)
			}
		})
	}
}

// TestProtoRejectsHostilePayloads — truncated, garbage and structurally-abusive
// migration payloads must error out, never crash.
func TestProtoRejectsHostilePayloads(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
	}{
		{"empty", []byte{}},
		{"truncated-varint", []byte{0xFF, 0xFF, 0xFF}},
		{"varint-overflow-shift", []byte{0x08, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
		{"unknown-wire-type", []byte{0x0F}}, // wire type 7 — undefined
		{"truncated-64bit", []byte{0x09, 0x01, 0x02}},
		{"truncated-32bit", []byte{0x0D, 0x01}},
		{"submessage-truncated", []byte{0x0A, 0x05, 0x0A, 0x10, 0x01}},
		{"wrong-wire-type-on-secret", []byte{0x0A, 0x02, 0x08, 0x01}}, // secret as varint
		{"garbage", []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			uri := "otpauth-migration://offline?data=" + base64.StdEncoding.EncodeToString(c.raw)
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("PANIC on hostile payload %q: %v", c.name, r)
					}
				}()
				_, _ = ParseMigrationURI(uri) // error is fine; a crash is not
			}()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Fatalf("HANG on hostile payload %q", c.name)
			}
		})
	}
}

// TestProtoRejectsTooManyAccounts — repeated field 1 lets one small URI declare
// unbounded accounts, each costing a keychain re-encrypt on import.
func TestProtoRejectsTooManyAccounts(t *testing.T) {
	secret := decodeBase32Secret(testSecret1B32)
	one := buildOtpParameters(secret, "a@b.com", "Iss", pbAlgorithmSHA1, pbDigitsSix, pbOtpTypeTOTP)

	var buf []byte
	for i := 0; i < maxMigrationAccounts+5; i++ {
		buf = append(buf, 0x0A)
		var v [binary.MaxVarintLen64]byte
		buf = append(buf, v[:binary.PutUvarint(v[:], uint64(len(one)))]...)
		buf = append(buf, one...)
	}
	uri := "otpauth-migration://offline?data=" + base64.StdEncoding.EncodeToString(buf)

	if _, err := ParseMigrationURI(uri); err == nil {
		t.Fatalf("payload with %d accounts was accepted — want a limit", maxMigrationAccounts+5)
	}
}

// ─── HTTP: auth + step-up ────────────────────────────────────────────────────

func post(t *testing.T, h *Handler, path, uid, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	if uid != "" {
		req.Header.Set("X-User-ID", uid)
	}
	switch path {
	case "/api/auth/totp/import":
		h.handleImport(rec, req)
	case "/api/auth/totp/export":
		h.handleExport(rec, req)
	default:
		t.Fatalf("unknown path %s", path)
	}
	return rec.Code, rec.Body.String()
}

// TestTOTPImportExportRejectUnauthenticated — no session-derived identity, no
// access. The handler does not assume the middleware ran.
func TestTOTPImportExportRejectUnauthenticated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := NewHandler()
	h.SetReauthenticator(func(string, string) bool { return true })

	if code, _ := post(t, h, "/api/auth/totp/import", "", `{"uri":"otpauth-migration://offline?data=AA=="}`); code != 401 {
		t.Errorf("unauthenticated import: got %d, want 401", code)
	}
	if code, _ := post(t, h, "/api/auth/totp/export", "", `{"account_password":"x","passphrase":"longenough"}`); code != 401 {
		t.Errorf("unauthenticated export: got %d, want 401", code)
	}
}

// TestTOTPExportFailsClosedWithoutReauthenticator — if nobody wires the step-up,
// export must REFUSE, not quietly serve every 2FA seed on a session cookie. An
// unwired gate that degrades to an open one is how this class of bug ships.
func TestTOTPExportFailsClosedWithoutReauthenticator(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := NewHandler() // deliberately NOT wired

	code, body := post(t, h, "/api/auth/totp/export", "u1",
		`{"account_password":"whatever","passphrase":"longenough"}`)
	if code != 503 {
		t.Fatalf("export with no reauthenticator: got %d %s, want 503 (fail closed)", code, body)
	}
	if strings.Contains(body, "secret") || strings.Contains(body, "ciphertext") {
		t.Fatal("unwired export returned seed material")
	}
}

// TestTOTPExportRequiresAccountPassword — session alone is never enough.
func TestTOTPExportRequiresAccountPassword(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := NewHandler()
	h.SetReauthenticator(func(uid, pw string) bool { return uid == "u1" && pw == "right-password" })

	if code, _ := post(t, h, "/api/auth/totp/export", "u1", `{"passphrase":"longenough"}`); code != 401 {
		t.Errorf("export with no account_password: got %d, want 401", code)
	}
	if code, _ := post(t, h, "/api/auth/totp/export", "u1",
		`{"account_password":"wrong","passphrase":"longenough"}`); code != 401 {
		t.Errorf("export with wrong account_password: got %d, want 401", code)
	}
	if code, body := post(t, h, "/api/auth/totp/export", "u1",
		`{"account_password":"right-password","passphrase":"longenough"}`); code != 200 {
		t.Errorf("export with correct account_password: got %d %s, want 200", code, body)
	}
}

// TestTOTPExportStepUpIsThrottled — an unthrottled step-up is an online password
// oracle. Fails closed once locked out.
func TestTOTPExportStepUpIsThrottled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := NewHandler()
	h.SetReauthenticator(func(uid, pw string) bool { return pw == "right-password" })

	for i := 0; i < stepUpMaxFailures; i++ {
		if code, _ := post(t, h, "/api/auth/totp/export", "u1",
			`{"account_password":"guess","passphrase":"longenough"}`); code != 401 {
			t.Fatalf("attempt %d: got %d, want 401", i+1, code)
		}
	}
	if code, _ := post(t, h, "/api/auth/totp/export", "u1",
		`{"account_password":"guess","passphrase":"longenough"}`); code != 429 {
		t.Errorf("after %d failures: got %d, want 429", stepUpMaxFailures, code)
	}
	// Even the right password stays refused while locked out.
	if code, _ := post(t, h, "/api/auth/totp/export", "u1",
		`{"account_password":"right-password","passphrase":"longenough"}`); code != 429 {
		t.Error("correct password while locked out was accepted — the throttle must fail closed")
	}
}

// TestTOTPExportRejectsWeakPassphrase — the blob is every 2FA seed the user has.
func TestTOTPExportRejectsWeakPassphrase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := NewHandler()
	h.SetReauthenticator(func(string, string) bool { return true })

	if code, _ := post(t, h, "/api/auth/totp/export", "u1",
		`{"account_password":"right","passphrase":"abc"}`); code != 400 {
		t.Errorf("weak passphrase: got %d, want 400", code)
	}
}

// TestTOTPImportRejectsOversizedBody — the decoder never sees an unbounded body.
func TestTOTPImportRejectsOversizedBody(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := NewHandler()

	huge := strings.Repeat("A", maxTOTPBody+4096)
	code, body := post(t, h, "/api/auth/totp/import", "u1", fmt.Sprintf(`{"uri":%q}`, huge))
	if code != 413 && code != 400 {
		t.Errorf("oversized body: got %d %s, want 413/400", code, body)
	}
}

// ─── The export must survive the box ─────────────────────────────────────────

// TestExportIsPortableAcrossBoxes is the reason an export exists.
//
// The old blob was encrypted with store.keychainKey — a random key living in
// ~/.vulos/auth/totp/<user>/keyfile ON THIS BOX. That makes the "backup"
// undecryptable in the exact scenario it is for: the box is gone. A backup you
// can only read on the machine you lost is not a backup.
//
// v2 derives its key from the user's passphrase, so the export restores onto a
// DIFFERENT box with a DIFFERENT keyfile.
func TestExportIsPortableAcrossBoxes(t *testing.T) {
	// Box A.
	oldBox, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewStoreAt: %v", err)
	}
	if _, err := oldBox.AddManual("GitHub", "GitHub", "alice@example.com", testSecret1B32); err != nil {
		t.Fatalf("AddManual: %v", err)
	}
	if _, err := oldBox.AddManual("Bank", "Bank", "alice@bank.com", testSecret2B32); err != nil {
		t.Fatalf("AddManual: %v", err)
	}

	blob, err := buildPortableExportBlob(oldBox, "my-backup-passphrase")
	if err != nil {
		t.Fatalf("buildPortableExportBlob: %v", err)
	}
	if blob.Version != exportVersionPassphrase {
		t.Fatalf("export version = %d, want %d (portable)", blob.Version, exportVersionPassphrase)
	}
	if blob.Count != 2 {
		t.Fatalf("blob.Count = %d, want 2", blob.Count)
	}

	// It really is encrypted.
	ct, _ := base64.StdEncoding.DecodeString(blob.Ciphertext)
	if strings.Contains(string(ct), testSecret1B32) {
		t.Fatal("export blob contains a plaintext TOTP secret")
	}

	// Box B: brand new store, its own random keyfile. This is "the old box died".
	newBox, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("NewStoreAt (new box): %v", err)
	}

	// Wrong passphrase must not open it.
	if _, _, err := importBlob(newBox, blob, "wrong-passphrase"); err == nil {
		t.Fatal("export decrypted on the new box with the WRONG passphrase")
	}

	// Right passphrase restores everything.
	imported, failures, err := importBlob(newBox, blob, "my-backup-passphrase")
	if err != nil {
		t.Fatalf("restore on new box: %v", err)
	}
	if len(failures) != 0 {
		t.Errorf("restore reported failures: %v", failures)
	}
	if len(imported) != 2 {
		t.Fatalf("restored %d accounts, want 2", len(imported))
	}

	// And the restored seeds actually generate codes.
	for _, acc := range imported {
		code, _, err := newBox.Code(acc.ID)
		if err != nil {
			t.Fatalf("restored account %q generates no code: %v", acc.Service, err)
		}
		if len(code) != 6 {
			t.Errorf("restored account %q: code %q", acc.Service, code)
		}
	}
}

// TestLegacyBoxKeyBlobStillImports — a v1 (box-key) blob is still restorable on
// the box that produced it, so an existing snapshot is not orphaned.
func TestLegacyBoxKeyBlobStillImports(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreAt(dir)
	if err != nil {
		t.Fatalf("NewStoreAt: %v", err)
	}
	if _, err := store.AddManual("GitHub", "GitHub", "alice@example.com", testSecret1B32); err != nil {
		t.Fatalf("AddManual: %v", err)
	}

	v1, err := buildExportBlob(store) // legacy box-key blob
	if err != nil {
		t.Fatalf("buildExportBlob: %v", err)
	}
	if v1.Version != exportVersionBoxKey {
		t.Fatalf("legacy blob version = %d, want %d", v1.Version, exportVersionBoxKey)
	}

	imported, _, err := importBlob(store, v1, "")
	if err != nil {
		t.Fatalf("v1 blob re-import on the same box: %v", err)
	}
	if len(imported) != 1 {
		t.Fatalf("imported %d, want 1", len(imported))
	}
}

// ─── Round trip through HTTP ─────────────────────────────────────────────────

// TestGoogleAuthenticatorRoundTripOverHTTP — a real-shaped otpauth-migration://
// payload imports over the endpoint, and exports back out. Without this, a user
// cannot adopt the TOTP vault at all (no way in) and cannot survive losing the
// box (no way out).
func TestGoogleAuthenticatorRoundTripOverHTTP(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := NewHandler()
	h.SetReauthenticator(func(uid, pw string) bool { return pw == "account-pw" })

	// A Google Authenticator export carrying 2 TOTP accounts and 1 HOTP (which
	// is not supported and must be reported, not silently dropped).
	s1 := decodeBase32Secret(testSecret1B32)
	s2 := decodeBase32Secret(testSecret2B32)
	e1 := buildOtpParameters(s1, "GitHub:alice@example.com", "GitHub", pbAlgorithmSHA1, pbDigitsSix, pbOtpTypeTOTP)
	e2 := buildOtpParameters(s2, "alice@bank.com", "Bank", pbAlgorithmSHA256, pbDigitsEight, pbOtpTypeTOTP)
	e3 := buildOtpParameters(s1, "legacy@hotp.com", "Legacy", pbAlgorithmSHA1, pbDigitsSix, pbOtpTypeHOTP)
	uri := buildMigrationURI([][]byte{e1, e2, e3})

	code, body := post(t, h, "/api/auth/totp/import", "u1", fmt.Sprintf(`{"uri":%q}`, uri))
	if code != 200 {
		t.Fatalf("import: %d %s", code, body)
	}

	var res struct {
		Parsed   int      `json:"parsed"`
		Imported int      `json:"imported"`
		Failed   int      `json:"failed"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("decode import result: %v", err)
	}
	if res.Parsed != 3 || res.Imported != 2 || res.Failed != 1 {
		t.Fatalf("import result %+v — want parsed=3 imported=2 failed=1 (the HOTP)", res)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "HOTP") {
		t.Errorf("the skipped HOTP account was not reported: %+v — silent partial success", res.Warnings)
	}

	// Export them back out.
	code, body = post(t, h, "/api/auth/totp/export", "u1",
		`{"account_password":"account-pw","passphrase":"my-backup-passphrase"}`)
	if code != 200 {
		t.Fatalf("export: %d %s", code, body)
	}
	var blob ExportBlob
	if err := json.Unmarshal([]byte(body), &blob); err != nil {
		t.Fatalf("decode blob: %v", err)
	}
	if blob.Count != 2 {
		t.Fatalf("exported %d accounts, want 2", blob.Count)
	}

	// The exported blob restores onto a fresh box.
	fresh, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatalf("fresh store: %v", err)
	}
	imported, _, err := importBlob(fresh, &blob, "my-backup-passphrase")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(imported) != 2 {
		t.Fatalf("restored %d, want 2", len(imported))
	}
}

// TestTOTPExportIsNotCacheable — the response is every 2FA seed the user has.
func TestTOTPExportIsNotCacheable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := NewHandler()
	h.SetReauthenticator(func(string, string) bool { return true })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/auth/totp/export",
		strings.NewReader(`{"account_password":"x","passphrase":"longenough"}`))
	req.Header.Set("X-User-ID", "u1")
	h.handleExport(rec, req)

	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}
