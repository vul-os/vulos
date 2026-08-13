package main

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"time"

	"vulos/backend/internal/crdtsync"
	"vulos/backend/internal/fabric"
	"vulos/backend/internal/multiinstance"
	"vulos/backend/internal/sqlcrdt"
)

// fabricPeerRoster answers "may this key exchange CRDT state with us" from the
// multi-instance roster — the SAME roster that already decides whose signed
// uninstall observations count toward quorum (CRDT-QUORUM-01) and whose key the
// rendezvous relay is asked to resolve.
//
// This is the entire authorisation boundary for WAN sync, so it is worth being
// explicit about what it means:
//
//   - Who authorises a new WAN peer: the OPERATOR, by enrolling that instance
//     in this box's roster. That is the same act that already makes a box a
//     fleet member. There is no trust-on-first-use, no "seen at the relay
//     therefore trusted", and no vouch chain: a peer cannot introduce a peer.
//
//   - Why the roster cannot be captured by the thing it authorises: no
//     peer-facing handler writes it. The roster is written by local
//     provisioning (multiinstance/provision.go), by local key rotation
//     (rotation.go), by this box publishing its OWN key (appsync SetIdentity),
//     and by control-plane sync (cloudsync.go, the operator's own account). The
//     fabric changeset path applies app_registry rows only — it never inserts
//     an instance or a public key — and the instance roster is not a
//     crdtsync-replicated domain (crdtsync/policy.go refuses app_registry
//     outright). So a peer that gains sync access cannot use it to grant sync
//     access to anyone else.
//
//   - Revocation and rotation come free, because they are already modelled
//     here: a revoked instance is refused outright, and a key inside its
//     rotation-overlap window is still accepted until PrevKeyExpiresAt, exactly
//     as multiinstance's own verifyChangesetSignature treats them.
//
// It re-reads the roster on every call rather than caching, so revoking a peer
// takes effect on the next request rather than the next restart.
type fabricPeerRoster struct{ reg *multiinstance.Registry }

// TrustedPeer implements crdtsync.PeerRoster. It fails CLOSED on every error
// path: an unreadable roster trusts nobody.
func (r fabricPeerRoster) TrustedPeer(pub ed25519.PublicKey) bool {
	if r.reg == nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	insts, err := r.reg.List()
	if err != nil {
		return false
	}
	now := time.Now().UTC()
	for _, in := range insts {
		// FABRIC-KEY-01: a revoked instance never counts, under either key.
		// Checked before any comparison so no branch below can resurrect it.
		if in.Revoked {
			continue
		}
		if keyMatches(in.Ed25519PublicKey, pub) {
			return true
		}
		// Rotation overlap: the previous key stays valid only until its
		// window closes. A zero expiry means no overlap is active, not an
		// unbounded one.
		if in.PrevEd25519PublicKey != "" && !in.PrevKeyExpiresAt.IsZero() &&
			now.Before(in.PrevKeyExpiresAt.UTC()) && keyMatches(in.PrevEd25519PublicKey, pub) {
			return true
		}
	}
	return false
}

// keyMatches compares a rostered key string against raw key bytes. The roster
// stores base64-STANDARD while the rendezvous layer uses base64url-raw;
// crdtsync.DecodePeerKey accepts both and the comparison is on the decoded
// bytes, so one key in two encodings is one identity, not two.
func keyMatches(rosterKey string, pub ed25519.PublicKey) bool {
	k, err := crdtsync.DecodePeerKey(rosterKey)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(k, pub) == 1
}

// startCRDTSync wires the general CRDT sync engine onto the LAN fabric.
//
// It is deliberately one function so the whole path is readable in one place:
// open the replica with the approved domains, mount the exchange endpoints on
// the LAN-only mux behind the shared secret fabric uses AND (when a signing key
// and a roster are available) per-peer Ed25519 authentication for WAN peers,
// bridge each approved SQL table, and start both loops.
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
	signer ed25519.PrivateKey,
	roster crdtsync.PeerRoster,
	// onApplied is called with a replicated table's name after rows are written
	// into the live database on a peer's behalf, so a service holding its own
	// in-memory view of that table can reload it. May be nil.
	onApplied func(table string),
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

	// ── peer identity ────────────────────────────────────────────────────────
	//
	// Both halves must be present or WAN sync does not run. Each is separately
	// fail-closed, and neither degrades to the shared secret:
	//
	//	signer — this box's Ed25519 fabric key, so it can sign requests peers
	//	         will accept and sign the responses it serves.
	//	roster — who is allowed. Without it a signature would prove who a peer
	//	         is and nothing about whether it may write here.
	//
	// With neither, this is exactly the LAN-only wiring that shipped before:
	// the secret authorizer, no WAN identity, WAN peers skipped.
	var (
		identity *crdtsync.PeerIdentity
		verifier *crdtsync.PeerVerifier
	)
	if len(signer) == ed25519.PrivateKeySize && roster != nil {
		id, ierr := crdtsync.NewPeerIdentity(signer)
		if ierr != nil {
			store.Close()
			return nil, fmt.Errorf("peer identity: %w", ierr)
		}
		v, verr := crdtsync.NewPeerVerifier(id.PublicKey(), roster)
		if verr != nil {
			store.Close()
			return nil, fmt.Errorf("peer verifier: %w", verr)
		}
		identity, verifier = id, v
		log.Printf("[crdtsync] WAN peer identity active (key=%s; peers authorised from the instance roster, deny-by-default)", id.ID())
	} else {
		log.Printf("[crdtsync] WAN peer identity NOT configured — WAN peers will be skipped, never dialled with the shared LAN secret")
	}

	// The exchange endpoints ride the LAN-only mux. Two independent schemes are
	// accepted, and a caller must satisfy one of them WHOLE:
	//
	//	the shared fabric secret  — the LAN path, unchanged, constant-time.
	//	a per-peer Ed25519 signature from a rostered peer, addressed to this
	//	box — the WAN path.
	//
	// AnyOf is not a weakening: the signature scheme is strictly stronger than
	// the secret (it names one peer instead of the fleet, cannot be replayed at
	// another box, and is not a bearer credential), so accepting it alongside
	// does not lower the bar for anyone who could not already clear it.
	authz := crdtsync.SecretAuthorizer(fabricSecret)
	if verifier != nil {
		authz = crdtsync.AnyOfAuthorizer(authz, crdtsync.PeerKeyAuthorizer(verifier))
	}
	store.RegisterHandlers(lanMux, authz, crdtsync.WithResponseSigning(identity))

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
			out = append(out, crdtsync.SyncPeer{
				InstanceID: p.InstanceID,
				BaseURL:    p.BaseURL,
				WAN:        p.WAN,
				// Carried through from rendezvous: the key we asked the relay
				// to resolve. Empty for mDNS peers, which is why a LAN peer
				// never accidentally takes the WAN path.
				PublicKey: p.PublicKey,
			})
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
		Identity:     identity,
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

	// The loop's own health, on the same mux and behind the same authorizer as
	// the engine endpoints. Without this an operator can see WHAT this box
	// holds and never whether it is talking to anyone.
	crdtsync.RegisterSyncStatusHandler(lanMux, authz, syncer)

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
		if onApplied != nil {
			table := rt.Spec.Name
			br.SetOnApplied(func(int) { onApplied(table) })
		}
		// Run and Close in ONE goroutine, in that order. They used to be two
		// goroutines both waiting on ctx.Done(), which left their order to the
		// scheduler: when Close won, it freed the session out from under a
		// loop that was still ticking and the next cycle panicked the process
		// during shutdown. Run returns as soon as ctx is done, so sequencing
		// them costs nothing and makes teardown deterministic.
		go func(b *sqlcrdt.Bridge) {
			b.Run(ctx, sqlcrdt.DefaultCycleInterval, syncer.Nudge)
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
	wanState := "WAN peers skipped (no peer identity)"
	if identity != nil {
		wanState = "WAN peers require a rostered peer signature"
	}
	log.Printf("[crdtsync] active (instance=%s, domains=%v, /api/crdt/{pull,push,status} on the LAN listener; %s)", instanceID, domains, wanState)
	return store, nil
}
