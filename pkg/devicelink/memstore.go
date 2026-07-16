package devicelink

import (
	"context"
	"sync"
	"time"
)

// MemStore is the dep-free in-memory Store for tests / dev mode. It mirrors the
// SQLStore semantics: raw device_code / credential values are held only as
// hashes, and the same state machine (pending→approved→consumed) is enforced.
type MemStore struct {
	mu    sync.Mutex
	links map[string]*memLink // keyed by device_code_hash
	byUC  map[string]string   // user_code → device_code_hash
	creds map[string]string   // credential token_hash → account_id
	now   func() time.Time
}

type memLink struct {
	userCode  string
	accountID string
	state     string
	expiresAt time.Time
}

// NewMemStore constructs an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{
		links: make(map[string]*memLink),
		byUC:  make(map[string]string),
		creds: make(map[string]string),
		now:   time.Now,
	}
}

// SetClock overrides the time source (tests).
func (m *MemStore) SetClock(f func() time.Time) { m.now = f }

func (m *MemStore) clock() time.Time {
	if m.now != nil {
		return m.now().UTC()
	}
	return time.Now().UTC()
}

// StartLink implements Store.
func (m *MemStore) StartLink(_ context.Context, verificationURL string, interval, ttl time.Duration) (Start, error) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	deviceCode, err := randToken()
	if err != nil {
		return Start{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	var userCode, ucNorm string
	for attempt := 0; attempt < 5; attempt++ {
		uc, uerr := genUserCode()
		if uerr != nil {
			return Start{}, uerr
		}
		n := NormalizeUserCode(uc)
		if _, taken := m.byUC[n]; !taken {
			userCode, ucNorm = uc, n
			break
		}
	}
	dh := hash(deviceCode)
	m.links[dh] = &memLink{userCode: ucNorm, state: "pending", expiresAt: now.Add(ttl)}
	m.byUC[ucNorm] = dh
	return Start{
		DeviceCode:      deviceCode,
		UserCode:        userCode,
		VerificationURL: verificationURL,
		Interval:        interval,
		ExpiresIn:       ttl,
	}, nil
}

// Approve implements Store.
func (m *MemStore) Approve(_ context.Context, userCode, accountID string) error {
	if accountID == "" {
		return ErrBadInput
	}
	uc := NormalizeUserCode(userCode)
	if uc == "" {
		return ErrBadInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dh, ok := m.byUC[uc]
	if !ok {
		return ErrNotFound
	}
	l := m.links[dh]
	if l == nil {
		return ErrNotFound
	}
	now := m.clock()
	if now.After(l.expiresAt) {
		return ErrNotFound
	}
	if l.state == "approved" || l.state == "consumed" {
		return ErrConsumed
	}
	if l.state != "pending" {
		return ErrNotFound
	}
	l.state = "approved"
	l.accountID = accountID
	return nil
}

// Poll implements Store.
func (m *MemStore) Poll(_ context.Context, deviceCode string) (Credential, error) {
	if deviceCode == "" {
		return Credential{}, ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.links[hash(deviceCode)]
	if l == nil {
		return Credential{}, ErrNotFound
	}
	if m.clock().After(l.expiresAt) {
		return Credential{}, ErrNotFound
	}
	switch l.state {
	case "pending":
		return Credential{}, ErrPending
	case "denied":
		return Credential{}, ErrDenied
	case "consumed":
		return Credential{}, ErrConsumed
	case "approved":
		token, err := randToken()
		if err != nil {
			return Credential{}, err
		}
		l.state = "consumed"
		m.creds[hash(token)] = l.accountID
		return Credential{Token: token, AccountID: l.accountID}, nil
	default:
		return Credential{}, ErrNotFound
	}
}

// ResolveCredential implements Store.
func (m *MemStore) ResolveCredential(_ context.Context, token string) (string, error) {
	if token == "" {
		return "", ErrCredential
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	acct, ok := m.creds[hash(token)]
	if !ok {
		return "", ErrCredential
	}
	return acct, nil
}

// Close implements Store.
func (m *MemStore) Close() error { return nil }

var _ Store = (*MemStore)(nil)
