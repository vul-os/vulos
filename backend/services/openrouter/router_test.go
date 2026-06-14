package openrouter_test

import (
	"testing"

	"vulos/backend/services/openrouter"
)

// TestClassify_TableDriven covers every rule path and the registry entry
// classifications that matter for the native-first re-architecture.
func TestClassify_TableDriven(t *testing.T) {
	tests := []struct {
		name   string
		intent openrouter.Intent
		want   openrouter.Lane
	}{
		// ── Rule 1: explicit URL always → WebApp ────────────────────────────
		{
			name:   "url_http",
			intent: openrouter.Intent{URL: "http://example.com"},
			want:   openrouter.LaneWebApp,
		},
		{
			name:   "url_https",
			intent: openrouter.Intent{URL: "https://vulos.org"},
			want:   openrouter.LaneWebApp,
		},
		{
			name:   "url_with_game_flag_still_webapp",
			intent: openrouter.Intent{URL: "https://game.example.com", Game: true},
			want:   openrouter.LaneWebApp,
		},

		// ── Rule 2: HTML MIME type → WebApp ─────────────────────────────────
		{
			name:   "mime_text_html",
			intent: openrouter.Intent{MIME: "text/html"},
			want:   openrouter.LaneWebApp,
		},
		{
			name:   "mime_text_html_charset",
			intent: openrouter.Intent{MIME: "text/html; charset=utf-8"},
			want:   openrouter.LaneWebApp,
		},

		// ── Rule 3: AppType==web or Web flag → WebApp ────────────────────────
		{
			name:   "app_type_web",
			intent: openrouter.Intent{AppID: "navidrome", AppType: "web"},
			want:   openrouter.LaneWebApp,
		},
		{
			name:   "web_flag",
			intent: openrouter.Intent{AppID: "mail", Web: true},
			want:   openrouter.LaneWebApp,
		},
		// Registry entries that should be web-native (ROUTER-01 tagging)
		{
			name:   "registry_office_web",
			intent: openrouter.Intent{AppID: "office", AppType: "web", Web: true},
			want:   openrouter.LaneWebApp,
		},
		{
			name:   "registry_mail_web",
			intent: openrouter.Intent{AppID: "mail", AppType: "web", Web: true},
			want:   openrouter.LaneWebApp,
		},
		{
			name:   "registry_terminal_web",
			intent: openrouter.Intent{AppID: "terminal", AppType: "web", Web: true},
			want:   openrouter.LaneWebApp,
		},
		{
			name:   "registry_notes_web",
			intent: openrouter.Intent{AppID: "notes", AppType: "web", Web: true},
			want:   openrouter.LaneWebApp,
		},
		{
			name:   "registry_smart_browser_web",
			intent: openrouter.Intent{AppID: "browser", AppType: "web", Web: true},
			want:   openrouter.LaneWebApp,
		},
		{
			name:   "registry_navidrome_web_type",
			intent: openrouter.Intent{AppID: "navidrome", AppType: "web"},
			want:   openrouter.LaneWebApp,
		},
		{
			name:   "registry_drawio_web_type",
			intent: openrouter.Intent{AppID: "drawio", AppType: "web"},
			want:   openrouter.LaneWebApp,
		},
		{
			name:   "registry_grafana_web_type",
			intent: openrouter.Intent{AppID: "grafana", AppType: "web"},
			want:   openrouter.LaneWebApp,
		},
		{
			name:   "registry_gitea_web_type",
			intent: openrouter.Intent{AppID: "gitea", AppType: "web"},
			want:   openrouter.LaneWebApp,
		},

		// ── Rule 4: LocalOnly flag → LocalOnly ───────────────────────────────
		{
			name:   "local_only_flag",
			intent: openrouter.Intent{AppID: "keepassxc", LocalOnly: true},
			want:   openrouter.LaneLocalOnly,
		},
		{
			name:   "local_only_overrides_compute",
			intent: openrouter.Intent{LocalOnly: true, ComputeJob: true},
			want:   openrouter.LaneLocalOnly,
		},

		// ── Rule 5: ComputeJob flag → ComputeWorker ──────────────────────────
		{
			name:   "compute_job_flag",
			intent: openrouter.Intent{AppID: "octave-batch", ComputeJob: true},
			want:   openrouter.LaneComputeWorker,
		},

		// ── Rule 6: NeedsGPU or Game flag → GPURoute ─────────────────────────
		{
			name:   "needs_gpu_flag",
			intent: openrouter.Intent{AppID: "blender", NeedsGPU: true},
			want:   openrouter.LaneGPURoute,
		},
		{
			name:   "registry_blender_needs_gpu",
			intent: openrouter.Intent{AppID: "blender", AppType: "desktop", NeedsGPU: true},
			want:   openrouter.LaneGPURoute,
		},
		{
			name:   "registry_kdenlive_needs_gpu",
			intent: openrouter.Intent{AppID: "kdenlive", AppType: "desktop", NeedsGPU: true},
			want:   openrouter.LaneGPURoute,
		},
		{
			name:   "game_flag",
			intent: openrouter.Intent{AppID: "steam", Game: true},
			want:   openrouter.LaneGPURoute,
		},
		{
			name:   "registry_steam_game",
			intent: openrouter.Intent{AppID: "steam", AppType: "desktop", Game: true},
			want:   openrouter.LaneGPURoute,
		},
		{
			name:   "registry_lutris_game",
			intent: openrouter.Intent{AppID: "lutris", AppType: "desktop", Game: true},
			want:   openrouter.LaneGPURoute,
		},
		{
			name:   "registry_wine_game",
			intent: openrouter.Intent{AppID: "wine", AppType: "desktop", Game: true},
			want:   openrouter.LaneGPURoute,
		},
		// ComputeJob takes priority over NeedsGPU (Rule 5 before Rule 6):
		// a headless GPU compute job routes as ComputeWorker.
		{
			name:   "compute_job_takes_priority_over_gpu",
			intent: openrouter.Intent{NeedsGPU: true, ComputeJob: true},
			want:   openrouter.LaneComputeWorker,
		},

		// ── Rule 7: plain native desktop app → CPUStream ─────────────────────
		{
			name:   "desktop_no_flags",
			intent: openrouter.Intent{AppID: "gimp", AppType: "desktop"},
			want:   openrouter.LaneCPUStream,
		},
		{
			name:   "registry_gimp_desktop",
			intent: openrouter.Intent{AppID: "gimp", AppType: "desktop"},
			want:   openrouter.LaneCPUStream,
		},
		{
			name:   "registry_inkscape_desktop",
			intent: openrouter.Intent{AppID: "inkscape", AppType: "desktop"},
			want:   openrouter.LaneCPUStream,
		},
		{
			name:   "registry_vlc_desktop",
			intent: openrouter.Intent{AppID: "vlc", AppType: "desktop"},
			want:   openrouter.LaneCPUStream,
		},
		{
			name:   "registry_audacity_desktop",
			intent: openrouter.Intent{AppID: "audacity", AppType: "desktop"},
			want:   openrouter.LaneCPUStream,
		},
		{
			name:   "registry_libreoffice_desktop",
			intent: openrouter.Intent{AppID: "libreoffice", AppType: "desktop"},
			want:   openrouter.LaneCPUStream,
		},
		{
			name:   "registry_libreoffice_desktop",
			intent: openrouter.Intent{AppID: "libreoffice", AppType: "desktop"},
			want:   openrouter.LaneCPUStream,
		},
		{
			name:   "registry_kicad_desktop",
			intent: openrouter.Intent{AppID: "kicad", AppType: "desktop"},
			want:   openrouter.LaneCPUStream,
		},
		{
			name:   "registry_geany_desktop",
			intent: openrouter.Intent{AppID: "geany", AppType: "desktop"},
			want:   openrouter.LaneCPUStream,
		},
		{
			name:   "registry_gnucash_desktop",
			intent: openrouter.Intent{AppID: "gnucash", AppType: "desktop"},
			want:   openrouter.LaneCPUStream,
		},
		{
			name:   "empty_intent_fallback",
			intent: openrouter.Intent{},
			want:   openrouter.LaneCPUStream,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := openrouter.Classify(tc.intent)
			if got != tc.want {
				t.Errorf("Classify(%+v) = %q; want %q", tc.intent, got, tc.want)
			}
		})
	}
}
