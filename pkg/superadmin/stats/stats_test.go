package stats

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ─── mock implementations ─────────────────────────────────────────────────────

type mockAccounts struct {
	total    int64
	active30 int64
	active1  int64
	signups  int64
	tierMix  SignupsByTier
}

func (m *mockAccounts) CountTotal(_ context.Context) (int64, error) { return m.total, nil }
func (m *mockAccounts) CountActiveAfter(_ context.Context, since time.Time) (int64, error) {
	// Use a simple heuristic to differentiate 30d vs 1d calls.
	cutoff := time.Now().UTC().AddDate(0, 0, -2)
	if since.After(cutoff) {
		return m.active1, nil
	}
	return m.active30, nil
}
func (m *mockAccounts) CountSignupsSince(_ context.Context, _ time.Time) (int64, error) {
	return m.signups, nil
}
func (m *mockAccounts) SignupsByTierSince(_ context.Context, _ time.Time) (SignupsByTier, error) {
	return m.tierMix, nil
}

type mockBilling struct {
	mrrZARCents int64
}

func (m *mockBilling) MRRZARCents(_ context.Context) (int64, error) { return m.mrrZARCents, nil }

func openTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// Test 1: snapshot accuracy — counts propagate correctly to the DB row.
func TestRunSnapshot_Accuracy(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	today := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)

	accounts := &mockAccounts{
		total:    1000,
		active30: 800,
		active1:  120,
		signups:  15,
		tierMix:  SignupsByTier{Free: 12, Pro: 2, Team: 1, Enterprise: 0},
	}
	billing := &mockBilling{mrrZARCents: 164700}

	if err := st.RunSnapshot(ctx, today, accounts, billing); err != nil {
		t.Fatalf("RunSnapshot: %v", err)
	}

	snaps, err := st.Series(ctx, today.AddDate(0, 0, -1), today.AddDate(0, 0, 1))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	sn := snaps[0]
	if sn.TotalAccounts != 1000 {
		t.Errorf("TotalAccounts: want 1000, got %d", sn.TotalAccounts)
	}
	if sn.Active30D != 800 {
		t.Errorf("Active30D: want 800, got %d", sn.Active30D)
	}
	if sn.SignupsToday != 15 {
		t.Errorf("SignupsToday: want 15, got %d", sn.SignupsToday)
	}
	if sn.MRRZARCents != 164700 {
		t.Errorf("MRRZARCents: want 164700, got %d", sn.MRRZARCents)
	}
}

// Test 2: MRR sum — billing.MRRZARCents is stored correctly.
func TestRunSnapshot_MRRSum(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	today := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)

	accounts := &mockAccounts{total: 500, active30: 400, active1: 50, signups: 5}
	billing := &mockBilling{mrrZARCents: 999_00} // 999 ZAR

	if err := st.RunSnapshot(ctx, today, accounts, billing); err != nil {
		t.Fatalf("RunSnapshot: %v", err)
	}
	snaps, err := st.Series(ctx, today, today)
	if err != nil || len(snaps) == 0 {
		t.Fatalf("Series: %v / %d", err, len(snaps))
	}
	if snaps[0].MRRZARCents != 99900 {
		t.Errorf("MRR: want 99900, got %d", snaps[0].MRRZARCents)
	}
}

// Test 3: tier mix — SignupsByTierJSON is parseable and accurate.
func TestRunSnapshot_TierMix(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	today := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)

	accounts := &mockAccounts{
		total:    2000,
		active30: 1600,
		active1:  200,
		signups:  20,
		tierMix:  SignupsByTier{Free: 16, Pro: 2, Team: 1, Enterprise: 1},
	}
	billing := &mockBilling{mrrZARCents: 500_00}

	if err := st.RunSnapshot(ctx, today, accounts, billing); err != nil {
		t.Fatalf("RunSnapshot: %v", err)
	}

	snaps, err := st.Series(ctx, today, today)
	if err != nil || len(snaps) == 0 {
		t.Fatalf("Series: %v", err)
	}

	var tier SignupsByTier
	if err := json.Unmarshal([]byte(snaps[0].SignupsByTierJSON), &tier); err != nil {
		t.Fatalf("unmarshal tier json: %v", err)
	}
	if tier.Free != 16 || tier.Pro != 2 || tier.Enterprise != 1 {
		t.Errorf("tier mix wrong: %+v", tier)
	}
}

// Test 4: idempotent re-run on same date overwrites with fresh data.
func TestRunSnapshot_IdempotentRerun(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	today := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)

	accounts1 := &mockAccounts{total: 100, active30: 80, active1: 10, signups: 5}
	billing1 := &mockBilling{mrrZARCents: 100_00}
	if err := st.RunSnapshot(ctx, today, accounts1, billing1); err != nil {
		t.Fatalf("first RunSnapshot: %v", err)
	}

	// Second run with different values — should overwrite.
	accounts2 := &mockAccounts{total: 200, active30: 160, active1: 20, signups: 8}
	billing2 := &mockBilling{mrrZARCents: 200_00}
	if err := st.RunSnapshot(ctx, today, accounts2, billing2); err != nil {
		t.Fatalf("second RunSnapshot: %v", err)
	}

	snaps, err := st.Series(ctx, today, today)
	if err != nil || len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot after idempotent re-run, got %d / %v", len(snaps), err)
	}
	if snaps[0].TotalAccounts != 200 {
		t.Errorf("expected overwritten value 200, got %d", snaps[0].TotalAccounts)
	}
	if snaps[0].MRRZARCents != 20000 {
		t.Errorf("expected overwritten MRR 20000, got %d", snaps[0].MRRZARCents)
	}
}

// Test 5: missing-day backfill inserts zero-value placeholder rows.
func TestBackfillMissingDays(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)

	// Insert only day 2 and day 4 — days 1, 3, 5 are missing.
	for _, dayOff := range []int{1, 3} {
		d := from.AddDate(0, 0, dayOff)
		if err := st.RunSnapshot(ctx, d,
			&mockAccounts{total: int64(dayOff * 10)},
			&mockBilling{mrrZARCents: 0},
		); err != nil {
			t.Fatalf("RunSnapshot day+%d: %v", dayOff, err)
		}
	}

	if err := st.BackfillMissingDays(ctx, from, to); err != nil {
		t.Fatalf("BackfillMissingDays: %v", err)
	}

	snaps, err := st.Series(ctx, from, to)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(snaps) != 5 {
		t.Errorf("expected 5 rows after backfill (one per day), got %d", len(snaps))
	}
}
