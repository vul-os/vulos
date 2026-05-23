// store_security_test.go — security-focused tests for identity/store.go.
//
// Tests:
//   1. patchMailMessage: whitelisted column accepted (read, archived, deleted).
//   2. patchMailMessage: non-whitelisted column rejected with ErrInvalidPatchField.
//   3. patchMailMessage: owner account patches own message (200-path).
//   4. patchMailMessage: other-account message returns no error but updates 0 rows (404-path).
package identity

import (
	"errors"
	"path/filepath"
	"testing"
)

// openTestStore opens a real SQLite-backed Store in t.TempDir().
func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "identity.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// ─── Fix #1: SQL-injection whitelist ────────────────────────────────────────

// TestPatchMailMessage_WhitelistedKeyAccepted verifies that the three allowed
// patch keys — "read", "archived", "deleted" — pass validation.
func TestPatchMailMessage_WhitelistedKeyAccepted(t *testing.T) {
	store := openTestStore(t)

	id, _ := newULID()
	if err := store.saveMailMessageForAccount(&MailMessage{
		ID:          id,
		FromAddress: "a@vulos.org",
		Subject:     "s",
		BodyEncrypted: []byte("e"),
	}, "owner-acct"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, col := range []string{"read", "archived", "deleted"} {
		if err := store.patchMailMessage(id, "owner-acct", map[string]interface{}{col: 1}); err != nil {
			t.Errorf("whitelisted column %q should be accepted, got error: %v", col, err)
		}
	}
}

// TestPatchMailMessage_NonWhitelistedKeyRejected verifies that any column not
// in allowedPatchColumns is rejected with ErrInvalidPatchField.
func TestPatchMailMessage_NonWhitelistedKeyRejected(t *testing.T) {
	store := openTestStore(t)

	id, _ := newULID()
	if err := store.saveMailMessageForAccount(&MailMessage{
		ID:          id,
		FromAddress: "b@vulos.org",
		Subject:     "s",
		BodyEncrypted: []byte("e"),
	}, "owner-acct"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	badKeys := []string{
		"body_encrypted",
		"subject",
		"from_address; DROP TABLE vumail_mailbox; --",
		"1=1",
	}
	for _, bad := range badKeys {
		err := store.patchMailMessage(id, "owner-acct", map[string]interface{}{bad: 1})
		if err == nil {
			t.Errorf("non-whitelisted key %q should be rejected, got nil error", bad)
			continue
		}
		if !errors.Is(err, ErrInvalidPatchField) {
			t.Errorf("non-whitelisted key %q: expected ErrInvalidPatchField, got: %v", bad, err)
		}
	}
}

// ─── Fix #5: Cross-tenant isolation ─────────────────────────────────────────

// TestPatchMailMessage_OwnMessageSucceeds verifies that a message can be
// patched by the account that owns it.
func TestPatchMailMessage_OwnMessageSucceeds(t *testing.T) {
	store := openTestStore(t)

	id, _ := newULID()
	if err := store.saveMailMessageForAccount(&MailMessage{
		ID:          id,
		FromAddress: "c@vulos.org",
		Subject:     "s",
		BodyEncrypted: []byte("e"),
	}, "alice"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := store.patchMailMessage(id, "alice", map[string]interface{}{"read": 1}); err != nil {
		t.Fatalf("own-message patch should succeed, got: %v", err)
	}

	msg, err := store.getMailMessage(id)
	if err != nil || msg == nil {
		t.Fatalf("getMailMessage: %v", err)
	}
	if !msg.Read {
		t.Error("expected read=true after own-message patch")
	}
}

// TestPatchMailMessage_OtherUserMessageNotFound verifies that patching
// another account's message silently updates 0 rows (404-equivalent path).
// It must NOT return an error (to avoid leaking existence).
func TestPatchMailMessage_OtherUserMessageNotFound(t *testing.T) {
	store := openTestStore(t)

	id, _ := newULID()
	if err := store.saveMailMessageForAccount(&MailMessage{
		ID:          id,
		FromAddress: "d@vulos.org",
		Subject:     "s",
		BodyEncrypted: []byte("e"),
	}, "alice"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Bob tries to patch Alice's message — must succeed at the store level
	// (returns no error, updates 0 rows) so the handler returns 404.
	if err := store.patchMailMessage(id, "bob", map[string]interface{}{"read": 1}); err != nil {
		t.Fatalf("cross-tenant patch should return nil error (handler returns 404), got: %v", err)
	}

	// Verify Alice's message was NOT modified.
	msg, err := store.getMailMessage(id)
	if err != nil || msg == nil {
		t.Fatalf("getMailMessage: %v", err)
	}
	if msg.Read {
		t.Error("Alice's message should not have been modified by Bob's patch")
	}
}
