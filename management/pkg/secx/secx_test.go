package secx_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/secx"
)

// storeFactory builds a fresh VisibilityStore for each sub-test.
type storeFactory func(t *testing.T) secx.VisibilityStore

// newMemFactory returns a factory that creates a MemStore.
func newMemFactory() storeFactory {
	return func(t *testing.T) secx.VisibilityStore {
		t.Helper()
		return secx.NewMemStore()
	}
}

// newSQLFactory returns a factory that creates an in-memory SQLStore.
func newSQLFactory() storeFactory {
	return func(t *testing.T) secx.VisibilityStore {
		t.Helper()
		db, err := cpdb.OpenSQLiteDSN(":memory:")
		if err != nil {
			t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
		}
		st, err := secx.Open(db)
		if err != nil {
			db.Close()
			t.Fatalf("secx.Open: %v", err)
		}
		t.Cleanup(func() { st.Close() })
		return st
	}
}

// runConformance runs all conformance tests against both MemStore and SQLStore.
func runConformance(t *testing.T, factory storeFactory) {
	t.Helper()

	t.Run("Get_UnknownApp", func(t *testing.T) {
		st := factory(t)
		ctx := context.Background()
		_, err := st.Get(ctx, "app1", "ULID01")
		if !errors.Is(err, secx.ErrUnknownApp) {
			t.Errorf("want ErrUnknownApp, got %v", err)
		}
	})

	t.Run("Set_and_Get", func(t *testing.T) {
		st := factory(t)
		ctx := context.Background()
		av := secx.AppVisibility{
			AppID:      "myapp",
			ULID:       "01J0000000000000000000001A",
			AccountID:  "acc-1",
			Visibility: secx.VisPrivate,
		}
		audit := secx.AuditEntry{
			AppID:     av.AppID,
			ULID:      av.ULID,
			AccountID: "acc-1",
			OldVis:    "",
			NewVis:    string(secx.VisPrivate),
			IP:        "127.0.0.1",
		}
		if err := st.Set(ctx, av, audit); err != nil {
			t.Fatalf("Set: %v", err)
		}
		got, err := st.Get(ctx, av.AppID, av.ULID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Visibility != secx.VisPrivate {
			t.Errorf("want private, got %s", got.Visibility)
		}
		if got.AccountID != "acc-1" {
			t.Errorf("want acc-1, got %s", got.AccountID)
		}
	})

	t.Run("Set_updates_existing", func(t *testing.T) {
		st := factory(t)
		ctx := context.Background()
		av := secx.AppVisibility{
			AppID: "app", ULID: "01J0000000000000000000001B",
			AccountID: "acc-2", Visibility: secx.VisPrivate,
		}
		_ = st.Set(ctx, av, secx.AuditEntry{AppID: av.AppID, ULID: av.ULID, AccountID: "acc-2", OldVis: "", NewVis: "private"})

		av.Visibility = secx.VisLocal
		_ = st.Set(ctx, av, secx.AuditEntry{AppID: av.AppID, ULID: av.ULID, AccountID: "acc-2", OldVis: "private", NewVis: "local"})

		got, err := st.Get(ctx, av.AppID, av.ULID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Visibility != secx.VisLocal {
			t.Errorf("want local, got %s", got.Visibility)
		}
	})

	t.Run("AuditRow_written_on_every_change", func(t *testing.T) {
		st := factory(t)
		ctx := context.Background()
		const appID = "audit-app"
		const ulid = "01J0000000000000000000001C"

		for i, vis := range []secx.Visibility{secx.VisPrivate, secx.VisLocal, secx.VisPublic} {
			av := secx.AppVisibility{
				AppID: appID, ULID: ulid, AccountID: "acc-3", Visibility: vis,
			}
			oldVis := ""
			if i > 0 {
				oldVis = string([]secx.Visibility{secx.VisPrivate, secx.VisLocal}[i-1])
			}
			audit := secx.AuditEntry{
				AppID: appID, ULID: ulid, AccountID: "acc-3",
				OldVis: oldVis, NewVis: string(vis), IP: "10.0.0.1",
			}
			if err := st.Set(ctx, av, audit); err != nil {
				t.Fatalf("Set[%d]: %v", i, err)
			}
		}

		log, err := st.AuditLog(ctx, appID, ulid, 0)
		if err != nil {
			t.Fatalf("AuditLog: %v", err)
		}
		if len(log) != 3 {
			t.Errorf("want 3 audit rows, got %d", len(log))
		}
		// Newest first — last set was "public".
		if len(log) > 0 && log[0].NewVis != "public" {
			t.Errorf("want newest NewVis=public, got %s", log[0].NewVis)
		}
	})

	t.Run("ListByULID_returns_all_apps_for_ulid", func(t *testing.T) {
		st := factory(t)
		ctx := context.Background()
		const ulid = "01J0000000000000000000001D"

		apps := []string{"alpha", "beta", "gamma"}
		for _, app := range apps {
			av := secx.AppVisibility{
				AppID: app, ULID: ulid, AccountID: "acc-4", Visibility: secx.VisPrivate,
			}
			if err := st.Set(ctx, av, secx.AuditEntry{
				AppID: app, ULID: ulid, AccountID: "acc-4", OldVis: "", NewVis: "private",
			}); err != nil {
				t.Fatalf("Set %s: %v", app, err)
			}
		}

		// Also set a record for a different ULID — must not appear.
		otherAV := secx.AppVisibility{
			AppID: "other", ULID: "01J0000000000000000000001E", AccountID: "acc-x", Visibility: secx.VisLocal,
		}
		_ = st.Set(ctx, otherAV, secx.AuditEntry{AppID: "other", ULID: otherAV.ULID})

		list, err := st.ListByULID(ctx, ulid)
		if err != nil {
			t.Fatalf("ListByULID: %v", err)
		}
		if len(list) != len(apps) {
			t.Errorf("want %d records, got %d", len(apps), len(list))
		}
		for i, av := range list {
			if av.ULID != ulid {
				t.Errorf("record %d: unexpected ULID %s", i, av.ULID)
			}
		}
		// Results must be ordered by app_id.
		for i := 1; i < len(list); i++ {
			if list[i].AppID < list[i-1].AppID {
				t.Errorf("not sorted by app_id at index %d", i)
			}
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Run conformance for both MemStore and SQLStore
// ─────────────────────────────────────────────────────────────────────────────

func TestMemStore_Conformance(t *testing.T) {
	runConformance(t, newMemFactory())
}

func TestSQLStore_Conformance(t *testing.T) {
	runConformance(t, newSQLFactory())
}

// ─────────────────────────────────────────────────────────────────────────────
// SQLStore-specific: multiple stores sharing same in-memory DB must not clash
// ─────────────────────────────────────────────────────────────────────────────

func TestSQLStore_UniqueMemDB(t *testing.T) {
	// Each iteration opens a fresh in-memory DB so they don't share state.
	for i := 0; i < 3; i++ {
		db, err := cpdb.OpenSQLiteDSN(":memory:")
		if err != nil {
			t.Fatalf("cpdb.OpenSQLiteDSN %d: %v", i, err)
		}
		st, err := secx.Open(db)
		if err != nil {
			db.Close()
			t.Fatalf("open %d: %v", i, err)
		}
		defer st.Close()

		ctx := context.Background()
		av := secx.AppVisibility{
			AppID:      fmt.Sprintf("app-%d", i),
			ULID:       "01J000000000000000000000AA",
			AccountID:  fmt.Sprintf("acc-%d", i),
			Visibility: secx.VisPublic,
		}
		if err := st.Set(ctx, av, secx.AuditEntry{AppID: av.AppID, ULID: av.ULID, AccountID: av.AccountID, NewVis: "public"}); err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ValidVisibility helper
// ─────────────────────────────────────────────────────────────────────────────

func TestValidVisibility(t *testing.T) {
	cases := []struct {
		v    secx.Visibility
		want bool
	}{
		{secx.VisPrivate, true},
		{secx.VisLocal, true},
		{secx.VisPublic, true},
		{"", false},
		{"unlisted", false},
		{"PUBLIC", false},
	}
	for _, c := range cases {
		if got := secx.ValidVisibility(c.v); got != c.want {
			t.Errorf("ValidVisibility(%q) = %v, want %v", c.v, got, c.want)
		}
	}
}
