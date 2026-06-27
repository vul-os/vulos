package multiinstance_test

// MINST-02 tests: cloud-sync pull + upsert, presence events, HTTP handler.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"vulos/backend/internal/multiinstance"
)

// TestMain opts into the plaintext-http escape hatch for the whole package: the
// httptest cloud stubs below serve http:// URLs (and some tests point
// VULOS_CLOUD_URL at http://127.0.0.1:0), which requireSecureCloudBase
// otherwise rejects to avoid leaking the device bearer token over plaintext.
// This mirrors the dev/test-only VULOS_LANCERT_ALLOW_INSECURE usage in the lan
// tests.
func TestMain(m *testing.M) {
	os.Setenv("VULOS_CLOUD_ALLOW_INSECURE", "1")
	// provision_billing_test.go points a cpbilling.Client at an http stub too.
	os.Setenv("VULOS_CP_ALLOW_INSECURE", "1")
	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// startCloudStub launches a test HTTP server that mimics the Vulos cloud
// control plane.  instances is the slice returned by GET /api/instances.
// It returns the server URL and a cleanup function.
func startCloudStub(t *testing.T, instances []interface{}) (string, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/instances", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(instances)
	})
	srv := httptest.NewServer(mux)
	return srv.URL, srv.Close
}

// ---------------------------------------------------------------------------
// Sync (one-shot pull)
// ---------------------------------------------------------------------------

func TestCloudSync_SyncUpserts(t *testing.T) {
	reg := openTempRegistry(t)

	wire := []interface{}{
		map[string]interface{}{
			"ulid":               "01HWZCLOUD00000000000001",
			"display_name":       "Cloud Laptop",
			"kind":               "device",
			"endpoint_url":       "https://laptop.example.com",
			"ed25519_public_key": "PUBKEY1",
			"role":               "owner",
			"status":             "online",
			"last_seen_at":       time.Now().UTC().Format(time.RFC3339Nano),
		},
		map[string]interface{}{
			"ulid":         "01HWZCLOUD00000000000002",
			"display_name": "Cloud Pi",
			"kind":         "device",
			"role":         "peer",
			"status":       "offline",
		},
	}

	url, cleanup := startCloudStub(t, wire)
	defer cleanup()

	t.Setenv("VULOS_CLOUD_URL", url)

	cs := multiinstance.NewCloudSyncer(reg, "test-token")
	if err := cs.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	list, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(list))
	}

	found := make(map[string]multiinstance.Instance)
	for _, inst := range list {
		found[inst.ULID] = inst
	}

	inst1 := found["01HWZCLOUD00000000000001"]
	if inst1.DisplayName != "Cloud Laptop" {
		t.Errorf("DisplayName: got %q want %q", inst1.DisplayName, "Cloud Laptop")
	}
	if inst1.Role != multiinstance.RoleOwner {
		t.Errorf("Role: got %q want %q", inst1.Role, multiinstance.RoleOwner)
	}
	if inst1.Status != multiinstance.StatusOnline {
		t.Errorf("Status: got %q want %q", inst1.Status, multiinstance.StatusOnline)
	}

	inst2 := found["01HWZCLOUD00000000000002"]
	if inst2.Status != multiinstance.StatusOffline {
		t.Errorf("Status: got %q want %q", inst2.Status, multiinstance.StatusOffline)
	}
}

func TestCloudSync_SyncUpsertDeduplicates(t *testing.T) {
	reg := openTempRegistry(t)

	ulid := "01HWZCLOUD00000000000010"

	// Seed the registry.
	if err := reg.Upsert(multiinstance.Instance{
		ULID:        ulid,
		DisplayName: "Old Name",
		Kind:        multiinstance.KindDevice,
		Status:      multiinstance.StatusUnknown,
	}); err != nil {
		t.Fatalf("Upsert seed: %v", err)
	}

	wire := []interface{}{
		map[string]interface{}{
			"ulid":         ulid,
			"display_name": "New Name",
			"kind":         "device",
			"status":       "online",
		},
	}
	url, cleanup := startCloudStub(t, wire)
	defer cleanup()
	t.Setenv("VULOS_CLOUD_URL", url)

	cs := multiinstance.NewCloudSyncer(reg, "")
	if err := cs.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	list, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 instance (dedup), got %d", len(list))
	}
	if list[0].DisplayName != "New Name" {
		t.Errorf("DisplayName: got %q want %q", list[0].DisplayName, "New Name")
	}
}

func TestCloudSync_SyncGracefulDegradationOnUnreachable(t *testing.T) {
	reg := openTempRegistry(t)

	// Seed one instance so we can verify it survives.
	if err := reg.Upsert(multiinstance.Instance{
		ULID:        "01HWZCLOUD00000000000020",
		DisplayName: "Existing Instance",
		Kind:        multiinstance.KindDevice,
		Status:      multiinstance.StatusUnknown,
	}); err != nil {
		t.Fatalf("Upsert seed: %v", err)
	}

	// Point at a URL that is guaranteed to fail.
	t.Setenv("VULOS_CLOUD_URL", "http://127.0.0.1:0")

	cs := multiinstance.NewCloudSyncer(reg, "token")
	err := cs.Sync(context.Background())
	if err == nil {
		t.Fatal("Sync: expected error for unreachable cloud, got nil")
	}
	// LastSyncErr must reflect the failure.
	if cs.LastSyncErr() == nil {
		t.Error("LastSyncErr: expected non-nil after failed Sync")
	}

	// The existing registry entry must be intact (graceful degradation).
	list, err2 := reg.List()
	if err2 != nil {
		t.Fatalf("List: %v", err2)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 instance to survive unreachable cloud, got %d", len(list))
	}
	if list[0].DisplayName != "Existing Instance" {
		t.Errorf("DisplayName: got %q want %q", list[0].DisplayName, "Existing Instance")
	}
}

func TestCloudSync_SyncClearsErrorOnSuccess(t *testing.T) {
	reg := openTempRegistry(t)

	// First sync to an unreachable server to set an error.
	t.Setenv("VULOS_CLOUD_URL", "http://127.0.0.1:0")
	cs := multiinstance.NewCloudSyncer(reg, "")
	_ = cs.Sync(context.Background())
	if cs.LastSyncErr() == nil {
		t.Fatal("expected LastSyncErr != nil after first failed sync")
	}

	// Now point at a working stub.
	url, cleanup := startCloudStub(t, []interface{}{})
	defer cleanup()
	t.Setenv("VULOS_CLOUD_URL", url)

	if err := cs.Sync(context.Background()); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if cs.LastSyncErr() != nil {
		t.Errorf("LastSyncErr: expected nil after successful sync, got %v", cs.LastSyncErr())
	}
}

func TestCloudSync_SyncNon200Response(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	t.Setenv("VULOS_CLOUD_URL", srv.URL)

	reg := openTempRegistry(t)
	cs := multiinstance.NewCloudSyncer(reg, "bad-token")
	err := cs.Sync(context.Background())
	if err == nil {
		t.Fatal("Sync: expected error for 401 response")
	}
}

// ---------------------------------------------------------------------------
// ApplyPresenceEvent
// ---------------------------------------------------------------------------

func TestApplyPresenceEvent_Online(t *testing.T) {
	reg := openTempRegistry(t)

	ulid := "01HWZCLOUD00000000000030"
	if err := reg.Upsert(multiinstance.Instance{
		ULID:   ulid,
		Kind:   multiinstance.KindDevice,
		Status: multiinstance.StatusOffline,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	cs := multiinstance.NewCloudSyncer(reg, "")
	cs.ApplyPresenceEvent(multiinstance.PresenceEvent{
		Type: "online",
		ULID: ulid,
	})

	got, ok := reg.Get(ulid)
	if !ok {
		t.Fatal("Get: instance not found")
	}
	if got.Status != multiinstance.StatusOnline {
		t.Errorf("Status after online event: got %q want %q", got.Status, multiinstance.StatusOnline)
	}
}

func TestApplyPresenceEvent_Offline(t *testing.T) {
	reg := openTempRegistry(t)

	ulid := "01HWZCLOUD00000000000031"
	if err := reg.Upsert(multiinstance.Instance{
		ULID:   ulid,
		Kind:   multiinstance.KindDevice,
		Status: multiinstance.StatusOnline,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	cs := multiinstance.NewCloudSyncer(reg, "")
	cs.ApplyPresenceEvent(multiinstance.PresenceEvent{
		Type: "offline",
		ULID: ulid,
	})

	got, ok := reg.Get(ulid)
	if !ok {
		t.Fatal("Get: instance not found")
	}
	if got.Status != multiinstance.StatusOffline {
		t.Errorf("Status after offline event: got %q want %q", got.Status, multiinstance.StatusOffline)
	}
}

func TestApplyPresenceEvent_Upsert(t *testing.T) {
	reg := openTempRegistry(t)

	ulid := "01HWZCLOUD00000000000032"
	cs := multiinstance.NewCloudSyncer(reg, "")
	cs.ApplyPresenceEvent(multiinstance.PresenceEvent{
		Type: "upsert",
		Instance: &multiinstance.CloudInstance{
			ULID:        ulid,
			DisplayName: "Fresh Instance",
			Kind:        "cloud",
			Status:      "online",
		},
	})

	got, ok := reg.Get(ulid)
	if !ok {
		t.Fatal("Get: instance not found after upsert event")
	}
	if got.DisplayName != "Fresh Instance" {
		t.Errorf("DisplayName: got %q want %q", got.DisplayName, "Fresh Instance")
	}
	if got.Kind != multiinstance.KindCloud {
		t.Errorf("Kind: got %q want %q", got.Kind, multiinstance.KindCloud)
	}
}

func TestApplyPresenceEvent_InstanceFieldTakesPrecedenceOverULIDField(t *testing.T) {
	reg := openTempRegistry(t)

	ulid := "01HWZCLOUD00000000000033"
	if err := reg.Upsert(multiinstance.Instance{
		ULID:   ulid,
		Kind:   multiinstance.KindDevice,
		Status: multiinstance.StatusOffline,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	cs := multiinstance.NewCloudSyncer(reg, "")
	// Both ULID field and Instance.ULID present — Instance.ULID wins.
	cs.ApplyPresenceEvent(multiinstance.PresenceEvent{
		Type: "online",
		ULID: "irrelevant-ulid",
		Instance: &multiinstance.CloudInstance{
			ULID: ulid,
		},
	})

	got, ok := reg.Get(ulid)
	if !ok {
		t.Fatal("Get: instance not found")
	}
	if got.Status != multiinstance.StatusOnline {
		t.Errorf("Status: got %q want %q", got.Status, multiinstance.StatusOnline)
	}
}

func TestApplyPresenceEvent_UnknownTypeIsNoOp(t *testing.T) {
	reg := openTempRegistry(t)
	cs := multiinstance.NewCloudSyncer(reg, "")
	// Should not panic.
	cs.ApplyPresenceEvent(multiinstance.PresenceEvent{Type: "garbage"})
}

// ---------------------------------------------------------------------------
// GET /api/instances HTTP handler
// ---------------------------------------------------------------------------

func TestRegisterSyncHandlers_ReturnsRegistryList(t *testing.T) {
	reg := openTempRegistry(t)

	ulid1 := "01HWZCLOUD00000000000040"
	ulid2 := "01HWZCLOUD00000000000041"
	for _, ulid := range []string{ulid1, ulid2} {
		if err := reg.Upsert(multiinstance.Instance{
			ULID:   ulid,
			Kind:   multiinstance.KindDevice,
			Status: multiinstance.StatusOnline,
		}); err != nil {
			t.Fatalf("Upsert %s: %v", ulid, err)
		}
	}

	cs := multiinstance.NewCloudSyncer(reg, "")
	mux := http.NewServeMux()
	multiinstance.RegisterSyncHandlers(mux, cs)

	req := httptest.NewRequest(http.MethodGet, "/api/instances", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q want application/json", ct)
	}

	var resp struct {
		Instances      []multiinstance.Instance `json:"instances"`
		CloudSyncError string                   `json:"cloud_sync_error,omitempty"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Instances) != 2 {
		t.Fatalf("expected 2 instances, got %d", len(resp.Instances))
	}
	if resp.CloudSyncError != "" {
		t.Errorf("unexpected cloud_sync_error: %q", resp.CloudSyncError)
	}
}

func TestRegisterSyncHandlers_OfflineModeUsesLastKnown(t *testing.T) {
	reg := openTempRegistry(t)

	// Seed the registry as if a previous sync had run.
	if err := reg.Upsert(multiinstance.Instance{
		ULID:        "01HWZCLOUD00000000000050",
		DisplayName: "Cached Instance",
		Kind:        multiinstance.KindDevice,
		Status:      multiinstance.StatusUnknown,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Trigger a sync error to simulate the cloud being offline.
	t.Setenv("VULOS_CLOUD_URL", "http://127.0.0.1:0")
	cs := multiinstance.NewCloudSyncer(reg, "")
	_ = cs.Sync(context.Background())

	mux := http.NewServeMux()
	multiinstance.RegisterSyncHandlers(mux, cs)

	req := httptest.NewRequest(http.MethodGet, "/api/instances", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200 (graceful degradation); body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Instances      []multiinstance.Instance `json:"instances"`
		CloudSyncError string                   `json:"cloud_sync_error,omitempty"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The last-known instance should still be there.
	if len(resp.Instances) != 1 {
		t.Fatalf("expected 1 last-known instance, got %d", len(resp.Instances))
	}
	if resp.Instances[0].DisplayName != "Cached Instance" {
		t.Errorf("DisplayName: got %q want %q", resp.Instances[0].DisplayName, "Cached Instance")
	}
	// And the error must be surfaced.
	if resp.CloudSyncError == "" {
		t.Error("expected cloud_sync_error to be populated when sync failed")
	}
}

func TestRegisterSyncHandlers_EmptyRegistryReturnsEmptyArray(t *testing.T) {
	reg := openTempRegistry(t)
	cs := multiinstance.NewCloudSyncer(reg, "")
	mux := http.NewServeMux()
	multiinstance.RegisterSyncHandlers(mux, cs)

	req := httptest.NewRequest(http.MethodGet, "/api/instances", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", w.Code)
	}

	var resp struct {
		Instances []multiinstance.Instance `json:"instances"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Instances == nil || len(resp.Instances) != 0 {
		t.Errorf("expected empty array, got %v", resp.Instances)
	}
}

// ---------------------------------------------------------------------------
// Authorization header
// ---------------------------------------------------------------------------

func TestCloudSync_SendsAuthorizationHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, "[]")
	}))
	defer srv.Close()

	t.Setenv("VULOS_CLOUD_URL", srv.URL)

	reg := openTempRegistry(t)
	cs := multiinstance.NewCloudSyncer(reg, "my-device-cert")
	if err := cs.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	want := "VulosDevice my-device-cert"
	if gotAuth != want {
		t.Errorf("Authorization: got %q want %q", gotAuth, want)
	}
}

func TestCloudSync_NoAuthHeaderWhenTokenEmpty(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, "[]")
	}))
	defer srv.Close()

	t.Setenv("VULOS_CLOUD_URL", srv.URL)

	reg := openTempRegistry(t)
	cs := multiinstance.NewCloudSyncer(reg, "")
	if err := cs.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if gotAuth != "" {
		t.Errorf("Authorization: expected empty, got %q", gotAuth)
	}
}

// ---------------------------------------------------------------------------
// Context cancellation
// ---------------------------------------------------------------------------

func TestCloudSync_SyncRespectsContextCancellation(t *testing.T) {
	// Server that hangs indefinitely.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	t.Setenv("VULOS_CLOUD_URL", srv.URL)

	reg := openTempRegistry(t)
	cs := multiinstance.NewCloudSyncer(reg, "")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := cs.Sync(ctx)
	if err == nil {
		t.Fatal("expected error when context is cancelled")
	}
}

// ---------------------------------------------------------------------------
// Ensure VULOS_CLOUD_URL does not bleed between tests
// ---------------------------------------------------------------------------

func init() {
	// Safety: clear the env override so tests that don't set it don't inherit
	// a value from a previous test that called t.Setenv and crashed.
	_ = os.Unsetenv("VULOS_CLOUD_URL")
}
