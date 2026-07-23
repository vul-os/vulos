// keydir_test.go — behavioural tests for MAIL-07 key directory.
// Run against both MemStore (unit) and SQLStore (integration via in-memory SQLite).
package keydir_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/keydir"
)

func TestValidateVulosAddress(t *testing.T) {
	cases := []struct {
		addr    string
		wantErr error
	}{
		{"alice@vulos.org", nil},
		{"ALICE@VULOS.ORG", nil}, // case-insensitive
		{"alice@gmail.com", keydir.ErrNotVumail},
		{"alice@vumail.com", keydir.ErrNotVumail},
		{"@vulos.org", keydir.ErrInvalidInput}, // empty local
		{"", keydir.ErrInvalidInput},
	}
	for _, c := range cases {
		err := keydir.ValidateVumailAddress(c.addr)
		if !errors.Is(err, c.wantErr) {
			t.Errorf("ValidateVumailAddress(%q) = %v; want %v", c.addr, err, c.wantErr)
		}
	}
}

// storeFactory returns a fresh Store for each sub-test.
type storeFactory func(t *testing.T) keydir.Store

func testStore(t *testing.T, factory storeFactory) {
	t.Helper()
	ctx := context.Background()

	t.Run("upsert_and_lookup_by_address", func(t *testing.T) {
		st := factory(t)
		defer st.Close()
		e, err := st.Upsert(ctx, keydir.Entry{
			AccountID:     "acc-1",
			VumailAddress: "alice@vulos.org",
			PublicKeyB64:  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			Discoverable:  true,
			State:         "active",
		})
		if err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if e.ID == 0 {
			t.Fatal("expected non-zero ID")
		}

		got, err := st.GetByAddress(ctx, "alice@vulos.org")
		if err != nil {
			t.Fatalf("GetByAddress: %v", err)
		}
		if got.PublicKeyB64 != e.PublicKeyB64 {
			t.Errorf("public key mismatch: got %q, want %q", got.PublicKeyB64, e.PublicKeyB64)
		}
	})

	t.Run("upsert_update_key", func(t *testing.T) {
		st := factory(t)
		defer st.Close()
		_, err := st.Upsert(ctx, keydir.Entry{
			AccountID:     "acc-2",
			VumailAddress: "bob@vulos.org",
			PublicKeyB64:  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			Discoverable:  true,
		})
		if err != nil {
			t.Fatalf("initial Upsert: %v", err)
		}
		updated, err := st.Upsert(ctx, keydir.Entry{
			AccountID:     "acc-2",
			VumailAddress: "bob@vulos.org",
			PublicKeyB64:  "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
			Discoverable:  true,
		})
		if err != nil {
			t.Fatalf("update Upsert: %v", err)
		}
		if updated.PublicKeyB64 != "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=" {
			t.Errorf("update not applied: got %q", updated.PublicKeyB64)
		}
	})

	t.Run("duplicate_address_different_account", func(t *testing.T) {
		st := factory(t)
		defer st.Close()
		_, err := st.Upsert(ctx, keydir.Entry{
			AccountID:     "acc-3",
			VumailAddress: "carol@vulos.org",
			PublicKeyB64:  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		})
		if err != nil {
			t.Fatalf("first Upsert: %v", err)
		}
		_, err = st.Upsert(ctx, keydir.Entry{
			AccountID:     "acc-4", // different account
			VumailAddress: "carol@vulos.org",
			PublicKeyB64:  "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
		})
		if !errors.Is(err, keydir.ErrDuplicate) {
			t.Errorf("expected ErrDuplicate, got %v", err)
		}
	})

	t.Run("non_vulos_address_rejected", func(t *testing.T) {
		st := factory(t)
		defer st.Close()
		_, err := st.Upsert(ctx, keydir.Entry{
			AccountID:     "acc-5",
			VumailAddress: "dave@gmail.com",
			PublicKeyB64:  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		})
		if !errors.Is(err, keydir.ErrNotVumail) {
			t.Errorf("expected ErrNotVumail, got %v", err)
		}
	})

	t.Run("get_by_address_not_found", func(t *testing.T) {
		st := factory(t)
		defer st.Close()
		_, err := st.GetByAddress(ctx, "nobody@vulos.org")
		if !errors.Is(err, keydir.ErrNotFound) {
			t.Errorf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("revoke_hides_from_lookup", func(t *testing.T) {
		st := factory(t)
		defer st.Close()
		_, err := st.Upsert(ctx, keydir.Entry{
			AccountID:     "acc-6",
			VumailAddress: "eve@vulos.org",
			PublicKeyB64:  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			State:         "active",
		})
		if err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if err := st.Revoke(ctx, "acc-6"); err != nil {
			t.Fatalf("Revoke: %v", err)
		}
		_, err = st.GetByAddress(ctx, "eve@vulos.org")
		if !errors.Is(err, keydir.ErrNotFound) {
			t.Errorf("expected ErrNotFound after revoke, got %v", err)
		}
	})

	t.Run("set_discoverable", func(t *testing.T) {
		st := factory(t)
		defer st.Close()
		_, err := st.Upsert(ctx, keydir.Entry{
			AccountID:     "acc-7",
			VumailAddress: "frank@vulos.org",
			PublicKeyB64:  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			Discoverable:  true,
		})
		if err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if err := st.SetDiscoverable(ctx, "acc-7", false); err != nil {
			t.Fatalf("SetDiscoverable: %v", err)
		}
		got, err := st.GetByAccount(ctx, "acc-7")
		if err != nil {
			t.Fatalf("GetByAccount: %v", err)
		}
		if got.Discoverable {
			t.Error("expected Discoverable=false after SetDiscoverable(false)")
		}
	})

	t.Run("missing_fields_rejected", func(t *testing.T) {
		st := factory(t)
		defer st.Close()
		_, err := st.Upsert(ctx, keydir.Entry{
			VumailAddress: "grace@vulos.org",
			PublicKeyB64:  "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			// AccountID missing
		})
		if !errors.Is(err, keydir.ErrInvalidInput) {
			t.Errorf("expected ErrInvalidInput for missing account_id, got %v", err)
		}
	})
}

func TestMemStore(t *testing.T) {
	testStore(t, func(t *testing.T) keydir.Store {
		return keydir.NewMemStore()
	})
}

func TestSQLStore(t *testing.T) {
	testStore(t, func(t *testing.T) keydir.Store {
		db, err := cpdb.OpenSQLiteDSN(":memory:")
		if err != nil {
			t.Fatalf("cpdb open: %v", err)
		}
		st, err := keydir.OpenSQLStore(db)
		if err != nil {
			db.Close()
			t.Fatalf("OpenSQLStore: %v", err)
		}
		return st
	})
}
