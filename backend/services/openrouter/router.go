// Package openrouter classifies an app-launch intent into one of five
// execution lanes. The classifier is the canonical source of truth for
// "which lane does this app run in?" — shell, backend route, and tests all
// call Classify(intent) rather than re-implementing the rules.
//
// Lanes
//
//	WebApp        — content opens in the host browser (zero stream.Session)
//	CPUStream     — native GUI app streamed from CPU (Xvfb + GStreamer)
//	GPURoute      — native GPU-accelerated app or game (peer BYO GPU preferred)
//	ComputeWorker — background headless compute job
//	LocalOnly     — runs entirely on the local device, no remote/stream path
package openrouter

// Lane is one of the five execution lanes every app launch routes into.
type Lane string

const (
	LaneWebApp        Lane = "WebApp"
	LaneCPUStream     Lane = "CPUStream"
	LaneGPURoute      Lane = "GPURoute"
	LaneComputeWorker Lane = "ComputeWorker"
	LaneLocalOnly     Lane = "LocalOnly"
)

// Intent carries the information the classifier uses to decide the lane.
// Callers populate only the fields they know; zero values are safe.
type Intent struct {
	// AppID is the registry key, e.g. "blender", "browser", "mail".
	AppID string

	// URL is non-empty when the intent is to open a specific URL.
	// Any non-empty URL always classifies as WebApp.
	URL string

	// MIME is the MIME type of a file to be opened (e.g. "text/html").
	// A MIME type starting with "text/html" classifies as WebApp.
	MIME string

	// AppType is the registry "type" field: "web", "desktop", "service".
	AppType string

	// Flags are mutually exclusive lane signals loaded from registry entries.
	// The first matching flag wins (priority order matches the rules below).
	Web        bool // registry flag: web-native app → WebApp
	NeedsGPU   bool // registry flag: GPU-accelerated app → GPURoute
	Game       bool // registry flag: game / Windows compat layer → GPURoute
	LocalOnly  bool // registry flag: bare-metal local only → LocalOnly
	ComputeJob bool // registry flag: headless background job → ComputeWorker
}

// Classify returns the lane for the given intent.
//
// Rules (evaluated in priority order):
//  1. Non-empty URL → WebApp
//  2. MIME type "text/html*" → WebApp
//  3. AppType == "web" OR Web flag → WebApp
//  4. LocalOnly flag → LocalOnly
//  5. ComputeJob flag → ComputeWorker
//  6. NeedsGPU OR Game flag → GPURoute
//  7. Anything else (native desktop app with no special flags) → CPUStream
func Classify(intent Intent) Lane {
	// Rule 1 — explicit URL
	if intent.URL != "" {
		return LaneWebApp
	}

	// Rule 2 — HTML MIME type
	if len(intent.MIME) >= 9 && intent.MIME[:9] == "text/html" {
		return LaneWebApp
	}

	// Rule 3 — web app by registry type or flag
	if intent.AppType == "web" || intent.Web {
		return LaneWebApp
	}

	// Rule 4 — local-only
	if intent.LocalOnly {
		return LaneLocalOnly
	}

	// Rule 5 — background compute job
	if intent.ComputeJob {
		return LaneComputeWorker
	}

	// Rule 6 — GPU-accelerated or game
	if intent.NeedsGPU || intent.Game {
		return LaneGPURoute
	}

	// Rule 7 — all other native GUI apps stream over CPU
	return LaneCPUStream
}
