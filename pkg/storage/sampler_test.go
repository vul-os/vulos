// sampler_test.go — unit tests for RunUsageSampler (STORE-05).
package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// TestRunUsageSampler_SamplesAndInsertsUsage verifies that RunUsageSampler
// calls Provider.BucketSize for each config row and persists the result via
// InsertUsage within the interval.
func TestRunUsageSampler_SamplesAndInsertsUsage(t *testing.T) {
	mem := NewMemProvider()
	_ = mem.EnsureBucket(context.Background(), "bkt-acc1")
	_ = mem.PutObject(context.Background(), "bkt-acc1", "obj1", strings.NewReader("hello"), 5)

	st := NewMemStore()
	_ = st.PutConfig(context.Background(), Config{
		AccountID: "acc1",
		Bucket:    "bkt-acc1",
	})

	svc := &Service{
		Store: st,
		ProviderForAccount: func(_ context.Context, _ string) (Provider, error) {
			return mem, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use a very short interval so the second tick fires quickly.
	interval := 50 * time.Millisecond
	RunUsageSampler(ctx, svc, interval)

	// Wait long enough for at least one sample cycle (first tick is immediate).
	deadline := time.After(2 * time.Second)
	for {
		u, err := st.LatestUsage(context.Background(), "acc1")
		if err == nil && u.SizeBytes == 5 && u.ObjectCount == 1 {
			return // success
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for usage sample; last err: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestRunUsageSampler_MissingBucketSkipsGracefully verifies that a config
// with an empty bucket is skipped without crashing the sampler.
func TestRunUsageSampler_MissingBucketSkipsGracefully(t *testing.T) {
	mem := NewMemProvider()
	st := NewMemStore()
	// Config with empty bucket.
	_ = st.PutConfig(context.Background(), Config{
		AccountID: "acc-empty",
		Bucket:    "", // empty — should be skipped
	})

	svc := &Service{
		Store: st,
		ProviderForAccount: func(_ context.Context, _ string) (Provider, error) {
			return mem, nil
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	// Should not panic; just run and cancel.
	RunUsageSampler(ctx, svc, 50*time.Millisecond)
	<-ctx.Done()
	// No assertion needed — test passes if it doesn't panic.
}

// TestUsageSampleInterval verifies the default and env override.
func TestUsageSampleInterval(t *testing.T) {
	// Default: 0 override → 24h.
	if got := UsageSampleInterval(0); got != 24*time.Hour {
		t.Errorf("default interval: got %v, want 24h", got)
	}
	// Non-zero override takes precedence.
	if got := UsageSampleInterval(5 * time.Minute); got != 5*time.Minute {
		t.Errorf("override interval: got %v, want 5m", got)
	}
}

// TestListConfigs_MemStore verifies that MemStore.ListConfigs returns all rows.
func TestListConfigs_MemStore(t *testing.T) {
	st := NewMemStore()
	_ = st.PutConfig(context.Background(), Config{AccountID: "a1", Bucket: "b1"})
	_ = st.PutConfig(context.Background(), Config{AccountID: "a2", Bucket: "b2"})

	cfgs, err := st.ListConfigs(context.Background())
	if err != nil {
		t.Fatalf("ListConfigs: %v", err)
	}
	if len(cfgs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(cfgs))
	}
	// Sorted by account_id.
	if cfgs[0].AccountID != "a1" || cfgs[1].AccountID != "a2" {
		t.Errorf("unexpected order: %v %v", cfgs[0].AccountID, cfgs[1].AccountID)
	}
}

// TestListConfigs_SQLStore verifies that SQLStore.ListConfigs returns all rows.
func TestListConfigs_SQLStore(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := Open(db, nil)
	if err != nil {
		_ = db.Close()
		t.Fatalf("storage.Open: %v", err)
	}
	defer st.Close()

	_ = st.PutConfig(context.Background(), Config{AccountID: "s1", Bucket: "b1"})
	_ = st.PutConfig(context.Background(), Config{AccountID: "s2", Bucket: "b2"})

	cfgs, err := st.ListConfigs(context.Background())
	if err != nil {
		t.Fatalf("ListConfigs: %v", err)
	}
	if len(cfgs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(cfgs))
	}
}
