package streamsession

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHTTPRelaySignaler_ForwardSignal_Verbatim spins up a fake relay HTTP
// endpoint, posts a binary signaling payload through HTTPRelaySignaler, and
// asserts:
//  1. the relay receives the bytes VERBATIM (cloud is a thin pass-through);
//  2. the cloud uses the documented path (/stream/signal);
//  3. the cloud passes session metadata via headers, NOT in the body.
//
// This is the integration-shaped guard on the relay-endpoint contract.
func TestHTTPRelaySignaler_ForwardSignal_Verbatim(t *testing.T) {
	var (
		gotPath        string
		gotBody        []byte
		gotSession     string
		gotHost        string
		gotAccount     string
		gotContentType string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSession = r.Header.Get("X-Vulos-Stream-Session")
		gotHost = r.Header.Get("X-Vulos-Stream-Host")
		gotAccount = r.Header.Get("X-Vulos-Stream-Account")
		gotContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = b
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	sig := NewHTTPRelaySignaler()
	payload := []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01, 'I', 'C', 'E'}
	sess := Session{
		ID:        "sess-XYZ",
		AccountID: "acct-1",
		HostULID:  "ulid-host-1",
	}
	if err := sig.ForwardSignal(context.Background(), srv.URL, sess, payload); err != nil {
		t.Fatalf("ForwardSignal: %v", err)
	}
	if gotPath != "/stream/signal" {
		t.Fatalf("path = %q, want /stream/signal", gotPath)
	}
	if !bytes.Equal(gotBody, payload) {
		t.Fatalf("body bytes != payload (cloud must not mutate):\n got %v\nwant %v", gotBody, payload)
	}
	if gotSession != "sess-XYZ" || gotHost != "ulid-host-1" || gotAccount != "acct-1" {
		t.Fatalf("metadata headers wrong: sess=%q host=%q acct=%q", gotSession, gotHost, gotAccount)
	}
	if gotContentType != "application/octet-stream" {
		t.Fatalf("content-type = %q", gotContentType)
	}
}

// TestHTTPRelaySignaler_ForwardSignal_Non2xxSurfacesErr — a 5xx from the
// relay becomes ErrUpstreamRelay.
func TestHTTPRelaySignaler_ForwardSignal_Non2xxSurfacesErr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	sig := NewHTTPRelaySignaler()
	err := sig.ForwardSignal(context.Background(), srv.URL,
		Session{ID: "s", AccountID: "a", HostULID: "u"}, []byte("x"))
	if !errors.Is(err, ErrUpstreamRelay) {
		t.Fatalf("want ErrUpstreamRelay, got %v", err)
	}
}

// TestHTTPRelaySignaler_EmptyEndpoint — short-circuits with ErrUpstreamRelay
// when no relay endpoint is configured.
func TestHTTPRelaySignaler_EmptyEndpoint(t *testing.T) {
	sig := NewHTTPRelaySignaler()
	err := sig.ForwardSignal(context.Background(), "",
		Session{ID: "s", AccountID: "a", HostULID: "u"}, []byte("x"))
	if !errors.Is(err, ErrUpstreamRelay) {
		t.Fatalf("want ErrUpstreamRelay, got %v", err)
	}
}

// TestHTTPRelaySignaler_NotifyTeardown — posts to /stream/teardown with
// session headers and no body.
func TestHTTPRelaySignaler_NotifyTeardown(t *testing.T) {
	var (
		gotPath    string
		gotSession string
		gotBodyLen int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSession = r.Header.Get("X-Vulos-Stream-Session")
		b, _ := io.ReadAll(r.Body)
		gotBodyLen = len(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	sig := NewHTTPRelaySignaler()
	if err := sig.NotifyTeardown(context.Background(), srv.URL,
		Session{ID: "sess-1", HostULID: "u"}); err != nil {
		t.Fatalf("NotifyTeardown: %v", err)
	}
	if gotPath != "/stream/teardown" || gotSession != "sess-1" || gotBodyLen != 0 {
		t.Fatalf("teardown wrong: path=%q sess=%q bodyLen=%d", gotPath, gotSession, gotBodyLen)
	}
}
