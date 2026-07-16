package main

// vault_mount_test.go — the authZ contract for the credential-vault and TOTP
// import/export endpoints.
//
// These four routes shipped as dead code: credvault.RegisterImportHandlers and
// authvault.RegisterMigrationHandlers were written and unit-tested but never
// called from main.go, so /api/auth/vault/import|export and
// /api/auth/totp/import|export did not exist at runtime. This file pins BOTH
// halves of the fix:
//
//  1. the routes exist (a mount regression here means no user can get their
//     passwords or 2FA seeds out of the box — the data-lock-in bug), and
//  2. they are gated exactly like the rest of the vault surface.
//
// Everything below runs against the REAL auth.Handler.Middleware, not a stub, so
// it exercises the actual identity path: the middleware DELETES any
// client-supplied X-User-ID and re-sets it from the validated session.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"vulos/backend/services/auth"
	"vulos/backend/services/authvault"
	"vulos/backend/services/credvault"
)

// ─── Harness ─────────────────────────────────────────────────────────────────

// vaultTestServer builds an httptest.Server that mounts the vault + TOTP routes
// EXACTLY as main.go does — including the import/export registrations — behind
// the real auth middleware.
func vaultTestServer(t *testing.T) (*httptest.Server, *auth.Store) {
	t.Helper()

	// authvault.storeFor resolves ~/.vulos/auth/totp/<uid> via os.UserHomeDir(),
	// so redirect HOME at the process level for the duration of the test.
	home := t.TempDir()
	t.Setenv("HOME", home)

	store, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth.NewStore: %v", err)
	}
	h := auth.NewHandler(store)

	mux := http.NewServeMux()

	// --- mirror of main.go ---
	totpHandler := authvault.NewHandler()
	totpHandler.RegisterHandlers(mux)
	totpHandler.SetReauthenticator(store.VerifyUserPassword)
	authvault.RegisterMigrationHandlers(mux, totpHandler)

	credVaultHandler := credvault.NewHandler(func(userID string) string {
		return filepath.Join(home, ".vulos", "auth", "vault", userID)
	})
	credVaultHandler.RegisterHandlers(mux)
	credvault.RegisterImportHandlers(mux, credVaultHandler)
	// --- end mirror ---

	srv := httptest.NewServer(h.Middleware(mux))
	t.Cleanup(srv.Close)
	return srv, store
}

// newPasswordUser registers a local user (with a bcrypt password, which the TOTP
// export step-up needs) and returns its id + a session token.
func newPasswordUser(t *testing.T, store *auth.Store, username, password string) (string, string) {
	t.Helper()
	u, err := store.Register(username, password, username)
	if err != nil {
		t.Fatalf("Register(%s): %v", username, err)
	}
	sess := store.CreateSession(u, "dev-"+username)
	return u.ID, sess.Token
}

// do issues a request. token=="" means NO session. hdr lets a test forge headers
// (that is the point of the cross-user tests).
func do(t *testing.T, srv *httptest.Server, method, path, token, body string, hdr map[string]string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

const testMaster = "correct-horse-battery-staple"

// unlockVault opens the caller's own vault over HTTP.
func unlockVault(t *testing.T, srv *httptest.Server, token string) {
	t.Helper()
	code, body := do(t, srv, "POST", "/api/auth/vault/unlock", token,
		fmt.Sprintf(`{"password":%q}`, testMaster), nil)
	if code != 200 {
		t.Fatalf("unlock: got %d %s", code, body)
	}
}

// addEntry creates one credential in the caller's vault.
func addEntry(t *testing.T, srv *httptest.Server, token, url, username, password string) {
	t.Helper()
	code, body := do(t, srv, "POST", "/api/auth/vault/entry", token,
		fmt.Sprintf(`{"url":%q,"username":%q,"password":%q}`, url, username, password), nil)
	if code != 201 {
		t.Fatalf("add entry: got %d %s", code, body)
	}
}

// ─── 1. The routes exist at all (the actual bug) ─────────────────────────────

// TestVaultAndTOTPImportExportAreMounted is the regression guard for the
// original defect: these handlers existed but nothing called Register*Handlers,
// so every one of these paths 404'd. A 404 here means the mount was lost again.
func TestVaultAndTOTPImportExportAreMounted(t *testing.T) {
	srv, store := vaultTestServer(t)
	_, token := newPasswordUser(t, store, "alice", "alice-account-pw")

	for _, path := range []string{
		"/api/auth/vault/import",
		"/api/auth/vault/export",
		"/api/auth/totp/import",
		"/api/auth/totp/export",
	} {
		code, body := do(t, srv, "POST", path, token, `{}`, nil)
		if code == 404 {
			t.Errorf("%s is NOT MOUNTED (404) — import/export is unreachable: %s", path, body)
		}
	}
}

// ─── 2. Unauthenticated access is refused ────────────────────────────────────

// TestVaultImportExportRequireSession — no session, no vault. MUTATION-CHECKED.
func TestVaultImportExportRequireSession(t *testing.T) {
	srv, _ := vaultTestServer(t)

	cases := []struct{ path, body string }{
		{"/api/auth/vault/import", `{"format":"chrome-csv","data":"eA=="}`},
		{"/api/auth/vault/export", `{"master_password":"x","password":"longenough"}`},
		{"/api/auth/totp/import", `{"uri":"otpauth-migration://offline?data=AA=="}`},
		{"/api/auth/totp/export", `{"account_password":"x","passphrase":"longenough"}`},
	}
	for _, c := range cases {
		code, body := do(t, srv, "POST", c.path, "", c.body, nil)
		if code != 401 {
			t.Errorf("unauthenticated %s: got %d %s, want 401", c.path, code, body)
		}
	}
}

// TestForgedUserHeaderAloneIsNotAuth — a bare X-User-ID header confers NOTHING.
// The middleware deletes it before routing; with no session the request is 401.
//
// These probes are deliberately IDENTITY-ONLY reads (no step-up, no unlock, no
// body validation in front of them): if the forged header were honoured they
// would return the victim's data. Anything that 401s for some *other* reason
// would make this test pass without testing anything.
func TestForgedUserHeaderAloneIsNotAuth(t *testing.T) {
	srv, store := vaultTestServer(t)
	victimID, victimTok := newPasswordUser(t, store, "victim", "victim-account-pw")

	// Give the victim something worth stealing.
	code, body := do(t, srv, "POST", "/api/auth/totp/add", victimTok,
		`{"service":"VictimBank","issuer":"VictimBank","account":"v@example.com","secret":"JBSWY3DPEHPK3PXP"}`, nil)
	if code != 200 {
		t.Fatalf("victim totp add: %d %s", code, body)
	}

	forged := map[string]string{"X-User-ID": victimID}
	// GET /api/auth/totp/list  — would 200 with the victim's accounts.
	// GET /api/auth/vault/entries — would 423 (locked vault) rather than 401.
	for _, path := range []string{
		"/api/auth/totp/list",
		"/api/auth/vault/entries",
	} {
		code, body := do(t, srv, "GET", path, "", "", forged)
		if code != 401 {
			t.Errorf("forged X-User-ID on %s with NO session: got %d %s, want 401 — "+
				"the header must never confer identity", path, code, body)
		}
		if strings.Contains(body, "VictimBank") {
			t.Fatalf("forged X-User-ID on %s returned the victim's data: %s", path, body)
		}
	}
}

// ─── 3. Cross-user access is refused ─────────────────────────────────────────

// TestVaultExportCannotReachAnotherUsersVault is the IDOR proof. MUTATION-CHECKED.
//
// Alice holds a valid session. She ALSO forges X-User-ID: <bob> on the request.
// The export she gets back must be HER vault — the forged header must be stripped
// and the identity re-derived from her session. If Bob's credential ever appears
// in Alice's export, one user can read another user's passwords.
func TestVaultExportCannotReachAnotherUsersVault(t *testing.T) {
	srv, store := vaultTestServer(t)
	aliceID, aliceTok := newPasswordUser(t, store, "alice", "alice-account-pw")
	bobID, bobTok := newPasswordUser(t, store, "bob", "bob-account-pw")

	// Each user unlocks their own vault and stores one distinctive secret.
	unlockVault(t, srv, aliceTok)
	addEntry(t, srv, aliceTok, "https://alice.example", "alice", "ALICE-SECRET-PW")
	unlockVault(t, srv, bobTok)
	addEntry(t, srv, bobTok, "https://bob.example", "bob", "BOB-SECRET-PW")

	// Alice exports while forging Bob's id.
	code, body := do(t, srv, "POST", "/api/auth/vault/export", aliceTok,
		fmt.Sprintf(`{"master_password":%q,"password":"export-passphrase"}`, testMaster),
		map[string]string{"X-User-ID": bobID})
	if code != 200 {
		t.Fatalf("alice export: got %d %s", code, body)
	}

	var out struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(out.Data)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}

	// Decrypt into a scratch vault to read what actually came out.
	scratch, err := credvault.New(t.TempDir())
	if err != nil {
		t.Fatalf("scratch vault: %v", err)
	}
	if err := scratch.Unlock("scratch-master-password"); err != nil {
		t.Fatalf("scratch unlock: %v", err)
	}
	if _, err := credvault.ImportEncrypted(scratch, raw, "export-passphrase"); err != nil {
		t.Fatalf("decrypt alice's export: %v", err)
	}
	entries, err := scratch.ListEntries()
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	var sawAlice, sawBob bool
	for _, e := range entries {
		if e.Password == "ALICE-SECRET-PW" {
			sawAlice = true
		}
		if e.Password == "BOB-SECRET-PW" {
			sawBob = true
		}
	}
	if sawBob {
		t.Fatalf("CROSS-USER LEAK: alice's export (id forged to bob=%s) contains BOB's password", bobID)
	}
	if !sawAlice {
		t.Fatalf("alice's export does not contain her own entry (aliceID=%s) — identity is wrong", aliceID)
	}
}

// TestTOTPExportCannotReachAnotherUsersSeeds is the same IDOR proof for 2FA seeds.
func TestTOTPExportCannotReachAnotherUsersSeeds(t *testing.T) {
	srv, store := vaultTestServer(t)
	_, aliceTok := newPasswordUser(t, store, "alice", "alice-account-pw")
	bobID, bobTok := newPasswordUser(t, store, "bob", "bob-account-pw")

	// Bob stores a TOTP seed. Alice stores none.
	code, body := do(t, srv, "POST", "/api/auth/totp/add", bobTok,
		`{"service":"BobBank","issuer":"BobBank","account":"bob@example.com","secret":"JBSWY3DPEHPK3PXP"}`, nil)
	if code != 200 {
		t.Fatalf("bob totp add: got %d %s", code, body)
	}

	// Alice exports, forging Bob's id.
	code, body = do(t, srv, "POST", "/api/auth/totp/export", aliceTok,
		`{"account_password":"alice-account-pw","passphrase":"export-passphrase"}`,
		map[string]string{"X-User-ID": bobID})
	if code != 200 {
		t.Fatalf("alice totp export: got %d %s", code, body)
	}

	var blob struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(body), &blob); err != nil {
		t.Fatalf("decode blob: %v", err)
	}
	if blob.Count != 0 {
		t.Fatalf("CROSS-USER LEAK: alice's TOTP export contains %d seed(s); she has none — she got Bob's", blob.Count)
	}
}

// ─── 4. Step-up: a session alone must not dump every secret ──────────────────

// TestVaultExportRequiresMasterPasswordStepUp — an unlocked vault + a valid
// session is NOT enough. This is the XSS/CSRF case: the attacker has the cookie
// and the vault is open, but cannot produce the master password.
func TestVaultExportRequiresMasterPasswordStepUp(t *testing.T) {
	srv, store := vaultTestServer(t)
	_, tok := newPasswordUser(t, store, "alice", "alice-account-pw")

	unlockVault(t, srv, tok)
	addEntry(t, srv, tok, "https://alice.example", "alice", "ALICE-SECRET-PW")

	// No master password at all → refused even though the vault is unlocked.
	code, body := do(t, srv, "POST", "/api/auth/vault/export", tok,
		`{"password":"export-passphrase"}`, nil)
	if code != 401 {
		t.Errorf("export with no step-up: got %d %s, want 401", code, body)
	}
	if strings.Contains(body, "ALICE-SECRET-PW") {
		t.Fatal("refused export leaked vault contents in the error body")
	}

	// Wrong master password → refused.
	code, body = do(t, srv, "POST", "/api/auth/vault/export", tok,
		`{"master_password":"not-the-master","password":"export-passphrase"}`, nil)
	if code != 401 {
		t.Errorf("export with wrong master password: got %d %s, want 401", code, body)
	}

	// Correct master password → allowed.
	code, body = do(t, srv, "POST", "/api/auth/vault/export", tok,
		fmt.Sprintf(`{"master_password":%q,"password":"export-passphrase"}`, testMaster), nil)
	if code != 200 {
		t.Fatalf("export with correct step-up: got %d %s, want 200", code, body)
	}
}

// TestTOTPExportRequiresAccountPasswordStepUp — same rule for 2FA seeds.
func TestTOTPExportRequiresAccountPasswordStepUp(t *testing.T) {
	srv, store := vaultTestServer(t)
	_, tok := newPasswordUser(t, store, "alice", "alice-account-pw")

	code, body := do(t, srv, "POST", "/api/auth/totp/export", tok,
		`{"passphrase":"export-passphrase"}`, nil)
	if code != 401 {
		t.Errorf("totp export with no step-up: got %d %s, want 401", code, body)
	}

	code, body = do(t, srv, "POST", "/api/auth/totp/export", tok,
		`{"account_password":"wrong-password","passphrase":"export-passphrase"}`, nil)
	if code != 401 {
		t.Errorf("totp export with wrong account password: got %d %s, want 401", code, body)
	}

	code, body = do(t, srv, "POST", "/api/auth/totp/export", tok,
		`{"account_password":"alice-account-pw","passphrase":"export-passphrase"}`, nil)
	if code != 200 {
		t.Fatalf("totp export with correct step-up: got %d %s, want 200", code, body)
	}
}

// TestExportIsNotCacheable — the export body is the whole vault. It must never
// be written to a browser/proxy cache.
func TestExportIsNotCacheable(t *testing.T) {
	srv, store := vaultTestServer(t)
	_, tok := newPasswordUser(t, store, "alice", "alice-account-pw")
	unlockVault(t, srv, tok)

	req, _ := http.NewRequest("POST", srv.URL+"/api/auth/vault/export",
		strings.NewReader(fmt.Sprintf(`{"master_password":%q,"password":"export-passphrase"}`, testMaster)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	defer res.Body.Close()

	cc := res.Header.Get("Cache-Control")
	if !strings.Contains(cc, "no-store") {
		t.Errorf("export Cache-Control = %q, want it to contain no-store", cc)
	}
}
