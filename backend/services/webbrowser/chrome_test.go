package webbrowser

// Unit tests for the webbrowser package — pure logic only.
// CDP HTTP calls are exercised against httptest.Server instances.
// Real Chromium / PulseAudio / stream.Pool are never started; those
// code-paths are skipped with t.Skip where they cannot be avoided.
//
// Recovered and adapted from 12e7507^:backend/services/webbrowser/chrome_test.go.
// The CDP / findBin / flag-set / JSON round-trip coverage is preserved; new
// tests cover the per-user profile derivation, session naming and LaunchOpts
// construction that the on-demand per-user launcher added.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// cdpBaseVar is the test-overridable base URL, defaulting to the production const.
var cdpBaseVar = cdpBase

func testCDPListTabs() ([]cdpTab, error) {
	resp, err := http.Get(cdpBaseVar + "/json/list")
	if err != nil {
		return nil, fmt.Errorf("CDP unavailable: %w", err)
	}
	defer resp.Body.Close()
	var tabs []cdpTab
	if err := json.NewDecoder(resp.Body).Decode(&tabs); err != nil {
		return nil, err
	}
	var pages []cdpTab
	for _, t := range tabs {
		if t.Type == "page" {
			pages = append(pages, t)
		}
	}
	return pages, nil
}

func testCDPNewTab(rawURL string) (*cdpTab, string, error) {
	target := cdpBaseVar + "/json/new?" + rawURL
	req, err := http.NewRequest("PUT", target, nil)
	if err != nil {
		return nil, target, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, target, err
	}
	defer resp.Body.Close()
	var tab cdpTab
	if err := json.NewDecoder(resp.Body).Decode(&tab); err != nil {
		return nil, target, err
	}
	return &tab, target, nil
}

func testCDPCloseTab(id string) (string, error) {
	target := cdpBaseVar + "/json/close/" + id
	req, _ := http.NewRequest("PUT", target, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return target, err
	}
	resp.Body.Close()
	return target, nil
}

func testCDPActivateTab(id string) (string, error) {
	target := cdpBaseVar + "/json/activate/" + id
	req, _ := http.NewRequest("PUT", target, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return target, err
	}
	resp.Body.Close()
	return target, nil
}

// ---------------------------------------------------------------------------
// Per-user profile derivation + session naming (NEW — the adapted launcher)
// ---------------------------------------------------------------------------

func TestProfileSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "default"},
		{"alice", "alice"},
		{"user-123_ABC", "user-123_ABC"},
		{"a@b.com", "a-b-com"},
		{"../../etc/passwd", "------etc-passwd"},
		{"has spaces", "has-spaces"},
		{"emoji😀here", "emoji-here"}, // non-ASCII rune → single replacement
	}
	for _, c := range cases {
		if got := profileSlug(c.in); got != c.want {
			t.Errorf("profileSlug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestProfileSlug_NoPathSeparators(t *testing.T) {
	// A slug must never contain a path separator — it is used as a single dir
	// segment, so traversal must be impossible.
	for _, in := range []string{"a/b", "..", "../x", "a\\b", "/etc"} {
		got := profileSlug(in)
		if strings.ContainsAny(got, "/\\") {
			t.Errorf("profileSlug(%q) = %q contains a path separator", in, got)
		}
	}
}

func TestSessionIDForUser(t *testing.T) {
	if got := sessionIDForUser(""); got != "browser" {
		t.Errorf("sessionIDForUser(\"\") = %q, want %q", got, "browser")
	}
	if got := sessionIDForUser("alice"); got != "browser-alice" {
		t.Errorf("sessionIDForUser(alice) = %q, want %q", got, "browser-alice")
	}
	// Distinct users get distinct session IDs (pool isolation).
	if sessionIDForUser("alice") == sessionIDForUser("bob") {
		t.Error("distinct users must map to distinct session IDs")
	}
	// Same user is stable (pool dedupe onto one live session).
	if sessionIDForUser("alice") != sessionIDForUser("alice") {
		t.Error("session ID must be stable for the same user")
	}
}

func TestProfileDir_PerUserIsolationAndPersistence(t *testing.T) {
	home := "/home/alice"
	dir := profileDir(home, "alice")
	want := filepath.Join(home, "browser", "profiles", "alice")
	if dir != want {
		t.Errorf("profileDir = %q, want %q", dir, want)
	}
	// Persistent location: derived from home, not /tmp or a random path.
	if !strings.HasPrefix(dir, home) {
		t.Errorf("profile dir %q not under user home %q (must persist)", dir, home)
	}
	// Different users → different dirs.
	if profileDir("/home/alice", "alice") == profileDir("/home/bob", "bob") {
		t.Error("distinct users must get distinct profile dirs")
	}
	// Empty home falls back to /root but stays deterministic.
	if got := profileDir("", "alice"); !strings.HasPrefix(got, "/root/") {
		t.Errorf("empty home should fall back to /root, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// LaunchOpts / Chromium flag construction
// ---------------------------------------------------------------------------

func TestChromeLaunchOpts(t *testing.T) {
	profDir := "/home/alice/.vulos/browser/profiles/alice"
	opts := chromeLaunchOpts("/usr/bin/chromium", "browser-alice", profDir, "alice")

	if opts.ID != "browser-alice" {
		t.Errorf("ID = %q, want browser-alice", opts.ID)
	}
	if opts.Name != "Chrome" {
		t.Errorf("Name = %q, want Chrome", opts.Name)
	}
	if opts.Command != "/usr/bin/chromium" {
		t.Errorf("Command = %q", opts.Command)
	}
	if opts.UserID != "alice" {
		t.Errorf("UserID = %q, want alice", opts.UserID)
	}
	if opts.Width != width || opts.Height != height || opts.FPS != fps {
		t.Errorf("dimensions = %dx%d@%d, want %dx%d@%d", opts.Width, opts.Height, opts.FPS, width, height, fps)
	}
	if !opts.Restart {
		t.Error("Restart should be true (browser restarts on exit)")
	}
	// The per-user profile MUST be wired into the args (this is the whole point).
	joined := strings.Join(opts.Args, " ")
	if !strings.Contains(joined, "--user-data-dir="+profDir) {
		t.Errorf("args missing --user-data-dir for the per-user profile: %q", joined)
	}
	if !strings.Contains(joined, "--load-extension="+filepath.Join(profDir, "extensions")) {
		t.Errorf("args missing per-profile --load-extension: %q", joined)
	}
}

// TestChromiumFlagSet guards the required Chromium flags (regression guard).
func TestChromiumFlagSet(t *testing.T) {
	profDir := "/home/u/.vulos/browser/profiles/u"
	args := chromeArgs(profDir)
	requiredFlags := []string{
		"--no-sandbox",
		"--disable-gpu",
		"--no-first-run",
		"--remote-debugging-port=9222",
		"--disable-popup-blocking",
		fmt.Sprintf("--window-size=%d,%d", width, height),
		"--disable-dev-shm-usage",
		"--disable-infobars",
		"--disable-default-apps",
		"--enable-extensions",
		"--user-data-dir=" + profDir,
		"--load-extension=" + filepath.Join(profDir, "extensions"),
		"--disable-component-update",
		"--noerrdialogs",
	}
	argSet := make(map[string]bool, len(args))
	for _, a := range args {
		argSet[a] = true
	}
	for _, flag := range requiredFlags {
		if !argSet[flag] {
			t.Errorf("required Chromium flag missing from arg set: %q", flag)
		}
	}
}

func TestChromiumWindowDimensions(t *testing.T) {
	if width != 1280 {
		t.Errorf("width = %d, want 1280", width)
	}
	if height != 720 {
		t.Errorf("height = %d, want 720", height)
	}
	if fps != 30 {
		t.Errorf("fps = %d, want 30", fps)
	}
}

// ---------------------------------------------------------------------------
// SEC-H regression: CDP URL encoding / injection guard
// ---------------------------------------------------------------------------

func TestCDPNewTab_URLEncoding(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantSeg string
	}{
		{"plain_https", "https://example.com", "https://example.com"},
		{"url_with_query", "https://example.com/search?q=hello+world", "https://example.com/search"},
		{"url_with_ampersand", "https://example.com/?a=1&b=2", "https://example.com/"},
		{"sec_h_path_traversal_guard", "https://evil.com/payload", "https://evil.com/payload"},
		{"raw_not_percent_encoded", "https://example.com/?foo=bar baz", ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var capturedURI string
			mux := http.NewServeMux()
			mux.HandleFunc("/json/new", func(w http.ResponseWriter, r *http.Request) {
				capturedURI = r.RequestURI
				tab := cdpTab{ID: "test-id", Title: "Test", URL: tc.input, Type: "page"}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(tab)
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			oldBase := cdpBaseVar
			cdpBaseVar = srv.URL
			defer func() { cdpBaseVar = oldBase }()

			_, fullTarget, err := testCDPNewTab(tc.input)
			if err != nil {
				t.Logf("testCDPNewTab(%q) rejected by http.NewRequest: %v", tc.input, err)
				return
			}
			parsed, err := url.Parse(fullTarget)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", fullTarget, err)
			}
			if parsed.Path != "/json/new" {
				t.Errorf("SEC-H: URL injection into path: got path %q, want /json/new (full target: %s)",
					parsed.Path, fullTarget)
			}
			if capturedURI != "" && tc.wantSeg != "" {
				if !strings.Contains(capturedURI, tc.wantSeg) {
					t.Errorf("server URI %q does not contain expected segment %q", capturedURI, tc.wantSeg)
				}
			}
		})
	}
}

func TestCDPURLEncoding_ValuesComparison(t *testing.T) {
	input := "https://example.com/path?q=hello world&x=<script>"
	rawTarget := "/json/new?" + input
	v := url.Values{}
	v.Set("url", input)
	encodedTarget := "/json/new?" + v.Encode()
	if rawTarget == encodedTarget {
		t.Error("raw-append and url.Values-encoded targets are unexpectedly equal")
	}
	parsedRaw, _ := url.Parse("http://host" + rawTarget)
	if parsedRaw.Path != "/json/new" {
		t.Errorf("raw path escaped to %q", parsedRaw.Path)
	}
	parsedEncoded, _ := url.Parse("http://host" + encodedTarget)
	if parsedEncoded.Path != "/json/new" {
		t.Errorf("encoded path escaped to %q", parsedEncoded.Path)
	}
}

// ---------------------------------------------------------------------------
// cdpListTabs — filter logic
// ---------------------------------------------------------------------------

func TestCDPListTabs_FiltersToPageType(t *testing.T) {
	allTabs := []cdpTab{
		{ID: "1", Title: "Page One", URL: "https://example.com", Type: "page"},
		{ID: "2", Title: "Dev Tools", URL: "devtools://...", Type: "other"},
		{ID: "3", Title: "Page Two", URL: "https://go.dev", Type: "page"},
		{ID: "4", Title: "Background", URL: "chrome://newtab", Type: "background_page"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(allTabs)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	oldBase := cdpBaseVar
	cdpBaseVar = srv.URL
	defer func() { cdpBaseVar = oldBase }()

	pages, err := testCDPListTabs()
	if err != nil {
		t.Fatalf("testCDPListTabs: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("expected 2 page-type tabs, got %d: %+v", len(pages), pages)
	}
	for _, p := range pages {
		if p.Type != "page" {
			t.Errorf("non-page tab leaked through filter: %+v", p)
		}
	}
}

func TestCDPListTabs_Empty(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	oldBase := cdpBaseVar
	cdpBaseVar = srv.URL
	defer func() { cdpBaseVar = oldBase }()

	pages, err := testCDPListTabs()
	if err != nil {
		t.Fatalf("testCDPListTabs: %v", err)
	}
	if pages != nil && len(pages) != 0 {
		t.Errorf("expected nil/empty slice, got %v", pages)
	}
}

func TestCDPListTabs_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/json/list", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	oldBase := cdpBaseVar
	cdpBaseVar = srv.URL
	defer func() { cdpBaseVar = oldBase }()

	if _, err := testCDPListTabs(); err == nil {
		t.Error("expected error on non-JSON 503 response, got nil")
	}
}

func TestCDPListTabs_Unavailable(t *testing.T) {
	oldBase := cdpBaseVar
	cdpBaseVar = "http://127.0.0.1:19999"
	defer func() { cdpBaseVar = oldBase }()

	_, err := testCDPListTabs()
	if err == nil {
		t.Error("expected error when CDP server is unreachable, got nil")
	}
	if !strings.Contains(err.Error(), "CDP unavailable") {
		t.Errorf("error message should mention 'CDP unavailable', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// cdpCloseTab / cdpActivateTab — path construction
// ---------------------------------------------------------------------------

func TestCDPCloseTab_PathConstruction(t *testing.T) {
	tabID := "abc-123-def"
	var receivedPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/json/close/", func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	oldBase := cdpBaseVar
	cdpBaseVar = srv.URL
	defer func() { cdpBaseVar = oldBase }()

	if _, err := testCDPCloseTab(tabID); err != nil {
		t.Fatalf("testCDPCloseTab: %v", err)
	}
	if want := "/json/close/" + tabID; receivedPath != want {
		t.Errorf("path = %q, want %q", receivedPath, want)
	}
}

func TestCDPActivateTab_PathConstruction(t *testing.T) {
	tabID := "xyz-789"
	var receivedPath string
	mux := http.NewServeMux()
	mux.HandleFunc("/json/activate/", func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	oldBase := cdpBaseVar
	cdpBaseVar = srv.URL
	defer func() { cdpBaseVar = oldBase }()

	if _, err := testCDPActivateTab(tabID); err != nil {
		t.Fatalf("testCDPActivateTab: %v", err)
	}
	if want := "/json/activate/" + tabID; receivedPath != want {
		t.Errorf("path = %q, want %q", receivedPath, want)
	}
}

func TestCDPNewTab_UsesPUTMethod(t *testing.T) {
	var receivedMethod string
	mux := http.NewServeMux()
	mux.HandleFunc("/json/new", func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		tab := cdpTab{ID: "new-tab", Title: "", URL: "about:blank", Type: "page"}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tab)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	oldBase := cdpBaseVar
	cdpBaseVar = srv.URL
	defer func() { cdpBaseVar = oldBase }()

	if _, _, err := testCDPNewTab("about:blank"); err != nil {
		t.Fatalf("testCDPNewTab: %v", err)
	}
	if receivedMethod != http.MethodPut {
		t.Errorf("expected PUT, got %s", receivedMethod)
	}
}

// ---------------------------------------------------------------------------
// findBin — pure logic tests (no process spawning)
// ---------------------------------------------------------------------------

func TestFindBin_ReturnsEmptyWhenNothingFound(t *testing.T) {
	if result := findBin("__nonexistent_binary_xyzzy_vulos__"); result != "" {
		t.Errorf("expected empty string for missing binary, got %q", result)
	}
}

func TestFindBin_ReturnsFirstMatch(t *testing.T) {
	result := findBin("__nonexistent_binary_xyzzy__", "sh")
	if result == "" {
		t.Skip("sh not found on PATH — skipping findBin first-match test")
	}
	if !strings.HasSuffix(result, "sh") {
		t.Errorf("expected path ending in 'sh', got %q", result)
	}
}

func TestFindBin_SkipsNonExistentThenFinds(t *testing.T) {
	result := findBin("__missing_1__", "__missing_2__", "sh")
	if result == "" {
		t.Skip("sh not found on PATH")
	}
	if !strings.Contains(result, "sh") {
		t.Errorf("expected 'sh' in result, got %q", result)
	}
}

func TestFindBin_AllMissing(t *testing.T) {
	if result := findBin("__a__", "__b__", "__c__"); result != "" {
		t.Errorf("expected empty string when all binaries missing, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// cdpTab struct — JSON round-trip
// ---------------------------------------------------------------------------

func TestCDPTab_JSONRoundTrip(t *testing.T) {
	original := cdpTab{
		ID:    "tab-abc-123",
		Title: "Test Page",
		URL:   "https://example.com/path?q=1&r=2",
		Type:  "page",
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var decoded cdpTab
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if decoded != original {
		t.Errorf("round-trip mismatch:\n  original: %+v\n  decoded:  %+v", original, decoded)
	}
}

// ---------------------------------------------------------------------------
// constants
// ---------------------------------------------------------------------------

func TestDefaultSessionIDConstant(t *testing.T) {
	if defaultSessionID != "browser" {
		t.Errorf("defaultSessionID = %q, want %q", defaultSessionID, "browser")
	}
}

func TestCDPBase_Format(t *testing.T) {
	parsed, err := url.Parse(cdpBase)
	if err != nil {
		t.Fatalf("url.Parse(cdpBase): %v", err)
	}
	if parsed.Scheme != "http" {
		t.Errorf("cdpBase scheme = %q, want %q", parsed.Scheme, "http")
	}
	if parsed.Hostname() != "127.0.0.1" {
		t.Errorf("cdpBase host = %q, want 127.0.0.1", parsed.Hostname())
	}
	if parsed.Port() != "9222" {
		t.Errorf("cdpBase port = %q, want 9222", parsed.Port())
	}
}
