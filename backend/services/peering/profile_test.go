// profile_test.go — tests for profile.go (PEER-11).
//
// Test coverage:
//   - ProfileStore: NewProfileStore, Get, Update, persistence across reload
//   - AvatarETag: absent → false; present → stable until content changes
//   - profileCanViewImage: public / peers / nobody visibility levels
//   - profileNNResize: output dimensions
//   - profileEncodeVP8L: produces valid RIFF/WEBP/VP8L container
//   - profileSaveAvatar: creates avatar.webp at AvatarPath
//   - HTTP GET /api/peering/profile: returns profile JSON
//   - HTTP PUT /api/peering/profile: partial update persists
//   - HTTP POST /api/peering/profile/image: resize + write + ETag header
//   - HTTP GET /api/peering/profile/image: visibility gate, ETag caching
package peering

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// ─── Helpers ─────────────────────────────────────────────────────────────────

// profileNewTmpStore creates a ProfileStore backed by a temp directory.
func profileNewTmpStore(t *testing.T, vulosID string, contacts profileContactChecker) *ProfileStore {
	t.Helper()
	dir := t.TempDir()
	ps, err := NewProfileStore(dir, vulosID, contacts)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	return ps
}

// profileMakePNG creates a small in-memory PNG image (w×h, solid colour c).
func profileMakePNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

// profileFakeContacts is a stub profileContactChecker.
type profileFakeContacts struct {
	approved map[string]bool
}

func (f *profileFakeContacts) IsApproved(vulosID string) bool {
	return f.approved[vulosID]
}

func newProfileFakeContacts(ids ...string) *profileFakeContacts {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return &profileFakeContacts{approved: m}
}

// ─── ProfileStore tests ───────────────────────────────────────────────────────

func TestProfileStore_NewCreatesDefaults(t *testing.T) {
	const vulosID = "vulos:ed25519:testid"
	ps := profileNewTmpStore(t, vulosID, nil)

	d := ps.Get()
	if d.VulosID != vulosID {
		t.Errorf("VulosID = %q, want %q", d.VulosID, vulosID)
	}
	if d.Visibility.Image != ProfileVisPublic {
		t.Errorf("default Image visibility = %q, want %q", d.Visibility.Image, ProfileVisPublic)
	}
	if d.Visibility.Bio != ProfileVisPeers {
		t.Errorf("default Bio visibility = %q, want %q", d.Visibility.Bio, ProfileVisPeers)
	}
	if d.Visibility.Email != ProfileVisNobody {
		t.Errorf("default Email visibility = %q, want %q", d.Visibility.Email, ProfileVisNobody)
	}
}

func TestProfileStore_UpdatePersists(t *testing.T) {
	ps := profileNewTmpStore(t, "vulos:ed25519:abc", nil)

	if err := ps.Update(func(d *ProfileData) {
		d.DisplayName = "Alice"
		d.Bio = "Builder"
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Reload from disk.
	ps2, err := NewProfileStore(ps.dir, "vulos:ed25519:abc", nil)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	d := ps2.Get()
	if d.DisplayName != "Alice" {
		t.Errorf("DisplayName = %q, want Alice", d.DisplayName)
	}
	if d.Bio != "Builder" {
		t.Errorf("Bio = %q, want Builder", d.Bio)
	}
}

func TestProfileStore_VisibilityPersists(t *testing.T) {
	ps := profileNewTmpStore(t, "vulos:ed25519:vis", nil)
	if err := ps.Update(func(d *ProfileData) {
		d.Visibility.Image = ProfileVisNobody
		d.Visibility.Bio = ProfileVisPublic
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	ps2, err := NewProfileStore(ps.dir, "vulos:ed25519:vis", nil)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	d := ps2.Get()
	if d.Visibility.Image != ProfileVisNobody {
		t.Errorf("Image visibility = %q, want nobody", d.Visibility.Image)
	}
	if d.Visibility.Bio != ProfileVisPublic {
		t.Errorf("Bio visibility = %q, want public", d.Visibility.Bio)
	}
}

// ─── AvatarETag tests ─────────────────────────────────────────────────────────

func TestAvatarETag_AbsentReturnsFalse(t *testing.T) {
	ps := profileNewTmpStore(t, "vulos:ed25519:etag", nil)
	_, ok := ps.AvatarETag()
	if ok {
		t.Error("AvatarETag: want false when no avatar")
	}
}

func TestAvatarETag_StableForSameContent(t *testing.T) {
	ps := profileNewTmpStore(t, "vulos:ed25519:etag2", nil)
	pngBytes := profileMakePNG(t, 64, 64, color.RGBA{R: 200, G: 100, B: 50, A: 255})

	if err := ps.profileSaveAvatar(bytes.NewReader(pngBytes)); err != nil {
		t.Fatalf("profileSaveAvatar: %v", err)
	}
	e1, _ := ps.AvatarETag()
	e2, _ := ps.AvatarETag()
	if e1 != e2 {
		t.Errorf("ETag not stable: %q vs %q", e1, e2)
	}
}

func TestAvatarETag_ChangesWithContent(t *testing.T) {
	ps := profileNewTmpStore(t, "vulos:ed25519:etag3", nil)

	if err := ps.profileSaveAvatar(bytes.NewReader(
		profileMakePNG(t, 64, 64, color.RGBA{R: 1, G: 2, B: 3, A: 255}),
	)); err != nil {
		t.Fatalf("first save: %v", err)
	}
	e1, _ := ps.AvatarETag()

	if err := ps.profileSaveAvatar(bytes.NewReader(
		profileMakePNG(t, 64, 64, color.RGBA{R: 10, G: 20, B: 30, A: 255}),
	)); err != nil {
		t.Fatalf("second save: %v", err)
	}
	e2, _ := ps.AvatarETag()

	if e1 == e2 {
		t.Error("ETag did not change after different image upload")
	}
}

// ─── Visibility resolver tests ────────────────────────────────────────────────

func TestProfileCanViewImage_Public(t *testing.T) {
	ps := profileNewTmpStore(t, "id", nil)
	// Default is public; any caller (even empty) may see.
	if !ps.profileCanViewImage("") {
		t.Error("public image: anonymous should be allowed")
	}
	if !ps.profileCanViewImage("vulos:ed25519:stranger") {
		t.Error("public image: stranger should be allowed")
	}
}

func TestProfileCanViewImage_Peers(t *testing.T) {
	contacts := newProfileFakeContacts("vulos:ed25519:bob")
	ps := profileNewTmpStore(t, "id", contacts)
	if err := ps.Update(func(d *ProfileData) {
		d.Visibility.Image = ProfileVisPeers
	}); err != nil {
		t.Fatal(err)
	}

	if ps.profileCanViewImage("") {
		t.Error("peers-only: anonymous should be blocked")
	}
	if ps.profileCanViewImage("vulos:ed25519:stranger") {
		t.Error("peers-only: unapproved stranger should be blocked")
	}
	if !ps.profileCanViewImage("vulos:ed25519:bob") {
		t.Error("peers-only: approved peer should be allowed")
	}
}

func TestProfileCanViewImage_Nobody(t *testing.T) {
	contacts := newProfileFakeContacts("vulos:ed25519:bob")
	ps := profileNewTmpStore(t, "id", contacts)
	if err := ps.Update(func(d *ProfileData) {
		d.Visibility.Image = ProfileVisNobody
	}); err != nil {
		t.Fatal(err)
	}

	if ps.profileCanViewImage("vulos:ed25519:bob") {
		t.Error("nobody: even approved peer should be blocked")
	}
}

// ─── Avatar resize + WebP encode tests ───────────────────────────────────────

func TestProfileNNResize_OutputDimensions(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 800, 600))
	dst := image.NewNRGBA(image.Rect(0, 0, profileAvatarDim, profileAvatarDim))
	profileNNResize(dst, src)
	b := dst.Bounds()
	if b.Dx() != profileAvatarDim || b.Dy() != profileAvatarDim {
		t.Errorf("resize output %dx%d, want %dx%d", b.Dx(), b.Dy(), profileAvatarDim, profileAvatarDim)
	}
}

func TestProfileEncodeVP8L_ValidRIFFHeader(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, profileAvatarDim, profileAvatarDim))
	// Fill with a gradient so we have many different colour values.
	for y := 0; y < profileAvatarDim; y++ {
		for x := 0; x < profileAvatarDim; x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(x),
				G: uint8(y),
				B: uint8((x + y) / 2),
				A: 255,
			})
		}
	}
	data, err := profileEncodeVP8L(img)
	if err != nil {
		t.Fatalf("profileEncodeVP8L: %v", err)
	}
	// Minimum size: "RIFF" + 4 + "WEBP" + "VP8L" + 4 + 1 (sig) = 22 bytes.
	if len(data) < 22 {
		t.Fatalf("output too short: %d bytes", len(data))
	}
	// Check RIFF magic.
	if string(data[0:4]) != "RIFF" {
		t.Errorf("RIFF magic: got %q, want RIFF", data[0:4])
	}
	// Check WEBP type.
	if string(data[8:12]) != "WEBP" {
		t.Errorf("WEBP type: got %q, want WEBP", data[8:12])
	}
	// Check VP8L chunk ID.
	if string(data[12:16]) != "VP8L" {
		t.Errorf("VP8L chunk: got %q, want VP8L", data[12:16])
	}
	// Check VP8L signature byte.
	if data[20] != 0x2F {
		t.Errorf("VP8L sig: got 0x%02x, want 0x2F", data[20])
	}
}

func TestProfileSaveAvatar_WritesWebPAtPath(t *testing.T) {
	ps := profileNewTmpStore(t, "vulos:ed25519:avatartest", nil)
	pngBytes := profileMakePNG(t, 128, 128, color.RGBA{R: 100, G: 150, B: 200, A: 255})

	if err := ps.profileSaveAvatar(bytes.NewReader(pngBytes)); err != nil {
		t.Fatalf("profileSaveAvatar: %v", err)
	}

	// File must exist at AvatarPath.
	data, err := os.ReadFile(ps.AvatarPath())
	if err != nil {
		t.Fatalf("read avatar: %v", err)
	}

	// Validate RIFF/WEBP signature.
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		t.Errorf("avatar.webp has invalid RIFF/WEBP header (len=%d)", len(data))
	}
}

// ─── HTTP handler tests ───────────────────────────────────────────────────────

func profileNewMux(t *testing.T, vulosID string, contacts profileContactChecker) (*http.ServeMux, *ProfileStore) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewProfileStore(dir, vulosID, contacts)
	if err != nil {
		t.Fatalf("NewProfileStore: %v", err)
	}
	svc := &profileSvc{store: store, vulosID: vulosID}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/peering/profile", svc.handleGet)
	mux.HandleFunc("PUT /api/peering/profile", svc.handlePut)
	mux.HandleFunc("POST /api/peering/profile/image", svc.handlePostImage)
	mux.HandleFunc("GET /api/peering/profile/image", svc.handleGetImage)
	return mux, store
}

func profileDo(mux *http.ServeMux, method, path, body, callerID string) *httptest.ResponseRecorder {
	var bodyR *strings.Reader
	if body != "" {
		bodyR = strings.NewReader(body)
	} else {
		bodyR = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, bodyR)
	if callerID != "" {
		req.Header.Set("X-Vula-ID", callerID)
	}
	if body != "" && method != "GET" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func TestHandleGetProfile_Returns200(t *testing.T) {
	const vulosID = "vulos:ed25519:gettest"
	mux, _ := profileNewMux(t, vulosID, nil)
	rr := profileDo(mux, http.MethodGet, "/api/peering/profile", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var resp profileGetResp
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.VulosID != vulosID {
		t.Errorf("VulosID = %q, want %q", resp.VulosID, vulosID)
	}
	if resp.Visibility.Image != ProfileVisPublic {
		t.Errorf("Visibility.Image = %q, want public", resp.Visibility.Image)
	}
}

func TestHandlePutProfile_UpdatesFields(t *testing.T) {
	mux, store := profileNewMux(t, "vulos:ed25519:puttest", nil)
	body := `{"display_name":"Bob","bio":"hello"}`
	rr := profileDo(mux, http.MethodPut, "/api/peering/profile", body, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	d := store.Get()
	if d.DisplayName != "Bob" {
		t.Errorf("DisplayName = %q, want Bob", d.DisplayName)
	}
	if d.Bio != "hello" {
		t.Errorf("Bio = %q, want hello", d.Bio)
	}
}

func TestHandlePutProfile_UpdatesVisibility(t *testing.T) {
	mux, store := profileNewMux(t, "vulos:ed25519:vistest", nil)
	body := `{"visibility":{"image":"nobody","bio":"public","email":"peers"}}`
	rr := profileDo(mux, http.MethodPut, "/api/peering/profile", body, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	d := store.Get()
	if d.Visibility.Image != ProfileVisNobody {
		t.Errorf("Image = %q, want nobody", d.Visibility.Image)
	}
	if d.Visibility.Bio != ProfileVisPublic {
		t.Errorf("Bio = %q, want public", d.Visibility.Bio)
	}
	if d.Visibility.Email != ProfileVisPeers {
		t.Errorf("Email = %q, want peers", d.Visibility.Email)
	}
}

func TestHandlePostImage_CreatesWebP(t *testing.T) {
	mux, store := profileNewMux(t, "vulos:ed25519:imgtest", nil)

	pngBytes := profileMakePNG(t, 200, 200, color.RGBA{R: 50, G: 100, B: 150, A: 255})
	req := httptest.NewRequest(http.MethodPost, "/api/peering/profile/image", bytes.NewReader(pngBytes))
	req.Header.Set("Content-Type", "image/png")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("POST image status = %d: %s", rr.Code, rr.Body.String())
	}

	// ETag header must be present.
	if rr.Header().Get("ETag") == "" {
		t.Error("POST image: ETag header missing")
	}

	// avatar.webp must exist with valid header.
	data, err := os.ReadFile(store.AvatarPath())
	if err != nil {
		t.Fatalf("read avatar: %v", err)
	}
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		t.Error("avatar.webp does not start with RIFF/WEBP")
	}
}

func TestHandleGetImage_PublicNoCallerAllowed(t *testing.T) {
	mux, store := profileNewMux(t, "vulos:ed25519:imgget", nil)
	pngBytes := profileMakePNG(t, 64, 64, color.RGBA{R: 255, A: 255})
	if err := store.profileSaveAvatar(bytes.NewReader(pngBytes)); err != nil {
		t.Fatal(err)
	}

	rr := profileDo(mux, http.MethodGet, "/api/peering/profile/image", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET image (public): status = %d", rr.Code)
	}
	if rr.Header().Get("ETag") == "" {
		t.Error("GET image: ETag header missing")
	}
	if rr.Header().Get("Content-Type") != "image/webp" {
		t.Errorf("Content-Type = %q, want image/webp", rr.Header().Get("Content-Type"))
	}
}

func TestHandleGetImage_PeersOnlyBlocked(t *testing.T) {
	contacts := newProfileFakeContacts("vulos:ed25519:alice")
	mux, store := profileNewMux(t, "vulos:ed25519:imgvis", contacts)

	pngBytes := profileMakePNG(t, 64, 64, color.RGBA{G: 255, A: 255})
	if err := store.profileSaveAvatar(bytes.NewReader(pngBytes)); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(d *ProfileData) {
		d.Visibility.Image = ProfileVisPeers
	}); err != nil {
		t.Fatal(err)
	}

	// Anonymous → 403.
	rr := profileDo(mux, http.MethodGet, "/api/peering/profile/image", "", "")
	if rr.Code != http.StatusForbidden {
		t.Errorf("anonymous: status = %d, want 403", rr.Code)
	}

	// Unapproved peer → 403.
	rr = profileDo(mux, http.MethodGet, "/api/peering/profile/image", "", "vulos:ed25519:stranger")
	if rr.Code != http.StatusForbidden {
		t.Errorf("stranger: status = %d, want 403", rr.Code)
	}

	// Approved peer → 200.
	rr = profileDo(mux, http.MethodGet, "/api/peering/profile/image", "", "vulos:ed25519:alice")
	if rr.Code != http.StatusOK {
		t.Errorf("alice: status = %d, want 200", rr.Code)
	}
}

func TestHandleGetImage_ETagConditional304(t *testing.T) {
	mux, store := profileNewMux(t, "vulos:ed25519:etag304", nil)
	pngBytes := profileMakePNG(t, 64, 64, color.RGBA{B: 255, A: 255})
	if err := store.profileSaveAvatar(bytes.NewReader(pngBytes)); err != nil {
		t.Fatal(err)
	}
	etag, _ := store.AvatarETag()

	req := httptest.NewRequest(http.MethodGet, "/api/peering/profile/image", nil)
	req.Header.Set("If-None-Match", etag)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotModified {
		t.Errorf("If-None-Match match: status = %d, want 304", rr.Code)
	}
}

func TestHandleGetImage_NoAvatar404(t *testing.T) {
	mux, _ := profileNewMux(t, "vulos:ed25519:noimg", nil)
	rr := profileDo(mux, http.MethodGet, "/api/peering/profile/image", "", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("no avatar: status = %d, want 404", rr.Code)
	}
}

func TestHandleGetImage_NobodyVisibility403(t *testing.T) {
	mux, store := profileNewMux(t, "vulos:ed25519:nobody", nil)
	pngBytes := profileMakePNG(t, 64, 64, color.RGBA{R: 255, A: 255})
	if err := store.profileSaveAvatar(bytes.NewReader(pngBytes)); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(d *ProfileData) {
		d.Visibility.Image = ProfileVisNobody
	}); err != nil {
		t.Fatal(err)
	}

	rr := profileDo(mux, http.MethodGet, "/api/peering/profile/image", "", "vulos:ed25519:anyone")
	if rr.Code != http.StatusForbidden {
		t.Errorf("nobody visibility: status = %d, want 403", rr.Code)
	}
}

func TestRegisterProfileHandlers_Smoke(t *testing.T) {
	dir := t.TempDir()
	mux := http.NewServeMux()
	RegisterProfileHandlers(mux, dir, "vulos:ed25519:smoke", nil)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/peering/profile", nil)
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("smoke GET /api/peering/profile: %d", rr.Code)
	}
}
