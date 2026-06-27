package storage

// Object-scoped, short-lived, ACL-gated storage grants (FILES-FOUNDATION).
//
// The gateway/seam already mints PREFIX-scoped credentials for storage-permitted
// apps (ResolveScoped + the MinIO STS minter). The Files control plane needs a
// finer grant: authorization to touch exactly ONE object — a single file in a
// user's Drive area — for a bounded time, and ONLY after the OS Files ACL has
// authorized the requesting user.
//
// This file adds that "direct-to-bucket" path WITHOUT weakening the existing
// posture:
//   - READ  → a presigned GET URL for the single object (no creds handed out).
//   - WRITE → an object-scoped STS credential (GetObject/PutObject/DeleteObject
//             on exactly that key), or, when STS is unavailable, a presigned PUT
//             URL. Never full-bucket, never prefix-wide.
//   - Standalone (no object store) → a LOCAL filesystem path under LocalRoot so
//             the OS itself serves the bytes; no cloud dependency.
//
// Grants always carry an explicit ExpiresAt. The ACL check lives in the Files
// service (services/files); this broker is the minting mechanism it calls only
// after authorization succeeds.

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// GrantType identifies how the bearer accesses the object.
type GrantType string

const (
	// GrantPresigned: URL is a presigned S3 URL (GET or PUT). No creds exposed.
	GrantPresigned GrantType = "presigned"
	// GrantSTS: Creds are object-scoped short-lived STS credentials (read/write).
	GrantSTS GrantType = "sts"
	// GrantLocal: LocalPath is a filesystem path under the local-FS fallback root.
	GrantLocal GrantType = "local"
)

// defaultGrantTTL is the lifetime of a minted object grant when the caller does
// not specify one. Short by design — re-request on expiry.
const defaultGrantTTL = 15 * time.Minute

// ObjectGrant is a short-lived authorization to access ONE object. Exactly one
// of {URL, Creds, LocalPath} is meaningful, per Type. It is safe to serialize to
// the client EXCEPT that Creds.SecretKey/SessionToken are sensitive (they are
// object-scoped + short-lived, the same trust level as the existing seam creds).
type ObjectGrant struct {
	Type      GrantType   `json:"type"`
	Method    string      `json:"method"` // "GET" or "PUT"
	Bucket    string      `json:"bucket"`
	Key       string      `json:"key"`
	URL       string      `json:"url,omitempty"`        // GrantPresigned
	Endpoint  string      `json:"endpoint,omitempty"`   // GrantSTS consumers
	Region    string      `json:"region,omitempty"`     // GrantSTS consumers
	Creds     ScopedCreds `json:"creds,omitempty"`      // GrantSTS
	LocalPath string      `json:"local_path,omitempty"` // GrantLocal
	ExpiresAt time.Time   `json:"expires_at"`
}

// GrantBroker mints object-scoped grants. It reuses the existing Resolver for
// per-user bucket/endpoint resolution and the same STS calling identity as the
// seam minter. A zero STSConfig.Endpoint disables STS write grants (the broker
// falls back to presigned PUT), exactly mirroring the seam's fail-soft behaviour.
type GrantBroker struct {
	resolver *Resolver
	sts      STSConfig
	ttl      time.Duration
}

// NewGrantBroker builds a broker bound to resolver. ttl<=0 uses defaultGrantTTL.
// sts may be a zero value to disable STS write grants (presigned-PUT fallback).
func NewGrantBroker(resolver *Resolver, sts STSConfig, ttl time.Duration) *GrantBroker {
	if ttl <= 0 {
		ttl = defaultGrantTTL
	}
	return &GrantBroker{resolver: resolver, sts: sts, ttl: ttl}
}

// resolveOwner returns the object-store binding for the bucket OWNER (the user
// whose Drive area holds the bytes). The grant is minted against the owner's
// bucket; the Files ACL is what authorizes a DIFFERENT user to receive it.
func (b *GrantBroker) resolveOwner(ctx context.Context, ownerID string) Resolution {
	return b.resolver.Resolve(ctx, ownerID)
}

// MintRead returns a read grant for bucket/key. When an object store is present
// it is a presigned GET URL; otherwise a local-FS path under LocalRoot. ttl<=0
// uses the broker default.
func (b *GrantBroker) MintRead(ctx context.Context, ownerID, bucket, key string, ttl time.Duration) (ObjectGrant, error) {
	if ttl <= 0 {
		ttl = b.ttl
	}
	res := b.resolveOwner(ctx, ownerID)
	exp := time.Now().Add(ttl)

	if !res.Configured() {
		return ObjectGrant{
			Type:      GrantLocal,
			Method:    "GET",
			Bucket:    bucket,
			Key:       key,
			LocalPath: b.localPath(key),
			ExpiresAt: exp,
		}, nil
	}

	mc, err := b.client(res)
	if err != nil {
		return ObjectGrant{}, err
	}
	u, err := mc.PresignedGetObject(ctx, bucket, key, ttl, nil)
	if err != nil {
		return ObjectGrant{}, fmt.Errorf("presign GET %s/%s: %w", bucket, key, err)
	}
	return ObjectGrant{
		Type:      GrantPresigned,
		Method:    "GET",
		Bucket:    bucket,
		Key:       key,
		URL:       u.String(),
		ExpiresAt: exp,
	}, nil
}

// MintWrite returns a write grant for bucket/key. Preference order:
//  1. object-scoped STS credentials (GetObject/PutObject/DeleteObject on exactly
//     key) when STS is configured — enables in-place collaborative edit;
//  2. a presigned PUT URL when an object store is present but STS is not;
//  3. a local-FS path under LocalRoot when no object store is configured.
func (b *GrantBroker) MintWrite(ctx context.Context, ownerID, bucket, key string, ttl time.Duration) (ObjectGrant, error) {
	if ttl <= 0 {
		ttl = b.ttl
	}
	res := b.resolveOwner(ctx, ownerID)
	exp := time.Now().Add(ttl)

	if !res.Configured() {
		return ObjectGrant{
			Type:      GrantLocal,
			Method:    "PUT",
			Bucket:    bucket,
			Key:       key,
			LocalPath: b.localPath(key),
			ExpiresAt: exp,
		}, nil
	}

	// Prefer object-scoped STS so the bearer never gets a prefix/bucket-wide cred.
	if sc, ok := b.mintObjectSTS(bucket, key, int(ttl.Seconds())); ok {
		return ObjectGrant{
			Type:      GrantSTS,
			Method:    "PUT",
			Bucket:    bucket,
			Key:       key,
			Endpoint:  res.Endpoint,
			Region:    res.Region,
			Creds:     sc,
			ExpiresAt: exp,
		}, nil
	}

	mc, err := b.client(res)
	if err != nil {
		return ObjectGrant{}, err
	}
	u, err := mc.PresignedPutObject(ctx, bucket, key, ttl)
	if err != nil {
		return ObjectGrant{}, fmt.Errorf("presign PUT %s/%s: %w", bucket, key, err)
	}
	return ObjectGrant{
		Type:      GrantPresigned,
		Method:    "PUT",
		Bucket:    bucket,
		Key:       key,
		URL:       u.String(),
		ExpiresAt: exp,
	}, nil
}

// mintObjectSTS performs an STS AssumeRole with an inline policy locked to the
// SINGLE object key (read+write). Returns (_, false) when STS is unconfigured or
// the call fails, so callers fall back to a presigned URL.
func (b *GrantBroker) mintObjectSTS(bucket, key string, durSec int) (ScopedCreds, bool) {
	if b.sts.Endpoint == "" || b.resolver.cfg.AccessKey == "" || b.resolver.cfg.SecretKey == "" {
		return ScopedCreds{}, false
	}
	if durSec <= 0 {
		durSec = 900
	}
	policy, err := objectScopedPolicy(bucket, key)
	if err != nil {
		return ScopedCreds{}, false
	}
	creds, err := credentials.NewSTSAssumeRole(b.sts.Endpoint, credentials.STSAssumeRoleOptions{
		AccessKey:       b.resolver.cfg.AccessKey,
		SecretKey:       b.resolver.cfg.SecretKey,
		Policy:          policy,
		DurationSeconds: durSec,
		RoleARN:         b.sts.RoleARN,
		RoleSessionName: stsSessionName(key),
		Location:        b.resolver.cfg.Region,
	})
	if err != nil {
		return ScopedCreds{}, false
	}
	v, err := creds.Get()
	if err != nil {
		return ScopedCreds{}, false
	}
	return ScopedCreds{
		AccessKey:    v.AccessKeyID,
		SecretKey:    v.SecretAccessKey,
		SessionToken: v.SessionToken,
	}, true
}

// client builds a minio client from a resolution (its static per-user-bucket
// credentials) for presigning. Presigning is a local signing operation; the
// resulting URL is scoped to the single object + expiry.
func (b *GrantBroker) client(res Resolution) (*minio.Client, error) {
	host, useSSL := res.S3Host()
	mc, err := minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(res.AccessKey, res.SecretKey, res.SessionToken),
		Secure: useSSL,
		Region: res.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("storage: minio client: %w", err)
	}
	return mc, nil
}

// localPath maps an object key to its location under the local-FS fallback root.
// The key is already the full Drive key ("<ownerID>/drive/..."), so the local
// layout mirrors the bucket layout. filepath.Clean defends against traversal.
func (b *GrantBroker) localPath(key string) string {
	return filepath.Join(b.resolver.LocalRoot(), filepath.Clean("/"+key))
}
