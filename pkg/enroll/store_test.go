package enroll

import (
	"context"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ─────────────────────────────────────────────────────────────────────────────
// Conformance helpers — run the same suite against any Store implementation.
// ─────────────────────────────────────────────────────────────────────────────

func openSQLStoreForTest(t *testing.T) *SQLStore {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("OpenSQLStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// runConformanceSuite exercises the full Store contract on any Store.
func runConformanceSuite(t *testing.T, newStore func() Store) {
	t.Helper()

	t.Run("HappyPath", func(t *testing.T) {
		ctx := context.Background()
		st := newStore()
		defer st.Close() //nolint:errcheck
		signer := NewMemCertSigner()
		pubKey := signer.PublicKeyBytes()
		g := makeGrant(15 * time.Minute)

		if err := st.StartGrant(ctx, g, pubKey); err != nil {
			t.Fatalf("StartGrant: %v", err)
		}

		// PollGrant before approve → ErrAuthPending
		_, err := st.PollGrant(ctx, g.DeviceCode)
		if err != ErrAuthPending {
			t.Fatalf("PollGrant before approve: want ErrAuthPending, got %v", err)
		}

		const accountID = "01ACCOUNTULID0000000000000"
		const ulid = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
		cert, err := signer.SignDeviceCert(pubKey, accountID, ulid)
		if err != nil {
			t.Fatalf("SignDeviceCert: %v", err)
		}

		if err := st.ApproveGrant(ctx, g.UserCode, accountID, ulid, cert); err != nil {
			t.Fatalf("ApproveGrant: %v", err)
		}

		result, err := st.PollGrant(ctx, g.DeviceCode)
		if err != nil {
			t.Fatalf("PollGrant after approve: %v", err)
		}
		if result.ULID != ulid {
			t.Errorf("ULID: want %q, got %q", ulid, result.ULID)
		}
		if result.AccountID != accountID {
			t.Errorf("AccountID: want %q, got %q", accountID, result.AccountID)
		}
		if len(result.DeviceCert) == 0 {
			t.Error("DeviceCert is empty")
		}
	})

	t.Run("PollBeforeApprove", func(t *testing.T) {
		ctx := context.Background()
		st := newStore()
		defer st.Close() //nolint:errcheck
		g := makeGrant(15 * time.Minute)

		if err := st.StartGrant(ctx, g, freshPubKey(t)); err != nil {
			t.Fatalf("StartGrant: %v", err)
		}
		_, err := st.PollGrant(ctx, g.DeviceCode)
		if err != ErrAuthPending {
			t.Fatalf("want ErrAuthPending, got %v", err)
		}
	})

	t.Run("Expired", func(t *testing.T) {
		ctx := context.Background()
		st := newStore()
		defer st.Close() //nolint:errcheck
		g := makeGrant(-1 * time.Second)

		if err := st.StartGrant(ctx, g, freshPubKey(t)); err != nil {
			t.Fatalf("StartGrant: %v", err)
		}
		_, err := st.PollGrant(ctx, g.DeviceCode)
		if err != ErrExpired {
			t.Fatalf("want ErrExpired, got %v", err)
		}
	})

	t.Run("AlreadyConsumed", func(t *testing.T) {
		ctx := context.Background()
		st := newStore()
		defer st.Close() //nolint:errcheck
		signer := NewMemCertSigner()
		pubKey := signer.PublicKeyBytes()
		g := makeGrant(15 * time.Minute)

		if err := st.StartGrant(ctx, g, pubKey); err != nil {
			t.Fatalf("StartGrant: %v", err)
		}
		const accountID = "01ACCOUNTULID0000000000000"
		const ulid = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
		cert, _ := signer.SignDeviceCert(pubKey, accountID, ulid)
		if err := st.ApproveGrant(ctx, g.UserCode, accountID, ulid, cert); err != nil {
			t.Fatalf("ApproveGrant: %v", err)
		}
		if _, err := st.PollGrant(ctx, g.DeviceCode); err != nil {
			t.Fatalf("first PollGrant: %v", err)
		}
		_, err := st.PollGrant(ctx, g.DeviceCode)
		if err != ErrAlreadyConsumed {
			t.Fatalf("second PollGrant: want ErrAlreadyConsumed, got %v", err)
		}
	})

	t.Run("Deny", func(t *testing.T) {
		ctx := context.Background()
		st := newStore()
		defer st.Close() //nolint:errcheck
		g := makeGrant(15 * time.Minute)

		if err := st.StartGrant(ctx, g, freshPubKey(t)); err != nil {
			t.Fatalf("StartGrant: %v", err)
		}
		if err := st.DenyGrant(ctx, g.UserCode); err != nil {
			t.Fatalf("DenyGrant: %v", err)
		}
		_, err := st.PollGrant(ctx, g.DeviceCode)
		if err != ErrAccessDenied {
			t.Fatalf("want ErrAccessDenied, got %v", err)
		}
	})

	t.Run("UnknownCodes", func(t *testing.T) {
		ctx := context.Background()
		st := newStore()
		defer st.Close() //nolint:errcheck

		_, err := st.PollGrant(ctx, "nosuchcode")
		if err != ErrUnknownDeviceCode {
			t.Errorf("PollGrant unknown: want ErrUnknownDeviceCode, got %v", err)
		}

		err = st.ApproveGrant(ctx, "XXXX-9999", "acc", "ulid", nil)
		if err != ErrUnknownUserCode {
			t.Errorf("ApproveGrant unknown: want ErrUnknownUserCode, got %v", err)
		}

		err = st.DenyGrant(ctx, "XXXX-9999")
		if err != ErrUnknownUserCode {
			t.Errorf("DenyGrant unknown: want ErrUnknownUserCode, got %v", err)
		}

		_, err = st.GetByUserCode(ctx, "XXXX-9999")
		if err != ErrUnknownUserCode {
			t.Errorf("GetByUserCode unknown: want ErrUnknownUserCode, got %v", err)
		}
	})

	t.Run("InvalidPubKey", func(t *testing.T) {
		ctx := context.Background()
		st := newStore()
		defer st.Close() //nolint:errcheck
		g := makeGrant(15 * time.Minute)

		err := st.StartGrant(ctx, g, []byte("tooshort"))
		if err != ErrInvalidPubKey {
			t.Fatalf("want ErrInvalidPubKey, got %v", err)
		}
	})

	t.Run("GetByUserCode", func(t *testing.T) {
		ctx := context.Background()
		st := newStore()
		defer st.Close() //nolint:errcheck
		g := makeGrant(15 * time.Minute)

		if err := st.StartGrant(ctx, g, freshPubKey(t)); err != nil {
			t.Fatalf("StartGrant: %v", err)
		}
		got, err := st.GetByUserCode(ctx, g.UserCode)
		if err != nil {
			t.Fatalf("GetByUserCode: %v", err)
		}
		if got.UserCode != g.UserCode {
			t.Errorf("UserCode: want %q, got %q", g.UserCode, got.UserCode)
		}
		if got.DeviceCode != g.DeviceCode {
			t.Errorf("DeviceCode: want %q, got %q", g.DeviceCode, got.DeviceCode)
		}
	})

	t.Run("ApproveExpired", func(t *testing.T) {
		ctx := context.Background()
		st := newStore()
		defer st.Close() //nolint:errcheck
		g := makeGrant(-1 * time.Second)

		if err := st.StartGrant(ctx, g, freshPubKey(t)); err != nil {
			t.Fatalf("StartGrant: %v", err)
		}
		err := st.ApproveGrant(ctx, g.UserCode, "acc", "ulid", nil)
		if err != ErrExpired {
			t.Fatalf("want ErrExpired, got %v", err)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// MemStore conformance (reference; should always pass — belt-and-suspenders)
// ─────────────────────────────────────────────────────────────────────────────

func TestMemStore_Conformance(t *testing.T) {
	runConformanceSuite(t, func() Store { return NewMemStore() })
}

// ─────────────────────────────────────────────────────────────────────────────
// SQLStore conformance
// ─────────────────────────────────────────────────────────────────────────────

func TestSQLStore_Conformance(t *testing.T) {
	runConformanceSuite(t, func() Store {
		db, err := cpdb.OpenSQLiteDSN(":memory:")
		if err != nil {
			t.Fatalf("cpdb open: %v", err)
		}
		s, err := OpenSQLStore(db)
		if err != nil {
			db.Close()
			t.Fatalf("OpenSQLStore: %v", err)
		}
		return s
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// SQLStore-specific: OpenSQLStore creates schema on a fresh :memory: db
// ─────────────────────────────────────────────────────────────────────────────

func TestSQLStore_Open(t *testing.T) {
	s := openSQLStoreForTest(t)
	// A simple query proves the table exists.
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM enroll_grants`).Scan(&n); err != nil {
		t.Fatalf("enroll_grants table not found: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Ed25519 pubkey validation (both store implementations)
// ─────────────────────────────────────────────────────────────────────────────

func TestStore_InvalidPubKey_SQL(t *testing.T) {
	ctx := context.Background()
	s := openSQLStoreForTest(t)
	g := makeGrant(15 * time.Minute)

	// Too short
	if err := s.StartGrant(ctx, g, []byte("bad")); err != ErrInvalidPubKey {
		t.Errorf("too-short key: want ErrInvalidPubKey, got %v", err)
	}
	// Too long (33 bytes)
	if err := s.StartGrant(ctx, g, make([]byte, 33)); err != ErrInvalidPubKey {
		t.Errorf("too-long key: want ErrInvalidPubKey, got %v", err)
	}
	// Exactly 32 bytes — should succeed
	if err := s.StartGrant(ctx, g, freshPubKey(t)); err != nil {
		t.Errorf("valid key: unexpected error: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Expiry path: DenyGrant on expired grant → ErrExpired
// ─────────────────────────────────────────────────────────────────────────────

func TestSQLStore_DenyExpired(t *testing.T) {
	ctx := context.Background()
	s := openSQLStoreForTest(t)
	g := makeGrant(-1 * time.Second)

	if err := s.StartGrant(ctx, g, freshPubKey(t)); err != nil {
		t.Fatalf("StartGrant: %v", err)
	}
	if err := s.DenyGrant(ctx, g.UserCode); err != ErrExpired {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// compile-time assertion
// ─────────────────────────────────────────────────────────────────────────────

func TestSQLStore_InterfaceConformance(t *testing.T) {
	var _ Store = (*SQLStore)(nil)
}
