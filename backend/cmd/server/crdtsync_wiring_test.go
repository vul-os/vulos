package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"vulos/backend/internal/crdtsync"
	"vulos/backend/internal/fabric"
	"vulos/backend/internal/multiinstance"
	"vulos/backend/internal/sqlcrdt"
)

// These tests exist because of a specific precedent in this repo: a sync hot
// path once shipped with passing tests, zero callers and its route registered
// on no mux. Green tests over a transport nothing calls prove nothing, so the
// wiring itself is what is asserted here.

const wiringSecret = "wiring-test-secret"

func newWiringDBDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Create the real reminders table so the bridge has something to bind.
	db, err := sql.Open("sqlite", filepath.Join(dir, "reminders.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS reminders (
		id TEXT PRIMARY KEY, user_id TEXT NOT NULL, text TEXT NOT NULL,
		remind_at INTEGER NOT NULL, created_at INTEGER NOT NULL,
		done INTEGER NOT NULL DEFAULT 0);`); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestStartCRDTSyncRegistersWorkingRoutes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux := http.NewServeMux()
	store, err := startCRDTSync(ctx, mux, newWiringDBDir(t), "INSTANCE-A", wiringSecret,
		fabric.NewStaticDiscoverer(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("startCRDTSync: %v", err)
	}
	defer store.Close()

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Every endpoint must exist, and must refuse an unauthenticated caller.
	for _, ep := range []struct{ method, path string }{
		{http.MethodPost, "/api/crdt/pull"},
		{http.MethodPost, "/api/crdt/push"},
		{http.MethodGet, "/api/crdt/status"},
	} {
		req, err := http.NewRequest(ep.method, srv.URL+ep.path, strings.NewReader(`{"domain":"`+crdtsync.DomainReminders+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("%s: %v", ep.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			t.Errorf("%s is not registered on the LAN mux", ep.path)
			continue
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without the secret: status %d, want 401", ep.path, resp.StatusCode)
		}

		req2, _ := http.NewRequest(ep.method, srv.URL+ep.path, strings.NewReader(`{"domain":"`+crdtsync.DomainReminders+`"}`))
		req2.Header.Set(crdtsync.AuthHeader, wiringSecret)
		resp2, err := srv.Client().Do(req2)
		if err != nil {
			t.Fatalf("%s: %v", ep.path, err)
		}
		resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			t.Errorf("%s with the secret: status %d, want 200", ep.path, resp2.StatusCode)
		}
	}
}

func TestStartCRDTSyncOpensTheApprovedDomainsOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := startCRDTSync(ctx, http.NewServeMux(), newWiringDBDir(t), "INSTANCE-A", wiringSecret,
		fabric.NewStaticDiscoverer(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("startCRDTSync: %v", err)
	}
	defer store.Close()

	if err := store.Set(crdtsync.DomainReminders, "id:1", "text", []byte("x")); err != nil {
		t.Errorf("the approved domain was not opened: %v", err)
	}
	// sql:profiles was in this list while its row was a single JSON blob mixing
	// AIAPIKey and PinHash with Theme and Locale. That is fixed at the storage
	// layer (auth/profile_secrets.go writes credentials to a separate,
	// never-bound table), so profiles now replicates — deliberately, and it is
	// NOT one of the domains TestCredentialDomainsAreRefused pins.
	if err := store.Set("sql:profiles", "u1", "data", []byte("{}")); err != nil {
		t.Errorf("sql:profiles should replicate now its credentials live in a separate table: %v", err)
	}
	// sql:users replicates now: the password hash is what makes an account
	// usable on another of the owner's boxes. Signed off deliberately, with the
	// residual recorded in the policy entry — see the note on mustRefuse in
	// crdtsync/policy_test.go.
	if err := store.Set("sql:users", "u1", "data", []byte("{}")); err != nil {
		t.Errorf("sql:users should replicate — an account that cannot be used on a second box defeats the fleet: %v", err)
	}
	for _, refused := range []string{"sql:sessions", "sql:master_key_blobs"} {
		if err := store.Set(refused, "k", "f", []byte("x")); err == nil {
			t.Errorf("%s is replicable through the production wiring", refused)
		}
	}
}

func TestStartCRDTSyncFailsClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := newWiringDBDir(t)

	t.Run("no secret", func(t *testing.T) {
		// An unauthenticated exchange endpoint must never be mounted.
		mux := http.NewServeMux()
		if store, err := startCRDTSync(ctx, mux, dir, "A", "", fabric.NewStaticDiscoverer(), nil, nil, nil, nil, nil); err == nil {
			store.Close()
			t.Fatal("startCRDTSync succeeded with no secret")
		}
		req := httptest.NewRequest(http.MethodPost, "/api/crdt/pull", nil)
		if _, pattern := mux.Handler(req); pattern != "" {
			t.Fatalf("a route was mounted despite no secret: %q", pattern)
		}
	})

	t.Run("no discoverer", func(t *testing.T) {
		if store, err := startCRDTSync(ctx, http.NewServeMux(), dir, "A", wiringSecret, nil, nil, nil, nil, nil, nil); err == nil {
			store.Close()
			t.Fatal("startCRDTSync succeeded with no discoverer")
		}
	})

	t.Run("no bridgeable table", func(t *testing.T) {
		// An empty db dir has no reminders table: the engine must report
		// failure rather than run with nothing bridged, which would look
		// healthy while replicating nothing.
		empty := t.TempDir()
		if store, err := startCRDTSync(ctx, http.NewServeMux(), empty, "A", wiringSecret,
			fabric.NewStaticDiscoverer(), nil, nil, nil, nil, nil); err == nil {
			store.Close()
			t.Fatal("startCRDTSync succeeded with no bridgeable table")
		}
	})
}

// ── reachability of the call site ────────────────────────────────────────────
//
// TestMainWiresCRDTSync below is a source-text grep. It catches DELETION of the
// call, which is half of what happened to services/sync/hotpath.go, and it is
// cheap — so it stays. But a grep cannot tell a call that RUNS from a call that
// merely EXISTS: `if false { startCRDTSync(...) }` satisfies it, and so does a
// call behind a config flag that is never true.
//
// TestCRDTSyncCallSiteIsReachable closes that by reading the syntax tree rather
// than the bytes. It finds the actual call node, walks its enclosing statements,
// and PINS the chain of conditions that gate it. Wrapping the call in anything
// new — `if false`, `if someFlagThatIsNeverTrue`, a loop that never runs —
// changes that chain and fails the test, which forces the change to be a
// deliberate one rather than a silent regression.

// gateChain returns the source text of every condition that gates the call to
// fnName in file, in outermost-to-innermost order.
//
// A condition in an if-statement's INIT does not gate the call — the init runs
// before the condition is evaluated — so the `if store, err := f(); err != nil`
// idiom at the call site is correctly not counted as a gate on f itself.
func gateChain(t *testing.T, filename, fnName string) (gates []string, found bool) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	text := func(n ast.Node) string {
		var b strings.Builder
		if err := printer.Fprint(&b, fset, n); err != nil {
			return "<unprintable>"
		}
		return strings.Join(strings.Fields(b.String()), " ")
	}

	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok || id.Name != fnName {
			return true
		}
		found = true
		for i, encl := range stack {
			switch st := encl.(type) {
			case *ast.IfStmt:
				// The call sits somewhere below this if. Which branch decides
				// the POLARITY: reporting `fabricSecret == ""` for a call that
				// actually lives in the else branch would invert the meaning of
				// the pin and make it worse than useless.
				if i+1 >= len(stack) {
					continue
				}
				child := stack[i+1]
				switch {
				case st.Init != nil && containsNode(st.Init, child):
					// The init runs before the condition is evaluated, so the
					// `if x, err := f(); err != nil` idiom is not f gating itself.
					continue
				case containsNode(st.Body, child):
					gates = append(gates, text(st.Cond))
				case st.Else != nil && containsNode(st.Else, child):
					gates = append(gates, "!("+text(st.Cond)+")")
				}
			case *ast.ForStmt:
				if st.Cond != nil {
					gates = append(gates, "for "+text(st.Cond))
				}
			case *ast.CaseClause:
				gates = append(gates, "case "+text(st))
			}
		}
		return true
	})
	return gates, found
}

// containsNode reports whether outer encloses inner by position.
func containsNode(outer, inner ast.Node) bool {
	return inner.Pos() >= outer.Pos() && inner.End() <= outer.End()
}

func TestCRDTSyncCallSiteIsReachable(t *testing.T) {
	gates, found := gateChain(t, "main.go", "startCRDTSync")
	if !found {
		t.Fatal("main.go contains no call to startCRDTSync — the engine would have no callers")
	}

	// No gate may be a constant false: that is dead code that still greps.
	for _, g := range gates {
		if g == "false" || strings.HasPrefix(g, "false &&") {
			t.Fatalf("the call to startCRDTSync is unreachable — gated by %q", g)
		}
	}

	// The gate chain is PINNED. Every condition here is one that has been
	// reviewed and is legitimate. If this fails because the wiring moved,
	// update the list ON PURPOSE and say why in the commit — that deliberation
	// is the entire value of the test.
	//
	// All four gates are correct preconditions, not accidents: the engine
	// shares fabric's LAN-only mux and its shared secret, so it cannot be
	// mounted anywhere fabric itself is not.
	//
	// NOTE the first one. The engine runs only where the LAN layer runs. That
	// is off for a bare `vulos-server` process and ON in the shipped systemd
	// unit (build.sh sets Environment=VULOS_LAN_ENABLE=1), so it is live on a
	// real box and dormant in a bare dev run. This test is where that fact is
	// recorded, because it is the difference between "wired" and "running".
	allowed := map[string]bool{
		`os.Getenv("VULOS_LAN_ENABLE") == "1"`: true, // the LAN layer is enabled at all
		`!(fabricSecret == "")`:                true, // a shared fabric secret exists to authenticate with
		`!(fabricAppSync == nil)`:              true, // the app-registry sync constructed (fabric's own precondition)
		`!(ferr != nil)`:                       true, // the fabric service itself constructed
	}
	for _, g := range gates {
		if !allowed[g] {
			t.Errorf("startCRDTSync gained an unreviewed gate: %q\n"+
				"If this gate is intended, add it to the allowed set with a reason. "+
				"A gate that is never true at runtime is exactly the dead-code failure this test exists to catch.", g)
		}
	}
	t.Logf("startCRDTSync call site gated by: %v", gates)
}

// TestGateChainDetectsDeadCode is the meta-test: it proves gateChain actually
// reports a gate rather than silently returning an empty chain (which would
// make TestCRDTSyncCallSiteIsReachable pass no matter what main.go said).
//
// It runs the analyser against a fixture whose call IS dead, and requires the
// dead gate to be reported.
func TestGateChainDetectsDeadCode(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "fixture.go")
	const src = `package main

func startCRDTSync() {}
func neverTrue() bool { return false }

func main() {
	if false {
		startCRDTSync()
	}
	if neverTrue() {
		startCRDTSync()
	}
}
`
	if err := os.WriteFile(fixture, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	gates, found := gateChain(t, fixture, "startCRDTSync")
	if !found {
		t.Fatal("gateChain failed to find the call at all")
	}
	var sawFalse, sawFlag bool
	for _, g := range gates {
		if g == "false" {
			sawFalse = true
		}
		if g == "neverTrue()" {
			sawFlag = true
		}
	}
	if !sawFalse {
		t.Errorf("gateChain did not report an `if false` gate: %v", gates)
	}
	if !sawFlag {
		t.Errorf("gateChain did not report a runtime-flag gate: %v", gates)
	}
}

// TestGateChainIgnoresInitOfItsOwnIf pins the one subtlety in the analyser: the
// `if x, err := f(); err != nil` idiom must NOT be reported as f gating itself,
// or the real call site would look permanently gated and the pin would be
// meaningless.
func TestGateChainIgnoresInitOfItsOwnIf(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join(dir, "fixture.go")
	const src = `package main

func startCRDTSync() error { return nil }

func main() {
	if err := startCRDTSync(); err != nil {
		_ = err
	}
}
`
	if err := os.WriteFile(fixture, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	gates, found := gateChain(t, fixture, "startCRDTSync")
	if !found {
		t.Fatal("gateChain failed to find the call")
	}
	if len(gates) != 0 {
		t.Fatalf("the init of an if-statement was miscounted as a gate: %v", gates)
	}
}

// TestMainWiresCRDTSync is the cheap textual half of the anti-dead-code guard.
// It catches outright deletion of the call; TestCRDTSyncCallSiteIsReachable
// catches a call that survives but never runs.
func TestMainWiresCRDTSync(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "startCRDTSync(") {
		t.Fatal("main.go does not call startCRDTSync — the engine would have no callers")
	}
	// It must be mounted on the LAN-only mux, not the public one.
	idx := strings.Index(body, "startCRDTSync(")
	call := body[idx:min(idx+220, len(body))]
	if !strings.Contains(call, "fabricMux") {
		t.Errorf("startCRDTSync is not passed the LAN-only mux: %q", call)
	}
}

// TestStartCRDTSyncIsNotDeadCode guards the other half of the precedent: a
// wiring function that exists but is never reached. The call site above is
// asserted textually; this asserts the function itself still does the three
// things that make it worth calling.
func TestStartCRDTSyncIsNotDeadCode(t *testing.T) {
	src, err := os.ReadFile("crdtsync_wiring.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{
		"RegisterHandlers(",   // a route is mounted
		"crdtsync.NewSyncer(", // a transport loop exists
		"sqlcrdt.New(",        // a real table is bridged
	} {
		if !strings.Contains(body, want) {
			t.Errorf("crdtsync_wiring.go no longer contains %q", want)
		}
	}
}

// TestReplicatedTablesArePolicyApproved re-asserts at the wiring layer what
// sqlcrdt asserts internally: nothing gets bridged that policy did not approve.
func TestReplicatedTablesArePolicyApproved(t *testing.T) {
	approved := map[string]bool{}
	for _, d := range crdtsync.SyncableDomains() {
		approved[d] = true
	}
	for _, rt := range sqlcrdt.ReplicatedTables() {
		if !approved[rt.Domain] {
			t.Errorf("%s is bridged by the wiring but not approved by policy", rt.Domain)
		}
	}
}

// ── WAN peer identity at the wiring site ─────────────────────────────────────
//
// The precedent this file exists for was a route on no mux. The identity
// equivalent — and the shape a hurried wiring change would actually take — is a
// call that IS reached and IS on a mux but is handed `nil, nil` for the signing
// key and the roster, so every WAN peer is silently skipped forever while the
// engine reports itself healthy. These tests close that.

func newWiringIdentity(t *testing.T) (ed25519.PrivateKey, *crdtsync.PeerIdentity) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := crdtsync.NewPeerIdentity(priv)
	if err != nil {
		t.Fatal(err)
	}
	return priv, id
}

func TestStartCRDTSyncMountsPeerKeyAuthWhenIdentityIsSupplied(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	selfPriv, selfID := newWiringIdentity(t)
	_, peer := newWiringIdentity(t)
	_, stranger := newWiringIdentity(t)
	roster, err := crdtsync.NewStaticPeerRoster(peer.ID())
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	store, err := startCRDTSync(ctx, mux, newWiringDBDir(t), "INSTANCE-A", wiringSecret,
		fabric.NewStaticDiscoverer(), nil, nil, selfPriv, roster, nil)
	if err != nil {
		t.Fatalf("startCRDTSync: %v", err)
	}
	defer store.Close()

	srv := httptest.NewServer(mux)
	defer srv.Close()

	body := []byte(`{"domain":"` + crdtsync.DomainReminders + `"}`)
	call := func(from *crdtsync.PeerIdentity) (int, string, string) {
		t.Helper()
		header, _, err := from.SignRequest(http.MethodPost, "/api/crdt/pull", body, selfID.ID())
		if err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/crdt/pull", bytes.NewReader(body))
		req.Header.Set(crdtsync.PeerAuthHeader, header)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get(crdtsync.PeerAuthResponseHeader), string(bytes.TrimRight(raw, "\n"))
	}

	// A rostered peer gets in with no shared secret at all, and gets a response
	// it can attribute to this box.
	code, sig, respBody := call(peer)
	if code != http.StatusOK {
		t.Fatalf("a rostered signed peer got %d: %s", code, respBody)
	}
	if sig == "" {
		t.Fatal("the production wiring served an UNSIGNED response to a WAN peer")
	}

	// A stranger with a perfectly valid key does not.
	if code, _, _ := call(stranger); code != http.StatusUnauthorized {
		t.Fatalf("an unrostered peer got %d, want 401", code)
	}

	// And the LAN secret path is untouched.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/crdt/pull", bytes.NewReader(body))
	req.Header.Set(crdtsync.AuthHeader, wiringSecret)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the LAN shared-secret path regressed: %d", resp.StatusCode)
	}
}

func TestStartCRDTSyncWithoutIdentityMountsOnlyTheSecretPath(t *testing.T) {
	// The fail-closed half: no key or no roster means WAN peers are skipped,
	// and a signed request is NOT accepted (there is nothing to verify it with).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, peer := newWiringIdentity(t)
	_, selfID := newWiringIdentity(t)

	mux := http.NewServeMux()
	store, err := startCRDTSync(ctx, mux, newWiringDBDir(t), "INSTANCE-A", wiringSecret,
		fabric.NewStaticDiscoverer(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("startCRDTSync: %v", err)
	}
	defer store.Close()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body := []byte(`{"domain":"` + crdtsync.DomainReminders + `"}`)
	header, _, err := peer.SignRequest(http.MethodPost, "/api/crdt/pull", body, selfID.ID())
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/crdt/pull", bytes.NewReader(body))
	req.Header.Set(crdtsync.PeerAuthHeader, header)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a signed request was accepted with no verifier configured: %d", resp.StatusCode)
	}
}

func TestFabricPeerRosterHonoursRevocationAndRotation(t *testing.T) {
	// The roster IS the authorisation boundary, so its edge cases are the
	// security-relevant ones, not decoration.
	dir := t.TempDir()
	reg, err := multiinstance.Open(filepath.Join(dir, "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	roster := fabricPeerRoster{reg: reg}

	mk := func() (ed25519.PublicKey, string) {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		// Stored in base64-STANDARD, which is what the roster column uses —
		// while the rendezvous layer speaks base64url. If those ever became two
		// identities every peer would be silently denied.
		return pub, base64.StdEncoding.EncodeToString(pub)
	}

	good, goodB64 := mk()
	revoked, revokedB64 := mk()
	rotated, rotatedNewB64 := mk()
	rotatedOld, rotatedOldB64 := mk()
	expired, expiredOldB64 := mk()
	_, expiredNewB64 := mk()
	stranger, _ := mk()

	for _, in := range []multiinstance.Instance{
		{ULID: "GOOD", Ed25519PublicKey: goodB64},
		{ULID: "REVOKED", Ed25519PublicKey: revokedB64, Revoked: true},
		{ULID: "ROTATED", Ed25519PublicKey: rotatedNewB64,
			PrevEd25519PublicKey: rotatedOldB64, PrevKeyExpiresAt: time.Now().Add(time.Hour)},
		{ULID: "EXPIRED", Ed25519PublicKey: expiredNewB64,
			PrevEd25519PublicKey: expiredOldB64, PrevKeyExpiresAt: time.Now().Add(-time.Hour)},
	} {
		if err := reg.Upsert(in); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name string
		key  ed25519.PublicKey
		want bool
	}{
		{"a rostered instance", good, true},
		{"a REVOKED instance", revoked, false},
		{"the new key after a rotation", rotated, true},
		{"the previous key inside the overlap window", rotatedOld, true},
		{"the previous key after the window closed", expired, false},
		{"a key nobody enrolled", stranger, false},
	} {
		if got := roster.TrustedPeer(tc.key); got != tc.want {
			t.Errorf("%s: TrustedPeer = %v, want %v", tc.name, got, tc.want)
		}
	}

	// A revoked instance is refused even when the key presented is its PREVIOUS
	// one inside a live overlap window — revocation must dominate rotation.
	revokedOld, revokedOldB64 := mk()
	if err := reg.Upsert(multiinstance.Instance{
		ULID: "REVOKED", Ed25519PublicKey: revokedB64, Revoked: true,
		PrevEd25519PublicKey: revokedOldB64, PrevKeyExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if roster.TrustedPeer(revokedOld) {
		t.Error("a revoked instance was trusted via its rotation-overlap key")
	}

	// Fail closed with no registry at all.
	if (fabricPeerRoster{}).TrustedPeer(good) {
		t.Error("a nil registry trusted a peer")
	}
}

// TestCRDTSyncCallSitePassesRealIdentityAndRoster pins the ARGUMENTS, not just
// the reachability, of main.go's call.
//
// A call that is reached, mounted, and handed `nil, nil` for the signing key
// and the roster compiles, passes every behavioural test in this file, logs
// "active", and skips every WAN peer forever. That is the same class of failure
// as a route on no mux, one layer in, so it gets the same treatment: read the
// syntax tree and require the arguments to be real expressions.
func TestCRDTSyncCallSitePassesRealIdentityAndRoster(t *testing.T) {
	args, found := callArgs(t, "main.go", "startCRDTSync")
	if !found {
		t.Fatal("main.go contains no call to startCRDTSync")
	}
	const (
		signerArg = 8
		rosterArg = 9
	)
	if len(args) <= rosterArg {
		t.Fatalf("startCRDTSync is called with %d arguments; the identity and roster are not being passed at all: %v", len(args), args)
	}
	if args[signerArg] == "nil" {
		t.Error("main.go passes nil for the WAN signing identity — every WAN peer would be silently skipped while the engine reports itself active")
	}
	if args[rosterArg] == "nil" {
		t.Error("main.go passes nil for the peer roster — with no roster there is nobody to authorise, so WAN sync could never run")
	}
	// The roster must be built from the real instance registry, not an empty
	// literal that would trust nobody.
	if !strings.Contains(args[rosterArg], "sharedInstanceRegistry") {
		t.Errorf("the peer roster is not built from the instance registry: %q", args[rosterArg])
	}
	t.Logf("startCRDTSync(identity=%s, roster=%s)", args[signerArg], args[rosterArg])
}

// callArgs returns the source text of each argument of the first call to fnName
// in file.
func callArgs(t *testing.T, filename, fnName string) (args []string, found bool) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok || id.Name != fnName {
			return true
		}
		found = true
		for _, a := range call.Args {
			var b strings.Builder
			if err := printer.Fprint(&b, fset, a); err != nil {
				args = append(args, "<unprintable>")
				continue
			}
			args = append(args, strings.Join(strings.Fields(b.String()), " "))
		}
		return false
	})
	return args, found
}

// TestCallArgsDetectsNilArguments is the meta-test for callArgs: it proves the
// analyser reports arguments at all. Without it, a bug that returned an empty
// slice would make the pin above pass no matter what main.go said.
func TestCallArgsDetectsNilArguments(t *testing.T) {
	dir := t.TempDir()
	src := `package main
func startCRDTSync(a, b any) {}
func main() { startCRDTSync(nil, realThing()) }
func realThing() any { return nil }
`
	path := filepath.Join(dir, "fixture.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	args, found := callArgs(t, path, "startCRDTSync")
	if !found {
		t.Fatal("callArgs found no call in the fixture")
	}
	if len(args) != 2 || args[0] != "nil" || args[1] != "realThing()" {
		t.Fatalf("callArgs returned %q; it is not reading arguments correctly", args)
	}
}
