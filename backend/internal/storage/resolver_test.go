package storage

import (
	"context"
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
