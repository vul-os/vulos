package cloudenroll

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
)

// FetchBrokerPubkey reads the CP's active login-broker key from the
// unauthenticated GET /api/profile/broker/pubkey endpoint and decodes it.
func TestFetchBrokerPubkey(t *testing.T) {
	brokerPub, _, _ := ed25519.GenerateKey(rand.Reader)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/profile/broker/pubkey" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"active":{"public_key":"` +
			base64.StdEncoding.EncodeToString(brokerPub) + `"}}`))
	}))
	defer srv.Close()

	e := New(srv.URL, t.TempDir(), xorSealer{})
	got, err := e.FetchBrokerPubkey(context.Background())
	if err != nil {
		t.Fatalf("FetchBrokerPubkey: %v", err)
	}
	if !got.Equal(brokerPub) {
		t.Fatal("fetched broker pubkey does not match the served key")
	}
}

// A non-200 from the endpoint surfaces an error (so the caller falls back to
// first-login TOFU rather than pinning garbage).
func TestFetchBrokerPubkey_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	e := New(srv.URL, t.TempDir(), xorSealer{})
	if _, err := e.FetchBrokerPubkey(context.Background()); err == nil {
		t.Fatal("expected an error on non-200 status")
	}
}
