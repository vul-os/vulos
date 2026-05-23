// recovery_test.go — unit tests for VUMAIL-06 account recovery.
package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ─── inMemRecoveryStore ───────────────────────────────────────────────────────

// inMemRecoveryStore is a thread-safe in-memory RecoveryStore for tests.
type inMemRecoveryStore struct {
	mu    sync.RWMutex
	blobs map[string][]byte
}

func newInMemRecoveryStore() *inMemRecoveryStore {
	return &inMemRecoveryStore{blobs: make(map[string][]byte)}
}

func (s *inMemRecoveryStore) SaveRecoveryBlob(userID string, blob []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(blob))
	copy(cp, blob)
	s.blobs[userID] = cp
	return nil
}

func (s *inMemRecoveryStore) LoadRecoveryBlob(userID string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b := s.blobs[userID]
	if b == nil {
		return nil, nil
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp, nil
}

// ─── GenerateRecoveryKit ──────────────────────────────────────────────────────

func TestGenerateRecoveryKit_Basic(t *testing.T) {
	kit, err := GenerateRecoveryKit("mypassword")
	if err != nil {
		t.Fatalf("GenerateRecoveryKit: %v", err)
	}
	if kit.Mnemonic == "" {
		t.Fatal("Mnemonic is empty")
	}
	words := strings.Fields(kit.Mnemonic)
	if len(words) != 24 {
		t.Fatalf("mnemonic has %d words, want 24", len(words))
	}
	if len(kit.EncryptedBlob) == 0 {
		t.Fatal("EncryptedBlob is empty")
	}
}

func TestGenerateRecoveryKit_EmptyPassword(t *testing.T) {
	if _, err := GenerateRecoveryKit(""); err == nil {
		t.Fatal("expected error for empty password, got nil")
	}
}

func TestGenerateRecoveryKit_DifferentEachTime(t *testing.T) {
	kit1, err1 := GenerateRecoveryKit("pw1")
	kit2, err2 := GenerateRecoveryKit("pw2")
	if err1 != nil || err2 != nil {
		t.Fatalf("GenerateRecoveryKit: %v %v", err1, err2)
	}
	if kit1.Mnemonic == kit2.Mnemonic {
		t.Error("two independent kits produced the same mnemonic (extremely unlikely)")
	}
}

// ─── Encrypt / Decrypt round-trip ────────────────────────────────────────────

func TestEncryptDecryptRoundTrip(t *testing.T) {
	const pass = "recovery-test-password"
	kit, err := GenerateRecoveryKit(pass)
	if err != nil {
		t.Fatalf("GenerateRecoveryKit: %v", err)
	}

	got, err := DecryptRecoveryKit(kit.EncryptedBlob, pass)
	if err != nil {
		t.Fatalf("DecryptRecoveryKit: %v", err)
	}
	if got != kit.Mnemonic {
		t.Errorf("decrypted mnemonic = %q, want %q", got, kit.Mnemonic)
	}
}

func TestDecryptRecoveryKit_WrongPassword(t *testing.T) {
	kit, _ := GenerateRecoveryKit("correct-password")
	if _, err := DecryptRecoveryKit(kit.EncryptedBlob, "wrong-password"); err == nil {
		t.Fatal("expected error for wrong password, got nil")
	}
}

// ─── ValidateMnemonic ─────────────────────────────────────────────────────────

func TestValidateMnemonic_Valid(t *testing.T) {
	kit, err := GenerateRecoveryKit("pw")
	if err != nil {
		t.Fatalf("GenerateRecoveryKit: %v", err)
	}
	if err := ValidateMnemonic(kit.Mnemonic); err != nil {
		t.Fatalf("ValidateMnemonic on generated mnemonic: %v", err)
	}
}

func TestValidateMnemonic_TooFewWords(t *testing.T) {
	if err := ValidateMnemonic("one two three"); err == nil {
		t.Fatal("expected error for 3-word mnemonic, got nil")
	}
}

func TestValidateMnemonic_UnknownWord(t *testing.T) {
	kit, _ := GenerateRecoveryKit("pw")
	words := strings.Fields(kit.Mnemonic)
	words[0] = "zzznonsensexxx"
	bad := strings.Join(words, " ")
	if err := ValidateMnemonic(bad); err == nil {
		t.Fatal("expected error for unknown word, got nil")
	}
}

func TestValidateMnemonic_CaseInsensitive(t *testing.T) {
	kit, err := GenerateRecoveryKit("pw")
	if err != nil {
		t.Fatalf("GenerateRecoveryKit: %v", err)
	}
	upper := strings.ToUpper(kit.Mnemonic)
	if err := ValidateMnemonic(upper); err != nil {
		t.Fatalf("ValidateMnemonic with uppercase: %v", err)
	}
}

// ─── RestoreFromMnemonic ──────────────────────────────────────────────────────

func TestRestoreFromMnemonic_Deterministic(t *testing.T) {
	kit, err := GenerateRecoveryKit("pw")
	if err != nil {
		t.Fatalf("GenerateRecoveryKit: %v", err)
	}

	k1, err := RestoreFromMnemonic(kit.Mnemonic)
	if err != nil {
		t.Fatalf("RestoreFromMnemonic (1st): %v", err)
	}
	k2, err := RestoreFromMnemonic(kit.Mnemonic)
	if err != nil {
		t.Fatalf("RestoreFromMnemonic (2nd): %v", err)
	}

	if string(k1.Ed25519PrivKey) != string(k2.Ed25519PrivKey) {
		t.Error("private keys differ across identical mnemonic restores")
	}
	if string(k1.KeyringRootKey) != string(k2.KeyringRootKey) {
		t.Error("keyring root keys differ across identical mnemonic restores")
	}
}

func TestRestoreFromMnemonic_KeyLengths(t *testing.T) {
	kit, _ := GenerateRecoveryKit("pw")
	keys, err := RestoreFromMnemonic(kit.Mnemonic)
	if err != nil {
		t.Fatalf("RestoreFromMnemonic: %v", err)
	}
	if len(keys.Ed25519PrivKey) != 64 {
		t.Errorf("Ed25519PrivKey length = %d, want 64", len(keys.Ed25519PrivKey))
	}
	if len(keys.Ed25519PubKey) != 32 {
		t.Errorf("Ed25519PubKey length = %d, want 32", len(keys.Ed25519PubKey))
	}
	if len(keys.KeyringRootKey) != 32 {
		t.Errorf("KeyringRootKey length = %d, want 32", len(keys.KeyringRootKey))
	}
}

func TestRestoreFromMnemonic_PublicKeyMatchesPrivate(t *testing.T) {
	kit, _ := GenerateRecoveryKit("pw")
	keys, err := RestoreFromMnemonic(kit.Mnemonic)
	if err != nil {
		t.Fatalf("RestoreFromMnemonic: %v", err)
	}
	// Ed25519 private key is seed (32 bytes) + public key (32 bytes).
	// The second half should equal the returned public key.
	derivedPub := keys.Ed25519PrivKey[32:]
	if string(derivedPub) != string(keys.Ed25519PubKey) {
		t.Error("public key does not match the public portion of the private key")
	}
}

func TestRestoreFromMnemonic_InvalidMnemonic(t *testing.T) {
	if _, err := RestoreFromMnemonic("invalid mnemonic words here"); err == nil {
		t.Fatal("expected error for invalid mnemonic, got nil")
	}
}

// ─── RestoreFromEscrow ────────────────────────────────────────────────────────

func TestRestoreFromEscrow_RoundTrip(t *testing.T) {
	const pass = "escrow-password"
	kit, err := GenerateRecoveryKit(pass)
	if err != nil {
		t.Fatalf("GenerateRecoveryKit: %v", err)
	}

	keys, mnemonic, err := RestoreFromEscrow(kit.EscrowBlob(), pass)
	if err != nil {
		t.Fatalf("RestoreFromEscrow: %v", err)
	}
	if mnemonic != kit.Mnemonic {
		t.Errorf("mnemonic mismatch: got %q, want %q", mnemonic, kit.Mnemonic)
	}
	if len(keys.Ed25519PrivKey) != 64 {
		t.Errorf("Ed25519PrivKey length = %d, want 64", len(keys.Ed25519PrivKey))
	}
}

func TestRestoreFromEscrow_WrongPassword(t *testing.T) {
	kit, _ := GenerateRecoveryKit("right")
	if _, _, err := RestoreFromEscrow(kit.EscrowBlob(), "wrong"); err == nil {
		t.Fatal("expected error with wrong escrow password, got nil")
	}
}

// ─── Different mnemonics yield different keys ─────────────────────────────────

func TestRestoreFromMnemonic_DifferentMnemonicsDifferentKeys(t *testing.T) {
	kit1, _ := GenerateRecoveryKit("pw1")
	kit2, _ := GenerateRecoveryKit("pw2")
	if kit1.Mnemonic == kit2.Mnemonic {
		t.Skip("improbably identical mnemonics")
	}
	k1, _ := RestoreFromMnemonic(kit1.Mnemonic)
	k2, _ := RestoreFromMnemonic(kit2.Mnemonic)
	if string(k1.Ed25519PrivKey) == string(k2.Ed25519PrivKey) {
		t.Error("different mnemonics produced the same private key")
	}
}

// ─── HTTP handlers ────────────────────────────────────────────────────────────

func TestHandleGenerate_RequiresSession(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRecoveryHandlers(mux, newInMemRecoveryStore())

	req := httptest.NewRequest("POST", "/api/auth/recovery/generate", strings.NewReader(`{"password":"pw"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestHandleGenerate_ReturnsMnemonic(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRecoveryHandlers(mux, newInMemRecoveryStore())

	req := httptest.NewRequest("POST", "/api/auth/recovery/generate", strings.NewReader(`{"password":"mypassword"}`))
	req.Header.Set("X-User-ID", "user-1")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "mnemonic") {
		t.Errorf("response does not contain 'mnemonic': %s", body)
	}
}

func TestHandleGenerate_MissingPassword(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRecoveryHandlers(mux, newInMemRecoveryStore())

	req := httptest.NewRequest("POST", "/api/auth/recovery/generate", strings.NewReader(`{}`))
	req.Header.Set("X-User-ID", "user-1")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", w.Code)
	}
}

func TestHandleRestore_ValidMnemonic(t *testing.T) {
	kit, _ := GenerateRecoveryKit("pw")
	mux := http.NewServeMux()
	RegisterRecoveryHandlers(mux, newInMemRecoveryStore())

	body := `{"mnemonic":"` + kit.Mnemonic + `"}`
	req := httptest.NewRequest("POST", "/api/auth/recovery/restore", strings.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "public_key_b64") {
		t.Errorf("response does not contain public_key_b64: %s", w.Body.String())
	}
}

func TestHandleRestore_InvalidMnemonic(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRecoveryHandlers(mux, newInMemRecoveryStore())

	req := httptest.NewRequest("POST", "/api/auth/recovery/restore", strings.NewReader(`{"mnemonic":"bad words here not valid"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleEscrow_RequiresSession(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRecoveryHandlers(mux, newInMemRecoveryStore())

	req := httptest.NewRequest("POST", "/api/auth/recovery/escrow", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestHandleEscrow_NoKit(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRecoveryHandlers(mux, newInMemRecoveryStore())

	req := httptest.NewRequest("POST", "/api/auth/recovery/escrow", strings.NewReader(`{"password":"pw"}`))
	req.Header.Set("X-User-ID", "user-no-kit")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleEscrow_WithKit(t *testing.T) {
	store := newInMemRecoveryStore()
	mux := http.NewServeMux()
	RegisterRecoveryHandlers(mux, store)

	// First generate a kit.
	genReq := httptest.NewRequest("POST", "/api/auth/recovery/generate", strings.NewReader(`{"password":"mypass"}`))
	genReq.Header.Set("X-User-ID", "user-escrow-1")
	genW := httptest.NewRecorder()
	mux.ServeHTTP(genW, genReq)
	if genW.Code != http.StatusOK {
		t.Fatalf("generate status = %d", genW.Code)
	}

	// Now escrow it.
	escrReq := httptest.NewRequest("POST", "/api/auth/recovery/escrow", strings.NewReader(`{"password":"mypass"}`))
	escrReq.Header.Set("X-User-ID", "user-escrow-1")
	escrW := httptest.NewRecorder()
	mux.ServeHTTP(escrW, escrReq)
	if escrW.Code != http.StatusOK {
		t.Fatalf("escrow status = %d; body: %s", escrW.Code, escrW.Body.String())
	}
	if !strings.Contains(escrW.Body.String(), "escrowed") {
		t.Errorf("escrow response missing 'escrowed': %s", escrW.Body.String())
	}
}

func TestHandleEscrow_WrongPassword(t *testing.T) {
	store := newInMemRecoveryStore()
	mux := http.NewServeMux()
	RegisterRecoveryHandlers(mux, store)

	// Generate kit.
	genReq := httptest.NewRequest("POST", "/api/auth/recovery/generate", strings.NewReader(`{"password":"rightpass"}`))
	genReq.Header.Set("X-User-ID", "user-esc-wrong")
	mux.ServeHTTP(httptest.NewRecorder(), genReq)

	// Try to escrow with wrong password.
	escrReq := httptest.NewRequest("POST", "/api/auth/recovery/escrow", strings.NewReader(`{"password":"wrongpass"}`))
	escrReq.Header.Set("X-User-ID", "user-esc-wrong")
	escrW := httptest.NewRecorder()
	mux.ServeHTTP(escrW, escrReq)
	if escrW.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", escrW.Code)
	}
}
