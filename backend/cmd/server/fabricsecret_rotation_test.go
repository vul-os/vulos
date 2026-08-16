package main

// FABRIC-SECRET-ROT-01 — the crdtsync door's half of secret rotation.
//
// internal/fabric owns the ring and tests it against fabric's own handlers.
// This file covers the OTHER door: /api/crdt/{pull,push,sync-status}, whose
// secret arm is wired in crdtsync_wiring.go. Two things have to hold, and they
// fail in different ways:
//
//	the MECHANISM — the authorizer actually admits the overlap value before the
//	  deadline and refuses it after. Tested behaviourally, both directions.
//	the CALL SITE — the wiring passes a real ring rather than the bare string it
//	  used to. A revert to crdtsync.SecretAuthorizer(fabricSecret) compiles, runs,
//	  serves every legitimate peer, and silently makes this door unrotatable
//	  again. Nothing behavioural in this package would notice, because a door
//	  with one acceptable secret behaves identically until somebody rotates.

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"vulos/backend/internal/crdtsync"
	"vulos/backend/internal/fabric"
)

const (
	rotOldSecret = "crdt-door-secret-OLD-000000000000"
	rotNewSecret = "crdt-door-secret-NEW-111111111111"
)

type rotClock struct{ v atomic.Int64 }

func (c *rotClock) set(t time.Time) { c.v.Store(t.UnixNano()) }
func (c *rotClock) now() time.Time  { return time.Unix(0, c.v.Load()).UTC() }

// TestCRDTDoorAcceptsTheOldSecretBeforeTheWindowClosesAndRefusesAfter is the
// both-directions test for the door crdtsync_wiring.go builds.
//
// It exercises the real Authorizer through a real ServeMux, because that is the
// shape the wiring installs: an Authorizer is only ever consulted from inside a
// handler, and an authorizer that is correct in isolation but never reached is
// the failure this codebase keeps finding.
func TestCRDTDoorAcceptsTheOldSecretBeforeTheWindowClosesAndRefusesAfter(t *testing.T) {
	start := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	deadline := start.Add(3 * time.Hour)
	clk := &rotClock{}
	clk.set(start)

	// The box has COMMITTED to the new secret and is running a closing overlap
	// on the old one — mid phase 2 of a roll.
	ring := fabric.NewSecretRing(rotNewSecret, rotOldSecret, deadline).WithClock(clk.now)
	authz := crdtsync.RingSecretAuthorizer(ring)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/crdt/pull", func(w http.ResponseWriter, r *http.Request) {
		if !authz(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	call := func(secret string) int {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/crdt/pull", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set(crdtsync.AuthHeader, secret)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("call: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// BEFORE — a peer that has not been rolled yet still syncs. Without this
	// half, a door that refused the old secret from the very start would pass
	// the second half while having partitioned the fleet.
	clk.set(start.Add(time.Minute))
	if got := call(rotOldSecret); got != http.StatusOK {
		t.Fatalf("BEFORE the deadline the CRDT door gave a box holding only the OLD secret %d, want 200 — "+
			"the overlap never opened on this door, so rolling the fleet would partition it", got)
	}

	// AFTER — same process, same ring, one clock tick later.
	clk.set(deadline.Add(time.Second))
	if got := call(rotOldSecret); got != http.StatusUnauthorized {
		t.Fatalf("AFTER the deadline the CRDT door gave a box holding only the OLD secret %d, want 401 — "+
			"the window never closes on this door, which makes it two permanent secrets rather than a rotation", got)
	}

	// The current secret is served in both eras, and an unrelated value in
	// neither, so neither result above is "the door admits everything" or "the
	// door admits nothing".
	for _, at := range []time.Time{start.Add(time.Minute), deadline.Add(time.Hour)} {
		clk.set(at)
		if got := call(rotNewSecret); got != http.StatusOK {
			t.Fatalf("the CURRENT secret got %d at %s, want 200 — closing the overlap took the fleet down with it", got, at)
		}
		if got := call("neither-secret"); got != http.StatusUnauthorized {
			t.Fatalf("an unrelated secret got %d at %s, want 401", got, at)
		}
	}
}

func TestRingSecretAuthorizerWithNoRingAuthorisesNothing(t *testing.T) {
	authz := crdtsync.RingSecretAuthorizer(nil)
	req, err := http.NewRequest(http.MethodPost, "http://box/api/crdt/pull", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"", rotNewSecret, rotOldSecret} {
		req.Header.Set(crdtsync.AuthHeader, secret)
		if authz(req) {
			t.Fatalf("a nil ring admitted %q — a misconfigured box must serve no endpoints, not open ones", secret)
		}
	}
}

// ── the call site ────────────────────────────────────────────────────────────

// TestCRDTDoorIsWiredToTheRotationRing pins the wiring, because the behavioural
// test above passes just as happily against a door that was reverted to a single
// immutable string: one acceptable secret and two acceptable secrets are
// indistinguishable until somebody actually rotates.
func TestCRDTDoorIsWiredToTheRotationRing(t *testing.T) {
	const file = "crdtsync_wiring.go"

	args, found := selectorCallArgs(t, file, "crdtsync", "RingSecretAuthorizer")
	if !found {
		t.Fatal("crdtsync_wiring.go does not call crdtsync.RingSecretAuthorizer — the CRDT door's secret arm is a single " +
			"immutable string again, so VULOS_FABRIC_SECRET cannot be rotated without partitioning the fleet")
	}
	if len(args) != 1 {
		t.Fatalf("crdtsync.RingSecretAuthorizer called with %d arguments: %v", len(args), args)
	}
	if args[0] == "nil" {
		t.Fatal("the CRDT door is wired to a nil ring — it would authorise nobody")
	}

	// The ring must come from the environment loader, not a literal that would
	// hard-code one secret and reintroduce exactly what this replaced.
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	if !strings.Contains(string(src), "fabric.LoadSecretRingFromEnv(") {
		t.Error("the CRDT door's ring is not built by fabric.LoadSecretRingFromEnv — an overlap secret set in the " +
			"environment would be ignored on this door while fabric's own handlers honoured it, which is worse than " +
			"no rotation because half the fleet's doors would disagree")
	}

	// And the OUTBOUND secret must still be the current one. Sending the overlap
	// value would disclose it to a peer that was never given it out of band,
	// which is the one thing the ring must never do.
	if !strings.Contains(string(src), "Secret:  fabricSecret") && !strings.Contains(string(src), "Secret: fabricSecret") {
		t.Error("the syncer no longer sends fabricSecret as its outbound credential — check that it is not sending an " +
			"overlap value, which would hand the new secret to whoever it dials")
	}
	t.Logf("crdtsync.RingSecretAuthorizer(%s)", args[0])
}

// selectorCallArgs returns the source text of each argument of the first call to
// pkg.fn in file. It is the selector-expression counterpart of callArgs (which
// matches bare identifiers only and so cannot see a qualified call).
func selectorCallArgs(t *testing.T, filename, pkg, fn string) (args []string, found bool) {
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
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != fn {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != pkg {
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

// TestSelectorCallArgsFindsAndMissesTheRightThings is the meta-test for the
// analyser above.
//
// An AST check that silently matches nothing reports PASS forever — it is one of
// the recurring hollow-gate shapes in this codebase — so the analyser is proved
// to find a real call, to report its arguments, and to NOT match a same-named
// function on a different package or a bare identifier.
func TestSelectorCallArgsFindsAndMissesTheRightThings(t *testing.T) {
	dir := t.TempDir()
	src := `package main
func main() {
	other.RingSecretAuthorizer(wrongPackage)
	RingSecretAuthorizer(bareIdent)
	crdtsync.RingSecretAuthorizer(secretRing)
}
`
	path := filepath.Join(dir, "fixture.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	args, found := selectorCallArgs(t, path, "crdtsync", "RingSecretAuthorizer")
	if !found {
		t.Fatal("the analyser found no crdtsync.RingSecretAuthorizer call in a fixture that contains one — " +
			"it would report PASS on a wiring that had removed the call entirely")
	}
	if len(args) != 1 || args[0] != "secretRing" {
		t.Fatalf("analyser reported args %v, want [secretRing] — it matched the wrong call or reported no arguments", args)
	}
	if _, f := selectorCallArgs(t, path, "crdtsync", "SomethingElse"); f {
		t.Fatal("the analyser matched a function name that does not appear")
	}
	if _, f := selectorCallArgs(t, path, "nosuchpkg", "RingSecretAuthorizer"); f {
		t.Fatal("the analyser ignored the package qualifier, so a same-named call on any package would satisfy the pin")
	}
}
