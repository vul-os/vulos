package main

// routes_meethost_alloc_test.go — SFU Phase 2 allocation: resolveSFUServerURL,
// the seam the self-host Meet token mint uses to hand a big call a registered,
// VERIFIED self-host SFU serverUrl (direct-first, relay-fallback).

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveSFU_NoRelayConfigured_Empty(t *testing.T) {
	t.Setenv("VULOS_RELAY_BASE_URL", "")
	if got := resolveSFUServerURL(context.Background()); got != "" {
		t.Fatalf("with no relay configured, meet_url must be empty (inert default), got %q", got)
	}
}

func TestResolveSFU_Available_ReturnsServerURL(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/meet/host/resolve" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"available":true,"server_url":"https://box1.example","host_id":"vula:box1"}`))
	}))
	defer relay.Close()

	t.Setenv("VULOS_RELAY_BASE_URL", relay.URL)
	if got := resolveSFUServerURL(context.Background()); got != "https://box1.example" {
		t.Fatalf("resolve must return the registered SFU serverUrl, got %q", got)
	}
}

func TestResolveSFU_NoHostRegistered_Empty(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"available":false}`)) // registry empty / disabled
	}))
	defer relay.Close()

	t.Setenv("VULOS_RELAY_BASE_URL", relay.URL)
	if got := resolveSFUServerURL(context.Background()); got != "" {
		t.Fatalf("available=false must degrade to empty meet_url, got %q", got)
	}
}

func TestResolveSFU_RelayError_DegradesToEmpty(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer relay.Close()

	t.Setenv("VULOS_RELAY_BASE_URL", relay.URL)
	if got := resolveSFUServerURL(context.Background()); got != "" {
		t.Fatalf("a relay error must never block the mint — meet_url must be empty, got %q", got)
	}
}
