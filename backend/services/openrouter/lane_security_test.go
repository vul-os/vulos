package openrouter

// lane_security_test.go — PENTEST-01 adversarial regressions for Open Router
// lane-confusion attacks.
//
// Attack categories covered:
//
//	LANE-SEC-01  WebApp intent cannot be coerced into a CPUStream / GPURoute /
//	             ComputeWorker lane — web apps must always stay in the WebApp lane
//	             so the launcher never opens a stream.Session for them.
//	LANE-SEC-02  LocalOnly intent cannot be misrouted to CPUStream or GPURoute
//	             (cross-tenant compute resource hijack).
//	LANE-SEC-03  ComputeJob intent cannot be coerced into WebApp or CPUStream lanes.
//	LANE-SEC-04  An explicit URL always overrides any flag combination — it must
//	             land on WebApp and never on a streaming lane.
//	LANE-SEC-05  Conflicting flag combinations cannot escalate a web-native app
//	             from WebApp to a GPU/Compute lane via NeedsGPU / ComputeJob flags.
//	LANE-SEC-06  MIME type "text/html*" is not exploitable to downgrade a
//	             NeedsGPU/Game intent from GPURoute (the URL/MIME rule is priority
//	             1/2 only when the attacker does NOT also set NeedsGPU; if both
//	             are set the MIME short-circuits to WebApp — which is a safe
//	             conservative downgrade, not an escalation to GPU compute).
//	LANE-SEC-07  An unknown AppID (no registry entry) conservatively defaults to
//	             CPUStream — it must never produce WebApp or LocalOnly silently.

import (
	"testing"
)

// ─── LANE-SEC-01: web app cannot be coerced into a streaming lane ─────────────

// TestWebApp_CannotBeCoercedToStream asserts that any intent with AppType=="web"
// or Web==true is always classified as WebApp, regardless of what other flags
// an attacker might add. This prevents a crafted intent from causing the
// launcher to open an unnecessary stream.Session for a browser-native app.
// Guards: PENTEST-01 LANE-SEC-01 — web apps never spawn stream.Session.
func TestWebApp_CannotBeCoercedToStream(t *testing.T) {
	coercions := []struct {
		desc   string
		intent Intent
	}{
		{
			"web type + NeedsGPU flag",
			Intent{AppID: "office", AppType: "web", NeedsGPU: true},
		},
		{
			"web type + Game flag",
			Intent{AppID: "steam-web", AppType: "web", Game: true},
		},
		{
			"web type + ComputeJob flag",
			Intent{AppID: "jupyter", AppType: "web", ComputeJob: true},
		},
		{
			"web type + LocalOnly flag",
			Intent{AppID: "mail", AppType: "web", LocalOnly: true},
		},
		{
			"Web flag + NeedsGPU",
			Intent{AppID: "browser", Web: true, NeedsGPU: true},
		},
		{
			"Web flag + Game + ComputeJob",
			Intent{AppID: "portal", Web: true, Game: true, ComputeJob: true},
		},
	}

	for _, tc := range coercions {
		t.Run(tc.desc, func(t *testing.T) {
			lane := Classify(tc.intent)
			if lane != LaneWebApp {
				t.Fatalf("LANE-SEC-01 REGRESSION: intent %+v classified as %q "+
					"— must be WebApp. A crafted intent with web+GPU/compute flags "+
					"is incorrectly spawning a stream session.", tc.intent, lane)
			}
		})
	}
}

// TestWebApp_URL_CannotBeDowngraded asserts that a non-empty URL always forces
// the WebApp lane, even if every streaming flag is set simultaneously. This is
// the highest-priority rule (Rule 1) and must be tamper-resistant.
// Guards: PENTEST-01 LANE-SEC-04.
func TestWebApp_URL_CannotBeDowngraded(t *testing.T) {
	intent := Intent{
		URL:        "https://app.example.com/",
		NeedsGPU:   true,
		Game:       true,
		ComputeJob: true,
		LocalOnly:  false,
		AppType:    "desktop",
	}
	lane := Classify(intent)
	if lane != LaneWebApp {
		t.Fatalf("LANE-SEC-04 REGRESSION: intent with non-empty URL classified as %q "+
			"(want WebApp). URL rule (priority 1) must override all flags — "+
			"an attacker adding NeedsGPU/Game flags to a URL intent must not "+
			"cause a GPU stream session to open.", lane)
	}
}

// TestWebApp_MIME_HTML_CannotBeDowngraded asserts that a text/html MIME type
// forces WebApp even when NeedsGPU/Game are set.
// Guards: PENTEST-01 LANE-SEC-06.
func TestWebApp_MIME_HTML_CannotBeDowngraded(t *testing.T) {
	cases := []struct {
		mime string
		desc string
	}{
		{"text/html", "exact text/html"},
		{"text/html; charset=utf-8", "text/html with charset"},
	}
	for _, tc := range cases {
		intent := Intent{MIME: tc.mime, NeedsGPU: true, Game: true}
		lane := Classify(intent)
		if lane != LaneWebApp {
			t.Fatalf("LANE-SEC-06 REGRESSION: MIME %q + NeedsGPU+Game classified as %q "+
				"(want WebApp). The MIME rule (priority 2) must fire before GPU flags — "+
				"an HTML file must open in a browser, not a GPU stream.", tc.mime, lane)
		}
	}
}

// ─── LANE-SEC-02: LocalOnly cannot be misrouted ───────────────────────────────

// TestLocalOnly_CannotBeMisrouted asserts that an intent flagged LocalOnly is
// always classified as LocalOnly and never falls through to CPUStream or
// GPURoute, which would allow it to be dispatched to a remote compute peer.
// Guards: PENTEST-01 LANE-SEC-02 — local-only apps must not cross tenant boundary.
func TestLocalOnly_CannotBeMisrouted(t *testing.T) {
	coercions := []struct {
		desc   string
		intent Intent
	}{
		{
			"LocalOnly alone",
			Intent{AppID: "local-tool", LocalOnly: true},
		},
		{
			"LocalOnly + ComputeJob (LocalOnly wins — priority 4 > 5)",
			Intent{AppID: "local-tool", LocalOnly: true, ComputeJob: true},
		},
	}

	for _, tc := range coercions {
		t.Run(tc.desc, func(t *testing.T) {
			lane := Classify(tc.intent)
			if lane != LaneLocalOnly {
				t.Fatalf("LANE-SEC-02 REGRESSION: LocalOnly intent %+v classified as %q "+
					"— must be LocalOnly. This would dispatch a local-only app to a "+
					"remote compute peer, potentially leaking data cross-tenant.", tc.intent, lane)
			}
		})
	}
}

// TestLocalOnly_WithGPUFlags_StaysLocalOnly asserts that adding NeedsGPU or
// Game to a LocalOnly intent cannot escalate it to GPURoute (which routes to
// a peer's GPU). LocalOnly (priority 4) beats GPU flags (priority 6).
// Guards: PENTEST-01 LANE-SEC-02.
func TestLocalOnly_WithGPUFlags_StaysLocalOnly(t *testing.T) {
	intent := Intent{AppID: "local-gpu-tool", LocalOnly: true, NeedsGPU: true}
	lane := Classify(intent)
	if lane != LaneLocalOnly {
		t.Fatalf("LANE-SEC-02 REGRESSION: LocalOnly+NeedsGPU intent classified as %q "+
			"— must be LocalOnly (priority 4 > priority 6). An attacker cannot "+
			"coerce a local-only intent into the GPURoute lane (which would send "+
			"it to a remote peer and leak user data cross-tenant).", lane)
	}
}

// ─── LANE-SEC-03: ComputeJob cannot be coerced into WebApp or CPUStream ───────

// TestComputeJob_CannotBeCoercedToWebApp asserts that a ComputeJob intent is
// never classified as WebApp — a background compute job must not be exposed
// as a browser-side app, which would bypass its sandboxing.
// Guards: PENTEST-01 LANE-SEC-03.
func TestComputeJob_CannotBeCoercedToWebApp(t *testing.T) {
	// AppType "desktop" + ComputeJob should give ComputeWorker, NOT WebApp.
	intent := Intent{AppID: "batch-job", AppType: "desktop", ComputeJob: true}
	lane := Classify(intent)
	if lane != LaneComputeWorker {
		t.Fatalf("LANE-SEC-03 REGRESSION: ComputeJob intent classified as %q "+
			"(want ComputeWorker). A compute job must stay headless and not "+
			"be coerced into an interactive lane.", lane)
	}
}

// TestComputeJob_NotOverriddenByCPUStream asserts that setting ComputeJob on
// a desktop-type app gives ComputeWorker, not CPUStream (which would spin up
// a Xvfb stream session).
// Guards: PENTEST-01 LANE-SEC-03.
func TestComputeJob_NotOverriddenByCPUStream(t *testing.T) {
	intent := Intent{AppType: "desktop", ComputeJob: true}
	lane := Classify(intent)
	if lane == LaneCPUStream {
		t.Fatalf("LANE-SEC-03 REGRESSION: ComputeJob intent classified as CPUStream " +
			"— a headless compute job must not open a Xvfb stream session. " +
			"This wastes resource and cross-contaminates stream sessions.")
	}
	if lane != LaneComputeWorker {
		t.Fatalf("LANE-SEC-03: ComputeJob+desktop should give ComputeWorker, got %q", lane)
	}
}

// ─── LANE-SEC-05: GPU flag cannot escalate a web-native app ───────────────────

// TestGPU_CannotEscalateWebNativeApp asserts that for apps with Web==true,
// NeedsGPU or Game flags are silently ignored (WebApp wins by priority). This
// prevents an attacker from adding GPU flags to a web-native app description
// in a crafted registry entry or API call to force it onto an expensive peer GPU.
// Guards: PENTEST-01 LANE-SEC-05 — cost and resource escalation prevention.
func TestGPU_CannotEscalateWebNativeApp(t *testing.T) {
	apps := []struct {
		id     string
		intent Intent
	}{
		{"office+GPU", Intent{AppID: "office", Web: true, NeedsGPU: true}},
		{"mail+Game", Intent{AppID: "mail", AppType: "web", Game: true}},
		{"notes+GPU+Game", Intent{AppID: "notes", Web: true, NeedsGPU: true, Game: true}},
	}
	for _, tc := range apps {
		t.Run(tc.id, func(t *testing.T) {
			lane := Classify(tc.intent)
			if lane == LaneGPURoute {
				t.Fatalf("LANE-SEC-05 REGRESSION: web-native app %q was escalated to "+
					"GPURoute by adding NeedsGPU/Game flags. Web+GPU flags must keep "+
					"the app in WebApp lane — sending it to a GPU peer wastes "+
					"resources and could expose stream sessions cross-tenant.", tc.id)
			}
		})
	}
}

// ─── LANE-SEC-07: unknown app ID never silently becomes WebApp ────────────────

// TestUnknown_DefaultsToSafeLane asserts that an unknown app ID (no registry
// entry, no flags) defaults to CPUStream — never to WebApp (which would bypass
// stream session creation) or LocalOnly (which would bypass remote routing).
// The conservative default is the safe choice.
// Guards: PENTEST-01 LANE-SEC-07.
func TestUnknown_DefaultsToSafeLane(t *testing.T) {
	intent := Intent{AppID: "unknown-app-xyz-12345"}
	lane := Classify(intent)
	if lane == LaneWebApp {
		t.Fatalf("LANE-SEC-07 REGRESSION: unknown app %q defaulted to WebApp — "+
			"an unrecognised app must not be silently treated as a web-native app, "+
			"which would bypass stream-session creation and stream auth.", intent.AppID)
	}
	if lane == LaneLocalOnly {
		t.Fatalf("LANE-SEC-07 REGRESSION: unknown app %q defaulted to LocalOnly — "+
			"an unrecognised app must not be silently restricted to local-only mode "+
			"(could be used to bypass routing). Default must be CPUStream.", intent.AppID)
	}
	// The safe default is CPUStream.
	if lane != LaneCPUStream {
		t.Fatalf("LANE-SEC-07: unknown app should default to CPUStream, got %q", lane)
	}
}

// TestClassify_ZeroValueIntent asserts that a completely empty intent does not
// panic and returns CPUStream (the conservative safe default).
func TestClassify_ZeroValueIntent(t *testing.T) {
	lane := Classify(Intent{})
	if lane != LaneCPUStream {
		t.Fatalf("zero-value Intent classified as %q — want CPUStream (safe default)", lane)
	}
}
