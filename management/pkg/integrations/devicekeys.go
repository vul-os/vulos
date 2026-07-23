package integrations

// devicekeys.go — INTEG-SEC-01: per-device public-key registry + verification.
//
// The token mint (routes_integrations_token.go) is authenticated by the
// fleet-wide DEVICE_SHARED_SECRET, which only proves "caller is a fleet box".
// To prove "caller is THIS box" we pin each device ULID to the public half of
// its on-box identity key (devicekey.KeyStore, ECDSA P-256) and require a
// per-device signature on mint.
//
// Trust-on-first-use: the first registration for a ULID wins. A registration
// presenting a different key for an already-pinned ULID is rejected with
// ErrDeviceKeyConflict (a tamper signal worth auditing). Possession of the
// private key is proven at registration via a self-signature.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// AlgoECDSAP256 is the only device-key algorithm currently supported (matches
// the box devicekey.KeyStore identity key).
const AlgoECDSAP256 = "ecdsa-p256"

var (
	// ErrNoDeviceKey is returned by DeviceKeyStore.Get when no key is pinned.
	ErrNoDeviceKey = errors.New("integrations: no device key pinned")
	// ErrDeviceKeyConflict is returned by Pin when a DIFFERENT key is already
	// pinned for the ULID (trust-on-first-use violation / tamper signal).
	ErrDeviceKeyConflict = errors.New("integrations: device key conflict")
	// ErrBadDeviceKey is returned when a pubkey/signature is malformed.
	ErrBadDeviceKey = errors.New("integrations: malformed device key or signature")
)

// DeviceKey is a pinned device identity public key.
type DeviceKey struct {
	ULID      string
	PubKeyDER []byte // PKIX DER
	Algo      string
	CreatedAt time.Time
}

// DeviceKeyStore persists the ULID→device-pubkey pinning. SQLDeviceKeyStore is
// the production impl; MemDeviceKeyStore is the test double — both semantically
// identical.
type DeviceKeyStore interface {
	// Pin records ulid→pubKeyDER on first use. If a key is already pinned:
	//   - identical bytes → idempotent success, pinned=false
	//   - different bytes → ErrDeviceKeyConflict
	Pin(ctx context.Context, ulid string, pubKeyDER []byte, algo string) (pinned bool, err error)
	// Get returns the pinned key for ulid, or ErrNoDeviceKey.
	Get(ctx context.Context, ulid string) (DeviceKey, error)
	// Delete un-pins ulid's key (owner-authed rotation/revocation). No error if
	// absent — idempotent. After deletion the box may re-register a fresh key.
	Delete(ctx context.Context, ulid string) error
}

// RegisterSigMessage is the purpose-bound message a box signs (with its device
// private key) to prove possession at registration time. The box and CP must
// agree on this exact string.
func RegisterSigMessage(ulid string) string { return "integrations:register:" + ulid }

// ParseECDSAPublicKey parses PKIX DER bytes into an ECDSA P-256 public key.
// Non-ECDSA keys and curves other than P-256 are rejected (the box device
// identity key is always P-256).
func ParseECDSAPublicKey(der []byte) (*ecdsa.PublicKey, error) {
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, ErrBadDeviceKey
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, ErrBadDeviceKey
	}
	return ec, nil
}

// VerifyDeviceSig verifies an ASN.1-DER ECDSA signature over sha256(message)
// against the PKIX-DER public key. Returns false on any malformation.
func VerifyDeviceSig(pubKeyDER []byte, message string, sigASN1 []byte) bool {
	pub, err := ParseECDSAPublicKey(pubKeyDER)
	if err != nil {
		return false
	}
	digest := sha256.Sum256([]byte(message))
	return ecdsa.VerifyASN1(pub, digest[:], sigASN1)
}

// ---------------------------------------------------------------------------
// SQLDeviceKeyStore
// ---------------------------------------------------------------------------

// SQLDeviceKeyStore is the cpdb-backed DeviceKeyStore. It shares no state with
// the connection SQLStore beyond the same DB handle (table device_pubkeys).
type SQLDeviceKeyStore struct {
	db *cpdb.DB
	mu sync.Mutex
}

// Pin implements DeviceKeyStore. First-write-wins (trust-on-first-use).
func (s *SQLDeviceKeyStore) Pin(ctx context.Context, ulid string, pubKeyDER []byte, algo string) (bool, error) {
	if ulid == "" || len(pubKeyDER) == 0 {
		return false, ErrBadDeviceKey
	}
	if algo == "" {
		algo = AlgoECDSAP256
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.get(ctx, ulid)
	if err == nil {
		if bytes.Equal(existing.PubKeyDER, pubKeyDER) {
			return false, nil // idempotent re-registration
		}
		return false, ErrDeviceKeyConflict
	}
	if !errors.Is(err, ErrNoDeviceKey) {
		return false, err
	}
	if _, err := s.db.ExecContext(ctx,
		s.db.Rebind(`INSERT INTO device_pubkeys (ulid, pubkey_der, algo, created_at) VALUES (?, ?, ?, ?)`),
		ulid, pubKeyDER, algo, time.Now().Unix()); err != nil {
		return false, fmt.Errorf("integrations: pin device key: %w", err)
	}
	return true, nil
}

// Get implements DeviceKeyStore.
func (s *SQLDeviceKeyStore) Get(ctx context.Context, ulid string) (DeviceKey, error) {
	return s.get(ctx, ulid)
}

// Delete implements DeviceKeyStore.
func (s *SQLDeviceKeyStore) Delete(ctx context.Context, ulid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(ctx, s.db.Rebind(`DELETE FROM device_pubkeys WHERE ulid = ?`), ulid); err != nil {
		return fmt.Errorf("integrations: delete device key: %w", err)
	}
	return nil
}

func (s *SQLDeviceKeyStore) get(ctx context.Context, ulid string) (DeviceKey, error) {
	var dk DeviceKey
	var createdAt int64
	err := s.db.QueryRowContext(ctx,
		s.db.Rebind(`SELECT ulid, pubkey_der, algo, created_at FROM device_pubkeys WHERE ulid = ?`), ulid,
	).Scan(&dk.ULID, &dk.PubKeyDER, &dk.Algo, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceKey{}, ErrNoDeviceKey
	}
	if err != nil {
		return DeviceKey{}, fmt.Errorf("integrations: get device key: %w", err)
	}
	dk.CreatedAt = time.Unix(createdAt, 0).UTC()
	return dk, nil
}

// ---------------------------------------------------------------------------
// MemDeviceKeyStore
// ---------------------------------------------------------------------------

// MemDeviceKeyStore is the in-memory DeviceKeyStore (tests + dev fallback).
type MemDeviceKeyStore struct {
	mu   sync.Mutex
	keys map[string]DeviceKey
}

// NewMemDeviceKeyStore returns an empty in-memory device-key store.
func NewMemDeviceKeyStore() *MemDeviceKeyStore {
	return &MemDeviceKeyStore{keys: make(map[string]DeviceKey)}
}

// Pin implements DeviceKeyStore.
func (m *MemDeviceKeyStore) Pin(_ context.Context, ulid string, pubKeyDER []byte, algo string) (bool, error) {
	if ulid == "" || len(pubKeyDER) == 0 {
		return false, ErrBadDeviceKey
	}
	if algo == "" {
		algo = AlgoECDSAP256
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ex, ok := m.keys[ulid]; ok {
		if bytes.Equal(ex.PubKeyDER, pubKeyDER) {
			return false, nil
		}
		return false, ErrDeviceKeyConflict
	}
	cp := make([]byte, len(pubKeyDER))
	copy(cp, pubKeyDER)
	m.keys[ulid] = DeviceKey{ULID: ulid, PubKeyDER: cp, Algo: algo, CreatedAt: time.Now().UTC()}
	return true, nil
}

// Get implements DeviceKeyStore.
func (m *MemDeviceKeyStore) Get(_ context.Context, ulid string) (DeviceKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dk, ok := m.keys[ulid]
	if !ok {
		return DeviceKey{}, ErrNoDeviceKey
	}
	return dk, nil
}

// Delete implements DeviceKeyStore.
func (m *MemDeviceKeyStore) Delete(_ context.Context, ulid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.keys, ulid)
	return nil
}
