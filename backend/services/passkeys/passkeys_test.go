package passkeys

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	wa "github.com/go-webauthn/webauthn/webauthn"

	"vulos/backend/services/devicekey"
)

// --- helpers ---

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "passkeys-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// newTestKeyStore opens a software-backed devicekey.KeyStore in a temp dir.
func newTestKeyStore(t *testing.T) devicekey.KeyStore {
	t.Helper()
	ks, err := devicekey.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open devicekey: %v", err)
	}
	t.Cleanup(func() { _ = ks.Close() })
	return ks
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	svc := New(tempDir(t), newTestKeyStore(t))
	// Force localhost — go-webauthn rejects origins that don't match the RPID.
	svc.rpID = "localhost"
	svc.origins = []string{"http://localhost:8080"}
	return svc
}

func newTestServiceAt(t *testing.T, dir string, ks devicekey.KeyStore) *Service {
	t.Helper()
	svc := New(dir, ks)
	svc.rpID = "localhost"
	svc.origins = []string{"http://localhost:8080"}
	return svc
}

// --- devicekey sealing round-trip (the real main-branch API) ---

func TestDeviceKeySealUnseal(t *testing.T) {
	ks := newTestKeyStore(t)
	plain := []byte("hello world 🔑")
	sealed, err := ks.Seal(plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if string(sealed) == string(plain) {
		t.Fatal("sealed blob equals plaintext (not encrypted)")
	}
	got, err := ks.Unseal(sealed)
	if err != nil {
		t.Fatalf("unseal: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("roundtrip mismatch: got %q want %q", got, plain)
	}
}

func TestStoredCredentialIsSealedOnDisk(t *testing.T) {
	svc := newTestService(t)
	userID := "u-disk-1"

	credID := make([]byte, 16)
	rand.Read(credID)
	privKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cred := wa.Credential{
		ID:              credID,
		PublicKey:       marshalCOSEKey(t, privKey),
		AttestationType: "none",
	}
	if err := svc.au12SealAndStore(userID, &cred); err != nil {
		t.Fatalf("seal+store: %v", err)
	}

	id := base64.RawURLEncoding.EncodeToString(credID)
	path, err := svc.au12CredPath(userID, id)
	if err != nil {
		t.Fatalf("credPath: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credential file: %v", err)
	}
	// The raw COSE public key bytes must not appear in plaintext on disk.
	if containsBytes(raw, cred.PublicKey) {
		t.Fatal("credential public key found in plaintext on disk")
	}
}

// --- Service construction ---

func TestNewService(t *testing.T) {
	dir := tempDir(t)
	ks := newTestKeyStore(t)
	svc := New(dir, ks)
	if svc == nil {
		t.Fatal("New returned nil")
	}
	if svc.vaultDir != dir {
		t.Errorf("vaultDir = %q, want %q", svc.vaultDir, dir)
	}
	if svc.ks == nil {
		t.Fatal("KeyStore not set")
	}
}

// --- List ---

func TestListEmpty(t *testing.T) {
	svc := newTestService(t)
	creds, err := svc.List("user-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(creds) != 0 {
		t.Fatalf("expected 0 creds, got %d", len(creds))
	}
}

// --- Delete ---

func TestDeleteNotFound(t *testing.T) {
	svc := newTestService(t)
	err := svc.Delete("user-1", "nonexistent")
	if err == nil {
		t.Fatal("expected error deleting nonexistent credential")
	}
}

// --- Persist sealed records directly (white-box) ---

func TestSealAndLoadCreds(t *testing.T) {
	svc := newTestService(t)
	userID := "u-seal-1"

	privKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	credID := make([]byte, 16)
	rand.Read(credID)

	cred := wa.Credential{
		ID:              credID,
		PublicKey:       marshalCOSEKey(t, privKey),
		AttestationType: "none",
		Flags:           wa.CredentialFlags{UserPresent: true},
	}

	if err := svc.au12SealAndStore(userID, &cred); err != nil {
		t.Fatalf("SealAndStore: %v", err)
	}

	loaded, err := svc.au12LoadCreds(userID)
	if err != nil {
		t.Fatalf("LoadCreds: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 loaded cred, got %d", len(loaded))
	}
	if string(loaded[0].ID) != string(credID) {
		t.Fatalf("credential ID mismatch")
	}
}

func TestListAfterSeal(t *testing.T) {
	svc := newTestService(t)
	userID := "u-list-1"

	credID := make([]byte, 16)
	rand.Read(credID)
	privKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	cred := wa.Credential{
		ID:              credID,
		PublicKey:       marshalCOSEKey(t, privKey),
		AttestationType: "none",
	}
	if err := svc.au12SealAndStore(userID, &cred); err != nil {
		t.Fatalf("seal: %v", err)
	}

	creds, err := svc.List(userID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("expected 1 cred, got %d", len(creds))
	}
	want := base64.RawURLEncoding.EncodeToString(credID)
	if creds[0].ID != want {
		t.Errorf("ID = %q, want %q", creds[0].ID, want)
	}
}

func TestDeleteRemovesCredential(t *testing.T) {
	svc := newTestService(t)
	userID := "u-del-1"

	credID := make([]byte, 16)
	rand.Read(credID)
	privKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cred := wa.Credential{
		ID:              credID,
		PublicKey:       marshalCOSEKey(t, privKey),
		AttestationType: "none",
	}
	if err := svc.au12SealAndStore(userID, &cred); err != nil {
		t.Fatalf("seal: %v", err)
	}

	id := base64.RawURLEncoding.EncodeToString(credID)
	if err := svc.Delete(userID, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	creds, _ := svc.List(userID)
	if len(creds) != 0 {
		t.Fatalf("expected 0 creds after delete, got %d", len(creds))
	}
}

func TestDeletePersistenceAcrossLoad(t *testing.T) {
	dir := tempDir(t)
	ks := newTestKeyStore(t)
	userID := "u-persist-1"

	svc1 := newTestServiceAt(t, dir, ks)

	credID := make([]byte, 16)
	rand.Read(credID)
	privKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cred := wa.Credential{
		ID:              credID,
		PublicKey:       marshalCOSEKey(t, privKey),
		AttestationType: "none",
	}
	if err := svc1.au12SealAndStore(userID, &cred); err != nil {
		t.Fatalf("seal: %v", err)
	}

	id := base64.RawURLEncoding.EncodeToString(credID)
	if err := svc1.Delete(userID, id); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// New service instance (same dir + same KeyStore) reads from disk.
	svc2 := newTestServiceAt(t, dir, ks)
	creds, _ := svc2.List(userID)
	if len(creds) != 0 {
		t.Fatalf("expected 0 creds after reload, got %d", len(creds))
	}
}

// --- BeginRegistration ---

func TestBeginRegistrationReturnsOptions(t *testing.T) {
	svc := newTestService(t)
	challenge, sessionData, err := svc.BeginRegistration("u-1", "Alice")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	if len(challenge) == 0 {
		t.Fatal("challenge is empty")
	}
	if len(sessionData) == 0 {
		t.Fatal("sessionData is empty")
	}

	var opts map[string]any
	if err := json.Unmarshal(challenge, &opts); err != nil {
		t.Fatalf("challenge not valid JSON: %v", err)
	}
	if _, ok := opts["publicKey"]; !ok {
		t.Fatal("challenge JSON missing publicKey field")
	}
}

func TestBeginRegistrationSessionStored(t *testing.T) {
	svc := newTestService(t)
	_, sessionData, err := svc.BeginRegistration("u-2", "Bob")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	tok, err := au12ExtractToken(sessionData)
	if err != nil {
		t.Fatalf("extractToken: %v", err)
	}
	svc.mu.Lock()
	_, ok := svc.sessions[tok]
	svc.mu.Unlock()
	if !ok {
		t.Fatal("session not stored after BeginRegistration")
	}
}

// --- BeginAssertion ---

func TestBeginAssertionNoCredentialsErrors(t *testing.T) {
	svc := newTestService(t)
	_, _, err := svc.BeginAssertion("u-no-creds")
	if err == nil {
		t.Fatal("expected error for user with no credentials")
	}
}

func TestBeginAssertionWithCredentials(t *testing.T) {
	svc := newTestService(t)
	userID := "u-assert-1"

	credID := make([]byte, 16)
	rand.Read(credID)
	privKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cred := wa.Credential{
		ID:              credID,
		PublicKey:       marshalCOSEKey(t, privKey),
		AttestationType: "none",
		Flags:           wa.CredentialFlags{UserPresent: true},
	}
	if err := svc.au12SealAndStore(userID, &cred); err != nil {
		t.Fatalf("seal: %v", err)
	}

	challenge, sessionData, err := svc.BeginAssertion(userID)
	if err != nil {
		t.Fatalf("BeginAssertion: %v", err)
	}
	if len(challenge) == 0 {
		t.Fatal("assertion challenge is empty")
	}
	if len(sessionData) == 0 {
		t.Fatal("assertion sessionData is empty")
	}

	var opts map[string]any
	if err := json.Unmarshal(challenge, &opts); err != nil {
		t.Fatalf("assertion challenge not valid JSON: %v", err)
	}
	if _, ok := opts["publicKey"]; !ok {
		t.Fatal("assertion JSON missing publicKey field")
	}
}

// --- Registration + Assertion round-trip ---
//
// go-webauthn's FinishRegistration and FinishLogin require a real client-signed
// response generated against the challenge produced by Begin*. We simulate a
// software authenticator by constructing valid CBOR-encoded attestation and
// assertion responses from scratch.

func TestRegistrationRoundTrip(t *testing.T) {
	svc := newTestService(t)
	userID := "u-rt-1"

	challenge, sessionData, err := svc.BeginRegistration(userID, "RT User")
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}

	var creationOpts struct {
		PublicKey struct {
			Challenge string `json:"challenge"` // base64url
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(challenge, &creationOpts); err != nil {
		t.Fatalf("parse challenge: %v", err)
	}
	rawChallenge, err := base64.RawURLEncoding.DecodeString(creationOpts.PublicKey.Challenge)
	if err != nil {
		t.Fatalf("decode challenge: %v", err)
	}

	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	credID := make([]byte, 16)
	rand.Read(credID)

	attestResp := buildAttestationResponse(t, credID, privKey, rawChallenge, svc.rpID, svc.origins[0])

	credentialID, err := svc.FinishRegistration(userID, attestResp, sessionData)
	if err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}
	if credentialID == "" {
		t.Fatal("empty credentialID returned")
	}

	creds, err := svc.List(userID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("expected 1 cred, got %d", len(creds))
	}

	// Now begin + finish assertion.
	aChallenge, aSessionData, err := svc.BeginAssertion(userID)
	if err != nil {
		t.Fatalf("BeginAssertion: %v", err)
	}

	var assertionOpts struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(aChallenge, &assertionOpts); err != nil {
		t.Fatalf("parse assertion challenge: %v", err)
	}
	rawAssertChallenge, err := base64.RawURLEncoding.DecodeString(assertionOpts.PublicKey.Challenge)
	if err != nil {
		t.Fatalf("decode assertion challenge: %v", err)
	}

	assertResp := buildAssertionResponse(t, credID, privKey, rawAssertChallenge, svc.rpID, svc.origins[0], 1)

	verified, err := svc.FinishAssertion(userID, assertResp, aSessionData)
	if err != nil {
		t.Fatalf("FinishAssertion: %v", err)
	}
	if !verified {
		t.Fatal("assertion not verified")
	}
}

func TestDeleteAfterRegistration(t *testing.T) {
	svc := newTestService(t)
	userID := "u-del-rt-1"

	challenge, sessionData, _ := svc.BeginRegistration(userID, "Del User")
	var creationOpts struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	json.Unmarshal(challenge, &creationOpts)
	rawChallenge, _ := base64.RawURLEncoding.DecodeString(creationOpts.PublicKey.Challenge)
	privKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	credID := make([]byte, 16)
	rand.Read(credID)
	attestResp := buildAttestationResponse(t, credID, privKey, rawChallenge, svc.rpID, svc.origins[0])
	credentialID, err := svc.FinishRegistration(userID, attestResp, sessionData)
	if err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}

	if err := svc.Delete(userID, credentialID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	creds, _ := svc.List(userID)
	if len(creds) != 0 {
		t.Fatalf("expected 0 creds after delete, got %d", len(creds))
	}
}

// --- Session expiry ---

func TestFinishRegistrationExpiredSession(t *testing.T) {
	svc := newTestService(t)
	userID := "u-exp-1"

	_, sessionData, _ := svc.BeginRegistration(userID, "Expired")

	tok, _ := au12ExtractToken(sessionData)
	svc.mu.Lock()
	svc.sessions[tok].ExpiresAt = time.Now().Add(-time.Second)
	svc.mu.Unlock()

	_, err := svc.FinishRegistration(userID, []byte(`{}`), sessionData)
	if err == nil {
		t.Fatal("expected error for expired session")
	}
}

func TestFinishRegistrationUserMismatch(t *testing.T) {
	svc := newTestService(t)
	_, sessionData, _ := svc.BeginRegistration("u-owner", "Owner")
	_, err := svc.FinishRegistration("u-attacker", []byte(`{}`), sessionData)
	if err == nil {
		t.Fatal("expected error for session userID mismatch")
	}
}

// --- Authenticator simulation helpers ---

// marshalCOSEKey returns the raw public key bytes in COSE format for a P-256 key.
func marshalCOSEKey(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	pub := key.PublicKey
	m := map[int]interface{}{
		1:  2,             // kty: EC2
		3:  -7,            // alg: ES256
		-1: 1,             // crv: P-256
		-2: pub.X.Bytes(), // x
		-3: pub.Y.Bytes(), // y
	}
	b, err := cborMarshal(m)
	if err != nil {
		t.Fatalf("COSE key marshal: %v", err)
	}
	return b
}

// buildAttestationResponse builds a minimal "none" attestation JSON body that
// go-webauthn will accept.
func buildAttestationResponse(
	t *testing.T,
	credID []byte,
	privKey *ecdsa.PrivateKey,
	challenge []byte,
	rpID string,
	origin string,
) []byte {
	t.Helper()

	rpIDHash := sha256Hash([]byte(rpID))
	flags := byte(0x41) // UP | AT
	counter := uint32(0)
	aaguid := make([]byte, 16)

	coseKey := marshalCOSEKey(t, privKey)

	credIDLen := uint16(len(credID))
	atCredData := make([]byte, 0, 16+2+len(credID)+len(coseKey))
	atCredData = append(atCredData, aaguid...)
	atCredData = append(atCredData, byte(credIDLen>>8), byte(credIDLen))
	atCredData = append(atCredData, credID...)
	atCredData = append(atCredData, coseKey...)

	authData := make([]byte, 0, 37+len(atCredData))
	authData = append(authData, rpIDHash[:]...)
	authData = append(authData, flags)
	authData = append(authData, byte(counter>>24), byte(counter>>16), byte(counter>>8), byte(counter))
	authData = append(authData, atCredData...)

	clientDataJSON, _ := json.Marshal(map[string]string{
		"type":      "webauthn.create",
		"challenge": base64.RawURLEncoding.EncodeToString(challenge),
		"origin":    origin,
	})

	attObj, err := cborMarshal(map[string]interface{}{
		"fmt":      "none",
		"attStmt":  map[string]interface{}{},
		"authData": authData,
	})
	if err != nil {
		t.Fatalf("marshal attestation object: %v", err)
	}

	resp := map[string]interface{}{
		"id":    base64.RawURLEncoding.EncodeToString(credID),
		"rawId": base64.RawURLEncoding.EncodeToString(credID),
		"type":  "public-key",
		"response": map[string]string{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientDataJSON),
			"attestationObject": base64.RawURLEncoding.EncodeToString(attObj),
		},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// buildAssertionResponse builds a minimal assertion JSON body.
func buildAssertionResponse(
	t *testing.T,
	credID []byte,
	privKey *ecdsa.PrivateKey,
	challenge []byte,
	rpID string,
	origin string,
	counter uint32,
) []byte {
	t.Helper()

	rpIDHash := sha256Hash([]byte(rpID))
	flags := byte(0x01) // UP

	authData := make([]byte, 37)
	copy(authData[:32], rpIDHash[:])
	authData[32] = flags
	authData[33] = byte(counter >> 24)
	authData[34] = byte(counter >> 16)
	authData[35] = byte(counter >> 8)
	authData[36] = byte(counter)

	clientDataJSON, _ := json.Marshal(map[string]string{
		"type":      "webauthn.get",
		"challenge": base64.RawURLEncoding.EncodeToString(challenge),
		"origin":    origin,
	})

	clientDataHash := sha256Hash(clientDataJSON)
	sigInput := append(authData, clientDataHash[:]...)
	hash := sha256Hash(sigInput)

	r, s, err := ecdsa.Sign(rand.Reader, privKey, hash[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sig := derEncodeECDSA(r, s)

	resp := map[string]interface{}{
		"id":    base64.RawURLEncoding.EncodeToString(credID),
		"rawId": base64.RawURLEncoding.EncodeToString(credID),
		"type":  "public-key",
		"response": map[string]string{
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientDataJSON),
			"authenticatorData": base64.RawURLEncoding.EncodeToString(authData),
			"signature":         base64.RawURLEncoding.EncodeToString(sig),
			"userHandle":        "",
		},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sha256Hash(b []byte) [32]byte {
	return sha256.Sum256(b)
}

// containsBytes reports whether needle appears as a contiguous subsequence of
// haystack (used to assert sealed-at-rest).
func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// derEncodeECDSA produces a minimal DER-encoded ECDSA signature from r, s.
func derEncodeECDSA(r, s *big.Int) []byte {
	rb := bigIntToBytes(r)
	sb := bigIntToBytes(s)

	seq := make([]byte, 0, 6+len(rb)+len(sb))
	seq = append(seq, 0x30) // SEQUENCE
	inner := make([]byte, 0, 4+len(rb)+len(sb))
	inner = append(inner, 0x02, byte(len(rb)))
	inner = append(inner, rb...)
	inner = append(inner, 0x02, byte(len(sb)))
	inner = append(inner, sb...)
	seq = append(seq, byte(len(inner)))
	seq = append(seq, inner...)
	return seq
}

func bigIntToBytes(n *big.Int) []byte {
	b := n.Bytes()
	if len(b) > 0 && b[0]&0x80 != 0 {
		b = append([]byte{0}, b...)
	}
	return b
}

// cborMarshal is a minimal CBOR map encoder for test use only.
func cborMarshal(v interface{}) ([]byte, error) {
	switch val := v.(type) {
	case map[string]interface{}:
		return cborEncodeStringMap(val)
	case map[int]interface{}:
		return cborEncodeIntMap(val)
	default:
		return nil, fmt.Errorf("cborMarshal: unsupported type %T", v)
	}
}

func cborEncodeStringMap(m map[string]interface{}) ([]byte, error) {
	var buf []byte
	buf = append(buf, cborMapHeader(len(m))...)
	for k, v := range m {
		buf = append(buf, cborEncodeString(k)...)
		vb, err := cborEncodeValue(v)
		if err != nil {
			return nil, err
		}
		buf = append(buf, vb...)
	}
	return buf, nil
}

func cborEncodeIntMap(m map[int]interface{}) ([]byte, error) {
	var buf []byte
	buf = append(buf, cborMapHeader(len(m))...)
	for k, v := range m {
		buf = append(buf, cborEncodeInt(k)...)
		vb, err := cborEncodeValue(v)
		if err != nil {
			return nil, err
		}
		buf = append(buf, vb...)
	}
	return buf, nil
}

func cborEncodeValue(v interface{}) ([]byte, error) {
	switch val := v.(type) {
	case string:
		return cborEncodeString(val), nil
	case []byte:
		return cborEncodeBytes(val), nil
	case int:
		return cborEncodeInt(val), nil
	case map[string]interface{}:
		return cborEncodeStringMap(val)
	case map[int]interface{}:
		return cborEncodeIntMap(val)
	default:
		return nil, fmt.Errorf("cborEncodeValue: unsupported type %T", v)
	}
}

func cborMapHeader(n int) []byte {
	if n < 24 {
		return []byte{0xa0 | byte(n)}
	}
	return []byte{0xb8, byte(n)}
}

func cborEncodeString(s string) []byte {
	b := []byte(s)
	var out []byte
	if len(b) < 24 {
		out = append(out, 0x60|byte(len(b)))
	} else {
		out = append(out, 0x78, byte(len(b)))
	}
	return append(out, b...)
}

func cborEncodeBytes(b []byte) []byte {
	var out []byte
	if len(b) < 24 {
		out = append(out, 0x40|byte(len(b)))
	} else {
		out = append(out, 0x58, byte(len(b)))
	}
	return append(out, b...)
}

func cborEncodeInt(n int) []byte {
	if n >= 0 {
		if n < 24 {
			return []byte{byte(n)}
		}
		return []byte{0x18, byte(n)}
	}
	neg := -n - 1
	if neg < 24 {
		return []byte{0x20 | byte(neg)}
	}
	return []byte{0x38, byte(neg)}
}

// Ensure protocol and webauthncose packages are used (imported for their types).
var _ = protocol.AuthenticatorAttachment("")
var _ = webauthncose.EC2PublicKeyData{}

// fmt is used in cborMarshal error messages; keep the import referenced.
var _ = filepath.Base
