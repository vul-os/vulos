// Package sshrec is the vulos.cloud SSH-recovery CA broker.
//
// It mints short-lived signed SSH certificates (TTL ≤ 300 s, single principal
// "recovery", command/source-restricted) on behalf of owners who have
// explicitly armed the recovery window. Co-approval scaffolding is wired for
// Team/Enterprise; single-owner personal path uses required_approvals=0.
//
// THIS FILE IS THE FROZEN CONTRACT. Do not edit it in feature branches —
// every sshrec agent builds against these types, this Store interface, the
// reference MemStore (which DEFINES Store semantics), and the KeyProvider
// interface. SQL implementation must match MemStore behaviourally; handlers
// depend ONLY on these interfaces.
package sshrec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// -----------------------------------------------------------------------------
// Sentinel errors — handlers map these to HTTP status codes.
// -----------------------------------------------------------------------------

var (
	ErrNotArmed         = errors.New("sshrec: not armed")
	ErrInvalidPubKey    = errors.New("sshrec: invalid SSH public key")
	ErrNotOwner         = errors.New("sshrec: account not ULID owner")
	ErrApprovalsMissing = errors.New("sshrec: insufficient approvals")
	ErrUnknownRequest   = errors.New("sshrec: unknown request")
	ErrRevoked          = errors.New("sshrec: cert revoked")
	ErrNotFound         = errors.New("sshrec: not found")
	// ErrSelfApproval is returned when the requester tries to approve their own
	// request (dual-control requires a distinct approver).
	ErrSelfApproval = errors.New("sshrec: requester cannot approve own request")
	// ErrAlreadyApproved is returned when the same account approves a request more
	// than once (defeats quorum-by-approving-twice).
	ErrAlreadyApproved = errors.New("sshrec: already approved by this account")
)

// -----------------------------------------------------------------------------
// Domain types
// -----------------------------------------------------------------------------

// Approval records a single co-approval vote on a request.
type Approval struct {
	By string    `json:"by"`
	At time.Time `json:"at"`
}

// ArmRequest is the input to Store.Arm.
type ArmRequest struct {
	ULID      string
	AccountID string
	Reason    string
	Window    time.Duration
}

// Request represents a pending or settled recovery-cert request.
type Request struct {
	ID                string
	ULID              string
	AccountID         string
	PublicKeySSH      string
	State             string // 'pending'|'approved'|'rejected'|'minted'|'expired'
	Approvals         []Approval
	RequiredApprovals int
	SourceIP          string
	CreatedAt         time.Time
	ExpiresAt         time.Time
}

// Cert represents a minted SSH certificate record.
type Cert struct {
	KeyID          string
	RequestID      string
	ULID           string
	Serial         uint64
	NotBefore      time.Time
	NotAfter       time.Time
	Principal      string
	SourceRestrict string
	CertBlob       []byte // raw SSH certificate bytes (wire-encoded)
	Revoked        bool
	RevokedAt      *time.Time
	RevokedReason  string
}

// AuditEvent is a single immutable audit entry.
type AuditEvent struct {
	ID        int64
	At        time.Time
	ULID      string
	AccountID string
	Action    string // 'arm'|'request'|'approve'|'reject'|'mint'|'connect'|'expire'|'revoke'
	KeyID     string
	Detail    map[string]any
}

// CertReq is the input to KeyProvider.SignCert.
type CertReq struct {
	UserPubKey  ssh.PublicKey
	KeyID       string
	Principal   string
	ValidAfter  time.Time
	ValidBefore time.Time
	SourceCIDRs []string // e.g. ["10.0.0.1/32"]
	Command     string   // forced command, e.g. "vulosrec"
}

// -----------------------------------------------------------------------------
// Store interface
// -----------------------------------------------------------------------------

// Store is the sshrec persistence contract. SQLStore and MemStore both satisfy
// it; handlers depend ONLY on this interface. MemStore is the behavioural
// source of truth.
type Store interface {
	// Arm sets (or refreshes) the arming window for ulid. Replaces any
	// existing arming record.
	Arm(ctx context.Context, req ArmRequest) error
	// GetArming returns the armed-until time for ulid.
	// ok is false if ulid is not armed or the window has expired.
	GetArming(ctx context.Context, ulid string) (armedUntil time.Time, ok bool)

	// InsertRequest stores a new recovery request. ID must be unique.
	InsertRequest(ctx context.Context, r Request) error
	// GetRequest returns the request by ID. Returns ErrUnknownRequest if absent.
	GetRequest(ctx context.Context, id string) (Request, error)
	// Approve appends an approval from byAccount to the request and returns
	// the updated request. Returns ErrUnknownRequest if absent.
	Approve(ctx context.Context, id, byAccount string) (Request, error)
	// Reject sets state=rejected and returns the updated request.
	Reject(ctx context.Context, id, byAccount string) (Request, error)

	// InsertCert stores a minted cert. KeyID must be unique.
	InsertCert(ctx context.Context, c Cert) error
	// GetCert returns the cert record by key_id. Returns ErrNotFound if absent.
	GetCert(ctx context.Context, keyID string) (Cert, error)
	// RevokeCert marks a cert revoked with the given reason.
	// Returns ErrNotFound if keyID is unknown.
	RevokeCert(ctx context.Context, keyID, reason string) error
	// ListRevoked returns the key IDs of all revoked certs (for KRL building).
	ListRevoked(ctx context.Context) ([]string, error)

	// ListPendingByULIDs returns all requests in state 'pending' whose ULID is
	// in the provided set. Used by the co-approval pending-list endpoint.
	// An empty ulids slice returns an empty slice (not all pending requests).
	ListPendingByULIDs(ctx context.Context, ulids []string) ([]Request, error)

	// Audit appends an immutable audit event.
	Audit(ctx context.Context, e AuditEvent) error
	// ListAudit returns the most recent audit events for ulid, up to limit,
	// in chronological order (oldest first).
	ListAudit(ctx context.Context, ulid string, limit int) ([]AuditEvent, error)

	Close() error
}

// -----------------------------------------------------------------------------
// KeyProvider interface
// -----------------------------------------------------------------------------

// KeyProvider abstracts SSH CA key custody so file-backed, env-backed, and
// HSM-backed implementations are swappable without changing handlers.
type KeyProvider interface {
	// SignerName returns a human-readable identifier for logging.
	SignerName() string
	// CAPublicSSH returns the CA's public key in authorized_keys-format.
	CAPublicSSH() []byte
	// SignCert mints a new SSH user certificate per req.
	// The returned bytes are the raw OpenSSH certificate wire encoding.
	SignCert(req CertReq) ([]byte, error)
}

// -----------------------------------------------------------------------------
// MemKeyProvider — deterministic test key (do NOT use in production)
// -----------------------------------------------------------------------------

// MemKeyProvider is a deterministic in-memory KeyProvider for tests.
// It generates a fresh Ed25519 CA key on construction.
type MemKeyProvider struct {
	signer    ssh.Signer
	pubKeySSH []byte
}

// NewMemKeyProvider generates a fresh Ed25519 CA key pair in memory.
func NewMemKeyProvider() *MemKeyProvider {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic("sshrec: MemKeyProvider key gen: " + err.Error())
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		panic("sshrec: MemKeyProvider signer: " + err.Error())
	}
	pub := ssh.MarshalAuthorizedKey(s.PublicKey())
	return &MemKeyProvider{signer: s, pubKeySSH: pub}
}

func (m *MemKeyProvider) SignerName() string  { return "mem" }
func (m *MemKeyProvider) CAPublicSSH() []byte { return m.pubKeySSH }

func (m *MemKeyProvider) SignCert(req CertReq) ([]byte, error) {
	return signCert(m.signer, req)
}

// -----------------------------------------------------------------------------
// FileKeyProvider — file-backed, generates key on first boot (R-E requirement)
// -----------------------------------------------------------------------------

// FileKeyProvider loads the CA private key from path. If the file does not
// exist, it generates a fresh Ed25519 key, writes it (mode 0o600), and uses
// it. Production deployments should pre-place the key or use EnvKeyProvider.
type FileKeyProvider struct {
	path      string
	signer    ssh.Signer
	pubKeySSH []byte
}

// NewFileKeyProvider creates a FileKeyProvider, generating the key if absent.
// The directory must already exist; the key file is created with mode 0o600.
func NewFileKeyProvider(path string) (*FileKeyProvider, error) {
	var privBytes []byte

	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Generate fresh Ed25519 key.
		_, priv, genErr := ed25519.GenerateKey(rand.Reader)
		if genErr != nil {
			return nil, genErr
		}
		privBytes = priv.Seed() // 32-byte seed is the canonical store form

		if mkErr := os.MkdirAll(filepath.Dir(path), 0o700); mkErr != nil {
			return nil, mkErr
		}
		// Write seed; mode 0o600, no symlink follow (O_WRONLY|O_CREATE|O_EXCL)
		f, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr != nil {
			return nil, createErr
		}
		enc := base64.StdEncoding.EncodeToString(privBytes)
		if _, wErr := f.WriteString(enc); wErr != nil {
			f.Close()
			return nil, wErr
		}
		if cErr := f.Close(); cErr != nil {
			return nil, cErr
		}
	} else if err != nil {
		return nil, err
	} else {
		// Read existing key.
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, readErr
		}
		seed, decErr := base64.StdEncoding.DecodeString(string(raw))
		if decErr != nil {
			return nil, decErr
		}
		privBytes = seed
	}

	priv := ed25519.NewKeyFromSeed(privBytes)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	pub := ssh.MarshalAuthorizedKey(s.PublicKey())
	return &FileKeyProvider{path: path, signer: s, pubKeySSH: pub}, nil
}

func (f *FileKeyProvider) SignerName() string  { return "file:" + f.path }
func (f *FileKeyProvider) CAPublicSSH() []byte { return f.pubKeySSH }
func (f *FileKeyProvider) SignCert(req CertReq) ([]byte, error) {
	return signCert(f.signer, req)
}

// -----------------------------------------------------------------------------
// EnvKeyProvider — SSHREC_CA_KEY_BASE64-backed
// -----------------------------------------------------------------------------

// EnvKeyProvider reads the CA private key from SSHREC_CA_KEY_BASE64 (base64
// standard encoding of the 32-byte Ed25519 seed). Returns an error if the
// env var is absent or malformed.
type EnvKeyProvider struct {
	signer    ssh.Signer
	pubKeySSH []byte
}

// NewEnvKeyProvider constructs an EnvKeyProvider from SSHREC_CA_KEY_BASE64.
func NewEnvKeyProvider() (*EnvKeyProvider, error) {
	enc := os.Getenv("SSHREC_CA_KEY_BASE64")
	if enc == "" {
		return nil, errors.New("sshrec: SSHREC_CA_KEY_BASE64 not set")
	}
	seed, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, errors.New("sshrec: SSHREC_CA_KEY_BASE64 base64 decode: " + err.Error())
	}
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("sshrec: SSHREC_CA_KEY_BASE64 must be 32 bytes")
	}
	priv := ed25519.NewKeyFromSeed(seed)
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	pub := ssh.MarshalAuthorizedKey(s.PublicKey())
	return &EnvKeyProvider{signer: s, pubKeySSH: pub}, nil
}

func (e *EnvKeyProvider) SignerName() string  { return "env" }
func (e *EnvKeyProvider) CAPublicSSH() []byte { return e.pubKeySSH }
func (e *EnvKeyProvider) SignCert(req CertReq) ([]byte, error) {
	return signCert(e.signer, req)
}

// -----------------------------------------------------------------------------
// signCert — shared SSH certificate construction used by all KeyProviders
// -----------------------------------------------------------------------------

// signCert builds and signs an SSH user certificate per the sshrec contract:
//   - type: ssh.UserCert
//   - single principal from req.Principal
//   - source-address restriction from req.SourceCIDRs (if non-empty)
//   - forced command from req.Command (if non-empty)
func signCert(ca ssh.Signer, req CertReq) ([]byte, error) {
	cert := &ssh.Certificate{
		CertType:        ssh.UserCert,
		Key:             req.UserPubKey,
		KeyId:           req.KeyID,
		ValidPrincipals: []string{req.Principal},
		ValidAfter:      uint64(req.ValidAfter.Unix()),
		ValidBefore:     uint64(req.ValidBefore.Unix()),
		Permissions: ssh.Permissions{
			CriticalOptions: map[string]string{},
			Extensions:      map[string]string{},
		},
	}

	if req.Command != "" {
		cert.Permissions.CriticalOptions["force-command"] = req.Command
	}
	if len(req.SourceCIDRs) > 0 {
		// "source-address" critical option: comma-separated CIDR list.
		cert.Permissions.CriticalOptions["source-address"] = joinCIDRs(req.SourceCIDRs)
	}

	if err := cert.SignCert(rand.Reader, ca); err != nil {
		return nil, err
	}
	return cert.Marshal(), nil
}

func joinCIDRs(cidrs []string) string {
	out := ""
	for i, c := range cidrs {
		if i > 0 {
			out += ","
		}
		out += c
	}
	return out
}

// SourceCIDR converts a raw IP string to a /32 or /128 CIDR for the cert.
// If ip is already a CIDR notation it is returned as-is (after basic parse).
func SourceCIDR(ip string) string {
	if ip == "" {
		return ""
	}
	if _, _, err := net.ParseCIDR(ip); err == nil {
		return ip
	}
	if p := net.ParseIP(ip); p != nil {
		if p.To4() != nil {
			return ip + "/32"
		}
		return ip + "/128"
	}
	return ""
}

// -----------------------------------------------------------------------------
// MemStore — reference in-memory Store implementation
// -----------------------------------------------------------------------------

// MemStore is the in-memory reference Store. Concurrency-safe. It DEFINES the
// semantics every other implementation must match.
type MemStore struct {
	mu       sync.Mutex
	arming   map[string]armingRecord // ulid -> record
	requests map[string]Request      // id -> request
	certs    map[string]Cert         // key_id -> cert
	audit    []AuditEvent
	serial   atomic.Uint64
}

type armingRecord struct {
	armedUntil time.Time
	armedBy    string
	reason     string
}

// NewMemStore returns an empty reference store.
func NewMemStore() *MemStore {
	return &MemStore{
		arming:   map[string]armingRecord{},
		requests: map[string]Request{},
		certs:    map[string]Cert{},
	}
}

// Arm sets or refreshes the arming window for the given ULID.
func (m *MemStore) Arm(_ context.Context, req ArmRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.arming[req.ULID] = armingRecord{
		armedUntil: time.Now().UTC().Add(req.Window),
		armedBy:    req.AccountID,
		reason:     req.Reason,
	}
	return nil
}

// GetArming returns the armed-until time and whether the ULID is currently armed.
func (m *MemStore) GetArming(_ context.Context, ulid string) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.arming[ulid]
	if !ok {
		return time.Time{}, false
	}
	if time.Now().UTC().After(rec.armedUntil) {
		return time.Time{}, false
	}
	return rec.armedUntil, true
}

// InsertRequest stores a new request.
func (m *MemStore) InsertRequest(_ context.Context, r Request) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.requests[r.ID]; exists {
		return errors.New("sshrec: duplicate request id")
	}
	if r.Approvals == nil {
		r.Approvals = []Approval{}
	}
	m.requests[r.ID] = r
	return nil
}

// GetRequest returns the request by ID.
func (m *MemStore) GetRequest(_ context.Context, id string) (Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.requests[id]
	if !ok {
		return Request{}, ErrUnknownRequest
	}
	return r, nil
}

// Approve appends an approval and returns the updated request.
func (m *MemStore) Approve(_ context.Context, id, byAccount string) (Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.requests[id]
	if !ok {
		return Request{}, ErrUnknownRequest
	}
	// Dual-control: the requester may not approve their own request.
	if byAccount == r.AccountID {
		return r, ErrSelfApproval
	}
	// Dedup: an account may approve a request at most once.
	for _, a := range r.Approvals {
		if a.By == byAccount {
			return r, ErrAlreadyApproved
		}
	}
	r.Approvals = append(r.Approvals, Approval{By: byAccount, At: time.Now().UTC()})
	if len(r.Approvals) >= r.RequiredApprovals && r.RequiredApprovals > 0 {
		r.State = "approved"
	}
	m.requests[id] = r
	return r, nil
}

// Reject sets state=rejected.
func (m *MemStore) Reject(_ context.Context, id, byAccount string) (Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.requests[id]
	if !ok {
		return Request{}, ErrUnknownRequest
	}
	_ = byAccount
	r.State = "rejected"
	m.requests[id] = r
	return r, nil
}

// InsertCert stores a minted cert.
func (m *MemStore) InsertCert(_ context.Context, c Cert) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.certs[c.KeyID]; exists {
		return errors.New("sshrec: duplicate key_id")
	}
	m.certs[c.KeyID] = c
	return nil
}

// GetCert returns the cert by key_id.
func (m *MemStore) GetCert(_ context.Context, keyID string) (Cert, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.certs[keyID]
	if !ok {
		return Cert{}, ErrNotFound
	}
	return c, nil
}

// RevokeCert marks a cert revoked.
func (m *MemStore) RevokeCert(_ context.Context, keyID, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.certs[keyID]
	if !ok {
		return ErrNotFound
	}
	c.Revoked = true
	now := time.Now().UTC()
	c.RevokedAt = &now
	c.RevokedReason = reason
	m.certs[keyID] = c
	return nil
}

// ListRevoked returns key IDs of all revoked certs, sorted.
func (m *MemStore) ListRevoked(_ context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for id, c := range m.certs {
		if c.Revoked {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ListPendingByULIDs returns all pending requests whose ULID is in ulids.
func (m *MemStore) ListPendingByULIDs(_ context.Context, ulids []string) ([]Request, error) {
	set := make(map[string]bool, len(ulids))
	for _, u := range ulids {
		set[u] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Request
	for _, r := range m.requests {
		if r.State == "pending" && set[r.ULID] {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// Audit appends an immutable audit event.
func (m *MemStore) Audit(_ context.Context, e AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.ID = int64(len(m.audit) + 1)
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	m.audit = append(m.audit, e)
	return nil
}

// ListAudit returns audit events for ulid, up to limit, oldest first.
func (m *MemStore) ListAudit(_ context.Context, ulid string, limit int) ([]AuditEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []AuditEvent
	for _, e := range m.audit {
		if e.ULID == ulid {
			out = append(out, e)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

// Close is a no-op for MemStore.
func (m *MemStore) Close() error { return nil }

// NextSerial returns the next monotonic serial number (for MemStore tests).
func (m *MemStore) NextSerial() uint64 {
	return m.serial.Add(1)
}

// compile-time assertions.
var _ Store = (*MemStore)(nil)
var _ KeyProvider = (*MemKeyProvider)(nil)
var _ KeyProvider = (*FileKeyProvider)(nil)
var _ KeyProvider = (*EnvKeyProvider)(nil)
