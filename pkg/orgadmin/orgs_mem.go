package orgadmin

// orgs_mem.go — in-memory OrgStore for tests / dev mode. Concurrency-safe.

import (
	"context"
	"sync"
)

// MemOrgStore is the dep-free reference OrgStore.
type MemOrgStore struct {
	mu      sync.Mutex
	orgs    map[string]OrgRecord         // id → org
	members map[string]map[string]string // orgID → (accountID → role)
	active  map[string]string            // accountID → orgID
}

// NewMemOrgStore constructs an empty MemOrgStore.
func NewMemOrgStore() *MemOrgStore {
	return &MemOrgStore{
		orgs:    map[string]OrgRecord{},
		members: map[string]map[string]string{},
		active:  map[string]string{},
	}
}

// CreateOrg implements OrgStore.
func (m *MemOrgStore) CreateOrg(_ context.Context, rec OrgRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.orgs {
		if o.Slug == rec.Slug {
			return ErrSlugUnavailable
		}
	}
	m.orgs[rec.ID] = rec
	if m.members[rec.ID] == nil {
		m.members[rec.ID] = map[string]string{}
	}
	m.members[rec.ID][rec.OwnerAccount] = string(RoleOwner)
	return nil
}

// CreateOrgAtomic implements OrgStore. The whole count → decide → insert sequence
// runs under m.mu, so it is atomic for this in-memory store (the reference for the
// SQLStore's per-account-locked transaction).
func (m *MemOrgStore) CreateOrgAtomic(_ context.Context, rec OrgRecord, gate OrgCreateGate) (OrgRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Authoritative owned-count under the lock.
	owned := 0
	for _, o := range m.orgs {
		if o.OwnerAccount == rec.OwnerAccount {
			owned++
		}
	}
	if owned >= 1 {
		if gate.Cap > 0 && owned >= gate.Cap {
			return OrgRecord{}, ErrOrgLimitReached
		}
		if gate.MaxPerWindow > 0 && gate.WindowSince != "" {
			recent := 0
			for _, o := range m.orgs {
				if o.OwnerAccount == rec.OwnerAccount && o.CreatedAt >= gate.WindowSince {
					recent++
				}
			}
			if recent >= gate.MaxPerWindow {
				return OrgRecord{}, ErrOrgLimitReached
			}
		}
	}

	if gate.MailDomain != "" && (owned == 0 || gate.PaidPlan) {
		rec.RootMailbox = rec.Slug + "@" + gate.MailDomain
		rec.MailboxDomain = gate.MailDomain
		rec.MailboxState = MailboxPending
	}

	for _, o := range m.orgs {
		if o.Slug == rec.Slug {
			return OrgRecord{}, ErrSlugUnavailable
		}
	}
	m.orgs[rec.ID] = rec
	if m.members[rec.ID] == nil {
		m.members[rec.ID] = map[string]string{}
	}
	m.members[rec.ID][rec.OwnerAccount] = string(RoleOwner)
	return rec, nil
}

// GetOrg implements OrgStore.
func (m *MemOrgStore) GetOrg(_ context.Context, id string) (OrgRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.orgs[id]
	if !ok {
		return OrgRecord{}, ErrNotFound
	}
	return rec, nil
}

// SlugExists implements OrgStore.
func (m *MemOrgStore) SlugExists(_ context.Context, slug string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.orgs {
		if o.Slug == slug {
			return true, nil
		}
	}
	return false, nil
}

// AddOrgMember implements OrgStore.
func (m *MemOrgStore) AddOrgMember(_ context.Context, orgID, accountID, role string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.members[orgID] == nil {
		m.members[orgID] = map[string]string{}
	}
	m.members[orgID][accountID] = role
	return nil
}

// OrgMemberRole implements OrgStore.
func (m *MemOrgStore) OrgMemberRole(_ context.Context, orgID, accountID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if roles, ok := m.members[orgID]; ok {
		if role, ok := roles[accountID]; ok {
			return role, nil
		}
	}
	return "", ErrNotFound
}

// ListOrgsForAccount implements OrgStore (oldest-first by created_at then id).
func (m *MemOrgStore) ListOrgsForAccount(_ context.Context, accountID string) ([]OrgMembership, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []OrgMembership
	for orgID, roles := range m.members {
		role, ok := roles[accountID]
		if !ok {
			continue
		}
		if rec, ok := m.orgs[orgID]; ok {
			out = append(out, OrgMembership{OrgRecord: rec, Role: role})
		}
	}
	// Deterministic ordering: created_at, then id.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a, b := out[j-1], out[j]
			if a.CreatedAt > b.CreatedAt || (a.CreatedAt == b.CreatedAt && a.ID > b.ID) {
				out[j-1], out[j] = out[j], out[j-1]
			} else {
				break
			}
		}
	}
	if out == nil {
		out = []OrgMembership{}
	}
	return out, nil
}

// CountOrgsOwnedByAccount implements OrgStore.
func (m *MemOrgStore) CountOrgsOwnedByAccount(_ context.Context, accountID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, o := range m.orgs {
		if o.OwnerAccount == accountID {
			n++
		}
	}
	return n, nil
}

// CountOrgsOwnedSince implements OrgStore. CreatedAt is RFC3339 UTC, so a string
// >= comparison is a correct chronological comparison.
func (m *MemOrgStore) CountOrgsOwnedSince(_ context.Context, accountID, sinceRFC3339 string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, o := range m.orgs {
		if o.OwnerAccount == accountID && o.CreatedAt >= sinceRFC3339 {
			n++
		}
	}
	return n, nil
}

// SetMailboxState implements OrgStore.
func (m *MemOrgStore) SetMailboxState(_ context.Context, orgID, state string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rec, ok := m.orgs[orgID]; ok {
		rec.MailboxState = state
		m.orgs[orgID] = rec
	}
	return nil
}

// SetActiveOrg implements OrgStore.
func (m *MemOrgStore) SetActiveOrg(_ context.Context, accountID, orgID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active[accountID] = orgID
	return nil
}

// GetActiveOrg implements OrgStore.
func (m *MemOrgStore) GetActiveOrg(_ context.Context, accountID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[accountID], nil
}

// RemoveOrgMember implements OrgStore. Idempotent.
func (m *MemOrgStore) RemoveOrgMember(_ context.Context, orgID, accountID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if roles, ok := m.members[orgID]; ok {
		delete(roles, accountID)
	}
	return nil
}

// CountOrgOwners implements OrgStore.
func (m *MemOrgStore) CountOrgOwners(_ context.Context, orgID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, role := range m.members[orgID] {
		if role == string(RoleOwner) {
			n++
		}
	}
	return n, nil
}
