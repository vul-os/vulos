package syncrz

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
)

// MemObjectStore is an in-memory ObjectStore for tests and dev. Concurrency-
// safe. Holds bytes verbatim in maps — NEVER inspects them.
type MemObjectStore struct {
	mu      sync.Mutex
	buckets map[string]map[string][]byte
	// Inspections is incremented every time a byte payload is read from the
	// store via Get. Tests use it ONLY to assert that the coordinator's own
	// code paths never read a payload (the byte map itself stays opaque).
	// External pull / blob-fetch handlers obviously DO read it; that is the
	// whole point.
	Inspections int
}

// NewMemObjectStore returns an empty MemObjectStore.
func NewMemObjectStore() *MemObjectStore {
	return &MemObjectStore{buckets: map[string]map[string][]byte{}}
}

// Put stores opaque bytes under (bucket, key). Idempotent on (bucket, key)
// when the bytes match; on a hash collision with different bytes Put still
// overwrites — content-address verification lives one layer up.
func (m *MemObjectStore) Put(_ context.Context, bucket, key string, data []byte) error {
	if bucket == "" || key == "" {
		return ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.buckets[bucket]
	if !ok {
		b = map[string][]byte{}
		m.buckets[bucket] = b
	}
	// Copy so the caller cannot mutate stored bytes.
	cp := make([]byte, len(data))
	copy(cp, data)
	b[key] = cp
	return nil
}

// Get returns the bytes for (bucket, key) or ErrUnknown.
func (m *MemObjectStore) Get(_ context.Context, bucket, key string) ([]byte, error) {
	if bucket == "" || key == "" {
		return nil, ErrInvalidInput
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.buckets[bucket]
	if !ok {
		return nil, ErrUnknown
	}
	data, ok := b[key]
	if !ok {
		return nil, ErrUnknown
	}
	m.Inspections++
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

// Has reports whether (bucket, key) is present.
func (m *MemObjectStore) Has(_ context.Context, bucket, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.buckets[bucket]
	if !ok {
		return false, nil
	}
	_, ok = b[key]
	return ok, nil
}

// Snapshot returns a copy of (bucket, key) bytes for tests, or nil if absent.
// Distinct from Get so tests can peek without bumping Inspections.
func (m *MemObjectStore) Snapshot(bucket, key string) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.buckets[bucket]
	if !ok {
		return nil
	}
	v, ok := b[key]
	if !ok {
		return nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp
}

// Stream returns a ReadCloser over (bucket, key). Used by the handler's
// /api/sync/blob/<hash> proxy path. Bumps Inspections.
func (m *MemObjectStore) Stream(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	data, err := m.Get(ctx, bucket, key)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// String returns a short debug summary.
func (m *MemObjectStore) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fmt.Sprintf("MemObjectStore{buckets=%d}", len(m.buckets))
}

// compile-time assertion.
var _ ObjectStore = (*MemObjectStore)(nil)
