package llmkeys

import (
	"context"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func testKEK() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

// runCRUD exercises the full PUT→GET→DELETE round-trip and asserts the raw key
// is recoverable internally but never surfaced in the List view.
func runCRUD(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	const acct = "acct-1"
	const raw = "sk-secret-ABC123-do-not-leak"

	// Empty / invalid inputs are rejected.
	if err := store.Put(ctx, acct, "", raw); err != ErrInvalidProvider {
		t.Fatalf("empty provider err = %v, want ErrInvalidProvider", err)
	}
	if err := store.Put(ctx, acct, "openai", "   "); err != ErrEmptyKey {
		t.Fatalf("empty key err = %v, want ErrEmptyKey", err)
	}

	// PUT (provider id is normalised to lowercase).
	if err := store.Put(ctx, acct, "OpenAI", raw); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Put(ctx, acct, "anthropic", "sk-ant-xyz"); err != nil {
		t.Fatalf("Put 2: %v", err)
	}

	// GET (list) returns providers WITHOUT raw keys.
	infos, err := store.List(ctx, acct)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("list len = %d, want 2", len(infos))
	}
	for _, pi := range infos {
		if pi.Provider != strings.ToLower(pi.Provider) {
			t.Fatalf("provider not normalised: %q", pi.Provider)
		}
		// The ProviderInfo struct has no key field at all — assert via JSON-ish
		// inspection that nothing carries the secret.
		if strings.Contains(pi.Provider, raw) || strings.Contains(pi.CreatedAt, raw) {
			t.Fatalf("raw key leaked into List view: %+v", pi)
		}
	}

	// The decrypted key round-trips for server-internal use only.
	got, err := store.Get(ctx, acct, "openai")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != raw {
		t.Fatalf("decrypted key = %q, want %q", got, raw)
	}

	// DELETE removes only the targeted provider.
	if err := store.Delete(ctx, acct, "openai"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(ctx, acct, "openai"); err != ErrNotFound {
		t.Fatalf("after delete Get err = %v, want ErrNotFound", err)
	}
	infos, _ = store.List(ctx, acct)
	if len(infos) != 1 || infos[0].Provider != "anthropic" {
		t.Fatalf("after delete list = %+v, want [anthropic]", infos)
	}

	// Deleting a missing provider is a no-op.
	if err := store.Delete(ctx, acct, "openai"); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}

	// Cross-account isolation: another account sees nothing.
	other, _ := store.List(ctx, "acct-2")
	if len(other) != 0 {
		t.Fatalf("cross-account list len = %d, want 0", len(other))
	}
}

func TestLLMKeysCRUD_MemStore(t *testing.T) {
	runCRUD(t, NewMemStore(testKEK()))
}

func TestLLMKeysCRUD_SQLStore(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	store, err := OpenSQLStore(db, testKEK())
	if err != nil {
		db.Close()
		t.Fatalf("OpenSQLStore: %v", err)
	}
	defer store.Close()
	runCRUD(t, store)
}

// TestEncryptedAtRest asserts the persisted ciphertext is not the plaintext key.
func TestEncryptedAtRest(t *testing.T) {
	kek := testKEK()
	const raw = "sk-plaintext-should-never-persist"
	enc, err := encrypt(raw, kek)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if strings.Contains(enc, raw) || enc == raw {
		t.Fatalf("ciphertext contains plaintext: %q", enc)
	}
	dec, err := decrypt(enc, kek)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if dec != raw {
		t.Fatalf("round-trip = %q, want %q", dec, raw)
	}
	// Wrong KEK fails to decrypt (authenticated).
	wrong := make([]byte, 32)
	if _, err := decrypt(enc, wrong); err == nil {
		t.Fatalf("decrypt with wrong KEK should fail")
	}
}
