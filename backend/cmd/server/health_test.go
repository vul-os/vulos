package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// GET /api/health is in auth.publicPaths so that "curl the health endpoint"
// works as a first diagnostic on a box nobody can log into — three docs pages
// and roadmap/NETWORK.md's external-router design both assume it. What that
// buys an anonymous caller must stay limited to the VERDICT.
//
// The per-check detail is the sensitive half:
//
//	data_dir_writable on failure is "degraded: " + err.Error(), carrying the
//	  box's absolute data-dir path and the raw OS error;
//	disk_space carries exact free capacity in MiB;
//	sync_lag reveals whether S3 cluster sync exists and how recently it ran.
//
// These tests pin the split. They are the reason /api/health may be in
// publicPaths at all — TestPublicPaths_ExhaustiveAllowList in
// backend/services/security_test.go names them.

// decodeHealth runs the handler and returns the decoded body as a generic map,
// so an absent "checks" key is distinguishable from an empty one.
func decodeHealth(t *testing.T, dataDir string, userID string) (int, map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	if userID != "" {
		// What the auth middleware sets after validating a real session (it
		// strips any client-supplied copy first — C1/SEC-A).
		req.Header.Set("X-User-ID", userID)
	}
	rr := httptest.NewRecorder()
	handleClusterHealth(dataDir, nil)(rr, req)

	body := rr.Body.String()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("GET /api/health: body is not JSON: %v (body=%q)", err, body)
	}
	return rr.Code, out, body
}

func TestClusterHealth_AnonymousGetsVerdictOnly(t *testing.T) {
	code, out, body := decodeHealth(t, t.TempDir(), "")

	if code != 200 {
		t.Fatalf("healthy box, anonymous: want 200, got %d (body=%q)", code, body)
	}
	if out["status"] != "ok" {
		t.Errorf("anonymous caller must still get the verdict: want status=ok, got %v (body=%q)", out["status"], body)
	}
	if out["timestamp"] == "" || out["timestamp"] == nil {
		t.Errorf("anonymous caller must still get a timestamp (body=%q)", body)
	}
	if _, ok := out["checks"]; ok {
		t.Errorf("SECURITY: anonymous caller received the per-check detail — "+
			"it leaks the absolute data-dir path (on error), exact free disk, and S3 sync topology. body=%q", body)
	}
	// Belt and braces: no check string may reach the wire even under another key.
	for _, leak := range []string{"MiB free", "data_dir_writable", "sync_lag", "disk_space"} {
		if strings.Contains(body, leak) {
			t.Errorf("SECURITY: anonymous body contains %q — per-check detail leaked. body=%q", leak, body)
		}
	}
}

func TestClusterHealth_AnonymousDegradedLeaksNoPath(t *testing.T) {
	// A data dir that cannot be written: os.WriteFile's error carries the full
	// absolute probe path. This is the case that actually matters — a sick box
	// is exactly when someone curls this endpoint.
	badDir := "/nonexistent-vulos-health-probe-dir"

	code, out, body := decodeHealth(t, badDir, "")
	if code != 503 {
		t.Fatalf("degraded box, anonymous: want 503, got %d (body=%q)", code, body)
	}
	if out["status"] != "degraded" {
		t.Errorf("anonymous caller must still learn the box is degraded: got %v (body=%q)", out["status"], body)
	}
	if _, ok := out["checks"]; ok {
		t.Errorf("SECURITY: degraded anonymous response carried the checks map. body=%q", body)
	}
	if strings.Contains(body, badDir) {
		t.Errorf("SECURITY: degraded anonymous response leaked the data-dir path %q. body=%q", badDir, body)
	}
}

func TestClusterHealth_AuthenticatedGetsFullDetail(t *testing.T) {
	code, out, body := decodeHealth(t, t.TempDir(), "user-1")

	if code != 200 {
		t.Fatalf("healthy box, authenticated: want 200, got %d (body=%q)", code, body)
	}
	checks, ok := out["checks"].(map[string]any)
	if !ok {
		t.Fatalf("authenticated caller must get the per-check breakdown — the whole point of the "+
			"Box Health panel and of `curl -b cookie /api/health | jq`. body=%q", body)
	}
	for _, k := range []string{"data_dir_writable", "disk_space", "sync_lag"} {
		if _, ok := checks[k]; !ok {
			t.Errorf("authenticated checks map is missing %q: %v", k, checks)
		}
	}
}

func TestClusterHealth_AuthenticatedDegradedKeepsDiagnosis(t *testing.T) {
	badDir := "/nonexistent-vulos-health-probe-dir"

	code, out, body := decodeHealth(t, badDir, "user-1")
	if code != 503 {
		t.Fatalf("degraded box, authenticated: want 503, got %d (body=%q)", code, body)
	}
	checks, ok := out["checks"].(map[string]any)
	if !ok {
		t.Fatalf("authenticated caller must get the failing check — otherwise the endpoint cannot "+
			"diagnose anything. body=%q", body)
	}
	got, _ := checks["data_dir_writable"].(string)
	if !strings.HasPrefix(got, "degraded:") {
		t.Errorf("want data_dir_writable to report the failure, got %q", got)
	}
	if !strings.Contains(got, badDir) {
		t.Errorf("the authenticated detail is what names the failing path; want %q inside %q", badDir, got)
	}
}
