package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"vulos/backend/internal/crdtsync"
	"vulos/backend/internal/fabric"
	"vulos/backend/internal/sqlcrdt"
)

// These tests exist because of a specific precedent in this repo: a sync hot
// path once shipped with passing tests, zero callers and its route registered
// on no mux. Green tests over a transport nothing calls prove nothing, so the
// wiring itself is what is asserted here.

const wiringSecret = "wiring-test-secret"

func newWiringDBDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Create the real reminders table so the bridge has something to bind.
	db, err := sql.Open("sqlite", filepath.Join(dir, "reminders.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS reminders (
		id TEXT PRIMARY KEY, user_id TEXT NOT NULL, text TEXT NOT NULL,
		remind_at INTEGER NOT NULL, created_at INTEGER NOT NULL,
		done INTEGER NOT NULL DEFAULT 0);`); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestStartCRDTSyncRegistersWorkingRoutes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux := http.NewServeMux()
	store, err := startCRDTSync(ctx, mux, newWiringDBDir(t), "INSTANCE-A", wiringSecret,
		fabric.NewStaticDiscoverer(), nil, nil)
	if err != nil {
		t.Fatalf("startCRDTSync: %v", err)
	}
	defer store.Close()

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Every endpoint must exist, and must refuse an unauthenticated caller.
	for _, ep := range []struct{ method, path string }{
		{http.MethodPost, "/api/crdt/pull"},
		{http.MethodPost, "/api/crdt/push"},
		{http.MethodGet, "/api/crdt/status"},
	} {
		req, err := http.NewRequest(ep.method, srv.URL+ep.path, strings.NewReader(`{"domain":"`+crdtsync.DomainReminders+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s: %v", ep.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			t.Errorf("%s is not registered on the LAN mux", ep.path)
			continue
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without the secret: status %d, want 401", ep.path, resp.StatusCode)
		}

		req2, _ := http.NewRequest(ep.method, srv.URL+ep.path, strings.NewReader(`{"domain":"`+crdtsync.DomainReminders+`"}`))
		req2.Header.Set(crdtsync.AuthHeader, wiringSecret)
		resp2, err := srv.Client().Do(req2)
		if err != nil {
			t.Fatalf("%s: %v", ep.path, err)
		}
		resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			t.Errorf("%s with the secret: status %d, want 200", ep.path, resp2.StatusCode)
		}
	}
}

func TestStartCRDTSyncOpensTheApprovedDomainsOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := startCRDTSync(ctx, http.NewServeMux(), newWiringDBDir(t), "INSTANCE-A", wiringSecret,
		fabric.NewStaticDiscoverer(), nil, nil)
	if err != nil {
		t.Fatalf("startCRDTSync: %v", err)
	}
	defer store.Close()

	if err := store.Set(crdtsync.DomainReminders, "id:1", "text", []byte("x")); err != nil {
		t.Errorf("the approved domain was not opened: %v", err)
	}
	for _, refused := range []string{"sql:sessions", "sql:users", "sql:profiles"} {
		if err := store.Set(refused, "k", "f", []byte("x")); err == nil {
			t.Errorf("%s is replicable through the production wiring", refused)
		}
	}
}

func TestStartCRDTSyncFailsClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := newWiringDBDir(t)

	t.Run("no secret", func(t *testing.T) {
		// An unauthenticated exchange endpoint must never be mounted.
		mux := http.NewServeMux()
		if store, err := startCRDTSync(ctx, mux, dir, "A", "", fabric.NewStaticDiscoverer(), nil, nil); err == nil {
			store.Close()
			t.Fatal("startCRDTSync succeeded with no secret")
		}
		req := httptest.NewRequest(http.MethodPost, "/api/crdt/pull", nil)
		if _, pattern := mux.Handler(req); pattern != "" {
			t.Fatalf("a route was mounted despite no secret: %q", pattern)
		}
	})

	t.Run("no discoverer", func(t *testing.T) {
		if store, err := startCRDTSync(ctx, http.NewServeMux(), dir, "A", wiringSecret, nil, nil, nil); err == nil {
			store.Close()
			t.Fatal("startCRDTSync succeeded with no discoverer")
		}
	})

	t.Run("no bridgeable table", func(t *testing.T) {
		// An empty db dir has no reminders table: the engine must report
		// failure rather than run with nothing bridged, which would look
		// healthy while replicating nothing.
		empty := t.TempDir()
		if store, err := startCRDTSync(ctx, http.NewServeMux(), empty, "A", wiringSecret,
			fabric.NewStaticDiscoverer(), nil, nil); err == nil {
			store.Close()
			t.Fatal("startCRDTSync succeeded with no bridgeable table")
		}
	})
}

// TestMainWiresCRDTSync is the anti-dead-code guard. startCRDTSync could be
// perfect and still be worth nothing if main.go never calls it — which is
// exactly what happened to services/sync/hotpath.go. This asserts the call
// exists at the LAN mux site.
func TestMainWiresCRDTSync(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "startCRDTSync(") {
		t.Fatal("main.go does not call startCRDTSync — the engine would have no callers")
	}
	// It must be mounted on the LAN-only mux, not the public one.
	idx := strings.Index(body, "startCRDTSync(")
	call := body[idx:min(idx+220, len(body))]
	if !strings.Contains(call, "fabricMux") {
		t.Errorf("startCRDTSync is not passed the LAN-only mux: %q", call)
	}
}

// TestReplicatedTablesArePolicyApproved re-asserts at the wiring layer what
// sqlcrdt asserts internally: nothing gets bridged that policy did not approve.
func TestReplicatedTablesArePolicyApproved(t *testing.T) {
	approved := map[string]bool{}
	for _, d := range crdtsync.SyncableDomains() {
		approved[d] = true
	}
	for _, rt := range sqlcrdt.ReplicatedTables() {
		if !approved[rt.Domain] {
			t.Errorf("%s is bridged by the wiring but not approved by policy", rt.Domain)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
