package multiinstance_test

// NODE-CAP-01 tests: store-only members — a cluster member that syncs but is
// never an ingress / route target. These lock in the two safety properties:
//   1. The zero value / default is SERVING (no behavior change on upgrade).
//   2. router.BuildTable and notify fan-out EXCLUDE store-only instances, while
//      persistence and the CP wire format carry the flag.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"vulos/backend/internal/multiinstance"
)

// A store-only flag round-trips through Upsert/Get/List, and — critically — the
// zero value persists as serving so nothing that omits the field is silently
// demoted to sync-only.
func TestStoreOnly_DefaultsToServingAndPersists(t *testing.T) {
	r := openTempRegistry(t)

	// (a) An instance created WITHOUT touching StoreOnly defaults to serving.
	const serving = "01HWZSTOR00000000000SERVE1"
	if err := r.Upsert(multiinstance.Instance{ULID: serving, Kind: multiinstance.KindDevice}); err != nil {
		t.Fatalf("Upsert serving: %v", err)
	}
	if got, _ := r.Get(serving); got.StoreOnly {
		t.Errorf("instance created without StoreOnly should default to serving (false), got true")
	}

	// (b) An instance created store-only reads back store-only.
	const store = "01HWZSTOR00000000000STORE1"
	if err := r.Upsert(multiinstance.Instance{ULID: store, Kind: multiinstance.KindDevice, StoreOnly: true}); err != nil {
		t.Fatalf("Upsert store-only: %v", err)
	}
	if got, _ := r.Get(store); !got.StoreOnly {
		t.Errorf("instance created with StoreOnly=true read back as serving")
	}

	// (c) Upsert toggles BOTH directions (ON CONFLICT must write the new value).
	if err := r.Upsert(multiinstance.Instance{ULID: store, Kind: multiinstance.KindDevice, StoreOnly: false}); err != nil {
		t.Fatalf("Upsert toggle back: %v", err)
	}
	if got, _ := r.Get(store); got.StoreOnly {
		t.Errorf("Upsert with StoreOnly=false did not clear the flag")
	}

	// (d) List carries the flag too.
	if err := r.Upsert(multiinstance.Instance{ULID: store, Kind: multiinstance.KindDevice, StoreOnly: true}); err != nil {
		t.Fatalf("Upsert re-set: %v", err)
	}
	list, err := r.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	seen := map[string]bool{}
	for _, inst := range list {
		seen[inst.ULID] = inst.StoreOnly
	}
	if seen[serving] {
		t.Errorf("List: serving instance reported StoreOnly=true")
	}
	if !seen[store] {
		t.Errorf("List: store-only instance reported StoreOnly=false")
	}
}

func TestSetStoreOnly_TogglesAndReturnsRow(t *testing.T) {
	r := openTempRegistry(t)
	const ulid = "01HWZSTOR0000000000000SET1"
	if err := r.Upsert(multiinstance.Instance{ULID: ulid, Kind: multiinstance.KindDevice}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := r.SetStoreOnly(ulid, true)
	if err != nil {
		t.Fatalf("SetStoreOnly true: %v", err)
	}
	if !got.StoreOnly {
		t.Errorf("SetStoreOnly returned row with StoreOnly=false")
	}
	if fresh, _ := r.Get(ulid); !fresh.StoreOnly {
		t.Errorf("SetStoreOnly true did not persist")
	}

	got, err = r.SetStoreOnly(ulid, false)
	if err != nil {
		t.Fatalf("SetStoreOnly false: %v", err)
	}
	if got.StoreOnly {
		t.Errorf("SetStoreOnly false returned row with StoreOnly=true")
	}

	if _, err := r.SetStoreOnly("01HWZSTOR00000000000NOPE01", true); !errors.Is(err, multiinstance.ErrNotFound) {
		t.Errorf("SetStoreOnly on unknown ULID: want ErrNotFound, got %v", err)
	}
}

// SetStoreOnly must not disturb the other columns (it is a targeted UPDATE, not
// a full Upsert that could reset display name / endpoint / status).
func TestSetStoreOnly_PreservesOtherFields(t *testing.T) {
	r := openTempRegistry(t)
	const ulid = "01HWZSTOR0000000000PRESRV1"
	seed := multiinstance.Instance{
		ULID:        ulid,
		DisplayName: "Studio Laptop",
		Kind:        multiinstance.KindDevice,
		EndpointURL: "https://laptop.relay.vulos.org",
		Role:        multiinstance.RolePeer,
		Status:      multiinstance.StatusOnline,
	}
	if err := r.Upsert(seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := r.SetStoreOnly(ulid, true); err != nil {
		t.Fatalf("SetStoreOnly: %v", err)
	}
	got, _ := r.Get(ulid)
	if got.DisplayName != "Studio Laptop" || got.EndpointURL != "https://laptop.relay.vulos.org" ||
		got.Role != multiinstance.RolePeer || got.Status != multiinstance.StatusOnline {
		t.Errorf("SetStoreOnly disturbed other fields: %+v", got)
	}
}

func TestBuildTable_ExcludesStoreOnly_Expand(t *testing.T) {
	r := openTempRegistry(t)
	const servingULID = "01HWZSTOR0000000000RT0SRV1"
	const storeULID = "01HWZSTOR0000000000RT0STO1"
	if err := r.Upsert(multiinstance.Instance{ULID: servingULID, Kind: multiinstance.KindDevice, EndpointURL: "https://srv.vulos.org"}); err != nil {
		t.Fatalf("Upsert serving: %v", err)
	}
	if err := r.Upsert(multiinstance.Instance{ULID: storeULID, Kind: multiinstance.KindDevice, EndpointURL: "https://sto.vulos.org", StoreOnly: true}); err != nil {
		t.Fatalf("Upsert store-only: %v", err)
	}

	ar := multiinstance.NewAppRouter(r, "vulos.org")
	table, err := ar.BuildTable([]multiinstance.PublishedApp{{App: "notes", Profile: "default"}})
	if err != nil {
		t.Fatalf("BuildTable: %v", err)
	}
	for _, e := range table {
		if e.InstanceULID == storeULID {
			t.Errorf("routing table advertised a store-only instance: %+v", e)
		}
	}
	// The serving instance must still be present.
	found := false
	for _, e := range table {
		if e.InstanceULID == servingULID {
			found = true
		}
	}
	if !found {
		t.Errorf("routing table dropped the serving instance")
	}
}

func TestBuildTable_ExcludesStoreOnly_Pinned(t *testing.T) {
	r := openTempRegistry(t)
	const storeULID = "01HWZSTOR0000000000PIN0STO1"
	if err := r.Upsert(multiinstance.Instance{ULID: storeULID, Kind: multiinstance.KindDevice, EndpointURL: "https://sto.vulos.org", StoreOnly: true}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	ar := multiinstance.NewAppRouter(r, "vulos.org")
	table, err := ar.BuildTable([]multiinstance.PublishedApp{{App: "notes", Profile: "default", InstanceULID: storeULID}})
	if err != nil {
		t.Fatalf("BuildTable: %v", err)
	}
	if len(table) != 0 {
		t.Errorf("app pinned to a store-only instance should yield no route, got %d entries: %+v", len(table), table)
	}
}

// Fan-out must skip store-only members even when they are online with an
// endpoint — the endpoint-present-implies-routable assumption is exactly what
// NODE-CAP-01 breaks.
func TestFanOut_ExcludesStoreOnly(t *testing.T) {
	reg := openTempRegistry(t)
	if err := reg.Upsert(multiinstance.Instance{
		ULID: "01HWZSTOR000000000FANSERVE1", Kind: multiinstance.KindDevice,
		Status: multiinstance.StatusOnline, EndpointURL: "https://serving.relay.vulos.org",
	}); err != nil {
		t.Fatalf("Upsert serving: %v", err)
	}
	if err := reg.Upsert(multiinstance.Instance{
		ULID: "01HWZSTOR000000000FANSTORE1", Kind: multiinstance.KindDevice,
		Status: multiinstance.StatusOnline, EndpointURL: "https://storeonly.relay.vulos.org", StoreOnly: true,
	}); err != nil {
		t.Fatalf("Upsert store-only: %v", err)
	}

	var mu sync.Mutex
	var targets []string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env struct {
			Target string `json:"target"`
		}
		_ = json.NewDecoder(r.Body).Decode(&env)
		mu.Lock()
		targets = append(targets, env.Target)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()
	t.Setenv("VULOS_RELAY_BASE_URL", relay.URL)
	t.Setenv("VULOS_RELAY_NAME", "sender-box")
	t.Setenv("VULOS_RELAY_TOKEN", "test-relay-token")

	nf := multiinstance.NewNotifyFanout(reg)
	if err := nf.Notify(context.Background(), multiinstance.Notification{ID: "notif-store-only-001", Title: "Test", Priority: 0}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, tgt := range targets {
		if strings.Contains(tgt, "storeonly") {
			t.Errorf("fan-out delivered to a store-only member (target=%q)", tgt)
		}
	}
	sawServing := false
	for _, tgt := range targets {
		if strings.Contains(tgt, "serving") {
			sawServing = true
		}
	}
	if !sawServing {
		t.Errorf("fan-out skipped the serving member; targets=%v", targets)
	}
}

// The CP wire format carries store_only both ways, and omits it (meaning
// SERVING) when false — the safe default a CP that predates the field produces.
func TestCloudInstance_WireCarriesStoreOnly(t *testing.T) {
	// (a) Marshal: true is present, false is omitted (omitempty == serving default).
	on, _ := json.Marshal(multiinstance.CloudInstance{ULID: "x", StoreOnly: true})
	if !strings.Contains(string(on), `"store_only":true`) {
		t.Errorf("StoreOnly=true not serialized: %s", on)
	}
	off, _ := json.Marshal(multiinstance.CloudInstance{ULID: "x", StoreOnly: false})
	if strings.Contains(string(off), "store_only") {
		t.Errorf("StoreOnly=false should be omitted (omitempty), got: %s", off)
	}

	// (b) An upsert presence event carrying store_only lands in the registry.
	reg := openTempRegistry(t)
	cs := multiinstance.NewCloudSyncer(reg, "")
	cs.ApplyPresenceEvent(multiinstance.PresenceEvent{
		Type:     "upsert",
		Instance: &multiinstance.CloudInstance{ULID: "01HWZSTOR0000000000WIRESTO1", Kind: "device", StoreOnly: true},
	})
	got, ok := reg.Get("01HWZSTOR0000000000WIRESTO1")
	if !ok {
		t.Fatalf("instance not upserted from presence event")
	}
	if !got.StoreOnly {
		t.Errorf("store_only from the CP wire did not reach the registry")
	}
}

// A full-list Sync() (not just a presence event) carries store_only for a peer.
func TestSync_CarriesStoreOnlyForPeer(t *testing.T) {
	reg := openTempRegistry(t)
	wire := []interface{}{
		map[string]interface{}{
			"ulid": "01HWZSTOR0000000SYNCPEER01", "kind": "device",
			"role": "peer", "status": "offline", "store_only": true,
		},
	}
	url, cleanup := startCloudStub(t, wire)
	defer cleanup()
	t.Setenv("VULOS_CLOUD_URL", url)

	cs := multiinstance.NewCloudSyncer(reg, "test-token")
	if err := cs.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got, _ := reg.Get("01HWZSTOR0000000SYNCPEER01"); !got.StoreOnly {
		t.Errorf("Sync did not carry store_only for a peer")
	}
}

// REGRESSION (owner self-clobber): the box owns the truth for its OWN serving
// posture. A CP that does not yet carry store_only returns the owner row as
// serving; Sync must NOT overwrite the locally-set owner store-only. Peers,
// however, take the CP value.
func TestSync_PreservesOwnerStoreOnly(t *testing.T) {
	reg := openTempRegistry(t)
	const ownerU = "01HWZSTOR0000000SYNCOWNER1"
	const peerU = "01HWZSTOR0000000SYNCPEER99"

	// The operator has marked THIS box (owner) store-only locally.
	if err := reg.Upsert(multiinstance.Instance{
		ULID: ownerU, Kind: multiinstance.KindDevice, Role: multiinstance.RoleOwner,
		Status: multiinstance.StatusOnline, StoreOnly: true,
	}); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	// A peer that the operator locally believes is serving.
	if err := reg.Upsert(multiinstance.Instance{
		ULID: peerU, Kind: multiinstance.KindDevice, Role: multiinstance.RolePeer,
		Status: multiinstance.StatusOnline, StoreOnly: false,
	}); err != nil {
		t.Fatalf("seed peer: %v", err)
	}

	// The CP (predating the field) returns the owner as serving (store_only
	// omitted) and asserts the peer is store-only.
	wire := []interface{}{
		map[string]interface{}{
			"ulid": ownerU, "kind": "device", "role": "owner", "status": "online",
			// store_only intentionally omitted → would decode to false
		},
		map[string]interface{}{
			"ulid": peerU, "kind": "device", "role": "peer", "status": "online",
			"store_only": true,
		},
	}
	url, cleanup := startCloudStub(t, wire)
	defer cleanup()
	t.Setenv("VULOS_CLOUD_URL", url)

	cs := multiinstance.NewCloudSyncer(reg, "test-token")
	if err := cs.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if owner, _ := reg.Get(ownerU); !owner.StoreOnly {
		t.Errorf("owner store-only was clobbered by CP sync (the box must own its own serving posture)")
	}
	if peer, _ := reg.Get(peerU); !peer.StoreOnly {
		t.Errorf("peer store-only from the CP was not applied (peers are CP-authoritative)")
	}
}

// The presence "upsert" path shares the same owner-preserve as Sync (both go
// through Registry.Upsert's SQL). A presence event must not clobber the owner.
func TestPresenceUpsert_PreservesOwnerStoreOnly(t *testing.T) {
	reg := openTempRegistry(t)
	const ownerU = "01HWZSTOR000000PRESOWNER01"
	if err := reg.Upsert(multiinstance.Instance{
		ULID: ownerU, Kind: multiinstance.KindDevice, Role: multiinstance.RoleOwner,
		Status: multiinstance.StatusOnline, StoreOnly: true,
	}); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	cs := multiinstance.NewCloudSyncer(reg, "")
	// The CP sends the owner as serving (store_only omitted).
	cs.ApplyPresenceEvent(multiinstance.PresenceEvent{
		Type:     "upsert",
		Instance: &multiinstance.CloudInstance{ULID: ownerU, Kind: "device", Role: "owner", Status: "online"},
	})
	if got, _ := reg.Get(ownerU); !got.StoreOnly {
		t.Errorf("presence upsert clobbered the owner's store-only")
	}
}

// A non-owner Upsert that omits StoreOnly must NOT preserve — only the owner row
// is box-authoritative. This pins that the SQL CASE is scoped to role='owner'.
func TestUpsert_PeerStoreOnlyIsNotPreserved(t *testing.T) {
	reg := openTempRegistry(t)
	const peerU = "01HWZSTOR0000000PEERNOPRSV1"
	if err := reg.Upsert(multiinstance.Instance{
		ULID: peerU, Kind: multiinstance.KindDevice, Role: multiinstance.RolePeer, StoreOnly: true,
	}); err != nil {
		t.Fatalf("seed peer: %v", err)
	}
	// A later CP-driven upsert reporting the peer as serving must win (peers are
	// CP-authoritative).
	if err := reg.Upsert(multiinstance.Instance{
		ULID: peerU, Kind: multiinstance.KindDevice, Role: multiinstance.RolePeer, StoreOnly: false,
	}); err != nil {
		t.Fatalf("re-upsert peer: %v", err)
	}
	if got, _ := reg.Get(peerU); got.StoreOnly {
		t.Errorf("peer store_only was preserved, but only the owner row should be box-authoritative")
	}
}

// The box seeds its own store-only from VULOS_STORE_ONLY on FIRST registration,
// and a later identity refresh must NOT re-read the env and stomp an operator's
// since-changed Settings choice.
func TestSelfRegister_SeedsStoreOnlyFromEnvOnce(t *testing.T) {
	const selfULID = "01HWZSTOR0000000SELFSEED001"
	t.Setenv("VULOS_STORE_ONLY", "1")

	reg, as := openTempAppSync(t)
	if _, err := as.GenerateAndSetIdentity(selfULID); err != nil {
		t.Fatalf("GenerateAndSetIdentity: %v", err)
	}
	if got, ok := reg.Get(selfULID); !ok || !got.StoreOnly {
		t.Fatalf("first self-registration did not seed StoreOnly from VULOS_STORE_ONLY: %+v", got)
	}

	// Operator turns it OFF via the Settings toggle.
	if _, err := reg.SetStoreOnly(selfULID, false); err != nil {
		t.Fatalf("SetStoreOnly: %v", err)
	}
	// A later identity refresh (restart / key rotation path) must preserve the
	// operator's choice, NOT re-seed from the still-set env var.
	if _, err := as.GenerateAndSetIdentity(selfULID); err != nil {
		t.Fatalf("second GenerateAndSetIdentity: %v", err)
	}
	if got, _ := reg.Get(selfULID); got.StoreOnly {
		t.Errorf("a later self-registration re-seeded StoreOnly from env and clobbered the operator's Settings choice")
	}
}
