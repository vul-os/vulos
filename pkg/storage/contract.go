// Package storage is the vulos.cloud control-plane storage subsystem.
//
// THIS FILE IS THE FROZEN CONTRACT. Do not edit it in feature branches —
// every storage agent builds against these types, this Store/Provider
// interface, the reference MemProvider/MemStore (which DEFINE semantics),
// and sentinel errors. SQL and S3Provider implementations must match
// the in-memory reference behaviourally; handlers depend only on the
// interfaces.
//
// Risk callout (R-B): S3Provider relies on the S3 backend S3-compatible API
// contracts at https://fly.storage.managed.dev. If the S3 backend changes its API
// surface the S3Provider must be updated; MemProvider is unaffected.
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/storageport"
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// Config holds per-account storage configuration. SecretKey is decrypted
// in-memory from secret_key_enc; it is NEVER serialised to JSON.
type Config struct {
	AccountID string `json:"account_id"`
	BYO       bool   `json:"byo"`      // false = vulos-managed object storage bucket
	Endpoint  string `json:"endpoint"` // empty when byo=false
	Region    string `json:"region"`   // default "auto"
	Bucket    string `json:"bucket"`
	AccessKey string `json:"access_key"` // empty when byo=false
	SecretKey string `json:"-"`          // decrypted; never in JSON
}

// UsageSample records a point-in-time metering snapshot for an account bucket.
type UsageSample struct {
	AccountID   string    `json:"account_id"`
	Bucket      string    `json:"bucket"`
	SizeBytes   int64     `json:"size_bytes"`
	ObjectCount int64     `json:"object_count"`
	SampledAt   time.Time `json:"sampled_at"`
}

// ObjectInfo describes a single object in a bucket listing.
type ObjectInfo struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"last_modified"`
}

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

var (
	ErrUnknownAccount = errors.New("storage: unknown account")
	ErrUnknownBucket  = errors.New("storage: unknown bucket")
	ErrProviderFailed = errors.New("storage: provider operation failed")
)

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// Store is the persistence contract for storage configs and usage samples.
// MemStore is the behavioural source of truth; SQLStore must match it.
type Store interface {
	GetConfig(ctx context.Context, accountID string) (Config, error)
	PutConfig(ctx context.Context, c Config) error
	DeleteConfig(ctx context.Context, accountID string) error
	InsertUsage(ctx context.Context, u UsageSample) error
	LatestUsage(ctx context.Context, accountID string) (UsageSample, error)
	Close() error
}

// ---------------------------------------------------------------------------
// Provider interface
// ---------------------------------------------------------------------------

// Provider wraps the S3 operations the cp actually needs. S3Provider
// is the production implementation; MemProvider is the test double.
type Provider interface {
	EnsureBucket(ctx context.Context, name string) error
	PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (url string, err error)
	PresignPut(ctx context.Context, bucket, key string, ttl time.Duration) (url string, err error)
	ListBucket(ctx context.Context, bucket, prefix string, max int) ([]ObjectInfo, error)
	GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	PutObject(ctx context.Context, bucket, key string, r io.Reader, size int64) error
	DeleteObject(ctx context.Context, bucket, key string) error
	// BucketSize returns (sizeBytes, objectCount, error).
	BucketSize(ctx context.Context, bucket string) (int64, int64, error)
}

// ---------------------------------------------------------------------------
// SnapshotURLer — cross-package seam to managed (#6)
// ---------------------------------------------------------------------------

// SnapshotURLer is the interface managed (#6) uses to obtain a presigned
// snapshot URL without importing this package directly. Service implements it.
// DEFINED here; IMPORTED by managed.
type SnapshotURLer interface {
	SnapshotURL(ctx context.Context, accountID, ulid string) (string, error)
}

// ---------------------------------------------------------------------------
// Service — ties Store + Provider + per-account config selection
// ---------------------------------------------------------------------------

// Service is the top-level façade used by HTTP handlers. It holds the
// metadata Store, a function that selects the correct Provider per account
// (managed vs. BYO), and the key-encryption key for BYO secret_key_enc.
//
// STORAGE-QUOTA-GATE-01: the ONLY storage-quota gate is
// billing.Store.CheckStorageQuotaH (BILLING-LOCATION-01) — called directly by
// the HTTP layer (routes_storage.go) before issuing a presigned PUT. This
// Service previously carried a SECOND gate (QuotaSource/CheckStorageQuota,
// removed) that consulted a stale, disconnected allowance table (billing's
// now-removed StorageAllowedGB: 2 GB for "team"/10 GB for "enterprise" —
// numbers that had drifted from quota_table.go's real 500 GB/1000 GB ladder
// years ago) and was BYO-unaware. It was next to a no-op for free/personal/pro
// (fail-open whenever allowedGB<=0) and would have WRONGLY blocked team/
// enterprise uploads between 2 GB/10 GB and their real 500 GB/1000 GB
// allowance had it not been shadowed by the correct gate running first. Two
// gates enforcing two different numbers on the same request is exactly the
// "conflicting storage gates" bug flagged in the audit — reconciled to one.
type Service struct {
	Store
	// ProviderForAccount returns the Provider for the given accountID.
	// It must consult the store config and return S3Provider (for managed)
	// or a provider built from the BYO config.
	//
	// NOTE: this is the DATA-PLANE selector (presign/get/put/list). Bucket
	// CREATION does NOT go through here — it goes through Provisioner, the
	// storageport seam, so the operational control plane never creates a bucket
	// on its own.
	ProviderForAccount func(ctx context.Context, accountID string) (Provider, error)
	// Provisioner creates managed object-storage buckets through the storageport
	// seam. This is the ONE storage capability the operational control plane must
	// never perform on its own: the management default is NoopProvisioner
	// (bring-your-own-bucket — provisioning returns a "create it first" error and
	// no bucket is created), while the cloud composition root injects a
	// Tigris-backed StorageProvisioner. A nil value is treated as NoopProvisioner
	// (see provisioner()).
	Provisioner storageport.StorageProvisioner
	// KEK is the 32-byte AES-GCM key-encryption key read from STORAGE_KEK env.
	// Empty means BYO secret encryption is disabled (dev mode).
	KEK []byte
}

// provisioner returns the injected StorageProvisioner, or the bring-your-own-bucket
// NoopProvisioner when none was injected. Management deployments leave it nil and
// thus never create a bucket; the cloud composition root injects a Tigris-backed
// provisioner.
func (s *Service) provisioner() storageport.StorageProvisioner {
	if s.Provisioner != nil {
		return s.Provisioner
	}
	return storageport.NewNoopProvisioner()
}

// maxSnapshotTTL is the maximum TTL for snapshot presigned URLs (5 minutes).
const maxSnapshotTTL = 5 * time.Minute

// managedBucketName returns the canonical the S3 backend bucket name for a ULID.
// Convention: "vulos-{ulid_lower}".
func managedBucketName(ulid string) string {
	return "vulos-" + strings.ToLower(ulid)
}

// SnapshotURL implements SnapshotURLer. It presigns a GET URL for the object
// "latest.snap.enc" in the managed bucket for the given account/ULID.
// TTL is clamped to ≤ 300 s.
func (s *Service) SnapshotURL(ctx context.Context, accountID, ulid string) (string, error) {
	p, err := s.ProviderForAccount(ctx, accountID)
	if err != nil {
		return "", fmt.Errorf("storage: SnapshotURL provider: %w", err)
	}
	bucket := managedBucketName(ulid)
	return p.PresignGet(ctx, bucket, "latest.snap.enc", maxSnapshotTTL)
}

// ---------------------------------------------------------------------------
// MemStore — in-memory reference Store
// ---------------------------------------------------------------------------

// MemStore is the in-memory reference Store. Concurrency-safe. It DEFINES
// the semantics every other implementation must match.
type MemStore struct {
	mu     sync.Mutex
	cfgs   map[string]Config      // accountID -> Config
	usages map[string]UsageSample // accountID -> latest sample
	// cell holds per-cell scoped credentials (storage_cell_creds), lazily
	// initialised so MemStore also satisfies CellCredStore (see cellcreds.go).
	// This is an orthogonal, additive capability — it does not affect the frozen
	// Store/Provider semantics defined here.
	cellOnce sync.Once
	cell     *memCellCreds
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{
		cfgs:   map[string]Config{},
		usages: map[string]UsageSample{},
	}
}

func (m *MemStore) GetConfig(_ context.Context, accountID string) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.cfgs[accountID]
	if !ok {
		return Config{}, ErrUnknownAccount
	}
	return c, nil
}

func (m *MemStore) PutConfig(_ context.Context, c Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfgs[c.AccountID] = c
	return nil
}

func (m *MemStore) DeleteConfig(_ context.Context, accountID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.cfgs, accountID)
	return nil
}

func (m *MemStore) InsertUsage(_ context.Context, u UsageSample) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usages[u.AccountID] = u
	return nil
}

func (m *MemStore) LatestUsage(_ context.Context, accountID string) (UsageSample, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.usages[accountID]
	if !ok {
		return UsageSample{}, ErrUnknownAccount
	}
	return u, nil
}

func (m *MemStore) Close() error { return nil }

// compile-time assertion: MemStore satisfies Store.
var _ Store = (*MemStore)(nil)

// ---------------------------------------------------------------------------
// MemProvider — in-memory reference Provider (test double)
// ---------------------------------------------------------------------------

// memObject is a single stored object.
type memObject struct {
	data         []byte
	lastModified time.Time
}

// MemProvider is a concurrency-safe, in-memory Provider used by tests and by
// self-host/dev deployments with no object store configured.
// PresignGet/PresignPut return synthetic URLs of the form
// "mem://bucket/key?sig=<pseudo>"; they never contact a network.
type MemProvider struct {
	mu      sync.Mutex
	buckets map[string]map[string]memObject // bucket -> key -> object
	regions map[string]string               // bucket -> region it was placed in
}

// NewMemProvider returns an empty MemProvider.
func NewMemProvider() *MemProvider {
	return &MemProvider{
		buckets: map[string]map[string]memObject{},
		regions: map[string]string{},
	}
}

// EnsureBucket creates the named bucket if it does not already exist
// (idempotent).
func (m *MemProvider) EnsureBucket(ctx context.Context, name string) error {
	return m.EnsureBucketInRegion(ctx, name, "")
}

// EnsureBucketInRegion implements RegionalBucketProvider. The data never leaves
// this process, so "placement" is recording which region the bucket was asked
// for — enough to satisfy the residency contract honestly for a single-node
// deployment (the operator's own machine IS the region), and enough for tests to
// assert that the requested region reached the provider at all.
//
// A provider that stores data REMOTELY must genuinely place the bucket; one that
// cannot must simply not implement this interface, and provisioning with a
// residency requirement will then fail closed.
func (m *MemProvider) EnsureBucketInRegion(_ context.Context, name, s3Region string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.buckets[name]; !ok {
		m.buckets[name] = map[string]memObject{}
	}
	if s3Region != "" {
		m.regions[name] = s3Region
	}
	return nil
}

// BucketRegion reports the region a bucket was placed in ("" when none was
// requested). Test/observability accessor.
func (m *MemProvider) BucketRegion(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.regions[name]
}

// PresignGet returns a synthetic presigned GET URL. The bucket need not exist.
func (m *MemProvider) PresignGet(_ context.Context, bucket, key string, ttl time.Duration) (string, error) {
	return fmt.Sprintf("mem://%s/%s?op=get&ttl=%d", bucket, key, int64(ttl.Seconds())), nil
}

// PresignPut returns a synthetic presigned PUT URL. The bucket need not exist.
func (m *MemProvider) PresignPut(_ context.Context, bucket, key string, ttl time.Duration) (string, error) {
	return fmt.Sprintf("mem://%s/%s?op=put&ttl=%d", bucket, key, int64(ttl.Seconds())), nil
}

// ListBucket returns objects whose key has the given prefix, up to max entries.
// Returns ErrUnknownBucket if the bucket does not exist.
func (m *MemProvider) ListBucket(_ context.Context, bucket, prefix string, max int) ([]ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	objs, ok := m.buckets[bucket]
	if !ok {
		return nil, ErrUnknownBucket
	}
	var out []ObjectInfo
	for k, o := range objs {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			out = append(out, ObjectInfo{Key: k, Size: int64(len(o.data)), LastModified: o.lastModified})
			if max > 0 && len(out) >= max {
				break
			}
		}
	}
	return out, nil
}

// GetObject returns the stored object body. Returns ErrUnknownBucket /
// ErrUnknownBucket (for missing key) as appropriate.
func (m *MemProvider) GetObject(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	objs, ok := m.buckets[bucket]
	if !ok {
		return nil, ErrUnknownBucket
	}
	o, ok := objs[key]
	if !ok {
		return nil, fmt.Errorf("%w: key %q not found in bucket %q", ErrUnknownBucket, key, bucket)
	}
	return io.NopCloser(bytes.NewReader(o.data)), nil
}

// PutObject stores the object. Creates the bucket if necessary.
func (m *MemProvider) PutObject(_ context.Context, bucket, key string, r io.Reader, _ int64) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("%w: read body: %v", ErrProviderFailed, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.buckets[bucket]; !ok {
		m.buckets[bucket] = map[string]memObject{}
	}
	m.buckets[bucket][key] = memObject{data: data, lastModified: time.Now().UTC()}
	return nil
}

// DeleteObject removes an object. Returns ErrUnknownBucket if the bucket or
// key does not exist.
func (m *MemProvider) DeleteObject(_ context.Context, bucket, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	objs, ok := m.buckets[bucket]
	if !ok {
		return ErrUnknownBucket
	}
	if _, ok := objs[key]; !ok {
		return fmt.Errorf("%w: key %q", ErrUnknownBucket, key)
	}
	delete(objs, key)
	return nil
}

// BucketSize returns the total size and object count for the bucket.
// Returns ErrUnknownBucket if the bucket does not exist.
func (m *MemProvider) BucketSize(_ context.Context, bucket string) (int64, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	objs, ok := m.buckets[bucket]
	if !ok {
		return 0, 0, ErrUnknownBucket
	}
	var total int64
	for _, o := range objs {
		total += int64(len(o.data))
	}
	return total, int64(len(objs)), nil
}

// compile-time assertions.
var _ Provider = (*MemProvider)(nil)
var _ SnapshotURLer = (*Service)(nil)
