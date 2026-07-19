package main

// routes_router_test.go — unit tests for GET /api/router/classify and the
// supporting classifyAppID helper.  These tests run with CGO_ENABLED=0.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"vulos/backend/services/openrouter"
)

// TestClassifyAppID_KnownEntries checks that each registry-tagged app maps to
// its expected lane, verifying no entry is silently "unknown".
func TestClassifyAppID_KnownEntries(t *testing.T) {
	tests := []struct {
		appID string
		want  openrouter.Lane
	}{
		// Web-native built-ins
		{"office", openrouter.LaneWebApp},
		{"mail", openrouter.LaneWebApp},
		{"terminal", openrouter.LaneWebApp},
		{"notes", openrouter.LaneWebApp},
		{"library", openrouter.LaneWebApp},
		{"browser", openrouter.LaneWebApp},
		{"spaces", openrouter.LaneWebApp},
		{"calendar", openrouter.LaneWebApp},
		{"dashboard", openrouter.LaneWebApp},
		{"messages", openrouter.LaneWebApp},

		// registry.json type:web apps
		{"navidrome", openrouter.LaneWebApp},
		{"gitea", openrouter.LaneWebApp},
		{"grafana", openrouter.LaneWebApp},
		{"drawio", openrouter.LaneWebApp},
		{"cinny", openrouter.LaneWebApp},
		{"syncthing", openrouter.LaneWebApp},
		{"transmission", openrouter.LaneWebApp},
		{"cockpit", openrouter.LaneWebApp},
		{"jupyter", openrouter.LaneWebApp},

		// GPU-accelerated
		{"blender", openrouter.LaneGPURoute},
		{"kdenlive", openrouter.LaneGPURoute},
		{"darktable", openrouter.LaneGPURoute},
		{"obs-studio", openrouter.LaneGPURoute},
		{"shotcut", openrouter.LaneGPURoute},

		// Games
		{"steam", openrouter.LaneGPURoute},
		{"lutris", openrouter.LaneGPURoute},
		{"wine", openrouter.LaneGPURoute},

		// Plain desktop / CPU stream
		{"gimp", openrouter.LaneCPUStream},
		{"inkscape", openrouter.LaneCPUStream},
		{"vlc", openrouter.LaneCPUStream},
		{"audacity", openrouter.LaneCPUStream},
		{"libreoffice", openrouter.LaneCPUStream},
		{"kicad", openrouter.LaneCPUStream},
		{"geany", openrouter.LaneCPUStream},
	}

	for _, tc := range tests {
		t.Run(tc.appID, func(t *testing.T) {
			got := classifyAppID(tc.appID)
			if got != tc.want {
				t.Errorf("classifyAppID(%q) = %q; want %q", tc.appID, got, tc.want)
			}
		})
	}
}

// TestClassifyAppID_UnknownDefaultsCPUStream ensures an unrecognised app ID
// falls back to CPUStream rather than panicking or returning an empty lane.
func TestClassifyAppID_UnknownDefaultsCPUStream(t *testing.T) {
	got := classifyAppID("totally-unknown-app-xyz")
	if got != openrouter.LaneCPUStream {
		t.Errorf("unknown app ID: got %q; want %q", got, openrouter.LaneCPUStream)
	}
}

// TestRouterClassifyEndpoint_ValidApp checks the HTTP endpoint returns the
// correct JSON for a known app ID.
func TestRouterClassifyEndpoint_ValidApp(t *testing.T) {
	mux := http.NewServeMux()
	registerRouterRoutes(mux)

	tests := []struct {
		appID string
		lane  string
	}{
		{"blender", "GPURoute"},
		{"steam", "GPURoute"},
		{"mail", "WebApp"},
		{"browser", "WebApp"},
		{"gimp", "CPUStream"},
		{"navidrome", "WebApp"},
	}

	for _, tc := range tests {
		t.Run(tc.appID, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/router/classify?app="+tc.appID, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("status %d; want 200", w.Code)
			}
			var resp map[string]string
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp["lane"] != tc.lane {
				t.Errorf("lane = %q; want %q", resp["lane"], tc.lane)
			}
		})
	}
}

// TestRouterClassifyEndpoint_MissingAppID checks that omitting the app query
// parameter returns HTTP 400.
func TestRouterClassifyEndpoint_MissingAppID(t *testing.T) {
	mux := http.NewServeMux()
	registerRouterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/router/classify", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400; got %d", w.Code)
	}
}

// TestRouterClassifyEndpoint_InvalidChars checks that an app ID with illegal
// characters returns HTTP 400.
func TestRouterClassifyEndpoint_InvalidChars(t *testing.T) {
	mux := http.NewServeMux()
	registerRouterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/router/classify?app=../../etc/passwd", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400; got %d", w.Code)
	}
}

// TestWebAppLaunchSpawnsNoStreamSession asserts that the WebApp lane is
// returned for all web-native apps — the caller contract is that WebApp lane
// must result in zero stream.Session spawns (the frontend enforces this).
// This test provides a backend-level assertion that the classify result is
// correct; the non-stream dispatch lives in the frontend launcher.
func TestWebAppLaunchSpawnsNoStreamSession(t *testing.T) {
	webNativeApps := []string{
		"office", "mail", "terminal", "notes", "browser",
		"spaces", "calendar", "dashboard", "messages",
		"navidrome", "gitea", "grafana", "drawio", "cinny",
	}
	for _, appID := range webNativeApps {
		lane := classifyAppID(appID)
		if lane != openrouter.LaneWebApp {
			t.Errorf("app %q classified as %q — must be WebApp to guarantee zero stream.Session", appID, lane)
		}
	}
}
