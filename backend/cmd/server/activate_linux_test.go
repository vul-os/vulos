//go:build linux

package main

// activate_linux_test.go — the whole launch path, end to end, for real.
//
// Everything else about LAUNCH-01 is testable on any platform with a fake
// upstream. This is the part that is not: it creates a real network namespace
// with iproute2/iptables, forks a real python process into it as nobody, and
// serves a real HTTP request through the gateway to it. `ip netns` is
// Linux-only and needs root, so this cannot run on a developer Mac at all.
//
// Run it with scripts/prove-launch.sh, which supplies a privileged Linux
// container. It is opt-in via VULOS_E2E_LAUNCH=1 rather than silently skipping,
// so a run that was meant to prove something cannot quietly prove nothing.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/services/appnet"
	"vulos/backend/services/auth"
	"vulos/backend/services/gateway"
)

func requireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("VULOS_E2E_LAUNCH") != "1" {
		t.Skip("set VULOS_E2E_LAUNCH=1 (and run as root on Linux) — see scripts/prove-launch.sh")
	}
	if os.Geteuid() != 0 {
		t.Fatal("VULOS_E2E_LAUNCH=1 but not root: creating a network namespace needs " +
			"CAP_SYS_ADMIN. Refusing to report a pass for a test that cannot do its work.")
	}
	for _, bin := range []string{"ip", "iptables", "setpriv", "sysctl", "python3"} {
		if _, err := os.Stat("/usr/sbin/" + bin); err == nil {
			continue
		}
		found := false
		for _, d := range strings.Split(os.Getenv("PATH"), ":") {
			if _, err := os.Stat(filepath.Join(d, bin)); err == nil {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s is not installed — the launch path cannot run without it", bin)
		}
	}
}

// bundledAppsDir locates frontend/apps in the checkout this test is running
// from. Those are the exact directories build.sh copies into /opt/vulos/apps.
func bundledAppsDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd() // backend/cmd/server
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := filepath.Join(wd, "..", "..", "..", "frontend", "apps")
	if _, err := os.Stat(filepath.Join(dir, "calculator", "app.json")); err != nil {
		t.Fatalf("bundled apps not found at %s: %v", dir, err)
	}
	return dir
}

type e2eRig struct {
	srv      *httptest.Server
	token    string
	userID   string
	netMgr   *appnet.Manager
	launcher *appnet.Launcher
}

func newE2ERig(t *testing.T) *e2eRig {
	t.Helper()
	t.Setenv("VULOS_BUNDLED_APPS", bundledAppsDir(t))

	ctx := context.Background()
	netMgr := appnet.NewManager()
	if err := netMgr.Init(ctx); err != nil {
		t.Fatalf("appnet init: %v", err)
	}
	portPool := appnet.NewPortPool(7070, 7999)
	launcher := appnet.NewLauncher(netMgr)
	t.Cleanup(func() {
		launcher.StopAll(context.Background())
		netMgr.DestroyAll(context.Background())
	})

	appStore := appnet.NewAppStore(filepath.Join(t.TempDir(), "apps"))
	authStore, err := auth.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("auth store: %v", err)
	}
	u, err := authStore.Register("e2euser", "e2epassword123", "E2E User")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	sess := authStore.CreateSession(u, "e2e")

	g := gateway.New(authStore, netMgr, portPool)
	g.SetActivator(newAppActivator(appStore, launcher, portPool, g))

	srv := httptest.NewServer(g.Handler())
	t.Cleanup(srv.Close)
	return &e2eRig{srv: srv, token: sess.Token, userID: u.ID, netMgr: netMgr, launcher: launcher}
}

func (r *e2eRig) get(t *testing.T, path string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", r.srv.URL+path, nil)
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: r.token})
	resp, err := r.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(body)
}

// TestLaunchEndToEnd_ColdOpenServesTheApp is the founder's bug, end to end and
// with nothing mocked: a cold box, an app that has never run, one request.
//
// Before LAUNCH-01 this request could only ever return
// {"error":"app not running"}, and before PORTBIND-01 the process it started
// would have died on bind() before it could answer.
func TestLaunchEndToEnd_ColdOpenServesTheApp(t *testing.T) {
	requireE2E(t)
	rig := newE2ERig(t)

	if _, ok := rig.netMgr.GetForProfile("calculator", rig.userID, "default"); ok {
		t.Fatal("a namespace already exists on a cold box — the test is not starting cold")
	}

	start := time.Now()
	code, body := rig.get(t, "/app/calculator/")
	elapsed := time.Since(start)

	if code != 200 {
		t.Fatalf("cold open of calculator returned %d: %s", code, body)
	}
	if !strings.Contains(strings.ToLower(body), "<html") {
		t.Fatalf("calculator answered 200 with something that is not its page (%d bytes): %.200s",
			len(body), body)
	}
	if _, ok := rig.netMgr.GetForProfile("calculator", rig.userID, "default"); !ok {
		t.Fatal("the request was served but no namespace was registered for it")
	}
	t.Logf("cold open served in %s, %d bytes", elapsed.Round(time.Millisecond), len(body))
}

// TestLaunchEndToEnd_EveryBundledAppOpens walks all 15 process-backed apps.
// A fix that only works for calculator is not a fix.
func TestLaunchEndToEnd_EveryBundledAppOpens(t *testing.T) {
	requireE2E(t)
	rig := newE2ERig(t)

	dir := bundledAppsDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	type result struct {
		id     string
		code   int
		detail string
	}
	var checked, served int
	var failures []result
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "_") {
			continue
		}
		m, err := appnet.LoadAndValidateManifest(filepath.Join(dir, e.Name(), "app.json"))
		if err != nil {
			failures = append(failures, result{e.Name(), 0, "invalid manifest: " + err.Error()})
			continue
		}
		if m.Command == "" {
			// A static type:"web" app has no process. It is covered by its own
			// expectation below, not this walk.
			continue
		}
		checked++
		t0 := time.Now()
		code, body := rig.get(t, "/app/"+e.Name()+"/")
		took := time.Since(t0).Round(time.Millisecond)
		if code == 200 && strings.Contains(strings.ToLower(body), "<html") {
			served++
			// Per-app evidence, so the findings doc can cite a line per app
			// rather than one aggregate number.
			ns, _ := rig.netMgr.GetForProfile(e.Name(), rig.userID, "default")
			nsip := "?"
			if ns != nil {
				nsip = fmt.Sprintf("%s:%d", ns.NSIP, ns.AppPort)
			}
			t.Logf("APP %-16s port=%-4d cold-open=%-7s bytes=%-6d ns=%s",
				e.Name(), m.Port, took, len(body), nsip)
			continue
		}
		failures = append(failures, result{e.Name(), code, firstLine(body)})
	}

	// COVERAGE ASSERTION — a walk that examined nothing must never pass.
	if checked < 15 {
		t.Fatalf("only %d process-backed apps were exercised; %s ships 15. A gate that "+
			"checks fewer apps than exist is not a gate.", checked, dir)
	}
	if len(failures) > 0 {
		for _, f := range failures {
			t.Errorf("app %-16s status=%-3d %s", f.id, f.code, f.detail)
		}
		t.Fatalf("%d of %d bundled apps did not launch and serve", len(failures), checked)
	}
	t.Logf("%d/%d bundled process apps launched on demand and served their page", served, checked)
}

// TestLaunchEndToEnd_StaticAppSaysWhy. site-template is a static type:"web" app
// with no command. There is no process to start and the gateway has no static
// lane, so it cannot be served — but it must say so, not claim it is "not
// running" as though it might start later.
func TestLaunchEndToEnd_StaticAppSaysWhy(t *testing.T) {
	requireE2E(t)
	rig := newE2ERig(t)

	code, body := rig.get(t, "/app/site-template/")
	if code != 404 {
		t.Fatalf("static app returned %d, want 404: %s", code, body)
	}
	if !strings.Contains(body, "no process") {
		t.Fatalf("static app 404 does not explain itself: %s", body)
	}
}

// TestLaunchEndToEnd_MailIsHonest. The shell registers Mail as `lilmail` at
// /app/lilmail/ (src/core/AppRegistry.ts:262) and the ⌘K palette opens the same
// path (CommandPalette.tsx:391). Nothing on a stock image implements it: no
// frontend/apps/lilmail, no entry in registry.json, nothing in the rootfs but
// product-logos/lilmail.svg. lilmail is a separate product the operator runs
// themselves (see routes_mail.go's VULOS_MAIL_URL, default localhost:3000 —
// which, separately, nothing in the shell reads).
//
// That tile cannot be made to work by this box, so it must at least be honest
// about why.
func TestLaunchEndToEnd_MailIsHonest(t *testing.T) {
	requireE2E(t)
	rig := newE2ERig(t)

	code, body := rig.get(t, "/app/lilmail/")
	if code != 404 {
		t.Fatalf("Mail returned %d, want 404: %s", code, body)
	}
	if !strings.Contains(body, "not installed") {
		t.Fatalf("Mail's failure does not say the app is not installed, so it reads "+
			"identically to an app that has simply not started yet: %s", body)
	}
}

// TestLaunchEndToEnd_DeadAppIsRecovered. Before LAUNCH-01 a process that exited
// on its own left its namespace registered forever: GetForProfile kept
// answering with it, so the gateway proxied to a dead 10.200.x.2 and every
// later request 502'd, with nothing anywhere to start the app again.
func TestLaunchEndToEnd_DeadAppIsRecovered(t *testing.T) {
	requireE2E(t)
	rig := newE2ERig(t)

	if code, body := rig.get(t, "/app/clock/"); code != 200 {
		t.Fatalf("first open returned %d: %s", code, body)
	}

	// Kill it the way a crash would, not via Stop() — Stop is the path that
	// already tore the namespace down.
	var killed bool
	for _, app := range rig.launcher.ListRunning() {
		if strings.HasSuffix(app.ID, "-clock") && app.Process != nil {
			if err := app.Process.Kill(); err != nil {
				t.Fatalf("kill: %v", err)
			}
			killed = true
		}
	}
	if !killed {
		t.Fatal("clock was serving but no running process was found to kill")
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := rig.netMgr.GetForProfile("clock", rig.userID, "default"); !ok {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, ok := rig.netMgr.GetForProfile("clock", rig.userID, "default"); ok {
		t.Fatal("the process is dead but its namespace is still registered — the " +
			"gateway will proxy to nothing and 502 forever, and nothing will ever " +
			"restart the app")
	}

	code, body := rig.get(t, "/app/clock/")
	if code != 200 {
		t.Fatalf("re-open after a crash returned %d: %s", code, body)
	}
	if !strings.Contains(strings.ToLower(body), "<html") {
		t.Fatalf("re-open answered 200 with something that is not the app page: %.200s", body)
	}
}

// TestLaunchEndToEnd_AppBindsItsDeclaredPort proves PORTBIND-01 through the
// real launcher rather than through a hand-built probe: the manifest says 80,
// so the namespace must have the app listening on 80.
func TestLaunchEndToEnd_AppBindsItsDeclaredPort(t *testing.T) {
	requireE2E(t)
	rig := newE2ERig(t)

	if code, body := rig.get(t, "/app/notes/"); code != 200 {
		t.Fatalf("notes returned %d: %s", code, body)
	}
	ns, ok := rig.netMgr.GetForProfile("notes", rig.userID, "default")
	if !ok {
		t.Fatal("no namespace after a successful open")
	}
	if ns.AppPort != 80 {
		t.Fatalf("app listening on %d, but its manifest declares 80", ns.AppPort)
	}
	t.Logf("notes bound its declared privileged port 80 as nobody in %s → %s:%d",
		ns.Name, ns.NSIP, ns.AppPort)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160]
	}
	return fmt.Sprintf("%q", s)
}
