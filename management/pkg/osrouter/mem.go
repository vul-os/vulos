package osrouter

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// MemDirectory is an in-memory reference Directory. It DEFINES the membership +
// cluster semantics the wiring adapter must match, and backs the package tests.
// Concurrency-safe.
type MemDirectory struct {
	mu sync.Mutex
	// orgs by id.
	orgs map[string]Org
	// slug → org id.
	bySlug map[string]string
	// account id → ordered org ids (membership).
	members map[string][]string
	// account id → role per org.
	roles map[string]map[string]string
	// account id → active org id.
	active map[string]string
	// org id → boxes.
	boxes map[string][]Box
	// box id (upper) → org id (for BoxByID).
	boxOrg map[string]string
}

// NewMemDirectory returns an empty in-memory directory.
func NewMemDirectory() *MemDirectory {
	return &MemDirectory{
		orgs:    map[string]Org{},
		bySlug:  map[string]string{},
		members: map[string][]string{},
		roles:   map[string]map[string]string{},
		active:  map[string]string{},
		boxes:   map[string][]Box{},
		boxOrg:  map[string]string{},
	}
}

// AddOrg registers an org (idempotent by id; slug indexed lowercased).
func (m *MemDirectory) AddOrg(o Org) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orgs[o.ID] = o
	if o.Slug != "" {
		m.bySlug[strings.ToLower(o.Slug)] = o.ID
	}
}

// AddMember adds accountID to orgID with role (order preserved; idempotent).
func (m *MemDirectory) AddMember(accountID, orgID, role string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.members[accountID] {
		if id == orgID {
			if m.roles[accountID] == nil {
				m.roles[accountID] = map[string]string{}
			}
			m.roles[accountID][orgID] = role
			return
		}
	}
	m.members[accountID] = append(m.members[accountID], orgID)
	if m.roles[accountID] == nil {
		m.roles[accountID] = map[string]string{}
	}
	m.roles[accountID][orgID] = role
}

// SetActive records the account's default/active org.
func (m *MemDirectory) SetActive(accountID, orgID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active[accountID] = orgID
}

// AddBox registers a box in its org's cluster (idempotent by ID within the org).
func (m *MemDirectory) AddBox(b Box) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b.ID = strings.ToUpper(b.ID)
	list := m.boxes[b.OrgID]
	replaced := false
	for i, existing := range list {
		if existing.ID == b.ID {
			list[i] = b
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, b)
	}
	m.boxes[b.OrgID] = list
	m.boxOrg[b.ID] = b.OrgID
}

func (m *MemDirectory) OrgsForAccount(_ context.Context, accountID string) ([]Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := m.members[accountID]
	out := make([]Org, 0, len(ids))
	for _, id := range ids {
		o, ok := m.orgs[id]
		if !ok {
			continue
		}
		if r := m.roles[accountID][id]; r != "" {
			o.Role = r
		}
		out = append(out, o)
	}
	return out, nil
}

func (m *MemDirectory) ActiveOrg(_ context.Context, accountID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active[accountID], nil
}

func (m *MemDirectory) BoxesForOrg(_ context.Context, orgID string) ([]Box, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	src := m.boxes[orgID]
	out := make([]Box, len(src))
	copy(out, src)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *MemDirectory) BoxByID(_ context.Context, boxID string) (Box, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	boxID = strings.ToUpper(boxID)
	orgID, ok := m.boxOrg[boxID]
	if !ok {
		return Box{}, ErrNotFound
	}
	for _, b := range m.boxes[orgID] {
		if b.ID == boxID {
			return b, nil
		}
	}
	return Box{}, ErrNotFound
}

// compile-time assertion.
var _ Directory = (*MemDirectory)(nil)
