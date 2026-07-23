// vanity_admin.go — extra vanity-admin methods (release + list) on MemStore.
// These extend MemStore without touching the frozen contract.go.
// The matching SQLStore methods live in store.go.
package routing

import (
	"context"
	"sort"
)

// ReleaseVanity removes a vanity name owned by accountID from the in-memory
// store. Returns ErrNotFound if name is unknown, ErrNotOwner if accountID
// does not own it.
func (m *MemStore) ReleaseVanity(_ context.Context, name, accountID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.vanity[name]
	if !ok {
		return ErrNotFound
	}
	if v.AccountID != accountID {
		return ErrNotOwner
	}
	delete(m.vanity, name)
	return nil
}

// ListVanityByAccount returns all vanity records owned by accountID, sorted
// by name. Returns an empty (non-nil) slice if none.
func (m *MemStore) ListVanityByAccount(_ context.Context, accountID string) ([]Vanity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Vanity, 0)
	for _, v := range m.vanity {
		if v.AccountID == accountID {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
