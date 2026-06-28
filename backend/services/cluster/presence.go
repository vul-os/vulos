// Package cluster implements cross-node cluster coordination for Vulos.
// This file provides advisory presence leases that let nodes detect when another
// node has a file open for editing. Leases are written to S3-compatible storage
// under the key leases/{user_id}/{sha256(file)} and expire after 60 seconds
// without a heartbeat renewal. No write is ever blocked — leases are advisory only.
package cluster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// LeaseStore is the minimal storage interface required by PresenceManager.
// The production implementation wraps the cluster S3 client; tests supply a mock.
type LeaseStore interface {
	// Put writes data to the given key. Overwrites silently.
	Put(key string, r io.Reader) error
	// Get returns a reader for the given key. Returns an error (ideally
	// wrapping os.ErrNotExist) when the key is absent.
	Get(key string) (io.ReadCloser, error)
	// Delete removes a key. It is not an error if the key is absent.
	Delete(key string) error
}

// Lease is the JSON document stored at leases/{user}/{hash} in S3.
type Lease struct {
	Node      string    `json:"node"`
	User      string    `json:"user"`
	File      string    `json:"file"`
	Heartbeat time.Time `json:"heartbeat"`
}

const (
	leasePrefix = "leases/"

	// leaseHeartbeatInterval is how often AcquireLease renews the lease in S3.
	// Named distinctly from the package-level heartbeatInterval in cluster.go.
	leaseHeartbeatInterval = 30 * time.Second

	// leaseStaleAfter is the duration after which a lease is considered stale.
	// Named distinctly from the package-level staleThreshold in cluster.go.
	leaseStaleAfter = 60 * time.Second
)

// PresenceManager manages advisory file-edit leases backed by a LeaseStore.
//
// Usage (orchestrator wires this up later):
//
//	pm := cluster.NewPresenceManager(nodeID, store)
//	stop := pm.AcquireLease(ctx, "alice", "/home/alice/notes.txt")
//	holder, fresh, _ := pm.CheckLease(ctx, "alice", "/home/alice/notes.txt")
//	pm.ReleaseLease(ctx, "alice", "/home/alice/notes.txt")
//	stop() // also stops the heartbeat
type PresenceManager struct {
	nodeID string
	store  LeaseStore

	mu     sync.Mutex
	timers map[string]context.CancelFunc // key -> heartbeat cancel
}

// NewPresenceManager creates a PresenceManager for the node identified by nodeID.
func NewPresenceManager(nodeID string, store LeaseStore) *PresenceManager {
	return &PresenceManager{
		nodeID: nodeID,
		store:  store,
		timers: make(map[string]context.CancelFunc),
	}
}

// leaseKey returns the S3 key for a (user, file) pair.
// The file path is hashed so that slashes and special characters in paths
// do not interfere with prefix-based listing.
func leaseKey(user, file string) string {
	h := sha256.Sum256([]byte(file))
	return fmt.Sprintf("%s%s/%x", leasePrefix, user, h)
}

// writeLease serialises a Lease and stores it at the appropriate S3 key.
func (pm *PresenceManager) writeLease(user, file string, t time.Time) error {
	l := Lease{
		Node:      pm.nodeID,
		User:      user,
		File:      file,
		Heartbeat: t,
	}
	data, err := json.Marshal(l)
	if err != nil {
		return fmt.Errorf("presence: marshal lease: %w", err)
	}
	if err := pm.store.Put(leaseKey(user, file), bytes.NewReader(data)); err != nil {
		return fmt.Errorf("presence: write lease: %w", err)
	}
	return nil
}

// AcquireLease writes an initial lease for (user, file) and starts a 30-second
// heartbeat that renews it while the context is live. The returned stop function
// cancels the heartbeat without deleting the lease; callers should call
// ReleaseLease explicitly when the file is closed.
//
// If a lease already exists this node's lease overwrites it — leases are advisory
// and never block progress.
func (pm *PresenceManager) AcquireLease(ctx context.Context, user, file string) (stop func()) {
	key := leaseKey(user, file)

	// Write the initial lease immediately (best-effort; never blocks caller).
	_ = pm.writeLease(user, file, time.Now().UTC())

	hbCtx, cancel := context.WithCancel(ctx)

	pm.mu.Lock()
	if prev, ok := pm.timers[key]; ok {
		prev() // cancel any previous heartbeat for the same key
	}
	pm.timers[key] = cancel
	pm.mu.Unlock()

	// Heartbeat goroutine: renew every leaseHeartbeatInterval until cancelled.
	go func() {
		ticker := time.NewTicker(leaseHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case t := <-ticker.C:
				// Best-effort renewal; ignore transient S3 errors.
				_ = pm.writeLease(user, file, t.UTC())
			}
		}
	}()

	return cancel
}

// CheckLease reads the current lease for (user, file) and returns:
//   - holder: the node ID that holds the lease (empty string if none / stale)
//   - fresh: true if the lease exists and its heartbeat is within leaseStaleAfter (60 s)
//   - err: non-nil only for unexpected store errors (absent key is not an error)
//
// A stale lease is silently ignored and (holder="", fresh=false) is returned.
// This call never blocks the caller from proceeding.
func (pm *PresenceManager) CheckLease(ctx context.Context, user, file string) (holder string, fresh bool, err error) {
	rc, err := pm.store.Get(leaseKey(user, file))
	if err != nil {
		// Treat any read error (including not-found) as "no lease".
		return "", false, nil //nolint:nilerr
	}
	defer rc.Close()

	var l Lease
	if err := json.NewDecoder(rc).Decode(&l); err != nil {
		// Corrupt lease document — treat as absent.
		return "", false, nil
	}

	if time.Since(l.Heartbeat) > leaseStaleAfter {
		// Lease exists but heartbeat is stale; advisory only, return empty.
		return "", false, nil
	}

	return l.Node, true, nil
}

// ReleaseLease deletes the lease for (user, file) and stops any running heartbeat
// previously created by AcquireLease on this manager instance.
func (pm *PresenceManager) ReleaseLease(ctx context.Context, user, file string) error {
	key := leaseKey(user, file)

	// Cancel the heartbeat goroutine if one is running.
	pm.mu.Lock()
	if cancel, ok := pm.timers[key]; ok {
		cancel()
		delete(pm.timers, key)
	}
	pm.mu.Unlock()

	if err := pm.store.Delete(key); err != nil {
		// Ignore "not found" — treat all store errors as non-fatal for release.
		if !leaseIsNotFound(err) {
			return fmt.Errorf("presence: release lease: %w", err)
		}
	}
	return nil
}

// leaseIsNotFound returns true for errors that indicate a key was absent.
// The LocalStore returns os.ErrNotExist wrapped inside *os.PathError; we also
// handle string matching for mock stores that use fmt.Errorf.
// Named with the "lease" prefix to avoid colliding with any isNotFound in the package.
func leaseIsNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "not exist") ||
		strings.Contains(s, "not found") ||
		strings.Contains(s, "no such file")
}
