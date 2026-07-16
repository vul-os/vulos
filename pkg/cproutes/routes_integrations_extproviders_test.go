// routes_integrations_extproviders_test.go — EXT-PROVIDERS: Drive WRITE scope,
// the GCS mint alias, and the Dropbox provider. Uses in-memory stores + fake
// Exchangers; no real Google/Dropbox calls.
package cproutes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/integrations"
)

// driveWriteToken is a connected Google account whose grant includes Drive write.
func driveWriteToken() *integrations.Token {
	return &integrations.Token{
		AccessToken:  "access-drive-write",
		RefreshToken: "refresh-1",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       "openid email " + integrations.ScopeDriveFile,
		Email:        "u@gmail.com",
		Sub:          "sub-1",
	}
}

// gcsToken is a connected Google account whose grant includes GCS read/write.
func gcsToken() *integrations.Token {
	return &integrations.Token{
		AccessToken:  "access-gcs",
		RefreshToken: "refresh-1",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       "openid email " + integrations.ScopeDevstorageReadWrite,
		Email:        "u@gmail.com",
		Sub:          "sub-1",
	}
}

// mint calls the token mint with a scope hint, authenticated by the env's
// lazily-enrolled per-device key (X-Device-Sig) — the fleet-wide HMAC is no
// longer accepted for minting.
func (e *integEnv) mint(t *testing.T, provider, scope string) *httptest.ResponseRecorder {
	t.Helper()
	priv := e.ensureDeviceKey(t)
	path := "/api/integrations/" + provider + "/token"
	if scope != "" {
		path += "?scope=" + url.QueryEscape(scope)
	}
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("X-Device-ULID", integTestULID)
	r.Header.Set("X-Device-Sig", deviceSignB64(t, priv, "integrations:token:"+provider+":"+integTestULID))
	e.mux.ServeHTTP(rr, r)
	return rr
}

// ---------------------------------------------------------------------------
// Drive WRITE
// ---------------------------------------------------------------------------

func TestMintDriveWriteGranted(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: driveWriteToken()})
	env.connect(t)

	rr := env.mint(t, "google", "drive.file")
	if rr.Code != http.StatusOK {
		t.Fatalf("drive.file granted: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	json.NewDecoder(rr.Body).Decode(&out)
	if out["access_token"] != "access-drive-write" {
		t.Fatalf("want access-drive-write, got %+v", out)
	}
	if out["required_scope"] != integrations.ScopeDriveFile {
		t.Fatalf("want required_scope=%s, got %+v", integrations.ScopeDriveFile, out["required_scope"])
	}
}

// A read-only Drive grant must NOT satisfy a write request → 403, no token.
func TestMintDriveWriteNotGrantedOnReadonly(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: driveToken()}) // drive.readonly only
	env.connect(t)

	rr := env.mint(t, "google", "drive.file")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("drive.file on readonly: want 403, got %d: %s", rr.Code, rr.Body.String())
	}
	if contains(rr.Body.String(), "access-drive") {
		t.Fatal("403 response must not contain the access token")
	}
}

// ---------------------------------------------------------------------------
// GCS alias (brokered via the Google connection)
// ---------------------------------------------------------------------------

func TestMintGCSAliasGranted(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: gcsToken()})
	env.connect(t) // connects google

	// No scope hint: the gcs alias applies the devstorage default automatically.
	rr := env.mint(t, "gcs", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("gcs granted: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	json.NewDecoder(rr.Body).Decode(&out)
	if out["access_token"] != "access-gcs" {
		t.Fatalf("want access-gcs, got %+v", out)
	}
	if out["required_scope"] != integrations.ScopeDevstorageReadWrite {
		t.Fatalf("want required_scope=%s, got %+v", integrations.ScopeDevstorageReadWrite, out["required_scope"])
	}
	if out["provider"] != "gcs" {
		t.Fatalf("want provider=gcs echoed, got %+v", out["provider"])
	}
}

// A Google grant without devstorage must fail the implicit gcs scope check → 403.
func TestMintGCSAliasNotGranted(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()}) // openid email only
	env.connect(t)

	rr := env.mint(t, "gcs", "")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("gcs not granted: want 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestMintUnknownProvider(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	env.connect(t)
	rr := env.mint(t, "bogusprov", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unknown provider: want 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Dropbox provider (connect flow + token mint)
// ---------------------------------------------------------------------------

// dropboxToken is a connected Dropbox account with file read/write scopes.
func dropboxToken() *integrations.Token {
	return &integrations.Token{
		AccessToken:  "dbx-access-1",
		RefreshToken: "dbx-refresh-1",
		Expiry:       time.Now().Add(time.Hour),
		Scopes:       integrations.ScopeDropboxContentRead + " " + integrations.ScopeDropboxContentWrite + " " + integrations.ScopeDropboxMetadataRead,
		Email:        "u@dropbox.example",
		Sub:          "dbid:1",
	}
}

// connectDropbox drives the dropbox start → callback flow against a registered
// dropbox fake exchanger.
func (e *integEnv) connectDropbox(t *testing.T) {
	t.Helper()
	rrStart := httptest.NewRecorder()
	e.mux.ServeHTTP(rrStart, e.req(http.MethodGet, "/api/integrations/dropbox/start", true))
	if rrStart.Code != http.StatusFound {
		t.Fatalf("dropbox start: want 302, got %d: %s", rrStart.Code, rrStart.Body.String())
	}
	loc, _ := url.Parse(rrStart.Header().Get("Location"))
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("dropbox start: no state in redirect")
	}
	var stateCookie *http.Cookie
	for _, c := range rrStart.Result().Cookies() {
		if c.Name == "vc_integ_state" {
			stateCookie = c
		}
	}
	if stateCookie == nil {
		t.Fatal("dropbox start: no state cookie set")
	}
	rrCb := httptest.NewRecorder()
	cb := e.req(http.MethodGet, "/api/integrations/dropbox/callback?code=dbx-code&state="+url.QueryEscape(state), true)
	cb.AddCookie(stateCookie)
	e.mux.ServeHTTP(rrCb, cb)
	if rrCb.Code != http.StatusFound {
		t.Fatalf("dropbox callback: want 302, got %d: %s", rrCb.Code, rrCb.Body.String())
	}
	if loc := rrCb.Header().Get("Location"); !contains(loc, "integration=dropbox") || !contains(loc, "status=connected") {
		t.Fatalf("dropbox callback: unexpected redirect %q", loc)
	}
}

func TestDropboxConnectStatusAndMint(t *testing.T) {
	// Google fake exchanger (default) is unused here; register a dropbox fake.
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	t.Setenv("DROPBOX_OAUTH_CLIENT_ID", "dbx-client") // makes DropboxConfigured() true
	env.svc.RegisterExchanger(integrations.ProviderDropbox, &fakeExchanger{
		exchangeTok: dropboxToken(),
		refreshTok:  dropboxToken(),
	})

	env.connectDropbox(t)

	// /status reflects the dropbox connection.
	rrSt := httptest.NewRecorder()
	env.mux.ServeHTTP(rrSt, env.req(http.MethodGet, "/api/integrations/dropbox/status", true))
	if rrSt.Code != http.StatusOK {
		t.Fatalf("dropbox status: want 200, got %d", rrSt.Code)
	}
	var st map[string]any
	json.NewDecoder(rrSt.Body).Decode(&st)
	if st["connected"] != true || st["provider"] != "dropbox" || st["email"] != "u@dropbox.example" {
		t.Fatalf("dropbox status: unexpected %+v", st)
	}

	// Token mint over the device channel returns the dropbox access token.
	rr := env.mint(t, "dropbox", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("dropbox mint: want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	json.NewDecoder(rr.Body).Decode(&out)
	if out["access_token"] != "dbx-access-1" {
		t.Fatalf("dropbox mint: want dbx-access-1, got %+v", out)
	}
	if out["provider"] != "dropbox" {
		t.Fatalf("dropbox mint: want provider=dropbox, got %+v", out["provider"])
	}

	// A write scope hint is satisfied by the granted write scope.
	rrW := env.mint(t, "dropbox", "files.write")
	if rrW.Code != http.StatusOK {
		t.Fatalf("dropbox files.write: want 200, got %d: %s", rrW.Code, rrW.Body.String())
	}
}

func TestDropboxNotConfigured(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	// DROPBOX_OAUTH_CLIENT_ID unset → start returns 503.
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, env.req(http.MethodGet, "/api/integrations/dropbox/start", true))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("dropbox not configured: want 503, got %d", rr.Code)
	}
}

// GCS has no connect flow — start must 404.
func TestGCSHasNoConnectFlow(t *testing.T) {
	env := newIntegEnv(t, &fakeExchanger{exchangeTok: validToken()})
	rr := httptest.NewRecorder()
	env.mux.ServeHTTP(rr, env.req(http.MethodGet, "/api/integrations/gcs/start", true))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("gcs start: want 404, got %d", rr.Code)
	}
}
