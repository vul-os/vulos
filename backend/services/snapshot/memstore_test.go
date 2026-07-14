package snapshot

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"sync"
)

// memStore is an in-memory ObjectStore for tests. It faithfully models the
// contract the S3 adapter provides: recursive prefix listing, missing-object
// Stat returning exists=false, and idempotent Delete.
type memStore struct {
	mu   sync.Mutex
	data map[string][]byte
	// puts counts every successful Put keyed by object key; tests use it to
	// prove that unchanged blobs are NOT re-uploaded (incremental/dedupe).
	putCount map[string]int
}

func newMemStore() *memStore {
	return &memStore{data: map[string][]byte{}, putCount: map[string]int{}}
}

func (m *memStore) List(_ context.Context, prefix string) ([]ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ObjectInfo
	for k, v := range m.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, ObjectInfo{Key: k, Size: int64(len(v))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (m *memStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, &notFound{key}
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return io.NopCloser(bytes.NewReader(cp)), nil
}

func (m *memStore) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = b
	m.putCount[key]++
	return nil
}

func (m *memStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memStore) Stat(_ context.Context, key string) (ObjectInfo, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return ObjectInfo{}, false, nil
	}
	return ObjectInfo{Key: key, Size: int64(len(v))}, true, nil
}

// set / get / has are test conveniences bypassing the ObjectStore interface.
func (m *memStore) set(key string, b []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = append([]byte(nil), b...)
}

func (m *memStore) getRaw(key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	return v, ok
}

func (m *memStore) count(prefix string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}

type notFound struct{ key string }

func (e *notFound) Error() string { return "not found: " + e.key }
