package peering

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestWellKnown_PublishesSignedPreKeyNotOPKs proves Item 5: the well-known
// endpoint (a cacheable, unauthenticated GET) publishes ONLY the signed prekey and
// NEVER the one-time-prekey pool. Publishing the pool there would let any sender
// that skips the claim endpoint reuse the same OPK, defeating per-sender forward
// secrecy.
func TestWellKnown_PublishesSignedPreKeyNotOPKs(t *testing.T) {
	dir := wkTempDir(t)
	os.Setenv("VULOS_PEERING_DIR", dir)
	defer os.Unsetenv("VULOS_PEERING_DIR")

	priv, id := pkIdentity(t)
	store, err := NewPreKeyStore(t.TempDir(), id, priv, 8)
	if err != nil {
		t.Fatalf("NewPreKeyStore: %v", err)
	}
	if store.OneTimePreKeyCount() == 0 {
		t.Fatal("store should have a non-empty OPK pool for this test to be meaningful")
	}

	// Wire the well-known publisher exactly as the live server does (Item 5).
	SetPreKeyPublisher(store.PublicBundleSignedOnly)
	t.Cleanup(func() { SetPreKeyPublisher(nil) })

	mux := http.NewServeMux()
	RegisterWellKnownHandlers(mux)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/vula-id", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("well-known returned %d: %s", rec.Code, rec.Body)
	}

	var resp WKIdentityResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.PreKeys == nil {
		t.Fatal("well-known must publish a prekey bundle")
	}
	if resp.PreKeys.SignedPreKey.ID == "" || len(resp.PreKeys.SignedPreKey.Sig) == 0 {
		t.Fatal("well-known bundle must contain the signed prekey")
	}
	if len(resp.PreKeys.OneTimePreKeys) != 0 {
		t.Fatalf("well-known bundle must NOT contain one-time prekeys, got %d", len(resp.PreKeys.OneTimePreKeys))
	}

	// Belt-and-suspenders: the raw JSON must not carry the OPK array key at all.
	if got := rec.Body.String(); strings.Contains(got, "one_time_prekeys") {
		t.Fatalf("well-known response leaked one_time_prekeys: %s", got)
	}
}
