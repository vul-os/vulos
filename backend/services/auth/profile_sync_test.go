package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"vulos/backend/services/signing"
)

// ─── Test helpers ─────────────────────────────────────────────────────────────

// makeTestSyncer creates a ProfileSyncer backed by a fresh test store with
// the trust-anchor written to a temp file.
// It returns the syncer, the corresponding Ed25519 private key (for signing
// test envelopes), and a cleanup function.
func makeTestSyncer(t *testing.T) (*ProfileSyncer, ed25519.PrivateKey) {
	t.Helper()

	store := makeTestStore(t)

	// Generate a fresh key pair; write the public key as the trust anchor.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}

	anchorPath := writeTestAnchor(t, pub)
	auditPath := filepath.Join(t.TempDir(), "profile-sync.log")

	syncer := NewProfileSyncer(store, anchorPath, auditPath)
	return syncer, priv
}

// makeTestSyncerWithStore creates a ProfileSyncer backed by a provided store,
// useful when the test needs to pre-populate the store.
func makeTestSyncerWithStore(t *testing.T, store *Store) (*ProfileSyncer, ed25519.PrivateKey) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}

	anchorPath := writeTestAnchor(t, pub)
	auditPath := filepath.Join(t.TempDir(), "profile-sync.log")

	syncer := NewProfileSyncer(store, anchorPath, auditPath)
	return syncer, priv
}

// writeTestAnchor persists pub as a base64-encoded trust anchor file and
// returns its path.  The format matches signing.LoadAnchor expectations.
func writeTestAnchor(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "trust-anchor.pub")
	b64 := base64.StdEncoding.EncodeToString(pub)
	if err := os.WriteFile(path, []byte(b64+"\n"), 0600); err != nil {
		t.Fatalf("writeTestAnchor: %v", err)
	}
	return path
}

// signEnvelope signs payload and returns a ProfileUpdateEnvelope.
func signEnvelope(t *testing.T, priv ed25519.PrivateKey, payload ProfileUpdatePayload) ProfileUpdateEnvelope {
	t.Helper()
	canon, err := signing.Canonical(payload)
	if err != nil {
		t.Fatalf("signing.Canonical: %v", err)
	}
	sig := signing.Sign(priv, canon)
	return ProfileUpdateEnvelope{
		PayloadBytes: canon,
		Signature:    sig,
	}
}

// registerCloudUser adds a user linked to a cloud AccountID to store and
// returns that user.
func registerCloudUser(t *testing.T, store *Store, accountID, username, name string) *User {
	t.Helper()
	u := store.FindOrCreateUser("cloud", accountID, username+"@example.com", name, "")
	return u
}

// freshPayload returns a valid, non-expired ProfileUpdatePayload.
func freshPayload(accountID string) ProfileUpdatePayload {
	return ProfileUpdatePayload{
		AccountID: accountID,
		IssuedAt:  time.Now().UTC().Add(-5 * time.Minute),
		ExpiresAt: time.Now().UTC().Add(30 * time.Minute),
	}
}

// readAuditRecords parses all JSON-line audit records from auditLog.
func readAuditRecords(t *testing.T, path string) []ProfileSyncAuditRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("readAuditRecords: %v", err)
	}
	var records []ProfileSyncAuditRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	for dec.More() {
		var r ProfileSyncAuditRecord
		if err := dec.Decode(&r); err != nil {
			t.Fatalf("decode audit record: %v", err)
		}
		records = append(records, r)
	}
	return records
}

// ─── Tests ────────────────────────────────────────────────────────────────────

// TestApply_UpdateUsername verifies that a signed username update is applied
// and audited correctly.
func TestApply_UpdateUsername(t *testing.T) {
	store := makeTestStore(t)
	syncer, priv := makeTestSyncerWithStore(t, store)

	const accountID = "01HZACC001"
	registerCloudUser(t, store, accountID, "alice", "Alice One")

	payload := freshPayload(accountID)
	payload.Username = "alicenew"
	env := signEnvelope(t, priv, payload)

	if err := syncer.Apply(env); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Verify the username was updated in the store.
	found := false
	store.mu.RLock()
	for _, u := range store.users {
		if u.Providers["cloud"] == accountID {
			if u.Username != "alicenew" {
				t.Errorf("username: got %q, want %q", u.Username, "alicenew")
			}
			found = true
		}
	}
	store.mu.RUnlock()
	if !found {
		t.Fatal("user not found after apply")
	}

	// Verify the audit record.
	records := readAuditRecords(t, syncer.auditLog)
	if len(records) != 1 {
		t.Fatalf("expected 1 audit record, got %d", len(records))
	}
	if records[0].Field != "username" {
		t.Errorf("audit field: got %q, want %q", records[0].Field, "username")
	}
	if records[0].NewValue != "alicenew" {
		t.Errorf("audit new: got %q, want %q", records[0].NewValue, "alicenew")
	}
	if records[0].AccountID != accountID {
		t.Errorf("audit account_id: got %q, want %q", records[0].AccountID, accountID)
	}
}

// TestApply_UpdatePassword verifies that a password update is applied and
// that no plain-text password appears in the audit log.
func TestApply_UpdatePassword(t *testing.T) {
	store := makeTestStore(t)
	syncer, priv := makeTestSyncerWithStore(t, store)

	const accountID = "01HZACC002"
	registerCloudUser(t, store, accountID, "bob", "Bob Two")

	payload := freshPayload(accountID)
	payload.Password = "supersecret123"
	env := signEnvelope(t, priv, payload)

	if err := syncer.Apply(env); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Verify the password was stored (hashed form, not plain).
	store.mu.RLock()
	var storedHash string
	for _, u := range store.users {
		if u.Providers["cloud"] == accountID {
			storedHash = u.PasswordHash
		}
	}
	store.mu.RUnlock()

	if storedHash == "" {
		t.Fatal("password hash is empty after update")
	}
	if storedHash == "supersecret123" {
		t.Fatal("password was stored in plain text — must be hashed")
	}
	if !verifyPassword(storedHash, "supersecret123") {
		t.Error("stored hash does not verify the applied password")
	}

	// Audit record must not contain the plain password.
	records := readAuditRecords(t, syncer.auditLog)
	if len(records) != 1 {
		t.Fatalf("expected 1 audit record, got %d", len(records))
	}
	if records[0].Field != "password" {
		t.Errorf("audit field: got %q, want %q", records[0].Field, "password")
	}
	if records[0].OldValue != "" || records[0].NewValue != "" {
		t.Errorf("password audit record must not contain old/new values; got old=%q new=%q",
			records[0].OldValue, records[0].NewValue)
	}
}

// TestApply_UpdateFullName verifies that a full-name update is reflected in
// both User.Name and Profile.DisplayName.
func TestApply_UpdateFullName(t *testing.T) {
	store := makeTestStore(t)
	syncer, priv := makeTestSyncerWithStore(t, store)

	const accountID = "01HZACC003"
	user := registerCloudUser(t, store, accountID, "charlie", "Charlie Old")

	// Ensure the profile exists with the old display name.
	if _, ok := store.GetProfile(user.ID); !ok {
		t.Fatal("profile not found after FindOrCreateUser")
	}

	payload := freshPayload(accountID)
	payload.FullName = "Charlie Updated"
	env := signEnvelope(t, priv, payload)

	if err := syncer.Apply(env); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Verify User.Name.
	store.mu.RLock()
	var newName string
	for _, u := range store.users {
		if u.Providers["cloud"] == accountID {
			newName = u.Name
		}
	}
	store.mu.RUnlock()
	if newName != "Charlie Updated" {
		t.Errorf("User.Name: got %q, want %q", newName, "Charlie Updated")
	}

	// Verify Profile.DisplayName.
	p, ok := store.GetProfile(user.ID)
	if !ok {
		t.Fatal("profile not found after apply")
	}
	if p.DisplayName != "Charlie Updated" {
		t.Errorf("Profile.DisplayName: got %q, want %q", p.DisplayName, "Charlie Updated")
	}

	// Audit record.
	records := readAuditRecords(t, syncer.auditLog)
	if len(records) != 1 {
		t.Fatalf("expected 1 audit record, got %d", len(records))
	}
	if records[0].Field != "full_name" {
		t.Errorf("audit field: got %q, want %q", records[0].Field, "full_name")
	}
}

// TestApply_MultipleFields verifies that applying username + password +
// full_name in a single envelope produces three audit records.
func TestApply_MultipleFields(t *testing.T) {
	store := makeTestStore(t)
	syncer, priv := makeTestSyncerWithStore(t, store)

	const accountID = "01HZACC004"
	registerCloudUser(t, store, accountID, "dana", "Dana Four")

	payload := freshPayload(accountID)
	payload.Username = "dananew"
	payload.Password = "newpass456"
	payload.FullName = "Dana Updated"
	payload.CloudManaged = true
	env := signEnvelope(t, priv, payload)

	if err := syncer.Apply(env); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	records := readAuditRecords(t, syncer.auditLog)
	if len(records) != 3 {
		t.Fatalf("expected 3 audit records, got %d", len(records))
	}
	fields := map[string]bool{}
	for _, r := range records {
		fields[r.Field] = true
		if !r.CloudManaged {
			t.Errorf("cloud_managed should be true in all records")
		}
	}
	for _, want := range []string{"username", "password", "full_name"} {
		if !fields[want] {
			t.Errorf("missing audit record for field %q", want)
		}
	}
}

// TestApply_BadSignature verifies fail-closed behaviour on a tampered payload.
func TestApply_BadSignature(t *testing.T) {
	store := makeTestStore(t)
	syncer, priv := makeTestSyncerWithStore(t, store)

	const accountID = "01HZACC005"
	registerCloudUser(t, store, accountID, "eve", "Eve Five")

	payload := freshPayload(accountID)
	payload.Username = "evehacked"
	env := signEnvelope(t, priv, payload)

	// Tamper the payload after signing.
	env.PayloadBytes[0] ^= 0xFF

	err := syncer.Apply(env)
	if err == nil {
		t.Fatal("expected error for tampered payload, got nil")
	}
	if !isErrProfileSyncBadSig(err) {
		t.Errorf("expected ErrProfileSyncBadSignature, got %v", err)
	}

	// Confirm the username was NOT changed.
	store.mu.RLock()
	for _, u := range store.users {
		if u.Providers["cloud"] == accountID && u.Username == "evehacked" {
			t.Error("username was updated despite bad signature — fail-closed violated")
		}
	}
	store.mu.RUnlock()

	// Confirm no audit record was written.
	records := readAuditRecords(t, syncer.auditLog)
	if len(records) != 0 {
		t.Errorf("expected 0 audit records after bad-sig rejection, got %d", len(records))
	}
}

// TestApply_WrongKey verifies rejection when a different key signed the envelope.
func TestApply_WrongKey(t *testing.T) {
	store := makeTestStore(t)
	syncer, _ := makeTestSyncerWithStore(t, store) // real anchor key

	// Sign with a completely different private key.
	_, differentPriv, _ := ed25519.GenerateKey(rand.Reader)

	const accountID = "01HZACC006"
	registerCloudUser(t, store, accountID, "frank", "Frank Six")

	payload := freshPayload(accountID)
	payload.Username = "franknew"
	env := signEnvelope(t, differentPriv, payload)

	err := syncer.Apply(env)
	if !isErrProfileSyncBadSig(err) {
		t.Errorf("expected ErrProfileSyncBadSignature for wrong key, got %v", err)
	}
}

// TestApply_EmptySignature verifies that a missing signature is rejected
// fail-closed.
func TestApply_EmptySignature(t *testing.T) {
	syncer, _ := makeTestSyncer(t)

	payload := freshPayload("01HZACC007")
	canon, _ := signing.Canonical(payload)

	env := ProfileUpdateEnvelope{
		PayloadBytes: canon,
		Signature:    nil, // empty
	}
	err := syncer.Apply(env)
	if !isErrProfileSyncBadSig(err) {
		t.Errorf("expected ErrProfileSyncBadSignature for empty sig, got %v", err)
	}
}

// TestApply_EmptyPayload verifies that an empty payload is rejected
// fail-closed.
func TestApply_EmptyPayload(t *testing.T) {
	syncer, _ := makeTestSyncer(t)

	env := ProfileUpdateEnvelope{
		PayloadBytes: nil,
		Signature:    make([]byte, 64),
	}
	err := syncer.Apply(env)
	if !isErrProfileSyncBadSig(err) {
		t.Errorf("expected ErrProfileSyncBadSignature for empty payload, got %v", err)
	}
}

// TestApply_ExpiredEnvelope verifies that a past-expiry envelope is rejected
// even if the signature is valid.
func TestApply_ExpiredEnvelope(t *testing.T) {
	store := makeTestStore(t)
	syncer, priv := makeTestSyncerWithStore(t, store)

	const accountID = "01HZACC008"
	registerCloudUser(t, store, accountID, "grace", "Grace Eight")

	payload := freshPayload(accountID)
	payload.ExpiresAt = time.Now().UTC().Add(-1 * time.Minute) // already expired
	env := signEnvelope(t, priv, payload)

	err := syncer.Apply(env)
	if err != ErrProfileSyncExpired {
		t.Errorf("expected ErrProfileSyncExpired, got %v", err)
	}
}

// TestApply_NoLocalUser verifies that ErrProfileSyncNoUser is returned when
// no local user is linked to the cloud AccountID.
func TestApply_NoLocalUser(t *testing.T) {
	syncer, priv := makeTestSyncer(t)

	payload := freshPayload("01HZACC-UNKNOWN")
	payload.Username = "nobody"
	env := signEnvelope(t, priv, payload)

	err := syncer.Apply(env)
	if err != ErrProfileSyncNoUser {
		t.Errorf("expected ErrProfileSyncNoUser, got %v", err)
	}
}

// TestApply_NoChange verifies that no audit records are written when the
// cloud pushes values identical to what is already stored.
func TestApply_NoChange(t *testing.T) {
	store := makeTestStore(t)
	syncer, priv := makeTestSyncerWithStore(t, store)

	const accountID = "01HZACC009"
	user := registerCloudUser(t, store, accountID, "henry", "Henry Nine")

	// Push the same username and full name that are already stored.
	payload := freshPayload(accountID)
	payload.Username = user.Username // same as current
	payload.FullName = user.Name     // same as current
	env := signEnvelope(t, priv, payload)

	if err := syncer.Apply(env); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	records := readAuditRecords(t, syncer.auditLog)
	if len(records) != 0 {
		t.Errorf("expected 0 audit records when nothing changed, got %d", len(records))
	}
}

// TestApply_UsernameSanitized verifies that the username sanitization rules
// are applied (uppercase stripped, non-alphanum dropped, numeric prefix handled).
func TestApply_UsernameSanitized(t *testing.T) {
	store := makeTestStore(t)
	syncer, priv := makeTestSyncerWithStore(t, store)

	const accountID = "01HZACC010"
	registerCloudUser(t, store, accountID, "ivan", "Ivan Ten")

	payload := freshPayload(accountID)
	payload.Username = "IVAN-NEW_2026" // must be lower-cased
	env := signEnvelope(t, priv, payload)

	if err := syncer.Apply(env); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	store.mu.RLock()
	var got string
	for _, u := range store.users {
		if u.Providers["cloud"] == accountID {
			got = u.Username
		}
	}
	store.mu.RUnlock()
	if got != "ivan-new_2026" {
		t.Errorf("username: got %q, want %q", got, "ivan-new_2026")
	}
}

// TestApply_PasswordInvalidatesSessionsSECJ verifies SEC-J behaviour: when the
// cloud pushes a new password, all existing sessions for that user are revoked.
func TestApply_PasswordInvalidatesSessionsSECJ(t *testing.T) {
	store := makeTestStore(t)
	syncer, priv := makeTestSyncerWithStore(t, store)

	const accountID = "01HZACC011"
	user := registerCloudUser(t, store, accountID, "julia", "Julia Eleven")

	// Create a session.
	sess := store.CreateSession(user, "test-device")
	if _, ok := store.ValidateToken(sess.Token); !ok {
		t.Fatal("session not valid before password update")
	}

	payload := freshPayload(accountID)
	payload.Password = "freshpassword!"
	env := signEnvelope(t, priv, payload)

	if err := syncer.Apply(env); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The session must have been invalidated.
	if _, ok := store.ValidateToken(sess.Token); ok {
		t.Error("session still valid after password update — SEC-J violated")
	}
}

// TestApply_CloudManagedFlag verifies that the cloud_managed flag is faithfully
// recorded in audit records.
func TestApply_CloudManagedFlag(t *testing.T) {
	store := makeTestStore(t)
	syncer, priv := makeTestSyncerWithStore(t, store)

	const accountID = "01HZACC012"
	registerCloudUser(t, store, accountID, "karl", "Karl Twelve")

	for _, managed := range []bool{true, false} {
		// Reset audit log for each sub-test.
		os.Remove(syncer.auditLog)

		payload := freshPayload(accountID)
		payload.FullName = "Karl Updated"
		payload.CloudManaged = managed
		env := signEnvelope(t, priv, payload)

		if err := syncer.Apply(env); err != nil {
			t.Fatalf("Apply (cloud_managed=%v): %v", managed, err)
		}

		records := readAuditRecords(t, syncer.auditLog)
		if len(records) == 0 {
			t.Fatalf("expected audit record (cloud_managed=%v)", managed)
		}
		if records[0].CloudManaged != managed {
			t.Errorf("cloud_managed: got %v, want %v", records[0].CloudManaged, managed)
		}

		// Reset name for next iteration.
		store.mu.Lock()
		for _, u := range store.users {
			if u.Providers["cloud"] == accountID {
				u.Name = "Karl Twelve"
			}
		}
		store.mu.Unlock()
	}
}

// TestSanitizeProfileUsername covers edge cases of the sanitizer.
func TestSanitizeProfileUsername(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"alice", "alice"},
		{"ALICE", "alice"},
		{"alice123", "alice123"},
		{"123alice", "u123alice"},
		{"alice smith", "alicesmith"},
		{"alice!@#$%", "alice"},
		{"", ""},
		{"UPPER-Case_123", "upper-case_123"},
	}
	for _, c := range cases {
		got := sanitizeProfileUsername(c.in)
		if got != c.want {
			t.Errorf("sanitizeProfileUsername(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestNewProfileSyncer_EnvOverrides verifies that env vars can override the
// anchor and audit-log paths.
func TestNewProfileSyncer_EnvOverrides(t *testing.T) {
	store := makeTestStore(t)
	dir := t.TempDir()

	anchorPath := filepath.Join(dir, "anchor.pub")
	auditPath := filepath.Join(dir, "audit.log")

	t.Setenv("VULOS_PROFILE_SYNC_ANCHOR", anchorPath)
	t.Setenv("VULOS_PROFILE_SYNC_AUDIT_LOG", auditPath)

	syncer := NewProfileSyncer(store, "", "")

	if syncer.anchorPath != anchorPath {
		t.Errorf("anchorPath: got %q, want %q", syncer.anchorPath, anchorPath)
	}
	if syncer.auditLog != auditPath {
		t.Errorf("auditLog: got %q, want %q", syncer.auditLog, auditPath)
	}
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

// isErrProfileSyncBadSig returns true if err wraps ErrProfileSyncBadSignature.
func isErrProfileSyncBadSig(err error) bool {
	if err == nil {
		return false
	}
	if err == ErrProfileSyncBadSignature {
		return true
	}
	// errors.Is unwrap chain.
	return containsErr(err, ErrProfileSyncBadSignature)
}

func containsErr(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
