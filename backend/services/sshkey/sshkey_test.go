package sshkey

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// IS03_ prefix required by task spec.

func IS03_tempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

// IS03_TestEnsureHostKey_Generates verifies that EnsureHostKey creates a key
// on first call and returns a valid public key + SHA256 fingerprint.
func IS03_TestEnsureHostKey_Generates(t *testing.T) {
	home := IS03_tempHome(t)
	pub, fp, err := EnsureHostKey(home)
	if err != nil {
		t.Fatalf("EnsureHostKey: %v", err)
	}
	if len(pub) == 0 {
		t.Fatal("expected non-empty public key")
	}
	if !strings.HasPrefix(fp, "SHA256:") {
		t.Fatalf("expected fingerprint starting with SHA256:, got %q", fp)
	}

	// Key file must exist with mode 0600.
	path := filepath.Join(home, ".vulos", "db", "ssh_host_ed25519_key")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("key file missing: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected mode 0600, got %v", info.Mode().Perm())
	}
}

// IS03_TestEnsureHostKey_Idempotent verifies that calling EnsureHostKey twice
// returns the same key.
func IS03_TestEnsureHostKey_Idempotent(t *testing.T) {
	home := IS03_tempHome(t)
	pub1, fp1, err := EnsureHostKey(home)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	pub2, fp2, err := EnsureHostKey(home)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if string(pub1) != string(pub2) {
		t.Fatal("public key changed between calls")
	}
	if fp1 != fp2 {
		t.Fatal("fingerprint changed between calls")
	}
}

// IS03_TestAuthStore_RoundTrip exercises Add/List/Remove.
func IS03_TestAuthStore_RoundTrip(t *testing.T) {
	home := IS03_tempHome(t)
	store, err := Open(home)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Use a real Ed25519 key generated via EnsureHostKey for the test.
	// We need a separate one so generate in a different tempdir.
	home2 := t.TempDir()
	rawPub, fp, err := EnsureHostKey(home2)
	if err != nil {
		t.Fatalf("EnsureHostKey for test key: %v", err)
	}

	// List should be empty initially.
	keys, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys, got %d", len(keys))
	}

	// Add the key.
	if err := store.Add("test-comment", strings.TrimSpace(string(rawPub))); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// List should now have 1 key.
	keys, err = store.List()
	if err != nil {
		t.Fatalf("List after Add: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d", len(keys))
	}
	if keys[0].Fingerprint != fp {
		t.Fatalf("fingerprint mismatch: got %q want %q", keys[0].Fingerprint, fp)
	}
	if keys[0].Comment != "test-comment" {
		t.Fatalf("comment mismatch: got %q", keys[0].Comment)
	}

	// Adding the same key again should fail.
	if err := store.Add("dup", strings.TrimSpace(string(rawPub))); err == nil {
		t.Fatal("expected error on duplicate add")
	}

	// Remove the key.
	if err := store.Remove(fp); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// List should be empty again.
	keys, err = store.List()
	if err != nil {
		t.Fatalf("List after Remove: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys after remove, got %d", len(keys))
	}

	// Removing a non-existent key should fail.
	if err := store.Remove(fp); err == nil {
		t.Fatal("expected error on remove of absent key")
	}
}

// IS03_TestAuthStore_InvalidKey verifies that Add rejects malformed keys.
func IS03_TestAuthStore_InvalidKey(t *testing.T) {
	home := IS03_tempHome(t)
	store, err := Open(home)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Add("bad", "not-a-valid-key"); err == nil {
		t.Fatal("expected error for invalid key")
	}
}

// IS03_TestAuthorizedKeysFilePermissions verifies authorized_keys is 0600.
func IS03_TestAuthorizedKeysFilePermissions(t *testing.T) {
	home := IS03_tempHome(t)
	if _, err := Open(home); err != nil {
		t.Fatalf("Open: %v", err)
	}
	path := filepath.Join(home, ".ssh", "authorized_keys")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat authorized_keys: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected mode 0600, got %v", info.Mode().Perm())
	}
}

// TestMain drives the IS03_-prefixed tests using the standard testing framework.
// The prefix is part of the task specification, not a Go convention.
func TestEnsureHostKey_Generates(t *testing.T)       { IS03_TestEnsureHostKey_Generates(t) }
func TestEnsureHostKey_Idempotent(t *testing.T)      { IS03_TestEnsureHostKey_Idempotent(t) }
func TestAuthStore_RoundTrip(t *testing.T)           { IS03_TestAuthStore_RoundTrip(t) }
func TestAuthStore_InvalidKey(t *testing.T)          { IS03_TestAuthStore_InvalidKey(t) }
func TestAuthorizedKeysFilePermissions(t *testing.T) { IS03_TestAuthorizedKeysFilePermissions(t) }
