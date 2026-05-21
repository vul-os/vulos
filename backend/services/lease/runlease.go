package lease

// runlease.go — per-profile-per-app run-lease for singleton apps (CONC-02).
//
// AcquireRunLease is the entry point used by the launcher.  It:
//   - Ensures the lease object exists (idempotent bootstrap).
//   - Attempts to Acquire leases/run/<profile>/<app>.json with the given TTL.
//   - On success returns a *RunLease that the caller uses to Renew (while the
//     app is running) and Release (on stop).
//   - On ErrNotHolder / ErrLost (another instance holds it) returns
//     (nil, ErrNotHolder) so the launcher can skip launching a duplicate.
//
// RunScope returns the canonical scope string so external code (e.g. tests)
// can seed / inspect the lease object.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"
)

// RunLease is a live run-lease token returned by AcquireRunLease.
// Call Renew on a timer while the app is running; call Release when the app
// stops.  Never mutate fields directly.
type RunLease struct {
	held    *Held
	mgr     *Manager
	Profile string
	AppID   string
}

// Fence returns the current fencing token.  Callers that gate I/O on the fence
// should store this value and call lease.ValidateFence before every write.
func (r *RunLease) Fence() int64 { return r.held.Fence }

// RunScope returns the lease scope for a (profile, appID) pair.
// Scope: run/<profile>/<appID>
func RunScope(profile, appID string) string {
	return "run/" + profile + "/" + appID
}

// AcquireRunLease tries to acquire the run-lease for (profile, appID).
//
//   - holderID should uniquely identify this node/process (e.g. nodeID-pid).
//   - ttl is the lease TTL; the caller must renew before it expires (15–30 s).
//
// Returns:
//
//	(*RunLease, nil)       — this instance acquired the lease; proceed with launch.
//	(nil, ErrNotHolder)   — another instance holds the lease; do NOT launch.
//	(nil, other error)    — unexpected error.
func AcquireRunLease(ctx context.Context, mgr *Manager, profile, appID, holderID string, ttl time.Duration) (*RunLease, error) {
	scope := RunScope(profile, appID)

	// Idempotent bootstrap: create the lease object if absent.
	if err := mgr.EnsureLease(ctx, scope); err != nil {
		return nil, fmt.Errorf("AcquireRunLease %s/%s: EnsureLease: %w", profile, appID, err)
	}

	// Append a random nonce so two nodes with the same holderID prefix can't
	// accidentally steal each other's lease.
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("AcquireRunLease %s/%s: rand: %w", profile, appID, err)
	}
	uniqueHolder := holderID + "-" + hex.EncodeToString(nonce[:])

	held, err := mgr.Acquire(ctx, scope, uniqueHolder, ttl)
	if err != nil {
		if errors.Is(err, ErrNotHolder) || errors.Is(err, ErrLost) {
			return nil, ErrNotHolder
		}
		return nil, fmt.Errorf("AcquireRunLease %s/%s: Acquire: %w", profile, appID, err)
	}

	return &RunLease{held: held, mgr: mgr, Profile: profile, AppID: appID}, nil
}

// Renew refreshes the TTL and bumps the fencing token.
// Returns ErrNotHolder if the lease has been taken over (stale fence).
// Callers should treat any error as a signal to stop the app.
func (r *RunLease) Renew(ctx context.Context, ttl time.Duration) error {
	if err := r.mgr.Renew(ctx, r.held, ttl); err != nil {
		return fmt.Errorf("RunLease.Renew %s/%s: %w", r.Profile, r.AppID, err)
	}
	return nil
}

// Release releases the lease (marks it free) so another instance can take over.
// Should be called when the app stops cleanly.  Errors are logged but not fatal.
func (r *RunLease) Release(ctx context.Context) {
	if err := r.mgr.Release(ctx, r.held); err != nil {
		if !errors.Is(err, ErrNotHolder) {
			log.Printf("[runlease] Release %s/%s: %v", r.Profile, r.AppID, err)
		}
		// ErrNotHolder means the lease already expired/was taken over — that's fine.
	}
}

// StartRenewLoop launches a background goroutine that renews the run-lease
// every renewEvery until stopCh is closed or the lease is lost.
//
// If the renew loop detects lease loss (ErrNotHolder / ErrLost / any error
// after exhausting retries), it calls onLost() exactly once and exits.  The
// caller's onLost implementation should stop the local app process.
//
// The goroutine exits when stopCh is closed (caller stopped the app normally).
// It does NOT close stopCh itself.
func (r *RunLease) StartRenewLoop(ctx context.Context, ttl, renewEvery time.Duration, stopCh <-chan struct{}, onLost func(err error)) {
	go func() {
		ticker := time.NewTicker(renewEvery)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				// App stopped normally — renew loop exits; Release is called by caller.
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := r.Renew(ctx, ttl); err != nil {
					// Lease lost or fence stale — signal the caller to stop the app.
					onLost(err)
					return
				}
			}
		}
	}()
}
