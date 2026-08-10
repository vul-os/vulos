package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"time"

	"vulos/backend/internal/crdtsync"
	"vulos/backend/internal/fabric"
	"vulos/backend/internal/sqlcrdt"
)

// startCRDTSync wires the general CRDT sync engine onto the LAN fabric.
//
// It is deliberately one function so the whole path is readable in one place:
// open the replica with the approved domains, mount the exchange endpoints on
// the LAN-only mux behind the same shared secret fabric uses, bridge each
// approved SQL table, and start both loops.
//
// Everything fails CLOSED. No secret, no discoverer, or a bridge that cannot
// open its table means that part of sync does not run — it never means running
// it unauthenticated or replicating something that was not approved.
//
// Returns the store so the caller can close it; a nil store means sync is off.
func startCRDTSync(
	ctx context.Context,
	lanMux *http.ServeMux,
	dbDir string,
	instanceID string,
	fabricSecret string,
	disc fabric.Discoverer,
	wanClient *fabric.WANClient,
	selfBaseURLs []string,
) (*crdtsync.Store, error) {
	if fabricSecret == "" {
		return nil, fmt.Errorf("no fabric secret: an unauthenticated CRDT exchange endpoint is never acceptable")
	}
	if disc == nil {
		return nil, fmt.Errorf("no peer discoverer")
	}
	domains := crdtsync.SyncableDomains()
	if len(domains) == 0 {
		return nil, fmt.Errorf("no domain is approved for replication")
	}

	store, err := crdtsync.Open(filepath.Join(dbDir, "crdtsync.db"), instanceID, domains)
	if err != nil {
		return nil, fmt.Errorf("open replica: %w", err)
	}

	// The exchange endpoints ride the LAN-only mux, gated by the same
	// constant-time shared-secret check as fabric's own routes.
	store.RegisterHandlers(lanMux, crdtsync.SecretAuthorizer(fabricSecret))

	// The discoverer is adapted rather than imported into the engine: a WAN
	// rendezvous discoverer satisfies the same fabric.Discoverer interface and
	// arrives here with no change to crdtsync at all.
	peers := crdtsync.PeerSourceFunc(func(ctx context.Context) ([]crdtsync.SyncPeer, error) {
		fp, err := disc.Peers(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]crdtsync.SyncPeer, 0, len(fp))
		for _, p := range fp {
			out = append(out, crdtsync.SyncPeer{InstanceID: p.InstanceID, BaseURL: p.BaseURL, WAN: p.WAN})
		}
		return out, nil
	})

	syncerCfg := crdtsync.SyncerConfig{
		Store:        store,
		Peers:        peers,
		Domains:      domains,
		Secret:       fabricSecret,
		HTTPClient:   fabric.NewLANClient(10 * time.Second),
		SelfBaseURLs: selfBaseURLs,
	}
	// A nil *fabric.WANClient must not become a non-nil Doer interface holding a
	// nil pointer, or the fail-closed WAN check would be bypassed by a value
	// that is "not nil" but unusable.
	if wanClient != nil {
		syncerCfg.WANHTTPClient = wanClient
	}

	syncer, err := crdtsync.NewSyncer(syncerCfg)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("syncer: %w", err)
	}

	// A local write nudges the network round rather than waiting for the tick.
	store.SetOnLocalChange(syncer.Nudge)

	bridged := 0
	for _, rt := range sqlcrdt.ReplicatedTables() {
		livePath := rt.LiveDBPath(dbDir)
		br, berr := sqlcrdt.New(sqlcrdt.Config{
			LivePath: livePath,
			Tables:   []sqlcrdt.TableSpec{rt.Spec},
			CRDT:     store,
		})
		if berr != nil {
			// One table failing to bridge (not created yet, schema drift) must
			// not take the whole engine down with it.
			log.Printf("[crdtsync] %s not bridged: %v", rt.Domain, berr)
			continue
		}
		go br.Run(ctx, sqlcrdt.DefaultCycleInterval, syncer.Nudge)
		go func(b *sqlcrdt.Bridge) {
			<-ctx.Done()
			_ = b.Close()
		}(br)
		bridged++
		log.Printf("[crdtsync] bridging %s (%s)", rt.Domain, livePath)
	}
	if bridged == 0 {
		store.Close()
		return nil, fmt.Errorf("no table could be bridged")
	}

	go syncer.Run(ctx)
	log.Printf("[crdtsync] active (instance=%s, domains=%v, /api/crdt/{pull,push,status} on the LAN listener)", instanceID, domains)
	return store, nil
}
