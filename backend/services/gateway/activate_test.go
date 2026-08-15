package gateway

// activate_test.go — LAUNCH-01 gate.
//
// The defect these pin: NOTHING on the box ever started a bundled app, so
// Handler's namespace lookup always missed and every app open ended at
// {"error":"app not running"}. The tests below fail if the gateway goes back to
// treating a miss as terminal, if the launch is not single-flighted, or if a
// failed launch is reported as anything other than a failure.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vulos/backend/services/appnet"
)

// TestNamespaceMiss_ActivatesTheApp is the founder's bug, in one test.
//
// A request arrives for an app that is installed but not running. Before
// LAUNCH-01 the only possible answer was 404 "app not running". Now the gateway
// must run the activator and then serve the app it brought up.
func TestNamespaceMiss_ActivatesTheApp(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	g := New(store, mgr, pool)
	token, userID := seedSession(t, store)

	var calls int32
	g.SetActivator(func(ctx context.Context, appID, uid, profile string) error {
		atomic.AddInt32(&calls, 1)
		if appID != "calculator" {
			t.Errorf("activator got appID %q, want calculator", appID)
		}
		if uid != userID {
			t.Errorf("activator got userID %q, want %q", uid, userID)
		}
		if profile != "default" {
			t.Errorf("activator got profile %q, want default", profile)
		}
		// Stand in for a real launch: the app is now reachable.
		startFakeApp(t, mgr, "calculator", userID)
		return nil
	})

	srv := httptest.NewServer(g.Handler())
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/app/calculator/", nil)
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: token})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("activator called %d times, want 1 — the gateway answered a "+
			"namespace miss WITHOUT trying to start the app, which is exactly the "+
			`bug: opening Calculator returns {"error":"app not running"}`, calls)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status %d, want 200 — the app was activated but the request was "+
			"not served through to it", resp.StatusCode)
	}
}

// TestNoActivator_KeepsThe404 pins the fallback: a box with no activator wired
// behaves exactly as it did before LAUNCH-01. This is what makes the seam safe
// to leave nil in tests and in any embedder that does not launch processes.
func TestNoActivator_KeepsThe404(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	g := New(store, mgr, pool)
	token, _ := seedSession(t, store)
	_ = mgr

	srv := httptest.NewServer(g.Handler())
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/app/calculator/", nil)
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: token})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status %d, want 404 with no activator installed", resp.StatusCode)
	}
}

// TestFailedActivation_IsReportedAsAFailure. A launch that does not come up
// must not be laundered into a 200 or into the old flat 404 — the caller has to
// be able to tell "this app is broken" from "this app does not exist".
func TestFailedActivation_IsReportedAsAFailure(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	g := New(store, mgr, pool)
	token, _ := seedSession(t, store)
	_ = mgr

	g.SetActivator(func(ctx context.Context, appID, uid, profile string) error {
		return fmt.Errorf("python3: no such file or directory")
	})

	srv := httptest.NewServer(g.Handler())
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/app/calculator/", nil)
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: token})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status %d, want 504 for a launch that failed", resp.StatusCode)
	}
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	if !contains(body, "no such file") {
		t.Fatalf("body %q does not carry the reason the launch failed — an "+
			"operator cannot debug a box that only says the app is not running", body)
	}
}

// TestStaticApp_IsAPermanent404. A static type:"web" app has no process, ever.
// Answering it with the 504 a real launch failure gets would tell the caller to
// retry something that can never succeed.
func TestStaticApp_IsAPermanent404(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	g := New(store, mgr, pool)
	token, _ := seedSession(t, store)
	_ = mgr

	g.SetActivator(func(ctx context.Context, appID, uid, profile string) error {
		return fmt.Errorf("app %s is a static web app: %w", appID, ErrNoProcess)
	})

	srv := httptest.NewServer(g.Handler())
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/app/site-template/", nil)
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: token})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status %d, want 404 for an app with no process to start", resp.StatusCode)
	}
}

// TestUninstalledApp_SaysSo. The shell's registry offers entries that nothing
// on the box implements — Mail (`lilmail`) is one on a stock image. Answering
// those with the same "app not running" as a cold Calculator tells the user to
// wait for something that is never coming.
func TestUninstalledApp_SaysSo(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	g := New(store, mgr, pool)
	token, _ := seedSession(t, store)
	_ = mgr

	g.SetActivator(func(ctx context.Context, appID, uid, profile string) error {
		return fmt.Errorf("%q is offered by the shell but no manifest for it exists: %w",
			appID, ErrNotInstalled)
	})

	srv := httptest.NewServer(g.Handler())
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/app/lilmail/", nil)
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: token})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("status %d, want 404 for an app the box does not have", resp.StatusCode)
	}
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	if !contains(body, "not installed") {
		t.Fatalf("body %q does not say the app is not installed — it is "+
			"indistinguishable from an app that simply has not started yet", body)
	}
}

// TestConcurrentRequests_LaunchOnce is the thundering-herd guard.
//
// Loading an app page fires the document plus every subresource at once, and
// they ALL miss the namespace together. Without single-flighting, each one
// allocates a host port and races to create the same namespace.
func TestConcurrentRequests_LaunchOnce(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	g := New(store, mgr, pool)
	token, userID := seedSession(t, store)

	var calls int32
	release := make(chan struct{})
	g.SetActivator(func(ctx context.Context, appID, uid, profile string) error {
		atomic.AddInt32(&calls, 1)
		<-release // hold the launch open so every caller piles up behind it
		startFakeApp(t, mgr, "calculator", userID)
		return nil
	})

	srv := httptest.NewServer(g.Handler())
	defer srv.Close()

	const n = 12
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, _ := http.NewRequest("GET", srv.URL+"/app/calculator/", nil)
			req.AddCookie(&http.Cookie{Name: "vulos_session", Value: token})
			resp, err := srv.Client().Do(req)
			if err != nil {
				codes[i] = -1
				return
			}
			resp.Body.Close()
			codes[i] = resp.StatusCode
		}(i)
	}
	// Give every request time to reach the activate() call before releasing.
	time.Sleep(150 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("activator called %d times for %d concurrent requests, want 1 — "+
			"a page load would launch the app once per subresource", got, n)
	}
	for i, c := range codes {
		if c != 200 {
			t.Fatalf("request %d got %d, want 200 — a waiter did not get the "+
				"app the leader started", i, c)
		}
	}
}

// TestDifferentUsers_LaunchSeparately. Namespaces are keyed (user, profile,
// app), so collapsing two users' activations onto one launch would leave the
// second user with no namespace of their own and a 404 they can never clear.
//
// The two requests MUST overlap in time. An earlier version of this test ran
// them one after the other and survived a mutation that keyed the single-flight
// on appID alone — the first activation had already finished and cleared its
// key before the second began, so the collapse never happened. The activator
// here blocks until both requests are inside it.
func TestDifferentUsers_LaunchSeparately(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	g := New(store, mgr, pool)

	uA, err := store.Register("alice", "alicepass123456", "Alice")
	if err != nil {
		t.Fatalf("Register alice: %v", err)
	}
	uB, err := store.Register("bob", "bobpass1234567", "Bob")
	if err != nil {
		t.Fatalf("Register bob: %v", err)
	}
	tokA := store.CreateSession(uA, "dev-a").Token
	tokB := store.CreateSession(uB, "dev-b").Token

	var mu sync.Mutex
	seen := map[string]int{}
	release := make(chan struct{})
	g.SetActivator(func(ctx context.Context, appID, uid, profile string) error {
		mu.Lock()
		seen[uid]++
		mu.Unlock()
		<-release // hold both activations open at once
		startFakeApp(t, mgr, "calculator", uid)
		return nil
	})

	srv := httptest.NewServer(g.Handler())
	defer srv.Close()

	toks := []string{tokA, tokB}
	codes := make([]int, len(toks))
	var wg sync.WaitGroup
	for i, tok := range toks {
		wg.Add(1)
		go func(i int, tok string) {
			defer wg.Done()
			req, _ := http.NewRequest("GET", srv.URL+"/app/calculator/", nil)
			req.AddCookie(&http.Cookie{Name: "vulos_session", Value: tok})
			resp, err := srv.Client().Do(req)
			if err != nil {
				codes[i] = -1
				return
			}
			resp.Body.Close()
			codes[i] = resp.StatusCode
		}(i, tok)
	}
	time.Sleep(150 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if seen[uA.ID] != 1 || seen[uB.ID] != 1 {
		t.Fatalf("per-user launches = %v, want one each for %s and %s — "+
			"activation is not scoped to the user the namespace is scoped to, so "+
			"one user's launch was made to stand in for another's",
			seen, uA.ID, uB.ID)
	}
	for i, c := range codes {
		if c != 200 {
			t.Fatalf("user %d got %d, want 200 — their request was collapsed onto "+
				"another user's launch and found no namespace of its own", i, c)
		}
	}
}

// TestRunningApp_IsNotRelaunched. Activation must only fire on a MISS: calling
// it on every request would fork a launch attempt per request on the hot path.
func TestRunningApp_IsNotRelaunched(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	g := New(store, mgr, pool)
	token, userID := seedSession(t, store)
	startFakeApp(t, mgr, "calculator", userID)

	var calls int32
	g.SetActivator(func(ctx context.Context, appID, uid, profile string) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})

	srv := httptest.NewServer(g.Handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/app/calculator/", nil)
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: token})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("activator ran %d times for an already-running app, want 0", calls)
	}
}

// TestPublicHandler_DoesNotActivate. The anonymous public-web entrypoint must
// never be able to spawn a process: that would let an unauthenticated visitor
// fork work on the box.
func TestPublicHandler_DoesNotActivate(t *testing.T) {
	store, mgr, pool := newTestDeps(t)
	g := New(store, mgr, pool)
	g.SetPublicVisibility(func(appID string) bool { return true })
	_ = mgr

	var calls int32
	g.SetActivator(func(ctx context.Context, appID, uid, profile string) error {
		atomic.AddInt32(&calls, 1)
		return nil
	})

	srv := httptest.NewServer(g.PublicHandler())
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/app/calculator/")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("an ANONYMOUS public request started %d launches, want 0", calls)
	}
}

// contains is a tiny helper so the test file does not need strings just for one
// substring check inside a failure message.
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

// keep the appnet import honest — the fake-app helper returns its Namespace type.
var _ = (*appnet.Namespace)(nil)
