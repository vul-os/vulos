package trydemo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestFlyClient stands up an httptest.Server with the provided handler and
// returns a flyClient pointed at it. The base URL is overridden so the client
// talks to the test server instead of the live Fly API.
func newTestFlyClient(t *testing.T, app, machineID string, handler http.HandlerFunc) *flyClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &flyClient{
		token:     "test-token",
		app:       app,
		machineID: machineID,
		hc:        srv.Client(),
		baseURL:   srv.URL,
	}
}

func TestFlyClient_Start(t *testing.T) {
	var gotPath, gotAuth, gotMethod string
	c := newTestFlyClient(t, "vulos-try", "m-1", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	})
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if gotPath != "/apps/vulos-try/machines/m-1/start" {
		t.Errorf("path = %q, want /apps/vulos-try/machines/m-1/start", gotPath)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("auth = %q, want Bearer test-token", gotAuth)
	}
}

func TestFlyClient_Stop(t *testing.T) {
	var gotPath string
	c := newTestFlyClient(t, "vulos-try", "m-1", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})
	if err := c.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if gotPath != "/apps/vulos-try/machines/m-1/stop" {
		t.Errorf("path = %q, want /apps/vulos-try/machines/m-1/stop", gotPath)
	}
}

func TestFlyClient_Restart(t *testing.T) {
	var gotPath string
	c := newTestFlyClient(t, "vulos-try", "m-1", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})
	if err := c.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if gotPath != "/apps/vulos-try/machines/m-1/restart" {
		t.Errorf("path = %q, want /apps/vulos-try/machines/m-1/restart", gotPath)
	}
}

func TestFlyClient_Status_Started(t *testing.T) {
	c := newTestFlyClient(t, "vulos-try", "m-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if r.URL.Path != "/apps/vulos-try/machines/m-1" {
			t.Errorf("path = %q, want /apps/vulos-try/machines/m-1", r.URL.Path)
		}
		resp := map[string]any{
			"state":      "started",
			"region":     "jnb",
			"created_at": "2026-05-24T10:00:00Z",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Started {
		t.Error("expected Started=true for started machine")
	}
	if st.Region != "jnb" {
		t.Errorf("Region = %q, want jnb", st.Region)
	}
	if st.StartedAt.IsZero() {
		t.Error("expected StartedAt parsed from created_at")
	}
}

func TestFlyClient_Status_Stopped(t *testing.T) {
	c := newTestFlyClient(t, "vulos-try", "m-1", func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{"state": "stopped", "region": "jnb"}
		_ = json.NewEncoder(w).Encode(resp)
	})
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Started {
		t.Error("expected Started=false for stopped machine")
	}
}

func TestFlyClient_Status_NoMachineConfigured(t *testing.T) {
	// No machine id → Status reports not-started, no HTTP performed.
	c := &flyClient{token: "test-token", app: "vulos-try", machineID: "", baseURL: flyAPIBase, hc: http.DefaultClient}
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status (no machine): %v", err)
	}
	if st.Started {
		t.Error("expected Started=false when no machine configured")
	}
}

func TestFlyClient_Start_NoMachineConfigured(t *testing.T) {
	c := &flyClient{token: "test-token", app: "vulos-try", machineID: "", baseURL: flyAPIBase, hc: http.DefaultClient}
	if err := c.Start(context.Background()); err == nil {
		t.Fatal("expected error starting with no machine id, got nil")
	}
}

func TestFlyClient_Status_HTTPError(t *testing.T) {
	c := newTestFlyClient(t, "vulos-try", "m-1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if _, err := c.Status(context.Background()); err == nil {
		t.Fatal("expected error on 500, got nil")
	}
}

func TestFlyClient_Post_HTTPError(t *testing.T) {
	c := newTestFlyClient(t, "vulos-try", "m-1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	err := c.Start(context.Background())
	if err == nil {
		t.Fatal("expected error on 502, got nil")
	}
	if !strings.Contains(err.Error(), "fly") {
		t.Errorf("error %q should mention fly", err)
	}
}

// TestNewFlyClient_DefaultBaseURL ensures the constructor wires the production
// base URL when no override is supplied.
func TestNewFlyClient_DefaultBaseURL(t *testing.T) {
	dm := NewFlyClient("tok", "vulos-try", "m-1")
	fc, ok := dm.(*flyClient)
	if !ok {
		t.Fatalf("NewFlyClient returned %T, want *flyClient", dm)
	}
	if fc.baseURL != flyAPIBase {
		t.Errorf("baseURL = %q, want %q", fc.baseURL, flyAPIBase)
	}
	if fc.machineURL("start") != flyAPIBase+"/apps/vulos-try/machines/m-1/start" {
		t.Errorf("machineURL(start) = %q", fc.machineURL("start"))
	}
}
