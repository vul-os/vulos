//go:build e2e

// Package e2e drives the REAL Vulos server binary over real HTTP.
//
// Everything else that calls itself an end-to-end test in this repo stops
// short of the real process:
//
//   - frontend/e2e (Playwright) fulfils every backend route in the browser,
//     so no Go code runs at all.
//   - The Go route tests build an httptest mux out of hand-picked handlers.
//   - backend/firstboot/e2e seeds and drives the first-boot lifecycle
//     IN-PROCESS against mocks (no real S3, no real network). It is the
//     complement of this file, not a duplicate: it goes wide over the
//     lifecycle in one process; this goes narrow over auth through a real one.
//
// None of them has ever proved that `cmd/server` — the actual shipped binary,
// with its real wiring, real middleware order, real on-disk stores and real
// session cookies — can take a user from first boot to a logged-in session
// and back.
//
// That gap is why this exists. It compiles cmd/server, runs it on a temp data
// directory and an ephemeral port, and talks to it with a plain HTTP client.
// No mocks, no in-process mux, no shortcuts past the middleware.
//
// Build-tagged so a normal `go test ./...` stays fast and hermetic:
//
//	go test -tags e2e ./e2e/...
//
// The tag is deliberate rather than a skip. A test that quietly skips itself
// reads as a pass in CI output; this one cannot run by accident and cannot
// silently not-run when it was meant to.
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	// The server is a large binary; a cold `go build` on a loaded machine can
	// take a while, and a real first boot does a lot of on-disk setup.
	buildTimeout = 4 * time.Minute
	bootTimeout  = 60 * time.Second
)

// box is a running instance of the real server.
type box struct {
	t       *testing.T
	baseURL string
	dataDir string
	// lanURL is the box's LAN HTTPS listener, when one was started. The fabric
	// and CRDT exchange endpoints are served only there — baseURL reaches the
	// public mux, which 401s them. Empty for boxes started without it.
	lanURL string
	cmd    *exec.Cmd
	logBuf *syncBuffer
}

// syncBuffer collects the server's stdout+stderr so a failure can print what
// the server actually said, instead of leaving the reader guessing. The server
// writes from its own goroutines while the test reads, so it needs the lock.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// freePort asks the kernel for an unused port and immediately releases it.
// There is an unavoidable race between release and the server's own bind, but
// it is far better than a hardcoded port that collides with a developer's
// running box or with a parallel test.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// TestMain builds cmd/server ONCE for the whole package and removes it at the
// end. It deliberately does not live in a t.Cleanup: the binary is shared by
// every test, so cleaning it up on the first test's `t` deletes it out from
// under the rest — which is exactly what happened the first time this was
// written, leaving "fork/exec ...: no such file or directory" on every test
// after the first.
func TestMain(m *testing.M) {
	bin, err := buildServer()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: %v\n", err)
		os.Exit(1)
	}
	builtServerPath = bin
	code := m.Run()
	_ = os.Remove(bin)
	os.Exit(code)
}

// builtServerPath is the compiled server, set by TestMain before any test runs.
var builtServerPath string

func buildServer() (string, error) {
	out := filepath.Join(os.TempDir(), fmt.Sprintf("vulos-server-e2e-%d", os.Getpid()))

	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	cmd := exec.Command("go", "build", "-o", out, "./cmd/server")
	cmd.Dir = filepath.Dir(wd) // this file lives in backend/e2e; build from backend/
	var logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &logs, &logs

	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start `go build ./cmd/server`: %w", err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("build cmd/server: %w\n%s", err, logs.String())
		}
	case <-time.After(buildTimeout):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("build cmd/server timed out after %s\n%s", buildTimeout, logs.String())
	}
	return out, nil
}

// startBox boots a real server on a fresh data directory.
func startBox(t *testing.T) *box {
	t.Helper()
	return startBoxAt(t, builtServerPath, t.TempDir())
}

// startBoxAt boots the server against an EXISTING data directory, so a test can
// restart a box and assert that state genuinely survived to disk.
func startBoxAt(t *testing.T, bin, dataDir string) *box {
	t.Helper()
	port := freePort(t)

	cmd := exec.Command(bin, "--env", "local")
	cmd.Env = append(os.Environ(),
		"PORT="+fmt.Sprint(port),
		"VULOS_DATA_DIR="+dataDir,
		"HOME="+dataDir,
		// Keep the box inert: no relay dial-out, no mail sidecar, no AI backend.
		// This test is about the OS's own auth and persistence, and a box that
		// reaches the network would make it slow and flaky.
		"VULOS_AI_MODE=off",
		"VULOS_MAIL_URL=",
		"VULOS_RENDEZVOUS_URL=",
	)
	logs := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = logs, logs

	if err := cmd.Start(); err != nil {
		t.Fatalf("start the server: %v", err)
	}

	b := &box{
		t:       t,
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		dataDir: dataDir,
		cmd:     cmd,
		logBuf:  logs,
	}
	t.Cleanup(b.stop)
	b.waitReady()
	return b
}

// waitReady polls a real endpoint until the server answers.
//
// It polls rather than sleeping, and it FAILS LOUDLY on timeout with the
// server's own log attached. A harness that silently proceeds when the server
// never came up is worse than no harness: every later assertion would fail for
// the wrong reason, or worse, vacuously pass.
func (b *box) waitReady() {
	b.t.Helper()
	deadline := time.Now().Add(bootTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if b.cmd.ProcessState != nil && b.cmd.ProcessState.Exited() {
			b.t.Fatalf("server exited during boot (code %d)\n--- server log ---\n%s",
				b.cmd.ProcessState.ExitCode(), b.logBuf.String())
		}
		resp, err := http.Get(b.baseURL + "/api/setup/status")
		if err == nil {
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
			return
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	b.t.Fatalf("server did not answer /api/setup/status within %s (last error: %v)\n--- server log ---\n%s",
		bootTimeout, lastErr, b.logBuf.String())
}

func (b *box) stop() {
	if b.cmd == nil || b.cmd.Process == nil {
		return
	}
	_ = b.cmd.Process.Kill()
	_, _ = b.cmd.Process.Wait()
}

// client returns an HTTP client with its own cookie jar — one jar per logical
// user, so a test can hold two independent sessions against the same box and
// prove they cannot see each other.
func (b *box) client() *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		b.t.Fatalf("cookiejar: %v", err)
	}
	return &http.Client{
		Jar:     jar,
		Timeout: 20 * time.Second,
		// Do not follow redirects: a 302 to a login page would otherwise turn
		// an auth failure into a 200 and hide it.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type reply struct {
	status int
	body   []byte
}

func (r reply) json(t *testing.T, into any) {
	t.Helper()
	if err := json.Unmarshal(r.body, into); err != nil {
		t.Fatalf("decode response %q: %v", string(r.body), err)
	}
}

func (b *box) do(c *http.Client, method, path string, body any) reply {
	b.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			b.t.Fatalf("marshal request: %v", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, b.baseURL+path, rdr)
	if err != nil {
		b.t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v\n--- server log ---\n%s", method, path, err, b.logBuf.String())
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, body: raw}
}

// register creates a user. The FIRST call on a fresh box needs no auth (first
// -run setup); later ones require an admin session, which is the real server's
// own rule, not something this harness arranges.
func (b *box) register(c *http.Client, username, password, display string) reply {
	b.t.Helper()
	return b.do(c, "POST", "/api/auth/register", map[string]string{
		"username": username, "password": password, "display_name": display,
	})
}

func (b *box) login(c *http.Client, username, password string) reply {
	b.t.Helper()
	return b.do(c, "POST", "/api/auth/login", map[string]string{
		"username": username, "password": password,
	})
}

// hasSessionCookie reports whether the jar holds a Vulos session for the box.
func (b *box) hasSessionCookie(c *http.Client) bool {
	u := mustParseURL(b.t, b.baseURL)
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == "vulos_session" && ck.Value != "" {
			return true
		}
	}
	return false
}

// sessionToken returns the raw session token the jar holds, so a test can
// replay it AFTER logout and prove the server revoked it rather than trusting
// that the client forgot it.
func (b *box) sessionToken(c *http.Client) string {
	u := mustParseURL(b.t, b.baseURL)
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == "vulos_session" {
			return ck.Value
		}
	}
	return ""
}

// doWithToken sends a request carrying an explicit session token and NO cookie
// jar. extractToken accepts a Bearer header, so this reaches the same session
// the browser cookie would, without depending on client-side cookie state.
func (b *box) doWithToken(token, method, path string) reply {
	b.t.Helper()
	req, err := http.NewRequest(method, b.baseURL+path, nil)
	if err != nil {
		b.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		b.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, body: raw}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

// meResponse is the real shape of GET /api/auth/me: the user, their profile
// (which carries the role) and the live session, each nested rather than
// flattened.
type meResponse struct {
	User struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"user"`
	Profile struct {
		UserID      string `json:"user_id"`
		Role        string `json:"role"`
		DisplayName string `json:"display_name"`
	} `json:"profile"`
	Session struct {
		ID     string `json:"id"`
		UserID string `json:"user_id"`
	} `json:"session"`
}

// ─────────────────────────────────────────────────────────────────────────────

// TestFirstBootToLoggedIn is the flow that has never been exercised against the
// real binary: an empty box, a first user, a session, an authenticated read,
// and a logout that actually revokes.
func TestFirstBootToLoggedIn(t *testing.T) {
	b := startBox(t)
	c := b.client()

	// A fresh box must have no users, and /api/auth/me must be unauthenticated.
	if r := b.do(c, "GET", "/api/auth/me", nil); r.status == http.StatusOK {
		t.Fatalf("a fresh box served /api/auth/me without a session (status %d, body %s)", r.status, r.body)
	}

	// First-run registration needs no credentials — this is the real server's
	// HasAnyUsers() branch, not a test affordance.
	if r := b.register(c, "ada", "correct-horse-battery-staple", "Ada Lovelace"); r.status != http.StatusOK {
		t.Fatalf("first-run register: status %d, body %s\n--- server log ---\n%s", r.status, r.body, b.logBuf.String())
	}

	// First-run registration signs the new owner in (the real server calls
	// CreateSession in handleRegister), so `c` already holds a session here.
	// The wrong-password check therefore runs on a CLEAN client — otherwise it
	// would be reading the cookie registration just set and would pass no
	// matter how login behaved.
	attacker := b.client()
	if r := b.login(attacker, "ada", "not-the-password"); r.status == http.StatusOK {
		t.Fatalf("login succeeded with the wrong password (status %d)", r.status)
	}
	if b.hasSessionCookie(attacker) {
		t.Fatal("a failed login minted a session cookie")
	}
	if r := b.do(attacker, "GET", "/api/auth/me", nil); r.status == http.StatusOK {
		t.Fatalf("a client that never logged in reached /api/auth/me (status %d, body %s)", r.status, r.body)
	}

	// A real login on that same clean client must mint a real cookie.
	if r := b.login(attacker, "ada", "correct-horse-battery-staple"); r.status != http.StatusOK {
		t.Fatalf("login: status %d, body %s", r.status, r.body)
	}
	if !b.hasSessionCookie(attacker) {
		t.Fatal("a successful login set no vulos_session cookie")
	}
	c = attacker

	// And that cookie must actually authenticate a request.
	me := b.do(c, "GET", "/api/auth/me", nil)
	if me.status != http.StatusOK {
		t.Fatalf("GET /api/auth/me with a session: status %d, body %s", me.status, me.body)
	}
	var user meResponse
	me.json(t, &user)
	if user.User.Username != "ada" {
		t.Errorf("/api/auth/me returned username %q, want ada (body %s)", user.User.Username, me.body)
	}
	if user.User.ID == "" {
		t.Errorf("/api/auth/me returned no user id (body %s)", me.body)
	}
	if user.Profile.Role != "admin" {
		t.Errorf("the first user on a box has role %q, want admin (body %s)", user.Profile.Role, me.body)
	}

	// Logout must revoke SERVER-SIDE, not merely clear the client's cookie.
	//
	// Capturing the token first is the whole point. Checking only that the
	// jar's next request fails proves the CLIENT forgot the session, which it
	// would do even if the server kept it alive forever — and that is not a
	// hypothetical: deleting the RevokeSession call left this test green until
	// the replay below was added.
	token := b.sessionToken(c)
	if token == "" {
		t.Fatal("no session token to replay — the jar held no vulos_session before logout")
	}
	if r := b.doWithToken(token, "GET", "/api/auth/me"); r.status != http.StatusOK {
		t.Fatalf("the captured token did not work BEFORE logout (status %d) — the replay check would prove nothing", r.status)
	}

	if r := b.do(c, "POST", "/api/auth/logout", nil); r.status != http.StatusOK {
		t.Fatalf("logout: status %d, body %s", r.status, r.body)
	}
	if r := b.do(c, "GET", "/api/auth/me", nil); r.status == http.StatusOK {
		t.Fatalf("the session still worked after logout (status %d, body %s)", r.status, r.body)
	}
	if r := b.doWithToken(token, "GET", "/api/auth/me"); r.status == http.StatusOK {
		t.Fatalf("REPLAY: the session token still authenticates after logout (status %d, body %s) — logout cleared the cookie but never revoked the session",
			r.status, r.body)
	}
}

// TestSessionSurvivesRestart proves the box wrote its user to DISK rather than
// holding it in memory — the property a personal server lives or dies by, and
// one no in-process test can observe.
func TestStateSurvivesRestart(t *testing.T) {
	bin := builtServerPath
	dataDir := t.TempDir()

	first := startBoxAt(t, bin, dataDir)
	c := first.client()
	if r := first.register(c, "ada", "correct-horse-battery-staple", "Ada Lovelace"); r.status != http.StatusOK {
		t.Fatalf("register: status %d, body %s", r.status, r.body)
	}
	first.stop()

	// Same data directory, new process, new port.
	second := startBoxAt(t, bin, dataDir)
	c2 := second.client()

	// The user must still exist: registering again unauthenticated must now be
	// REFUSED (the box is no longer in first-run state), and the original
	// credentials must still log in.
	if r := second.register(c2, "mallory", "another-password", "Mallory"); r.status == http.StatusOK {
		t.Errorf("unauthenticated registration succeeded after restart — the box forgot it already had a user")
	}
	if r := second.login(c2, "ada", "correct-horse-battery-staple"); r.status != http.StatusOK {
		t.Fatalf("login after restart: status %d, body %s\n--- server log ---\n%s", r.status, r.body, second.logBuf.String())
	}
	if !second.hasSessionCookie(c2) {
		t.Fatal("login after restart set no session cookie")
	}
}

// TestSecondProfileIsIsolated is the multi-user property, asserted against the
// real binary rather than a hand-built mux: two accounts on one box, each
// seeing only itself.
func TestSecondProfileIsIsolated(t *testing.T) {
	b := startBox(t)

	admin := b.client()
	if r := b.register(admin, "ada", "correct-horse-battery-staple", "Ada"); r.status != http.StatusOK {
		t.Fatalf("register admin: status %d, body %s", r.status, r.body)
	}
	if r := b.login(admin, "ada", "correct-horse-battery-staple"); r.status != http.StatusOK {
		t.Fatalf("login admin: status %d, body %s", r.status, r.body)
	}

	// A second account can only be created by an admin — verify the real rule
	// holds before relying on it.
	anon := b.client()
	if r := b.register(anon, "bob", "hunter2-hunter2", "Bob"); r.status == http.StatusOK {
		t.Fatal("an unauthenticated caller created a second account on a provisioned box")
	}
	if r := b.register(admin, "bob", "hunter2-hunter2", "Bob"); r.status != http.StatusOK {
		t.Fatalf("admin creating a second account: status %d, body %s", r.status, r.body)
	}

	bob := b.client()
	if r := b.login(bob, "bob", "hunter2-hunter2"); r.status != http.StatusOK {
		t.Fatalf("login bob: status %d, body %s", r.status, r.body)
	}

	adaMe := b.do(admin, "GET", "/api/auth/me", nil)
	bobMe := b.do(bob, "GET", "/api/auth/me", nil)
	var adaU, bobU meResponse
	adaMe.json(t, &adaU)
	bobMe.json(t, &bobU)

	if adaU.User.ID == "" || bobU.User.ID == "" {
		t.Fatalf("missing user ids (ada %s / bob %s)", adaMe.body, bobMe.body)
	}
	if adaU.User.ID == bobU.User.ID {
		t.Fatalf("both sessions resolve to the SAME user id %q — the sessions are not distinct", adaU.User.ID)
	}
	if bobU.User.Username != "bob" {
		t.Errorf("bob's session reports username %q", bobU.User.Username)
	}
	// The second account must NOT inherit admin — only the first user does.
	if bobU.Profile.Role == "admin" {
		t.Errorf("the second account was created as an admin (body %s)", bobMe.body)
	}

	// Bob must not be able to edit Ada's profile by naming her id.
	upd := b.do(bob, "PUT", "/api/profiles/"+adaU.User.ID, map[string]string{"display_name": "Pwned"})
	if upd.status == http.StatusOK {
		t.Errorf("IDOR: bob updated ada's profile via PUT /api/profiles/%s (status %d, body %s)",
			adaU.User.ID, upd.status, upd.body)
	}

	// And Ada's profile must be unchanged when she reads it back.
	after := b.do(admin, "GET", "/api/auth/me", nil)
	if strings.Contains(string(after.body), "Pwned") {
		t.Errorf("ada's profile was mutated by another account: %s", after.body)
	}
}
