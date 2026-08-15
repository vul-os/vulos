package gateway

// activate.go — LAUNCH-01: on-demand app activation.
//
// ── The gap this closes ───────────────────────────────────────────────────────
//
// Nothing on the box ever started a bundled app.
//
//   - Every process-backed app ships `"auto_start": false`, and nothing anywhere
//     reads AutoStart to start anything at boot either way.
//   - The shell's WebApp lane (frontend/src/shell/launchApp.ts) opens the window
//     and returns; it only calls POST /api/apps/launch on the `else` branch, for
//     an app with NO url. Every bundled app declares `url: '/app/<id>/'` in
//     src/core/AppRegistry.ts, so that branch is dead for all of them.
//   - appnet.Manager.GetForProfile is a pure map lookup. It never launches.
//
// So opening Calculator resolved no namespace and Handler answered
// {"error":"app not running"} — the founder's bug, verbatim.
//
// ── Why the fix lives HERE and not in the shell ───────────────────────────────
//
// The three candidate places were: the shell calls launch before opening the
// window; the gateway starts the app on first request; or something starts
// auto_start:false apps on demand elsewhere. This is the gateway option, for
// four reasons that are properties of the code, not preferences:
//
//  1. POST /api/apps/launch is ADMIN-ONLY (main.go: `p.Role != auth.RoleAdmin`
//     → 403). A shell-side call would 403 for every non-admin user on the box.
//     Relaxing that gate to make the shell lane work would widen a privileged
//     exec endpoint for every caller, not just app opens.
//  2. The shell is not the only caller. A bookmark, a restored window, a PWA
//     entry, a deep link, the service worker, and every SUBRESOURCE of the app
//     document all arrive at Handler without going through launchApp.ts. A
//     shell-side launch fixes the first open and nothing else.
//  3. It cannot race the window. A shell-side launch is a fire-and-forget fetch
//     that runs CONCURRENTLY with the iframe navigation, so the first request
//     usually loses the race and still gets "app not running". Activating inside
//     the request means the request that needed the app is the one that waits
//     for it.
//  4. An app that dies is recovered for free: the next request finds no
//     namespace (the launcher tears one down on process exit) and activates
//     again. A shell-side launch only ever runs on an explicit user click.
//
// ── The failure modes it has to answer for ────────────────────────────────────
//
//   - Slow first open: bounded. The wait has a hard deadline (activateTimeout);
//     past it the caller gets 504 with a reason, never an indefinite hang.
//   - Thundering herd: a page load fires the document plus every subresource at
//     once, all missing the namespace together. Activation is single-flighted per
//     (app, user, profile) — the first request launches, the rest wait on the
//     same result. Without this, N concurrent requests would each allocate a
//     port and each try to create the same namespace.
//   - Anonymous activation: NOT wired. PublicHandler (public.go) keeps the plain
//     lookup, so an unauthenticated public-web request can never spawn a process.
//     Activation is reachable only after Handler's session check has passed.
//
// The gateway does not know how to launch anything — it holds a seam. main.go
// supplies the implementation (manifest resolution, port, app secret,
// LaunchManifest, readiness), which is where the app store and launcher live.

import (
	"context"
	"errors"
	"time"
)

// ErrNoProcess is what an Activator returns for an app that is installed and
// valid but has no process to start — a static `type: "web"` app, which is a
// directory of files, not a server. It is a PERMANENT condition, so the gateway
// answers it with a 404 and an explanation instead of the 504 it gives a launch
// that genuinely failed or timed out. Retrying would never help.
var ErrNoProcess = errors.New("app has no process to start")

// ErrNotInstalled is what an Activator returns for an app that the shell offers
// but the box does not have — no manifest in the install dir, none in the
// bundled dirs the image ships.
//
// This is a different fact from "app not running" and the old answer conflated
// them, which is how a Mail tile that resolves to nothing on a stock image
// looked identical to a Calculator that simply had not been started yet. Like
// ErrNoProcess it is permanent (a 404), but the explanation has to name the
// actual problem: nothing will start, because there is nothing to start.
var ErrNotInstalled = errors.New("app is not installed on this box")

// activateTimeout bounds how long a request will wait for an app to come up.
// A python process-backed app measured ~0.2s to bind and serve
// (scripts/check-apps-run.py); the namespace setup around it is a dozen
// iproute2/iptables execs. 20s leaves generous headroom on a loaded box while
// still guaranteeing the request terminates.
const activateTimeout = 20 * time.Second

// Activator brings an app up for a specific (appID, userID, profile) and
// returns only once it is reachable, or an error explaining why it is not.
// It must be safe to call concurrently and idempotent for an already-running
// app.
type Activator func(ctx context.Context, appID, userID, profile string) error

// SetActivator installs the on-demand activation implementation. When nil (the
// default), Handler keeps the pre-LAUNCH-01 behaviour exactly: a namespace miss
// is a 404 and nothing is started.
func (g *Gateway) SetActivator(fn Activator) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.activator = fn
}

// inFlight is one activation other requests can wait on.
type inFlight struct {
	done chan struct{}
	err  error
}

// activate runs the installed Activator for one app instance, collapsing
// concurrent callers for the same instance onto a single launch.
//
// Returns (false, nil) when no activator is installed, so the caller can keep
// the original 404 behaviour verbatim.
func (g *Gateway) activate(ctx context.Context, appID, userID, profile string) (attempted bool, err error) {
	g.mu.RLock()
	fn := g.activator
	g.mu.RUnlock()
	if fn == nil {
		return false, nil
	}

	if profile == "" {
		profile = "default"
	}
	// Key on all three dimensions the namespace itself is keyed on. Collapsing
	// on appID alone would make one user's launch satisfy another user's
	// request, and that request would then find no namespace of its own.
	key := userID + "\x00" + profile + "\x00" + appID

	g.activeMu.Lock()
	if g.activeFlights == nil {
		g.activeFlights = make(map[string]*inFlight)
	}
	if f, ok := g.activeFlights[key]; ok {
		g.activeMu.Unlock()
		// Wait for the in-flight launch — but never longer than this request's
		// own context allows.
		select {
		case <-f.done:
			return true, f.err
		case <-ctx.Done():
			return true, ctx.Err()
		}
	}
	f := &inFlight{done: make(chan struct{})}
	g.activeFlights[key] = f
	g.activeMu.Unlock()

	// Deliberately NOT derived from the triggering request's context: a client
	// that navigates away mid-launch must not abort a launch that other waiters
	// (and the next request) depend on.
	launchCtx, cancel := context.WithTimeout(context.Background(), activateTimeout)
	defer cancel()

	f.err = fn(launchCtx, appID, userID, profile)

	g.activeMu.Lock()
	delete(g.activeFlights, key)
	g.activeMu.Unlock()
	close(f.done)

	return true, f.err
}
