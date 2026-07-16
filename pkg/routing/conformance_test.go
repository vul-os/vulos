// Package routing — Store conformance suite.
//
// # Running against MemStore (always)
//
// go test ./internal/routing/... runs storeConformance against NewMemStore().
//
// # Adding the SQL store after merge
//
// When routing-store merges OpenSQLStore into this branch, extend storeFactories
// with a single line inside TestStoreConformance (or init):
//
//	storeFactories = append(storeFactories, storeFactory{"sql", func() Store {
//	    s, err := OpenSQLStore(":memory:")
//	    if err != nil { panic(err) }
//	    return s
//	}})
//
// No other changes required; the conformance suite is generic over Store.
package routing

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// storeFactory pairs a name with a constructor so the conformance suite can
// run under every registered Store implementation.
type storeFactory struct {
	name    string
	newFunc func() Store
}

// storeFactories lists every Store implementation to run the conformance suite
// against. Add SQL factory here once routing-store merges (see package doc).
// sqlConfSeq gives each SQL conformance store a unique in-memory DB name so
// every newStore() sub-case is fully isolated (no cross-case leakage).
var sqlConfSeq atomic.Int64

var storeFactories = []storeFactory{
	{"MemStore", func() Store { return NewMemStore() }},
	{"SQLStore", func() Store {
		// cache=shared keeps the unique in-memory DB alive for the store's
		// pooled connection; a fresh name per call isolates sub-cases.
		dsn := fmt.Sprintf("file:conf%d?mode=memory&cache=shared", sqlConfSeq.Add(1))
		db, err := cpdb.OpenSQLiteDSN(dsn)
		if err != nil {
			panic(err)
		}
		s, err := Open(db)
		if err != nil {
			db.Close()
			panic(err)
		}
		return s
	}},
}

func TestStoreConformance(t *testing.T) {
	t.Parallel()
	for _, sf := range storeFactories {
		sf := sf
		t.Run(sf.name, func(t *testing.T) {
			t.Parallel()
			storeConformance(t, sf.newFunc)
		})
	}
}

// storeConformance exercises the full Store contract as defined in contract.go
// and ROUTING_API.md. It is implementation-agnostic; pass any func() Store.
func storeConformance(t *testing.T, newStore func() Store) {
	t.Helper()
	ctx := context.Background()

	// Canonical ULIDs used throughout the suite (valid, distinct).
	const (
		ulid1 = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
		ulid2 = "01BX5ZZKBKACTAV9WEVGEMMVRY" // second distinct ULID
	)
	const (
		acct1 = "account-alpha"
		acct2 = "account-beta"
	)

	// ─────────────────────────────────────────────────────────────────────────
	// Enroll
	// ─────────────────────────────────────────────────────────────────────────

	t.Run("Enroll/happy-path", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		b, err := s.Enroll(ctx, ulid1, acct1, ModeFabric)
		if err != nil {
			t.Fatalf("Enroll: %v", err)
		}
		if b.ULID != ulid1 {
			t.Errorf("ULID = %q; want %q", b.ULID, ulid1)
		}
		if b.AccountID != acct1 {
			t.Errorf("AccountID = %q; want %q", b.AccountID, acct1)
		}
		if b.Mode != ModeFabric {
			t.Errorf("Mode = %q; want %q", b.Mode, ModeFabric)
		}
		if b.CreatedAt.IsZero() {
			t.Error("CreatedAt should not be zero")
		}
		if b.UpdatedAt.IsZero() {
			t.Error("UpdatedAt should not be zero")
		}
	})

	t.Run("Enroll/idempotent-same-account", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		b1, err := s.Enroll(ctx, ulid1, acct1, ModeFabric)
		if err != nil {
			t.Fatalf("first Enroll: %v", err)
		}

		// Second enroll: same ULID + same account but different mode.
		time.Sleep(time.Millisecond) // ensure UpdatedAt can advance
		b2, err := s.Enroll(ctx, ulid1, acct1, ModeDirect)
		if err != nil {
			t.Fatalf("idempotent Enroll: %v", err)
		}
		if b2.ULID != ulid1 {
			t.Errorf("ULID preserved: got %q; want %q", b2.ULID, ulid1)
		}
		if b2.AccountID != acct1 {
			t.Errorf("AccountID preserved: got %q; want %q", b2.AccountID, acct1)
		}
		// Mode should be refreshed to the new value.
		if b2.Mode != ModeDirect {
			t.Errorf("Mode refreshed: got %q; want %q", b2.Mode, ModeDirect)
		}
		// CreatedAt must be unchanged (same original binding).
		if !b2.CreatedAt.Equal(b1.CreatedAt) {
			t.Errorf("CreatedAt changed on re-enroll: %v → %v", b1.CreatedAt, b2.CreatedAt)
		}
	})

	t.Run("Enroll/cross-account-409", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		if _, err := s.Enroll(ctx, ulid1, acct1, ModeFabric); err != nil {
			t.Fatalf("first Enroll: %v", err)
		}
		_, err := s.Enroll(ctx, ulid1, acct2, ModeFabric)
		if !errors.Is(err, ErrULIDBoundToOtherAccount) {
			t.Fatalf("cross-account Enroll error = %v; want ErrULIDBoundToOtherAccount", err)
		}
		// Verify the original binding is unchanged.
		b, err := s.GetBinding(ctx, ulid1)
		if err != nil {
			t.Fatalf("GetBinding after cross-account attempt: %v", err)
		}
		if b.AccountID != acct1 {
			t.Errorf("binding should still belong to %q; got %q", acct1, b.AccountID)
		}
	})

	t.Run("Enroll/invalid-mode", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		_, err := s.Enroll(ctx, ulid1, acct1, "local")
		if !errors.Is(err, ErrInvalidMode) {
			t.Fatalf("Enroll(invalid mode) = %v; want ErrInvalidMode", err)
		}
	})

	// ─────────────────────────────────────────────────────────────────────────
	// GetBinding
	// ─────────────────────────────────────────────────────────────────────────

	t.Run("GetBinding/not-enrolled", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		_, err := s.GetBinding(ctx, ulid1)
		if !errors.Is(err, ErrNotEnrolled) {
			t.Fatalf("GetBinding(unenrolled) = %v; want ErrNotEnrolled", err)
		}
	})

	t.Run("GetBinding/returns-correct-binding", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		enrolled, err := s.Enroll(ctx, ulid1, acct1, ModeFabric)
		if err != nil {
			t.Fatalf("Enroll: %v", err)
		}
		got, err := s.GetBinding(ctx, ulid1)
		if err != nil {
			t.Fatalf("GetBinding: %v", err)
		}
		if got.ULID != enrolled.ULID || got.AccountID != enrolled.AccountID || got.Mode != enrolled.Mode {
			t.Errorf("GetBinding returned %+v; want %+v", got, enrolled)
		}
	})

	// ─────────────────────────────────────────────────────────────────────────
	// SetConnMode
	// ─────────────────────────────────────────────────────────────────────────

	t.Run("SetConnMode/happy-path", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		if _, err := s.Enroll(ctx, ulid1, acct1, ModeFabric); err != nil {
			t.Fatalf("Enroll: %v", err)
		}
		if err := s.SetConnMode(ctx, ulid1, ModeDirect); err != nil {
			t.Fatalf("SetConnMode: %v", err)
		}
		b, err := s.GetBinding(ctx, ulid1)
		if err != nil {
			t.Fatalf("GetBinding: %v", err)
		}
		if b.Mode != ModeDirect {
			t.Errorf("Mode = %q after SetConnMode; want %q", b.Mode, ModeDirect)
		}
	})

	t.Run("SetConnMode/not-enrolled", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		err := s.SetConnMode(ctx, ulid1, ModeFabric)
		if !errors.Is(err, ErrNotEnrolled) {
			t.Fatalf("SetConnMode(unenrolled) = %v; want ErrNotEnrolled", err)
		}
	})

	t.Run("SetConnMode/invalid-mode", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		if _, err := s.Enroll(ctx, ulid1, acct1, ModeFabric); err != nil {
			t.Fatalf("Enroll: %v", err)
		}
		err := s.SetConnMode(ctx, ulid1, "local")
		if !errors.Is(err, ErrInvalidMode) {
			t.Fatalf("SetConnMode(invalid mode) = %v; want ErrInvalidMode", err)
		}
	})

	// ─────────────────────────────────────────────────────────────────────────
	// SetDirectIP
	// ─────────────────────────────────────────────────────────────────────────

	t.Run("SetDirectIP/owner-can-set", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		if _, err := s.Enroll(ctx, ulid1, acct1, ModeDirect); err != nil {
			t.Fatalf("Enroll: %v", err)
		}
		if err := s.SetDirectIP(ctx, ulid1, acct1, "1.2.3.4"); err != nil {
			t.Fatalf("SetDirectIP: %v", err)
		}
		b, err := s.GetBinding(ctx, ulid1)
		if err != nil {
			t.Fatalf("GetBinding: %v", err)
		}
		if b.DirectIP != "1.2.3.4" {
			t.Errorf("DirectIP = %q; want %q", b.DirectIP, "1.2.3.4")
		}
	})

	t.Run("SetDirectIP/non-owner-rejected", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		if _, err := s.Enroll(ctx, ulid1, acct1, ModeDirect); err != nil {
			t.Fatalf("Enroll: %v", err)
		}
		err := s.SetDirectIP(ctx, ulid1, acct2, "1.2.3.4")
		if !errors.Is(err, ErrNotOwner) {
			t.Fatalf("SetDirectIP(non-owner) = %v; want ErrNotOwner", err)
		}
	})

	t.Run("SetDirectIP/not-enrolled", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		err := s.SetDirectIP(ctx, ulid1, acct1, "1.2.3.4")
		if !errors.Is(err, ErrNotEnrolled) {
			t.Fatalf("SetDirectIP(unenrolled) = %v; want ErrNotEnrolled", err)
		}
	})

	t.Run("SetDirectIP/ipv6-accepted", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		if _, err := s.Enroll(ctx, ulid1, acct1, ModeDirect); err != nil {
			t.Fatalf("Enroll: %v", err)
		}
		// Routable IPv6 (RFC 3849 doc range). NOTE: not "::1" — loopback is
		// correctly rejected by ValidDirectIP since direct mode is a public IP.
		if err := s.SetDirectIP(ctx, ulid1, acct1, "2001:db8::1"); err != nil {
			t.Fatalf("SetDirectIP(IPv6): %v", err)
		}
	})

	// ─────────────────────────────────────────────────────────────────────────
	// Devices
	// ─────────────────────────────────────────────────────────────────────────

	t.Run("Devices/upsert-and-list", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		d1 := Device{ID: "dev-1", ULID: ulid1, AccountID: acct1, Mode: ModeFabric, LastSeen: time.Now().UTC()}
		d2 := Device{ID: "dev-2", ULID: ulid1, AccountID: acct1, Mode: ModeDirect, LastSeen: time.Now().UTC()}

		if err := s.UpsertDevice(ctx, d1); err != nil {
			t.Fatalf("UpsertDevice d1: %v", err)
		}
		if err := s.UpsertDevice(ctx, d2); err != nil {
			t.Fatalf("UpsertDevice d2: %v", err)
		}

		devs, err := s.ListDevices(ctx, ulid1)
		if err != nil {
			t.Fatalf("ListDevices: %v", err)
		}
		if len(devs) != 2 {
			t.Fatalf("ListDevices returned %d devices; want 2", len(devs))
		}
		// Results are sorted by ID.
		if devs[0].ID != "dev-1" || devs[1].ID != "dev-2" {
			t.Errorf("order wrong: %v", devs)
		}
	})

	t.Run("Devices/upsert-overwrites", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		d := Device{ID: "dev-x", ULID: ulid1, AccountID: acct1, Mode: ModeFabric}
		if err := s.UpsertDevice(ctx, d); err != nil {
			t.Fatalf("UpsertDevice: %v", err)
		}
		// Update mode.
		d.Mode = ModeDirect
		if err := s.UpsertDevice(ctx, d); err != nil {
			t.Fatalf("UpsertDevice (update): %v", err)
		}
		devs, err := s.ListDevices(ctx, ulid1)
		if err != nil {
			t.Fatalf("ListDevices: %v", err)
		}
		if len(devs) != 1 {
			t.Fatalf("expected 1 device after upsert; got %d", len(devs))
		}
		if devs[0].Mode != ModeDirect {
			t.Errorf("Mode after upsert = %q; want %q", devs[0].Mode, ModeDirect)
		}
	})

	t.Run("Devices/list-filters-by-ulid", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		d1 := Device{ID: "dev-a", ULID: ulid1, AccountID: acct1, Mode: ModeFabric}
		d2 := Device{ID: "dev-b", ULID: ulid2, AccountID: acct2, Mode: ModeFabric}
		if err := s.UpsertDevice(ctx, d1); err != nil {
			t.Fatalf("UpsertDevice d1: %v", err)
		}
		if err := s.UpsertDevice(ctx, d2); err != nil {
			t.Fatalf("UpsertDevice d2: %v", err)
		}
		devs, err := s.ListDevices(ctx, ulid1)
		if err != nil {
			t.Fatalf("ListDevices: %v", err)
		}
		if len(devs) != 1 || devs[0].ID != "dev-a" {
			t.Errorf("ListDevices(ulid1) = %v; want [dev-a]", devs)
		}
	})

	t.Run("Devices/list-empty-ulid-returns-empty", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		devs, err := s.ListDevices(ctx, ulid1)
		if err != nil {
			t.Fatalf("ListDevices: %v", err)
		}
		if len(devs) != 0 {
			t.Errorf("ListDevices(no devices) = %v; want []", devs)
		}
	})

	// ─────────────────────────────────────────────────────────────────────────
	// PoP
	// ─────────────────────────────────────────────────────────────────────────

	t.Run("PoP/upsert-and-healthypops", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		p1 := PoP{ID: "pop-za", Region: "af-south", IP: "10.0.0.1", Lat: -33.9, Lon: 18.4, Healthy: true}
		p2 := PoP{ID: "pop-eu", Region: "eu-west", IP: "10.0.0.2", Lat: 51.5, Lon: -0.1, Healthy: true}

		if err := s.UpsertPoP(ctx, p1); err != nil {
			t.Fatalf("UpsertPoP p1: %v", err)
		}
		if err := s.UpsertPoP(ctx, p2); err != nil {
			t.Fatalf("UpsertPoP p2: %v", err)
		}

		pops, err := s.HealthyPoPs(ctx)
		if err != nil {
			t.Fatalf("HealthyPoPs: %v", err)
		}
		if len(pops) != 2 {
			t.Fatalf("HealthyPoPs returned %d; want 2", len(pops))
		}
	})

	t.Run("PoP/unhealthy-excluded-from-HealthyPoPs", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		if err := s.UpsertPoP(ctx, PoP{ID: "sick", Healthy: false, IP: "10.0.0.1"}); err != nil {
			t.Fatalf("UpsertPoP: %v", err)
		}
		pops, err := s.HealthyPoPs(ctx)
		if err != nil {
			t.Fatalf("HealthyPoPs: %v", err)
		}
		if len(pops) != 0 {
			t.Errorf("HealthyPoPs should exclude unhealthy; got %v", pops)
		}
	})

	t.Run("PoP/SetPoPHealth-toggles", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		p := PoP{ID: "pop-toggle", IP: "10.0.0.3", Healthy: true}
		if err := s.UpsertPoP(ctx, p); err != nil {
			t.Fatalf("UpsertPoP: %v", err)
		}

		// Mark unhealthy.
		if err := s.SetPoPHealth(ctx, "pop-toggle", false); err != nil {
			t.Fatalf("SetPoPHealth(false): %v", err)
		}
		pops, _ := s.HealthyPoPs(ctx)
		if len(pops) != 0 {
			t.Errorf("after SetPoPHealth(false) HealthyPoPs should be empty; got %v", pops)
		}

		// Mark healthy again.
		if err := s.SetPoPHealth(ctx, "pop-toggle", true); err != nil {
			t.Fatalf("SetPoPHealth(true): %v", err)
		}
		pops, _ = s.HealthyPoPs(ctx)
		if len(pops) != 1 {
			t.Errorf("after SetPoPHealth(true) HealthyPoPs should have 1; got %v", pops)
		}
	})

	t.Run("PoP/SetPoPHealth-unknown-returns-ErrNotFound", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		err := s.SetPoPHealth(ctx, "does-not-exist", true)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("SetPoPHealth(unknown) = %v; want ErrNotFound", err)
		}
	})

	t.Run("PoP/HealthyPoPs-ordering-by-id", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		// Insert in reverse alphabetical order.
		for _, id := range []string{"z-pop", "m-pop", "a-pop"} {
			if err := s.UpsertPoP(ctx, PoP{ID: id, Healthy: true, IP: "1.1.1.1"}); err != nil {
				t.Fatalf("UpsertPoP %q: %v", id, err)
			}
		}
		pops, err := s.HealthyPoPs(ctx)
		if err != nil {
			t.Fatalf("HealthyPoPs: %v", err)
		}
		if len(pops) != 3 {
			t.Fatalf("want 3 PoPs; got %d", len(pops))
		}
		if pops[0].ID != "a-pop" || pops[1].ID != "m-pop" || pops[2].ID != "z-pop" {
			t.Errorf("HealthyPoPs order wrong: %v", pops)
		}
	})

	t.Run("PoP/UpsertPoP-overwrites", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		if err := s.UpsertPoP(ctx, PoP{ID: "p", Healthy: true, IP: "1.0.0.1", Region: "old"}); err != nil {
			t.Fatalf("UpsertPoP: %v", err)
		}
		if err := s.UpsertPoP(ctx, PoP{ID: "p", Healthy: true, IP: "1.0.0.2", Region: "new"}); err != nil {
			t.Fatalf("UpsertPoP (overwrite): %v", err)
		}
		pops, _ := s.HealthyPoPs(ctx)
		if len(pops) != 1 || pops[0].Region != "new" {
			t.Errorf("UpsertPoP overwrite: got %v", pops)
		}
	})

	// ─────────────────────────────────────────────────────────────────────────
	// Vanity
	// ─────────────────────────────────────────────────────────────────────────

	t.Run("Vanity/claim-happy-path", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		if err := s.ClaimVanity(ctx, "myhome", ulid1, acct1); err != nil {
			t.Fatalf("ClaimVanity: %v", err)
		}
	})

	t.Run("Vanity/claim-idempotent-same-owner", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		if err := s.ClaimVanity(ctx, "myhome", ulid1, acct1); err != nil {
			t.Fatalf("first ClaimVanity: %v", err)
		}
		// Same name, same ULID, same account → idempotent.
		if err := s.ClaimVanity(ctx, "myhome", ulid1, acct1); err != nil {
			t.Fatalf("idempotent ClaimVanity: %v", err)
		}
	})

	t.Run("Vanity/claim-taken-different-account", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		if err := s.ClaimVanity(ctx, "myhome", ulid1, acct1); err != nil {
			t.Fatalf("first ClaimVanity: %v", err)
		}
		err := s.ClaimVanity(ctx, "myhome", ulid2, acct2)
		if !errors.Is(err, ErrVanityTaken) {
			t.Fatalf("ClaimVanity(taken) = %v; want ErrVanityTaken", err)
		}
	})

	t.Run("Vanity/claim-taken-same-account-different-ulid", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		if err := s.ClaimVanity(ctx, "myhome", ulid1, acct1); err != nil {
			t.Fatalf("first ClaimVanity: %v", err)
		}
		// Same account, different ULID target → still taken.
		err := s.ClaimVanity(ctx, "myhome", ulid2, acct1)
		if !errors.Is(err, ErrVanityTaken) {
			t.Fatalf("ClaimVanity(same account, diff ULID) = %v; want ErrVanityTaken", err)
		}
	})

	t.Run("Vanity/claim-invalid-name", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		// Names containing "--" are rejected by ValidName.
		err := s.ClaimVanity(ctx, "bad--name", ulid1, acct1)
		if !errors.Is(err, ErrInvalidName) {
			t.Fatalf("ClaimVanity(invalid name) = %v; want ErrInvalidName", err)
		}
	})

	t.Run("Vanity/resolve-unknown", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		_, err := s.ResolveVanity(ctx, "nobody")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("ResolveVanity(unknown) = %v; want ErrNotFound", err)
		}
	})

	t.Run("Vanity/resolve-known", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		if err := s.ClaimVanity(ctx, "myhome", ulid1, acct1); err != nil {
			t.Fatalf("ClaimVanity: %v", err)
		}
		v, err := s.ResolveVanity(ctx, "myhome")
		if err != nil {
			t.Fatalf("ResolveVanity: %v", err)
		}
		if v.Name != "myhome" || v.ULID != ulid1 || v.AccountID != acct1 {
			t.Errorf("ResolveVanity = %+v; want {myhome %s %s}", v, ulid1, acct1)
		}
	})

	t.Run("Vanity/resolve-different-names-independent", func(t *testing.T) {
		s := newStore()
		defer s.Close() //nolint:errcheck

		if err := s.ClaimVanity(ctx, "alpha", ulid1, acct1); err != nil {
			t.Fatalf("ClaimVanity alpha: %v", err)
		}
		if err := s.ClaimVanity(ctx, "beta", ulid2, acct2); err != nil {
			t.Fatalf("ClaimVanity beta: %v", err)
		}
		va, err := s.ResolveVanity(ctx, "alpha")
		if err != nil || va.ULID != ulid1 {
			t.Errorf("alpha resolved to %v, err=%v", va, err)
		}
		vb, err := s.ResolveVanity(ctx, "beta")
		if err != nil || vb.ULID != ulid2 {
			t.Errorf("beta resolved to %v, err=%v", vb, err)
		}
	})

	// ─────────────────────────────────────────────────────────────────────────
	// Close
	// ─────────────────────────────────────────────────────────────────────────

	t.Run("Close/no-error", func(t *testing.T) {
		s := newStore()
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
}
