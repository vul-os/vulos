// provision_test.go — tests for per-tenant bucket provisioning.
package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/region"
	"github.com/vul-os/vulos-management/pkg/storageport"
)

// TestProvisionManagedBucket verifies that ProvisionManagedBucket calls
// EnsureBucket and persists a Config row with the canonical bucket name.
func TestProvisionManagedBucket(t *testing.T) {
	mem := NewMemProvider()
	st := NewMemStore()
	svc := &Service{
		Store: st,
		ProviderForAccount: func(_ context.Context, _ string) (Provider, error) {
			return mem, nil
		},
		Provisioner: ProvisionerFromProvider(mem),
	}

	ulid := "01HZPROVISION1234"
	accountID := "acct-provision"

	cfg, err := svc.ProvisionManagedBucket(context.Background(), accountID, ulid, ProvisionOptions{})
	if err != nil {
		t.Fatalf("ProvisionManagedBucket: %v", err)
	}

	// Returned config should have the canonical bucket name.
	expectedBucket := "vulos-" + strings.ToLower(ulid)
	if cfg.Bucket != expectedBucket {
		t.Errorf("expected bucket %q, got %q", expectedBucket, cfg.Bucket)
	}
	if cfg.BYO {
		t.Error("expected BYO=false for managed bucket")
	}
	if cfg.AccountID != accountID {
		t.Errorf("expected AccountID=%q, got %q", accountID, cfg.AccountID)
	}

	// The bucket should now exist in the MemProvider.
	if err := mem.EnsureBucket(context.Background(), expectedBucket); err != nil {
		t.Errorf("EnsureBucket after provision should be idempotent: %v", err)
	}
	// Verify bucket was created (EnsureBucket is idempotent, not a getter;
	// verify via PutObject succeeding).
	if err := mem.PutObject(context.Background(), expectedBucket, "probe", strings.NewReader("x"), 1); err != nil {
		t.Errorf("bucket not created by ProvisionManagedBucket: %v", err)
	}

	// The config should be readable from the store.
	stored, err := st.GetConfig(context.Background(), accountID)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if stored.Bucket != expectedBucket {
		t.Errorf("stored bucket mismatch: got %q", stored.Bucket)
	}
}

// TestProvisionManagedBucketIdempotent verifies that calling
// ProvisionManagedBucket twice does not error and leaves a consistent config.
func TestProvisionManagedBucketIdempotent(t *testing.T) {
	mem := NewMemProvider()
	st := NewMemStore()
	svc := &Service{
		Store: st,
		ProviderForAccount: func(_ context.Context, _ string) (Provider, error) {
			return mem, nil
		},
	}

	svc.Provisioner = ProvisionerFromProvider(mem)

	ulid := "01HZIDEM999"
	accountID := "acct-idem"

	_, err := svc.ProvisionManagedBucket(context.Background(), accountID, ulid, ProvisionOptions{})
	if err != nil {
		t.Fatalf("first provision: %v", err)
	}
	_, err = svc.ProvisionManagedBucket(context.Background(), accountID, ulid, ProvisionOptions{})
	if err != nil {
		t.Fatalf("second provision (should be idempotent): %v", err)
	}
}

// TestProvisionBucketIfAbsent verifies the lazy provisioner:
// - First call creates the bucket.
// - Second call returns the existing config without calling EnsureBucket again.
func TestProvisionBucketIfAbsent(t *testing.T) {
	mem := NewMemProvider()
	st := NewMemStore()
	prov := &countingProvisioner{StorageProvisioner: ProvisionerFromProvider(mem)}
	svc := &Service{
		Store: st,
		ProviderForAccount: func(_ context.Context, _ string) (Provider, error) {
			return mem, nil
		},
		Provisioner: prov,
	}

	ulid := "01HZLAZY12345"
	accountID := "acct-lazy"

	// First call: no config row → provisions (one EnsureBucket).
	cfg1, err := svc.ProvisionBucketIfAbsent(context.Background(), accountID, ulid, ProvisionOptions{})
	if err != nil {
		t.Fatalf("first ProvisionBucketIfAbsent: %v", err)
	}
	if cfg1.Bucket == "" {
		t.Fatal("expected bucket after first call")
	}
	firstCalls := prov.ensureCalls

	// Second call: config row exists → should return early.
	cfg2, err := svc.ProvisionBucketIfAbsent(context.Background(), accountID, ulid, ProvisionOptions{})
	if err != nil {
		t.Fatalf("second ProvisionBucketIfAbsent: %v", err)
	}
	if cfg2.Bucket != cfg1.Bucket {
		t.Errorf("bucket changed on second call: %q vs %q", cfg1.Bucket, cfg2.Bucket)
	}
	// The provisioner should NOT be asked to create a bucket again.
	if prov.ensureCalls != firstCalls {
		t.Errorf("provisioner EnsureBucket called %d extra times on second ProvisionBucketIfAbsent (expected 0)", prov.ensureCalls-firstCalls)
	}
}

// countingProvisioner counts EnsureBucket/EnsureBucketInRegion calls so a test can
// assert the lazy path does not re-provision.
type countingProvisioner struct {
	storageport.StorageProvisioner
	ensureCalls int
}

func (c *countingProvisioner) EnsureBucket(ctx context.Context, name string) error {
	c.ensureCalls++
	return c.StorageProvisioner.EnsureBucket(ctx, name)
}

func (c *countingProvisioner) EnsureBucketInRegion(ctx context.Context, name, region string) error {
	c.ensureCalls++
	return c.StorageProvisioner.EnsureBucketInRegion(ctx, name, region)
}

// TestProvisionManagedBucket_NoopProvisionerBringYourOwn verifies the management
// default: with no provisioner injected (NoopProvisioner), provisioning creates
// NO bucket and returns a clear bring-your-own-bucket error.
func TestProvisionManagedBucket_NoopProvisionerBringYourOwn(t *testing.T) {
	mem := NewMemProvider()
	st := NewMemStore()
	svc := &Service{
		Store: st,
		ProviderForAccount: func(_ context.Context, _ string) (Provider, error) {
			return mem, nil
		},
		// Provisioner left nil → provisioner() returns NoopProvisioner.
	}

	ulid := "01HZBYO0001"
	_, err := svc.ProvisionManagedBucket(context.Background(), "acct-byo", ulid, ProvisionOptions{})
	if err == nil {
		t.Fatal("expected bring-your-own-bucket error with the NoOp provisioner, got nil")
	}
	if !errors.Is(err, storageport.ErrProvisioningDisabled) {
		t.Fatalf("expected ErrProvisioningDisabled, got %v", err)
	}
	// No bucket must have been created (ListBucket returns ErrUnknownBucket for a
	// bucket that does not exist; PutObject would auto-create and mask this).
	if _, err := mem.ListBucket(context.Background(), managedBucketName(ulid), "", 1); !errors.Is(err, ErrUnknownBucket) {
		t.Error("a bucket was created despite the NoOp provisioner — management must never provision")
	}
	// And no config row must have been written.
	if _, err := st.GetConfig(context.Background(), "acct-byo"); !errors.Is(err, ErrUnknownAccount) {
		t.Errorf("a storage_configs row was written despite provisioning being disabled: %v", err)
	}
}

// TestProvisionBucketIfAbsent_SQLStore verifies provision + store round-trip
// using SQLStore (persistent across two calls).
func TestProvisionBucketIfAbsent_SQLStore(t *testing.T) {
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

	mem := NewMemProvider()
	svc := &Service{
		Store: st,
		ProviderForAccount: func(_ context.Context, _ string) (Provider, error) {
			return mem, nil
		},
		Provisioner: ProvisionerFromProvider(mem),
	}

	ctx := context.Background()
	ulid := "SQLPROV001"
	accountID := "acct-sql-prov"

	cfg, err := svc.ProvisionBucketIfAbsent(ctx, accountID, ulid, ProvisionOptions{})
	if err != nil {
		t.Fatalf("ProvisionBucketIfAbsent (SQLStore): %v", err)
	}
	expectedBucket := "vulos-" + strings.ToLower(ulid)
	if cfg.Bucket != expectedBucket {
		t.Errorf("expected bucket %q, got %q", expectedBucket, cfg.Bucket)
	}

	// Round-trip: GetConfig should return the provisioned row.
	stored, err := st.GetConfig(ctx, accountID)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if stored.Bucket != expectedBucket {
		t.Errorf("stored bucket mismatch: %q", stored.Bucket)
	}
}

// TestProvisionManagedBucket_RegionOption verifies that ProvisionOptions.Region
// is honoured: the persisted Config.Region matches what was passed in (COMPLY-01).
func TestProvisionManagedBucket_RegionOption(t *testing.T) {
	mem := NewMemProvider()
	st := NewMemStore()
	svc := &Service{
		Store: st,
		ProviderForAccount: func(_ context.Context, _ string) (Provider, error) {
			return mem, nil
		},
		Provisioner: ProvisionerFromProvider(mem),
	}

	ctx := context.Background()
	ulid := "01HZREGION001"
	accountID := "acct-region"

	cfg, err := svc.ProvisionManagedBucket(ctx, accountID, ulid, ProvisionOptions{Region: "eu-west-1"})
	if err != nil {
		t.Fatalf("ProvisionManagedBucket: %v", err)
	}
	// The row records the CANONICAL region (one vocabulary across the CP), so
	// assert the region itself rather than a particular spelling of it.
	if !region.Same(cfg.Region, "eu-west-1") {
		t.Errorf("expected the eu-west-1 region, got %q", cfg.Region)
	}
	// COMPLY-01: the region must have been ENFORCED at the provider, not merely
	// written to the row. This is what the residency guarantee actually rests on.
	if got := mem.BucketRegion(managedBucketName(ulid)); got != "eu-west-1" {
		t.Errorf("bucket placed in region %q, want eu-west-1 — a recorded region that was never placed is not residency", got)
	}

	// Verify persisted row has the same region.
	stored, err := st.GetConfig(ctx, accountID)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if !region.Same(stored.Region, "eu-west-1") {
		t.Errorf("stored Region mismatch: got %q", stored.Region)
	}
}

// A provider that cannot place buckets by region must not be able to accept a
// residency requirement: recording a region we never enforced turns an unkept
// promise into a compliance record.
func TestProvisionManagedBucket_UnplaceableRegionFailsClosed(t *testing.T) {
	svc := &Service{
		Store: NewMemStore(),
		Provisioner: ProvisionerFromProvider(
			nonRegionalProvider{Provider: NewMemProvider()},
		),
	}
	_, err := svc.ProvisionManagedBucket(context.Background(), "acct", "01HZNOREGION", ProvisionOptions{Region: "eu-west-1"})
	if err == nil {
		t.Fatal("provisioning succeeded with a residency region the provider cannot place — the row would claim a residency the data does not have")
	}
	// An unknown region is likewise refused, rather than travelling onward.
	if _, err := svc.ProvisionManagedBucket(context.Background(), "acct", "01HZBADREGION", ProvisionOptions{Region: "atlantis"}); err == nil {
		t.Fatal("provisioning succeeded with an unknown region")
	}
}

// nonRegionalProvider is a Provider WITHOUT the RegionalBucketProvider
// capability: embedding the INTERFACE gives it every Provider method while
// deliberately not carrying EnsureBucketInRegion, so the type assertion in
// ensureBucketPlaced fails — exactly as it would for a real object store that
// cannot choose a bucket's region.
type nonRegionalProvider struct{ Provider }

// TestProvisionManagedBucket_DefaultRegion verifies that an empty opts.Region
// falls back to "auto".
func TestProvisionManagedBucket_DefaultRegion(t *testing.T) {
	mem := NewMemProvider()
	st := NewMemStore()
	svc := &Service{
		Store: st,
		ProviderForAccount: func(_ context.Context, _ string) (Provider, error) {
			return mem, nil
		},
		Provisioner: ProvisionerFromProvider(mem),
	}

	cfg, err := svc.ProvisionManagedBucket(context.Background(), "acct-default-region", "01HZDEFAULT", ProvisionOptions{})
	if err != nil {
		t.Fatalf("ProvisionManagedBucket: %v", err)
	}
	if cfg.Region != "auto" {
		t.Errorf("expected default Region='auto', got %q", cfg.Region)
	}
}
