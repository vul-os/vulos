package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	storagepkg "vulos/backend/internal/storage"
)

// newStorageGateway builds a Gateway with just the fields the storage-injection
// logic needs (no network / auth wiring).
func newStorageGateway() *Gateway {
	return &Gateway{
		appSecrets:  make(map[string]string),
		appHits:     make(map[string]*rateBucket),
		storageApps: make(map[string]string),
	}
}

func TestInjectStorage_PermittedApp(t *testing.T) {
	g := newStorageGateway()
	g.SetStorageResolver(func(_ context.Context, userID string) (storagepkg.Resolution, bool) {
		return storagepkg.Resolution{
			Endpoint:  "https://s3.example.com",
			Bucket:    "vulos-" + userID,
			Region:    "us-east-1",
			AccessKey: "AK",
			SecretKey: "SK",
		}, true
	})
	g.AllowStorage("office", "office/")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	g.injectStorageHeaders(context.Background(), r, "user123", "office")

	if got := r.Header.Get("X-Vulos-Storage-Endpoint"); got != "https://s3.example.com" {
		t.Fatalf("endpoint = %q", got)
	}
	if got := r.Header.Get("X-Vulos-Storage-Bucket"); got != "vulos-user123" {
		t.Fatalf("bucket = %q", got)
	}
	if got := r.Header.Get("X-Vulos-Storage-Prefix"); got != "office/" {
		t.Fatalf("prefix = %q", got)
	}
	if got := r.Header.Get("X-Vulos-Storage-Region"); got != "us-east-1" {
		t.Fatalf("region = %q", got)
	}
	if got := r.Header.Get("X-Vulos-Storage-Access-Key"); got != "AK" {
		t.Fatalf("access key = %q", got)
	}
	if got := r.Header.Get("X-Vulos-Storage-Secret-Key"); got != "SK" {
		t.Fatalf("secret key = %q", got)
	}
}

func TestInjectStorage_UnpermittedApp(t *testing.T) {
	g := newStorageGateway()
	g.SetStorageResolver(func(context.Context, string) (storagepkg.Resolution, bool) {
		return storagepkg.Resolution{Endpoint: "https://s3.example.com", Bucket: "b"}, true
	})
	// "notes" was never granted storage.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	g.injectStorageHeaders(context.Background(), r, "user123", "notes")

	if got := r.Header.Get("X-Vulos-Storage-Endpoint"); got != "" {
		t.Fatalf("unpermitted app must not receive storage headers, got %q", got)
	}
}

func TestInjectStorage_StripsSpoofedHeaders(t *testing.T) {
	g := newStorageGateway()
	g.SetStorageResolver(func(context.Context, string) (storagepkg.Resolution, bool) {
		// Resolver declines (e.g. cloud unavailable) → no legitimate headers.
		return storagepkg.Resolution{}, false
	})
	g.AllowStorage("office", "office/")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Vulos-Storage-Access-Key", "attacker-controlled")
	r.Header.Set("X-Vulos-Storage-Endpoint", "https://evil.example.com")
	g.injectStorageHeaders(context.Background(), r, "user123", "office")

	if got := r.Header.Get("X-Vulos-Storage-Access-Key"); got != "" {
		t.Fatalf("spoofed access key must be stripped, got %q", got)
	}
	if got := r.Header.Get("X-Vulos-Storage-Endpoint"); got != "" {
		t.Fatalf("spoofed endpoint must be stripped, got %q", got)
	}
}

// Even an unpermitted app must have inbound storage headers stripped so it can
// never read client-supplied credentials.
func TestInjectStorage_StripsSpoofedForUnpermitted(t *testing.T) {
	g := newStorageGateway()
	g.SetStorageResolver(func(context.Context, string) (storagepkg.Resolution, bool) {
		return storagepkg.Resolution{}, false
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Vulos-Storage-Secret-Key", "attacker-controlled")
	g.injectStorageHeaders(context.Background(), r, "user123", "notes")

	if got := r.Header.Get("X-Vulos-Storage-Secret-Key"); got != "" {
		t.Fatalf("spoofed secret key must be stripped even for unpermitted apps, got %q", got)
	}
}

func TestInjectStorage_EmptyEndpointSignalsFallback(t *testing.T) {
	g := newStorageGateway()
	g.SetStorageResolver(func(_ context.Context, userID string) (storagepkg.Resolution, bool) {
		// Local-FS fallback: empty endpoint, bucket still derived.
		return storagepkg.Resolution{Bucket: "vulos-" + userID}, true
	})
	g.AllowStorage("office", "office/")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	g.injectStorageHeaders(context.Background(), r, "user123", "office")

	if _, ok := r.Header["X-Vulos-Storage-Endpoint"]; !ok {
		t.Fatal("endpoint header should be present (empty) to signal fallback")
	}
	if got := r.Header.Get("X-Vulos-Storage-Endpoint"); got != "" {
		t.Fatalf("endpoint should be empty for fallback, got %q", got)
	}
}

func TestInjectStorage_NoResolverIsNoop(t *testing.T) {
	g := newStorageGateway()
	g.AllowStorage("office", "office/")
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	g.injectStorageHeaders(context.Background(), r, "user123", "office")
	if r.Header.Get("X-Vulos-Storage-Bucket") != "" {
		t.Fatal("no resolver should produce no headers")
	}
}
