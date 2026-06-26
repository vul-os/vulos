package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// ResolverConfig is the box-side static configuration for the Resolver, read
// from the environment in self-host deployments.
type ResolverConfig struct {
	Endpoint     string // raw host[:port] or full URL; empty => local-FS fallback
	Region       string
	AccessKey    string
	SecretKey    string
	SessionToken string
	UseSSL       bool
	// Bucket, when set, is the per-user account bucket shared across this box
	// (single-tenant self-host). When empty, the account bucket is derived per
	// user as "<BucketPrefix><userID>".
	Bucket       string
	BucketPrefix string // default "vulos-"
	// LocalRoot is the local-FS fallback root used when no object store is
	// configured (Endpoint empty).
	LocalRoot string
}

// LoadResolverConfig reads the unified storage config from the environment.
// VULOS_STORAGE_* take precedence; they fall back to the existing cluster
// VULOS_S3_* variables so the OS storage binding matches the cluster config
// out of the box (self-host). Defaults mirror cluster.LoadS3Config so wiring the
// cluster through the resolver is behaviour-preserving.
func LoadResolverConfig() ResolverConfig {
	home, _ := os.UserHomeDir()
	return ResolverConfig{
		Endpoint:     firstNonEmpty(os.Getenv("VULOS_STORAGE_ENDPOINT"), os.Getenv("VULOS_S3_ENDPOINT")),
		Region:       firstNonEmpty(os.Getenv("VULOS_STORAGE_REGION"), os.Getenv("VULOS_S3_REGION"), "us-east-1"),
		AccessKey:    firstNonEmpty(os.Getenv("VULOS_STORAGE_ACCESS_KEY"), os.Getenv("VULOS_S3_ACCESS_KEY")),
		SecretKey:    firstNonEmpty(os.Getenv("VULOS_STORAGE_SECRET_KEY"), os.Getenv("VULOS_S3_SECRET_KEY")),
		SessionToken: os.Getenv("VULOS_STORAGE_SESSION_TOKEN"),
		UseSSL:       firstNonEmpty(os.Getenv("VULOS_STORAGE_USE_SSL"), os.Getenv("VULOS_S3_USE_SSL"), "false") == "true",
		Bucket:       firstNonEmpty(os.Getenv("VULOS_STORAGE_BUCKET"), os.Getenv("VULOS_S3_BUCKET"), "vulos-cluster"),
		BucketPrefix: firstNonEmpty(os.Getenv("VULOS_STORAGE_BUCKET_PREFIX"), "vulos-"),
		LocalRoot:    firstNonEmpty(os.Getenv("VULOS_STORAGE_LOCAL_ROOT"), filepath.Join(home, ".vulos", "storage")),
	}
}

// CloudHook lets the cloud control plane override resolution with CP-provided
// per-user storage credentials (e.g. STS-scoped to vulos-<ulid>). It returns
// (Resolution, true) to take over, or (_, false) to fall through to box config.
type CloudHook func(ctx context.Context, userID string) (Resolution, bool)

// Resolver yields the per-user object-store binding for the current request.
// In self-host it is fed entirely from box config/env; in cloud a CloudHook
// supplies CP-brokered, per-user-scoped credentials.
type Resolver struct {
	cfg       ResolverConfig
	cloudHook CloudHook
}

// NewResolver builds a Resolver from box-side configuration.
func NewResolver(cfg ResolverConfig) *Resolver {
	if cfg.BucketPrefix == "" {
		cfg.BucketPrefix = "vulos-"
	}
	return &Resolver{cfg: cfg}
}

// SetCloudHook installs the cloud control-plane override (cloud deployments).
// Pass nil to use box config exclusively (self-host).
func (r *Resolver) SetCloudHook(h CloudHook) { r.cloudHook = h }

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

// bucketFor returns the per-user account bucket. A configured Bucket is the
// shared single-tenant account bucket; otherwise the bucket is derived per user.
func (r *Resolver) bucketFor(userID string) string {
	if r.cfg.Bucket != "" {
		return r.cfg.Bucket
	}
	if userID == "" {
		return ""
	}
	return r.cfg.BucketPrefix + userID
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
