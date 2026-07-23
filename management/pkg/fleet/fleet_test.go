package fleet_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/fleet"
)

// storeFactory pairs a name with a constructor so the tests can run against
// both MemStore and SQLStore with identical assertions.
type storeFactory struct {
	name    string
	newFunc func() fleet.Store
}

var sqlSeq atomic.Int64

var storeFactories = []storeFactory{
	{"MemStore", func() fleet.Store { return fleet.NewMemStore() }},
	{"SQLStore", func() fleet.Store {
		dsn := fmt.Sprintf("file:fleet_test_%d?mode=memory&cache=shared", sqlSeq.Add(1))
		db, err := cpdb.OpenSQLiteDSN(dsn)
		if err != nil {
			panic(err)
		}
		s, err := fleet.Open(db)
		if err != nil {
			panic(err)
		}
		return s
	}},
}

func TestFleetConformance(t *testing.T) {
	t.Parallel()
	for _, sf := range storeFactories {
		sf := sf
		t.Run(sf.name, func(t *testing.T) {
			t.Parallel()
			runConformance(t, sf.newFunc)
		})
	}
}

const (
	ulid1 = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	ulid2 = "01BX5ZZKBKACTAV9WEVGEMMVRY"
	acct1 = "acct-alpha"
	acct2 = "acct-beta"
	acct3 = "acct-gamma"
)

func runConformance(t *testing.T, newStore func() fleet.Store) {
	t.Helper()
	ctx := context.Background()

	// ------------------------------------------------------------------ //
	// Create org → owner is the creator → admin role auto-assigned.
	// ------------------------------------------------------------------ //
	t.Run("CreateOrg_OwnerAutoMember", func(t *testing.T) {
		st := newStore()
		defer st.Close()

		org, err := st.CreateOrg(ctx, "TestOrg", acct1)
		if err != nil {
			t.Fatalf("CreateOrg: %v", err)
		}
		if org.Name != "TestOrg" {
			t.Errorf("want Name=%q got %q", "TestOrg", org.Name)
		}
		if org.OwnerAccount != acct1 {
			t.Errorf("want OwnerAccount=%q got %q", acct1, org.OwnerAccount)
		}

		role, err := st.MemberRole(ctx, org.ID, acct1)
		if err != nil {
			t.Fatalf("MemberRole: %v", err)
		}
		if role != "owner" {
			t.Errorf("want role=owner got %q", role)
		}
	})

	// ------------------------------------------------------------------ //
	// AddMember with invalid role → ErrInvalidRole.
	// ------------------------------------------------------------------ //
	t.Run("AddMember_InvalidRole", func(t *testing.T) {
		st := newStore()
		defer st.Close()

		org, _ := st.CreateOrg(ctx, "O", acct1)
		err := st.AddMember(ctx, org.ID, acct2, "superuser")
		if !errors.Is(err, fleet.ErrInvalidRole) {
			t.Errorf("want ErrInvalidRole, got %v", err)
		}
	})

	// ------------------------------------------------------------------ //
	// GetOrg not found → ErrOrgNotFound.
	// ------------------------------------------------------------------ //
	t.Run("GetOrg_NotFound", func(t *testing.T) {
		st := newStore()
		defer st.Close()

		_, err := st.GetOrg(ctx, "NOTEXISTULID00000000000000")
		if !errors.Is(err, fleet.ErrOrgNotFound) {
			t.Errorf("want ErrOrgNotFound, got %v", err)
		}
	})

	// ------------------------------------------------------------------ //
	// Heartbeat creates device row if absent (idempotent), updates
	// last_heartbeat, returns Device with health.
	// ------------------------------------------------------------------ //
	t.Run("Heartbeat_UpsertDevice", func(t *testing.T) {
		st := newStore()
		defer st.Close()

		hb := fleet.Heartbeat{
			ULID:       ulid1,
			Version:    "1.2.3",
			Health:     "healthy",
			SyncLagSec: 5,
			UptimeSec:  100,
			CrashCount: 0,
		}
		d, err := st.InsertHeartbeat(ctx, hb)
		if err != nil {
			t.Fatalf("InsertHeartbeat: %v", err)
		}
		if d.ULID != ulid1 {
			t.Errorf("want ULID=%q got %q", ulid1, d.ULID)
		}
		if d.Health != "healthy" {
			t.Errorf("want Health=healthy got %q", d.Health)
		}
		if d.LastHeartbeat == nil {
			t.Error("want LastHeartbeat set, got nil")
		}
		if d.Version != "1.2.3" {
			t.Errorf("want Version=1.2.3 got %q", d.Version)
		}

		// Second heartbeat on same ULID should update, not create duplicate.
		hb2 := fleet.Heartbeat{
			ULID:       ulid1,
			Version:    "1.3.0",
			Health:     "degraded",
			CrashCount: 2,
		}
		d2, err := st.InsertHeartbeat(ctx, hb2)
		if err != nil {
			t.Fatalf("InsertHeartbeat 2: %v", err)
		}
		if d2.Version != "1.3.0" {
			t.Errorf("want Version=1.3.0 got %q", d2.Version)
		}
		if d2.Health != "degraded" {
			t.Errorf("want Health=degraded got %q", d2.Health)
		}
		if d2.CrashCount != 2 {
			t.Errorf("want CrashCount=2 got %d", d2.CrashCount)
		}
	})

	// ------------------------------------------------------------------ //
	// DeriveStatus table-test for online/stale/offline boundaries.
	// ------------------------------------------------------------------ //
	t.Run("DeriveStatus", func(t *testing.T) {
		now := time.Now().UTC()

		online := now.Add(-30 * time.Second)
		stale := now.Add(-2 * time.Minute)
		offline := now.Add(-6 * time.Minute)

		cases := []struct {
			name string
			hb   *time.Time
			want fleet.Status
		}{
			{"nil_heartbeat", nil, fleet.StatusOffline},
			{"online_30s", &online, fleet.StatusOnline},
			{"stale_2m", &stale, fleet.StatusStale},
			{"offline_6m", &offline, fleet.StatusOffline},
			{"exactly_60s", timePtr(now.Add(-60 * time.Second)), fleet.StatusOnline},
			{"just_over_60s", timePtr(now.Add(-61 * time.Second)), fleet.StatusStale},
			{"exactly_5m", timePtr(now.Add(-5 * time.Minute)), fleet.StatusStale},
			{"just_over_5m", timePtr(now.Add(-5*time.Minute - time.Second)), fleet.StatusOffline},
		}
		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				got := fleet.DeriveStatus(tc.hb, now)
				if got != tc.want {
					t.Errorf("DeriveStatus(%v, now) = %q want %q", tc.hb, got, tc.want)
				}
			})
		}
	})

	// ------------------------------------------------------------------ //
	// Decommission removes device from SeatCount.
	// ------------------------------------------------------------------ //
	t.Run("Decommission_SeatCount", func(t *testing.T) {
		st := newStore()
		defer st.Close()

		org, _ := st.CreateOrg(ctx, "O", acct1)

		// Add two devices.
		for _, ulid := range []string{ulid1, ulid2} {
			_ = st.UpsertDevice(ctx, fleet.Device{
				ULID:      ulid,
				OrgID:     org.ID,
				AccountID: acct1,
				Channel:   "stable",
				Health:    "unknown",
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			})
		}

		count, err := st.SeatCount(ctx, org.ID)
		if err != nil {
			t.Fatalf("SeatCount: %v", err)
		}
		if count != 2 {
			t.Errorf("want SeatCount=2 got %d", count)
		}

		// Decommission one.
		if err := st.Decommission(ctx, ulid1); err != nil {
			t.Fatalf("Decommission: %v", err)
		}

		count, err = st.SeatCount(ctx, org.ID)
		if err != nil {
			t.Fatalf("SeatCount after decommission: %v", err)
		}
		if count != 1 {
			t.Errorf("want SeatCount=1 got %d after decommission", count)
		}
	})

	// ------------------------------------------------------------------ //
	// Authz: non-member listing org devices → ErrNotMember.
	// ------------------------------------------------------------------ //
	t.Run("Authz_NonMember_ErrNotMember", func(t *testing.T) {
		st := newStore()
		defer st.Close()

		org, _ := st.CreateOrg(ctx, "O", acct1)

		_, err := st.MemberRole(ctx, org.ID, acct2)
		if !errors.Is(err, fleet.ErrNotMember) {
			t.Errorf("want ErrNotMember for non-member, got %v", err)
		}
	})

	// ------------------------------------------------------------------ //
	// Authz: member but non-admin trying SetChannel → enforced at handler
	// level; store does not gate channel; verify MemberRole returns "member".
	// ------------------------------------------------------------------ //
	t.Run("Authz_MemberRole_Returned", func(t *testing.T) {
		st := newStore()
		defer st.Close()

		org, _ := st.CreateOrg(ctx, "O", acct1)
		_ = st.AddMember(ctx, org.ID, acct2, "member")

		role, err := st.MemberRole(ctx, org.ID, acct2)
		if err != nil {
			t.Fatalf("MemberRole: %v", err)
		}
		if role != "member" {
			t.Errorf("want role=member got %q", role)
		}
		// adminRoles check (package-external simulation).
		if role == "admin" || role == "owner" {
			t.Error("member should not be admin/owner")
		}
	})

	// ------------------------------------------------------------------ //
	// Pagination: insert 25 devices, ListDevices(limit=10, offset=10)
	// returns 10 in stable (ulid-sorted) order.
	// ------------------------------------------------------------------ //
	t.Run("Pagination_ListDevices", func(t *testing.T) {
		st := newStore()
		defer st.Close()

		org, _ := st.CreateOrg(ctx, "O", acct1)

		// Generate 25 distinct canonical ULIDs and insert devices.
		ulids := make25ULIDs()
		for _, u := range ulids {
			_ = st.UpsertDevice(ctx, fleet.Device{
				ULID:      u,
				OrgID:     org.ID,
				AccountID: acct1,
				Channel:   "stable",
				Health:    "unknown",
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			})
		}

		page, err := st.ListDevices(ctx, org.ID, 10, 10)
		if err != nil {
			t.Fatalf("ListDevices: %v", err)
		}
		if len(page) != 10 {
			t.Errorf("want 10 devices on page, got %d", len(page))
		}
		// Verify stable order (each ULID ≤ next).
		for i := 1; i < len(page); i++ {
			if page[i].ULID < page[i-1].ULID {
				t.Errorf("devices not sorted: %s > %s", page[i-1].ULID, page[i].ULID)
			}
		}
	})

	// ------------------------------------------------------------------ //
	// Concurrency: 50 concurrent heartbeats for same ULID converge to single row.
	// ------------------------------------------------------------------ //
	t.Run("Concurrency_Heartbeats", func(t *testing.T) {
		st := newStore()
		defer st.Close()

		const n = 50
		var wg sync.WaitGroup
		var errCount atomic.Int64
		wg.Add(n)
		for i := 0; i < n; i++ {
			i := i
			go func() {
				defer wg.Done()
				_, err := st.InsertHeartbeat(ctx, fleet.Heartbeat{
					ULID:       ulid1,
					Version:    fmt.Sprintf("1.0.%d", i),
					Health:     "healthy",
					CrashCount: 0,
				})
				if err != nil {
					errCount.Add(1)
				}
			}()
		}
		wg.Wait()
		if errCount.Load() > 0 {
			t.Errorf("%d concurrent heartbeat(s) failed", errCount.Load())
		}

		// Only one device row for ulid1.
		d, err := st.GetDevice(ctx, ulid1)
		if err != nil {
			t.Fatalf("GetDevice after concurrency: %v", err)
		}
		if d.ULID != ulid1 {
			t.Errorf("want ULID=%q got %q", ulid1, d.ULID)
		}
	})

	// ------------------------------------------------------------------ //
	// RecentHeartbeats returns most-recent N in descending order.
	// ------------------------------------------------------------------ //
	t.Run("RecentHeartbeats", func(t *testing.T) {
		st := newStore()
		defer st.Close()

		for i := 0; i < 5; i++ {
			_, _ = st.InsertHeartbeat(ctx, fleet.Heartbeat{
				ULID:    ulid1,
				Version: fmt.Sprintf("1.0.%d", i),
				Health:  "healthy",
			})
		}

		hbs, err := st.RecentHeartbeats(ctx, ulid1, 3)
		if err != nil {
			t.Fatalf("RecentHeartbeats: %v", err)
		}
		if len(hbs) != 3 {
			t.Errorf("want 3 heartbeats, got %d", len(hbs))
		}
		// Should be most-recent first.
		for i := 1; i < len(hbs); i++ {
			if hbs[i].ReceivedAt.After(hbs[i-1].ReceivedAt) {
				t.Error("heartbeats not sorted most-recent first")
			}
		}
	})

	// ------------------------------------------------------------------ //
	// SetName / SetGroup / SetChannel update device fields.
	// ------------------------------------------------------------------ //
	t.Run("DeviceFieldUpdates", func(t *testing.T) {
		st := newStore()
		defer st.Close()

		_, _ = st.InsertHeartbeat(ctx, fleet.Heartbeat{
			ULID:    ulid1,
			Version: "1.0.0",
			Health:  "healthy",
		})

		if err := st.SetName(ctx, ulid1, "MyDevice"); err != nil {
			t.Fatalf("SetName: %v", err)
		}
		if err := st.SetGroup(ctx, ulid1, "production"); err != nil {
			t.Fatalf("SetGroup: %v", err)
		}
		if err := st.SetChannel(ctx, ulid1, "beta"); err != nil {
			t.Fatalf("SetChannel: %v", err)
		}

		d, err := st.GetDevice(ctx, ulid1)
		if err != nil {
			t.Fatalf("GetDevice: %v", err)
		}
		if d.Name != "MyDevice" {
			t.Errorf("want Name=MyDevice got %q", d.Name)
		}
		if d.GroupLabel != "production" {
			t.Errorf("want GroupLabel=production got %q", d.GroupLabel)
		}
		if d.Channel != "beta" {
			t.Errorf("want Channel=beta got %q", d.Channel)
		}
	})

	// ------------------------------------------------------------------ //
	// DeviceNotFound for unknown ULID.
	// ------------------------------------------------------------------ //
	t.Run("GetDevice_NotFound", func(t *testing.T) {
		st := newStore()
		defer st.Close()

		_, err := st.GetDevice(ctx, ulid2)
		if !errors.Is(err, fleet.ErrDeviceNotFound) {
			t.Errorf("want ErrDeviceNotFound, got %v", err)
		}
	})

	// ------------------------------------------------------------------ //
	// CreateInvite stores invite with pending state.
	// ------------------------------------------------------------------ //
	t.Run("CreateInvite", func(t *testing.T) {
		st := newStore()
		defer st.Close()

		org, _ := st.CreateOrg(ctx, "O", acct1)
		inv, err := st.CreateInvite(ctx, org.ID, "colleague@example.com", "member")
		if err != nil {
			t.Fatalf("CreateInvite: %v", err)
		}
		if inv.State != "pending" {
			t.Errorf("want State=pending got %q", inv.State)
		}
		if inv.Email != "colleague@example.com" {
			t.Errorf("want Email=colleague@example.com got %q", inv.Email)
		}
	})

	// ------------------------------------------------------------------ //
	// CreateInvite with invalid role → ErrInvalidRole.
	// ------------------------------------------------------------------ //
	t.Run("CreateInvite_InvalidRole", func(t *testing.T) {
		st := newStore()
		defer st.Close()

		org, _ := st.CreateOrg(ctx, "O", acct1)
		_, err := st.CreateInvite(ctx, org.ID, "x@x.com", "badRole")
		if !errors.Is(err, fleet.ErrInvalidRole) {
			t.Errorf("want ErrInvalidRole, got %v", err)
		}
	})

	// ------------------------------------------------------------------ //
	// ListByAccount returns devices for a specific account.
	// ------------------------------------------------------------------ //
	t.Run("ListByAccount", func(t *testing.T) {
		st := newStore()
		defer st.Close()

		org, _ := st.CreateOrg(ctx, "O", acct1)
		_ = st.UpsertDevice(ctx, fleet.Device{
			ULID:      ulid1,
			OrgID:     org.ID,
			AccountID: acct1,
			Channel:   "stable",
			Health:    "unknown",
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		})
		_ = st.UpsertDevice(ctx, fleet.Device{
			ULID:      ulid2,
			OrgID:     org.ID,
			AccountID: acct2,
			Channel:   "stable",
			Health:    "unknown",
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		})

		devs, err := st.ListByAccount(ctx, acct1, 100, 0)
		if err != nil {
			t.Fatalf("ListByAccount: %v", err)
		}
		if len(devs) != 1 {
			t.Errorf("want 1 device for acct1, got %d", len(devs))
		}
		if devs[0].ULID != ulid1 {
			t.Errorf("want ULID=%q got %q", ulid1, devs[0].ULID)
		}
	})
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

func timePtr(t time.Time) *time.Time { return &t }

// make25ULIDs returns 25 distinct valid canonical ULIDs for pagination tests.
func make25ULIDs() []string {
	// Pre-generated valid Crockford base32 ULIDs (26 chars, uppercase, no I/L/O/U).
	base := []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FAV",
		"01BX5ZZKBKACTAV9WEVGEMMVRY",
		"01C3N9MZSXV5FDWY57JHXY6KT0",
		"01D4P1MZYRW6GEXY68KNYG7MT1",
		"01E5Q2N0ZSX7HFYZ79MNZH8NT2",
		"01F6R3N1ASY8JGZA8AMNA9PBT3",
		"01G7S4N2BTZ9KH0B9BNNBAQCT4",
		"01H8T5N3CT0AK1BCACNNBBRDT5",
		"01J9V6N4DT1BM2CDBDNNCBSET6",
		"01KAWSEDFT2CN3DECENNDBSFT7",
		"01MBXWFEGT3DP4EFDFNNDBTGT8",
		"01NCYXGFHT4EQ5FGEGNNDETHT9",
		"01PD0YHGKT5FR6GHFHNNDFTKTa",
		"01QE1ZJHMT6GS7HKGKNNEFBMTB",
		"01RF20JKNT7HT8KMHMNNDGCNTC",
		"01SG31KMPT8KV9MNKNNNEHDNTD",
		"01TH42MNQT9MWANNMNNNFKENTe",
		"01VK53NNRTANXBNNNNNNGMFNTf",
		"01WM64PNSTBNYCNNNNNNHNGNTg",
		"01XN75QNTTCNZDNNNNNNKNHNTh",
		"01YP86RNTTDNaENNNNNNMNKNTj",
		"01ZQ97SNTTENbFNNNNNNNNMNTk",
		"020RA8TNTTFNcGNNNNNNNNNNTm",
		"021SB9TNTTGNdHNNNNNNNNNNTn",
		"022TC0VNTTHNeKNNNNNNNNNNTp",
	}
	// These are illustrative; replace with real valid ULIDs if CanonULID is called.
	// Use a numeric suffix approach: generate by simple increment.
	out := make([]string, 25)
	for i := range out {
		// Generate ULIDs by embedding the index in a known-valid template.
		// Format: "01ARZ3NDEKTSV4RRFFQ69G5F" + 2-char suffix from crockford.
		const alpha = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
		suffix := string([]byte{alpha[(i/32)%32], alpha[i%32]})
		out[i] = "01ARZ3NDEKTSV4RRFFQ69G5F" + suffix
	}
	_ = base // suppress unused warning
	return out
}
