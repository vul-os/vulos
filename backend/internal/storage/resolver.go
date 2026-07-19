package storage

import (
	"context"
	"os"
	"strings"
	"vulos/backend/internal/datadir"
)

// Resolution is the per-user object-store binding handed to OS subsystems and,
// via the gateway, to apps that declare the "storage" permission. Its fields map
// one-for-one onto the X-Vulos-Storage-* request-header contract.
//
// Endpoint is an S3 URL (scheme://host[:port]). An EMPTY Endpoint signals the
// consumer to fall back to local/standalone storage (no object store available).
type Resolution struct {
	Endpoint     string // S3 URL; empty => local-FS fallback
	Bucket       string // per-user account bucket (shared across the box / user)
	Prefix       string // key prefix within the bucket (e.g. "os/", "office/")
	Region       string
	AccessKey    string
	SecretKey    string
	SessionToken string // optional (STS / temporary credentials)
	// Scoped reports whether AccessKey/SecretKey/SessionToken (when non-empty)
	// are scoped DOWN to exactly Bucket/Prefix (e.g. minted via STS AssumeRole
	// with an inline session policy) as opposed to the box's static, full-
	// bucket credential. Only ResolveScoped ever sets this true, and only when
	// a CredentialMinter actually succeeded. Consumers that inject credentials
	// into a storage-permitted app (the gateway) MUST treat Scoped=false as
	// "no usable credential" and refuse to inject anything — a storage-
	// permitted app must never receive a static full-bucket credential
	// (SECURITY, per-app isolation).
	Scoped bool
}

// Configured reports whether the resolution points at a usable object store.
// When false, consumers fall back to local filesystem storage.
func (r Resolution) Configured() bool {
	return r.Endpoint != "" && r.Bucket != ""
}

// WithPrefix returns a copy of r whose Prefix is set to p, normalised to a
// single trailing slash (empty stays empty). The OS uses "os/"; the gateway
// stamps "<appID>/" for each storage-permitted app.
func (r Resolution) WithPrefix(p string) Resolution {
	r.Prefix = normalizePrefix(p)
	return r
}

// S3Host splits the URL Endpoint back into a host[:port] and a UseSSL flag,
// the shape expected by the minio-go based clients (cluster, lease). Returns
// ("", false) for an empty/local-fallback Endpoint.
func (r Resolution) S3Host() (host string, useSSL bool) {
	e := r.Endpoint
	switch {
	case e == "":
		return "", false
	case strings.HasPrefix(e, "https://"):
		return strings.TrimPrefix(e, "https://"), true
	case strings.HasPrefix(e, "http://"):
		return strings.TrimPrefix(e, "http://"), false
	default:
		// Bare host[:port] — assume plaintext (matches dev MinIO defaults).
		return e, false
	}
}

func normalizePrefix(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	return p + "/"
}

// defaultOSBucket is the legacy cluster bucket the OS uses for its own
// (cluster/sync) data when no explicit bucket is configured. It is the
// "box-user" bucket and is NEVER shared with per-user app storage.
const defaultOSBucket = "vulos-cluster"

// ResolverConfig is the box-side static configuration for the Resolver, read
// from the environment in self-host deployments.
type ResolverConfig struct {
	Endpoint     string // raw host[:port] or full URL; empty => local-FS fallback
	Region       string
	AccessKey    string
	SecretKey    string
	SessionToken string
	UseSSL       bool
	// Bucket, when set, is an EXPLICIT shared account bucket used for ALL users
	// (single-tenant self-host). C2: configuring this in a multi-user box is a
	// cross-user-isolation hazard — main() refuses to boot in that case. When
	// empty (the default), the account bucket is derived PER USER as
	// "<BucketPrefix><userID>" so isolation holds by construction.
	Bucket       string
	BucketPrefix string // default "vulos-"
	// OSBucket is the bucket for the OS's own (cluster/sync) data — the box-user
	// bucket. Defaults to "vulos-cluster" (legacy). It is independent of per-user
	// buckets so the OS never co-mingles with user data.
	OSBucket string
	// LocalRoot is the local-FS fallback root used when no object store is
	// configured (Endpoint empty).
	LocalRoot string
}

// LoadResolverConfig reads the unified storage config from the environment.
// VULOS_STORAGE_* take precedence; connection settings fall back to the existing
// cluster VULOS_S3_* variables so the OS storage binding matches the cluster
// config out of the box (self-host).
//
// C2 (per-user isolation by construction): Bucket is read ONLY from the explicit
// VULOS_STORAGE_BUCKET opt-in and has NO default. When unset, every user gets a
// derived per-user bucket "<BucketPrefix><userID>" — one shared bucket is never
// handed to all users by default. The legacy VULOS_S3_BUCKET (and the
// "vulos-cluster" default) now feed only OSBucket, the OS's own (cluster/sync)
// bucket, so existing cluster behaviour is preserved without leaking that bucket
// to per-user app storage.
func LoadResolverConfig() ResolverConfig {
	return ResolverConfig{
		Endpoint:     firstNonEmpty(os.Getenv("VULOS_STORAGE_ENDPOINT"), os.Getenv("VULOS_S3_ENDPOINT")),
		Region:       firstNonEmpty(os.Getenv("VULOS_STORAGE_REGION"), os.Getenv("VULOS_S3_REGION"), "us-east-1"),
		AccessKey:    firstNonEmpty(os.Getenv("VULOS_STORAGE_ACCESS_KEY"), os.Getenv("VULOS_S3_ACCESS_KEY")),
		SecretKey:    firstNonEmpty(os.Getenv("VULOS_STORAGE_SECRET_KEY"), os.Getenv("VULOS_S3_SECRET_KEY")),
		SessionToken: os.Getenv("VULOS_STORAGE_SESSION_TOKEN"),
		UseSSL:       firstNonEmpty(os.Getenv("VULOS_STORAGE_USE_SSL"), os.Getenv("VULOS_S3_USE_SSL"), "false") == "true",
		// Explicit shared-bucket opt-in only — NO default (C2).
		Bucket:       os.Getenv("VULOS_STORAGE_BUCKET"),
		BucketPrefix: firstNonEmpty(os.Getenv("VULOS_STORAGE_BUCKET_PREFIX"), "vulos-"),
		// OS/box bucket keeps the legacy cluster default.
		OSBucket:  firstNonEmpty(os.Getenv("VULOS_STORAGE_OS_BUCKET"), os.Getenv("VULOS_S3_BUCKET"), defaultOSBucket),
		LocalRoot: firstNonEmpty(os.Getenv("VULOS_STORAGE_LOCAL_ROOT"), datadir.Join("storage")),
	}
}

// CloudHook lets the cloud control plane override resolution with CP-provided
// per-user storage credentials (e.g. STS-scoped to vulos-<ulid>). It returns
// (Resolution, true) to take over, or (_, false) to fall through to box config.
// This is the C1/C3 seam by which the cloud CP supplies short-lived, scoped
// credentials instead of the box's long-lived static creds.
type CloudHook func(ctx context.Context, userID string) (Resolution, bool)

// ScopedCreds is a set of short-lived credentials scoped to a single
// bucket/prefix (e.g. from STS AssumeRole with an inline session policy).
type ScopedCreds struct {
	AccessKey    string
	SecretKey    string
	SessionToken string
}

// CredentialMinter issues SHORT-LIVED, PREFIX-SCOPED credentials for the given
// bucket+prefix (C1/C3). It returns (creds, true) on success or (_, false) to
// fall back to the resolver's static credentials. The MinIO/STS implementation
// is in sts.go; the cloud CP may supply its own via SetCredentialMinter.
type CredentialMinter func(ctx context.Context, bucket, prefix string) (ScopedCreds, bool)

// Resolver yields the per-user object-store binding for the current request.
// In self-host it is fed entirely from box config/env; in cloud a CloudHook
// supplies CP-brokered, per-user-scoped credentials.
type Resolver struct {
	cfg       ResolverConfig
	cloudHook CloudHook
	minter    CredentialMinter
}

// NewResolver builds a Resolver from box-side configuration.
func NewResolver(cfg ResolverConfig) *Resolver {
	if cfg.BucketPrefix == "" {
		cfg.BucketPrefix = "vulos-"
	}
	if cfg.OSBucket == "" {
		cfg.OSBucket = defaultOSBucket
	}
	return &Resolver{cfg: cfg}
}

// SetCloudHook installs the cloud control-plane override (cloud deployments).
// Pass nil to use box config exclusively (self-host).
func (r *Resolver) SetCloudHook(h CloudHook) { r.cloudHook = h }

// SetCredentialMinter installs the short-lived prefix-scoped credential minter
// (C1/C3). Pass nil to hand out static per-user-bucket credentials instead.
func (r *Resolver) SetCredentialMinter(m CredentialMinter) { r.minter = m }

// SharedBucketConfigured reports whether an EXPLICIT shared account bucket is
// set (VULOS_STORAGE_BUCKET). main() uses this to refuse multi-user boot, since
// a single shared bucket across users defeats per-user isolation (C2).
func (r *Resolver) SharedBucketConfigured() bool { return r.cfg.Bucket != "" }

// StaticallyConfigured reports whether the box has a static object-store
// endpoint configured (VULOS_STORAGE_ENDPOINT/VULOS_S3_ENDPOINT), independent
// of any particular user or CloudHook override. main() uses this at BOOT time
// to decide whether a static credential exists that would need STS scoping —
// a CloudHook-supplied (cloud/dynamic) resolution is intentionally NOT part of
// this check since it cannot be known before any request arrives; that path
// is covered instead by the per-request fail-closed behavior in
// ResolveScoped/the gateway.
func (r *Resolver) StaticallyConfigured() bool { return r.cfg.Endpoint != "" }

// LocalRoot returns the local-FS fallback root used when no object store is
// configured.
func (r *Resolver) LocalRoot() string { return r.cfg.LocalRoot }

// Resolve returns the storage binding for userID. The returned Resolution has
// no Prefix set — callers stamp their own (OS: "os/", apps: "<appID>/") via
// WithPrefix. An empty Endpoint means "no object store; use local fallback".
func (r *Resolver) Resolve(ctx context.Context, userID string) Resolution {
	if r.cloudHook != nil {
		if res, ok := r.cloudHook(ctx, userID); ok {
			return res
		}
	}
	return Resolution{
		Endpoint:     endpointURL(r.cfg.Endpoint, r.cfg.UseSSL),
		Bucket:       r.bucketFor(userID),
		Region:       r.cfg.Region,
		AccessKey:    r.cfg.AccessKey,
		SecretKey:    r.cfg.SecretKey,
		SessionToken: r.cfg.SessionToken,
	}
}

// osUserID is the synthetic identity under which the OS's own storage resolves.
const osUserID = "os"

// OSPrefix is the key prefix under which the OS namespaces its own data in the
// shared per-user bucket.
const OSPrefix = "os/"

// OSResolution returns the OS's own storage binding (prefix "os/"). To preserve
// the historical cluster/sync behaviour, when credentials are present but no
// endpoint is configured it defaults the endpoint to localhost:9000 (the legacy
// cluster default) rather than reporting a local-FS fallback.
func (r *Resolver) OSResolution(ctx context.Context) Resolution {
	res := r.Resolve(ctx, osUserID).WithPrefix(OSPrefix)
	if res.Endpoint == "" && r.cfg.AccessKey != "" {
		res.Endpoint = endpointURL("localhost:9000", r.cfg.UseSSL)
	}
	return res
}

// bucketFor returns the account bucket for userID. The OS resolves to its own
// box-user bucket (osBucket). For real users, an EXPLICIT shared Bucket is used
// when configured (single-tenant; guarded against multi-user at boot — C2),
// otherwise the bucket is derived PER USER so cross-user access is impossible by
// construction.
func (r *Resolver) bucketFor(userID string) string {
	if userID == osUserID {
		return r.osBucket()
	}
	if r.cfg.Bucket != "" {
		return r.cfg.Bucket
	}
	if userID == "" {
		return ""
	}
	return r.cfg.BucketPrefix + userID
}

// BucketFor returns the account bucket for userID (the per-user "vulos-<userID>"
// bucket in the default self-host configuration). The Files control plane uses
// it to record where a node's bytes live. It exposes bucketFor without revealing
// credentials; cloud per-user STS bucket overrides (CloudHook) are not reflected
// here — foundation/self-host derive the bucket statically.
func (r *Resolver) BucketFor(userID string) string { return r.bucketFor(userID) }

// osBucket returns the bucket for the OS's own (cluster/sync) data. When an
// explicit shared Bucket is configured the OS shares it (single-tenant);
// otherwise it uses OSBucket (default "vulos-cluster") — never a per-user bucket.
func (r *Resolver) osBucket() string {
	if r.cfg.Bucket != "" {
		return r.cfg.Bucket
	}
	if r.cfg.OSBucket != "" {
		return r.cfg.OSBucket
	}
	return defaultOSBucket
}

// ResolveScoped returns the storage binding for userID with Prefix set to
// prefix and, when a CredentialMinter is configured AND succeeds, credentials
// scoped DOWN to bucket/prefix via short-lived STS (C1/C3). The gateway passes
// the full per-user/app prefix ("<userID>/<appID>/") so the minted policy is
// locked to that path.
//
// SECURITY (fail-closed, per-app isolation): when an object store IS
// configured (Configured()==true) but a scoped credential could NOT be minted
// — either because no CredentialMinter is installed, or because minting
// failed — the returned Resolution carries EMPTY AccessKey/SecretKey/
// SessionToken and Scoped=false. It NEVER falls back to the resolver's static,
// full-bucket credential. A storage-permitted app must never receive a
// credential valid for the whole bucket; callers (the gateway) that see
// Scoped=false alongside Configured()==true must withhold injection entirely
// (the app can use the presign endpoint instead, or degrade to no storage).
// Cross-USER isolation holds regardless (per-user bucket/prefix, C2); this is
// what closes cross-APP-within-one-user isolation without depending on gateway
// discipline alone.
//
// When no object store is configured at all (Configured()==false — local-FS
// fallback), there is nothing to protect and the empty Resolution is returned
// with Scoped left false but harmless (no headers, no credentials, no store).
//
// The bool mirrors the resolver func contract (always true here; the gateway
// decides whether the app is permitted at all).
func (r *Resolver) ResolveScoped(ctx context.Context, userID, prefix string) (Resolution, bool) {
	res := r.Resolve(ctx, userID).WithPrefix(prefix)
	// No object store (local-FS fallback) — nothing to scope; signal fallback.
	if !res.Configured() {
		return res, true
	}
	// BUG FIX (2026-07-12): a CloudHook may already have returned a
	// Resolution with Scoped=true (e.g. a cloud per-bucket IAM minter that
	// scopes credentials itself, upstream of this method). Previously the
	// code below unconditionally fell through to "no minter installed ⇒
	// blank the credentials" whenever r.minter was nil, which DESTROYED an
	// already-scoped CloudHook credential the instant no self-host STS
	// minter was ALSO configured — the two seams are independent and a
	// cloud deployment legitimately has a CloudHook but no self-host
	// CredentialMinter. Respect an already-scoped result and skip the
	// minter/fail-closed logic entirely.
	if res.Scoped {
		return res, true
	}
	if r.minter != nil {
		if sc, ok := r.minter(ctx, res.Bucket, res.Prefix); ok {
			res.AccessKey = sc.AccessKey
			res.SecretKey = sc.SecretKey
			res.SessionToken = sc.SessionToken
			res.Scoped = true
			return res, true
		}
	}
	// FAIL CLOSED: no minter installed, or minting failed. Blank the static
	// credentials rather than falling back to them — see doc above.
	res.AccessKey = ""
	res.SecretKey = ""
	res.SessionToken = ""
	res.Scoped = false
	return res, true
}

// endpointURL renders a raw host[:port] (or already-qualified URL) as an S3 URL.
func endpointURL(endpoint string, useSSL bool) string {
	if endpoint == "" {
		return ""
	}
	if strings.Contains(endpoint, "://") {
		return endpoint
	}
	if useSSL {
		return "https://" + endpoint
	}
	return "http://" + endpoint
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
