// checker.go — VisibilityChecker interface and in-memory implementation for
// circuit-check authz. The httpVisibilityChecker (relay-side) lives in the
// relay forwarder package; this file provides the cp-side interface and the
// MemVisibilityChecker used in tests.
package secx

import "context"

// CircuitCheckRequest is the body sent to POST /api/secx/circuit-check.
type CircuitCheckRequest struct {
	AppID         string `json:"app_id"`
	FromAccountID string `json:"from_account_id"`
	ToULID        string `json:"to_ulid"`
}

// PublicCircuitQuota carries the per-tier public-circuit quota envelope
// attached to circuit-check responses for public apps.
type PublicCircuitQuota struct {
	Tier           string `json:"tier"`
	ConcurrencyCap int    `json:"concurrency_cap"`
}

// CircuitCheckResponse is the response from POST /api/secx/circuit-check.
type CircuitCheckResponse struct {
	Allowed     bool                `json:"allowed"`
	Reason      string              `json:"reason,omitempty"`
	PublicQuota *PublicCircuitQuota `json:"public_quota,omitempty"`
}

// VisibilityChecker is the interface the relay forwarder calls on circuit open.
// The httpVisibilityChecker calls the cp's /api/secx/circuit-check endpoint;
// MemVisibilityChecker is used in tests (and can be seeded directly).
type VisibilityChecker interface {
	Check(ctx context.Context, req CircuitCheckRequest) (CircuitCheckResponse, error)
}

// MemVisibilityChecker is an injectable test double for VisibilityChecker.
// It satisfies the interface and returns a pre-seeded result or falls back
// to allowed=true for unknown (app_id, from_account_id) pairs.
type MemVisibilityChecker struct {
	// Results maps "app_id\x00from_account_id" to the canned response.
	Results map[string]CircuitCheckResponse
	// CallCount records how many times Check was invoked (for cache tests).
	CallCount int
}

// NewMemVisibilityChecker returns an empty MemVisibilityChecker that allows
// all circuits by default.
func NewMemVisibilityChecker() *MemVisibilityChecker {
	return &MemVisibilityChecker{
		Results: map[string]CircuitCheckResponse{},
	}
}

// Seed registers a canned result for the given (appID, fromAccountID) pair.
func (m *MemVisibilityChecker) Seed(appID, fromAccountID string, resp CircuitCheckResponse) {
	m.Results[appID+"\x00"+fromAccountID] = resp
}

// Check returns the seeded result for (app_id, from_account_id), or
// {Allowed: true} if no entry exists.
func (m *MemVisibilityChecker) Check(_ context.Context, req CircuitCheckRequest) (CircuitCheckResponse, error) {
	m.CallCount++
	key := req.AppID + "\x00" + req.FromAccountID
	if r, ok := m.Results[key]; ok {
		return r, nil
	}
	return CircuitCheckResponse{Allowed: true}, nil
}

// compile-time assertion: MemVisibilityChecker satisfies VisibilityChecker.
var _ VisibilityChecker = (*MemVisibilityChecker)(nil)
