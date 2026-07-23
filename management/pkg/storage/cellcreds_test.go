package storage

import (
	"context"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// makeKEK returns a deterministic 32-byte key for tests.
func makeKEK() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func openSQLCellStore(t *testing.T, kek []byte) *SQLStore {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	st, err := Open(db, kek)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func roundTrip(t *testing.T, store CellCredStore) {
	t.Helper()
	ctx := context.Background()
	cc := CellCred{
		AccountID: "ACCT1",
		ULID:      "ULID1",
		Bucket:    "vulos-ulid1",
		CredID:    "ak-123",
		PolicyID:  "arn:pol",
		AccessKey: "AKIDXYZ",
		Secret:    "super-secret-value",
		Endpoint:  "https://s3.example.invalid",
		Region:    "auto",
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second),
	}
	if err := store.PutCellCred(ctx, cc); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetCellCred(ctx, "ACCT1", "ULID1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Account-scoping: the same ulid under a DIFFERENT account must NOT resolve.
	if _, err := store.GetCellCred(ctx, "OTHER-ACCT", "ULID1"); err == nil {
		t.Fatal("expected miss for a different account's same ulid")
	}
	if got.Secret != cc.Secret {
		t.Fatalf("secret round-trip: got %q want %q", got.Secret, cc.Secret)
	}
	if got.CredID != cc.CredID || got.Bucket != cc.Bucket || got.PolicyID != cc.PolicyID {
		t.Fatalf("field mismatch: %+v", got)
	}
	if got.ExpiresAt.Unix() != cc.ExpiresAt.Unix() {
		t.Fatalf("expiry: got %v want %v", got.ExpiresAt, cc.ExpiresAt)
	}

	list, err := store.ListCellCreds(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: err=%v n=%d", err, len(list))
	}

	if err := store.DeleteCellCred(ctx, "ACCT1", "ULID1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.GetCellCred(ctx, "ACCT1", "ULID1"); err == nil {
		t.Fatal("expected ErrUnknownAccount after delete")
	}
}

func TestCellCredStore_SQL_RoundTrip_Encrypted(t *testing.T) {
	roundTrip(t, openSQLCellStore(t, makeKEK()))
}

func TestCellCredStore_SQL_RoundTrip_DevMode(t *testing.T) {
	roundTrip(t, openSQLCellStore(t, nil)) // no KEK: plaintext dev mode
}

func TestCellCredStore_Mem_RoundTrip(t *testing.T) {
	roundTrip(t, NewMemStore())
}

// TestCellCredStore_SecretEncryptedAtRest verifies the stored ciphertext does NOT
// contain the plaintext secret when a KEK is set.
func TestCellCredStore_SecretEncryptedAtRest(t *testing.T) {
	st := openSQLCellStore(t, makeKEK())
	ctx := context.Background()
	secret := "PLAINTEXT-SECRET-MUST-NOT-APPEAR"
	if err := st.PutCellCred(ctx, CellCred{
		AccountID: "A", ULID: "U", Bucket: "b", CredID: "c", AccessKey: "ak", Secret: secret,
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	var raw string
	err := st.db.QueryRowContext(ctx,
		st.db.Rebind(`SELECT secret_enc FROM storage_cell_creds WHERE ulid = ?`), "U").Scan(&raw)
	if err != nil {
		t.Fatalf("raw select: %v", err)
	}
	if raw == secret || contains(raw, secret) {
		t.Fatalf("secret stored in plaintext: %q", raw)
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (hay == needle ||
		func() bool {
			for i := 0; i+len(needle) <= len(hay); i++ {
				if hay[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}())
}
