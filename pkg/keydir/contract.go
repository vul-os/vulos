// Package keydir implements MAIL-07: the vulos.org public key directory.
//
// Cloud-side responsibilities (this package):
//   - Map each verified Vulos address (e.g. alice@vulos.org) to the instance's
//     Ed25519 public key published by vulos-relay.
//   - Enforce the vulos.org-only identity for Free-tier accounts (no BYO domain).
//   - Expose a public discovery endpoint queried by the OSS vulos-relay peering
//     transport so peers can locate keys without touching external DNS.
//   - Allow the key-owner to rotate their key and to toggle discoverability.
//
// What this package does NOT do:
//   - Run any SMTP or mail transport (that is vulos-mail OSS).
//   - Perform the Ed25519 X25519 peering handshake (that is vulos-relay OSS).
//   - Verify domain ownership for BYO domains (separate DNS-verify flow).
package keydir

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

var (
	// ErrNotFound is returned when no entry exists for the given address or account.
	ErrNotFound = errors.New("keydir: entry not found")

	// ErrDuplicate is returned when a Vulos account address is already registered by
	// another account.
	ErrDuplicate = errors.New("keydir: Vulos address already registered")

	// ErrInvalidInput is returned when a required field is missing or malformed.
	ErrInvalidInput = errors.New("keydir: invalid input")

	// ErrNotVulosOrg is returned when the address is not an @vulos.org address.
	// ErrNotVumail is kept as an alias for backwards compat.
	ErrNotVumail = errors.New("keydir: address must be @vulos.org")
)

// VumailDomain (VulosDomain) is the only domain accepted for Free-tier identity.
const VumailDomain = "@vulos.org"

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// Entry is a key-directory record mapping a Vulos account address to a public key.
type Entry struct {
	ID            int64     `json:"id"`
	AccountID     string    `json:"account_id"`
	VumailAddress string    `json:"vumail_address"`
	PublicKeyB64  string    `json:"public_key_b64"` // base64url Ed25519 public key
	PeerEndpoint  string    `json:"peer_endpoint"`  // optional relay-reachable hint
	Discoverable  bool      `json:"discoverable"`   // listed in directory lookup
	State         string    `json:"state"`          // "active" | "revoked"
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// Store is the keydir persistence contract.
// MemStore is the behavioural reference; SQLStore must match it exactly.
type Store interface {
	// Upsert creates or updates the key-directory entry for an account.
	// Returns ErrNotVumail (ErrNotVulosOrg) if the address is not @vulos.org.
	// Returns ErrDuplicate if the address is already claimed by another account.
	// Returns ErrInvalidInput when account_id, VumailAddress (Vulos address), or public_key_b64 are empty.
	Upsert(ctx context.Context, e Entry) (Entry, error)

	// GetByAddress returns the active entry for the given Vulos account address.
	// Returns ErrNotFound when no active entry exists.
	GetByAddress(ctx context.Context, address string) (Entry, error)

	// GetByAccount returns the entry for the given account_id.
	// Returns ErrNotFound when no entry exists.
	GetByAccount(ctx context.Context, accountID string) (Entry, error)

	// Revoke marks the entry for accountID as revoked.
	// No-op (nil error) if the account has no entry.
	Revoke(ctx context.Context, accountID string) error

	// SetDiscoverable toggles the discoverable flag for an account's entry.
	// Returns ErrNotFound when the account has no entry.
	SetDiscoverable(ctx context.Context, accountID string, discoverable bool) error

	// Close releases the underlying database connection.
	Close() error
}

// ---------------------------------------------------------------------------
// Validation helper
// ---------------------------------------------------------------------------

// ValidateVumailAddress checks that addr is a non-empty @vulos.org address (accepts either VumailAddress or VulosAddress field).
func ValidateVumailAddress(addr string) error {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return ErrInvalidInput
	}
	if !strings.HasSuffix(addr, VumailDomain) {
		return ErrNotVumail
	}
	local := strings.TrimSuffix(addr, VumailDomain)
	if local == "" || strings.ContainsAny(local, " \t\r\n") {
		return ErrInvalidInput
	}
	return nil
}

// ---------------------------------------------------------------------------
// MemStore — in-memory reference Store (test double + dev fallback)
// ---------------------------------------------------------------------------

// MemStore is the concurrency-safe, in-memory reference Store.
// It defines the semantics every other implementation must match.
type MemStore struct {
	mu        sync.Mutex
	byAccount map[string]*Entry // accountID → entry
	byAddress map[string]string // address → accountID (for uniqueness check)
	nextID    int64
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{
		byAccount: map[string]*Entry{},
		byAddress: map[string]string{},
	}
}

func (m *MemStore) Upsert(_ context.Context, e Entry) (Entry, error) {
	if e.AccountID == "" || e.VumailAddress == "" || e.PublicKeyB64 == "" {
		return Entry{}, ErrInvalidInput
	}
	e.VumailAddress = strings.ToLower(strings.TrimSpace(e.VumailAddress))
	if err := ValidateVumailAddress(e.VumailAddress); err != nil {
		return Entry{}, err
	}
	if e.State == "" {
		e.State = "active"
	}
	now := time.Now().UTC()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check address uniqueness across other accounts.
	if owner, exists := m.byAddress[e.VumailAddress]; exists && owner != e.AccountID {
		return Entry{}, ErrDuplicate
	}

	if existing, ok := m.byAccount[e.AccountID]; ok {
		// Update — if address is changing, clean up old mapping.
		if existing.VumailAddress != e.VumailAddress {
			delete(m.byAddress, existing.VumailAddress)
		}
		e.ID = existing.ID
		e.CreatedAt = existing.CreatedAt
		e.UpdatedAt = now
	} else {
		m.nextID++
		e.ID = m.nextID
		e.CreatedAt = now
		e.UpdatedAt = now
	}

	cp := e
	m.byAccount[e.AccountID] = &cp
	m.byAddress[e.VumailAddress] = e.AccountID
	return cp, nil
}

func (m *MemStore) GetByAddress(_ context.Context, address string) (Entry, error) {
	address = strings.ToLower(strings.TrimSpace(address))
	m.mu.Lock()
	defer m.mu.Unlock()
	accountID, ok := m.byAddress[address]
	if !ok {
		return Entry{}, ErrNotFound
	}
	e := m.byAccount[accountID]
	if e == nil || e.State != "active" {
		return Entry{}, ErrNotFound
	}
	cp := *e
	return cp, nil
}

func (m *MemStore) GetByAccount(_ context.Context, accountID string) (Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.byAccount[accountID]
	if !ok {
		return Entry{}, ErrNotFound
	}
	cp := *e
	return cp, nil
}

func (m *MemStore) Revoke(_ context.Context, accountID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.byAccount[accountID]; ok {
		e.State = "revoked"
		e.UpdatedAt = time.Now().UTC()
	}
	return nil
}

func (m *MemStore) SetDiscoverable(_ context.Context, accountID string, discoverable bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.byAccount[accountID]
	if !ok {
		return ErrNotFound
	}
	e.Discoverable = discoverable
	e.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *MemStore) Close() error { return nil }

// compile-time assertion: MemStore satisfies Store.
var _ Store = (*MemStore)(nil)
