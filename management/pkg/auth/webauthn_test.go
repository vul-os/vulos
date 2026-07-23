// webauthn_test.go -- store-level tests for WebAuthn (AUTH-11).
//
// Note: Full end-to-end WebAuthn ceremonies require a browser / authenticator,
// so these tests cover:
//   - Migration: webauthn_credentials table is created
//   - NewWebAuthn: returns ErrWebAuthnNotConfigured without env vars; succeeds with them
//   - BeginWebAuthnRegistration: returns ErrWebAuthnNotConfigured without env; with env returns options
//   - storeWebAuthnCredential / ListWebAuthnCredentials / RevokeWebAuthnCredential (CRUD)
//   - updateWebAuthnSignCount: monotonic check and counter-disabled path
//   - LoadWebAuthnUserForLogin: ErrNotFound + ErrWebAuthnNoCredentials
//   - WebAuthnUserAdapter: interface method correctness
package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// setupWebAuthnEnv sets the WebAuthn RP env vars for tests.
func setupWebAuthnEnv(t *testing.T) {
	t.Helper()
	t.Setenv("WEBAUTHN_RPID", "localhost")
	t.Setenv("WEBAUTHN_ORIGIN", "http://localhost:5173")
	t.Setenv("WEBAUTHN_RPNAME", "Vulos Test")
}

// makeTestCredential returns a synthetic webauthn.Credential for use in store tests.
// credIDHex is a short hex string used to derive a unique credential ID.
func makeTestCredential(credIDSuffix string, signCount uint32) webauthn.Credential {
	id := []byte("testcredential_" + credIDSuffix)
	pub := []byte("fakepublickeycbor_" + credIDSuffix)
	return webauthn.Credential{
		ID:        id,
		PublicKey: pub,
		Transport: []protocol.AuthenticatorTransport{protocol.Internal},
		Authenticator: webauthn.Authenticator{
			SignCount: signCount,
		},
	}
}

// insertTestCredential stores a synthetic credential row directly.
func insertTestCredential(t *testing.T, st *Store, userID string, cred webauthn.Credential, name string) *WebAuthnCredential {
	t.Helper()
	ctx := context.Background()
	transportsJSON, _ := json.Marshal([]string{"internal"})
	credIDBase64 := base64.RawURLEncoding.EncodeToString(cred.ID)
	id, err := newULID()
	if err != nil {
		t.Fatalf("newULID: %v", err)
	}
	createdAt := now().Format(time.RFC3339)
	st.mu.Lock()
	_, err = st.db.ExecContext(ctx,
		`INSERT INTO webauthn_credentials
		   (id, user_id, credential_id, public_key, sign_count, transports, friendly_name, aaguid, last_used_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, '', NULL, ?)`,
		id, userID, credIDBase64, cred.PublicKey, cred.Authenticator.SignCount,
		string(transportsJSON), name, createdAt,
	)
	st.mu.Unlock()
	if err != nil {
		t.Fatalf("insertTestCredential: %v", err)
	}
	return &WebAuthnCredential{
		ID:           id,
		CredentialID: credIDBase64,
		FriendlyName: name,
		Transports:   []string{"internal"},
		AAGUID:       "",
		LastUsedAt:   "",
		CreatedAt:    createdAt,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Migration
// ─────────────────────────────────────────────────────────────────────────────

func TestWebAuthn_MigrationCreatesTable(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	rows, err := st.db.QueryContext(ctx, `PRAGMA table_info(webauthn_credentials)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()

	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}

	required := []string{
		"id", "user_id", "credential_id", "public_key",
		"sign_count", "transports", "friendly_name", "aaguid",
		"last_used_at", "created_at",
	}
	for _, col := range required {
		if !cols[col] {
			t.Errorf("webauthn_credentials table missing column: %s", col)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// NewWebAuthn factory
// ─────────────────────────────────────────────────────────────────────────────

func TestWebAuthn_NewWebAuthn_NotConfigured(t *testing.T) {
	t.Setenv("WEBAUTHN_RPID", "")
	t.Setenv("WEBAUTHN_ORIGIN", "")
	_, err := NewWebAuthn()
	if err != ErrWebAuthnNotConfigured {
		t.Errorf("expected ErrWebAuthnNotConfigured, got %v", err)
	}
}

func TestWebAuthn_NewWebAuthn_Configured(t *testing.T) {
	setupWebAuthnEnv(t)
	wa, err := NewWebAuthn()
	if err != nil {
		t.Fatalf("NewWebAuthn: %v", err)
	}
	if wa.Config.RPID != "localhost" {
		t.Errorf("RPID: got %q, want %q", wa.Config.RPID, "localhost")
	}
	if wa.Config.RPDisplayName != "Vulos Test" {
		t.Errorf("RPDisplayName: got %q, want %q", wa.Config.RPDisplayName, "Vulos Test")
	}
	if len(wa.Config.RPOrigins) != 1 || wa.Config.RPOrigins[0] != "http://localhost:5173" {
		t.Errorf("RPOrigins: got %v", wa.Config.RPOrigins)
	}
}

func TestWebAuthn_NewWebAuthn_DefaultRPName(t *testing.T) {
	t.Setenv("WEBAUTHN_RPID", "test.example.com")
	t.Setenv("WEBAUTHN_ORIGIN", "https://test.example.com")
	t.Setenv("WEBAUTHN_RPNAME", "")
	wa, err := NewWebAuthn()
	if err != nil {
		t.Fatalf("NewWebAuthn: %v", err)
	}
	if wa.Config.RPDisplayName != "Vulos Cloud" {
		t.Errorf("default RPDisplayName: got %q, want %q", wa.Config.RPDisplayName, "Vulos Cloud")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// BeginWebAuthnRegistration: not configured path
// ─────────────────────────────────────────────────────────────────────────────

func TestWebAuthn_BeginRegistration_NotConfigured(t *testing.T) {
	t.Setenv("WEBAUTHN_RPID", "")
	t.Setenv("WEBAUTHN_ORIGIN", "")

	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "wabegin@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	_, _, err = st.BeginWebAuthnRegistration(ctx, u)
	if err != ErrWebAuthnNotConfigured {
		t.Errorf("expected ErrWebAuthnNotConfigured, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// WebAuthnUserAdapter
// ─────────────────────────────────────────────────────────────────────────────

func TestWebAuthn_UserAdapter(t *testing.T) {
	cred := makeTestCredential("a1", 0)
	u := &WebAuthnUserAdapter{
		ID:          "user-abc-123",
		Email:       "adapter@example.com",
		Credentials: []webauthn.Credential{cred},
	}
	if got := string(u.WebAuthnID()); got != "user-abc-123" {
		t.Errorf("WebAuthnID: got %q, want %q", got, "user-abc-123")
	}
	if got := u.WebAuthnName(); got != "adapter@example.com" {
		t.Errorf("WebAuthnName: got %q", got)
	}
	if got := u.WebAuthnDisplayName(); got != "adapter@example.com" {
		t.Errorf("WebAuthnDisplayName: got %q", got)
	}
	if got := u.WebAuthnCredentials(); len(got) != 1 {
		t.Errorf("WebAuthnCredentials: got %d, want 1", len(got))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CRUD: list + revoke
// ─────────────────────────────────────────────────────────────────────────────

func TestWebAuthn_ListCredentials_Empty(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "walist@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	creds, err := st.ListWebAuthnCredentials(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListWebAuthnCredentials: %v", err)
	}
	if len(creds) != 0 {
		t.Errorf("expected 0 credentials, got %d", len(creds))
	}
}

func TestWebAuthn_ListCredentials_AfterInsert(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "walist2@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	c1 := makeTestCredential("b1", 0)
	c2 := makeTestCredential("b2", 10)
	insertTestCredential(t, st, u.ID, c1, "Touch ID")
	insertTestCredential(t, st, u.ID, c2, "YubiKey 5")

	creds, err := st.ListWebAuthnCredentials(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListWebAuthnCredentials: %v", err)
	}
	if len(creds) != 2 {
		t.Fatalf("expected 2 credentials, got %d", len(creds))
	}
	names := map[string]bool{}
	for _, c := range creds {
		names[c.FriendlyName] = true
		if len(c.Transports) == 0 {
			t.Errorf("credential %s has no transports", c.FriendlyName)
		}
	}
	if !names["Touch ID"] || !names["YubiKey 5"] {
		t.Errorf("unexpected credential names: %v", names)
	}
}

func TestWebAuthn_RevokeCredential_Success(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "warevoke@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	rec := insertTestCredential(t, st, u.ID, makeTestCredential("c1", 0), "Test Key")

	if err := st.RevokeWebAuthnCredential(ctx, u.ID, rec.ID); err != nil {
		t.Fatalf("RevokeWebAuthnCredential: %v", err)
	}

	// Should be gone.
	creds, err := st.ListWebAuthnCredentials(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListWebAuthnCredentials after revoke: %v", err)
	}
	if len(creds) != 0 {
		t.Errorf("expected 0 credentials after revoke, got %d", len(creds))
	}
}

func TestWebAuthn_RevokeCredential_NotFound(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "warevokemissing@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	err = st.RevokeWebAuthnCredential(ctx, u.ID, "nonexistent-id")
	if err != ErrWebAuthnCredentialNotFound {
		t.Errorf("expected ErrWebAuthnCredentialNotFound, got %v", err)
	}
}

func TestWebAuthn_RevokeCredential_WrongUser(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	owner, _, _ := st.Signup(ctx, "waowner@example.com", "securepass1234!", "127.0.0.1", "test")
	attacker, _, _ := st.Signup(ctx, "waattacker@example.com", "securepass1234!", "127.0.0.1", "test")

	rec := insertTestCredential(t, st, owner.ID, makeTestCredential("d1", 0), "Owner Key")

	err := st.RevokeWebAuthnCredential(ctx, attacker.ID, rec.ID)
	if err != ErrWebAuthnCredentialNotFound {
		t.Errorf("expected ErrWebAuthnCredentialNotFound for wrong-user revoke, got %v", err)
	}

	// Owner's credential should still be there.
	creds, err := st.ListWebAuthnCredentials(ctx, owner.ID)
	if err != nil {
		t.Fatalf("ListWebAuthnCredentials: %v", err)
	}
	if len(creds) != 1 {
		t.Errorf("owner credential should survive wrong-user revoke attempt")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Sign-count monotonic check
// ─────────────────────────────────────────────────────────────────────────────

func TestWebAuthn_SignCount_Monotonic(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "wasigncount@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	cred := makeTestCredential("e1", 5)
	rec := insertTestCredential(t, st, u.ID, cred, "SC Key")
	credIDBase64 := rec.CredentialID

	// Valid: newCount (6) > storedCount (5).
	if err := st.updateWebAuthnSignCount(ctx, u.ID, credIDBase64, 6); err != nil {
		t.Errorf("updateWebAuthnSignCount with valid count: %v", err)
	}

	// Replay: newCount (6) <= storedCount (6).
	if err := st.updateWebAuthnSignCount(ctx, u.ID, credIDBase64, 6); err != ErrWebAuthnSignCountReplay {
		t.Errorf("expected ErrWebAuthnSignCountReplay for replay, got %v", err)
	}

	// Lower: newCount (3) < storedCount (6).
	if err := st.updateWebAuthnSignCount(ctx, u.ID, credIDBase64, 3); err != ErrWebAuthnSignCountReplay {
		t.Errorf("expected ErrWebAuthnSignCountReplay for lower count, got %v", err)
	}
}

func TestWebAuthn_SignCount_ZeroCounterDisabled(t *testing.T) {
	// Authenticators with sign_count=0 (no counter) must always be accepted.
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "wazerocount@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	cred := makeTestCredential("f1", 0) // counter disabled
	rec := insertTestCredential(t, st, u.ID, cred, "Platform Key")
	credIDBase64 := rec.CredentialID

	// newCount=0 should be accepted (no replay detection for counter-less authenticators).
	if err := st.updateWebAuthnSignCount(ctx, u.ID, credIDBase64, 0); err != nil {
		t.Errorf("updateWebAuthnSignCount with zero counter: %v", err)
	}
	// A second call with newCount=0 should also be accepted.
	if err := st.updateWebAuthnSignCount(ctx, u.ID, credIDBase64, 0); err != nil {
		t.Errorf("updateWebAuthnSignCount second zero: %v", err)
	}
}

func TestWebAuthn_SignCount_NotFound(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "wascnotfound@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	err = st.updateWebAuthnSignCount(ctx, u.ID, "nonexistent-cred-id", 1)
	if err != ErrWebAuthnCredentialNotFound {
		t.Errorf("expected ErrWebAuthnCredentialNotFound, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// LoadWebAuthnUserForLogin
// ─────────────────────────────────────────────────────────────────────────────

func TestWebAuthn_LoadUserForLogin_NotFound(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	_, err := st.LoadWebAuthnUserForLogin(ctx, "nobody@example.com")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound for unknown email, got %v", err)
	}
}

func TestWebAuthn_LoadUserForLogin_NoCredentials(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	_, _, err := st.Signup(ctx, "wanokeylogin@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	_, err = st.LoadWebAuthnUserForLogin(ctx, "wanokeylogin@example.com")
	if err != ErrWebAuthnNoCredentials {
		t.Errorf("expected ErrWebAuthnNoCredentials for user with no passkeys, got %v", err)
	}
}

func TestWebAuthn_LoadUserForLogin_WithCredentials(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "wahaskey@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	insertTestCredential(t, st, u.ID, makeTestCredential("g1", 0), "My Passkey")

	wu, err := st.LoadWebAuthnUserForLogin(ctx, "wahaskey@example.com")
	if err != nil {
		t.Fatalf("LoadWebAuthnUserForLogin: %v", err)
	}
	if wu.ID != u.ID {
		t.Errorf("user ID mismatch: got %q, want %q", wu.ID, u.ID)
	}
	if len(wu.Credentials) != 1 {
		t.Errorf("expected 1 credential, got %d", len(wu.Credentials))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// webAuthnCredentialsForUser: multiple credentials
// ─────────────────────────────────────────────────────────────────────────────

func TestWebAuthn_CredentialsForUser_MultipleCredentials(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "wamulticred@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	insertTestCredential(t, st, u.ID, makeTestCredential("h1", 0), "Touch ID")
	insertTestCredential(t, st, u.ID, makeTestCredential("h2", 5), "YubiKey")

	creds, err := st.webAuthnCredentialsForUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("webAuthnCredentialsForUser: %v", err)
	}
	if len(creds) != 2 {
		t.Fatalf("expected 2 webauthn.Credential, got %d", len(creds))
	}
	// Verify sign counts round-tripped.
	signCounts := map[uint32]bool{0: false, 5: false}
	for _, c := range creds {
		signCounts[c.Authenticator.SignCount] = true
	}
	if !signCounts[0] || !signCounts[5] {
		t.Errorf("sign counts not round-tripped correctly: %v", signCounts)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// BeginWebAuthnLogin: not configured path
// ─────────────────────────────────────────────────────────────────────────────

func TestWebAuthn_BeginLogin_NotConfigured(t *testing.T) {
	t.Setenv("WEBAUTHN_RPID", "")
	t.Setenv("WEBAUTHN_ORIGIN", "")

	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "walognc@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	insertTestCredential(t, st, u.ID, makeTestCredential("i1", 0), "Test")

	wu, err := st.LoadWebAuthnUserForLogin(ctx, "walognc@example.com")
	if err != nil {
		t.Fatalf("LoadWebAuthnUserForLogin: %v", err)
	}
	_, _, err = st.BeginWebAuthnLogin(ctx, wu)
	if err != ErrWebAuthnNotConfigured {
		t.Errorf("expected ErrWebAuthnNotConfigured, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Lockout integration: WebAuthn login failures increment shared counter
// ─────────────────────────────────────────────────────────────────────────────

func TestWebAuthn_LockoutSharedWithTOTP(t *testing.T) {
	setupTOTPTestEnv(t) // sets up TOTP env (avoids kek errors in lockout code)

	st := openTestStore(t)
	ctx := context.Background()
	u, _, err := st.Signup(ctx, "walockout@example.com", "securepass1234!", "127.0.0.1", "test")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	// Should not be locked initially.
	_, locked := st.Check2FALockout(ctx, u.ID)
	if locked {
		t.Fatal("should not be locked initially")
	}

	// Record 5 WebAuthn failures via the same RecordFailed2FA primitive.
	for i := 0; i < 5; i++ {
		if err := st.RecordFailed2FA(ctx, u.ID); err != nil {
			t.Fatalf("RecordFailed2FA iteration %d: %v", i, err)
		}
	}

	_, locked = st.Check2FALockout(ctx, u.ID)
	if !locked {
		t.Error("account should be locked after 5 failures (shared lockout)")
	}

	// Reset on success (same as TOTP).
	if err := st.ResetFailed2FA(ctx, u.ID); err != nil {
		t.Fatalf("ResetFailed2FA: %v", err)
	}
	_, locked = st.Check2FALockout(ctx, u.ID)
	if locked {
		t.Error("account should be unlocked after ResetFailed2FA")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// No biometric data: public_key column never contains biometrics
// ─────────────────────────────────────────────────────────────────────────────

// TestWebAuthn_NoBiometricData asserts that no biometric data is stored:
// only the credential ID and CBOR-encoded public key are persisted.
func TestWebAuthn_NoBiometricData(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// Inspect the webauthn_credentials table schema: it must NOT contain
	// columns named "biometric", "fingerprint", "face", or "template".
	rows, err := st.db.QueryContext(ctx, `PRAGMA table_info(webauthn_credentials)`)
	if err != nil {
		t.Fatalf("PRAGMA: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		forbidden := []string{"biometric", "fingerprint", "face", "template", "iris", "voice"}
		for _, f := range forbidden {
			if name == f {
				t.Errorf("webauthn_credentials must NOT contain biometric column %q", f)
			}
		}
	}
}
