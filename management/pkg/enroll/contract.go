// Package enroll is the vulos.cloud control-plane bare-metal enrollment
// subsystem (see ../../BOOT_API.md and CLOUD_BUILDOUT_PLAN.md Surface 11).
//
// THIS FILE IS THE FROZEN CONTRACT. Do not edit it in feature branches —
// every enrollment agent builds against these types, this Store interface, the
// reference MemStore (which DEFINES Store semantics), the reference
// MemCertSigner (which DEFINES CertSigner semantics), and GenUserCode.
// The SQL Store implementation must match MemStore behaviourally; handlers
// depend only on the interfaces.
package enroll

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Domain types
// ─────────────────────────────────────────────────────────────────────────────

// EnrollGrant is the response to POST /enroll/start (RFC 8628 Device
// Authorization Grant). The device_code is secret (high-entropy, device-only);
// the user_code is short and human-typable.
type EnrollGrant struct {
	DeviceCode      string    `json:"device_code"`
	UserCode        string    `json:"user_code"`
	VerificationURI string    `json:"verification_uri"`
	Interval        int       `json:"interval"`
	ExpiresIn       int       `json:"expires_in"`
	ExpiresAt       time.Time `json:"-"`
}

// EnrollResult is the success payload for POST /enroll/poll once the user
// has approved the grant. It carries fleet management credentials only —
// never data-plane access material.
type EnrollResult struct {
	ULID       string `json:"ulid"`
	DeviceCert []byte `json:"device_cert"`
	AccountID  string `json:"account_id"`
}

// grantState is the internal lifecycle state of a grant.
type grantState string

const (
	statePending  grantState = "pending"
	stateApproved grantState = "approved"
	stateDenied   grantState = "denied"
	stateConsumed grantState = "consumed"
)

// grant is the internal store record — not exported to callers.
type grant struct {
	DeviceCode   string
	UserCode     string
	State        grantState
	AccountID    string // set on approval
	DevicePubKey []byte
	DeviceCert   []byte // set on approval after signing
	ULID         string // set on approval
	ExpiresAt    time.Time
	ApprovedAt   *time.Time
}

// ─────────────────────────────────────────────────────────────────────────────
// Sentinel errors
// ─────────────────────────────────────────────────────────────────────────────

var (
	// ErrAuthPending is returned by PollGrant when the user has not yet acted.
	ErrAuthPending = errors.New("enroll: authorization_pending")

	// ErrSlowDown is returned when the device is polling too frequently.
	ErrSlowDown = errors.New("enroll: slow_down")

	// ErrExpired is returned when the grant's TTL has elapsed.
	ErrExpired = errors.New("enroll: expired_token")

	// ErrAccessDenied is returned by PollGrant when the user denied the grant.
	ErrAccessDenied = errors.New("enroll: access_denied")

	// ErrUnknownDeviceCode is returned when no grant matches the device_code.
	ErrUnknownDeviceCode = errors.New("enroll: unknown device_code")

	// ErrUnknownUserCode is returned when no grant matches the user_code.
	ErrUnknownUserCode = errors.New("enroll: unknown user_code")

	// ErrInvalidPubKey is returned when the device public key is malformed or
	// the wrong length.
	ErrInvalidPubKey = errors.New("enroll: invalid public key")

	// ErrAlreadyConsumed is returned by PollGrant when the result was already
	// retrieved and the grant has been consumed.
	ErrAlreadyConsumed = errors.New("enroll: already_consumed")
)

// ─────────────────────────────────────────────────────────────────────────────
// Store interface
// ─────────────────────────────────────────────────────────────────────────────

// Store is the enrollment persistence contract. The SQL implementation and
// MemStore both satisfy it; handlers depend ONLY on this interface.
// MemStore is the behavioural source of truth.
type Store interface {
	// StartGrant persists a new pending grant for the given device public key.
	// Returns ErrInvalidPubKey if pubKey is not a 32-byte ed25519 public key.
	StartGrant(ctx context.Context, g EnrollGrant, pubKey []byte) error

	// PollGrant checks the state of the grant identified by deviceCode.
	//   - Pending → ErrAuthPending
	//   - Denied → ErrAccessDenied
	//   - Consumed → ErrAlreadyConsumed
	//   - Expired (any state) → ErrExpired
	//   - Approved → marks consumed, returns EnrollResult
	//   - Unknown device_code → ErrUnknownDeviceCode
	PollGrant(ctx context.Context, deviceCode string) (EnrollResult, error)

	// ApproveGrant transitions the grant identified by userCode to approved,
	// records the accountID, ULID, and signed device cert. The CertSigner call
	// happens in the handler before ApproveGrant is invoked.
	//   - Unknown user_code → ErrUnknownUserCode
	//   - Expired → ErrExpired
	ApproveGrant(ctx context.Context, userCode, accountID, ulid string, deviceCert []byte) error

	// DenyGrant transitions the grant identified by userCode to denied.
	//   - Unknown user_code → ErrUnknownUserCode
	//   - Expired → ErrExpired
	DenyGrant(ctx context.Context, userCode string) error

	// GetByUserCode returns the grant identified by userCode. Used by the
	// approve/deny handler to display device info to the logged-in user.
	//   - Unknown user_code → ErrUnknownUserCode
	GetByUserCode(ctx context.Context, userCode string) (EnrollGrant, error)

	Close() error
}

// ─────────────────────────────────────────────────────────────────────────────
// CertSigner interface
// ─────────────────────────────────────────────────────────────────────────────

// CertSigner signs device enrollment certificates. The management-scope cert
// is an ed25519 signature over a canonical payload encoding the device's
// public key, account ID, and ULID. It deliberately grants NO data access.
type CertSigner interface {
	// SignDeviceCert signs a management-scope device cert. pubKey must be a
	// 32-byte ed25519 public key. Returns the raw cert bytes (base64 in JSON).
	SignDeviceCert(pubKey []byte, accountID, ulid string) ([]byte, error)
}

// ─────────────────────────────────────────────────────────────────────────────
// GenUserCode — ambiguous-charset-safe 8-character code
// ─────────────────────────────────────────────────────────────────────────────

// userCodeAlphabet excludes visually ambiguous characters: 0 O 1 l I.
// The resulting 30-character alphabet is safe for hand-transcription.
const userCodeAlphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ"

// GenUserCode returns a random 8-character code in "XXXX-XXXX" format,
// drawn from userCodeAlphabet (no 0/O/1/l/I). Panics on crypto/rand failure.
func GenUserCode() string {
	alphabet := []byte(userCodeAlphabet)
	n := len(alphabet)
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic("enroll: GenUserCode rand.Read: " + err.Error())
	}
	out := make([]byte, 9) // 4 + dash + 4
	for i := 0; i < 4; i++ {
		out[i] = alphabet[int(buf[i])%n]
	}
	out[4] = '-'
	for i := 0; i < 4; i++ {
		out[5+i] = alphabet[int(buf[4+i])%n]
	}
	return string(out)
}

// ─────────────────────────────────────────────────────────────────────────────
// genDeviceCode — high-entropy opaque secret for the device
// ─────────────────────────────────────────────────────────────────────────────

func genDeviceCode() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("enroll: genDeviceCode rand.Read: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// ─────────────────────────────────────────────────────────────────────────────
// MemStore — in-memory reference implementation
// ─────────────────────────────────────────────────────────────────────────────

// MemStore is the in-memory reference Store. Concurrency-safe. It DEFINES the
// semantics every other implementation must match.
type MemStore struct {
	mu   sync.Mutex
	byDC map[string]*grant // device_code -> grant
	byUC map[string]*grant // user_code   -> grant
}

// NewMemStore returns an empty reference store.
func NewMemStore() *MemStore {
	return &MemStore{
		byDC: map[string]*grant{},
		byUC: map[string]*grant{},
	}
}

func (m *MemStore) StartGrant(_ context.Context, g EnrollGrant, pubKey []byte) error {
	if len(pubKey) != ed25519.PublicKeySize {
		return ErrInvalidPubKey
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r := &grant{
		DeviceCode:   g.DeviceCode,
		UserCode:     g.UserCode,
		State:        statePending,
		DevicePubKey: pubKey,
		ExpiresAt:    g.ExpiresAt,
	}
	m.byDC[g.DeviceCode] = r
	m.byUC[g.UserCode] = r
	return nil
}

func (m *MemStore) PollGrant(_ context.Context, deviceCode string) (EnrollResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.byDC[deviceCode]
	if !ok {
		return EnrollResult{}, ErrUnknownDeviceCode
	}
	if time.Now().UTC().After(r.ExpiresAt) {
		return EnrollResult{}, ErrExpired
	}
	switch r.State {
	case statePending:
		return EnrollResult{}, ErrAuthPending
	case stateDenied:
		return EnrollResult{}, ErrAccessDenied
	case stateConsumed:
		return EnrollResult{}, ErrAlreadyConsumed
	case stateApproved:
		r.State = stateConsumed
		return EnrollResult{
			ULID:       r.ULID,
			DeviceCert: r.DeviceCert,
			AccountID:  r.AccountID,
		}, nil
	}
	return EnrollResult{}, ErrAuthPending
}

func (m *MemStore) ApproveGrant(_ context.Context, userCode, accountID, ulid string, deviceCert []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.byUC[userCode]
	if !ok {
		return ErrUnknownUserCode
	}
	if time.Now().UTC().After(r.ExpiresAt) {
		return ErrExpired
	}
	now := time.Now().UTC()
	r.State = stateApproved
	r.AccountID = accountID
	r.ULID = ulid
	r.DeviceCert = deviceCert
	r.ApprovedAt = &now
	return nil
}

func (m *MemStore) DenyGrant(_ context.Context, userCode string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.byUC[userCode]
	if !ok {
		return ErrUnknownUserCode
	}
	if time.Now().UTC().After(r.ExpiresAt) {
		return ErrExpired
	}
	r.State = stateDenied
	return nil
}

func (m *MemStore) GetByUserCode(_ context.Context, userCode string) (EnrollGrant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.byUC[userCode]
	if !ok {
		return EnrollGrant{}, ErrUnknownUserCode
	}
	return EnrollGrant{
		DeviceCode: r.DeviceCode,
		UserCode:   r.UserCode,
		ExpiresAt:  r.ExpiresAt,
	}, nil
}

func (m *MemStore) Close() error { return nil }

// compile-time assertion: MemStore satisfies Store.
var _ Store = (*MemStore)(nil)

// ─────────────────────────────────────────────────────────────────────────────
// MemCertSigner — reference CertSigner with a generated test key
// ─────────────────────────────────────────────────────────────────────────────

// MemCertSigner implements CertSigner with an ephemeral ed25519 key pair.
// Intended for tests. The key is generated once at construction; never
// persisted. The cert payload is:
//
//	"vulos-device-cert:v1:<pubKeyBase64>:<accountID>:<ulid>"
//
// signed with ed25519. The raw signature bytes are the cert.
type MemCertSigner struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

// NewMemCertSigner returns a MemCertSigner with a freshly generated ed25519
// key pair.
func NewMemCertSigner() *MemCertSigner {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic("enroll: MemCertSigner key gen: " + err.Error())
	}
	return &MemCertSigner{priv: priv, pub: pub}
}

// SignDeviceCert signs a management-scope device cert. pubKey must be 32 bytes
// (ed25519 public key).
func (s *MemCertSigner) SignDeviceCert(pubKey []byte, accountID, ulid string) ([]byte, error) {
	if len(pubKey) != ed25519.PublicKeySize {
		return nil, ErrInvalidPubKey
	}
	payload := fmt.Sprintf("vulos-device-cert:v1:%s:%s:%s",
		base64.RawURLEncoding.EncodeToString(pubKey), accountID, ulid)
	sig := ed25519.Sign(s.priv, []byte(payload))
	return sig, nil
}

// PublicKeyBytes returns a copy of the CA public key (for verification in tests).
func (s *MemCertSigner) PublicKeyBytes() []byte {
	b := make([]byte, len(s.pub))
	copy(b, s.pub)
	return b
}

// compile-time assertion: MemCertSigner satisfies CertSigner.
var _ CertSigner = (*MemCertSigner)(nil)
