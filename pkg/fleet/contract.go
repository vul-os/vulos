// Package fleet is the vulos.cloud control-plane fleet subsystem
// (see ../../FLEET_API.md and roadmap/FLEET.md).
//
// THIS FILE IS THE FROZEN CONTRACT. Do not edit it in feature branches —
// every fleet agent builds against these types, this Store interface, the
// reference MemStore (which DEFINES Store semantics), and the pure helper
// DeriveStatus. The SQL Store implementation must match MemStore
// behaviourally; handlers depend only on the interface.
package fleet

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/idents"
)

// Sentinel errors. Handlers map these to HTTP status codes (see FLEET_API.md).
var (
	ErrInvalidRole    = errors.New("fleet: invalid role")
	ErrNotMember      = errors.New("fleet: caller not a member of org")
	ErrNotAdmin       = errors.New("fleet: caller is not admin/owner")
	ErrDeviceNotFound = errors.New("fleet: device not found")
	ErrOrgNotFound    = errors.New("fleet: org not found")
	ErrInviteNotFound = errors.New("fleet: invite not found")
)

// ValidRoles is the set of allowed org member roles.
var ValidRoles = map[string]bool{
	"owner":  true,
	"admin":  true,
	"member": true,
}

// adminRoles are roles that may perform admin actions.
var adminRoles = map[string]bool{
	"owner": true,
	"admin": true,
}

// Org is a fleet organisation that groups devices.
type Org struct {
	ID           string
	Name         string
	OwnerAccount string
	CreatedAt    time.Time
}

// Member is an account's membership in an org.
//
// DisplayName is a fleet-local human name for the member, set at invite/accept/
// add time from the invited user's name if known (see SetDisplayName via the
// MemberNamer seam). It may be empty — callers fall back to the member's email.
// It is fleet-local because auth.users is email+password only and carries no
// canonical display/full name to join against.
type Member struct {
	OrgID       string
	AccountID   string
	Role        string
	JoinedAt    time.Time
	DisplayName string `json:"display_name"`
}

// Device is an enrolled device tracked by the fleet subsystem.
type Device struct {
	ULID           string
	OrgID          string
	AccountID      string
	Name           string
	GroupLabel     string
	Version        string
	Channel        string
	Health         string
	LastHeartbeat  *time.Time
	CrashCount     int
	Decommissioned bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Heartbeat is a single telemetry report from a device.
type Heartbeat struct {
	ULID       string
	Version    string
	Health     string
	SyncLagSec int
	UptimeSec  int
	CrashCount int
	ReceivedAt time.Time
}

// Invite is a pending org invitation.
//
// DisplayName is the invited user's human name, carried from invite-creation
// time so it can be applied to the membership row's DisplayName when the member
// is created from this invite (see the MemberInviter seam in invite_name.go and
// MemberNamer.SetDisplayName in member_name.go). It may be empty — a nameless
// invite leaves the eventual member's DisplayName empty and the org-admin
// Members adapter falls back to the member's email. Additive field (mirrors
// Member.DisplayName); the frozen 3-arg CreateInvite never populates it.
type Invite struct {
	ID          string
	OrgID       string
	Email       string
	Role        string
	State       string
	CreatedAt   time.Time
	DisplayName string `json:"display_name"`
}

// Store is the fleet persistence contract. The SQL implementation and MemStore
// both satisfy it; handlers depend ONLY on this interface. MemStore is the
// behavioural source of truth.
type Store interface {
	// Org management.
	CreateOrg(ctx context.Context, name, ownerAccountID string) (Org, error)
	GetOrg(ctx context.Context, id string) (Org, error)

	// Membership management.
	AddMember(ctx context.Context, orgID, accountID, role string) error
	SetRole(ctx context.Context, orgID, accountID, role string) error
	Members(ctx context.Context, orgID string) ([]Member, error)
	// MemberRole returns the role string, or ErrNotMember if not a member.
	MemberRole(ctx context.Context, orgID, accountID string) (string, error)

	// Device management.
	UpsertDevice(ctx context.Context, d Device) error
	GetDevice(ctx context.Context, ulid string) (Device, error)
	ListDevices(ctx context.Context, orgID string, limit, offset int) ([]Device, error)
	ListByAccount(ctx context.Context, accountID string, limit, offset int) ([]Device, error)
	SetName(ctx context.Context, ulid, name string) error
	SetGroup(ctx context.Context, ulid, group string) error
	SetChannel(ctx context.Context, ulid, channel string) error
	Decommission(ctx context.Context, ulid string) error

	// Heartbeat / telemetry.
	InsertHeartbeat(ctx context.Context, hb Heartbeat) (Device, error) // returns updated device
	RecentHeartbeats(ctx context.Context, ulid string, n int) ([]Heartbeat, error)
	// PurgeHeartbeatsBefore deletes heartbeats older than cutoff (retention —
	// fleet_heartbeats is append-only and otherwise grows without bound). Returns
	// the number of rows removed.
	PurgeHeartbeatsBefore(ctx context.Context, cutoff time.Time) (int64, error)

	// Seat / billing helpers.
	SeatCount(ctx context.Context, orgID string) (int, error) // non-decommissioned devices

	// Invites.
	CreateInvite(ctx context.Context, orgID, email, role string) (Invite, error)
	GetInvite(ctx context.Context, id string) (Invite, error)
	RevokeInvite(ctx context.Context, id string) error
	ListInvites(ctx context.Context, orgID string) ([]Invite, error)

	Close() error
}

// Status is the device connectivity status as reported by DeriveStatus.
type Status string

const (
	StatusOnline  Status = "online"
	StatusStale   Status = "stale"
	StatusOffline Status = "offline"
)

// DeriveStatus computes the device online/stale/offline status from the last
// heartbeat timestamp and the current time. It is a pure function (no global
// clock). Returns:
//   - StatusOnline  if last heartbeat was ≤ 60 seconds ago
//   - StatusStale   if 60 s < last heartbeat age ≤ 5 minutes
//   - StatusOffline if last heartbeat was > 5 minutes ago, or never received
func DeriveStatus(lastHB *time.Time, now time.Time) Status {
	if lastHB == nil {
		return StatusOffline
	}
	ago := now.Sub(*lastHB)
	switch {
	case ago <= 60*time.Second:
		return StatusOnline
	case ago <= 5*time.Minute:
		return StatusStale
	default:
		return StatusOffline
	}
}

// newOrgULID returns a random 26-char Crockford base32 ID for org/invite rows.
//
// SECURITY: this previously used a time-seeded LCG, whose output is predictable
// from the (low-entropy, partly-known) wall-clock seed — an attacker who can
// guess the creation time can enumerate org/invite IDs. The MemStore is the
// reference store, so it must match the prod SQL path's crypto/rand source. We
// delegate to genULID (crypto/rand-backed, defined in store.go) rather than
// re-implement it.
func newOrgULID() string {
	_ = idents.ValidName // keep the idents dependency for portability checks
	return genULID()
}

// MemStore is the in-memory reference Store. Concurrency-safe. It DEFINES the
// semantics every other implementation must match.
type MemStore struct {
	mu         sync.Mutex
	orgs       map[string]Org
	members    map[string]map[string]Member // orgID -> accountID -> Member
	devices    map[string]Device            // ulid -> Device
	heartbeats []Heartbeat
	invites    map[string]Invite
}

// NewMemStore returns an empty reference store.
func NewMemStore() *MemStore {
	return &MemStore{
		orgs:    map[string]Org{},
		members: map[string]map[string]Member{},
		devices: map[string]Device{},
		invites: map[string]Invite{},
	}
}

// compile-time assertion: MemStore satisfies Store.
var _ Store = (*MemStore)(nil)

func (m *MemStore) CreateOrg(_ context.Context, name, ownerAccountID string) (Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	o := Org{
		ID:           newOrgULID(),
		Name:         name,
		OwnerAccount: ownerAccountID,
		CreatedAt:    time.Now().UTC(),
	}
	m.orgs[o.ID] = o

	// Auto-assign owner role.
	if m.members[o.ID] == nil {
		m.members[o.ID] = map[string]Member{}
	}
	m.members[o.ID][ownerAccountID] = Member{
		OrgID:     o.ID,
		AccountID: ownerAccountID,
		Role:      "owner",
		JoinedAt:  o.CreatedAt,
	}
	return o, nil
}

func (m *MemStore) GetOrg(_ context.Context, id string) (Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.orgs[id]
	if !ok {
		return Org{}, ErrOrgNotFound
	}
	return o, nil
}

func (m *MemStore) AddMember(_ context.Context, orgID, accountID, role string) error {
	if !ValidRoles[role] {
		return ErrInvalidRole
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.orgs[orgID]; !ok {
		return ErrOrgNotFound
	}
	if m.members[orgID] == nil {
		m.members[orgID] = map[string]Member{}
	}
	m.members[orgID][accountID] = Member{
		OrgID:     orgID,
		AccountID: accountID,
		Role:      role,
		JoinedAt:  time.Now().UTC(),
	}
	return nil
}

func (m *MemStore) SetRole(_ context.Context, orgID, accountID, role string) error {
	if !ValidRoles[role] {
		return ErrInvalidRole
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	mem, ok := m.members[orgID][accountID]
	if !ok {
		return ErrNotMember
	}
	mem.Role = role
	m.members[orgID][accountID] = mem
	return nil
}

func (m *MemStore) Members(_ context.Context, orgID string) ([]Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.orgs[orgID]; !ok {
		return nil, ErrOrgNotFound
	}
	var out []Member
	for _, mem := range m.members[orgID] {
		out = append(out, mem)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out, nil
}

func (m *MemStore) MemberRole(_ context.Context, orgID, accountID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mem, ok := m.members[orgID][accountID]
	if !ok {
		return "", ErrNotMember
	}
	return mem.Role, nil
}

func (m *MemStore) UpsertDevice(_ context.Context, d Device) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	if existing, ok := m.devices[d.ULID]; ok {
		// Preserve created_at on update.
		d.CreatedAt = existing.CreatedAt
		if d.UpdatedAt.IsZero() {
			d.UpdatedAt = now
		}
	} else {
		if d.CreatedAt.IsZero() {
			d.CreatedAt = now
		}
		if d.UpdatedAt.IsZero() {
			d.UpdatedAt = now
		}
	}
	m.devices[d.ULID] = d
	return nil
}

func (m *MemStore) GetDevice(_ context.Context, ulid string) (Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[ulid]
	if !ok {
		return Device{}, ErrDeviceNotFound
	}
	return d, nil
}

func (m *MemStore) ListDevices(_ context.Context, orgID string, limit, offset int) ([]Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []Device
	for _, d := range m.devices {
		if d.OrgID == orgID {
			all = append(all, d)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ULID < all[j].ULID })
	if offset >= len(all) {
		return []Device{}, nil
	}
	all = all[offset:]
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	return all, nil
}

func (m *MemStore) ListByAccount(_ context.Context, accountID string, limit, offset int) ([]Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []Device
	for _, d := range m.devices {
		if d.AccountID == accountID {
			all = append(all, d)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ULID < all[j].ULID })
	if offset >= len(all) {
		return []Device{}, nil
	}
	all = all[offset:]
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	return all, nil
}

func (m *MemStore) SetName(_ context.Context, ulid, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[ulid]
	if !ok {
		return ErrDeviceNotFound
	}
	d.Name = name
	d.UpdatedAt = time.Now().UTC()
	m.devices[ulid] = d
	return nil
}

func (m *MemStore) SetGroup(_ context.Context, ulid, group string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[ulid]
	if !ok {
		return ErrDeviceNotFound
	}
	d.GroupLabel = group
	d.UpdatedAt = time.Now().UTC()
	m.devices[ulid] = d
	return nil
}

func (m *MemStore) SetChannel(_ context.Context, ulid, channel string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[ulid]
	if !ok {
		return ErrDeviceNotFound
	}
	d.Channel = channel
	d.UpdatedAt = time.Now().UTC()
	m.devices[ulid] = d
	return nil
}

func (m *MemStore) Decommission(_ context.Context, ulid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[ulid]
	if !ok {
		return ErrDeviceNotFound
	}
	d.Decommissioned = true
	d.UpdatedAt = time.Now().UTC()
	m.devices[ulid] = d
	return nil
}

func (m *MemStore) InsertHeartbeat(_ context.Context, hb Heartbeat) (Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	hb.ReceivedAt = now

	// Upsert device row if absent (idempotent heartbeat creates device).
	d, ok := m.devices[hb.ULID]
	if !ok {
		d = Device{
			ULID:      hb.ULID,
			Channel:   "stable",
			Health:    "unknown",
			CreatedAt: now,
		}
	}

	// Update device from heartbeat.
	d.Version = hb.Version
	d.Health = hb.Health
	d.CrashCount = hb.CrashCount
	d.UpdatedAt = now
	t := now
	d.LastHeartbeat = &t

	m.devices[hb.ULID] = d
	m.heartbeats = append(m.heartbeats, hb)
	return d, nil
}

func (m *MemStore) RecentHeartbeats(_ context.Context, ulid string, n int) ([]Heartbeat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []Heartbeat
	for _, hb := range m.heartbeats {
		if hb.ULID == ulid {
			all = append(all, hb)
		}
	}
	// Most recent first.
	sort.Slice(all, func(i, j int) bool { return all[i].ReceivedAt.After(all[j].ReceivedAt) })
	if n > 0 && n < len(all) {
		all = all[:n]
	}
	return all, nil
}

// PurgeHeartbeatsBefore deletes heartbeats received before cutoff.
func (m *MemStore) PurgeHeartbeatsBefore(_ context.Context, cutoff time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.heartbeats[:0]
	var removed int64
	for _, hb := range m.heartbeats {
		if hb.ReceivedAt.Before(cutoff) {
			removed++
			continue
		}
		kept = append(kept, hb)
	}
	m.heartbeats = kept
	return removed, nil
}

func (m *MemStore) SeatCount(_ context.Context, orgID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, d := range m.devices {
		if d.OrgID == orgID && !d.Decommissioned {
			count++
		}
	}
	return count, nil
}

func (m *MemStore) CreateInvite(ctx context.Context, orgID, email, role string) (Invite, error) {
	// Delegate to the named variant (invite_name.go) with an empty name so the
	// frozen 3-arg path and the named path share one insert + stay identical.
	return m.CreateInviteWithName(ctx, orgID, email, role, "")
}

func (m *MemStore) GetInvite(_ context.Context, id string) (Invite, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.invites[id]
	if !ok {
		return Invite{}, ErrInviteNotFound
	}
	return inv, nil
}

func (m *MemStore) RevokeInvite(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	inv, ok := m.invites[id]
	if !ok {
		return ErrInviteNotFound
	}
	inv.State = "revoked"
	m.invites[id] = inv
	return nil
}

func (m *MemStore) ListInvites(_ context.Context, orgID string) ([]Invite, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Invite
	for _, inv := range m.invites {
		if inv.OrgID == orgID {
			out = append(out, inv)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *MemStore) Close() error { return nil }
