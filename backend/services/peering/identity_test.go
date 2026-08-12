package peering

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── Base58 round-trip ────────────────────────────────────────────────────────

func TestBase58RoundTrip(t *testing.T) {
	cases := [][]byte{
		{},
		{0x00},
		{0x00, 0x00, 0x01},
		{0xff, 0xfe, 0xfd},
		make([]byte, 32), // all zeros (like a zero public key)
	}

	// Generate a random 32-byte case.
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	cases = append(cases, random)

	for _, in := range cases {
		encoded := base58Encode(in)
		decoded, err := base58Decode(encoded)
		if err != nil {
			t.Errorf("base58Decode(%q) error: %v", encoded, err)
			continue
		}
		if !bytes.Equal(in, decoded) {
			t.Errorf("round-trip mismatch: in=%x encoded=%q decoded=%x", in, encoded, decoded)
		}
	}
}

func TestBase58KnownVector(t *testing.T) {
	// Bitcoin base58: []byte{0x00, 0x01, 0x02, 0x03} → should be consistent.
	in := []byte{0x01, 0x02, 0x03}
	enc := base58Encode(in)
	dec, err := base58Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(in, dec) {
		t.Fatalf("got %x want %x", dec, in)
	}
}

// ─── Vula ID encode / decode ──────────────────────────────────────────────────

func TestEncodeDecodeVulosID(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	id := encodeVulosID(pub)
	if !strings.HasPrefix(id, "vulos:ed25519:") {
		t.Fatalf("Vula ID missing prefix: %q", id)
	}

	decoded, err := decodeVulosID(id)
	if err != nil {
		t.Fatalf("decodeVulosID error: %v", err)
	}
	if !bytes.Equal(pub, decoded) {
		t.Fatalf("pub key mismatch after decode")
	}
}

func TestDecodeVulosID_BadPrefix(t *testing.T) {
	_, err := decodeVulosID("bad:prefix:abc")
	if err == nil {
		t.Fatal("expected error for bad prefix")
	}
}

func TestDecodeVulosID_WrongLength(t *testing.T) {
	// Encode too-short key.
	short := base58Encode([]byte{0x01, 0x02, 0x03})
	_, err := decodeVulosID("vulos:ed25519:" + short)
	if err == nil {
		t.Fatal("expected error for wrong key length")
	}
}

// ─── Address parsing ──────────────────────────────────────────────────────────

func TestParseVulaAddress_RoundTrip(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := encodeVulosID(pub)
	original := id + "@example.vulos.org:8080"

	addr, err := ParseVulaAddress(original)
	if err != nil {
		t.Fatalf("ParseVulaAddress(%q): %v", original, err)
	}
	if addr.VulosID != id {
		t.Errorf("VulosID: got %q want %q", addr.VulosID, id)
	}
	if addr.Host != "example.vulos.org" {
		t.Errorf("Host: got %q want %q", addr.Host, "example.vulos.org")
	}
	if addr.Port != 8080 {
		t.Errorf("Port: got %d want %d", addr.Port, 8080)
	}
	if addr.String() != original {
		t.Errorf("String(): got %q want %q", addr.String(), original)
	}
}

func TestParseVulaAddress_Errors(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"missing @", "vulos:ed25519:abc123:8080"},
		{"missing port", "vulos:ed25519:abc@example.com"},
		{"invalid port", "vulos:ed25519:abc@example.com:notaport"},
		{"bad vula id", "badid@example.com:8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseVulaAddress(tc.input)
			if err == nil {
				t.Fatalf("expected error for input %q", tc.input)
			}
		})
	}
}

// ─── Keypair persistence ──────────────────────────────────────────────────────

func TestLoadOrGenerate_FirstBoot(t *testing.T) {
	dir := t.TempDir()
	identityDir := filepath.Join(dir, "identity")
	if err := os.MkdirAll(identityDir, 0700); err != nil {
		t.Fatal(err)
	}

	priv, pub, vulosID, err := loadOrGenerate(identityDir)
	if err != nil {
		t.Fatalf("loadOrGenerate error: %v", err)
	}
	if len(priv) != ed25519.PrivateKeySize {
		t.Errorf("priv key size %d, want %d", len(priv), ed25519.PrivateKeySize)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Errorf("pub key size %d, want %d", len(pub), ed25519.PublicKeySize)
	}
	if !strings.HasPrefix(vulosID, "vulos:ed25519:") {
		t.Errorf("vulosID missing prefix: %q", vulosID)
	}

	// Files must exist with correct permissions.
	checkPerm(t, filepath.Join(identityDir, privKeyFile), 0600)
	checkPerm(t, filepath.Join(identityDir, pubKeyFile), 0644)
	checkPerm(t, filepath.Join(identityDir, vulosIDFile), 0644)
}

func TestLoadOrGenerate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	identityDir := filepath.Join(dir, "identity")
	if err := os.MkdirAll(identityDir, 0700); err != nil {
		t.Fatal(err)
	}

	_, pub1, id1, err := loadOrGenerate(identityDir)
	if err != nil {
		t.Fatal(err)
	}

	// Second call must load, not regenerate.
	_, pub2, id2, err := loadOrGenerate(identityDir)
	if err != nil {
		t.Fatal(err)
	}

	if id1 != id2 {
		t.Errorf("Vula ID changed across loads: %q → %q", id1, id2)
	}
	if !bytes.Equal(pub1, pub2) {
		t.Error("Public key changed across loads")
	}
}

// ─── Encrypted export / import ────────────────────────────────────────────────

func TestExportImportRoundTrip(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	vulosID := encodeVulosID(pub)
	passphrase := "correct horse battery staple"

	bundle, err := encryptExport(priv, vulosID, passphrase)
	if err != nil {
		t.Fatalf("encryptExport: %v", err)
	}

	importedPriv, importedID, err := decryptImport(bundle, passphrase)
	if err != nil {
		t.Fatalf("decryptImport: %v", err)
	}

	if importedID != vulosID {
		t.Errorf("Vula ID mismatch: got %q want %q", importedID, vulosID)
	}
	if !bytes.Equal(importedPriv, priv) {
		t.Error("Private key mismatch after export/import")
	}
}

func TestImport_WrongPassphrase(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	vulosID := encodeVulosID(pub)

	bundle, err := encryptExport(priv, vulosID, "correct-passphrase")
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = decryptImport(bundle, "wrong-passphrase")
	if err == nil {
		t.Fatal("expected error with wrong passphrase")
	}
}

// ─── Service integration: same Vula ID on reload ──────────────────────────────

func TestService_PersistsAndReloads(t *testing.T) {
	home := t.TempDir()

	svc1 := New(home)
	id1 := svc1.vulosID
	pub1 := svc1.pub

	svc2 := New(home)
	id2 := svc2.vulosID
	pub2 := svc2.pub

	if id1 != id2 {
		t.Errorf("Vula ID changed on reload: %q → %q", id1, id2)
	}
	if !bytes.Equal(pub1, pub2) {
		t.Error("Public key changed on reload")
	}
}

// ─── HTTP handler tests ───────────────────────────────────────────────────────

func TestHandleGetIdentity(t *testing.T) {
	home := t.TempDir()
	svc := New(home)

	req := httptest.NewRequest(http.MethodGet, "/api/peering/identity", nil)
	w := httptest.NewRecorder()
	svc.handleGetIdentity(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", w.Code)
	}
	var resp identityResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.HasPrefix(resp.VulosID, "vulos:ed25519:") {
		t.Errorf("bad VulosID: %q", resp.VulosID)
	}
	if resp.PublicKeyB58 == "" {
		t.Error("PublicKeyB58 is empty")
	}
}

func TestHandleExportImport_HTTP(t *testing.T) {
	home := t.TempDir()
	svc := New(home)
	originalID := svc.vulosID
	passphrase := "test-passphrase-123"

	// Export.
	exportReqBody, _ := json.Marshal(map[string]string{"passphrase": passphrase})
	req := httptest.NewRequest(http.MethodPost, "/api/peering/identity/export",
		bytes.NewReader(exportReqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	svc.handleExportIdentity(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("export status %d body: %s", w.Code, w.Body)
	}
	exportBundle := w.Body.Bytes()

	// Import onto a fresh service (fresh home directory).
	home2 := t.TempDir()
	svc2 := New(home2)

	// Build the import body: the export bundle JSON + "passphrase" field injected.
	var bundleMap map[string]any
	if err := json.Unmarshal(exportBundle, &bundleMap); err != nil {
		t.Fatalf("parse export bundle: %v", err)
	}
	bundleMap["passphrase"] = passphrase
	importBody, _ := json.Marshal(bundleMap)

	req2 := httptest.NewRequest(http.MethodPost, "/api/peering/identity/import",
		bytes.NewReader(importBody))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	svc2.handleImportIdentity(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("import status %d body: %s", w2.Code, w2.Body)
	}

	var resp identityResponse
	if err := json.NewDecoder(w2.Body).Decode(&resp); err != nil {
		t.Fatalf("decode import response: %v", err)
	}
	if resp.VulosID != originalID {
		t.Errorf("imported Vula ID %q, want %q", resp.VulosID, originalID)
	}

	// The service's in-memory state should reflect the imported identity.
	if svc2.vulosID != originalID {
		t.Errorf("svc2.vulosID %q, want %q", svc2.vulosID, originalID)
	}

	// A new service loaded from the same home2 should have the same ID.
	svc3 := New(home2)
	if svc3.vulosID != originalID {
		t.Errorf("reloaded svc3.vulosID %q, want %q", svc3.vulosID, originalID)
	}
}

func TestHandleExport_MissingPassphrase(t *testing.T) {
	home := t.TempDir()
	svc := New(home)

	req := httptest.NewRequest(http.MethodPost, "/api/peering/identity/export",
		strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	svc.handleExportIdentity(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// checkPerm verifies that the file at path has exactly the given permission bits.
// On darwin/linux only (permission checks are meaningful there).
func checkPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	got := info.Mode().Perm()
	if got != want {
		t.Errorf("permissions on %q: got %04o want %04o", path, got, want)
	}
}
