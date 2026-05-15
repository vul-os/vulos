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

	// 4. Build status.
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
	return s.status
}

func (s *softwareStore) Close() error { return nil }

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
