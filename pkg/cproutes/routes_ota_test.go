package cproutes

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/ota"
)

// otaSign returns the X-Ota-Sig value for a ulid: HMAC-SHA256(secret, ulid) hex.
func otaSign(secret, ulid string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ulid))
	return hex.EncodeToString(mac.Sum(nil))
}

// otaPostHdr is otaPost with extra request headers (e.g. X-Ota-Sig).
func otaPostHdr(t *testing.T, mux *http.ServeMux, path string, body any, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// otaTestMux returns a mux wired with OTA routes, a MemStore, a MemSigner,
// and no admin (admin endpoints will 503 without ADMIN_ACCOUNT_ID).
// Use otaTestMuxWithAdmin for admin-enabled tests.
func otaTestMux(t *testing.T) (*http.ServeMux, *ota.MemStore, *ota.MemSigner) {
	t.Helper()
	st := ota.NewMemStore()
	signer := ota.NewMemSigner()
	mux := http.NewServeMux()
	// No auth store, no admin, no fleet store — admin routes will 503 (adminID="").
	registerOTARoutes(mux, st, signer, nil, "")
	return mux, st, signer
}

// otaAdminMux builds a mux that allows admin requests using a fake admin user
// by wiring a real auth store and setting adminAccountID.
// For simplicity in tests we use a tiny fake auth middleware.
func otaDeviceMux(t *testing.T) (*http.ServeMux, *ota.MemStore, *ota.MemSigner) {
	t.Helper()
	return otaTestMux(t)
}

func otaPost(t *testing.T, mux *http.ServeMux, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func otaGet(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

const otaTestULID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

// buildSignedRelease constructs a release request body signed with signer.
func buildSignedRelease(t *testing.T, signer *ota.MemSigner, version, channel string) map[string]any {
	t.Helper()
	// Build the manifest that will be signed (matches marshalManifestForSig).
	m := ota.Manifest{
		Version:     version,
		ArtifactURL: "https://cdn.example.com/v" + version + ".tar.gz",
		Sha256:      "deadbeef" + version,
		Security:    false,
		DeferMaxSec: 604800,
	}
	mJSON, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	sig, err := signer.Sign(mJSON)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return map[string]any{
		"version":           version,
		"channel":           channel,
		"artifact_url":      m.ArtifactURL,
		"sha256":            m.Sha256,
		"min_from":          "1.0.0",
		"security":          false,
		"rollout_pct":       100,
		"signature":         sig,
		"defer_max_seconds": 604800,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/ota/feed
// ─────────────────────────────────────────────────────────────────────────────

func TestOTAFeed_NoUpgrade(t *testing.T) {
	mux, _, _ := otaDeviceMux(t)
	// No releases inserted — feed must return {} with 200.
	rr := otaGet(t, mux, "/api/ota/feed?ulid="+otaTestULID+"&channel=stable&version=1.0.0")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body)
	}
	var out map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("want empty object, got %v", out)
	}
}

func TestOTAFeed_MissingParams(t *testing.T) {
	mux, _, _ := otaDeviceMux(t)
	// Missing version param.
	rr := otaGet(t, mux, "/api/ota/feed?ulid="+otaTestULID+"&channel=stable")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body)
	}
}

func TestOTAFeed_InvalidULID(t *testing.T) {
	mux, _, _ := otaDeviceMux(t)
	rr := otaGet(t, mux, "/api/ota/feed?ulid=NOTAULID&channel=stable&version=1.0.0")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body)
	}
}

func TestOTAFeed_InvalidChannel(t *testing.T) {
	mux, _, _ := otaDeviceMux(t)
	rr := otaGet(t, mux, "/api/ota/feed?ulid="+otaTestULID+"&channel=nightly&version=1.0.0")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/ota/release (admin-only — without auth, expects 503 because adminID="")
// ─────────────────────────────────────────────────────────────────────────────

func TestOTARelease_NoAdminID_503(t *testing.T) {
	mux, _, signer := otaTestMux(t)
	body := buildSignedRelease(t, signer, "1.2.0", "stable")
	rr := otaPost(t, mux, "/api/ota/release", body)
	// adminID="" → signer503 fires → 503
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (no admin configured), got %d: %s", rr.Code, rr.Body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/ota/report
// ─────────────────────────────────────────────────────────────────────────────

func TestOTAReport_HappyPath(t *testing.T) {
	const secret = "ota-device-secret"
	t.Setenv("DEVICE_SHARED_SECRET", secret)
	mux, _, _ := otaDeviceMux(t)
	rr := otaPostHdr(t, mux, "/api/ota/report", map[string]any{
		"ulid":    otaTestULID,
		"version": "1.2.0",
		"result":  "applied",
	}, map[string]string{"X-Ota-Sig": otaSign(secret, otaTestULID)})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rr.Code, rr.Body)
	}
}

// TestOTAReport_NoSignature_Unauthorized verifies the report route now requires
// a valid device signature (audit H3).
func TestOTAReport_NoSignature_Unauthorized(t *testing.T) {
	t.Setenv("DEVICE_SHARED_SECRET", "ota-device-secret")
	mux, _, _ := otaDeviceMux(t)
	rr := otaPost(t, mux, "/api/ota/report", map[string]any{
		"ulid":    otaTestULID,
		"version": "1.2.0",
		"result":  "applied",
	})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("missing sig: want 401, got %d: %s", rr.Code, rr.Body)
	}
}

// TestOTAReport_NoSecret_FailsClosed verifies the route rejects when no shared
// secret is configured (fail-closed; audit H3).
func TestOTAReport_NoSecret_FailsClosed(t *testing.T) {
	t.Setenv("DEVICE_SHARED_SECRET", "")
	mux, _, _ := otaDeviceMux(t)
	rr := otaPost(t, mux, "/api/ota/report", map[string]any{
		"ulid":    otaTestULID,
		"version": "1.2.0",
		"result":  "applied",
	})
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("no secret: want 503, got %d: %s", rr.Code, rr.Body)
	}
}

func TestOTAReport_InvalidResult(t *testing.T) {
	mux, _, _ := otaDeviceMux(t)
	rr := otaPost(t, mux, "/api/ota/report", map[string]any{
		"ulid":    otaTestULID,
		"version": "1.2.0",
		"result":  "success", // invalid
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body)
	}
}

func TestOTAReport_MissingFields(t *testing.T) {
	mux, _, _ := otaDeviceMux(t)
	rr := otaPost(t, mux, "/api/ota/report", map[string]any{
		"ulid": otaTestULID,
		// missing version and result
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body)
	}
}

func TestOTAReport_InvalidULID(t *testing.T) {
	mux, _, _ := otaDeviceMux(t)
	rr := otaPost(t, mux, "/api/ota/report", map[string]any{
		"ulid":    "NOTVALID",
		"version": "1.0.0",
		"result":  "applied",
	})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// End-to-end: direct store insertion → feed
// ─────────────────────────────────────────────────────────────────────────────

// TestOTAFeedE2E inserts a release directly into the MemStore (bypassing admin
// auth which requires a real auth store) and then checks that the feed returns it.
func TestOTAFeedE2E(t *testing.T) {
	mux, st, signer := otaDeviceMux(t)

	// Build signed manifest.
	m := ota.Manifest{
		Version:     "1.5.0",
		ArtifactURL: "https://cdn.example.com/v1.5.0.tar.gz",
		Sha256:      "sha256ofartifact",
		Security:    false,
		DeferMaxSec: 604800,
	}
	mJSON, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sig, err := signer.Sign(mJSON)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// Insert directly via store.
	ctx := t.Context()
	id, err := st.InsertRelease(ctx, ota.Release{
		Version:      "1.5.0",
		Channel:      "stable",
		ArtifactURL:  m.ArtifactURL,
		Sha256:       m.Sha256,
		MinFrom:      "1.0.0",
		Security:     false,
		RolloutPct:   100,
		SignatureB64: sig,
		DeferMaxSec:  604800,
	})
	if err != nil {
		t.Fatalf("InsertRelease: %v", err)
	}
	if id <= 0 {
		t.Fatalf("InsertRelease returned id %d", id)
	}

	// Feed must return the release.
	rr := otaGet(t, mux, "/api/ota/feed?ulid="+otaTestULID+"&channel=stable&version=1.0.0")
	if rr.Code != http.StatusOK {
		t.Fatalf("feed: want 200, got %d: %s", rr.Code, rr.Body)
	}
	var manifest ota.Manifest
	if err := json.NewDecoder(rr.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.Version != "1.5.0" {
		t.Errorf("manifest.Version = %q; want 1.5.0", manifest.Version)
	}
	if manifest.Sha256 != m.Sha256 {
		t.Errorf("manifest.Sha256 = %q; want %q", manifest.Sha256, m.Sha256)
	}
	// Signature must match the one we put in.
	if manifest.SignatureB64 != sig {
		t.Errorf("manifest.SignatureB64 mismatch")
	}
}

// TestOTAFeedE2E_HaltedRelease inserts a release, halts it, verifies feed returns {}.
func TestOTAFeedE2E_HaltedRelease(t *testing.T) {
	_, st, signer := otaDeviceMux(t)

	m := ota.Manifest{Version: "1.6.0", ArtifactURL: "https://cdn.example.com/v1.6.0.tar.gz", Sha256: "sha256", Security: false}
	mJSON, _ := json.Marshal(m)
	sig, _ := signer.Sign(mJSON)

	ctx := t.Context()
	id, err := st.InsertRelease(ctx, ota.Release{
		Version: "1.6.0", Channel: "stable", ArtifactURL: m.ArtifactURL,
		Sha256: m.Sha256, MinFrom: "1.0.0", RolloutPct: 100,
		SignatureB64: sig, DeferMaxSec: 604800,
	})
	if err != nil {
		t.Fatalf("InsertRelease: %v", err)
	}
	if err := st.HaltRelease(ctx, id); err != nil {
		t.Fatalf("HaltRelease: %v", err)
	}

	// Build fresh mux with the same store.
	mux := http.NewServeMux()
	registerOTARoutes(mux, st, signer, nil, "")

	rr := otaGet(t, mux, "/api/ota/feed?ulid="+otaTestULID+"&channel=stable&version=1.0.0")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body)
	}
	var out map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("halted release should yield {}; got %v", out)
	}
}

// TestOTAFeedE2E_SignaturePreserved confirms that the signature stored with a
// release is faithfully returned in the feed manifest (it is the admin's
// responsibility to sign the manifest before ingest; the feed just echoes it).
// The test verifies the round-trip by signing, storing, fetching, and verifying.
func TestOTAFeedE2E_SignaturePreserved(t *testing.T) {
	_, st, signer := otaDeviceMux(t)

	// Build the manifest that will be signed. DeferMaxSec=0 is omitted by
	// `omitempty`, so the signed bytes are the minimal form. The admin signs
	// this form; the feed echoes the stored signature and the stored DeferMaxSec
	// (604800). The client must verify the stored signature against the SAME
	// canonical form the admin signed — not against the feed manifest JSON directly.
	const version = "1.7.0"
	m := ota.Manifest{
		Version:     version,
		ArtifactURL: "https://cdn.example.com/v1.7.0.tar.gz",
		Sha256:      "sha256v17",
		Security:    false,
		// DeferMaxSec intentionally 0 (omitted in JSON) — this is the signed form.
	}
	mJSON, _ := json.Marshal(m)
	sig, _ := signer.Sign(mJSON)

	ctx := t.Context()
	if _, err := st.InsertRelease(ctx, ota.Release{
		Version: version, Channel: "stable", ArtifactURL: m.ArtifactURL,
		Sha256: m.Sha256, MinFrom: "1.0.0", RolloutPct: 100,
		SignatureB64: sig, DeferMaxSec: 604800,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	mux := http.NewServeMux()
	registerOTARoutes(mux, st, signer, nil, "")

	rr := otaGet(t, mux, "/api/ota/feed?ulid="+otaTestULID+"&channel=stable&version=1.0.0")
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	var manifest ota.Manifest
	if err := json.NewDecoder(rr.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if manifest.Version != version {
		t.Fatalf("version = %q; want %q", manifest.Version, version)
	}
	// The stored signature is present in the response.
	if manifest.SignatureB64 != sig {
		t.Fatalf("SignatureB64 mismatch: stored %q, feed returned %q", sig, manifest.SignatureB64)
	}
	// Verify the signature against the same canonical form the admin signed.
	// (The admin signs the manifest without DeferMaxSec; the field is informational
	// to the client for grace-window enforcement.)
	canonForm := ota.Manifest{
		Version:     manifest.Version,
		ArtifactURL: manifest.ArtifactURL,
		Sha256:      manifest.Sha256,
		Security:    manifest.Security,
		// DeferMaxSec: 0 — omitted, matching the signed form above.
	}
	canonJSON, _ := json.Marshal(canonForm)
	if err := signer.Verify(canonJSON, manifest.SignatureB64); err != nil {
		t.Fatalf("signature verify on canonical manifest form: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Admin mux helper for dashboard endpoint tests
// ─────────────────────────────────────────────────────────────────────────────

const otaAdminID = "admin-account-0001"

// otaAdminMuxFull builds a mux wired with OTA routes, a real auth store with
// an admin user pre-seeded, and a MemStore + MemSigner. Returns the mux, store,
// signer, and admin session token.
func otaAdminMuxFull(t *testing.T) (*http.ServeMux, *ota.MemStore, *ota.MemSigner, string) {
	t.Helper()
	authStore, err := openAuthStoreForTest("file::memory:?_pragma=journal_mode(WAL)", []byte("ota-test-secret"))
	if err != nil {
		t.Fatalf("OpenAuthStore: %v", err)
	}
	t.Cleanup(func() { authStore.Close() })

	ctx := context.Background()
	u, token, err := authStore.Signup(ctx, "otaadmin@example.com", "password-1234", "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}
	// The adminAccountID must match the created user's ID.
	adminID := u.ID

	st := ota.NewMemStore()
	signer := ota.NewMemSigner()
	mux := http.NewServeMux()
	registerOTARoutes(mux, st, signer, authStore, adminID)
	return mux, st, signer, token
}

// otaGetAdmin sends a GET with an admin session cookie.
func otaGetAdmin(t *testing.T, mux *http.ServeMux, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/ota/releases — paginated list
// ─────────────────────────────────────────────────────────────────────────────

func TestOTAReleases_ReturnsEmptyList(t *testing.T) {
	mux, _, _, token := otaAdminMuxFull(t)
	rr := otaGetAdmin(t, mux, "/api/ota/releases", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body)
	}
	var out map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	releases, ok := out["releases"]
	if !ok {
		t.Fatal("response missing 'releases' key")
	}
	list, ok := releases.([]any)
	if !ok {
		t.Fatalf("releases should be array, got %T", releases)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d items", len(list))
	}
}

func TestOTAReleases_PaginatedList(t *testing.T) {
	mux, st, signer, token := otaAdminMuxFull(t)
	ctx := context.Background()

	// Insert 3 releases.
	for i, ver := range []string{"1.0.0", "1.1.0", "1.2.0"} {
		m := ota.Manifest{Version: ver, ArtifactURL: "https://example.com/v" + ver, Sha256: "sha-" + ver, Security: false}
		mJSON, _ := json.Marshal(m)
		sig, _ := signer.Sign(mJSON)
		_, err := st.InsertRelease(ctx, ota.Release{
			Version: ver, Channel: "stable", ArtifactURL: m.ArtifactURL,
			Sha256: m.Sha256, MinFrom: "0.9.0", RolloutPct: 100, SignatureB64: sig, DeferMaxSec: 604800,
		})
		if err != nil {
			t.Fatalf("InsertRelease[%d]: %v", i, err)
		}
	}

	// Default list — should return all 3.
	rr := otaGetAdmin(t, mux, "/api/ota/releases", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body)
	}
	var out map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	list := out["releases"].([]any)
	if len(list) != 3 {
		t.Errorf("expected 3 releases, got %d", len(list))
	}

	// Paginate: limit=2 offset=0 → first 2.
	rr2 := otaGetAdmin(t, mux, "/api/ota/releases?limit=2&offset=0", token)
	if rr2.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr2.Code, rr2.Body)
	}
	var out2 map[string]any
	if err := json.NewDecoder(rr2.Body).Decode(&out2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	list2 := out2["releases"].([]any)
	if len(list2) != 2 {
		t.Errorf("page 1 expected 2 releases, got %d", len(list2))
	}

	// Paginate: limit=2 offset=2 → last 1.
	rr3 := otaGetAdmin(t, mux, "/api/ota/releases?limit=2&offset=2", token)
	if rr3.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr3.Code, rr3.Body)
	}
	var out3 map[string]any
	if err := json.NewDecoder(rr3.Body).Decode(&out3); err != nil {
		t.Fatalf("decode: %v", err)
	}
	list3 := out3["releases"].([]any)
	if len(list3) != 1 {
		t.Errorf("page 2 expected 1 release, got %d", len(list3))
	}
}

func TestOTAReleases_ResponseShape(t *testing.T) {
	mux, st, signer, token := otaAdminMuxFull(t)
	ctx := context.Background()

	m := ota.Manifest{Version: "2.0.0", ArtifactURL: "https://example.com/v2", Sha256: "sha2", Security: false}
	mJSON, _ := json.Marshal(m)
	sig, _ := signer.Sign(mJSON)
	_, err := st.InsertRelease(ctx, ota.Release{
		Version: "2.0.0", Channel: "beta", ArtifactURL: m.ArtifactURL,
		Sha256: m.Sha256, MinFrom: "1.0.0", RolloutPct: 50, SignatureB64: sig, DeferMaxSec: 604800,
	})
	if err != nil {
		t.Fatalf("InsertRelease: %v", err)
	}

	rr := otaGetAdmin(t, mux, "/api/ota/releases", token)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body)
	}
	var out map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	list := out["releases"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected 1 release, got %d", len(list))
	}
	rel := list[0].(map[string]any)
	if rel["version"] != "2.0.0" {
		t.Errorf("version = %v; want 2.0.0", rel["version"])
	}
	if rel["channel"] != "beta" {
		t.Errorf("channel = %v; want beta", rel["channel"])
	}
	if rel["rollout_pct"] != float64(50) {
		t.Errorf("rollout_pct = %v; want 50", rel["rollout_pct"])
	}
	if rel["halted"] != false {
		t.Errorf("halted = %v; want false", rel["halted"])
	}
	if _, ok := rel["id"]; !ok {
		t.Error("response missing 'id' field")
	}
	if _, ok := rel["created_at"]; !ok {
		t.Error("response missing 'created_at' field")
	}
}

func TestOTAReleases_403ForNonAdmin(t *testing.T) {
	// No auth configured — admin gate returns 503 (no admin ID set).
	// To get a real 403, we need an auth store with a non-admin user.
	authStore, err := openAuthStoreForTest("file::memory:?_pragma=journal_mode(WAL)", []byte("secret"))
	if err != nil {
		t.Fatalf("OpenAuthStore: %v", err)
	}
	t.Cleanup(func() { authStore.Close() })

	ctx := context.Background()
	_, token, err := authStore.Signup(ctx, "notadmin@example.com", "password-1234", "127.0.0.1", "agent")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	st := ota.NewMemStore()
	signer := ota.NewMemSigner()
	mux := http.NewServeMux()
	// Different adminAccountID from the signed-up user → caller is not admin → 403.
	registerOTARoutes(mux, st, signer, authStore, otaAdminID)

	rr := otaGetAdmin(t, mux, "/api/ota/releases", token)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", rr.Code, rr.Body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/ota/release/{id}/adoption
// ─────────────────────────────────────────────────────────────────────────────

func TestOTAAdoption_CountBreakdown(t *testing.T) {
	mux, st, signer, token := otaAdminMuxFull(t)
	ctx := context.Background()

	// Insert a release.
	m := ota.Manifest{Version: "1.5.0", ArtifactURL: "https://cdn.example.com/v1.5.0", Sha256: "sha15", Security: false}
	mJSON, _ := json.Marshal(m)
	sig, _ := signer.Sign(mJSON)
	releaseID, err := st.InsertRelease(ctx, ota.Release{
		Version: "1.5.0", Channel: "stable", ArtifactURL: m.ArtifactURL,
		Sha256: m.Sha256, MinFrom: "1.0.0", RolloutPct: 100, SignatureB64: sig, DeferMaxSec: 604800,
	})
	if err != nil {
		t.Fatalf("InsertRelease: %v", err)
	}

	// Insert device reports for this version.
	if err := st.InsertReport(ctx, otaTestULID, "1.5.0", "applied"); err != nil {
		t.Fatalf("InsertReport applied: %v", err)
	}
	if err := st.InsertReport(ctx, "01BX5ZZKBKACTAV9WEVGEMMVRY", "1.5.0", "failed"); err != nil {
		t.Fatalf("InsertReport failed: %v", err)
	}
	if err := st.InsertReport(ctx, "01C3XK7BMTQRPD2FGVTCH8QN5A", "1.5.0", "rolled-back"); err != nil {
		t.Fatalf("InsertReport rolled-back: %v", err)
	}
	// Report for a different version — should NOT appear in counts.
	if err := st.InsertReport(ctx, "01D4YL8CNUTSQE3GHWUDI9RO6B", "1.4.0", "applied"); err != nil {
		t.Fatalf("InsertReport other version: %v", err)
	}

	path := "/api/ota/release/" + itoa64(releaseID) + "/adoption"
	rr := otaGetAdmin(t, mux, path, token)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body)
	}
	var out map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Expect 3 devices (3 distinct ULIDs reported for version 1.5.0).
	if out["total_devices"] != float64(3) {
		t.Errorf("total_devices = %v; want 3", out["total_devices"])
	}
	if out["applied"] != float64(1) {
		t.Errorf("applied = %v; want 1", out["applied"])
	}
	if out["failed"] != float64(1) {
		t.Errorf("failed = %v; want 1", out["failed"])
	}
	if out["rolled_back"] != float64(1) {
		t.Errorf("rolled_back = %v; want 1", out["rolled_back"])
	}
}

func TestOTAAdoption_NotFound(t *testing.T) {
	mux, _, _, token := otaAdminMuxFull(t)
	rr := otaGetAdmin(t, mux, "/api/ota/release/99999/adoption", token)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rr.Code, rr.Body)
	}
}

func TestOTAAdoption_InvalidID(t *testing.T) {
	mux, _, _, token := otaAdminMuxFull(t)
	rr := otaGetAdmin(t, mux, "/api/ota/release/notanid/adoption", token)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", rr.Code, rr.Body)
	}
}

func TestOTAAdoption_403ForNonAdmin(t *testing.T) {
	authStore, err := openAuthStoreForTest("file::memory:?_pragma=journal_mode(WAL)", []byte("secret2"))
	if err != nil {
		t.Fatalf("OpenAuthStore: %v", err)
	}
	t.Cleanup(func() { authStore.Close() })

	ctx := context.Background()
	_, token, err := authStore.Signup(ctx, "notadmin2@example.com", "password-1234", "127.0.0.1", "agent")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	st := ota.NewMemStore()
	signer := ota.NewMemSigner()
	mux := http.NewServeMux()
	registerOTARoutes(mux, st, signer, authStore, otaAdminID)

	rr := otaGetAdmin(t, mux, "/api/ota/release/1/adoption", token)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", rr.Code, rr.Body)
	}
}

// itoa64 converts int64 to decimal string.
func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
