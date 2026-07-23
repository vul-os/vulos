package integrations

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemStore is an in-memory Store for tests. Semantically identical to SQLStore.
type MemStore struct {
	mu sync.Mutex
	m  map[string]Connection // key: accountID + "\x00" + provider
}

// NewMemStore returns an empty in-memory Store.
func NewMemStore() *MemStore { return &MemStore{m: make(map[string]Connection)} }

func memKey(accountID, provider string) string { return accountID + "\x00" + provider }

func (s *MemStore) Close() error { return nil }

func (s *MemStore) Upsert(_ context.Context, c Connection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	c.UpdatedAt = now
	if existing, ok := s.m[memKey(c.AccountID, c.Provider)]; ok {
		c.CreatedAt = existing.CreatedAt // preserve original created_at
	} else {
		c.CreatedAt = now
	}
	s.m[memKey(c.AccountID, c.Provider)] = c
	return nil
}

func (s *MemStore) Get(_ context.Context, accountID, provider string) (Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.m[memKey(accountID, provider)]
	if !ok {
		return Connection{}, ErrNotConnected
	}
	return c, nil
}

func (s *MemStore) SetAccessToken(_ context.Context, accountID, provider, accessTokenEnc string, expiry time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.m[memKey(accountID, provider)]
	if !ok {
		return ErrNotConnected
	}
	c.AccessTokenEnc = accessTokenEnc
	c.AccessExpiry = expiry
	c.UpdatedAt = time.Now().UTC()
	s.m[memKey(accountID, provider)] = c
	return nil
}

func (s *MemStore) SetRefreshToken(_ context.Context, accountID, provider, refreshTokenEnc string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.m[memKey(accountID, provider)]
	if !ok {
		return ErrNotConnected
	}
	c.RefreshTokenEnc = refreshTokenEnc
	c.UpdatedAt = time.Now().UTC()
	s.m[memKey(accountID, provider)] = c
	return nil
}

func (s *MemStore) Delete(_ context.Context, accountID, provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, memKey(accountID, provider))
	return nil
}

func (s *MemStore) List(_ context.Context, accountID string) ([]Connection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Connection
	for _, c := range s.m {
		if c.AccountID == accountID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out, nil
}

var _ Store = (*MemStore)(nil)
var _ Store = (*SQLStore)(nil)
