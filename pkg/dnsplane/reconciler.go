// reconciler.go — dnsplane Reconciler: drives desired→applied for zone records.
//
// Reconciler walks the dnsplane Store and routing.Store, computes the desired
// record set, and pushes changes through the Provider interface.  It is the
// bridge between the routing subsystem's PoP/Binding data and the external
// DNS host.
//
// Design decisions:
//   - Reconciler holds Provider + Store + routing.Store by interface — fully
//     testable with MemProvider + MemDNSStore + routing.MemStore.
//   - ReconcileULID: for each enrolled ULID, ensure a wildcard A record
//     *.{ulid}.vulos.org exists pointing at the PoP pool address (fabric) or
//     the direct IP (direct mode). Upserts the record then applies it.
//   - ReconcilePool: reads all healthy PoPs from the routing store and calls
//     Provider.PoolUpsert.
//   - Run: background goroutine that ticks on interval.
package dnsplane

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/routing"
)

// StatusSnapshot is the shape returned by GET /api/dnsplane/status.
type StatusSnapshot struct {
	Provider        string     `json:"provider"`
	LastReconcileAt *time.Time `json:"last_reconcile_at"`
	DesiredCount    int        `json:"desired"`
	AppliedCount    int        `json:"applied"`
	FailedCount     int        `json:"failed"`
}

// Reconciler drives the desired→applied loop.
type Reconciler struct {
	Store        Store
	Provider     Provider
	RoutingStore routing.Store

	mu              sync.Mutex
	lastReconcileAt *time.Time
}

// ReconcileULID ensures the wildcard A record for ulid is desired and applies
// it through the Provider. It reads the binding from RoutingStore to determine
// the target (direct IP or PoP pool).
func (rec *Reconciler) ReconcileULID(ctx context.Context, ulid string) error {
	b, err := rec.RoutingStore.GetBinding(ctx, ulid)
	if err != nil {
		return fmt.Errorf("dnsplane: reconcile ULID %s: get binding: %w", ulid, err)
	}

	fqdn := routing.ZoneApex(ulid) // *.{ulid}.vulos.org

	var target string
	switch b.Mode {
	case routing.ModeDirect:
		target = b.DirectIP
		if target == "" {
			return fmt.Errorf("dnsplane: reconcile ULID %s: direct mode but no IP set", ulid)
		}
	default: // fabric / own-domain → point at pool
		// For fabric mode we resolve the nearest PoP at reconcile time using
		// NearestPoP (lat=0,lon=0 gives any healthy PoP — acceptable for DNS
		// pool management where we write one authoritative value).
		pops, pErr := rec.RoutingStore.HealthyPoPs(ctx)
		if pErr != nil {
			return fmt.Errorf("dnsplane: reconcile ULID %s: healthy pops: %w", ulid, pErr)
		}
		pop, pErr := routing.NearestPoP(pops, 0, 0)
		if pErr != nil {
			return fmt.Errorf("dnsplane: reconcile ULID %s: %w", ulid, pErr)
		}
		target = pop.IP
	}

	// Upsert into local store.
	r, err := rec.Store.Upsert(ctx, Record{
		FQDN:  fqdn,
		Type:  "A",
		Value: target,
		TTL:   60,
		ULID:  ulid,
	})
	if err != nil {
		return fmt.Errorf("dnsplane: reconcile ULID %s: upsert: %w", ulid, err)
	}

	// Apply via provider.
	if r.ProviderID == "" {
		pid, pErr := rec.Provider.CreateRecord(ctx, r)
		if pErr != nil {
			_ = rec.Store.MarkFailed(ctx, r.ID, pErr.Error())
			return fmt.Errorf("dnsplane: reconcile ULID %s: create record: %w", ulid, pErr)
		}
		return rec.Store.MarkApplied(ctx, r.ID, pid)
	}
	// Record already has a provider ID — update it.
	r.Value = target
	if pErr := rec.Provider.UpdateRecord(ctx, r); pErr != nil {
		_ = rec.Store.MarkFailed(ctx, r.ID, pErr.Error())
		return fmt.Errorf("dnsplane: reconcile ULID %s: update record: %w", ulid, pErr)
	}
	return rec.Store.MarkApplied(ctx, r.ID, r.ProviderID)
}

// ReconcilePool pushes the current healthy PoP set to the Provider's pool.
func (rec *Reconciler) ReconcilePool(ctx context.Context) error {
	pops, err := rec.RoutingStore.HealthyPoPs(ctx)
	if err != nil {
		return fmt.Errorf("dnsplane: reconcile pool: %w", err)
	}
	members := make([]PoolMember, 0, len(pops))
	for _, p := range pops {
		members = append(members, PoolMember{
			PoPID:   p.ID,
			Region:  p.Region,
			IP:      p.IP,
			Lat:     p.Lat,
			Lon:     p.Lon,
			Healthy: p.Healthy,
		})
	}
	return rec.Provider.PoolUpsert(ctx, "", members)
}

// reconcileDesired processes all "desired" records in the store up to 100
// records per tick. This is the background sweep that catches any records
// that were upserted but not immediately applied (e.g. from a previous failed
// reconcile attempt).
func (rec *Reconciler) reconcileDesired(ctx context.Context) {
	records, err := rec.Store.DesiredUnapplied(ctx, 100)
	if err != nil {
		log.Printf("[dnsplane] desired unapplied query: %v", err)
		return
	}
	for _, r := range records {
		var applyErr error
		if r.ProviderID == "" {
			pid, cErr := rec.Provider.CreateRecord(ctx, r)
			if cErr != nil {
				applyErr = cErr
			} else {
				applyErr = rec.Store.MarkApplied(ctx, r.ID, pid)
			}
		} else {
			if uErr := rec.Provider.UpdateRecord(ctx, r); uErr != nil {
				applyErr = uErr
			} else {
				applyErr = rec.Store.MarkApplied(ctx, r.ID, r.ProviderID)
			}
		}
		if applyErr != nil {
			_ = rec.Store.MarkFailed(ctx, r.ID, applyErr.Error())
			log.Printf("[dnsplane] failed to apply record %d (%s): %v", r.ID, r.FQDN, applyErr)
		}
	}
}

// Run starts the background reconcile loop. It blocks until ctx is cancelled.
// Call this in a goroutine.
func (rec *Reconciler) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Printf("[dnsplane] reconciler started (interval=%s, provider=%s)", interval, rec.Provider.Name())
	for {
		select {
		case <-ctx.Done():
			log.Println("[dnsplane] reconciler stopping")
			return
		case <-ticker.C:
			now := time.Now().UTC()
			rec.mu.Lock()
			rec.lastReconcileAt = &now
			rec.mu.Unlock()

			rec.reconcileDesired(ctx)
			if err := rec.ReconcilePool(ctx); err != nil {
				log.Printf("[dnsplane] pool reconcile: %v", err)
			}
		}
	}
}

// Status returns a snapshot for GET /api/dnsplane/status.
func (rec *Reconciler) Status(ctx context.Context) (StatusSnapshot, error) {
	rec.mu.Lock()
	lastAt := rec.lastReconcileAt
	rec.mu.Unlock()

	desired, err := rec.Store.DesiredUnapplied(ctx, 0)
	if err != nil {
		return StatusSnapshot{}, err
	}
	applied, err := rec.Store.CountApplied(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	failed, err := rec.Store.CountFailed(ctx)
	if err != nil {
		return StatusSnapshot{}, err
	}
	return StatusSnapshot{
		Provider:        rec.Provider.Name(),
		LastReconcileAt: lastAt,
		DesiredCount:    len(desired),
		AppliedCount:    applied,
		FailedCount:     failed,
	}, nil
}
