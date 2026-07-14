package main

// HTTP endpoint tests for the admin OS-snapshot routes (routes_snapshots.go).
//
// They exercise the wiring through an in-memory ObjectStore (no live MinIO),
// asserting:
//   - snapshot-now creates a listable snapshot
//   - list / usage report the snapshot
//   - restore is admin-gated AND requires the destructive confirm guard
//   - a full snapshot → mutate → restore roundtrip returns the box to state
//   - an integrity failure aborts restore fail-closed (HTTP 422, box untouched)
//   - non-admin / unauthenticated callers are rejected
//   - endpoints report 503 when no object store is configured

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"

	"vulos/backend/services/snapshot"
)

// ── in-memory ObjectStore satisfying snapshot.ObjectStore ────────────────────

type memObj struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemObj() *memObj { return &memObj{data: map[string][]byte{}} }

func (m *memObj) List(_ context.Context, prefix string) ([]snapshot.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []snapshot.ObjectInfo
	for k, v := range m.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, snapshot.ObjectInfo{Key: k, Size: int64(len(v))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
func (m *memObj) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, io.EOF
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), v...))), nil
}
func (m *memObj) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = b
	return nil
}
func (m *memObj) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}
func (m *memObj) Stat(_ context.Context, key string) (snapshot.ObjectInfo, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return snapshot.ObjectInfo{}, false, nil
	}
	return snapshot.ObjectInfo{Key: key, Size: int64(len(v))}, true, nil
}

func newSnapMux(t *testing.T, store *memObj, available bool) (http.Handler, string) {
	t.Helper()
	authStore, adminID := adminStore(t)
	deps := snapshotDeps{authStore: authStore, policy: snapshot.DefaultPolicy}
	if available {
		deps.newSnapshotter = func() (*snapshot.Snapshotter, error) {
			return snapshot.New(store, snapshot.Config{DataPrefix: "os", AccountID: "acct"}), nil
		}
	}
	mux := http.NewServeMux()
	registerSnapshotRoutes(mux, deps)
	return mux, adminID
}

func seedObj(m *memObj, kv map[string]string) {
	for k, v := range kv {
		m.data["os/"+k] = []byte(v)
	}
}

func TestSnapshotRoutesAdminGate(t *testing.T) {
	store := newMemObj()
	h, _ := newSnapMux(t, store, true)

	// unauthenticated
	if rec := doReq(t, h, "POST", "/api/admin/snapshots", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth POST snapshots = %d, want 401", rec.Code)
	}
	// non-admin
	if rec := doReq(t, h, "GET", "/api/admin/snapshots", "not-a-real-user", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin GET snapshots = %d, want 403", rec.Code)
	}
}

func TestSnapshotRoutesUnavailable503(t *testing.T) {
	store := newMemObj()
	h, adminID := newSnapMux(t, store, false) // no snapshotter
	if rec := doReq(t, h, "POST", "/api/admin/snapshots", adminID, ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no-store POST snapshots = %d, want 503", rec.Code)
	}
}

func TestSnapshotCreateListUsage(t *testing.T) {
	store := newMemObj()
	seedObj(store, map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	h, adminID := newSnapMux(t, store, true)

	rec := doReq(t, h, "POST", "/api/admin/snapshots", adminID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot now = %d: %s", rec.Code, rec.Body.String())
	}
	var idx snapshot.Index
	if err := json.Unmarshal(rec.Body.Bytes(), &idx); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	if idx.ObjectCount != 2 {
		t.Fatalf("object_count = %d, want 2", idx.ObjectCount)
	}

	rec = doReq(t, h, "GET", "/api/admin/snapshots", adminID, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), idx.ID) {
		t.Fatalf("list did not include snapshot %s: %d %s", idx.ID, rec.Code, rec.Body.String())
	}

	rec = doReq(t, h, "GET", "/api/admin/snapshots/usage", adminID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("usage = %d", rec.Code)
	}
	var u snapshot.Usage
	json.Unmarshal(rec.Body.Bytes(), &u)
	if u.BlobCount != 2 || u.TotalBytes == 0 {
		t.Fatalf("unexpected usage: %+v", u)
	}
}

func TestSnapshotRestoreConfirmGateAndRoundtrip(t *testing.T) {
	store := newMemObj()
	seedObj(store, map[string]string{"a.txt": "alpha", "b.txt": "beta"})
	h, adminID := newSnapMux(t, store, true)

	// create snapshot
	rec := doReq(t, h, "POST", "/api/admin/snapshots", adminID, "")
	var idx snapshot.Index
	json.Unmarshal(rec.Body.Bytes(), &idx)

	// mutate live state
	store.mu.Lock()
	store.data["os/a.txt"] = []byte("MUTATED")
	delete(store.data, "os/b.txt")
	store.data["os/c.txt"] = []byte("added")
	store.mu.Unlock()

	// restore without confirm → 400
	if rec := doReq(t, h, "POST", "/api/admin/snapshots/"+idx.ID+"/restore", adminID, "{}"); rec.Code != http.StatusBadRequest {
		t.Fatalf("restore w/o confirm = %d, want 400", rec.Code)
	}

	// restore with confirm → 200, state returns
	rec = doReq(t, h, "POST", "/api/admin/snapshots/"+idx.ID+"/restore", adminID, `{"confirm":"RESTORE"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore = %d: %s", rec.Code, rec.Body.String())
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if string(store.data["os/a.txt"]) != "alpha" {
		t.Fatalf("a.txt not restored: %q", store.data["os/a.txt"])
	}
	if string(store.data["os/b.txt"]) != "beta" {
		t.Fatalf("b.txt not restored: %q", store.data["os/b.txt"])
	}
	if _, ok := store.data["os/c.txt"]; ok {
		t.Fatalf("c.txt (added after snapshot) not deleted by restore")
	}
}

func TestSnapshotRestoreIntegrityFailure422(t *testing.T) {
	store := newMemObj()
	seedObj(store, map[string]string{"a.txt": "alpha"})
	h, adminID := newSnapMux(t, store, true)

	rec := doReq(t, h, "POST", "/api/admin/snapshots", adminID, "")
	var idx snapshot.Index
	json.Unmarshal(rec.Body.Bytes(), &idx)

	// Corrupt a blob so integrity verification fails.
	store.mu.Lock()
	for k := range store.data {
		if strings.Contains(k, "/_snapshots/blobs/") {
			store.data[k] = []byte("corrupt")
		}
	}
	store.data["os/a.txt"] = []byte("MUTATED")
	store.mu.Unlock()

	rec = doReq(t, h, "POST", "/api/admin/snapshots/"+idx.ID+"/restore", adminID, `{"confirm":"RESTORE"}`)
	if rec.Code != 422 {
		t.Fatalf("corrupt restore = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	// Box untouched (fail-closed).
	store.mu.Lock()
	defer store.mu.Unlock()
	if string(store.data["os/a.txt"]) != "MUTATED" {
		t.Fatalf("integrity-aborted restore mutated live state")
	}
}
