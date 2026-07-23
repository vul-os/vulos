// wire_operational_extra.go — the network/routing + storage operational route
// surface, wired into the zero-config self-host binary.
//
// Phase-3b finding: the minimal composition root (RegisterOperational) mounted
// auth + the console-operational groups, but NOT the device-enrollment flow, the
// OS routing plane (DNS/relay-status/edge/CDN/multi-location), the third-party
// OAuth data-broker, the mail key directory, the cloud-home directory, or the
// storage/files/export plane — even though every one of those has a real
// pkg/cproutes handler and a store-opening helper that needs no commercial seam.
// A self-hoster's box therefore could not enrol, get a routing binding, presign
// against a bring-your-own bucket, or broker a Google connection.
//
// These are all OPERATIONAL surfaces — a self-hoster's own fleet enrollment,
// routing, storage and integrations — so they belong in the MIT default. Every
// store here opens via cpdb (SQLite by default, Postgres when DATABASE_URL is
// set) and degrades to an in-memory store (or an honest 503) rather than
// fail-open. Nothing here opens a commercial store, charges money, or provisions
// a managed bucket: the only seams consulted are the entitlement resolver
// (no-op ⇒ unlimited) and the StorageProvisioner (no-op ⇒ bring-your-own-bucket).
//
// FAIL-CLOSED POSTURE. Every route group below is gated by the shared auth store
// (or a device/shared-secret gate). A group whose required secret is unset stays
// deny/unavailable, never fail-open:
//   - integrations: without INTEGRATIONS_KEK, Connect/Mint error (routes mounted,
//     the KEK-less broker refuses to custody tokens).
//   - cloud-home: without CLOUDHOME_KEK, WireCloudHome returns a nil service and
//     every route answers 503 (never custodies peering keys under a default key).
//   - storage managed provider: without S3 credentials the managed provider is
//     unconstructable, so a managed presign answers 503 (honest outage) — a
//     self-hoster connects a BYO bucket to get real object storage.
//   - the device-authenticated legs (integrations /token, storagesel sync-mode)
//     fail closed when CP_SHARED_SECRET / the ownership resolver cannot authorise.
package cproutes

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/vul-os/vulos-management/pkg/cdn"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/dnsplane"
	"github.com/vul-os/vulos-management/pkg/edge"
	"github.com/vul-os/vulos-management/pkg/enroll"
	"github.com/vul-os/vulos-management/pkg/fleet"
	"github.com/vul-os/vulos-management/pkg/keydir"
	"github.com/vul-os/vulos-management/pkg/multiloc"
	"github.com/vul-os/vulos-management/pkg/relayusage"
	"github.com/vul-os/vulos-management/pkg/routing"
	"github.com/vul-os/vulos-management/pkg/storage"
	"github.com/vul-os/vulos-management/pkg/storagesel"
)

// registerNetworkOperational mounts the device-enrollment + OS-routing + storage
// operational route groups and returns their store closers (run in reverse on
// shutdown). Called from RegisterOperational after the console groups. Never
// fails the caller: a store that cannot open logs and the group answers 503.
//
// fleetStore + routingStore are the SHARED stores opened once by
// RegisterOperational (see the comment there): enrollment writes a fleet device
// row + a routing binding that the console fleet/status views and the DNS
// plane / relay status / resolver all read back.
func registerNetworkOperational(mux *http.ServeMux, deps OperationalDeps, fleetStore fleet.Store, routingStore routing.Store) []func() {
	var closers []func()
	add := func(c func()) {
		if c != nil {
			closers = append(closers, c)
		}
	}

	// A ULID→account ownership resolver backed by the shared routing store. Used
	// by the device-authenticated legs (CDN admin/agent, integrations /token,
	// storage-selection sync-mode) to bind a fleet-global device secret to the
	// device's OWN account — a device can never act for another tenant.
	ownerResolver := &fleet.RoutingOwnershipResolver{Store: routingStore}

	// ── Device enrollment (RFC-8628 device-authorization grant) ───────────────
	// POST /enroll/{start,poll,approve,deny}: a bare-metal box starts a grant and
	// the logged-in owner approves it, minting a device cert + routing binding +
	// fleet row. approve/deny are session-gated; start/poll are per-IP-rate-limited
	// public (the box has no session yet). The CA signer is process-local in the
	// MIT default (a persistent management CA is a configured/commercial concern);
	// certs are still minted and bound, they just don't outlive a restart.
	enrollStore, enrollCloser := openEnrollStore(deps.DBDir)
	add(enrollCloser)
	RegisterBootEnrollRoutes(mux, enrollStore, enroll.NewMemCertSigner(), routingStore, deps.AuthStore, fleetStore)

	// Session-driven web-wizard enrollment (POST /api/enroll, /api/enroll/direct,
	// /api/connmode): the account is ALWAYS derived from the session, never the
	// body; /api/connmode additionally enforces ULID ownership.
	RegisterEnrollRoutes(mux, routingStore, deps.AuthStore)

	// ── OS routing plane: DNS, relay status, edge, CDN, multi-location ─────────

	// DNS plane (GET /api/dnsplane/zone/{ulid}, resync). The Provider is the
	// in-process MemProvider in the MIT default — a self-hoster runs their own
	// authoritative DNS, so the plane tracks desired records without driving an
	// external DNS vendor (the cloud root injects a Cloudflare provider). Owner
	// check: caller must own the ULID (or be the admin account).
	dnsStore, dnsCloser := openDNSPlaneStore(deps.DBDir)
	add(dnsCloser)
	dnsRec := &dnsplane.Reconciler{Store: dnsStore, Provider: dnsplane.NewMemProvider(), RoutingStore: routingStore}
	RegisterDNSPlane(mux, dnsStore, dnsRec, deps.AuthStore, deps.AdminAccountID)

	// Relay status (GET /api/relay/status) — session-gated per-account view of
	// PoPs + this month's relayed traffic. Distinct from the relay-scale demand
	// API wired in RegisterOperational.
	relayUsageStore, relayUsageCloser := openRelayUsageStore(deps.DBDir)
	add(relayUsageCloser)
	RegisterRelayStatus(mux, deps.AuthStore, routingStore, relayUsageStore)

	// Edge control plane (WEBAPP-04). Per-tenant domain config + bandwidth views
	// are session-gated and self-scoped; PoP node management is admin-gated; the
	// bandwidth ingest leg is shared-secret gated.
	edgeStore, edgeCloser := openEdgeStore(deps.DBDir)
	add(edgeCloser)
	RegisterEdge(mux, edgeStore, deps.AuthStore, fleetStore, deps.AdminAccountID)

	// BYO-CDN (GET/PUT/DELETE /api/cdn/config, firewall rules, admin refresh).
	// Config is session-gated + self-scoped; the Pro-tier gate runs through the
	// entitlement seam (no-op ⇒ allowed); the admin/agent legs verify the CP
	// shared secret (current OR previous).
	cdnStore, cdnCloser := openCDNStore(deps.DBDir)
	add(cdnCloser)
	RegisterCDN(mux, cdnStore, cdn.NewRangeFetcher(cdnStore), deps.AuthStore, deps.Entitlements, ownerResolver,
		func(ctx context.Context, presented string) bool { return validSharedSecretEither(ctx, presented) })

	// Multi-location management (POST /api/multiloc/enroll, GET locations, health
	// report) — org-admin gated via the shared fleet store's member roles.
	mlStore, mlCloser := openMultiLocStore(deps.DBDir)
	add(mlCloser)
	RegisterMultiLoc(mux, mlStore, deps.AuthStore, fleetStore)

	// ── Third-party OAuth data broker (Google / Microsoft / Dropbox) ──────────
	// Session-gated connect/list/status/disconnect; the device /token mint is
	// HMAC/cert gated. WireIntegrations fails loud-but-non-fatal without
	// INTEGRATIONS_KEK (Connect/Mint error; status still reports). The state
	// secret is the fleet-wide CP shared secret; caCertPubKey is nil in the MIT
	// default (owner-attested cert auth off ⇒ device register uses the HMAC
	// bootstrap only). ownerResolver binds the device /token leg to its own account.
	integ := WireIntegrations(deps.DBDir)
	add(integ.Closer)
	RegisterIntegrationsRoutes(mux, integ.Service, deps.AuthStore,
		[]byte(os.Getenv("CP_SHARED_SECRET")), ownerResolver, integ.DeviceKeys, nil)

	// ── Mail key directory (@vulos peering pubkeys) ───────────────────────────
	// PUT/GET /api/mail/keydir: a session account may publish a public key ONLY
	// for its own registered @vulos address (audit H4). The store is shared with
	// cloud-home below.
	keydirStore, keydirCloser := openKeyDirStore(deps.DBDir)
	add(keydirCloser)
	RegisterKeyDirRoutes(mux, keydirStore, deps.AuthStore)

	// ── Cloud-home directory + peering intake ─────────────────────────────────
	// GET /api/verify/lookup (enumeration-hardened public directory), the
	// capability/peering intake, and the identity lifecycle relay. FAIL-CLOSED:
	// WireCloudHome returns a nil service (⇒ 503 on every route) unless
	// CLOUDHOME_KEK is configured, so the cell never custodies peering keys under
	// a default key.
	ch := WireCloudHome(deps.DBDir, deps.AuthStore, keydirStore)
	add(ch.Closer)
	RegisterCloudHome(mux, ch.Svc, deps.AuthStore)

	// ── Storage / Files / account-export / storage-selection / mail resolver ───
	// The storage.Service is the bring-your-own-bucket data plane: its Store
	// persists per-account config + usage, its ProviderForAccount selects the
	// account's BYO S3 provider (or the operator-configured managed provider from
	// env), and its Provisioner is the no-op BYOB seam (never creates a bucket).
	storageSvc, storageCloser := openStorageService(deps.DBDir)
	add(storageCloser)
	if storageSvc != nil {
		// RegisterStorage ALSO mounts account-export (POST /api/account/export)
		// and the Files/Drive control plane (RegisterFiles + WireFiles). All
		// session-gated + self-only; write paths run the storage-quota gate
		// through the entitlement seam (no-op ⇒ unlimited).
		RegisterStorage(mux, storageSvc, deps.AuthStore, deps.Entitlements)

		// Storage-backend selection (GET/PUT /api/storage/backend, GET
		// /api/storage/sync-mode). The session legs are self-scoped; the box
		// sync-mode read is shared-secret + ownership gated (fail-closed).
		sel, selCloser := openStorageSelector(deps.DBDir)
		add(selCloser)
		if sel != nil {
			registerStorageSelRoutes(mux, sel, deps.AuthStore, ownerResolver)
		}

		// Mail-backend resolver + fabric proxy (GET /api/resolve/backend,
		// /backend/{service}/). Session-gated; resolves the caller's own backend
		// through the storage.Service. Self-host enrollment is de-scoped, so
		// accounts fall back to the hosted JMAP endpoint (see wireResolver).
		rr := wireResolver(storageSvc)
		add(rr.closer)
		registerResolverRoutes(mux, rr.res, deps.AuthStore)
	}

	return closers
}

// ── Store openers ─────────────────────────────────────────────────────────────
//
// Each mirrors the openFleetStore pattern in wire_console_ops.go: set
// VULOS_DB_DIR (so the SQLite file lands alongside the other stores), open
// through cpdb (Postgres when DATABASE_URL is set), and fall back to the
// package's in-memory store on any error so the route group degrades gracefully
// rather than crashing the process.

func openRoutingStore(dbDir string) (routing.Store, func()) {
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[routing] WARNING: could not set DB dir (%v) — using in-memory store", err)
		return routing.NewMemStore(), func() {}
	}
	db, err := cpdb.Open("routing")
	if err != nil {
		log.Printf("[routing] WARNING: could not open cpdb (%v) — using in-memory store", err)
		return routing.NewMemStore(), func() {}
	}
	st, err := routing.Open(db)
	if err != nil {
		_ = db.Close()
		log.Printf("[routing] WARNING: could not open store (%v) — using in-memory store", err)
		return routing.NewMemStore(), func() {}
	}
	log.Printf("[routing] store opened (backend=%s)", db.Backend())
	return st, func() {
		if cerr := st.Close(); cerr != nil {
			log.Printf("[routing] store close: %v", cerr)
		}
	}
}

func openEnrollStore(dbDir string) (enroll.Store, func()) {
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[enroll] WARNING: could not set DB dir (%v) — using in-memory store", err)
		return enroll.NewMemStore(), func() {}
	}
	db, err := cpdb.Open("enroll")
	if err != nil {
		log.Printf("[enroll] WARNING: could not open cpdb (%v) — using in-memory store", err)
		return enroll.NewMemStore(), func() {}
	}
	st, err := enroll.OpenSQLStore(db)
	if err != nil {
		_ = db.Close()
		log.Printf("[enroll] WARNING: could not open store (%v) — using in-memory store", err)
		return enroll.NewMemStore(), func() {}
	}
	log.Printf("[enroll] store opened (backend=%s)", db.Backend())
	return st, func() {
		if cerr := st.Close(); cerr != nil {
			log.Printf("[enroll] store close: %v", cerr)
		}
	}
}

func openDNSPlaneStore(dbDir string) (dnsplane.Store, func()) {
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[dnsplane] WARNING: could not set DB dir (%v) — using in-memory store", err)
		return dnsplane.NewMemDNSStore(), func() {}
	}
	db, err := cpdb.Open("dnsplane")
	if err != nil {
		log.Printf("[dnsplane] WARNING: could not open cpdb (%v) — using in-memory store", err)
		return dnsplane.NewMemDNSStore(), func() {}
	}
	st, err := dnsplane.Open(db)
	if err != nil {
		_ = db.Close()
		log.Printf("[dnsplane] WARNING: could not open store (%v) — using in-memory store", err)
		return dnsplane.NewMemDNSStore(), func() {}
	}
	log.Printf("[dnsplane] store opened (backend=%s)", db.Backend())
	return st, func() {
		if cerr := st.Close(); cerr != nil {
			log.Printf("[dnsplane] store close: %v", cerr)
		}
	}
}

func openRelayUsageStore(dbDir string) (relayusage.Store, func()) {
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[relayusage] WARNING: could not set DB dir (%v) — using in-memory store", err)
		return relayusage.NewMemStore(), func() {}
	}
	db, err := cpdb.Open("relayusage")
	if err != nil {
		log.Printf("[relayusage] WARNING: could not open cpdb (%v) — using in-memory store", err)
		return relayusage.NewMemStore(), func() {}
	}
	st, err := relayusage.OpenSQLStore(db)
	if err != nil {
		_ = db.Close()
		log.Printf("[relayusage] WARNING: could not open store (%v) — using in-memory store", err)
		return relayusage.NewMemStore(), func() {}
	}
	log.Printf("[relayusage] store opened (backend=%s)", db.Backend())
	return st, func() {
		if cerr := st.Close(); cerr != nil {
			log.Printf("[relayusage] store close: %v", cerr)
		}
	}
}

func openEdgeStore(dbDir string) (edge.Store, func()) {
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[edge] WARNING: could not set DB dir (%v) — using in-memory store", err)
		return edge.NewMemStore(), func() {}
	}
	db, err := cpdb.Open("edge")
	if err != nil {
		log.Printf("[edge] WARNING: could not open cpdb (%v) — using in-memory store", err)
		return edge.NewMemStore(), func() {}
	}
	st, err := edge.Open(db)
	if err != nil {
		_ = db.Close()
		log.Printf("[edge] WARNING: could not open store (%v) — using in-memory store", err)
		return edge.NewMemStore(), func() {}
	}
	log.Printf("[edge] store opened (backend=%s)", db.Backend())
	return st, func() {
		if cerr := st.Close(); cerr != nil {
			log.Printf("[edge] store close: %v", cerr)
		}
	}
}

func openCDNStore(dbDir string) (cdn.Store, func()) {
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[cdn] WARNING: could not set DB dir (%v) — using in-memory store", err)
		return cdn.NewMemStore(), func() {}
	}
	db, err := cpdb.Open("cdn")
	if err != nil {
		log.Printf("[cdn] WARNING: could not open cpdb (%v) — using in-memory store", err)
		return cdn.NewMemStore(), func() {}
	}
	st, err := cdn.Open(db)
	if err != nil {
		_ = db.Close()
		log.Printf("[cdn] WARNING: could not open store (%v) — using in-memory store", err)
		return cdn.NewMemStore(), func() {}
	}
	log.Printf("[cdn] store opened (backend=%s)", db.Backend())
	return st, func() {
		if cerr := st.Close(); cerr != nil {
			log.Printf("[cdn] store close: %v", cerr)
		}
	}
}

func openMultiLocStore(dbDir string) (multiloc.Store, func()) {
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[multiloc] WARNING: could not set DB dir (%v) — using in-memory store", err)
		return multiloc.NewMemStore(), func() {}
	}
	db, err := cpdb.Open("multiloc")
	if err != nil {
		log.Printf("[multiloc] WARNING: could not open cpdb (%v) — using in-memory store", err)
		return multiloc.NewMemStore(), func() {}
	}
	st, err := multiloc.OpenStore(db)
	if err != nil {
		_ = db.Close()
		log.Printf("[multiloc] WARNING: could not open store (%v) — using in-memory store", err)
		return multiloc.NewMemStore(), func() {}
	}
	log.Printf("[multiloc] store opened (backend=%s)", db.Backend())
	return st, func() {
		if cerr := st.Close(); cerr != nil {
			log.Printf("[multiloc] store close: %v", cerr)
		}
	}
}

func openKeyDirStore(dbDir string) (keydir.Store, func()) {
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[keydir] WARNING: could not set DB dir (%v) — using in-memory store", err)
		return keydir.NewMemStore(), func() {}
	}
	db, err := cpdb.Open("keydir")
	if err != nil {
		log.Printf("[keydir] WARNING: could not open cpdb (%v) — using in-memory store", err)
		return keydir.NewMemStore(), func() {}
	}
	st, err := keydir.OpenSQLStore(db)
	if err != nil {
		_ = db.Close()
		log.Printf("[keydir] WARNING: could not open store (%v) — using in-memory store", err)
		return keydir.NewMemStore(), func() {}
	}
	log.Printf("[keydir] store opened (backend=%s)", db.Backend())
	return st, func() {
		if cerr := st.Close(); cerr != nil {
			log.Printf("[keydir] store close: %v", cerr)
		}
	}
}

// openStorageSelector opens the storage-backend selection store. Unlike the
// others there is no in-memory fallback in the storagesel package, so a failure
// returns a nil Selector and the caller skips registering the routes (the
// storage-selection console tab is simply absent rather than 500-ing).
func openStorageSelector(dbDir string) (*storagesel.Selector, func()) {
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[storagesel] WARNING: could not set DB dir (%v) — storage-selection routes disabled", err)
		return nil, func() {}
	}
	db, err := cpdb.Open("storagesel")
	if err != nil {
		log.Printf("[storagesel] WARNING: could not open cpdb (%v) — storage-selection routes disabled", err)
		return nil, func() {}
	}
	sel, err := storagesel.Open(db)
	if err != nil {
		_ = db.Close()
		log.Printf("[storagesel] WARNING: could not open selector (%v) — storage-selection routes disabled", err)
		return nil, func() {}
	}
	log.Printf("[storagesel] selector opened (backend=%s)", db.Backend())
	return sel, func() { _ = db.Close() }
}

// openStorageService builds the bring-your-own-bucket storage data plane for the
// MIT self-host binary: a cpdb-backed Store, a ProviderForAccount selector, and
// the no-op (BYOB) Provisioner. Returns a nil Service (⇒ the storage/files/export
// routes are simply not mounted) if the store cannot be opened — there is NO
// in-memory fallback because a metadata index that forgets on restart while the
// bytes persist in a bucket is worse than an honest absence.
//
// KEK: STORAGE_KEK (32-byte hex) encrypts BYO secret keys at rest. Empty is
// tolerated (dev/self-host without BYO secret encryption); a malformed value
// logs and disables BYO secret encryption rather than crashing the process.
func openStorageService(dbDir string) (*storage.Service, func()) {
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[storage] WARNING: could not set DB dir (%v) — storage routes disabled", err)
		return nil, func() {}
	}
	kek, err := storage.KEKFromHex(os.Getenv("STORAGE_KEK"))
	if err != nil {
		log.Printf("[storage] WARNING: %v — BYO secret encryption disabled", err)
		kek = nil
	}
	db, err := cpdb.Open("storage")
	if err != nil {
		log.Printf("[storage] WARNING: could not open cpdb (%v) — storage/files/export routes disabled", err)
		return nil, func() {}
	}
	st, err := storage.Open(db, kek)
	if err != nil {
		_ = db.Close()
		log.Printf("[storage] WARNING: could not open store (%v) — storage/files/export routes disabled", err)
		return nil, func() {}
	}
	log.Printf("[storage] store opened (backend=%s)", db.Backend())
	svc := &storage.Service{
		Store:              st,
		ProviderForAccount: selfHostProviderForAccount(st),
		// Provisioner left nil ⇒ storage.Service.provisioner() resolves to the
		// bring-your-own-bucket NoopProvisioner: the operational control plane
		// never creates a bucket. The cloud composition root injects a real
		// (Tigris) provisioner here.
		Provisioner: nil,
		KEK:         kek,
	}
	return svc, func() {
		if cerr := st.Close(); cerr != nil {
			log.Printf("[storage] store close: %v", cerr)
		}
	}
}

// selfHostProviderForAccount is the data-plane provider selector for a self-host
// deployment: an account with a stored BYO config uses its OWN S3-compatible
// bucket (its credentials, confined to its own storage account); everything else
// is a "managed" account served by the operator-configured single object store
// read from env (S3_ENDPOINT / S3_ACCESS_KEY_ID / S3_SECRET_ACCESS_KEY /
// S3_REGION / S3_FORCE_PATH_STYLE). When those env vars are unset the managed
// provider is unconstructable and the presign answers 503 — the honest
// bring-your-own-bucket outage. This never signs with a cross-tenant credential:
// callerOwnsBucket (routes_storage.go) still pins a managed account to its own
// canonical "vulos-<ulid>" bucket.
func selfHostProviderForAccount(st storage.Store) func(ctx context.Context, accountID string) (storage.Provider, error) {
	return func(ctx context.Context, accountID string) (storage.Provider, error) {
		cfg, err := st.GetConfig(ctx, accountID)
		if err == nil && cfg.BYO {
			return storage.NewBYOProvider(cfg)
		}
		// Managed: build the operator-configured S3 provider from env. A trusted,
		// operator-supplied endpoint (no user input) ⇒ SSRFGuard off. Missing
		// credentials/endpoint ⇒ ErrProviderFailed ⇒ the handler maps it to 503.
		return storage.NewS3Provider(storage.S3Config{
			AccessKeyID:     os.Getenv("S3_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("S3_SECRET_ACCESS_KEY"),
			Endpoint:        os.Getenv("S3_ENDPOINT"),
			Region:          os.Getenv("S3_REGION"),
			ForcePathStyle:  os.Getenv("S3_FORCE_PATH_STYLE") == "1" || os.Getenv("S3_FORCE_PATH_STYLE") == "true",
		})
	}
}
