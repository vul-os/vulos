// mem.go — in-memory stubs for resolver tests (and minimal dev wiring).
package resolver

import (
	"context"
	"sync"
)

// ---------------------------------------------------------------------------
// MemStorageSource
// ---------------------------------------------------------------------------

// MemStorageSource is a concurrency-safe in-memory StorageSource for tests.
type MemStorageSource struct {
	mu       sync.Mutex
	accounts map[string]bool // accountID -> isSelfHost
}

// NewMemStorageSource returns an empty MemStorageSource.
func NewMemStorageSource() *MemStorageSource {
	return &MemStorageSource{accounts: map[string]bool{}}
}

// Set registers an account as hosted (selfHost=false) or self-hosted (selfHost=true).
func (m *MemStorageSource) Set(accountID string, selfHost bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accounts[accountID] = selfHost
}

// IsSelfHost implements StorageSource.
func (m *MemStorageSource) IsSelfHost(_ context.Context, accountID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.accounts[accountID]
	if !ok {
		return false, errStorageUnknown
	}
	return v, nil
}

// ---------------------------------------------------------------------------
// MemEnrollmentSource
// ---------------------------------------------------------------------------

// MemEnrollmentSource is a concurrency-safe in-memory EnrollmentSource for tests.
//
// "Registered box" is modelled in two independent slots:
//   - routes[accountID]  — fabric route (controls FabricRoute/selfhost path)
//   - boxIDs[accountID]  — registered box ID (controls LAN candidate)
//
// Tests can populate either or both depending on what they need to exercise
// (e.g. a hosted account with a co-located box: boxID set, route empty).
type MemEnrollmentSource struct {
	mu     sync.Mutex
	routes map[string]string // accountID -> fabricRoute
	boxIDs map[string]string // accountID -> registered box-id (LAN candidate)
}

// NewMemEnrollmentSource returns an empty MemEnrollmentSource.
func NewMemEnrollmentSource() *MemEnrollmentSource {
	return &MemEnrollmentSource{
		routes: map[string]string{},
		boxIDs: map[string]string{},
	}
}

// Set registers a fabric route for an account.  As a convenience, Set ALSO
// records the accountID as the box-id.  Tests that need to decouple the two
// slots should call SetBoxID explicitly.
func (m *MemEnrollmentSource) Set(accountID, fabricRoute string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes[accountID] = fabricRoute
	if _, ok := m.boxIDs[accountID]; !ok {
		m.boxIDs[accountID] = accountID
	}
}

// SetBoxID registers a box-id for the account without setting a fabric route.
// Use this to model a hosted account that has a co-located box for LAN
// failover.
func (m *MemEnrollmentSource) SetBoxID(accountID, boxID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.boxIDs[accountID] = boxID
}

// FabricRoute implements EnrollmentSource.
func (m *MemEnrollmentSource) FabricRoute(_ context.Context, accountID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.routes[accountID]
	if !ok || r == "" {
		return "", ErrNoEnrollment
	}
	return r, nil
}

// BoxID implements EnrollmentSource.
func (m *MemEnrollmentSource) BoxID(_ context.Context, accountID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.boxIDs[accountID]
	if !ok || id == "" {
		return "", ErrNoEnrollment
	}
	return id, nil
}
