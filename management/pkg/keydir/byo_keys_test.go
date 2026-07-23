package keydir_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/keydir"
)

// fakeAgeKey returns a syntactically plausible age X25519 public key string.
// The bech32 body is not cryptographically valid, but is long enough to pass
// the format check.
func fakeAgeKey(suffix string) string {
	base := "age1qyqszqgpqyqszqgpqyqszqgp" // dummy bech32-ish body
	return base + suffix
}

func openTestSQLStore(t *testing.T) *keydir.SQLStore {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := keydir.OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("OpenSQLStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// ---------------------------------------------------------------------------
// 1. Idempotent put: second PutBYOKey for the same account replaces the key.
// ---------------------------------------------------------------------------

func TestBYOKeyIdempotentPut(t *testing.T) {
	st := openTestSQLStore(t)
	ctx := context.Background()

	key1 := fakeAgeKey("aaa")
	if err := st.PutBYOKey(ctx, "acc-byo-1", key1); err != nil {
		t.Fatalf("first PutBYOKey: %v", err)
	}
	key2 := fakeAgeKey("bbb")
	if err := st.PutBYOKey(ctx, "acc-byo-1", key2); err != nil {
		t.Fatalf("second PutBYOKey: %v", err)
	}

	got, err := st.GetBYOKey(ctx, "acc-byo-1")
	if err != nil {
		t.Fatalf("GetBYOKey: %v", err)
	}
	if got != key2 {
		t.Errorf("got key %q, want %q", got, key2)
	}
}

// ---------------------------------------------------------------------------
// 2. Format validation rejects non-age keys.
// ---------------------------------------------------------------------------

func TestBYOKeyFormatValidation(t *testing.T) {
	st := openTestSQLStore(t)
	ctx := context.Background()

	cases := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"no age1 prefix", "ssh-ed25519 AAAAB3Nza..."},
		{"too short", "age1abc"},
		{"base64 key", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := st.PutBYOKey(ctx, "acc-byo-bad", c.key)
			if !errors.Is(err, keydir.ErrInvalidAgeKey) {
				t.Errorf("PutBYOKey(%q) = %v; want ErrInvalidAgeKey", c.key, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 3. Rotation count increments on each put.
// ---------------------------------------------------------------------------

func TestBYOKeyRotationCount(t *testing.T) {
	st := openTestSQLStore(t)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		key := fakeAgeKey(string(rune('a' + i)))
		if err := st.PutBYOKey(ctx, "acc-byo-rot", key); err != nil {
			t.Fatalf("PutBYOKey #%d: %v", i, err)
		}
	}

	history, err := st.ListBYOKeyHistory(ctx, "acc-byo-rot")
	if err != nil {
		t.Fatalf("ListBYOKeyHistory: %v", err)
	}
	if len(history) == 0 {
		t.Fatal("expected non-empty history")
	}
	if history[0].RotationCount != 3 {
		t.Errorf("rotation_count = %d, want 3", history[0].RotationCount)
	}
}

// ---------------------------------------------------------------------------
// 4. GetBYOKey returns ErrBYOKeyNotFound for unknown account.
// ---------------------------------------------------------------------------

func TestBYOKeyGetNotFound(t *testing.T) {
	st := openTestSQLStore(t)
	ctx := context.Background()

	_, err := st.GetBYOKey(ctx, "nonexistent-account")
	if !errors.Is(err, keydir.ErrBYOKeyNotFound) {
		t.Errorf("expected ErrBYOKeyNotFound, got %v", err)
	}
}
