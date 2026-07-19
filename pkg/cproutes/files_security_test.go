// routes_files_security_test.go — the boundaries around the cloud Drive.
//
// Files is the ONE surface that spans an account's whole bucket rather than a
// single app's prefix, so its gates matter more than any other storage route's:
//
//   - only a real user SESSION may drive it (an app-identity token — the
//     credential a proxied app backend such as Office or Board holds — must
//     not);
//   - identity comes from the session and nothing else (no X-User-ID header);
//   - a node belongs to exactly one owner, and nobody else can list, read, move,
//     delete or version it, nor have a grant minted for it;
//   - the "<account>/drive/" prefix is unreachable from /api/storage/presign,
//     whose app-scoped keys always live under "<account>/<app>/".
package cproutes

// COORDINATOR: depends on the storage test harness (storageTestEnv,
// newStorageTestEnv / newStorageEnvWith, sessionFor, byoStatusResponse) defined
// in routes_storage_test.go — that harness (and its call to RegisterFiles /
// RegisterAccountExport) must land in package cproutes for this test to compile.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/apptoken"
	"github.com/vul-os/vulos-management/pkg/auth"
)

// filesRoute is one /api/files/* endpoint, with a body good enough to get past
// decoding — every one of these must be refused before it is ever decoded.
type filesRoute struct {
	method string
	path   string
	body   any
}

func allFilesRoutes() []filesRoute {
	return []filesRoute{
		{http.MethodGet, "/api/files/list?parent=", nil},
		{http.MethodPost, "/api/files/folder", map[string]any{"parent_id": "", "name": "x"}},
		{http.MethodPost, "/api/files/move", map[string]any{"node_id": "x", "new_name": "y"}},
		{http.MethodPost, "/api/files/delete", map[string]any{"node_id": "x"}},
		{http.MethodPost, "/api/files/upload-grant", map[string]any{"parent_id": "", "name": "x.txt"}},
		{http.MethodPost, "/api/files/commit", map[string]any{"node_id": "x", "size": 1}},
		{http.MethodPost, "/api/files/download-grant", map[string]any{"node_id": "x"}},
		{http.MethodGet, "/api/files/versions?node=x", nil},
	}
}

// doRaw issues a request with arbitrary headers (the env's own do() only knows
// how to attach a session cookie).
func doRaw(t *testing.T, e *storageTestEnv, r filesRoute, headers map[string]string, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if r.body != nil {
		if err := json.NewEncoder(&buf).Encode(r.body); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	req := httptest.NewRequest(r.method, r.path, &buf)
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w
}

// filesAppTokenEnv is a Files env with an app-token signing key configured, so a
// token can be minted exactly the way the reverse proxy mints one.
func filesAppTokenEnv(t *testing.T) (*storageTestEnv, string, string) {
	t.Helper()
	t.Setenv("CP_SHARED_SECRET", "files-apptoken-test-secret")
	e := newFilesTestEnv(t)
	uid, tok := filesUser(t, e, "owner")
	initAppIdentity(e.authSt)
	return e, uid, tok
}

// ---------------------------------------------------------------------------
// Credential class
// ---------------------------------------------------------------------------

// SECURITY-C1. The reverse proxy hands each app backend an audience-bound
// app-identity token IN THE vc_session COOKIE (back-compat). RequireSession
// rejects that class outright — which is what stops a compromised app from
// walking the user's whole Drive, across every app's prefix, by replaying the
// credential it was legitimately given. Files deliberately does NOT use
// storageCaller (routes_storage.go), which accepts app tokens by design.
func TestFilesSecurity_AppTokenIsNotASession(t *testing.T) {
	e, uid, _ := filesAppTokenEnv(t)
	appTok := appTokenFor(t, uid, "files")

	for _, r := range allFilesRoutes() {
		// In the cookie, exactly as the proxy forwards it.
		w := doRaw(t, e, r, nil, appTok)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s with an app token in vc_session: status %d, want 403 (%s)",
				r.method, r.path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "url") {
			t.Errorf("%s %s minted a grant for an app token", r.method, r.path)
		}
		// And in the forward-looking header, with no session at all.
		w = doRaw(t, e, r, map[string]string{apptoken.HeaderName: appTok}, "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with an app token in %s: status %d, want 401 (%s)",
				r.method, r.path, apptoken.HeaderName, w.Code, w.Body.String())
		}
	}
}

func TestFilesSecurity_NoSessionIsUnauthorized(t *testing.T) {
	e := newFilesTestEnv(t)
	for _, r := range allFilesRoutes() {
		w := doRaw(t, e, r, nil, "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s unauthenticated: status %d, want 401", r.method, r.path, w.Code)
		}
	}
}

// The OS Files control plane reads X-User-ID because its own middleware injects
// it. On the CP that header is attacker-controlled and is never read: identity
// is the session, always.
func TestFilesSecurity_UserIDHeaderIsIgnored(t *testing.T) {
	e := newFilesTestEnv(t)
	victimID, victimTok := filesUser(t, e, "victim")
	_, attackerTok := filesUser(t, e, "attacker")
	victimFolder := createFolderThroughAPI(t, e, victimTok, "", "victim-docs")

	// The attacker's own session, claiming to be the victim.
	w := doRaw(t, e, filesRoute{http.MethodGet, "/api/files/list?parent=", nil},
		map[string]string{"X-User-ID": victimID}, attackerTok)
	if w.Code != http.StatusOK {
		t.Fatalf("list: status %d: %s", w.Code, w.Body.String())
	}
	if got := strings.TrimSpace(w.Body.String()); got != `{"nodes":[]}` {
		t.Errorf("X-User-ID was honoured — the attacker saw the victim's root: %s", got)
	}

	// And it cannot be used to reach a specific node either.
	w = doRaw(t, e, filesRoute{http.MethodGet, "/api/files/list?parent=" + victimFolder.ID, nil},
		map[string]string{"X-User-ID": victimID}, attackerTok)
	if w.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403: %s", w.Code, w.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Cross-account isolation
// ---------------------------------------------------------------------------

func TestFilesSecurity_CrossUserNodeIsUnreachable(t *testing.T) {
	e := newFilesTestEnv(t)
	_, victimTok := filesUser(t, e, "victim")
	_, attackerTok := filesUser(t, e, "attacker")

	victimFolder := createFolderThroughAPI(t, e, victimTok, "", "private")
	victimFile, _ := uploadThroughAPI(t, e, victimTok, victimFolder.ID, "secret.txt", "text/plain", "S")

	for _, r := range []filesRoute{
		{http.MethodGet, "/api/files/list?parent=" + victimFolder.ID, nil},
		{http.MethodPost, "/api/files/download-grant", map[string]any{"node_id": victimFile.ID}},
		{http.MethodPost, "/api/files/move", map[string]any{"node_id": victimFile.ID, "new_name": "mine.txt"}},
		{http.MethodPost, "/api/files/delete", map[string]any{"node_id": victimFile.ID}},
		{http.MethodPost, "/api/files/commit", map[string]any{"node_id": victimFile.ID, "size": 0}},
		{http.MethodGet, "/api/files/versions?node=" + victimFile.ID, nil},
		{http.MethodPost, "/api/files/upload-grant", map[string]any{"parent_id": victimFolder.ID, "name": "planted.txt"}},
	} {
		w := doRaw(t, e, r, nil, attackerTok)
		if w.Code != http.StatusForbidden && w.Code != http.StatusNotFound {
			t.Errorf("%s %s: status %d, want 403/404 (%s)", r.method, r.path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), `"url"`) {
			t.Errorf("%s %s minted a grant for another account's node: %s", r.method, r.path, w.Body.String())
		}
	}

	// The victim's data is intact and still theirs.
	if nodes := listThroughAPI(t, e, victimTok, victimFolder.ID); len(nodes) != 1 || nodes[0].ID != victimFile.ID {
		t.Errorf("the victim's folder was altered: %+v", nodes)
	}
}

// The Drive root is a shared parent_id (”) across every account, so the root
// listing has to be owner-filtered — in SQL, not after the fact.
func TestFilesSecurity_RootListingDoesNotLeakAnotherOwner(t *testing.T) {
	e := newFilesTestEnv(t)
	_, victimTok := filesUser(t, e, "victim")
	_, attackerTok := filesUser(t, e, "attacker")

	createFolderThroughAPI(t, e, victimTok, "", "victim-only")
	uploadThroughAPI(t, e, victimTok, "", "victim.txt", "text/plain", "V")
	createFolderThroughAPI(t, e, attackerTok, "", "attacker-only")

	nodes := listThroughAPI(t, e, attackerTok, "")
	if len(nodes) != 1 || nodes[0].Name != "attacker-only" {
		t.Fatalf("root listing = %+v, want only the caller's own node", nodes)
	}
}

// ---------------------------------------------------------------------------
// Prefix boundary: the Drive is not an app prefix
// ---------------------------------------------------------------------------

// "<account>/drive/" is its OWN prefix. /api/storage/presign binds an app
// token's key to "<account>/<app>/", so no app token — not even the `files`
// audience the Files app itself would hold — can presign a Drive object through
// the storage gateway and bypass the Files control plane's ownership checks.
func TestFilesSecurity_AppTokenCannotPresignTheDrivePrefix(t *testing.T) {
	e, uid, _ := filesAppTokenEnv(t)
	bucket := "vulos-" + strings.ToLower(boxULID(uid))

	for _, aud := range []string{"files", "os"} {
		tok := appTokenFor(t, uid, aud)
		for _, path := range []string{"/api/storage/presign/get", "/api/storage/presign/put"} {
			w := e.do(t, http.MethodPost, path, map[string]any{
				"account_id": uid,
				"bucket":     bucket,
				"key":        uid + "/drive/secret.txt",
			}, tok)
			if w.Code != http.StatusForbidden {
				t.Errorf("aud=%s %s: status %d, want 403 (%s)", aud, path, w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "http") {
				t.Errorf("aud=%s %s: a URL was minted for a Drive key: %s", aud, path, w.Body.String())
			}
		}
	}
}

// keyWithinDrivePrefix is the last line of defence before a grant is minted: it
// asserts the (server-derived) key really does live under "<owner>/drive/".
func TestFilesSecurity_KeyWithinDrivePrefix(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"u1/drive/report.txt", true},
		{"u1/drive/docs/report.txt", true},
		{"u1/drive/", false},            // the prefix itself is not an object
		{"u1/drive/../office/x", false}, // no traversal out of the Drive
		{"u1/office/x", false},
		{"u2/drive/report.txt", false}, // another owner's Drive
		{"drive/report.txt", false},
		{"", false},
	} {
		if got := keyWithinDrivePrefix(tc.key, "u1"); got != tc.want {
			t.Errorf("keyWithinDrivePrefix(%q, u1) = %v, want %v", tc.key, got, tc.want)
		}
	}
}
