package storage

import (
	"context"
	"strings"
	"testing"
)

func TestResolve_BoxConfig(t *testing.T) {
	r := NewResolver(ResolverConfig{
		Endpoint:  "minio:9000",
		Region:    "us-east-1",
		AccessKey: "AK",
		SecretKey: "SK",
		Bucket:    "vulos-acct",
		UseSSL:    false,
	})
	res := r.Resolve(context.Background(), "user1")
	if res.Endpoint != "http://minio:9000" {
		t.Fatalf("endpoint = %q", res.Endpoint)
	}
	if res.Bucket != "vulos-acct" {
		t.Fatalf("bucket = %q", res.Bucket)
	}
	if !res.Configured() {
		t.Fatal("expected configured")
	}
	if res.Prefix != "" {
		t.Fatalf("expected empty prefix, got %q", res.Prefix)
	}
}

func TestResolve_PerUserBucketDerivation(t *testing.T) {
	r := NewResolver(ResolverConfig{
		Endpoint:     "minio:9000",
		AccessKey:    "AK",
		SecretKey:    "SK",
		BucketPrefix: "vulos-",
		// Bucket empty → derive per user.
	})
	res := r.Resolve(context.Background(), "01ABCDEF")
	if res.Bucket != "vulos-01ABCDEF" {
		t.Fatalf("derived bucket = %q", res.Bucket)
	}
}

func TestResolve_EmptyEndpointIsLocalFallback(t *testing.T) {
	r := NewResolver(ResolverConfig{Bucket: "vulos-acct"})
	res := r.Resolve(context.Background(), "user1")
	if res.Endpoint != "" {
		t.Fatalf("expected empty endpoint, got %q", res.Endpoint)
	}
	if res.Configured() {
		t.Fatal("empty endpoint must report not configured (local fallback)")
	}
}

func TestWithPrefix_Normalisation(t *testing.T) {
	cases := map[string]string{
		"office":   "office/",
		"office/":  "office/",
		"/office/": "office/",
		"os/":      "os/",
		"":         "",
	}
	for in, want := range cases {
		if got := (Resolution{}).WithPrefix(in).Prefix; got != want {
			t.Fatalf("WithPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestS3Host_RoundTrip(t *testing.T) {
	cases := []struct {
		endpoint string
		wantHost string
		wantSSL  bool
	}{
		{"https://s3.amazonaws.com", "s3.amazonaws.com", true},
		{"http://localhost:9000", "localhost:9000", false},
		{"minio:9000", "minio:9000", false},
		{"", "", false},
	}
	for _, c := range cases {
		host, ssl := (Resolution{Endpoint: c.endpoint}).S3Host()
		if host != c.wantHost || ssl != c.wantSSL {
			t.Fatalf("S3Host(%q) = (%q,%v), want (%q,%v)", c.endpoint, host, ssl, c.wantHost, c.wantSSL)
		}
	}
}

func TestOSResolution_PreservesClusterDefaults(t *testing.T) {
	// Creds present, no endpoint → legacy cluster default localhost:9000.
	r := NewResolver(ResolverConfig{AccessKey: "AK", SecretKey: "SK", Bucket: "vulos-cluster"})
	res := r.OSResolution(context.Background())
	if res.Prefix != OSPrefix {
		t.Fatalf("prefix = %q, want %q", res.Prefix, OSPrefix)
	}
	host, ssl := res.S3Host()
	if host != "localhost:9000" || ssl {
		t.Fatalf("OS host = (%q,%v), want (localhost:9000,false)", host, ssl)
	}
	if res.Bucket != "vulos-cluster" {
		t.Fatalf("OS bucket = %q", res.Bucket)
	}
}

func TestOSResolution_NoCredsStaysLocal(t *testing.T) {
	r := NewResolver(ResolverConfig{Bucket: "vulos-cluster"})
	res := r.OSResolution(context.Background())
	if res.Endpoint != "" {
		t.Fatalf("no creds should leave endpoint empty, got %q", res.Endpoint)
	}
}

func TestCloudHook_Override(t *testing.T) {
	r := NewResolver(ResolverConfig{Endpoint: "minio:9000", Bucket: "box-bucket", AccessKey: "AK", SecretKey: "SK"})
	r.SetCloudHook(func(_ context.Context, userID string) (Resolution, bool) {
		return Resolution{Endpoint: "https://cp.example.com", Bucket: "vulos-" + userID, AccessKey: "CP", SecretKey: "CPS", SessionToken: "tok"}, true
	})
	res := r.Resolve(context.Background(), "u9")
	if res.Endpoint != "https://cp.example.com" || res.Bucket != "vulos-u9" || res.SessionToken != "tok" {
		t.Fatalf("cloud hook not applied: %+v", res)
	}
}

// C2: with no explicit shared bucket configured (the default), every user gets
// a derived per-user bucket — never one shared bucket.
func TestResolve_DefaultsToPerUserBucket(t *testing.T) {
	// No Bucket configured → per-user derivation (mirrors LoadResolverConfig
	// with VULOS_STORAGE_BUCKET unset).
	r := NewResolver(ResolverConfig{
		Endpoint:  "minio:9000",
		AccessKey: "AK",
		SecretKey: "SK",
	})
	if r.SharedBucketConfigured() {
		t.Fatal("no shared bucket should be configured by default")
	}
	a := r.Resolve(context.Background(), "alice")
	b := r.Resolve(context.Background(), "bob")
	if a.Bucket != "vulos-alice" || b.Bucket != "vulos-bob" {
		t.Fatalf("expected per-user buckets, got %q and %q", a.Bucket, b.Bucket)
	}
}

// C2: an explicit shared bucket is reported so main() can refuse multi-user boot.
func TestSharedBucketConfigured(t *testing.T) {
	r := NewResolver(ResolverConfig{Bucket: "vulos-shared"})
	if !r.SharedBucketConfigured() {
		t.Fatal("explicit Bucket must report shared")
	}
	a := r.Resolve(context.Background(), "alice")
	b := r.Resolve(context.Background(), "bob")
	if a.Bucket != "vulos-shared" || b.Bucket != "vulos-shared" {
		t.Fatalf("shared bucket must be returned for all users, got %q %q", a.Bucket, b.Bucket)
	}
}

// C2: the OS uses its own (box-user) bucket, independent of per-user buckets.
func TestOSBucket_IndependentOfUsers(t *testing.T) {
	r := NewResolver(ResolverConfig{Endpoint: "minio:9000", AccessKey: "AK", SecretKey: "SK"})
	os := r.OSResolution(context.Background())
	if os.Bucket != "vulos-cluster" {
		t.Fatalf("OS bucket = %q, want vulos-cluster (default OSBucket)", os.Bucket)
	}
	user := r.Resolve(context.Background(), "alice")
	if user.Bucket == os.Bucket {
		t.Fatalf("user bucket must not equal OS bucket (%q)", os.Bucket)
	}
}

// C1/C3: ResolveScoped applies the prefix and, when a minter is set, replaces
// the static creds with the minted short-lived scoped creds.
func TestResolveScoped_MintsScopedCreds(t *testing.T) {
	r := NewResolver(ResolverConfig{Endpoint: "minio:9000", AccessKey: "STATIC-AK", SecretKey: "STATIC-SK"})
	var gotBucket, gotPrefix string
	r.SetCredentialMinter(func(_ context.Context, bucket, prefix string) (ScopedCreds, bool) {
		gotBucket, gotPrefix = bucket, prefix
		return ScopedCreds{AccessKey: "TMP-AK", SecretKey: "TMP-SK", SessionToken: "TMP-TOK"}, true
	})
	res, ok := r.ResolveScoped(context.Background(), "alice", "alice/office/")
	if !ok {
		t.Fatal("expected ok")
	}
	if gotBucket != "vulos-alice" || gotPrefix != "alice/office/" {
		t.Fatalf("minter saw bucket=%q prefix=%q", gotBucket, gotPrefix)
	}
	if res.AccessKey != "TMP-AK" || res.SecretKey != "TMP-SK" || res.SessionToken != "TMP-TOK" {
		t.Fatalf("expected minted creds, got %+v", res)
	}
	if res.Prefix != "alice/office/" {
		t.Fatalf("prefix = %q", res.Prefix)
	}
	if !res.Scoped {
		t.Fatal("expected Scoped=true when the minter succeeds")
	}
}

// BUG FIX regression (2026-07-12): a CloudHook that already returns a
// Resolution with Scoped=true (e.g. a cloud per-bucket IAM minter, wired
// upstream of ResolveScoped rather than as a CredentialMinter) must NOT be
// blanked out just because no self-host CredentialMinter is ALSO installed.
// The two seams are independent — a cloud deployment typically has a
// CloudHook but no on-box STS minter.
func TestResolveScoped_PreservesCloudHookScopedCreds(t *testing.T) {
	r := NewResolver(ResolverConfig{}) // no static box config at all
	r.SetCloudHook(func(_ context.Context, userID string) (Resolution, bool) {
		return Resolution{
			Endpoint:  "https://fly.storage.tigris.dev",
			Bucket:    "vulos-" + userID,
			AccessKey: "CP-SCOPED-AK",
			SecretKey: "CP-SCOPED-SK",
			Scoped:    true, // the CloudHook already scoped these itself
		}, true
	})
	// Deliberately NOT calling SetCredentialMinter — this is the cloud shape.
	res, ok := r.ResolveScoped(context.Background(), "alice", "alice/office/")
	if !ok {
		t.Fatal("expected ok")
	}
	if !res.Scoped {
		t.Fatal("expected Scoped=true to be preserved from the CloudHook")
	}
	if res.AccessKey != "CP-SCOPED-AK" || res.SecretKey != "CP-SCOPED-SK" {
		t.Fatalf("CloudHook-scoped credentials must survive ResolveScoped, got %+v", res)
	}
	if res.Prefix != "alice/office/" {
		t.Fatalf("prefix = %q, want alice/office/", res.Prefix)
	}
}

// SECURITY (fail-closed): with no minter installed, ResolveScoped must NOT
// fall back to the resolver's static full-bucket credential — a storage-
// permitted app must never receive it. Cross-user isolation still holds
// (per-user bucket from C2); this closes the cross-app-within-one-user hole
// unconditionally rather than depending on gateway discipline.
func TestResolveScoped_NoMinter_FailsClosed(t *testing.T) {
	r := NewResolver(ResolverConfig{Endpoint: "minio:9000", AccessKey: "STATIC-AK", SecretKey: "STATIC-SK"})
	res, ok := r.ResolveScoped(context.Background(), "alice", "alice/office/")
	if !ok {
		t.Fatal("expected ok")
	}
	if res.AccessKey != "" || res.SecretKey != "" || res.SessionToken != "" {
		t.Fatalf("expected NO credentials without a minter, got %+v", res)
	}
	if res.Scoped {
		t.Fatal("expected Scoped=false without a minter")
	}
	// Bucket/endpoint metadata is still harmless to report (no creds attached).
	if res.Bucket != "vulos-alice" {
		t.Fatalf("bucket = %q", res.Bucket)
	}
}

// SECURITY (fail-closed): when a minter IS installed but fails to mint (e.g.
// a transient STS outage), ResolveScoped must still NOT fall back to the
// static credential.
func TestResolveScoped_MinterFails_FailsClosed(t *testing.T) {
	r := NewResolver(ResolverConfig{Endpoint: "minio:9000", AccessKey: "STATIC-AK", SecretKey: "STATIC-SK"})
	r.SetCredentialMinter(func(_ context.Context, _, _ string) (ScopedCreds, bool) {
		return ScopedCreds{}, false // minting failed
	})
	res, ok := r.ResolveScoped(context.Background(), "alice", "alice/office/")
	if !ok {
		t.Fatal("expected ok")
	}
	if res.AccessKey != "" || res.SecretKey != "" {
		t.Fatalf("expected NO credentials when minting fails, got %+v", res)
	}
	if res.Scoped {
		t.Fatal("expected Scoped=false when minting fails")
	}
}

// No object store configured at all (local-FS fallback): nothing to protect,
// ResolveScoped reports not-configured and Scoped stays false (harmless).
func TestResolveScoped_NoStore_LocalFallback(t *testing.T) {
	r := NewResolver(ResolverConfig{})
	res, ok := r.ResolveScoped(context.Background(), "alice", "alice/office/")
	if !ok {
		t.Fatal("expected ok")
	}
	if res.Configured() {
		t.Fatal("expected not configured (local-FS fallback)")
	}
	if res.Scoped {
		t.Fatal("Scoped must be false when there is no store")
	}
}

// C1/C3: the STS inline session policy must lock to exactly bucket/prefix*.
func TestScopedPolicy_LocksToPrefix(t *testing.T) {
	p, err := scopedPolicy("vulos-alice", "alice/office/")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "arn:aws:s3:::vulos-alice/alice/office/*") {
		t.Fatalf("policy missing object-scope resource: %s", p)
	}
	if !strings.Contains(p, "\"s3:prefix\":[\"alice/office/*\"]") {
		t.Fatalf("policy missing ListBucket prefix condition: %s", p)
	}
}
