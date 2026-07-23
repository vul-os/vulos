package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- shared test helpers (unique names, this file only) -------------------

// wtWriteTree materialises a map of slash-separated relative paths -> contents
// under root, creating intermediate directories as needed.
func wtWriteTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", p, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
}

// wtReadTarGz decodes a gzip'd tar into a map of regular-file name -> content.
// Directory entries (names ending in "/") are ignored.
func wtReadTarGz(t *testing.T, data []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip open: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("tar read %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = string(b)
	}
	return out
}

// wtLoopbackClient returns a client bound to srv, bypassing env resolution so a
// test can drive the request-shaping code directly.
func wtLoopbackClient(srv *httptest.Server, token string) *client {
	return &client{
		baseURL: strings.TrimRight(srv.URL, "/"),
		token:   token,
		http:    srv.Client(),
	}
}

// ---- packaging: tarGzDir --------------------------------------------------

func TestTarGzDirRoundTripsAndExcludes(t *testing.T) {
	dir := t.TempDir()
	wtWriteTree(t, dir, map[string]string{
		"index.html":              "<h1>hi</h1>",
		"assets/app.js":           "console.log(1)",
		"assets/styles/site.css":  "body{}",
		".env":                    "SECRET=1",       // dotfile -> excluded
		".git/config":             "[core]",         // dotdir -> excluded
		"node_modules/x/index.js": "module.exports", // dep dir -> excluded
		"sub/.hidden":             "nope",           // dotfile in subdir -> excluded
	})

	var buf bytes.Buffer
	n, err := tarGzDir(dir, &buf)
	if err != nil {
		t.Fatalf("tarGzDir: %v", err)
	}
	if n != 3 {
		t.Fatalf("regular-file count = %d, want 3", n)
	}

	got := wtReadTarGz(t, buf.Bytes())
	want := map[string]string{
		"index.html":             "<h1>hi</h1>",
		"assets/app.js":          "console.log(1)",
		"assets/styles/site.css": "body{}",
	}
	if len(got) != len(want) {
		t.Fatalf("archived files = %v, want keys %v", keysOf(got), keysOf(want))
	}
	for name, content := range want {
		gc, ok := got[name]
		if !ok {
			t.Errorf("missing %q in archive", name)
			continue
		}
		if gc != content {
			t.Errorf("%q content = %q, want %q", name, gc, content)
		}
	}
	// Explicitly assert the excluded paths never appear (forward-slash names).
	for _, bad := range []string{".env", ".git/config", "node_modules/x/index.js", "sub/.hidden"} {
		if _, ok := got[bad]; ok {
			t.Errorf("excluded path %q leaked into archive", bad)
		}
	}
}

func TestTarGzDirEmptyIsError(t *testing.T) {
	dir := t.TempDir()
	// Only excluded content present: nothing deployable.
	wtWriteTree(t, dir, map[string]string{
		"node_modules/dep/main.js": "x",
		".env":                     "y",
	})
	var buf bytes.Buffer
	n, err := tarGzDir(dir, &buf)
	if err == nil {
		t.Fatalf("expected error for empty deployable set, got nil (count=%d)", n)
	}
	if !strings.Contains(err.Error(), "no deployable files") {
		t.Fatalf("error = %v, want it to mention 'no deployable files'", err)
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---- env resolution: newClient --------------------------------------------

func TestNewClientRequiresToken(t *testing.T) {
	t.Setenv("VULOS_BOX_URL", "http://localhost:8080")
	for _, tok := range []string{"", "   "} {
		t.Setenv("VULOS_TOKEN", tok)
		if _, err := newClient(); err == nil {
			t.Fatalf("newClient() with token %q = nil error, want failure", tok)
		}
	}
}

func TestNewClientResolvesBoxURL(t *testing.T) {
	t.Setenv("VULOS_TOKEN", "tok")

	// Configured URL wins and trailing slashes are trimmed.
	t.Setenv("VULOS_BOX_URL", "http://box.example:9000/")
	c, err := newClient()
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	if c.baseURL != "http://box.example:9000" {
		t.Errorf("baseURL = %q, want %q", c.baseURL, "http://box.example:9000")
	}
	if c.token != "tok" {
		t.Errorf("token = %q, want %q", c.token, "tok")
	}

	// Empty -> loopback default.
	t.Setenv("VULOS_BOX_URL", "")
	c, err = newClient()
	if err != nil {
		t.Fatalf("newClient default: %v", err)
	}
	if c.baseURL != defaultBoxURL {
		t.Errorf("default baseURL = %q, want %q", c.baseURL, defaultBoxURL)
	}
}

// TestNewRequestPinsTokenToConfiguredHost asserts the token is only ever bound
// to a request whose host is the configured box — even when the caller hands in
// a path that looks like an absolute URL to another host.
func TestNewRequestPinsTokenToConfiguredHost(t *testing.T) {
	c := &client{baseURL: "http://127.0.0.1:12345", token: "sekret", http: http.DefaultClient}

	req, err := c.newRequest(http.MethodGet, "https://evil.example/steal", nil)
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}
	if req.URL.Host != "127.0.0.1:12345" {
		t.Fatalf("request host = %q, want configured box host (token must not reach evil.example)", req.URL.Host)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sekret" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer sekret")
	}

	// A box-relative path is left on the box host and prefixed with "/".
	req2, err := c.newRequest(http.MethodDelete, "api/web/sites/foo", nil)
	if err != nil {
		t.Fatalf("newRequest relative: %v", err)
	}
	if req2.URL.Host != "127.0.0.1:12345" || req2.URL.Path != "/api/web/sites/foo" {
		t.Fatalf("relative path routed to %q%q, want box host + /api/web/sites/foo", req2.URL.Host, req2.URL.Path)
	}
}

// ---- request shaping against a stand-in box -------------------------------

func TestClientGetJSONDecodesAndSendsAuth(t *testing.T) {
	var gotAuth, gotAccept, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotMethod = r.Method
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"sites":[{"name":"blog","url":"https://blog.box","bytes":2048,"updated_ts":1700000000,"domains":["blog.example.com"]}]}`)
	}))
	defer srv.Close()

	c := wtLoopbackClient(srv, "tok-123")
	var out siteList
	if err := c.getJSON("/api/web/sites", &out); err != nil {
		t.Fatalf("getJSON: %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotAuth != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want Bearer tok-123", gotAuth)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
	if len(out.Sites) != 1 || out.Sites[0].Name != "blog" {
		t.Fatalf("decoded sites = %+v, want one site named blog", out.Sites)
	}
	if out.Sites[0].Bytes != 2048 || len(out.Sites[0].Domains) != 1 {
		t.Errorf("decoded site = %+v, want bytes=2048 + one domain", out.Sites[0])
	}
}

func TestClientHTTPErrorSurfacesServerMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"site not found"}`)
	}))
	defer srv.Close()

	c := wtLoopbackClient(srv, "tok")
	err := c.getJSON("/api/web/sites", &siteList{})
	if err == nil {
		t.Fatalf("expected error on 404")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "site not found") {
		t.Fatalf("error = %v, want it to carry status 404 and the server message", err)
	}
}

// TestWebDeployShapesUpload exercises arg parsing + packaging + the client
// against a stand-in box, asserting the wire contract the box expects.
func TestWebDeployShapesUpload(t *testing.T) {
	var (
		gotMethod, gotPath, gotSite, gotCT, gotAuth string
		archivedIndex                               string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotSite = r.URL.Query().Get("site")
		gotCT = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		files := wtReadTarGz(t, body)
		archivedIndex = files["index.html"]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(deployResult{OK: true, URL: "https://mysite.box"})
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "src")
	wtWriteTree(t, dir, map[string]string{"index.html": "<h1>deploy me</h1>"})

	t.Setenv("VULOS_BOX_URL", srv.URL)
	t.Setenv("VULOS_TOKEN", "deploy-tok")

	if err := webDeploy([]string{"--site", "mysite", dir}); err != nil {
		t.Fatalf("webDeploy: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/web/sites" {
		t.Errorf("path = %q, want /api/web/sites", gotPath)
	}
	if gotSite != "mysite" {
		t.Errorf("?site = %q, want mysite", gotSite)
	}
	if gotCT != "application/gzip" {
		t.Errorf("Content-Type = %q, want application/gzip", gotCT)
	}
	if gotAuth != "Bearer deploy-tok" {
		t.Errorf("Authorization = %q, want Bearer deploy-tok", gotAuth)
	}
	if archivedIndex != "<h1>deploy me</h1>" {
		t.Errorf("uploaded index.html = %q, want the source content", archivedIndex)
	}
}

// TestWebDeployDerivesSiteNameFromDir confirms the default site name comes from
// the directory's base name when --site is omitted.
func TestWebDeployDerivesSiteNameFromDir(t *testing.T) {
	var gotSite string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSite = r.URL.Query().Get("site")
		_ = json.NewEncoder(w).Encode(deployResult{OK: true, URL: "https://x"})
	}))
	defer srv.Close()

	dir := filepath.Join(t.TempDir(), "MyBlog")
	wtWriteTree(t, dir, map[string]string{"index.html": "x"})

	t.Setenv("VULOS_BOX_URL", srv.URL)
	t.Setenv("VULOS_TOKEN", "tok")

	if err := webDeploy([]string{dir}); err != nil {
		t.Fatalf("webDeploy: %v", err)
	}
	if gotSite != "myblog" {
		t.Fatalf("derived site = %q, want myblog", gotSite)
	}
}

// ---- argument parsing / dispatch (no box contacted) -----------------------

func wtAssertUsage(t *testing.T, err error, ctx string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected a usage error, got nil", ctx)
	}
	if !errors.As(err, &usageError{}) {
		t.Fatalf("%s: error %v is not a usageError (would exit 2 not 1)", ctx, err)
	}
}

func TestArgParsingUsageErrors(t *testing.T) {
	// These paths all reject before any client is constructed, so no env / box
	// is needed; the important property is that they are usageError (exit 1).
	cases := []struct {
		ctx  string
		fn   func([]string) error
		args []string
	}{
		{"web with no subcommand", runWeb, nil},
		{"web unknown subcommand", runWeb, []string{"frobnicate"}},
		{"deploy without dir", webDeploy, nil},
		{"deploy with extra arg", webDeploy, []string{"a", "b"}},
		{"list with extra arg", webList, []string{"oops"}},
		{"rm without site", webRm, nil},
		{"rm invalid site name", webRm, []string{"-badname"}},
		{"domain with no subcommand", webDomain, nil},
		{"domain unknown subcommand", webDomain, []string{"twiddle"}},
		{"domain add wrong arity", webDomain, []string{"add", "site-only"}},
		{"domain add invalid site", webDomain, []string{"add", "Bad_Site", "example.com"}},
		{"domain status wrong arity", webDomain, []string{"status", "site"}},
		{"domain rm wrong arity", webDomain, []string{"rm", "site"}},
	}
	for _, tc := range cases {
		wtAssertUsage(t, tc.fn(tc.args), tc.ctx)
	}
}

func TestDeployMissingDirIsOperationError(t *testing.T) {
	// A non-existent dir is an operation error (exit 2), not a usage error: it
	// fails the os.Stat, not the arg check.
	err := webDeploy([]string{filepath.Join(t.TempDir(), "does-not-exist")})
	if err == nil {
		t.Fatalf("expected error for missing dir")
	}
	if errors.As(err, &usageError{}) {
		t.Fatalf("missing dir should be an operation error, got usageError: %v", err)
	}
}

func TestDispatchRoutesToSubcommands(t *testing.T) {
	// runWeb should route to the right handler; we detect routing by the
	// distinctive usage string each handler emits on an arity error.
	if err := runWeb([]string{"rm"}); err == nil || !strings.Contains(err.Error(), "vulos web rm") {
		t.Errorf("runWeb rm did not route to webRm: %v", err)
	}
	if err := runWeb([]string{"list", "extra"}); err == nil || !strings.Contains(err.Error(), "vulos web list") {
		t.Errorf("runWeb list did not route to webList: %v", err)
	}
	if err := runWeb([]string{"domain"}); err == nil || !strings.Contains(err.Error(), "vulos web domain") {
		t.Errorf("runWeb domain did not route to webDomain: %v", err)
	}
}

// ---- name helpers ----------------------------------------------------------

func TestValidSiteName(t *testing.T) {
	valid := []string{"a", "my-site", "site1", "a-b-c", strings.Repeat("x", 63)}
	invalid := []string{"", "-foo", "foo-", "Foo", "foo_bar", "foo.bar", strings.Repeat("x", 64)}
	for _, s := range valid {
		if !validSiteName(s) {
			t.Errorf("validSiteName(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if validSiteName(s) {
			t.Errorf("validSiteName(%q) = true, want false", s)
		}
	}
}

func TestDeriveSiteName(t *testing.T) {
	cases := map[string]string{
		"/foo/bar/My_Site": "my-site",
		"dist":             "dist",
		"/tmp/Cool.Site":   "cool-site",
		"--weird--":        "weird",
	}
	for in, want := range cases {
		if got := deriveSiteName(in); got != want {
			t.Errorf("deriveSiteName(%q) = %q, want %q", in, got, want)
		}
	}
}
