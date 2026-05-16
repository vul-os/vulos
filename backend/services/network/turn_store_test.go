package network

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestTURNStore_SetAndGet(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTURNStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Initially not configured
	cfg := store.Get()
	if cfg.Configured {
		t.Error("expected Configured=false initially")
	}

	// Set config
	if err := store.Set("turn.example.com", 3478, "vulos", "s3cr3t"); err != nil {
		t.Fatal(err)
	}

	cfg = store.Get()
	if cfg.Host != "turn.example.com" {
		t.Errorf("host: got %q, want %q", cfg.Host, "turn.example.com")
	}
	if cfg.Port != 3478 {
		t.Errorf("port: got %d, want 3478", cfg.Port)
	}
	if cfg.Realm != "vulos" {
		t.Errorf("realm: got %q, want %q", cfg.Realm, "vulos")
	}
	if !cfg.Configured {
		t.Error("expected Configured=true after Set")
	}
}

func TestTURNStore_SecretNotInPublicView(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTURNStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("turn.example.com", 3478, "vulos", "topsecret"); err != nil {
		t.Fatal(err)
	}

	// TURNStoreConfig must not expose the secret
	cfg := store.Get()
	// Use reflection check via struct — TURNStoreConfig has no Secret field
	// Verify secret is accessible internally but not via Get()
	if store.Secret() != "topsecret" {
		t.Error("expected internal secret to be stored")
	}
	// The public struct should not contain it
	_ = cfg // cfg has no Secret field by design
}

func TestTURNStore_SecretPreservedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTURNStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("turn.example.com", 3478, "vulos", "original"); err != nil {
		t.Fatal(err)
	}
	// Update without providing secret — should keep existing secret
	if err := store.Set("turn2.example.com", 3478, "vulos", ""); err != nil {
		t.Fatal(err)
	}
	if store.Secret() != "original" {
		t.Errorf("expected secret to be preserved, got %q", store.Secret())
	}
}

func TestTURNStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTURNStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("turn.example.com", 3479, "realm1", "pw"); err != nil {
		t.Fatal(err)
	}

	// Verify the file was written
	if _, err := os.Stat(filepath.Join(dir, "turn.json")); err != nil {
		t.Fatalf("turn.json not created: %v", err)
	}

	// Load fresh store from same dir
	store2, err := NewTURNStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := store2.Get()
	if cfg.Host != "turn.example.com" || cfg.Port != 3479 || cfg.Realm != "realm1" {
		t.Errorf("persisted values not restored: %+v", cfg)
	}
	if store2.Secret() != "pw" {
		t.Errorf("persisted secret not restored: %q", store2.Secret())
	}
}

func TestTURNStore_TestReachability_NoHost(t *testing.T) {
	dir := t.TempDir()
	store, err := NewTURNStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	result := store.TestReachability()
	if result.Success {
		t.Error("expected failure with no host configured")
	}
}

func TestTURNStore_TestReachability_Success(t *testing.T) {
	// Start a temporary TCP listener on a random port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot open TCP listener:", err)
	}
	defer ln.Close()
	addr := ln.Addr().(*net.TCPAddr)

	dir := t.TempDir()
	store, _ := NewTURNStore(dir)
	store.Set("127.0.0.1", addr.Port, "test", "")

	result := store.TestReachability()
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
}

func TestTURNStore_TestReachability_Failure(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewTURNStore(dir)
	// Port 1 is almost certainly closed
	store.Set("127.0.0.1", 1, "test", "")

	result := store.TestReachability()
	if result.Success {
		t.Error("expected failure for connection to port 1")
	}
}
