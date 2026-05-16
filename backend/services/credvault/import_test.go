package credvault

import (
	"encoding/base64"
	"os"
	"testing"
)

// openTestVault creates a temporary vault, unlocks it, and registers a cleanup.
func openTestVault(t *testing.T) *Vault {
	t.Helper()
	dir := t.TempDir()
	v, err := New(dir)
	if err != nil {
		t.Fatalf("New vault: %v", err)
	}
	if err := v.Unlock("test-master-password"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	return v
}

// ---- Format import tests ------------------------------------------------

func TestParseBitwardenJSON(t *testing.T) {
	raw := []byte(`{
		"items": [
			{
				"type": 1,
				"name": "Example Bank",
				"notes": "primary account",
				"login": {
					"username": "alice@example.com",
					"password": "hunter2",
					"uris": [{"uri": "https://bank.example.com"}]
				}
			},
			{
				"type": 2,
				"name": "A secure note — should be skipped",
				"notes": "not a login"
			}
		]
	}`)

	entries, err := ParseBitwardenJSON(raw)
	if err != nil {
		t.Fatalf("ParseBitwardenJSON: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 login entry, got %d", len(entries))
	}
	e := entries[0]
	if e.URL != "https://bank.example.com" {
		t.Errorf("URL: got %q", e.URL)
	}
	if e.Username != "alice@example.com" {
		t.Errorf("Username: got %q", e.Username)
	}
	if e.Password != "hunter2" {
		t.Errorf("Password: got %q", e.Password)
	}
	if e.Notes != "primary account" {
		t.Errorf("Notes: got %q", e.Notes)
	}
}

func TestImportBitwardenJSON(t *testing.T) {
	raw := []byte(`{
		"items": [
			{
				"type": 1,
				"name": "GitHub",
				"notes": "",
				"login": {
					"username": "bob",
					"password": "s3cr3t",
					"uris": [{"uri": "https://github.com"}]
				}
			},
			{
				"type": 1,
				"name": "GitLab",
				"notes": "",
				"login": {
					"username": "bob",
					"password": "gl-pass",
					"uris": [{"uri": "https://gitlab.com"}]
				}
			}
		]
	}`)

	v := openTestVault(t)
	parsed, err := ParseBitwardenJSON(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	result, err := ImportEntries(v, parsed)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Imported != 2 {
		t.Errorf("imported: want 2, got %d", result.Imported)
	}
	if result.Skipped != 0 {
		t.Errorf("skipped: want 0, got %d", result.Skipped)
	}

	entries, _ := v.ListEntries()
	if len(entries) != 2 {
		t.Errorf("vault entry count: want 2, got %d", len(entries))
	}
}

func TestImport1PIF(t *testing.T) {
	// Minimal .1pif: separator line + one WebForm record.
	raw := []byte(`**begin 1Password data**
{"typeName":"webforms.WebForm","location":"https://example.com","secureContents":{"fields":[{"designation":"username","value":"charlie"},{"designation":"password","value":"p4ssw0rd"}],"URL":"https://example.com/login","password":""}}
**end 1Password data**`)

	v := openTestVault(t)
	parsed, err := Parse1PIF(raw)
	if err != nil {
		t.Fatalf("Parse1PIF: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("want 1 entry, got %d", len(parsed))
	}
	if parsed[0].Username != "charlie" {
		t.Errorf("username: %q", parsed[0].Username)
	}
	if parsed[0].Password != "p4ssw0rd" {
		t.Errorf("password: %q", parsed[0].Password)
	}

	result, err := ImportEntries(v, parsed)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Imported != 1 {
		t.Errorf("imported: want 1, got %d", result.Imported)
	}
}

func TestImport1PasswordCSV(t *testing.T) {
	csv := []byte("Title,Username,Password,OTPAuth,URL,Notes\n" +
		"\"AWS Console\",admin,\"awsP@ss!\",,,\"root account\"\n" +
		"\"Heroku\",dev@example.com,heroku-secret,,https://heroku.com,\n")

	v := openTestVault(t)
	parsed, err := Parse1PasswordCSV(csv)
	if err != nil {
		t.Fatalf("Parse1PasswordCSV: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("want 2 entries, got %d", len(parsed))
	}

	// Check first entry
	if parsed[0].Username != "admin" {
		t.Errorf("entry 0 username: %q", parsed[0].Username)
	}
	if parsed[0].Password != "awsP@ss!" {
		t.Errorf("entry 0 password: %q", parsed[0].Password)
	}
	if parsed[0].Notes != "root account" {
		t.Errorf("entry 0 notes: %q", parsed[0].Notes)
	}

	result, err := ImportEntries(v, parsed)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Imported != 2 {
		t.Errorf("imported: want 2, got %d", result.Imported)
	}
}

func TestImportKeePassCSV(t *testing.T) {
	csv := []byte(`"Account","Login Name","Password","Web Site","Comments"
"Personal Email","dave@example.com","emailPass!","https://mail.example.com","personal"
"Work VPN","dave","vpnSecret","https://vpn.corp.com",""
`)

	v := openTestVault(t)
	parsed, err := ParseKeePassCSV(csv)
	if err != nil {
		t.Fatalf("ParseKeePassCSV: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("want 2 entries, got %d", len(parsed))
	}
	if parsed[0].URL != "https://mail.example.com" {
		t.Errorf("entry 0 URL: %q", parsed[0].URL)
	}
	if parsed[0].Username != "dave@example.com" {
		t.Errorf("entry 0 username: %q", parsed[0].Username)
	}

	result, err := ImportEntries(v, parsed)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Imported != 2 {
		t.Errorf("imported: want 2, got %d", result.Imported)
	}
}

func TestImportChromeCSV(t *testing.T) {
	csv := []byte("name,url,username,password\n" +
		"GitHub,https://github.com,eve,gh-token\n" +
		"Slack,https://app.slack.com/,eve@company.com,slackPass\n")

	v := openTestVault(t)
	parsed, err := ParseChromeCSV(csv)
	if err != nil {
		t.Fatalf("ParseChromeCSV: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("want 2 entries, got %d", len(parsed))
	}
	if parsed[0].URL != "https://github.com" {
		t.Errorf("entry 0 URL: %q", parsed[0].URL)
	}
	if parsed[1].Password != "slackPass" {
		t.Errorf("entry 1 password: %q", parsed[1].Password)
	}

	result, err := ImportEntries(v, parsed)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Imported != 2 {
		t.Errorf("imported: want 2, got %d", result.Imported)
	}
}

// ---- Encrypted export / re-import test ----------------------------------

func TestExportImportEncrypted(t *testing.T) {
	v := openTestVault(t)

	// Seed vault with two entries.
	entries := []Entry{
		{ID: "id-1", URL: "https://a.example.com", Username: "user-a", Password: "pass-a"},
		{ID: "id-2", URL: "https://b.example.com", Username: "user-b", Password: "pass-b"},
	}
	for _, e := range entries {
		if err := v.AddEntry(e); err != nil {
			t.Fatalf("AddEntry %s: %v", e.ID, err)
		}
	}

	// Export.
	exportPassword := "export-secret-xyz"
	ct, err := ExportEncrypted(v, exportPassword)
	if err != nil {
		t.Fatalf("ExportEncrypted: %v", err)
	}
	if len(ct) == 0 {
		t.Fatal("export ciphertext is empty")
	}

	// Import into a fresh vault.
	dir2 := t.TempDir()
	v2, _ := New(dir2)
	if err := v2.Unlock("another-master"); err != nil {
		t.Fatalf("Unlock v2: %v", err)
	}

	result, err := ImportEncrypted(v2, ct, exportPassword)
	if err != nil {
		t.Fatalf("ImportEncrypted: %v", err)
	}
	if result.Imported != 2 {
		t.Errorf("imported: want 2, got %d", result.Imported)
	}

	// Verify entries are equivalent.
	imported, err := v2.ListEntries()
	if err != nil {
		t.Fatalf("ListEntries v2: %v", err)
	}
	if len(imported) != 2 {
		t.Fatalf("v2 entry count: want 2, got %d", len(imported))
	}

	byURL := make(map[string]Entry)
	for _, e := range imported {
		byURL[e.URL] = e
	}
	for _, orig := range entries {
		imp, ok := byURL[orig.URL]
		if !ok {
			t.Errorf("missing entry for URL %q", orig.URL)
			continue
		}
		if imp.Username != orig.Username {
			t.Errorf("URL %q: username want %q, got %q", orig.URL, orig.Username, imp.Username)
		}
		if imp.Password != orig.Password {
			t.Errorf("URL %q: password want %q, got %q", orig.URL, orig.Password, imp.Password)
		}
	}
}

func TestExportImportRoundtripBase64(t *testing.T) {
	// Simulate the HTTP round-trip where the payload is base64-encoded.
	v := openTestVault(t)
	_ = v.AddEntry(Entry{ID: "id-x", URL: "https://x.test", Username: "x", Password: "xp"})

	ct, err := ExportEncrypted(v, "roundtrip-pass")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	b64 := base64.StdEncoding.EncodeToString(ct)
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}

	dir2 := t.TempDir()
	v2, _ := New(dir2)
	_ = v2.Unlock("other-master")
	result, err := ImportEncrypted(v2, decoded, "roundtrip-pass")
	if err != nil {
		t.Fatalf("ImportEncrypted: %v", err)
	}
	if result.Imported != 1 {
		t.Errorf("imported: want 1, got %d", result.Imported)
	}
}

func TestImportEncryptedWrongPassword(t *testing.T) {
	v := openTestVault(t)
	_ = v.AddEntry(Entry{ID: "id-y", URL: "https://y.test", Username: "y", Password: "yp"})

	ct, _ := ExportEncrypted(v, "correct-pass")

	dir2 := t.TempDir()
	v2, _ := New(dir2)
	_ = v2.Unlock("master")
	_, err := ImportEncrypted(v2, ct, "wrong-pass")
	if err == nil {
		t.Fatal("expected error for wrong export password, got nil")
	}
}

// ---- Deduplication test -------------------------------------------------

func TestDeduplication(t *testing.T) {
	v := openTestVault(t)

	// Pre-populate the vault with one entry.
	existing := Entry{
		ID:       "existing-1",
		URL:      "https://github.com/",
		Username: "alice",
		Password: "old-pass",
	}
	if err := v.AddEntry(existing); err != nil {
		t.Fatalf("AddEntry: %v", err)
	}

	// Try to import two entries: one duplicate, one new.
	// The duplicate has a slightly different URL (trailing slash differs) and
	// the same username — canonicalURL should normalise to the same host.
	parsed := []ParsedEntry{
		{URL: "https://github.com", Username: "alice", Password: "new-pass"},    // duplicate
		{URL: "https://github.com", Username: "bob", Password: "bob-pass"},      // different user → new
		{URL: "https://gitlab.com", Username: "alice", Password: "gitlab-pass"}, // different host → new
	}

	result, err := ImportEntries(v, parsed)
	if err != nil {
		t.Fatalf("ImportEntries: %v", err)
	}
	if result.Imported != 2 {
		t.Errorf("imported: want 2, got %d", result.Imported)
	}
	if result.Skipped != 1 {
		t.Errorf("skipped: want 1, got %d", result.Skipped)
	}

	entries, _ := v.ListEntries()
	// 1 existing + 2 new = 3
	if len(entries) != 3 {
		t.Errorf("total entries: want 3, got %d", len(entries))
	}

	// The existing entry's password must NOT have been overwritten.
	orig, err := v.GetEntry("existing-1")
	if err != nil {
		t.Fatalf("GetEntry: %v", err)
	}
	if orig.Password != "old-pass" {
		t.Errorf("existing entry password changed: got %q", orig.Password)
	}
}

func TestDeduplicationWithinBatch(t *testing.T) {
	// Two identical entries in the same import batch → only one should land.
	v := openTestVault(t)

	parsed := []ParsedEntry{
		{URL: "https://example.com", Username: "user", Password: "pass1"},
		{URL: "https://example.com", Username: "user", Password: "pass2"}, // exact dupe of above
	}

	result, err := ImportEntries(v, parsed)
	if err != nil {
		t.Fatalf("ImportEntries: %v", err)
	}
	if result.Imported != 1 {
		t.Errorf("imported: want 1, got %d", result.Imported)
	}
	if result.Skipped != 1 {
		t.Errorf("skipped: want 1, got %d", result.Skipped)
	}
}

// ---- CSV corner-case tests ----------------------------------------------

func TestParseCSVQuotedFields(t *testing.T) {
	// Fields with embedded commas and quotes.
	csv := []byte("name,url,username,password\n" +
		`"Site, Inc",https://site.example.com,"user,name","p""ass"` + "\n")

	parsed, err := ParseChromeCSV(csv)
	if err != nil {
		t.Fatalf("ParseChromeCSV: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("want 1 entry, got %d", len(parsed))
	}
	if parsed[0].Username != "user,name" {
		t.Errorf("username: %q", parsed[0].Username)
	}
	if parsed[0].Password != `p"ass` {
		t.Errorf("password: %q", parsed[0].Password)
	}
}

func TestParseCSVEmptyFile(t *testing.T) {
	parsed, err := ParseChromeCSV([]byte("name,url,username,password\n"))
	if err != nil {
		t.Fatalf("ParseChromeCSV: %v", err)
	}
	if len(parsed) != 0 {
		t.Errorf("want 0 entries for header-only file, got %d", len(parsed))
	}
}

// ---- Canonical URL test -------------------------------------------------

func TestCanonicalURL(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"https://GitHub.COM/login", "https://github.com"},
		{"https://github.com/", "https://github.com"},
		{"https://github.com", "https://github.com"},
		{"HTTP://EXAMPLE.COM/path?q=1#frag", "http://example.com"},
		{"", ""},
		{"not-a-url", "not-a-url"},
	}
	for _, c := range cases {
		got := canonicalURL(c.input)
		if got != c.want {
			t.Errorf("canonicalURL(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// ---- 1PIF fallback password field ---------------------------------------

func TestParse1PIFFallbackPassword(t *testing.T) {
	// Record where password is in the top-level "password" field, not in "fields".
	raw := []byte(`**begin 1Password data**
{"typeName":"webforms.WebForm","location":"https://fallback.example.com","secureContents":{"fields":[{"designation":"username","value":"frank"}],"URL":"","password":"fallback-pw"}}
**end 1Password data**`)

	parsed, err := Parse1PIF(raw)
	if err != nil {
		t.Fatalf("Parse1PIF: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("want 1 entry, got %d", len(parsed))
	}
	if parsed[0].Password != "fallback-pw" {
		t.Errorf("password: %q", parsed[0].Password)
	}
	if parsed[0].URL != "https://fallback.example.com" {
		t.Errorf("URL (fallback to location): %q", parsed[0].URL)
	}
}

// ---- Ensure temporary directories are cleaned up ------------------------

func TestMain(m *testing.M) {
	// os.TempDir cleanup is handled per-test via t.TempDir().
	os.Exit(m.Run())
}
