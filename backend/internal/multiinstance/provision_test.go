package multiinstance_test

// MINST-05 tests: Koyeb provisioning client + registry integration.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vulos/backend/internal/multiinstance"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// startProvisionStub starts a stub cloud control plane that handles:
//   - POST /api/instances/provision
//   - GET  /api/instances/{ulid}/status
func startProvisionStub(t *testing.T, provisionResp interface{}, statusResp interface{}) (string, func()) {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/instances/provision", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(provisionResp)
	})

	mux.HandleFunc("/api/instances/", func(w http.ResponseWriter, r *http.Request) {
		// matches /api/instances/{ulid}/status
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(statusResp)
	})

	srv := httptest.NewServer(mux)
	return srv.URL, srv.Close
}

// ---------------------------------------------------------------------------
// Provision
// ---------------------------------------------------------------------------

func TestProvision_ReturnsRequestAndUpserts(t *testing.T) {
	reg := openTempRegistry(t)

	cloudResp := map[string]interface{}{
		"request_id":    "req-001",
		"instance_ulid": "01HWZPROV0000000000000001",
		"status":        "provisioning",
		"endpoint_url":  "https://koyeb-instance.vulos.net",
	}
	url, cleanup := startProvisionStub(t, cloudResp, nil)
	defer cleanup()
	t.Setenv("VULOS_CLOUD_URL", url)

	p := multiinstance.NewProvisioner(reg, "test-device-cert")
	req, err := p.Provision(context.Background(), "ams", "small")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if req == nil {
		t.Fatal("Provision: returned nil request")
	}
	if req.InstanceULID != "01HWZPROV0000000000000001" {
		t.Errorf("InstanceULID: got %q want %q", req.InstanceULID, "01HWZPROV0000000000000001")
	}
	if req.Region != "ams" {
		t.Errorf("Region: got %q want %q", req.Region, "ams")
	}
	if req.Plan != "small" {
		t.Errorf("Plan: got %q want %q", req.Plan, "small")
	}

	// The new instance must appear in the registry immediately.
	inst, ok := reg.Get("01HWZPROV0000000000000001")
	if !ok {
		t.Fatal("Get: new instance not found in registry after Provision")
	}
	if inst.Kind != multiinstance.KindCloud {
		t.Errorf("Kind: got %q want %q", inst.Kind, multiinstance.KindCloud)
	}
}

func TestProvision_UnreachableCloudReturnsError(t *testing.T) {
	reg := openTempRegistry(t)
	t.Setenv("VULOS_CLOUD_URL", "http://127.0.0.1:0")

	p := multiinstance.NewProvisioner(reg, "token")
	_, err := p.Provision(context.Background(), "jnb", "small")
	if err == nil {
		t.Fatal("expected error for unreachable cloud, got nil")
	}
}

func TestProvision_EmptyRegionErrors(t *testing.T) {
	reg := openTempRegistry(t)
	p := multiinstance.NewProvisioner(reg, "token")
	_, err := p.Provision(context.Background(), "", "small")
	if err == nil {
		t.Fatal("expected error for empty region")
	}
}

func TestProvision_EmptyPlanErrors(t *testing.T) {
	reg := openTempRegistry(t)
	p := multiinstance.NewProvisioner(reg, "token")
	_, err := p.Provision(context.Background(), "ams", "")
	if err == nil {
		t.Fatal("expected error for empty plan")
	}
}

func TestProvision_Non200ResponseErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	t.Setenv("VULOS_CLOUD_URL", srv.URL)

	reg := openTempRegistry(t)
	p := multiinstance.NewProvisioner(reg, "bad-token")
	_, err := p.Provision(context.Background(), "ams", "small")
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

func TestProvision_SendsAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"request_id":    "req-auth",
			"instance_ulid": "01HWZPROV0000000000000010",
			"status":        "provisioning",
		})
	}))
	defer srv.Close()
	t.Setenv("VULOS_CLOUD_URL", srv.URL)

	reg := openTempRegistry(t)
	p := multiinstance.NewProvisioner(reg, "my-device-cert")
	_, err := p.Provision(context.Background(), "ams", "small")
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	want := "VulosDevice my-device-cert"
	if gotAuth != want {
		t.Errorf("Authorization header: got %q want %q", gotAuth, want)
	}
}

// ---------------------------------------------------------------------------
// PollStatus
// ---------------------------------------------------------------------------

func TestPollStatus_ReadyUpdatesRegistry(t *testing.T) {
	reg := openTempRegistry(t)

	ulid := "01HWZPROV0000000000000020"
	if err := reg.Upsert(multiinstance.Instance{
		ULID:   ulid,
		Kind:   multiinstance.KindCloud,
		Status: multiinstance.StatusUnknown,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	statusResp := map[string]interface{}{
		"instance_ulid": ulid,
		"status":        "ready",
		"endpoint_url":  "https://ready.vulos.net",
	}
	url, cleanup := startProvisionStub(t, nil, statusResp)
	defer cleanup()
	t.Setenv("VULOS_CLOUD_URL", url)

	p := multiinstance.NewProvisioner(reg, "token")
	status, err := p.PollStatus(context.Background(), ulid)
	if err != nil {
		t.Fatalf("PollStatus: %v", err)
	}
	if status != "ready" {
		t.Errorf("status: got %q want %q", status, "ready")
	}

	inst, ok := reg.Get(ulid)
	if !ok {
		t.Fatal("Get: instance not found after PollStatus")
	}
	if inst.Status != multiinstance.StatusOnline {
		t.Errorf("Status: got %q want %q", inst.Status, multiinstance.StatusOnline)
	}
	if inst.EndpointURL != "https://ready.vulos.net" {
		t.Errorf("EndpointURL: got %q want %q", inst.EndpointURL, "https://ready.vulos.net")
	}
}

func TestPollStatus_FailedSetsOffline(t *testing.T) {
	reg := openTempRegistry(t)

	ulid := "01HWZPROV0000000000000021"
	if err := reg.Upsert(multiinstance.Instance{
		ULID:   ulid,
		Kind:   multiinstance.KindCloud,
		Status: multiinstance.StatusUnknown,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	statusResp := map[string]interface{}{
		"instance_ulid": ulid,
		"status":        "failed",
		"error":         "launch timed out",
	}
	url, cleanup := startProvisionStub(t, nil, statusResp)
	defer cleanup()
	t.Setenv("VULOS_CLOUD_URL", url)

	p := multiinstance.NewProvisioner(reg, "token")
	status, err := p.PollStatus(context.Background(), ulid)
	if err != nil {
		t.Fatalf("PollStatus: %v", err)
	}
	if status != "failed" {
		t.Errorf("status: got %q want %q", status, "failed")
	}

	inst, ok := reg.Get(ulid)
	if !ok {
		t.Fatal("Get: instance not found after PollStatus")
	}
	if inst.Status != multiinstance.StatusOffline {
		t.Errorf("Status: got %q want %q", inst.Status, multiinstance.StatusOffline)
	}
}

func TestPollStatus_UnreachableCloudReturnsError(t *testing.T) {
	reg := openTempRegistry(t)
	t.Setenv("VULOS_CLOUD_URL", "http://127.0.0.1:0")

	p := multiinstance.NewProvisioner(reg, "token")
	_, err := p.PollStatus(context.Background(), "01HWZPROV0000000000000030")
	if err == nil {
		t.Fatal("expected error for unreachable cloud, got nil")
	}
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

func TestRegisterProvisionHandlers_ProvisionEndpoint(t *testing.T) {
	cloudResp := map[string]interface{}{
		"request_id":    "req-http-001",
		"instance_ulid": "01HWZPROV0000000000000040",
		"status":        "provisioning",
	}
	url, cleanup := startProvisionStub(t, cloudResp, nil)
	defer cleanup()
	t.Setenv("VULOS_CLOUD_URL", url)

	reg := openTempRegistry(t)
	p := multiinstance.NewProvisioner(reg, "token")

	mux := http.NewServeMux()
	multiinstance.RegisterProvisionHandlers(mux, p)

	body, _ := json.Marshal(map[string]string{"region": "lhr", "plan": "small"})
	req := httptest.NewRequest(http.MethodPost, "/api/instances/provision", jsonReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body: %s", w.Code, w.Body.String())
	}

	var resp multiinstance.ProvisionRequest
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.InstanceULID != "01HWZPROV0000000000000040" {
		t.Errorf("InstanceULID: got %q want %q", resp.InstanceULID, "01HWZPROV0000000000000040")
	}
}

func TestRegisterProvisionHandlers_MissingRegionReturns422(t *testing.T) {
	reg := openTempRegistry(t)
	p := multiinstance.NewProvisioner(reg, "")

	mux := http.NewServeMux()
	multiinstance.RegisterProvisionHandlers(mux, p)

	body, _ := json.Marshal(map[string]string{"plan": "small"})
	req := httptest.NewRequest(http.MethodPost, "/api/instances/provision", jsonReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status: got %d want 422", w.Code)
	}
}

func TestRegisterProvisionHandlers_StatusEndpoint(t *testing.T) {
	ulid := "01HWZPROV0000000000000050"
	statusResp := map[string]interface{}{
		"instance_ulid": ulid,
		"status":        "ready",
		"endpoint_url":  "https://ready2.vulos.net",
	}
	url, cleanup := startProvisionStub(t, nil, statusResp)
	defer cleanup()
	t.Setenv("VULOS_CLOUD_URL", url)

	reg := openTempRegistry(t)
	if err := reg.Upsert(multiinstance.Instance{
		ULID:   ulid,
		Kind:   multiinstance.KindCloud,
		Status: multiinstance.StatusUnknown,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	p := multiinstance.NewProvisioner(reg, "token")
	mux := http.NewServeMux()
	multiinstance.RegisterProvisionHandlers(mux, p)

	req := httptest.NewRequest(http.MethodGet, "/api/instances/"+ulid+"/status", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["status"] != "ready" {
		t.Errorf("status field: got %v want %q", resp["status"], "ready")
	}
}
