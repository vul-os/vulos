// minter.go — CredentialMinter seam (STORAGE-ISOLATION.md §1.2).
//
// The CP holds ONE master the S3 backend credential that can read/write EVERY bucket.
// Handing that master credential to a managed cell (see the historical TODO in
// managed/mailcell.go) means a single leaked cell env exposes every tenant's
// bucket. CredentialMinter closes that gap: it mints a credential scoped to ONE
// bucket, so a compromised cell touches one bucket, not all of them.
//
// Three tiers, in preference order (§1.2):
//   - Tier 1: the IAM minter (cloud) — a real bucket-scoped access key via the managed S3 IAM API
//     IAM API (create access key + attach a policy pinned to arn:.../vulos-{ulid}/*).
//     Primary, used when the configured credentials / minting is configured.
//   - Tier 2: STS/session token — same seam, SessionToken populated, short TTL.
//     Kept as a future CredentialMinter backend swap (the S3 backend does not yet expose
//     an AssumeRole surface — verified 2026-07, see the cloud IAM minter).
//   - Tier 3: presigned-URL fallback FLOOR — no minter at all. When
//     CredentialMinter is nil the cell gets NO key and uses the existing
//     presign-broker path (routes_storage.go). This is the guaranteed floor and
//     is the shipped default until a the S3 backend IAM master key is configured.
//
// This file is additive: it does NOT modify the frozen contract.go. MemMinter
// and StaticFallbackMinter are the reference implementations.
package storage

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// ScopedCred is a bucket-scoped, optionally time-limited S3 credential minted by
// a CredentialMinter. It carries everything a cell needs to build its own S3
// client against exactly one bucket.
//
// SessionToken is empty for long-lived scoped access keys (Tier 1) and populated
// for STS/session tokens (Tier 2). ExpiresAt is zero for non-expiring scoped keys.
type ScopedCred struct {
	ID              string    // stable provider access-key id (for Revoke/rotate bookkeeping)
	AccessKeyID     string    // S3 access key id
	SecretAccessKey string    // S3 secret — NEVER serialised to JSON (see json:"-")
	SessionToken    string    // empty for long-lived scoped keys
	Endpoint        string    // S3 endpoint (e.g. https://s3.example.invalid)
	Region          string    // S3 region
	Bucket          string    // the ONE bucket this cred is scoped to
	PolicyID        string    // provider policy id (for revoke cleanup); may be empty
	ExpiresAt       time.Time // zero = non-expiring scoped key
}

// SecretJSON returns a JSON-safe view of ScopedCred that OMITS the secret. Use
// this anywhere a ScopedCred might be logged or serialised.
func (c ScopedCred) Redacted() ScopedCred {
	c.SecretAccessKey = ""
	c.SessionToken = ""
	return c
}

// CredentialMinter mints a bucket-scoped, time-limited S3 credential.
//
// Implementations:
//   - the IAM minter (cloud)    — Tier 1, real scoped access key (production).
//   - StaticFallbackMinter — dev/self-host only: returns the master cred as-is
//     (== today's behaviour). NOT least-privilege; use only where the IAM API is
//     unavailable and the presign-broker floor is not desired.
//   - MemMinter          — tests: returns deterministic fake creds, records mints
//     and revokes.
type CredentialMinter interface {
	// MintBucketScoped returns creds that permit ONLY the S3 actions Vulos needs
	// (Get/Put/Delete/List/HEAD) on exactly the given buckets, expiring at/after
	// ttl (ttl <= 0 means non-expiring where the backend supports it). The first
	// bucket is recorded as ScopedCred.Bucket; additional buckets (e.g. the
	// per-customer backup bucket, §2.3) are also covered by the policy.
	MintBucketScoped(ctx context.Context, buckets []string, ttl time.Duration) (ScopedCred, error)
	// Revoke invalidates a previously minted credential by its ScopedCred.ID
	// (best-effort; also cleans up an attached policy when policyID is non-empty).
	Revoke(ctx context.Context, credID, policyID string) error
}

// ---------------------------------------------------------------------------
// StaticFallbackMinter — dev/self-host: returns the master cred unchanged.
// ---------------------------------------------------------------------------

// StaticFallbackMinter is a CredentialMinter that returns the master credential
// unchanged for every bucket. It provides NO isolation — it exists only so the
// mint seam can be wired in environments without the managed S3 IAM API IAM API while still
// injecting a (non-scoped) key, and for the interim direct-inject path. Prefer
// the presign-broker floor (nil minter) over this where isolation matters.
type StaticFallbackMinter struct {
	AccessKeyID     string
	SecretAccessKey string
	Endpoint        string
	Region          string
}

// MintBucketScoped returns the master credential, tagged with the first bucket.
func (m StaticFallbackMinter) MintBucketScoped(_ context.Context, buckets []string, _ time.Duration) (ScopedCred, error) {
	bucket := ""
	if len(buckets) > 0 {
		bucket = buckets[0]
	}
	return ScopedCred{
		ID:              "static",
		AccessKeyID:     m.AccessKeyID,
		SecretAccessKey: m.SecretAccessKey,
		Endpoint:        m.Endpoint,
		Region:          m.Region,
		Bucket:          bucket,
	}, nil
}

// Revoke is a no-op: the static master credential is never revoked here.
func (m StaticFallbackMinter) Revoke(_ context.Context, _, _ string) error { return nil }

// compile-time assertion.
var _ CredentialMinter = StaticFallbackMinter{}

// ---------------------------------------------------------------------------
// MemMinter — test double: deterministic fake creds + mint/revoke bookkeeping.
// ---------------------------------------------------------------------------

// MemMinter is an in-memory CredentialMinter for tests. It returns deterministic
// fake creds and records every mint + revoke so tests can assert the lifecycle
// (mint-at-provision, revoke-on-destroy).
type MemMinter struct {
	mu      sync.Mutex
	seq     int
	minted  map[string]ScopedCred // credID -> cred
	revoked map[string]bool       // credID -> revoked?
	// ForceErr, when non-nil, is returned by MintBucketScoped (to exercise the
	// mint-fails → presign-broker fallback path).
	ForceErr error
}

// NewMemMinter returns an empty MemMinter.
func NewMemMinter() *MemMinter {
	return &MemMinter{minted: map[string]ScopedCred{}, revoked: map[string]bool{}}
}

func (m *MemMinter) MintBucketScoped(_ context.Context, buckets []string, ttl time.Duration) (ScopedCred, error) {
	if m.ForceErr != nil {
		return ScopedCred{}, m.ForceErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	id := fmt.Sprintf("mem-%d", m.seq)
	bucket := ""
	if len(buckets) > 0 {
		bucket = buckets[0]
	}
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().UTC().Add(ttl)
	}
	c := ScopedCred{
		ID:              id,
		AccessKeyID:     "AKMEM" + id,
		SecretAccessKey: "secretMEM" + id,
		Endpoint:        "mem://iam",
		Region:          "auto",
		Bucket:          bucket,
		PolicyID:        "pol-" + id,
		ExpiresAt:       exp,
	}
	m.minted[id] = c
	return c, nil
}

func (m *MemMinter) Revoke(_ context.Context, credID, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revoked[credID] = true
	return nil
}

// WasRevoked reports whether Revoke was called for credID (test helper).
func (m *MemMinter) WasRevoked(credID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.revoked[credID]
}

// MintCount returns the number of creds minted (test helper).
func (m *MemMinter) MintCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.minted)
}

// compile-time assertion.
var _ CredentialMinter = (*MemMinter)(nil)
