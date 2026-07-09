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
	var gotName string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/meet/host/resolve" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotName = r.URL.Query().Get("name")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"available":true,"server_url":"https://box1.example","host_id":"vula:box1"}`))
	}))
	defer relay.Close()

	t.Setenv("VULOS_RELAY_BASE_URL", relay.URL)
	t.Setenv("VULOS_RELAY_NAME", "box1")
	if got := resolveSFUServerURL(context.Background()); got != "https://box1.example" {
		t.Fatalf("resolve must return the registered SFU serverUrl, got %q", got)
	}
	// The lookup MUST be scoped to this box's own tunnel name so a shared relay
	// never returns another tenant's SFU endpoint.
	if gotName != "box1" {
		t.Fatalf("resolve must pass ?name=box1 (tenant scope), relay saw name=%q", gotName)
	}
}

// TestResolveSFU_NoName_DegradesToEmpty proves a box with no VULOS_RELAY_NAME
// cannot scope the lookup, so it degrades to "" (keeps its co-located SFU) rather
// than risk being handed another tenant's SFU endpoint by the shared relay.
func TestResolveSFU_NoName_DegradesToEmpty(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("relay must NOT be called when no name is configured")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"available":true,"server_url":"https://leak.example"}`))
	}))
	defer relay.Close()

	t.Setenv("VULOS_RELAY_BASE_URL", relay.URL)
	t.Setenv("VULOS_RELAY_NAME", "")
	if got := resolveSFUServerURL(context.Background()); got != "" {
		t.Fatalf("no relay name must degrade to empty meet_url, got %q", got)
	}
}

func TestResolveSFU_NoHostRegistered_Empty(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"available":false}`)) // registry empty / disabled
	}))
	defer relay.Close()

	t.Setenv("VULOS_RELAY_BASE_URL", relay.URL)
	t.Setenv("VULOS_RELAY_NAME", "box1")
	if got := resolveSFUServerURL(context.Background()); got != "" {
		t.Fatalf("available=false must degrade to empty meet_url, got %q", got)
	}
}

// TestResolveSFU_DefaultsToCanonicalRelay proves the SFU-host resolve DEFAULTS to
// the canonical relay URL (VULOS_RELAY_BASE_URL) when the meet-specific override
// MEET_HOST_RELAY_URL is unset — so configuring reachability via the canonical env
// alone does NOT silently disable SFU-host resolve.
func TestResolveSFU_DefaultsToCanonicalRelay(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"available":true,"server_url":"https://box1.example","host_id":"vula:box1"}`))
	}))
	defer relay.Close()

	t.Setenv("MEET_HOST_RELAY_URL", "") // override unset — must fall back
	t.Setenv("VULOS_RELAY_BASE_URL", relay.URL)
	t.Setenv("VULOS_RELAY_NAME", "box1")
	if got := resolveSFUServerURL(context.Background()); got != "https://box1.example" {
		t.Fatalf("with MEET_HOST_RELAY_URL unset, resolve must default to VULOS_RELAY_BASE_URL, got %q", got)
	}
}

// TestResolveSFU_HostRelayOverrideWins proves MEET_HOST_RELAY_URL, when set, points
// SFU-host resolution at a DIFFERENT relay than the canonical reachability relay.
func TestResolveSFU_HostRelayOverrideWins(t *testing.T) {
	override := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"available":true,"server_url":"https://override.example"}`))
	}))
	defer override.Close()
	canonical := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("canonical relay must NOT be called when MEET_HOST_RELAY_URL override is set")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer canonical.Close()

	t.Setenv("MEET_HOST_RELAY_URL", override.URL)
	t.Setenv("VULOS_RELAY_BASE_URL", canonical.URL)
	t.Setenv("VULOS_RELAY_NAME", "box1")
	if got := resolveSFUServerURL(context.Background()); got != "https://override.example" {
		t.Fatalf("MEET_HOST_RELAY_URL override must win over VULOS_RELAY_BASE_URL, got %q", got)
	}
}

func TestResolveSFU_RelayError_DegradesToEmpty(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer relay.Close()

	t.Setenv("VULOS_RELAY_BASE_URL", relay.URL)
	t.Setenv("VULOS_RELAY_NAME", "box1")
	if got := resolveSFUServerURL(context.Background()); got != "" {
		t.Fatalf("a relay error must never block the mint — meet_url must be empty, got %q", got)
	}
}
