package main

// activate_apps.go — LAUNCH-01: the on-demand activation implementation.
//
// The gateway holds the seam (services/gateway/activate.go, which documents WHY
// the launch belongs there); this is the half that knows how to start an app,
// because the app store and the launcher live here.
//
// It deliberately mirrors POST /api/apps/launch's resolution rules exactly —
// command, work dir and port come from the VALIDATED manifest and from nothing
// else — so activation can never start anything the admin endpoint would not.
// What it does NOT mirror is the admin gate: the caller here is not an operator
// asking to exec something, it is an authenticated user opening an app the box
// ships, and the gateway has already put that request through session
// validation, entitlement gating and the per-app rate limit before it gets
// here. The kill switch still applies: VULOS_DISABLE_EXEC stops activation the
// same way it stops every other exec path.

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"vulos/backend/services/appnet"
	"vulos/backend/services/gateway"
)

// readyProbeInterval is how often activation re-dials a just-started app.
const readyProbeInterval = 50 * time.Millisecond

// newAppActivator builds the gateway.Activator for this box.
func newAppActivator(
	appStore *appnet.AppStore,
	launcher *appnet.Launcher,
	portPool *appnet.PortPool,
	appGateway *gateway.Gateway,
) gateway.Activator {
	return func(ctx context.Context, appID, userID, profile string) error {
		if execDisabled() {
			return fmt.Errorf("exec disabled by administrator")
		}
		if profile == "" {
			profile = "default"
		}

		// Resolve strictly from the validated manifest — installed dir first,
		// then the bundled dirs the image ships (/opt/vulos/apps). This is the
		// same call the admin launch endpoint makes.
		m, err := appStore.GetManifest(appID)
		if err != nil {
			// Distinguish "the box does not have this app" from "this app's
			// manifest is broken". The shell's registry (src/core/AppRegistry.ts)
			// offers entries that resolve to /app/<id>/ without anything on the box
			// implementing them — `lilmail` is one: it is a separate product, not a
			// bundled app, and there is no lilmail directory in frontend/apps/, no
			// entry in registry.json, and nothing in the rootfs but its logo. That
			// tile used to answer with the same {"error":"app not running"} as an
			// app that merely had not started yet, so a user could not tell a dead
			// entry point from a cold one. Say which it is.
			if os.IsNotExist(err) {
				return fmt.Errorf("%q is offered by the shell but no manifest for it "+
					"exists in the install dir or in the image's bundled apps: %w",
					appID, gateway.ErrNotInstalled)
			}
			return fmt.Errorf("app %s: %w", appID, err)
		}

		// A static type:"web" app is a directory of files, not a server. There is
		// no process to start and there never will be, so say so permanently
		// rather than making the caller wait out a launch timeout.
		if m.Command == "" {
			return fmt.Errorf("app %s is a static web app: %w", appID, gateway.ErrNoProcess)
		}

		appPort := m.Port
		if appPort == 0 {
			appPort = 80
		}

		// Host ports are allocated per INSTANCE, not per app. A namespace is
		// keyed (userID, profile, appID), so keying the port by appID alone —
		// as POST /api/apps/launch still does — hands two different users the
		// same host port and therefore the same 127.0.0.1 DNAT rule.
		instanceKey := userID + "-" + appID
		if profile != "default" {
			instanceKey = userID + "-" + profile + ":" + appID
		}
		hostPort, ok := portPool.Allocate(instanceKey)
		if !ok {
			return fmt.Errorf("app %s: no host ports available", appID)
		}

		appSecret := appGateway.GenerateAppSecret(appID)
		env := []string{
			"VULOS_APP_SECRET=" + appSecret,
			"VULOS_API=http://localhost:8080",
		}

		app, err := launcher.LaunchManifest(ctx, m, userID, profile, hostPort, appPort, nil, env)
		if err != nil {
			portPool.Release(instanceKey)
			appGateway.RemoveAppSecret(appID)
			return fmt.Errorf("launch %s: %w", appID, err)
		}

		// Wait until the app is actually reachable before telling the gateway to
		// proxy to it.
		//
		// LaunchManifest returns as soon as fork/exec SUCCEEDS, which says
		// nothing about whether the server inside has bound its port — a python
		// app takes ~0.2s to get there. Returning at exec time would hand the
		// triggering request straight to a closed port, which is the same race a
		// fire-and-forget launch from the shell would have lost. This is the
		// whole reason activation is worth doing inside the request.
		if app.Namespace == nil {
			return fmt.Errorf("launch %s: no namespace", appID)
		}
		addr := net.JoinHostPort(app.Namespace.NSIP, fmt.Sprintf("%d", app.Namespace.AppPort))
		if err := waitReachable(ctx, addr); err != nil {
			return fmt.Errorf("app %s started but never served %s: %w", appID, addr, err)
		}
		return nil
	}
}

// waitReachable dials addr until it accepts a connection or ctx expires.
//
// It also reports the ctx error rather than swallowing it, so an app that
// crashes on startup (or one whose port cannot be bound) surfaces as a real
// failure the gateway can explain, not as a silent success followed by a 502.
func waitReachable(ctx context.Context, addr string) error {
	var lastErr error
	for {
		d := net.Dialer{Timeout: time.Second}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			conn.Close()
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("%v (last dial: %v)", ctx.Err(), lastErr)
			}
			return ctx.Err()
		case <-time.After(readyProbeInterval):
		}
	}
}
