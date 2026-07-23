// pop_admin.go — extra PoP-admin methods (ListPoPs) on MemStore.
// These extend MemStore without touching the frozen contract.go.
// The matching SQLStore method lives in store.go.
package routing

import (
	"context"
	"sort"
)

// ListPoPs returns all PoPs (healthy and unhealthy), sorted by ID.
func (m *MemStore) ListPoPs(_ context.Context) ([]PoP, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PoP, 0, len(m.pops))
	for _, p := range m.pops {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
