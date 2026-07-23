package devicekey

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// softwareStore is a KeyStore backed by AES-256-GCM encryption.
// The wrapping key (wrapKey) is stored encrypted on disk; it is itself
// protected by a device-specific secret derived from the machine-ID file
// (falling back to a randomly generated secret persisted alongside the key).
//
// Layout under keyDir:
//
//	device_key.priv   — PKCS#8 DER of the ECDSA P-256 identity key, AES-GCM encrypted
//	device_key.pub    — PKIX DER of the public half (plaintext, for quick reads)
//	wrap.key          — AES-256 wrapping key, AES-GCM encrypted with the device secret
//	device.secret     — 32-byte random device secret (created once, never changed)
type softwareStore struct {
	mu      sync.Mutex
	keyDir  string
	wrapKey []byte // 32-byte AES-256 key used for Seal/Unseal
	privKey *ecdsa.PrivateKey
	status  Status

	// ── Rotation overlap (mirrors FABRIC-KEY-01) ─────────────────────────────
	// prevPubKeyDER + prevExpiresAt record the identity key rotated AWAY FROM,
	// so a signature made just before a rotation (still propagating) can be
	// verified by a caller that checks both the current and previous key
	// until the grace window closes. Empty/zero when no rotation has occurred.
	prevPubKeyDER []byte
	prevExpiresAt time.Time
}

// openSoftware initialises the software key store, creating key material on
// first run.
func openSoftware(keyDir string) (*softwareStore, error) {
	s := &softwareStore{keyDir: keyDir}

	// 1. Load or generate the device secret.
	secret, err := s.loadOrCreateSecret()
	if err != nil {
		return nil, fmt.Errorf("devicekey/software: device secret: %w", err)
	}

	// 2. Load or generate the wrapping key.
	wrapKey, err := s.loadOrCreateWrapKey(secret)
	if err != nil {
		return nil, fmt.Errorf("devicekey/software: wrap key: %w", err)
	}
	s.wrapKey = wrapKey

	// 3. Load or generate the ECDSA identity key.
	priv, err := s.loadOrCreateIdentityKey(wrapKey)
	if err != nil {
		return nil, fmt.Errorf("devicekey/software: identity key: %w", err)
	}
	s.privKey = priv

	// 4. Load any rotation-overlap state left from a previous rotation.
	s.prevPubKeyDER, s.prevExpiresAt = loadPrevKeyState(keyDir)

	// 5. Build status.
	pub, _ := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	h := sha256.Sum256(pub)
	s.status = Status{
		Backend:   BackendSoftware,
		Available: true,
		KeyDir:    keyDir,
		DeviceID:  hex.EncodeToString(h[:16]), // 16-byte prefix for readability
	}

	return s, nil
}

// --- KeyStore implementation ---

func (s *softwareStore) Seal(plaintext []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return aesGCMEncrypt(s.wrapKey, plaintext)
}

func (s *softwareStore) Unseal(ciphertext []byte) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return aesGCMDecrypt(s.wrapKey, ciphertext)
}

func (s *softwareStore) Sign(digest []byte, _ crypto.Hash) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pubDER, err := x509.MarshalPKIXPublicKey(&s.privKey.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("devicekey/software: Sign: marshal active pubkey: %w", err)
	}
	// SELF FAIL-CLOSED (see revocation.go): a revoked active key mints no
	// further signatures, regardless of who is asking (integrations
	// device-signer mint, self-revoke, self-rotate, ...).
	if err := assertActiveKeyNotRevoked(pubDER); err != nil {
		return nil, err
	}
	return ecdsa.SignASN1(rand.Reader, s.privKey, digest)
}

func (s *softwareStore) DeviceIdentity() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return x509.MarshalPKIXPublicKey(&s.privKey.PublicKey)
}

func (s *softwareStore) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status
	if pub, err := x509.MarshalPKIXPublicKey(&s.privKey.PublicKey); err == nil {
		st.Revoked = IsDeviceKeyRevoked(Fingerprint(pub))
	}
	return st
}

func (s *softwareStore) Close() error { return nil }

// --- rotation (device-key lifecycle) ---

// Rotate implements KeyStore.Rotate: a NORMAL, self-authorized rotation. The
// CURRENT key signs the RotationCert binding old→new BEFORE the new key
// becomes active, so the signature genuinely proves possession of the key
// being retired.
func (s *softwareStore) Rotate(reason string) (*RotationCert, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldPriv := s.privKey
	oldPubDER, err := x509.MarshalPKIXPublicKey(&oldPriv.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("devicekey/software: Rotate: marshal old pubkey: %w", err)
	}
	// SELF FAIL-CLOSED (see revocation.go): normal rotation signs with the
	// CURRENT (old) key to prove possession — meaningless if that key is
	// already revoked (self-revoked, or break-glass-revoked by fleet
	// quorum). A box in that state must use BreakGlassRotate instead, which
	// never relies on the revoked key's signature.
	if err := assertActiveKeyNotRevoked(oldPubDER); err != nil {
		return nil, err
	}

	newPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("devicekey/software: Rotate: generate new key: %w", err)
	}
	newPubDER, err := x509.MarshalPKIXPublicKey(&newPriv.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("devicekey/software: Rotate: marshal new pubkey: %w", err)
	}

	cert := newRotationCert(RotationMethodSelf, oldPubDER, newPubDER, reason)
	digest, err := rotationSigningDigest(cert)
	if err != nil {
		return nil, fmt.Errorf("devicekey/software: Rotate: digest: %w", err)
	}
	sig, err := ecdsa.SignASN1(rand.Reader, oldPriv, digest)
	if err != nil {
		return nil, fmt.Errorf("devicekey/software: Rotate: sign with old key: %w", err)
	}
	cert.setSig(sig)

	if err := s.installNewIdentity(newPriv, oldPubDER); err != nil {
		return nil, fmt.Errorf("devicekey/software: Rotate: install new identity: %w", err)
	}
	return cert, nil
}

// forceInstallIdentity implements the unexported KeyStore primitive: install
// newPriv unconditionally, no old-key signature required. Only reachable from
// BreakGlassRotate (rotation.go), which enforces the quorum check first.
func (s *softwareStore) forceInstallIdentity(newPriv *ecdsa.PrivateKey) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	oldPubDER, err := x509.MarshalPKIXPublicKey(&s.privKey.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("devicekey/software: forceInstallIdentity: marshal old pubkey: %w", err)
	}
	if err := s.installNewIdentity(newPriv, oldPubDER); err != nil {
		return nil, fmt.Errorf("devicekey/software: forceInstallIdentity: %w", err)
	}
	return oldPubDER, nil
}

// installNewIdentity persists newPriv as the active identity key, records
// oldPubDER as the previous key with a grace-window expiry, and switches the
// in-memory signer. Caller must hold s.mu.
func (s *softwareStore) installNewIdentity(newPriv *ecdsa.PrivateKey, oldPubDER []byte) error {
	der, err := x509.MarshalECPrivateKey(newPriv)
	if err != nil {
		return fmt.Errorf("marshal new private key: %w", err)
	}
	enc, err := aesGCMEncrypt(s.wrapKey, der)
	if err != nil {
		return fmt.Errorf("encrypt new private key: %w", err)
	}
	if err := atomicWrite(s.privKeyPath(), enc, 0600); err != nil {
		return fmt.Errorf("persist new private key: %w", err)
	}
	newPubDER, err := x509.MarshalPKIXPublicKey(&newPriv.PublicKey)
	if err != nil {
		return fmt.Errorf("marshal new public key: %w", err)
	}
	_ = atomicWrite(s.pubKeyPath(), newPubDER, 0644) // best-effort, mirrors loadOrCreateIdentityKey

	expiresAt := writePrevKeyState(s.keyDir, oldPubDER)

	s.privKey = newPriv
	s.prevPubKeyDER = append([]byte(nil), oldPubDER...)
	s.prevExpiresAt = expiresAt

	h := sha256.Sum256(newPubDER)
	s.status.DeviceID = hex.EncodeToString(h[:16])
	return nil
}

// PreviousIdentity returns the identity key rotated AWAY FROM and its grace
// expiry, or (nil, zero-time) if no rotation overlap is currently active
// (either no rotation has occurred, or the grace window has closed).
func (s *softwareStore) PreviousIdentity() ([]byte, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.prevPubKeyDER) == 0 || time.Now().UTC().After(s.prevExpiresAt) {
		return nil, time.Time{}
	}
	return append([]byte(nil), s.prevPubKeyDER...), s.prevExpiresAt
}

// --- internal helpers ---

func (s *softwareStore) secretPath() string  { return filepath.Join(s.keyDir, "device.secret") }
func (s *softwareStore) wrapKeyPath() string { return filepath.Join(s.keyDir, "wrap.key") }
func (s *softwareStore) privKeyPath() string { return filepath.Join(s.keyDir, "device_key.priv") }
func (s *softwareStore) pubKeyPath() string  { return filepath.Join(s.keyDir, "device_key.pub") }

// loadOrCreateSecret returns the 32-byte device secret, generating and
// persisting it if it does not yet exist.
func (s *softwareStore) loadOrCreateSecret() ([]byte, error) {
	path := s.secretPath()
	if data, err := os.ReadFile(path); err == nil && len(data) == 32 {
		return data, nil
	}
	secret := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, secret); err != nil {
		return nil, err
	}
	if err := atomicWrite(path, secret, 0600); err != nil {
		return nil, err
	}
	return secret, nil
}

// loadOrCreateWrapKey returns the AES-256 wrapping key.  The key is stored on
// disk encrypted with the device secret so that it survives restarts.
func (s *softwareStore) loadOrCreateWrapKey(secret []byte) ([]byte, error) {
	path := s.wrapKeyPath()
	if enc, err := os.ReadFile(path); err == nil {
		return aesGCMDecrypt(secret, enc)
	}

	// Generate a new wrap key.
	wrapKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, wrapKey); err != nil {
		return nil, err
	}
	enc, err := aesGCMEncrypt(secret, wrapKey)
	if err != nil {
		return nil, err
	}
	if err := atomicWrite(path, enc, 0600); err != nil {
		return nil, err
	}
	return wrapKey, nil
}

// loadOrCreateIdentityKey loads the ECDSA P-256 identity private key from disk,
// creating and persisting a new one if absent.
func (s *softwareStore) loadOrCreateIdentityKey(wrapKey []byte) (*ecdsa.PrivateKey, error) {
	privPath := s.privKeyPath()

	if enc, err := os.ReadFile(privPath); err == nil {
		der, err := aesGCMDecrypt(wrapKey, enc)
		if err != nil {
			return nil, fmt.Errorf("decrypt identity key: %w", err)
		}
		priv, err := x509.ParseECPrivateKey(der)
		if err != nil {
			return nil, fmt.Errorf("parse identity key: %w", err)
		}
		return priv, nil
	}

	// Generate.
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	enc, err := aesGCMEncrypt(wrapKey, der)
	if err != nil {
		return nil, err
	}
	if err := atomicWrite(privPath, enc, 0600); err != nil {
		return nil, err
	}

	// Persist the public key in plaintext for quick identity checks.
	pub, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, err
	}
	// best-effort; failure is non-fatal
	_ = atomicWrite(s.pubKeyPath(), pub, 0644)

	return priv, nil
}

// --- persistence helpers ---

// atomicWrite writes data to path via a temp file then renames it to achieve
// an atomic update. Permissions are applied before the rename.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// --- AES-256-GCM helpers ---

// envelope is the wire format for sealed data: a JSON wrapper that carries
// the nonce and ciphertext so the format is self-describing and extensible.
type envelope struct {
	Version    int    `json:"v"`
	Nonce      []byte `json:"n"`
	Ciphertext []byte `json:"c"`
}

func aesGCMEncrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)
	env := envelope{Version: 1, Nonce: nonce, Ciphertext: ct}
	return json.Marshal(env)
}

func aesGCMDecrypt(key, data []byte) ([]byte, error) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("bad envelope: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, env.Nonce, env.Ciphertext, nil)
}
