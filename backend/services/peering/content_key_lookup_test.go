package peering

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newContentKeyStore(t *testing.T, pub string) *ProfileStore {
	t.Helper()
	store, err := NewProfileStore(t.TempDir(), "vula:ed25519:test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if pub != "" {
		if err := store.Update(func(d *ProfileData) { d.ContentPubKey = pub }); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func doLookup(t *testing.T, secret, hdrSecret, account string, store *ProfileStore) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	RegisterContentKeyLookup(mux, store, secret)
	req := httptest.NewRequest(http.MethodGet, ContentKeyLookupPath+"?account="+account, nil)
	if hdrSecret != "" {
		req.Header.Set(ContentKeyAuthHeader, hdrSecret)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestContentKeyLookupAuthAndFailClosed(t *testing.T) {
	const pub = "Y5DLT0WlcfYTe6rR9OONEiA+IH1r76m7gTx2TpLcQzw="
	store := newContentKeyStore(t, pub)

	// No secret configured on the box → 503 (cell treats as fail-closed).
	if rec := doLookup(t, "", "whatever", "acct-bob", store); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured secret: want 503, got %d", rec.Code)
	}
	// Wrong secret → 401.
	if rec := doLookup(t, "s3cr3t", "wrong", "acct-bob", store); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret: want 401, got %d", rec.Code)
	}
	// Missing secret header → 401.
	if rec := doLookup(t, "s3cr3t", "", "acct-bob", store); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing secret: want 401, got %d", rec.Code)
	}
	// Missing account → 400.
	if rec := doLookup(t, "s3cr3t", "s3cr3t", "", store); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing account: want 400, got %d", rec.Code)
	}
	// Correct secret → 200 with the published content key.
	rec := doLookup(t, "s3cr3t", "s3cr3t", "acct-bob", store)
	if rec.Code != http.StatusOK {
		t.Fatalf("authorized: want 200, got %d", rec.Code)
	}
	var body struct {
		ContentPubKey string `json:"content_pub_key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ContentPubKey != pub {
		t.Fatalf("content key: got %q want %q", body.ContentPubKey, pub)
	}
}

func TestContentKeyLookupEmptyWhenUnpublished(t *testing.T) {
	store := newContentKeyStore(t, "") // no published key
	rec := doLookup(t, "s3cr3t", "s3cr3t", "acct-bob", store)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var body struct {
		ContentPubKey string `json:"content_pub_key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ContentPubKey != "" {
		t.Fatalf("want empty content key, got %q", body.ContentPubKey)
	}
}
