// Package pairing implements SECURE "approve a new device from an existing
// trusted device". An already-enrolled, authenticated OWNER device (gated by
// owner role + step-up at the HTTP layer) issues a short-lived, single-use,
// SIGNED pairing ticket (short-code + QR). A brand-new device redeems that
// ticket to ENROL — recording its OWN, locally-generated device key — and is
// then a trusted member of the fleet.
//
// # What the ticket carries (and, crucially, what it does NOT)
//
//   - The QR / short-code carries ONLY a high-entropy, single-use, short-TTL
//     bearer token, a box signature over the ticket, and non-secret connection
//     HINTS (endpoint, box id, expiry). It contains NO storage credentials
//     (no access key, no secret key) and NO data-encryption passphrase.
//   - Redeeming the ticket NEVER copies raw storage secrets. The old joincode
//     flow embedded the raw S3 access/secret keys directly in the scannable
//     code — a long-lived secret at rest in a photograph. pairing refuses to
//     do that: it does not even parse the secret fields of storage.json into
//     memory (see storageHints). Minting per-device, scoped, TTL'd storage
//     credentials is a separate downstream seam (cloud secure-provisioning);
//     pairing's job is to establish TRUST in the new device's own key.
//   - The new device generates its OWN device identity key locally and submits
//     only its PUBLIC key. It never receives, and never shares, the owner
//     device's key. Its public key's fingerprint (the same SHA-256 scheme
//     services/devicekey uses) becomes its stable DeviceID, so the identical
//     identifier that pairing enrols is the one a compromise-removal
//     (devicekey revocation) later locks out.
//
// # Why the ticket is SIGNED
//
// Every ticket is signed by a per-box Ed25519 pairing-authority key (persisted
// under the db dir, generated on first use). The signature gives three
// properties:
//
//  1. Tamper-evidence for the QR: a payload whose token, expiry, issuer or
//     hints were altered in transit fails VerifyTicket.
//  2. On-disk integrity: Claim re-verifies the STORED ticket's signature before
//     honouring it, so hand-editing pairing-tickets.json cannot forge a valid
//     ticket (ErrTampered).
//  3. A verifiable enrolment attestation: each PairedDevice records an
//     ApprovalSig — the box's signed statement that this device key was
//     approved via pairing by IssuerDeviceID at PairedAt. This is the
//     single-box analogue of a fleetid vouch (peer-quorum vouching applies to
//     the break-glass / compromise-removal path, not to owner-authorised
//     pairing, whose trust anchor is the owner session + step-up).
//
// # Fail-closed posture
//
// Every ambiguous state is a denial: an unknown/expired/consumed token, a
// malformed device key, a tampered ticket, or a self-pair attempt (the new
// device's key equals the issuing device's key) all refuse. Tickets are
// single-use — consumed atomically before enrolment is recorded — and pruned
// on each Issue.
package pairing

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"vulos/backend/services/signing"
)

// Crockford base32 alphabet — omits 0/O/1/I/L to avoid visual confusion when a
// human reads a short-code aloud.
const crockfordAlphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ"

// Store / key file names under the db directory.
const (
	ticketsFileName  = "pairing-tickets.json"
	devicesFileName  = "paired-devices.json"
	storageFileName  = "storage.json"
	authorityKeyFile = "pairing-authority.key" // Ed25519 seed, mode 0600
)

// DefaultTTL is the lifetime of a freshly-issued pairing ticket when the caller
// does not specify one. Short by design — a device is paired in one sitting.
const DefaultTTL = 15 * time.Minute

// MaxTTL caps a caller-supplied TTL. A pairing ticket is a bearer credential; it
// must not be long-lived.
const MaxTTL = time.Hour

// maxPairedDevices bounds the audit store as a DoS backstop. Generous relative
// to any real personal fleet.
const maxPairedDevices = 512

// Sentinel errors so the HTTP layer can map precise status codes.
var (
	// ErrExpired — the ticket exists but its TTL has passed.
	ErrExpired = errors.New("pairing: ticket expired")
	// ErrInvalid — the token is unknown (never issued, or already consumed).
	ErrInvalid = errors.New("pairing: ticket not found or already used")
	// ErrBadDeviceKey — the submitted device public key is missing/malformed.
	ErrBadDeviceKey = errors.New("pairing: device public key missing or malformed")
	// ErrTampered — the ticket's signature does not verify against this box's
	// pairing-authority key (a forged QR, or a hand-edited tickets file).
	ErrTampered = errors.New("pairing: ticket signature does not verify (tampered or forged)")
	// ErrSelfPair — the claiming device's key is the SAME key that issued the
	// ticket. A device may not "approve itself" as a brand-new device — the
	// pairing analogue of fleetid's self-vouch exclusion.
	ErrSelfPair = errors.New("pairing: refusing to pair a device with itself (issuer == claimant)")
	// ErrUnknownDevice — no paired device with the given DeviceID.
	ErrUnknownDevice = errors.New("pairing: no such paired device")
)

// mu guards read-modify-write on the on-disk JSON stores in this package.
var mu sync.Mutex

// authMu guards load-or-create of the pairing-authority key (a separate lock so
// authority-key access never has to nest inside mu).
var authMu sync.Mutex

// ─── types ──────────────────────────────────────────────────────────────────

// Ticket is a pending pairing authorisation. Sig is an Ed25519 signature by the
// box's pairing-authority key over the ticket's canonical bytes (Sig cleared).
type Ticket struct {
	Token          string    `json:"token"`            // high-entropy single-use bearer secret
	ShortCode      string    `json:"short_code"`       // human-readable label
	IssuerDeviceID string    `json:"issuer_device_id"` // fingerprint of the approving (owner) device
	DeviceName     string    `json:"device_name"`      // optional label for the device being added
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	Sig            string    `json:"sig"` // base64url Ed25519 over canonical(ticket w/o Sig)
}

// ConnectionHints are the NON-SECRET fields a new device needs to locate the
// shared bucket. Deliberately no access/secret key — see the package doc.
type ConnectionHints struct {
	Bucket   string `json:"bucket,omitempty"`
	Region   string `json:"region,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	UseSSL   bool   `json:"use_ssl,omitempty"`
}

// ClaimResult is returned to a device that successfully redeems a ticket.
type ClaimResult struct {
	Status         string          `json:"status"`           // always "enrolled"
	DeviceID       string          `json:"device_id"`        // sha256 fingerprint of the submitted public key
	IssuerDeviceID string          `json:"issuer_device_id"` // who approved the pairing (audit)
	ApprovalSig    string          `json:"approval_sig"`     // box attestation over the enrolment
	Hints          ConnectionHints `json:"hints"`
	// RequiresPassphrase is always true: pairing never transmits the
	// data-encryption passphrase; the user enters it on the new device.
	RequiresPassphrase bool `json:"requires_passphrase"`
}

// PairedDevice is the audit record of one enrolled device.
type PairedDevice struct {
	DeviceID       string     `json:"device_id"` // sha256 fingerprint of PublicKey
	Name           string     `json:"name,omitempty"`
	PublicKey      string     `json:"public_key"` // base64, exactly as submitted (PKIX DER)
	IssuerDeviceID string     `json:"issuer_device_id,omitempty"`
	ApprovalSig    string     `json:"approval_sig,omitempty"` // box attestation, verifiable via AuthorityPublicKey
	PairedAt       time.Time  `json:"paired_at"`
	Revoked        bool       `json:"revoked,omitempty"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
}

// ─── canonical signing payloads (string times → deterministic bytes) ─────────

type ticketSigPayload struct {
	Token          string `json:"token"`
	ShortCode      string `json:"short_code"`
	IssuerDeviceID string `json:"issuer_device_id"`
	DeviceName     string `json:"device_name"`
	CreatedAt      string `json:"created_at"`
	ExpiresAt      string `json:"expires_at"`
}

func (t *Ticket) sigPayload() ticketSigPayload {
	return ticketSigPayload{
		Token:          t.Token,
		ShortCode:      t.ShortCode,
		IssuerDeviceID: t.IssuerDeviceID,
		DeviceName:     t.DeviceName,
		CreatedAt:      t.CreatedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt:      t.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
}

type attestationPayload struct {
	Kind           string `json:"kind"`
	DeviceID       string `json:"device_id"`
	PublicKey      string `json:"public_key"`
	IssuerDeviceID string `json:"issuer_device_id"`
	PairedAt       string `json:"paired_at"`
}

// ─── paths ──────────────────────────────────────────────────────────────────

func ticketsPath(dbDir string) string { return filepath.Join(dbDir, ticketsFileName) }
func devicesPath(dbDir string) string { return filepath.Join(dbDir, devicesFileName) }
func storagePath(dbDir string) string { return filepath.Join(dbDir, storageFileName) }
func authKeyPath(dbDir string) string { return filepath.Join(dbDir, authorityKeyFile) }

// ─── pairing-authority key (per-box Ed25519, persisted) ───────────────────────

// authorityPriv loads (or, on first use, generates + persists) the box's
// pairing-authority Ed25519 private key.
func authorityPriv(dbDir string) (ed25519.PrivateKey, error) {
	authMu.Lock()
	defer authMu.Unlock()

	path := authKeyPath(dbDir)
	if data, err := os.ReadFile(path); err == nil {
		if len(data) == ed25519.SeedSize {
			return ed25519.NewKeyFromSeed(data), nil
		}
		if len(data) == ed25519.PrivateKeySize {
			return ed25519.PrivateKey(data), nil
		}
		return nil, fmt.Errorf("pairing: authority key at %s has unexpected size %d", path, len(data))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("pairing: read authority key: %w", err)
	}

	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("pairing: authority key rand: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("pairing: authority key dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, seed, 0600); err != nil {
		return nil, fmt.Errorf("pairing: write authority key: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, fmt.Errorf("pairing: install authority key: %w", err)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// AuthorityPublicKey returns the base64 (std) Ed25519 public key that verifies
// tickets and enrolment attestations issued by this box.
func AuthorityPublicKey(dbDir string) (string, error) {
	priv, err := authorityPriv(dbDir)
	if err != nil {
		return "", err
	}
	pub := priv.Public().(ed25519.PublicKey)
	return base64.StdEncoding.EncodeToString(pub), nil
}

func signCanonical(priv ed25519.PrivateKey, v any) (string, error) {
	payload, err := signing.Canonical(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, payload)), nil
}

func verifyCanonical(pub ed25519.PublicKey, v any, sigB64 string) error {
	sig, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrTampered
	}
	payload, err := signing.Canonical(v)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, payload, sig) {
		return ErrTampered
	}
	return nil
}

// ─── ticket persistence ─────────────────────────────────────────────────────

type ticketDB map[string]Ticket // keyed by Token

func loadTickets(path string) (ticketDB, error) {
	db := make(ticketDB)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return db, nil
		}
		return nil, fmt.Errorf("pairing: read tickets: %w", err)
	}
	if err := json.Unmarshal(data, &db); err != nil {
		return nil, fmt.Errorf("pairing: parse tickets: %w", err)
	}
	if db == nil {
		db = make(ticketDB)
	}
	return db, nil
}

func saveJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("pairing: ensure dir: %w", err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("pairing: marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("pairing: write: %w", err)
	}
	return os.Rename(tmp, path)
}

func loadDevices(path string) ([]PairedDevice, error) {
	var out []PairedDevice
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("pairing: read devices: %w", err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("pairing: parse devices: %w", err)
	}
	return out, nil
}

// storageHints reads ONLY the non-secret connection fields of storage.json. It
// deliberately does not declare the access/secret key fields, so those secrets
// are never even unmarshalled into this process's memory during pairing. A
// missing storage.json is not an error — pairing establishes trust and does not
// require storage to already be provisioned.
func storageHints(dbDir string) (ConnectionHints, error) {
	var cfg struct {
		Bucket     string `json:"bucket"`
		BucketName string `json:"bucket_name"`
		Region     string `json:"region"`
		Endpoint   string `json:"endpoint"`
		UseSSL     bool   `json:"use_ssl"`
	}
	data, err := os.ReadFile(storagePath(dbDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ConnectionHints{}, nil
		}
		return ConnectionHints{}, fmt.Errorf("pairing: read storage.json: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ConnectionHints{}, fmt.Errorf("pairing: parse storage.json: %w", err)
	}
	bucket := cfg.Bucket
	if bucket == "" {
		bucket = cfg.BucketName
	}
	return ConnectionHints{Bucket: bucket, Region: cfg.Region, Endpoint: cfg.Endpoint, UseSSL: cfg.UseSSL}, nil
}

// ─── token / short-code generation ──────────────────────────────────────────

func newToken() (token, shortCode string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("pairing: rand: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)

	alphabetSize := big.NewInt(int64(len(crockfordAlphabet)))
	seg := func() (string, error) {
		buf := make([]byte, 4)
		for i := range buf {
			n, err := rand.Int(rand.Reader, alphabetSize)
			if err != nil {
				return "", err
			}
			buf[i] = crockfordAlphabet[n.Int64()]
		}
		return string(buf), nil
	}
	a, err := seg()
	if err != nil {
		return "", "", fmt.Errorf("pairing: short-code: %w", err)
	}
	b, err := seg()
	if err != nil {
		return "", "", fmt.Errorf("pairing: short-code: %w", err)
	}
	shortCode = "VULOS-" + a + "-" + b
	return token, shortCode, nil
}

// ─── Issue ──────────────────────────────────────────────────────────────────

// Issue mints a new single-use, signed pairing ticket and persists it. It reads
// NO storage secret. ttl is clamped to (0, MaxTTL]; a non-positive ttl uses
// DefaultTTL.
//
// issuerDeviceID is the fingerprint of the already-trusted device authorising
// the pairing (resolved by the HTTP layer from this box's devicekey identity).
// It is recorded for audit and used to refuse a self-pair at Claim time.
func Issue(dbDir, issuerDeviceID, deviceName string, ttl time.Duration) (*Ticket, error) {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl > MaxTTL {
		ttl = MaxTTL
	}
	priv, err := authorityPriv(dbDir)
	if err != nil {
		return nil, err
	}
	token, shortCode, err := newToken()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	t := Ticket{
		Token:          token,
		ShortCode:      shortCode,
		IssuerDeviceID: strings.TrimSpace(issuerDeviceID),
		DeviceName:     strings.TrimSpace(deviceName),
		CreatedAt:      now,
		ExpiresAt:      now.Add(ttl),
	}
	sig, err := signCanonical(priv, t.sigPayload())
	if err != nil {
		return nil, fmt.Errorf("pairing: sign ticket: %w", err)
	}
	t.Sig = sig

	mu.Lock()
	defer mu.Unlock()

	path := ticketsPath(dbDir)
	db, err := loadTickets(path)
	if err != nil {
		return nil, err
	}
	pruneExpired(db)
	db[token] = t
	if err := saveJSON(path, db); err != nil {
		return nil, err
	}
	return &t, nil
}

// VerifyTicket checks a ticket's signature against this box's pairing-authority
// public key. A ticket parsed from a QR (see ParseQR) can be verified offline
// this way before the device ever contacts the box. Fails closed (ErrTampered).
func VerifyTicket(dbDir string, t *Ticket) error {
	if t == nil {
		return ErrTampered
	}
	priv, err := authorityPriv(dbDir)
	if err != nil {
		return err
	}
	pub := priv.Public().(ed25519.PublicKey)
	return verifyCanonical(pub, t.sigPayload(), t.Sig)
}

// ─── Claim ──────────────────────────────────────────────────────────────────

// Claim validates and consumes a ticket (single-use), records the claiming
// device's OWN public key as an enrolled PairedDevice with a box-signed
// attestation, and returns the NON-SECRET connection hints the new device
// needs. It never returns storage credentials or a passphrase
// (RequiresPassphrase is always true).
//
// Order of checks is deliberate and fail-closed:
//  1. token/pubkey well-formedness (no store mutation on malformed input);
//  2. ticket exists (else ErrInvalid) and is unexpired (else ErrExpired,
//     dropping the dead ticket);
//  3. the STORED ticket's signature verifies (else ErrTampered — a hand-edited
//     tickets file cannot forge a ticket);
//  4. the claimant is not the issuer (else ErrSelfPair — checked BEFORE the
//     token is consumed, so a self-pair attempt does not burn a legitimate
//     device's one-time ticket);
//  5. only then is the token consumed and the enrolment recorded.
func Claim(dbDir, token, deviceName, devicePubKeyB64 string) (*ClaimResult, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrInvalid
	}
	fingerprint, canonicalPubB64, err := fingerprintPubKey(devicePubKeyB64)
	if err != nil {
		return nil, err
	}

	priv, err := authorityPriv(dbDir)
	if err != nil {
		return nil, err
	}
	pub := priv.Public().(ed25519.PublicKey)

	hints, err := storageHints(dbDir)
	if err != nil {
		return nil, err
	}

	mu.Lock()
	defer mu.Unlock()

	path := ticketsPath(dbDir)
	db, err := loadTickets(path)
	if err != nil {
		return nil, err
	}
	t, ok := db[token]
	if !ok {
		return nil, ErrInvalid
	}
	if time.Now().After(t.ExpiresAt) {
		delete(db, token)
		_ = saveJSON(path, db) // best-effort cleanup
		return nil, ErrExpired
	}
	// On-disk integrity: a valid ticket must carry a valid authority signature.
	if err := verifyCanonical(pub, t.sigPayload(), t.Sig); err != nil {
		delete(db, token) // drop the corrupt/forged entry
		_ = saveJSON(path, db)
		return nil, ErrTampered
	}
	// Self-pair refusal BEFORE consuming (a self-pair must not burn a real
	// device's ticket).
	if t.IssuerDeviceID != "" && fingerprint == t.IssuerDeviceID {
		return nil, ErrSelfPair
	}

	// Consume (single-use) BEFORE recording, so a concurrent/replayed claim of
	// the same token gets ErrInvalid.
	delete(db, token)
	if err := saveJSON(path, db); err != nil {
		return nil, fmt.Errorf("pairing: consume ticket: %w", err)
	}

	name := strings.TrimSpace(deviceName)
	if name == "" {
		name = t.DeviceName
	}
	pairedAt := time.Now().UTC()
	att := attestationPayload{
		Kind:           "pairing-enrolment-v1",
		DeviceID:       fingerprint,
		PublicKey:      canonicalPubB64,
		IssuerDeviceID: t.IssuerDeviceID,
		PairedAt:       pairedAt.Format(time.RFC3339Nano),
	}
	approvalSig, err := signCanonical(priv, att)
	if err != nil {
		return nil, fmt.Errorf("pairing: sign enrolment attestation: %w", err)
	}

	// Record the enrolment (best-effort audit; a persistence failure must not
	// deny an otherwise-valid pairing — the token is already consumed).
	_ = recordDevice(dbDir, PairedDevice{
		DeviceID:       fingerprint,
		Name:           name,
		PublicKey:      canonicalPubB64,
		IssuerDeviceID: t.IssuerDeviceID,
		ApprovalSig:    approvalSig,
		PairedAt:       pairedAt,
	})

	return &ClaimResult{
		Status:             "enrolled",
		DeviceID:           fingerprint,
		IssuerDeviceID:     t.IssuerDeviceID,
		ApprovalSig:        approvalSig,
		Hints:              hints,
		RequiresPassphrase: true,
	}, nil
}

// recordDevice appends (or replaces by DeviceID) a PairedDevice. Caller holds mu.
func recordDevice(dbDir string, d PairedDevice) error {
	path := devicesPath(dbDir)
	devices, err := loadDevices(path)
	if err != nil {
		return err
	}
	for i := range devices {
		if devices[i].DeviceID == d.DeviceID {
			devices[i] = d // idempotent re-pair updates the record
			return saveJSON(path, devices)
		}
	}
	if len(devices) >= maxPairedDevices {
		return fmt.Errorf("pairing: paired-device store at capacity (%d)", maxPairedDevices)
	}
	devices = append(devices, d)
	return saveJSON(path, devices)
}

// ─── device audit / management surface ────────────────────────────────────────

// ListPairedDevices returns the audit records of every device paired via this
// package (insertion order).
func ListPairedDevices(dbDir string) ([]PairedDevice, error) {
	mu.Lock()
	defer mu.Unlock()
	return loadDevices(devicesPath(dbDir))
}

// GetPairedDevice returns the record for deviceID (and whether it exists). The
// PublicKey it carries is the PKIX DER a compromise-removal revokes.
func GetPairedDevice(dbDir, deviceID string) (*PairedDevice, bool, error) {
	mu.Lock()
	defer mu.Unlock()
	devices, err := loadDevices(devicesPath(dbDir))
	if err != nil {
		return nil, false, err
	}
	for i := range devices {
		if devices[i].DeviceID == deviceID {
			cp := devices[i]
			return &cp, true, nil
		}
	}
	return nil, false, nil
}

// MarkRevoked flags a paired device as revoked in the LOCAL audit store. This is
// bookkeeping that mirrors the authoritative devicekey revocation (which is what
// actually locks the device out fleet-wide); callers invoke it AFTER a
// devicekey revocation succeeds. Returns ErrUnknownDevice if there is no such
// record. Idempotent.
func MarkRevoked(dbDir, deviceID string) error {
	mu.Lock()
	defer mu.Unlock()
	path := devicesPath(dbDir)
	devices, err := loadDevices(path)
	if err != nil {
		return err
	}
	found := false
	now := time.Now().UTC()
	for i := range devices {
		if devices[i].DeviceID == deviceID {
			found = true
			if !devices[i].Revoked {
				devices[i].Revoked = true
				devices[i].RevokedAt = &now
			}
		}
	}
	if !found {
		return ErrUnknownDevice
	}
	return saveJSON(path, devices)
}

// ForgetPairedDevice removes the audit record for deviceID. Returns true if a
// record was removed. NOTE: this only clears the LOCAL audit record; it does NOT
// revoke fleet membership — that is the devicekey revocation path.
func ForgetPairedDevice(dbDir, deviceID string) (bool, error) {
	mu.Lock()
	defer mu.Unlock()
	path := devicesPath(dbDir)
	devices, err := loadDevices(path)
	if err != nil {
		return false, err
	}
	out := devices[:0]
	removed := false
	for _, d := range devices {
		if d.DeviceID == deviceID {
			removed = true
			continue
		}
		out = append(out, d)
	}
	if !removed {
		return false, nil
	}
	return true, saveJSON(path, out)
}

// ─── QR helpers ───────────────────────────────────────────────────────────────

// QRPayload encodes a ticket as a vulos://pair/v1 URL for a QR code. It carries
// the token, the box signature, the expiry, and non-secret hints — NEVER storage
// credentials.
func QRPayload(t *Ticket, endpoint, boxID string) string {
	q := url.Values{}
	q.Set("token", t.Token)
	q.Set("sig", t.Sig)
	q.Set("iss", t.IssuerDeviceID)
	q.Set("exp", t.ExpiresAt.UTC().Format(time.RFC3339Nano))
	q.Set("cre", t.CreatedAt.UTC().Format(time.RFC3339Nano))
	if t.ShortCode != "" {
		q.Set("code", t.ShortCode)
	}
	if t.DeviceName != "" {
		q.Set("name", t.DeviceName)
	}
	if endpoint != "" {
		q.Set("endpoint", endpoint)
	}
	if boxID != "" {
		q.Set("box", boxID)
	}
	return "vulos://pair/v1?" + q.Encode()
}

// ParseQR parses a vulos://pair/v1 payload back into a Ticket (signature NOT yet
// checked — call VerifyTicket). Fails closed on a malformed URL or wrong scheme.
func ParseQR(payload string) (*Ticket, error) {
	u, err := url.Parse(strings.TrimSpace(payload))
	if err != nil {
		return nil, fmt.Errorf("pairing: parse QR: %w", err)
	}
	if u.Scheme != "vulos" || u.Host != "pair" || u.Path != "/v1" {
		return nil, errors.New("pairing: not a vulos://pair/v1 payload")
	}
	q := u.Query()
	created, err := time.Parse(time.RFC3339Nano, q.Get("cre"))
	if err != nil {
		return nil, fmt.Errorf("pairing: QR created-at: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, q.Get("exp"))
	if err != nil {
		return nil, fmt.Errorf("pairing: QR expiry: %w", err)
	}
	return &Ticket{
		Token:          q.Get("token"),
		ShortCode:      q.Get("code"),
		IssuerDeviceID: q.Get("iss"),
		DeviceName:     q.Get("name"),
		CreatedAt:      created.UTC(),
		ExpiresAt:      expires.UTC(),
		Sig:            q.Get("sig"),
	}, nil
}

// ─── helpers ────────────────────────────────────────────────────────────────

// fingerprintPubKey validates a base64 device public key and returns its
// SHA-256 hex fingerprint (the SAME scheme services/devicekey.Fingerprint uses,
// so pairing's DeviceID equals the devicekey fingerprint a revocation targets)
// plus the canonical standard-base64 re-encoding of the key. Accepts standard or
// raw-url base64. Fails closed on anything malformed or an implausible length
// (32B Ed25519 .. 512B DER).
func fingerprintPubKey(b64 string) (fingerprint, canonicalB64 string, err error) {
	b64 = strings.TrimSpace(b64)
	if b64 == "" {
		return "", "", ErrBadDeviceKey
	}
	raw, derr := base64.StdEncoding.DecodeString(b64)
	if derr != nil {
		raw, derr = base64.RawURLEncoding.DecodeString(b64)
		if derr != nil {
			return "", "", ErrBadDeviceKey
		}
	}
	if len(raw) < 32 || len(raw) > 512 {
		return "", "", ErrBadDeviceKey
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:]), base64.StdEncoding.EncodeToString(raw), nil
}

// pruneExpired drops expired tickets in-place.
func pruneExpired(db ticketDB) {
	now := time.Now()
	for tok, t := range db {
		if now.After(t.ExpiresAt) {
			delete(db, tok)
		}
	}
}
