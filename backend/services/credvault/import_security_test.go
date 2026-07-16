package credvault

// import_security_test.go — adversarial input + step-up tests for the vault
// import/export HTTP surface.
//
// Import parses files that came from OUTSIDE the box (a Bitwarden/1Password/
// KeePass/Chrome export the user downloaded — or a file an attacker talked them
// into "restoring"). The contract these tests pin:
//
//   - a hostile file cannot crash, hang, or OOM the box;
//   - nothing in a hostile file can escape the vault (no path traversal);
//   - the caller is always told exactly what was imported / skipped / failed;
//   - export requires a master-password step-up and is throttled;
//   - a real-shaped export round-trips: import → entries in vault → export →
//     decrypts → same entries.
//
// The X-User-ID header these handlers read is SET BY THE AUTH MIDDLEWARE from the
// validated session (it deletes any client-supplied copy first) — see
// cmd/server/vault_mount_test.go for that end of the contract.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const secTestMaster = "test-master-password"

// newSecHandler returns a Handler with one user ("u1") whose vault is unlocked,
// plus the directory that vault lives in.
func newSecHandler(t *testing.T) (*Handler, string, string) {
	t.Helper()
	root := t.TempDir()
	h := NewHandler(func(uid string) string { return filepath.Join(root, uid) })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/auth/vault/unlock",
		strings.NewReader(fmt.Sprintf(`{"password":%q}`, secTestMaster)))
	req.Header.Set("X-User-ID", "u1")
	h.handleUnlock(rec, req)
	if rec.Code != 200 {
		t.Fatalf("unlock: %d %s", rec.Code, rec.Body.String())
	}
	return h, root, filepath.Join(root, "u1")
}

// postImport calls the import handler with an already-base64'd file.
func postImport(t *testing.T, h *Handler, uid, format, b64, password string) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"format":%q,"data":%q,"password":%q}`, format, b64, password)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/auth/vault/import", strings.NewReader(body))
	req.Header.Set("X-User-ID", uid)
	h.handleImport(rec, req)
	return rec.Code, rec.Body.String()
}

// importFile base64s raw and imports it.
func importFile(t *testing.T, h *Handler, uid, format string, raw []byte) (int, string) {
	t.Helper()
	return postImport(t, h, uid, format, base64.StdEncoding.EncodeToString(raw), "")
}

// ─── Unauthenticated access ──────────────────────────────────────────────────

// TestImportExportRejectUnauthenticated — no X-User-ID (i.e. the middleware found
// no session) means no vault, at the handler layer too. Defence in depth: the
// handler does not assume the middleware ran.
func TestImportExportRejectUnauthenticated(t *testing.T) {
	h, _, _ := newSecHandler(t)

	for _, path := range []string{"/api/auth/vault/import", "/api/auth/vault/export"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(`{}`)) // no X-User-ID
		if path == "/api/auth/vault/import" {
			h.handleImport(rec, req)
		} else {
			h.handleExport(rec, req)
		}
		if rec.Code != 401 {
			t.Errorf("%s without identity: got %d, want 401", path, rec.Code)
		}
	}
}

// ─── Adversarial import: the box must survive the file ───────────────────────

// TestImportHostileFilesDoNotCrashOrHang throws malformed, truncated, empty,
// binary-garbage and structurally-abusive files at every parser. The bar is low
// and absolute: return an error or an honest result — never panic, never hang,
// never write outside the vault.
func TestImportHostileFilesDoNotCrashOrHang(t *testing.T) {
	h, _, _ := newSecHandler(t)

	hostile := []struct {
		name   string
		format string
		raw    []byte
	}{
		{"empty", "bitwarden", []byte(``)},
		{"truncated-json", "bitwarden", []byte(`{"items":[{"type":1,"login":{"username":"a"`)},
		{"json-null-items", "bitwarden", []byte(`{"items":null}`)},
		{"json-wrong-types", "bitwarden", []byte(`{"items":[{"type":"not-an-int"}]}`)},
		{"binary-garbage", "bitwarden", []byte{0x00, 0xff, 0xfe, 0x00, 0x1b, 0x5b}},
		{"nul-bytes", "chrome-csv", []byte("name,url,username,password\n\x00,\x00,\x00,\x00\n")},
		{"header-only", "chrome-csv", []byte("name,url,username,password\n")},
		{"no-header", "chrome-csv", []byte("just,some,random,values\n")},
		{"missing-columns", "keepass-csv", []byte("Account\nonly-one-column\n")},
		{"ragged-rows", "chrome-csv", []byte("name,url,username,password\na\na,b\na,b,c,d,e,f,g\n")},
		{"unterminated-quote", "chrome-csv", []byte("name,url,username,password\n\"unterminated,,,\n")},
		{"crlf-only", "1password-csv", []byte("\r\n\r\n\r\n")},
		{"1pif-garbage", "1password-1pif", []byte("***5642bee8***\nnot json at all\n{\"broken\":")},
		{"1pif-empty-records", "1password-1pif", []byte("***5642bee8***\n***5642bee8***\n")},
		{"truncated-encrypted", "encrypted", []byte{0x01, 0x02, 0x03}},
	}

	for _, c := range hostile {
		t.Run(c.name, func(t *testing.T) {
			done := make(chan struct{})
			var code int
			var body string
			go func() {
				defer close(done)
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("PANIC on hostile %s/%s: %v", c.format, c.name, r)
					}
				}()
				if c.format == "encrypted" {
					code, body = postImport(t, h, "u1", c.format,
						base64.StdEncoding.EncodeToString(c.raw), "some-password")
				} else {
					code, body = importFile(t, h, "u1", c.format, c.raw)
				}
			}()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatalf("HANG on hostile %s/%s — no response in 20s", c.format, c.name)
			}
			// Any answer is fine; a 5xx would mean the box blamed itself for a bad file.
			if code >= 500 {
				t.Errorf("hostile %s/%s returned %d %s — want a 4xx, not a server error",
					c.format, c.name, code, body)
			}
		})
	}
}

// TestImportRejectsOversizedFile — a file too big to be a password export is
// refused with 413 rather than decoded into RAM.
func TestImportRejectsOversizedFile(t *testing.T) {
	h, _, _ := newSecHandler(t)

	// Decoded size just over maxImportFile, but the base64 body stays under
	// maxImportBody so we exercise the DECODED-size check specifically.
	big := make([]byte, maxImportFile+1024)
	for i := range big {
		big[i] = 'a'
	}
	code, body := importFile(t, h, "u1", "chrome-csv", big)
	if code != 413 {
		t.Errorf("oversized file: got %d %s, want 413", code, body)
	}
}

// TestImportRejectsOversizedBody — the request itself is capped, so an enormous
// upload never reaches the JSON decoder.
func TestImportRejectsOversizedBody(t *testing.T) {
	h, _, _ := newSecHandler(t)

	huge := strings.Repeat("A", maxImportBody+4096)
	code, body := postImport(t, h, "u1", "chrome-csv", huge, "")
	if code != 413 && code != 400 {
		t.Errorf("oversized body: got %d %s, want 413/400", code, body)
	}
}

// TestImportRefusesTooManyEntriesRatherThanTruncating — the anti-silent-partial
// rule. A file with more rows than we accept is REFUSED whole. Truncating it
// would leave the user believing they had migrated when they had not.
func TestImportRefusesTooManyEntriesRatherThanTruncating(t *testing.T) {
	h, _, dir := newSecHandler(t)

	var sb strings.Builder
	sb.WriteString("name,url,username,password\n")
	for i := 0; i < maxImportEntries+10; i++ {
		fmt.Fprintf(&sb, "n%d,https://s%d.example,u%d,p%d\n", i, i, i, i)
	}
	code, body := importFile(t, h, "u1", "chrome-csv", []byte(sb.String()))
	if code != 413 {
		t.Fatalf("over-limit import: got %d %s, want 413", code, body)
	}

	// And nothing was written.
	v, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := v.Unlock(secTestMaster); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	entries, err := v.ListEntries()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("refused import still wrote %d entries — it must be all-or-nothing", len(entries))
	}
}

// TestImportDeeplyNestedJSONIsBounded — a deeply-nested JSON document must be
// rejected by the decoder, not blow the stack.
func TestImportDeeplyNestedJSONIsBounded(t *testing.T) {
	h, _, _ := newSecHandler(t)

	depth := 200000
	raw := []byte(`{"items":` + strings.Repeat("[", depth) + strings.Repeat("]", depth) + `}`)

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("PANIC on deeply-nested JSON: %v", r)
			}
		}()
		code, _ := importFile(t, h, "u1", "bitwarden", raw)
		if code >= 500 {
			t.Errorf("deeply-nested JSON: got %d, want a 4xx", code)
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("HANG on deeply-nested JSON")
	}
}

// TestImportHostileFieldsCannotEscapeTheVault — path-traversal, NUL and control
// bytes in imported fields are DATA, never filenames. Entry ids are
// server-generated UUIDs and the vault is a single encrypted file, so a hostile
// "username" of ../../../etc/passwd must land inertly inside the vault and
// create nothing on disk outside it.
func TestImportHostileFieldsCannotEscapeTheVault(t *testing.T) {
	h, root, dir := newSecHandler(t)

	csv := "name,url,username,password\n" +
		"a,https://x.example,../../../etc/passwd,p1\n" +
		"b,file:///etc/shadow,../../../../root/.ssh/id_rsa,p2\n" +
		"c,https://y.example,..\\..\\windows\\system32,p3\n"

	code, body := importFile(t, h, "u1", "chrome-csv", []byte(csv))
	if code != 200 {
		t.Fatalf("import: got %d %s", code, body)
	}

	// The traversal strings are stored as ordinary usernames…
	v, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := v.Unlock(secTestMaster); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	entries, err := v.ListEntries()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(entries))
	}

	// …and the vault directory contains ONLY the vault's own files.
	found, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, f := range found {
		switch f.Name() {
		case "vault.enc", "vault.key":
		default:
			t.Errorf("unexpected file %q in vault dir — import wrote something it should not have", f.Name())
		}
	}
	// Nothing escaped upward either.
	up, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir root: %v", err)
	}
	for _, f := range up {
		if f.Name() != "u1" {
			t.Errorf("import created %q OUTSIDE the user's vault dir — path traversal", f.Name())
		}
	}
}

// TestImportOversizedFieldIsTruncatedAndReported — a single 6 MiB "username"
// gets clamped, and the caller is TOLD. Silent mutation of a credential is not
// acceptable.
func TestImportOversizedFieldIsTruncatedAndReported(t *testing.T) {
	h, _, _ := newSecHandler(t)

	giant := strings.Repeat("A", maxFieldLen*2)
	csv := "name,url,username,password\nx,https://x.example," + giant + ",p1\n"

	code, body := importFile(t, h, "u1", "chrome-csv", []byte(csv))
	if code != 200 {
		t.Fatalf("import: got %d %s", code, body)
	}
	var res ImportResult
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Error("oversized field was truncated with no warning — silent mutation")
	}
}

// ─── Honest accounting ───────────────────────────────────────────────────────

// TestImportReportsImportedSkippedFailed — parsed/imported/skipped/errors must
// reconcile, so the UI can never show a partial import as a clean success.
func TestImportReportsImportedSkippedFailed(t *testing.T) {
	h, _, _ := newSecHandler(t)

	csv := "name,url,username,password\n" +
		"a,https://a.example,alice,p1\n" +
		"b,https://b.example,bob,p2\n"
	code, body := importFile(t, h, "u1", "chrome-csv", []byte(csv))
	if code != 200 {
		t.Fatalf("first import: %d %s", code, body)
	}
	var first ImportResult
	json.Unmarshal([]byte(body), &first)
	if first.Parsed != 2 || first.Imported != 2 || first.Skipped != 0 || first.Errors != 0 {
		t.Fatalf("first import: %+v, want parsed=2 imported=2 skipped=0 errors=0", first)
	}

	// Re-import the same file plus one new row: 2 dupes skipped, 1 imported.
	csv2 := csv + "c,https://c.example,carol,p3\n"
	code, body = importFile(t, h, "u1", "chrome-csv", []byte(csv2))
	if code != 200 {
		t.Fatalf("second import: %d %s", code, body)
	}
	var second ImportResult
	json.Unmarshal([]byte(body), &second)
	if second.Parsed != 3 || second.Imported != 1 || second.Skipped != 2 || second.Errors != 0 {
		t.Fatalf("second import: %+v, want parsed=3 imported=1 skipped=2 errors=0", second)
	}
	if second.Parsed != second.Imported+second.Skipped+second.Errors {
		t.Errorf("counts do not reconcile: %+v", second)
	}
}

// ─── Export: step-up + throttle ──────────────────────────────────────────────

// TestExportRequiresMasterPasswordStepUp — the vault is UNLOCKED and the session
// is valid; that is exactly the XSS/CSRF attacker's position. Without the master
// password they still get nothing.
func TestExportRequiresMasterPasswordStepUp(t *testing.T) {
	h, _, _ := newSecHandler(t)

	post := func(body string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/auth/vault/export", strings.NewReader(body))
		req.Header.Set("X-User-ID", "u1")
		h.handleExport(rec, req)
		return rec.Code, rec.Body.String()
	}

	if code, _ := post(`{"password":"export-passphrase"}`); code != 401 {
		t.Errorf("no master_password: got %d, want 401", code)
	}
	if code, _ := post(`{"master_password":"wrong","password":"export-passphrase"}`); code != 401 {
		t.Errorf("wrong master_password: got %d, want 401", code)
	}
	code, _ := post(fmt.Sprintf(`{"master_password":%q,"password":"export-passphrase"}`, secTestMaster))
	if code != 200 {
		t.Errorf("correct master_password: got %d, want 200", code)
	}
}

// TestExportRejectsWeakExportPassword — the file that holds every password the
// user has must not be wrapped in a 3-character passphrase.
func TestExportRejectsWeakExportPassword(t *testing.T) {
	h, _, _ := newSecHandler(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/auth/vault/export",
		strings.NewReader(fmt.Sprintf(`{"master_password":%q,"password":"abc"}`, secTestMaster)))
	req.Header.Set("X-User-ID", "u1")
	h.handleExport(rec, req)
	if rec.Code != 400 {
		t.Errorf("weak export password: got %d, want 400", rec.Code)
	}
}

// TestExportStepUpIsThrottled — /export runs Argon2id (64 MiB) per attempt. Left
// unthrottled it is BOTH an online master-password oracle and a memory-exhaustion
// gadget. After stepUpMaxFailures wrong tries the user is locked out — and the
// lockout holds even when the CORRECT password is then supplied.
func TestExportStepUpIsThrottled(t *testing.T) {
	h, _, _ := newSecHandler(t)

	post := func(master string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/auth/vault/export",
			strings.NewReader(fmt.Sprintf(`{"master_password":%q,"password":"export-passphrase"}`, master)))
		req.Header.Set("X-User-ID", "u1")
		h.handleExport(rec, req)
		return rec.Code
	}

	for i := 0; i < stepUpMaxFailures; i++ {
		if code := post("wrong-guess"); code != 401 {
			t.Fatalf("attempt %d: got %d, want 401", i+1, code)
		}
	}
	if code := post("wrong-guess"); code != 429 {
		t.Errorf("after %d failures: got %d, want 429 (locked out)", stepUpMaxFailures, code)
	}
	// Fails CLOSED: even the right password is refused while locked out.
	if code := post(secTestMaster); code != 429 {
		t.Errorf("correct password while locked out: got %d, want 429 — the throttle must fail closed", code)
	}
}

// ─── Round trip: the whole point of the feature ──────────────────────────────

// TestRoundTripRealShapedExports imports a real-shaped Bitwarden, Chrome and
// KeePass export, confirms the entries are in the vault, exports the vault, and
// confirms the encrypted file DECRYPTS and contains them. If this test fails,
// a user cannot migrate onto Vulos or get their passwords back off it.
func TestRoundTripRealShapedExports(t *testing.T) {
	h, _, dir := newSecHandler(t)

	bitwarden := []byte(`{
	  "encrypted": false,
	  "folders": [{"id":"f1","name":"Personal"}],
	  "items": [
	    {"id":"i1","type":1,"name":"Example Bank","notes":"main account",
	     "login":{"username":"alice@example.com","password":"bw-pass-1",
	              "uris":[{"match":null,"uri":"https://bank.example.com"}]}},
	    {"id":"i2","type":2,"name":"A secure note","notes":"not a login"},
	    {"id":"i3","type":1,"name":"GitHub",
	     "login":{"username":"alice","password":"bw-pass-2",
	              "uris":[{"match":null,"uri":"https://github.com"}]}}
	  ]
	}`)
	chrome := []byte("name,url,username,password\n" +
		"news.example,https://news.example,alice-news,chrome-pass-1\n" +
		"shop.example,https://shop.example,alice-shop,chrome-pass-2\n")
	keepass := []byte(`"Account","Login Name","Password","Web Site","Comments"` + "\n" +
		`"Router","admin","keepass-pass-1","https://192.168.1.1","home router"` + "\n" +
		`"Mail","alice","keepass-pass-2","https://mail.example","with, a comma"` + "\n")

	for _, c := range []struct {
		format string
		raw    []byte
		want   int
	}{
		{"bitwarden", bitwarden, 2}, // the type-2 secure note is not a login
		{"chrome-csv", chrome, 2},
		{"keepass-csv", keepass, 2},
	} {
		code, body := importFile(t, h, "u1", c.format, c.raw)
		if code != 200 {
			t.Fatalf("%s import: %d %s", c.format, code, body)
		}
		var res ImportResult
		if err := json.Unmarshal([]byte(body), &res); err != nil {
			t.Fatalf("%s decode: %v", c.format, err)
		}
		if res.Imported != c.want {
			t.Fatalf("%s: imported %d, want %d (%+v)", c.format, res.Imported, c.want, res)
		}
	}

	// All six credentials are now in the vault.
	wantPasswords := []string{
		"bw-pass-1", "bw-pass-2",
		"chrome-pass-1", "chrome-pass-2",
		"keepass-pass-1", "keepass-pass-2",
	}

	// Export.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/auth/vault/export",
		strings.NewReader(fmt.Sprintf(`{"master_password":%q,"password":"my-backup-passphrase"}`, secTestMaster)))
	req.Header.Set("X-User-ID", "u1")
	h.handleExport(rec, req)
	if rec.Code != 200 {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	ct, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}

	// The blob must actually be encrypted.
	for _, p := range wantPasswords {
		if strings.Contains(string(ct), p) {
			t.Fatalf("export blob contains plaintext password %q — it is not encrypted", p)
		}
	}

	// A wrong passphrase must not open it.
	scratchBad, _ := New(t.TempDir())
	scratchBad.Unlock("scratch-master")
	if _, err := ImportEncrypted(scratchBad, ct, "wrong-passphrase"); err == nil {
		t.Fatal("export decrypted with the WRONG passphrase")
	}

	// The right passphrase restores every credential — onto a DIFFERENT vault
	// with a DIFFERENT master password, which is what "restore on a new box"
	// means.
	restored, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("restore vault: %v", err)
	}
	if err := restored.Unlock("a-completely-different-master"); err != nil {
		t.Fatalf("restore unlock: %v", err)
	}
	res, err := ImportEncrypted(restored, ct, "my-backup-passphrase")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if res.Imported != len(wantPasswords) {
		t.Fatalf("restored %d entries, want %d", res.Imported, len(wantPasswords))
	}

	entries, err := restored.ListEntries()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := make(map[string]bool, len(entries))
	for _, e := range entries {
		got[e.Password] = true
	}
	for _, p := range wantPasswords {
		if !got[p] {
			t.Errorf("restored vault is missing password %q", p)
		}
	}

	_ = dir
}

// TestBulkImportIsNotQuadratic — AddEntry rewrites the ENTIRE encrypted vault on
// every call, so importing N credentials one-by-one is O(N²): a routine 5k-entry
// migration would rewrite a growing blob 5,000 times and wedge the box. Import
// must go through the batched path (one encrypt-and-write).
//
// A generous ceiling — this is a "did someone reintroduce the per-entry write"
// alarm, not a benchmark.
func TestBulkImportIsNotQuadratic(t *testing.T) {
	h, _, _ := newSecHandler(t)

	const n = 5000
	var sb strings.Builder
	sb.WriteString("name,url,username,password\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "n%d,https://s%d.example,u%d,p%d\n", i, i, i, i)
	}

	start := time.Now()
	code, body := importFile(t, h, "u1", "chrome-csv", []byte(sb.String()))
	elapsed := time.Since(start)

	if code != 200 {
		t.Fatalf("bulk import: %d %s", code, body)
	}
	var res ImportResult
	json.Unmarshal([]byte(body), &res)
	if res.Imported != n {
		t.Fatalf("imported %d, want %d", res.Imported, n)
	}
	if elapsed > 30*time.Second {
		t.Errorf("importing %d entries took %s — the per-entry vault rewrite is back (O(N²))", n, elapsed)
	}
}
