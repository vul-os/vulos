// Package lease implements a bucket-backed distributed lease primitive with
// monotonic fencing tokens.
//
// Design (see roadmap/COORDINATION.md):
//
//   - One always-present S3 object per scope: leases/<scope>.json
//   - All mutations use If-Match <etag> CAS — never If-None-Match:* (MinIO #20346 wontfix).
//   - The object is pre-created at cluster init (LEASE-02); this package assumes it exists.
//   - Fence is a monotonic int64 bumped on every Acquire and Renew.
//   - Works on AWS S3 (SigV4), MinIO, and Tigris (single/multi-region bucket required for Tigris).
package lease

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ── Sentinel errors ───────────────────────────────────────────────────────────

// ErrLost is returned when the If-Match CAS fails (412 Precondition Failed).
// The caller lost the race and should back off before retrying.
var ErrLost = errors.New("lease: CAS lost (412); back off and retry")

// ErrNotHolder is returned when an operation requires the caller to hold the
// lease but the lease records a different holder.
var ErrNotHolder = errors.New("lease: caller is not the current holder")

// ErrStaleFence is returned when a protected resource rejects a holder whose
// fence token is lower than the resource's last-seen fence.
var ErrStaleFence = errors.New("lease: stale fence token rejected")

// ErrLeaseMissing is returned when the lease object does not exist in the
// bucket.  The object should have been pre-created at cluster init (LEASE-02).
var ErrLeaseMissing = errors.New("lease: object missing from bucket; was it created at cluster init?")

// ── Wire types ────────────────────────────────────────────────────────────────

type leaseState string

const (
	stateFree leaseState = "free"
	stateHeld leaseState = "held"
)

// leaseDoc is the JSON body stored at leases/<scope>.json.
type leaseDoc struct {
	State     leaseState `json:"state"`
	Holder    string     `json:"holder"`
	Fence     int64      `json:"fence"`
	ExpiresAt time.Time  `json:"expires_at"`
}

// ── Public types ──────────────────────────────────────────────────────────────

// Held is a token returned by Acquire that proves current ownership.
// Pass it to Renew and Release.  Never mutate the fields directly.
type Held struct {
	Scope    string
	HolderID string
	Fence    int64  // monotonic; hand this to whatever the lease guards
	etag     string // the ETag of the object version this Held was written with
}

// S3Config is the minimum configuration needed to reach an S3-compatible store.
// It mirrors the shape used by backend/services/cluster for consistency.
type S3Config struct {
	Endpoint  string // e.g. "localhost:9000" or "s3.amazonaws.com"
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool

	// BucketType declares the Tigris replication class when using a Tigris
	// endpoint.  Valid values: "single-region", "multi-region", "global",
	// "dual-region".  Leave empty if the backend is not Tigris.
	// Global and Dual-region buckets are eventually consistent and unsafe for
	// CAS leasing — see guard.go and roadmap/COORDINATION.md.
	BucketType TigrisBucketType

	// StrictConsistency controls what happens when an unsafe Tigris bucket type
	// is detected.  false (default) = warn loudly but allow construction.
	// true = return ErrUnsafeBucket and refuse to create the Manager.
	StrictConsistency bool
}

// Configured returns true when credentials are present.
func (c S3Config) Configured() bool {
	return c.AccessKey != "" && c.SecretKey != "" && c.Bucket != ""
}

// ── Backend interface (enables mocking in tests) ──────────────────────────────

// backend abstracts the S3 operations used by Manager.
// The production implementation wraps minio.Client; tests supply mockBackend.
type backend interface {
	// getWithETag fetches the object body and its current ETag.
	// Returns ErrLeaseMissing when the key does not exist.
	getWithETag(ctx context.Context, key string) (body []byte, etag string, err error)

	// putIfMatch overwrites the object only when the current ETag equals matchETag.
	// Returns ErrLost on 412 Precondition Failed.
	// MUST NOT use If-None-Match:* anywhere.
	putIfMatch(ctx context.Context, key string, body []byte, matchETag string) (newETag string, err error)

	// putUnconditional writes the object without any If-Match / If-None-Match
	// condition.  Used only by EnsureLease for the initial bootstrap PUT.
	// Callers must check for existence first; this is NOT safe for concurrent
	// lease mutation — use putIfMatch for that.
	putUnconditional(ctx context.Context, key string, body []byte) error
}

// ── Manager ───────────────────────────────────────────────────────────────────

// Manager operates on lease objects in the configured S3 bucket.
// Construct with New or, in tests, NewWithBackend.
type Manager struct {
	b      backend
	bucket string // kept for reference; actual bucket routing is inside b
}

// New creates a Manager using a real MinIO/S3 client.
//
// When cfg.Endpoint resolves to a Tigris backend, New checks the bucket
// consistency class via CheckConsistency.  If the bucket type is eventually
// consistent (Global or Dual-region) and cfg.StrictConsistency is true, New
// returns ErrUnsafeBucket and refuses to create the Manager.  With the default
// (StrictConsistency=false) a loud warning is logged instead.
func New(cfg S3Config) (*Manager, error) {
	if !cfg.Configured() {
		return nil, errors.New("lease: S3 not configured")
	}
	if err := CheckConsistency(ConsistencyConfig{
		Endpoint:   cfg.Endpoint,
		BucketType: cfg.BucketType,
		StrictMode: cfg.StrictConsistency,
	}); err != nil {
		return nil, err
	}
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("lease: minio.New: %w", err)
	}
	return &Manager{
		b:      &minioBackend{mc: mc, bucket: cfg.Bucket},
		bucket: cfg.Bucket,
	}, nil
}

// NewWithBackend creates a Manager backed by b.  Intended for tests.
func NewWithBackend(b backend) *Manager {
	return &Manager{b: b}
}

// objectKey returns the S3 key for a given scope.
func objectKey(scope string) string {
	return "leases/" + scope + ".json"
}

// Acquire attempts to acquire the lease for scope on behalf of holderID.
//
//   - Reads the current object + ETag.
//   - If state == "held" and not expired, returns ErrNotHolder (someone else holds it).
//   - If state == "free" (or held-but-expired), PUTs held+fence+1 with If-Match.
//   - On 412 (lost race) returns ErrLost — caller should back off and retry.
func (m *Manager) Acquire(ctx context.Context, scope, holderID string, ttl time.Duration) (*Held, error) {
	key := objectKey(scope)

	body, etag, err := m.b.getWithETag(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("lease.Acquire: read: %w", err)
	}

	var doc leaseDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("lease.Acquire: unmarshal: %w", err)
	}

	now := time.Now().UTC()
	if doc.State == stateHeld && doc.Holder != holderID && now.Before(doc.ExpiresAt) {
		return nil, ErrNotHolder
	}

	newFence := doc.Fence + 1
	newDoc := leaseDoc{
		State:     stateHeld,
		Holder:    holderID,
		Fence:     newFence,
		ExpiresAt: now.Add(ttl),
	}

	newBody, err := json.Marshal(newDoc)
	if err != nil {
		return nil, fmt.Errorf("lease.Acquire: marshal: %w", err)
	}

	newETag, err := m.b.putIfMatch(ctx, key, newBody, etag)
	if err != nil {
		return nil, err // ErrLost or other error, already wrapped
	}

	return &Held{
		Scope:    scope,
		HolderID: holderID,
		Fence:    newFence,
		etag:     newETag,
	}, nil
}

// Renew refreshes the lease TTL and bumps the fence token.
// held must have been obtained from Acquire on this Manager.
// Returns ErrNotHolder if the current object disagrees on holder or fence.
// Returns ErrLost on 412 (concurrent mutation).
func (m *Manager) Renew(ctx context.Context, held *Held, ttl time.Duration) error {
	key := objectKey(held.Scope)

	body, etag, err := m.b.getWithETag(ctx, key)
	if err != nil {
		return fmt.Errorf("lease.Renew: read: %w", err)
	}

	var doc leaseDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("lease.Renew: unmarshal: %w", err)
	}

	if doc.State != stateHeld || doc.Holder != held.HolderID || doc.Fence != held.Fence {
		return ErrNotHolder
	}

	newFence := doc.Fence + 1
	newDoc := leaseDoc{
		State:     stateHeld,
		Holder:    held.HolderID,
		Fence:     newFence,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}

	newBody, err := json.Marshal(newDoc)
	if err != nil {
		return fmt.Errorf("lease.Renew: marshal: %w", err)
	}

	newETag, err := m.b.putIfMatch(ctx, key, newBody, etag)
	if err != nil {
		return err
	}

	// Update held in-place so caller has the new fence and can keep using it.
	held.Fence = newFence
	held.etag = newETag
	return nil
}

// Release releases the lease, flipping state back to "free".
// held must have been obtained from Acquire on this Manager.
// Returns ErrNotHolder if the object has changed holder or fence since Acquire/Renew.
// Returns ErrLost on 412.
func (m *Manager) Release(ctx context.Context, held *Held) error {
	key := objectKey(held.Scope)

	body, etag, err := m.b.getWithETag(ctx, key)
	if err != nil {
		return fmt.Errorf("lease.Release: read: %w", err)
	}

	var doc leaseDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("lease.Release: unmarshal: %w", err)
	}

	if doc.State != stateHeld || doc.Holder != held.HolderID || doc.Fence != held.Fence {
		return ErrNotHolder
	}

	// fence is unchanged on release per design (see COORDINATION.md)
	newDoc := leaseDoc{
		State:     stateFree,
		Holder:    "",
		Fence:     doc.Fence,
		ExpiresAt: time.Time{},
	}

	newBody, err := json.Marshal(newDoc)
	if err != nil {
		return fmt.Errorf("lease.Release: marshal: %w", err)
	}

	_, err = m.b.putIfMatch(ctx, key, newBody, etag)
	return err
}

// ── Bootstrap helper ──────────────────────────────────────────────────────────

// EnsureLease creates the lease object for scope in the "free" state if it does
// not already exist.  It is safe to call multiple times (idempotent):
//
//   - If the object is absent (404 / NoSuchKey) it writes the initial free doc.
//   - If the object already exists in any state it does nothing — a held lease
//     is never clobbered.
//
// This intentionally avoids If-None-Match:* (MinIO #20346 wontfix) and instead
// uses a GET-then-conditional-PUT pattern.  The tiny race window at first bootstrap
// (two nodes both see 404 and both PUT) is harmless: both write an identical free
// doc, leaving the object in the correct state.
//
// EnsureLease must be called once at cluster init / storage provisioning before any
// Acquire / Renew / Release operation on scope.
func (m *Manager) EnsureLease(ctx context.Context, scope string) error {
	key := objectKey(scope)

	// Step 1: check whether the object already exists.
	_, _, err := m.b.getWithETag(ctx, key)
	if err == nil {
		// Object exists (in any state) — nothing to do.
		return nil
	}
	if !errors.Is(err, ErrLeaseMissing) {
		return fmt.Errorf("lease.EnsureLease: probe %q: %w", key, err)
	}

	// Step 2: object is absent — write the initial free doc unconditionally.
	initDoc := leaseDoc{
		State:     stateFree,
		Holder:    "",
		Fence:     0,
		ExpiresAt: time.Time{},
	}
	body, err := json.Marshal(initDoc)
	if err != nil {
		return fmt.Errorf("lease.EnsureLease: marshal: %w", err)
	}

	if err := m.b.putUnconditional(ctx, key, body); err != nil {
		return fmt.Errorf("lease.EnsureLease: create %q: %w", key, err)
	}
	return nil
}

// ── Fencing helper ────────────────────────────────────────────────────────────

// ValidateFence checks that fence is not stale relative to lastSeen.
// A protected resource should store the highest fence it has ever seen and
// call ValidateFence on every operation from a holder.
//
//	var lastFence int64
//	if err := lease.ValidateFence(held.Fence, lastFence); err != nil { ... }
//	lastFence = held.Fence
func ValidateFence(incoming, lastSeen int64) error {
	if incoming < lastSeen {
		return fmt.Errorf("%w: incoming=%d lastSeen=%d", ErrStaleFence, incoming, lastSeen)
	}
	return nil
}

// ── Production backend (minio) ────────────────────────────────────────────────

type minioBackend struct {
	mc     *minio.Client
	bucket string
}

func (b *minioBackend) getWithETag(ctx context.Context, key string) ([]byte, string, error) {
	obj, err := b.mc.GetObject(ctx, b.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		errResp := minio.ToErrorResponse(err)
		if errResp.Code == "NoSuchKey" {
			return nil, "", ErrLeaseMissing
		}
		return nil, "", fmt.Errorf("lease: get %q: %w", key, err)
	}
	defer obj.Close()

	// Stat to obtain the ETag before reading body.
	info, err := obj.Stat()
	if err != nil {
		errResp := minio.ToErrorResponse(err)
		if errResp.Code == "NoSuchKey" {
			return nil, "", ErrLeaseMissing
		}
		return nil, "", fmt.Errorf("lease: stat %q: %w", key, err)
	}

	body, err := io.ReadAll(obj)
	if err != nil {
		return nil, "", fmt.Errorf("lease: read %q: %w", key, err)
	}

	// minio returns ETags with surrounding quotes; strip them for consistency.
	etag := strings.Trim(info.ETag, "\"")
	return body, etag, nil
}

func (b *minioBackend) putUnconditional(ctx context.Context, key string, body []byte) error {
	_, err := b.mc.PutObject(ctx, b.bucket, key, bytes.NewReader(body), int64(len(body)),
		minio.PutObjectOptions{ContentType: "application/json"})
	if err != nil {
		return fmt.Errorf("lease: unconditional put %q: %w", key, err)
	}
	return nil
}

func (b *minioBackend) putIfMatch(ctx context.Context, key string, body []byte, matchETag string) (string, error) {
	opts := minio.PutObjectOptions{
		ContentType: "application/json",
	}
	// SetMatchETag sets the If-Match header — never If-None-Match:*.
	opts.SetMatchETag(matchETag)

	info, err := b.mc.PutObject(ctx, b.bucket, key, bytes.NewReader(body), int64(len(body)), opts)
	if err != nil {
		errResp := minio.ToErrorResponse(err)
		if errResp.Code == "PreconditionFailed" {
			return "", ErrLost
		}
		return "", fmt.Errorf("lease: put %q: %w", key, err)
	}

	newETag := strings.Trim(info.ETag, "\"")
	return newETag, nil
}
