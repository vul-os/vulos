// contacts.go — contacts store: allow list, trust states, permissions, persistence.
//
// # Trust state machine
//
//	Unknown ──► Pending ──► Approved ──► Blocked
//	                │                      ▲
//	                └──────────────────────┘
//
// Allowed transitions:
//
//	pending  → approved  (Accept)
//	pending  → blocked   (Block)
//	approved → blocked   (Block)
//	blocked  → pending   (Unblock — resets to pending so user must re-approve)
//	approved → removed   (Remove)
//	pending  → removed   (Remove)
//	blocked  → removed   (Remove)
//
// # Permissions
//
// Each approved contact carries a granular permission set:
//
//	message — can send text messages
//	media   — can send image/video/file attachments
//	call    — can initiate voice calls
//	video   — can initiate video calls
//
// Default on approval: all four permissions granted.
//
// # Persistence
//
// The store writes to ~/.vulos/peering/contacts.json atomically (temp file +
// rename) after every mutation.  On load the JSON is validated and unknown
// fields are silently dropped so future schema additions are backwards-safe.
//
// Thread safety: all public methods acquire the store's mutex; callers may
// call them concurrently without additional locking.
//
// # Usage (library — no HTTP wiring in this file)
//
//	cs, err := peering.NewContactStore(home)   // home = os.UserHomeDir()
//	if err != nil { ... }
//
//	// Receive a contact request from another peer.
//	if err := cs.Add("vulos:ed25519:5Hb7...", "Bob", "bob.vulos.org:8080"); err != nil { ... }
//
//	// Accept.
//	if err := cs.Approve("vulos:ed25519:5Hb7...", peering.DefaultPerms()); err != nil { ... }
//
//	// Gate an inbound message.
//	if !cs.IsApproved("vulos:ed25519:5Hb7...") { return } // drop
//	if !cs.Can("vulos:ed25519:5Hb7...", peering.PermMessage) { return } // no message perm
package peering

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ─── Permission constants ─────────────────────────────────────────────────────

// Perm is a granular capability that an approved contact may be granted.
type Perm string

const (
	// PermMessage allows the contact to send text messages.
	PermMessage Perm = "message"

	// PermMedia allows the contact to send image/video/file attachments.
	PermMedia Perm = "media"

	// PermCall allows the contact to initiate voice calls.
	PermCall Perm = "call"

	// PermVideo allows the contact to initiate video calls.
	PermVideo Perm = "video"
)

// allPerms is the complete set of defined permissions.
var allPerms = []Perm{PermMessage, PermMedia, PermCall, PermVideo}

// DefaultPerms returns all four permissions, which is what most approvals will
// use.  Callers may trim the slice before passing it to Approve if they want
// to restrict access at approval time.
func DefaultPerms() []Perm {
	out := make([]Perm, len(allPerms))
	copy(out, allPerms)
	return out
}

// validPerm reports whether p is a recognised permission constant.
func validPerm(p Perm) bool {
	for _, v := range allPerms {
		if p == v {
			return true
		}
	}
	return false
}

// ─── Contact state ────────────────────────────────────────────────────────────

// State is the trust state of a contact in the allow-list state machine.
type State string

const (
	// StatePending means the contact has been added (or re-unblocked) but the
	// local user has not yet approved them.
	StatePending State = "pending"

	// StateApproved means the local user has explicitly accepted the contact.
	// Only approved contacts may send traffic that passes IsApproved/Can checks.
	StateApproved State = "approved"

	// StateBlocked means the local user has rejected the contact.  Requests
	// from blocked contacts are silently dropped.  The contact is not informed.
	StateBlocked State = "blocked"
)

// ─── Contact record ───────────────────────────────────────────────────────────

// Contact is a single entry in the contacts store.
type Contact struct {
	// VulosID is the peer's canonical Vula ID ("vulos:ed25519:<base58>").
	VulosID string `json:"vulos_id"`

	// DisplayName is the peer's self-reported display name.
	DisplayName string `json:"display_name"`

	// Server is the peer's Vula server address ("host:port").  Used to reach
	// the peer for outbound deliveries.  May be empty for contacts received
	// via an inbound request before their server address is known.
	Server string `json:"server,omitempty"`

	// VulosAddress is the peer's Vulos identity address ("user@vulos.org"
	// or "user@custom-domain").  It is populated from the signed contact-card
	// JSON exchanged during peering invitation and serves as a first-class
	// identity for composing mail to this contact.  May be empty for contacts
	// added before IDENTITY-07 or peers that have not yet claimed a Vulos
	// identity.
	VulosAddress string `json:"vulos_address,omitempty"`

	// State is the current trust state.
	State State `json:"state"`

	// Permissions is the set of capabilities granted to this contact.
	// Only meaningful when State == StateApproved; empty otherwise.
	Permissions []Perm `json:"permissions,omitempty"`

	// AddedAt is when the contact was first added (pending created).
	AddedAt time.Time `json:"added_at"`

	// ApprovedAt is when the contact was most recently approved.
	// Zero value if never approved.
	ApprovedAt time.Time `json:"approved_at,omitempty"`

	// BlockedAt is when the contact was most recently blocked.
	// Zero value if never blocked.
	BlockedAt time.Time `json:"blocked_at,omitempty"`
}

// hasPermission reports whether c has been granted p.
func (c *Contact) hasPermission(p Perm) bool {
	for _, g := range c.Permissions {
		if g == p {
			return true
		}
	}
	return false
}

// ─── Disk format ─────────────────────────────────────────────────────────────

// diskStore is the JSON object persisted to contacts.json.
type diskStore struct {
	Contacts []*Contact `json:"contacts"`
}

// ─── ContactStore ─────────────────────────────────────────────────────────────

// ContactStore is the in-memory + on-disk contacts store.
// The zero value is not usable; obtain one via NewContactStore.
type ContactStore struct {
	mu       sync.RWMutex
	byID     map[string]*Contact // VulosID → *Contact
	filePath string              // absolute path to contacts.json
}

// NewContactStore opens (or creates) the contacts store rooted at
// filepath.Join(root, "peering", "contacts.json").
//
// If the file does not exist it is created with an empty contacts list.
// If the file exists but is malformed, NewContactStore returns an error.
func NewContactStore(home string) (*ContactStore, error) {
	dir := filepath.Join(home, "peering")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("peering/contacts: mkdir %s: %w", dir, err)
	}

	fp := filepath.Join(dir, "contacts.json")
	cs := &ContactStore{
		byID:     make(map[string]*Contact),
		filePath: fp,
	}

	if err := cs.load(); err != nil {
		return nil, err
	}

	return cs, nil
}

// ─── Persistence ─────────────────────────────────────────────────────────────

// load reads contacts.json into memory.  If the file does not exist it writes
// an empty store first and returns nil.  Caller must NOT hold cs.mu.
func (cs *ContactStore) load() error {
	data, err := os.ReadFile(cs.filePath)
	if errors.Is(err, os.ErrNotExist) {
		// First boot: persist an empty store.
		return cs.persist()
	}
	if err != nil {
		return fmt.Errorf("peering/contacts: read %s: %w", cs.filePath, err)
	}

	var ds diskStore
	if err := json.Unmarshal(data, &ds); err != nil {
		return fmt.Errorf("peering/contacts: parse %s: %w", cs.filePath, err)
	}

	for _, c := range ds.Contacts {
		if c.VulosID == "" {
			continue // skip corrupt entries
		}
		// Deep-copy so the map owns the pointer.
		dup := *c
		cs.byID[c.VulosID] = &dup
	}

	return nil
}

// persist writes the current in-memory state to disk atomically.
// Caller MUST hold cs.mu (at least read lock; write lock recommended).
func (cs *ContactStore) persist() error {
	ds := diskStore{
		Contacts: make([]*Contact, 0, len(cs.byID)),
	}
	for _, c := range cs.byID {
		dup := *c
		ds.Contacts = append(ds.Contacts, &dup)
	}

	data, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		return fmt.Errorf("peering/contacts: marshal: %w", err)
	}

	// Atomic write: temp file in same directory + rename.
	dir := filepath.Dir(cs.filePath)
	tmp, err := os.CreateTemp(dir, ".contacts-*.json.tmp")
	if err != nil {
		return fmt.Errorf("peering/contacts: temp file: %w", err)
	}
	tmpName := tmp.Name()

	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmpName)
		if werr != nil {
			return fmt.Errorf("peering/contacts: write temp: %w", werr)
		}
		return fmt.Errorf("peering/contacts: close temp: %w", cerr)
	}

	if err := os.Rename(tmpName, cs.filePath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("peering/contacts: rename: %w", err)
	}

	return nil
}

// ─── Mutation API ─────────────────────────────────────────────────────────────

// Add inserts a new contact in StatePending.  If a contact with vulosID already
// exists Add returns an error; use Update to change existing contacts.
//
// vulosID must be non-empty.  displayName and server may be empty strings.
func (cs *ContactStore) Add(vulosID, displayName, server string) error {
	if vulosID == "" {
		return errors.New("peering/contacts: Add: vulosID must not be empty")
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()

	if _, exists := cs.byID[vulosID]; exists {
		return fmt.Errorf("peering/contacts: Add: contact %q already exists", vulosID)
	}

	c := &Contact{
		VulosID:     vulosID,
		DisplayName: displayName,
		Server:      server,
		State:       StatePending,
		AddedAt:     time.Now().UTC(),
	}
	cs.byID[vulosID] = c

	return cs.persist()
}

// Approve transitions a contact to StateApproved and sets their permissions.
// The contact must already exist and be in StatePending or StateApproved (the
// latter allows updating permissions without re-going through pending).
//
// perms must be non-nil and contain only recognised permission constants.
// Use DefaultPerms() to grant all permissions.
func (cs *ContactStore) Approve(vulosID string, perms []Perm) error {
	if vulosID == "" {
		return errors.New("peering/contacts: Approve: vulosID must not be empty")
	}
	if len(perms) == 0 {
		return errors.New("peering/contacts: Approve: perms must not be empty")
	}
	for _, p := range perms {
		if !validPerm(p) {
			return fmt.Errorf("peering/contacts: Approve: unknown permission %q", p)
		}
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()

	c, exists := cs.byID[vulosID]
	if !exists {
		return fmt.Errorf("peering/contacts: Approve: contact %q not found", vulosID)
	}

	switch c.State {
	case StatePending, StateApproved:
		// allowed
	case StateBlocked:
		return fmt.Errorf("peering/contacts: Approve: contact %q is blocked; unblock first", vulosID)
	default:
		return fmt.Errorf("peering/contacts: Approve: unexpected state %q", c.State)
	}

	// Deduplicate permissions.
	seen := make(map[Perm]struct{}, len(perms))
	out := make([]Perm, 0, len(perms))
	for _, p := range perms {
		if _, dup := seen[p]; !dup {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}

	c.State = StateApproved
	c.Permissions = out
	c.ApprovedAt = time.Now().UTC()

	return cs.persist()
}

// Block transitions a contact to StateBlocked.  The contact must exist.
// Blocked contacts may be in any prior state (pending or approved).
func (cs *ContactStore) Block(vulosID string) error {
	if vulosID == "" {
		return errors.New("peering/contacts: Block: vulosID must not be empty")
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()

	c, exists := cs.byID[vulosID]
	if !exists {
		return fmt.Errorf("peering/contacts: Block: contact %q not found", vulosID)
	}

	if c.State == StateBlocked {
		return nil // idempotent
	}

	c.State = StateBlocked
	c.Permissions = nil
	c.BlockedAt = time.Now().UTC()

	return cs.persist()
}

// Unblock transitions a blocked contact back to StatePending so the local user
// can decide whether to approve them again.  Returns an error if the contact
// is not in StateBlocked.
func (cs *ContactStore) Unblock(vulosID string) error {
	if vulosID == "" {
		return errors.New("peering/contacts: Unblock: vulosID must not be empty")
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()

	c, exists := cs.byID[vulosID]
	if !exists {
		return fmt.Errorf("peering/contacts: Unblock: contact %q not found", vulosID)
	}

	if c.State != StateBlocked {
		return fmt.Errorf("peering/contacts: Unblock: contact %q is not blocked (state=%q)", vulosID, c.State)
	}

	c.State = StatePending
	c.Permissions = nil

	return cs.persist()
}

// Remove permanently deletes a contact from the store.  The contact must exist.
// Any state (pending, approved, blocked) can be removed.
func (cs *ContactStore) Remove(vulosID string) error {
	if vulosID == "" {
		return errors.New("peering/contacts: Remove: vulosID must not be empty")
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()

	if _, exists := cs.byID[vulosID]; !exists {
		return fmt.Errorf("peering/contacts: Remove: contact %q not found", vulosID)
	}

	delete(cs.byID, vulosID)

	return cs.persist()
}

// UpdateServer sets or updates the server address for an existing contact.
// This is used when we learn a peer's current server address from an inbound
// envelope or a discovery lookup.
func (cs *ContactStore) UpdateServer(vulosID, server string) error {
	if vulosID == "" {
		return errors.New("peering/contacts: UpdateServer: vulosID must not be empty")
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()

	c, exists := cs.byID[vulosID]
	if !exists {
		return fmt.Errorf("peering/contacts: UpdateServer: contact %q not found", vulosID)
	}

	c.Server = server

	return cs.persist()
}

// UpdateVulosAddress sets or updates the Vulos identity address for an
// existing contact.  address may be empty to clear a previously-set value.
// This is called when a peering invitation card arrives carrying a
// vulos_address field, or when a relay key-directory lookup populates the
// address retroactively.
func (cs *ContactStore) UpdateVulosAddress(vulosID, address string) error {
	if vulosID == "" {
		return errors.New("peering/contacts: UpdateVulosAddress: vulosID must not be empty")
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()

	c, exists := cs.byID[vulosID]
	if !exists {
		return fmt.Errorf("peering/contacts: UpdateVulosAddress: contact %q not found", vulosID)
	}

	c.VulosAddress = address

	return cs.persist()
}

// LookupByVulosAddress returns the contact whose VulosAddress matches
// address (case-insensitive), or (nil, false) if no such contact exists.
// This allows the mail compose UI to resolve a Vulos address to a contact
// without requiring the caller to iterate the full list.
func (cs *ContactStore) LookupByVulosAddress(address string) (*Contact, bool) {
	if address == "" {
		return nil, false
	}

	// Normalise to lower-case for a case-insensitive match.
	lower := toLower(address)

	cs.mu.RLock()
	defer cs.mu.RUnlock()

	for _, c := range cs.byID {
		if toLower(c.VulosAddress) == lower {
			dup := *c
			if len(c.Permissions) > 0 {
				dup.Permissions = make([]Perm, len(c.Permissions))
				copy(dup.Permissions, c.Permissions)
			}
			return &dup, true
		}
	}
	return nil, false
}

// toLower is a minimal ASCII-only lower-case helper used for Vulos address
// normalisation.  Vulos identity addresses are ASCII by spec; using strings.ToLower
// would require importing "strings" which the rest of this file does not need.
func toLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

// UpdatePermissions replaces the permission set for an approved contact.
// Returns an error if the contact is not in StateApproved.
func (cs *ContactStore) UpdatePermissions(vulosID string, perms []Perm) error {
	if vulosID == "" {
		return errors.New("peering/contacts: UpdatePermissions: vulosID must not be empty")
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()

	c, exists := cs.byID[vulosID]
	if !exists {
		return fmt.Errorf("peering/contacts: UpdatePermissions: contact %q not found", vulosID)
	}

	if c.State != StateApproved {
		return fmt.Errorf("peering/contacts: UpdatePermissions: contact %q is not approved (state=%q)", vulosID, c.State)
	}

	for _, p := range perms {
		if !validPerm(p) {
			return fmt.Errorf("peering/contacts: UpdatePermissions: unknown permission %q", p)
		}
	}

	// Deduplicate.
	seen := make(map[Perm]struct{}, len(perms))
	out := make([]Perm, 0, len(perms))
	for _, p := range perms {
		if _, dup := seen[p]; !dup {
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}

	c.Permissions = out

	return cs.persist()
}

// ─── Query API ────────────────────────────────────────────────────────────────

// Get returns a snapshot of the contact with the given Vula ID, or
// (nil, false) if the contact does not exist.
func (cs *ContactStore) Get(vulosID string) (*Contact, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	c, ok := cs.byID[vulosID]
	if !ok {
		return nil, false
	}

	// Return a copy so the caller cannot mutate store state.
	dup := *c
	if len(c.Permissions) > 0 {
		dup.Permissions = make([]Perm, len(c.Permissions))
		copy(dup.Permissions, c.Permissions)
	}
	return &dup, true
}

// List returns a snapshot of all contacts.  The returned slice is in no
// guaranteed order.  Each element is a copy.
func (cs *ContactStore) List() []*Contact {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	out := make([]*Contact, 0, len(cs.byID))
	for _, c := range cs.byID {
		dup := *c
		if len(c.Permissions) > 0 {
			dup.Permissions = make([]Perm, len(c.Permissions))
			copy(dup.Permissions, c.Permissions)
		}
		out = append(out, &dup)
	}
	return out
}

// ListByState returns contacts whose state matches the given value.
func (cs *ContactStore) ListByState(state State) []*Contact {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	var out []*Contact
	for _, c := range cs.byID {
		if c.State != state {
			continue
		}
		dup := *c
		if len(c.Permissions) > 0 {
			dup.Permissions = make([]Perm, len(c.Permissions))
			copy(dup.Permissions, c.Permissions)
		}
		out = append(out, &dup)
	}
	return out
}

// ─── Predicate API ────────────────────────────────────────────────────────────

// IsApproved reports whether the contact with the given Vula ID exists and is
// in StateApproved.  This is the primary gate check for inbound traffic.
func (cs *ContactStore) IsApproved(vulosID string) bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	c, ok := cs.byID[vulosID]
	return ok && c.State == StateApproved
}

// Can reports whether the contact is approved AND has been granted the given
// permission.  Returns false for any non-approved contact regardless of
// permissions.
func (cs *ContactStore) Can(vulosID string, p Perm) bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	c, ok := cs.byID[vulosID]
	if !ok || c.State != StateApproved {
		return false
	}
	return c.hasPermission(p)
}
