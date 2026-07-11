package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	storagepkg "vulos/backend/internal/storage"
)

// newStorageGateway builds a Gateway with just the fields the storage-injection
// logic needs (no network / auth wiring). A broker secret is preset since H2/H3
// makes the gateway fail closed without one.
func newStorageGateway() *Gateway {
	return &Gateway{
		appSecrets:          make(map[string]string),
		appHits:             make(map[string]*rateBucket),
		storageApps:         make(map[string]string),
		storageBrokerSecret: "broker-secret",
	}
}

func TestInjectStorage_PermittedApp(t *testing.T) {
	g := newStorageGateway()
	var gotPrefix string
	g.SetStorageResolver(func(_ context.Context, userID, prefix string) (storagepkg.Resolution, bool) {
		gotPrefix = prefix
		return storagepkg.Resolution{
			Endpoint:  "https://s3.example.com",
			Bucket:    "vulos-" + userID,
			Region:    "us-east-1",
			AccessKey: "AK",
			SecretKey: "SK",
			Scoped:    true, // STS (or equivalent) successfully scoped these creds
		}, true
	})
	g.AllowStorage("office", "office/")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	g.injectStorageHeaders(context.Background(), r, "user123", "office")

	// C2: the gateway must pass and stamp the per-user prefix "<userID>/<appID>/".
	if gotPrefix != "user123/office/" {
		t.Fatalf("resolver prefix = %q, want user123/office/", gotPrefix)
	}
	if got := r.Header.Get("X-Vulos-Storage-Endpoint"); got != "https://s3.example.com" {
		t.Fatalf("endpoint = %q", got)
	}
	if got := r.Header.Get("X-Vulos-Storage-Bucket"); got != "vulos-user123" {
		t.Fatalf("bucket = %q", got)
	}
	if got := r.Header.Get("X-Vulos-Storage-Prefix"); got != "user123/office/" {
		t.Fatalf("prefix = %q, want user123/office/", got)
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
	// H2/H3: the broker-auth header must be injected so apps can trust the seam.
	if got := r.Header.Get("X-Vulos-Storage-Broker-Auth"); got != "broker-secret" {
		t.Fatalf("broker-auth = %q, want broker-secret", got)
	}
}

// H2/H3: with no broker secret the gateway must NOT hand out any storage creds.
func TestInjectStorage_FailsClosedWithoutBrokerSecret(t *testing.T) {
	g := newStorageGateway()
	g.storageBrokerSecret = "" // simulate VULOS_STORAGE_BROKER_SECRET unset
	g.SetStorageResolver(func(_ context.Context, userID, prefix string) (storagepkg.Resolution, bool) {
		return storagepkg.Resolution{Endpoint: "https://s3.example.com", Bucket: "b", AccessKey: "AK", SecretKey: "SK"}, true
	})
	g.AllowStorage("office", "office/")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// A spoofed inbound header must still be stripped even when failing closed.
	r.Header.Set("X-Vulos-Storage-Access-Key", "attacker")
	g.injectStorageHeaders(context.Background(), r, "user123", "office")

	if got := r.Header.Get("X-Vulos-Storage-Access-Key"); got != "" {
		t.Fatalf("fail-closed must inject no creds (and strip spoof), got %q", got)
	}
	if got := r.Header.Get("X-Vulos-Storage-Broker-Auth"); got != "" {
		t.Fatalf("fail-closed must not emit broker-auth, got %q", got)
	}
}

// SECURITY (fail-closed): Configured()==true (an object store IS configured,
// there ARE credentials) but Scoped==false (no STS/scoping available) must
// result in NO injected headers at all — never the static/unscoped
// credential. This is the actual isolation hole this change closes.
func TestInjectStorage_ConfiguredButUnscoped_FailsClosed(t *testing.T) {
	g := newStorageGateway()
	g.SetStorageResolver(func(_ context.Context, userID, prefix string) (storagepkg.Resolution, bool) {
		return storagepkg.Resolution{
			Endpoint:  "https://s3.example.com",
			Bucket:    "vulos-" + userID,
			AccessKey: "STATIC-FULL-BUCKET-AK",
			SecretKey: "STATIC-FULL-BUCKET-SK",
			Scoped:    false, // e.g. no STS minter, or minting failed, or cloud/Tigris
		}, true
	})
	g.AllowStorage("office", "office/")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	g.injectStorageHeaders(context.Background(), r, "user123", "office")

	if got := r.Header.Get("X-Vulos-Storage-Access-Key"); got != "" {
		t.Fatalf("must NEVER inject an unscoped/static credential, got %q", got)
	}
	if got := r.Header.Get("X-Vulos-Storage-Secret-Key"); got != "" {
		t.Fatalf("must NEVER inject an unscoped/static credential, got %q", got)
	}
	if got := r.Header.Get("X-Vulos-Storage-Endpoint"); got != "" {
		t.Fatalf("fail-closed should inject NOTHING (not even endpoint), got %q", got)
	}
	if got := r.Header.Get("X-Vulos-Storage-Broker-Auth"); got != "" {
		t.Fatalf("fail-closed must not emit broker-auth, got %q", got)
	}
}

func TestInjectStorage_UnpermittedApp(t *testing.T) {
	g := newStorageGateway()
	g.SetStorageResolver(func(context.Context, string, string) (storagepkg.Resolution, bool) {
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
	g.SetStorageResolver(func(context.Context, string, string) (storagepkg.Resolution, bool) {
		// Resolver declines (e.g. cloud unavailable) → no legitimate headers.
		return storagepkg.Resolution{}, false
	})
	g.AllowStorage("office", "office/")

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Vulos-Storage-Access-Key", "attacker-controlled")
	r.Header.Set("X-Vulos-Storage-Endpoint", "https://evil.example.com")
	r.Header.Set("X-Vulos-Storage-Broker-Auth", "attacker-broker")
	g.injectStorageHeaders(context.Background(), r, "user123", "office")

	if got := r.Header.Get("X-Vulos-Storage-Access-Key"); got != "" {
		t.Fatalf("spoofed access key must be stripped, got %q", got)
	}
	if got := r.Header.Get("X-Vulos-Storage-Endpoint"); got != "" {
		t.Fatalf("spoofed endpoint must be stripped, got %q", got)
	}
	if got := r.Header.Get("X-Vulos-Storage-Broker-Auth"); got != "" {
		t.Fatalf("spoofed broker-auth must be stripped, got %q", got)
	}
}

// Even an unpermitted app must have inbound storage headers stripped so it can
// never read client-supplied credentials.
func TestInjectStorage_StripsSpoofedForUnpermitted(t *testing.T) {
	g := newStorageGateway()
	g.SetStorageResolver(func(context.Context, string, string) (storagepkg.Resolution, bool) {
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
	g.SetStorageResolver(func(_ context.Context, userID, prefix string) (storagepkg.Resolution, bool) {
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
