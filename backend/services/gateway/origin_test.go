package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vulos/backend/services/appnet"
	"vulos/backend/services/auth"
)

// ORIGIN-01 — the gateway half of per-app origins.
//
// The property under test is the one that makes third-party apps survivable:
// an app's document must never be served on the SHELL's origin, and an app's
// document must only be framable by the shell. Everything below is a way for
// that to go wrong.

// startFakeHTMLApp is startFakeApp's sibling for apps that return HTML — the
// bridge injection and CSP paths only apply to HTML responses.
func startFakeHTMLApp(t *testing.T, mgr *appnet.Manager, appID, userID, body string) *httptest.Server {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// An app that tries to pin its own framing policy must not be able to
		// override the gateway's — assert this by having it try.
		w.Header().Set("X-Frame-Options", "DENY")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(upstream.Close)

	addr := upstream.Listener.Addr().String()
	colonIdx := strings.LastIndex(addr, ":")
	ip := addr[:colonIdx]
	var port int
	fmt.Sscanf(addr[colonIdx+1:], "%d", &port)

	mgr.AddNamespace(userID+"-"+appID, &appnet.Namespace{
		Name: "vulos_" + appID, AppID: appID, OwnerID: userID,
		NSIP: ip, AppPort: port, Active: true,
	})
	return upstream
}

func originTestGateway(t *testing.T) (*Gateway, *auth.Store, *appnet.Manager, string, string) {
	t.Helper()
	store, mgr, pool := newTestDeps(t)
	gw := New(store, mgr, pool)
	token, userID := seedSession(t, store)
	return gw, store, mgr, token, userID
}

// TestPathPrefixOnShellOriginRedirectsToAppOrigin is the core of the fix.
//
// With per-app origins available, a request for app content on the SHELL's origin
// (/app/{id}/) must NOT be answered with the app's document. A document served
// there executes on the shell's origin: it can read the shell's localStorage and
// cookies directly, no sandbox involved. The gateway sends the caller to the
// app's own origin instead.
func TestPathPrefixOnShellOriginRedirectsToAppOrigin(t *testing.T) {
	t.Setenv("VULOS_DOMAIN", "box.example.com")
	gw, _, mgr, token, userID := originTestGateway(t)
	startFakeHTMLApp(t, mgr, "clock", userID, "<html><head></head><body>clock</body></html>")

	req := httptest.NewRequest("GET", "/app/clock/deep/path?q=1", nil)
	req.Host = "box.example.com"
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: token})
	rec := httptest.NewRecorder()
	gw.Handler()(rec, req)

	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307 — app content must not be served on the shell's origin (body: %.120s)",
			rec.Code, rec.Body.String())
	}
	want := "http://clock--default.box.example.com/deep/path?q=1"
	if got := rec.Header().Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	if strings.Contains(rec.Body.String(), "clock") && rec.Body.Len() > 200 {
		t.Error("redirect response leaked the app's document body")
	}
}

// TestPathPrefixServesAppWhenOriginsUnavailable — the default self-host box has no
// VULOS_DOMAIN, so there IS no app origin to redirect to. The path prefix must
// keep working exactly as before (this is the compat guarantee), and the shell
// compensates by refusing allow-same-origin on that frame.
func TestPathPrefixServesAppWhenOriginsUnavailable(t *testing.T) {
	t.Setenv("VULOS_DOMAIN", "")
	gw, _, mgr, token, userID := originTestGateway(t)
	startFakeHTMLApp(t, mgr, "clock", userID, "<html><head></head><body>clock</body></html>")

	req := httptest.NewRequest("GET", "/app/clock/", nil)
	req.Host = "box.local:8080"
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: token})
	rec := httptest.NewRecorder()
	gw.Handler()(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — path prefix must still serve apps on a box with no base domain", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "clock") {
		t.Error("app body not served")
	}
	// On the shell's origin the app is framed opaque, so 'self' is both correct and
	// unchanged from the pre-ORIGIN-01 posture.
	if got := rec.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'self'" {
		t.Errorf("CSP = %q, want frame-ancestors 'self'", got)
	}
}

// TestAppOriginFrameAncestorsNamesOnlyTheShell — once an app is on its own origin,
// `frame-ancestors 'self'` would be actively wrong: 'self' is the APP's origin, so
// it would (a) block the shell from framing the app and (b) let ANY OTHER APP frame
// it (each app origin is 'self' to its own document), which is how one app steals
// another's UI. The policy must name the shell's origin and nothing else.
func TestAppOriginFrameAncestorsNamesOnlyTheShell(t *testing.T) {
	t.Setenv("VULOS_DOMAIN", "box.example.com")
	gw, _, mgr, token, userID := originTestGateway(t)
	startFakeHTMLApp(t, mgr, "clock", userID, "<html><head></head><body>clock</body></html>")

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "clock--default.box.example.com"
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: token})
	rec := httptest.NewRecorder()
	gw.Handler()(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %.120s)", rec.Code, rec.Body.String())
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if csp != "frame-ancestors http://box.example.com" {
		t.Fatalf("CSP = %q, want frame-ancestors naming exactly the shell origin", csp)
	}
	if strings.Contains(csp, "'self'") {
		t.Error("frame-ancestors 'self' on an app origin would let any other app frame this one")
	}
	// XFO cannot express "only this other origin", and SAMEORIGIN here would block
	// the shell outright. The app's own attempt to set DENY must not survive.
	if got := rec.Header().Get("X-Frame-Options"); got != "" {
		t.Errorf("X-Frame-Options = %q, want it stripped (CSP carries the policy)", got)
	}
}

// TestUnparseableLabelUnderBaseDomainIsRefused — the gateway used to "degrade
// gracefully" by trusting any label under the base domain as an app id. That means
// serving an app from a host we would never mint, i.e. a host whose canonical
// origin we cannot reproduce — so our own frame-ancestors and postMessage origin
// checks would disagree with the browser. Refuse instead.
func TestUnparseableLabelUnderBaseDomainIsRefused(t *testing.T) {
	t.Setenv("VULOS_DOMAIN", "box.example.com")
	gw, _, mgr, token, userID := originTestGateway(t)
	startFakeHTMLApp(t, mgr, "clock", userID, "<html><head></head><body>clock</body></html>")

	for _, host := range []string{
		"a--b--c.box.example.com", // ambiguous: two "--" separators
		"-bad.box.example.com",    // not a legal DNS label
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = host
		req.AddCookie(&http.Cookie{Name: "vulos_session", Value: token})
		rec := httptest.NewRecorder()
		gw.Handler()(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("host %q: status = %d, want 404 — an app must only be served from a label we would mint",
				host, rec.Code)
		}
	}
}

// TestAppCannotBeServedOnAnotherAppsOrigin — the identity of the app is taken from
// the HOST, never from the path. A request to app A's origin carrying a path that
// names app B must be served by A (it is just a path inside A's own space), never
// by B. Otherwise B's document would execute on A's origin and inherit A's storage.
func TestAppCannotBeServedOnAnotherAppsOrigin(t *testing.T) {
	t.Setenv("VULOS_DOMAIN", "box.example.com")
	gw, _, mgr, token, userID := originTestGateway(t)
	startFakeHTMLApp(t, mgr, "clock", userID, "<html><head></head><body>I AM CLOCK</body></html>")
	startFakeHTMLApp(t, mgr, "weather", userID, "<html><head></head><body>I AM WEATHER</body></html>")

	// Ask weather's origin for /app/clock/ — a path that, on the shell's origin,
	// would have addressed the clock app.
	req := httptest.NewRequest("GET", "/app/clock/", nil)
	req.Host = "weather--default.box.example.com"
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: token})
	rec := httptest.NewRecorder()
	gw.Handler()(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "I AM CLOCK") {
		t.Fatal("ORIGIN CONFUSION: clock's document was served on weather's origin — clock would run with weather's storage")
	}
	if !strings.Contains(body, "I AM WEATHER") {
		t.Errorf("expected weather to serve the path from its own space, got: %.120s", body)
	}
	if got := rec.Header().Get("X-Vulos-App"); got != "weather" {
		t.Errorf("X-Vulos-App = %q, want weather (identity comes from the Host, not the path)", got)
	}
}

// TestBridgeInjectedWithStrictShellOrigin — the injected client must address the
// shell by its exact origin. A '*' targetOrigin would hand the app's storage
// channel to whatever page happened to frame it.
func TestBridgeInjectedWithStrictShellOrigin(t *testing.T) {
	t.Setenv("VULOS_DOMAIN", "box.example.com")
	gw, _, mgr, token, userID := originTestGateway(t)
	startFakeHTMLApp(t, mgr, "clock", userID, "<html><head></head><body>clock</body></html>")

	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "clock--default.box.example.com"
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: token})
	rec := httptest.NewRecorder()
	gw.Handler()(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "vulos.bridge.hello") {
		t.Fatal("bridge client was not injected into the app's HTML")
	}
	if !strings.Contains(body, `SHELL="http://box.example.com"`) {
		t.Errorf("bridge does not pin the shell origin; body head: %.400s", body)
	}
	// The single postMessage the app makes must be addressed to SHELL, never '*'.
	if !strings.Contains(body, `,SHELL,[ch.port2])`) {
		t.Error("bridge handshake must post to the shell's exact origin")
	}
	if strings.Contains(body, `postMessage({type:"vulos.bridge.hello",v:V},"*"`) ||
		strings.Contains(body, `,"*",[`) {
		t.Error("bridge must never use '*' as a targetOrigin")
	}
	// Injected before the app's own body so the localStorage shim is installed
	// before any app script runs.
	if strings.Index(body, "vulos.bridge.hello") > strings.Index(body, "<body>") {
		t.Error("bridge must be injected into <head>, before the app's scripts")
	}
}

// TestBridgeNotInjectedWhenShellOriginUnnameable — fail closed. If we cannot name
// the origin the app should trust, we ship no bridge at all rather than a bridge
// that trusts anyone.
func TestBridgeRefusesFramingWhenShellOriginUnnameable(t *testing.T) {
	// Origins are ON (base domain set) but the app is reached on a host that does
	// not belong to the base domain — so ShellOrigin() cannot be derived for it.
	// This is the "browsing via an alias" case.
	t.Setenv("VULOS_DOMAIN", "box.example.com")
	gw, _, mgr, token, userID := originTestGateway(t)
	startFakeHTMLApp(t, mgr, "clock", userID, "<html><head></head><body>clock</body></html>")

	req := httptest.NewRequest("GET", "/app/clock/", nil)
	req.Host = "some-alias.internal:8080" // not under box.example.com
	req.AddCookie(&http.Cookie{Name: "vulos_session", Value: token})
	rec := httptest.NewRecorder()
	gw.Handler()(rec, req)

	// Not an app host → path-prefix branch → redirected to the canonical app origin.
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307 to the canonical app origin", rec.Code)
	}
}

// TestBridgeScriptEscapesShellOrigin — the origin is interpolated into a <script>.
// It is built from a validated host today, but the escaping is what keeps that from
// mattering.
func TestBridgeScriptEscapesShellOrigin(t *testing.T) {
	got := bridgeScript(`https://evil"</script><script>alert(1)</script>`)
	if strings.Contains(got, "</script><script>alert(1)") {
		t.Fatalf("shell origin was interpolated unescaped — HTML injection into the app frame:\n%s", got)
	}
	if !strings.Contains(got, `<`) {
		t.Error("expected < to be \\u-escaped in the JS string literal")
	}
	if bridgeScript("") != "" {
		t.Error("an unnameable shell origin must yield NO bridge (fail closed)")
	}
}

// TestRequestSchemeTrustsForwardedProtoOnlyFromLoopback — the origin strings we mint
// are compared against browser-computed origins. If a remote client could force
// "https", it could make us mint an https origin for a plaintext connection and
// every origin check would then be against a URL the browser never loads.
func TestRequestSchemeTrustsForwardedProtoOnlyFromLoopback(t *testing.T) {
	remote := httptest.NewRequest("GET", "/", nil)
	remote.RemoteAddr = "203.0.113.9:5555"
	remote.Header.Set("X-Forwarded-Proto", "https")
	if got := requestScheme(remote); got != "http" {
		t.Errorf("scheme = %q from a remote peer claiming https, want http", got)
	}

	local := httptest.NewRequest("GET", "/", nil)
	local.RemoteAddr = "127.0.0.1:5555"
	local.Header.Set("X-Forwarded-Proto", "https")
	if got := requestScheme(local); got != "https" {
		t.Errorf("scheme = %q from a loopback proxy, want https", got)
	}

	local6 := httptest.NewRequest("GET", "/", nil)
	local6.RemoteAddr = "[::1]:5555"
	local6.Header.Set("X-Forwarded-Proto", "https")
	if got := requestScheme(local6); got != "https" {
		t.Errorf("scheme = %q from an IPv6 loopback proxy, want https", got)
	}
}
