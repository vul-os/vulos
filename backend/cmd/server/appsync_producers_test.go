package main

// SYNC-APPS-01: the installed app set's producers must be REACHED, not merely
// present.
//
// internal/sqlcrdt's TestInstalledAppSetHasBothProducers scans the source for a
// call to DesireInstall / LocalInstall and is the guard that caught the original
// defect. It has a blind spot this test exists to cover, and the blind spot was
// found by mutation rather than by reading: main.go wraps the producers in
// closures (recordAppDesire, recordAppRealised, recordAppUnrealised), so
// deleting every call to those closures from the HTTP handlers leaves the
// producer calls sitting inside the closure bodies where the scan still finds
// them. The mutant survived, the guard reported PASS, and the installed app set
// would silently be back to inventory-only replication.
//
// Collection is not execution. This test reads the syntax tree for the call
// SITES instead, in the same spirit as TestCRDTSyncCallSiteIsReachable, and
// computes its own gate chain (see callSites for why delegating did not work).

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestAppSetProducersAreCalledFromTheStoreHandlers pins that every install and
// uninstall route records BOTH facts — the fleet's desire and this box's
// realisation.
//
// The counts are lower bounds with a named reason each, not magic numbers:
//
//	recordAppDesire     3  POST /api/store/install, POST /api/store/registry/install
//	                       (both express "the user wants this app"), and
//	                       POST /api/store/uninstall (the tombstone). The
//	                       uninstall one is the load-bearing one: without it a
//	                       removal is spelled as an absence, and the next sync
//	                       with any box that still has the app puts it back.
//	recordAppRealised   2  the two install routes.
//	recordAppUnrealised 1  the uninstall route.
func TestAppSetProducersAreCalledFromTheStoreHandlers(t *testing.T) {
	sites := callSites(t, "main.go")

	for _, want := range []struct {
		fn     string
		atteat int
		why    string
	}{
		{"recordAppDesire", 3, "two install routes express desire, and the uninstall route writes the tombstone that stops the app being resurrected"},
		{"recordAppRealised", 2, "both install routes must report that THIS box now has the app, or the fleet cannot say which instance holds it"},
		{"recordAppUnrealised", 1, "the uninstall route must report that this box no longer has it"},
	} {
		got := sites[want.fn]
		if len(got) < want.atteat {
			t.Errorf("main.go calls %s() from %d place(s), want at least %d — %s.\n"+
				"A producer that is defined but not CALLED still satisfies a source scan, which is how the installed app set "+
				"could quietly go back to replicating a per-instance inventory with every test green.",
				want.fn, len(got), want.atteat, want.why)
			continue
		}
		for _, g := range got {
			for _, gate := range g.gates {
				if gate == "false" || strings.HasPrefix(gate, "false &&") {
					t.Errorf("a call to %s() is unreachable — gated by %q", want.fn, gate)
				}
			}
		}
	}
}

// TestAppReconcileLoopIsStarted pins the other direction. Producing desire rows
// that nothing acts on would replicate an intention and leave every other box
// exactly as it was — the same shape of defect one layer up.
//
// The loop's own gates are checked rather than pinned verbatim: it must not be
// dead code, and the kill switch must remain an OPT-OUT. A default-off
// reconciler would mean the directive's default (everything syncs) required
// configuration, which is the opposite of a default.
func TestAppReconcileLoopIsStarted(t *testing.T) {
	sites := callSites(t, "main.go")
	got := sites["Reconcile"]
	if len(got) == 0 {
		t.Fatal("main.go never calls Reconcile — the fleet's desired set would be replicated and never acted on, " +
			"so an app installed on one box would appear in the other's database and never on its disk")
	}
	for _, g := range got {
		for _, gate := range g.gates {
			if gate == "false" || strings.HasPrefix(gate, "false &&") {
				t.Fatalf("the reconcile loop is unreachable — gated by %q", gate)
			}
		}
	}

	src := readMainSource(t)
	if !strings.Contains(src, `VULOS_APP_RECONCILE`) {
		t.Error("the reconcile loop has no kill switch: a loop that installs and removes software on its own and cannot be " +
			"stopped is worse than one that can")
	}
	// The switch must be checked for "off", i.e. opt-OUT. A check for "on" would
	// make the directive's default require configuration.
	if !strings.Contains(src, `EqualFold(os.Getenv("VULOS_APP_RECONCILE"), "off")`) {
		t.Error(`VULOS_APP_RECONCILE is not an opt-OUT. It must disable on "off" rather than enable on "on": under the standing ` +
			`directive, apps following you between your own boxes is the default, not a feature someone has to turn on.`)
	}
}

// readMainSource returns main.go's bytes as a string.
func readMainSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	return string(b)
}

// callSite is one call of a named function, with the conditions gating it.
type callSite struct {
	gates []string
}

// callSites parses filename and returns, per called function name, one entry per
// call site with the conditions gating THAT site.
//
// It computes the gate chain itself rather than delegating to gateChain, and
// that is not duplication for its own sake — the first version did delegate, and
// a mutation walked straight through it. gateChain matches `call.Fun.(*ast.Ident)`,
// so it finds `startCRDTSync(...)` and never `fabricAppSync.Reconcile(...)`,
// which is a *ast.SelectorExpr. It returned found=false, no gates, and the
// "is it dead code" loop ran over an empty slice and reported PASS on a
// reconcile loop wrapped in `if false`. A check that iterates nothing passes
// everything.
func callSites(t *testing.T, filename string) map[string][]callSite {
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

	out := map[string][]callSite{}
	names := map[string]bool{
		"recordAppDesire": true, "recordAppRealised": true,
		"recordAppUnrealised": true, "Reconcile": true,
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
		var name string
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			name = fun.Name
		case *ast.SelectorExpr:
			name = fun.Sel.Name
		}
		if !names[name] {
			return true
		}

		var gates []string
		for i, encl := range stack {
			st, ok := encl.(*ast.IfStmt)
			if !ok || i+1 >= len(stack) {
				continue
			}
			child := stack[i+1]
			switch {
			case st.Init != nil && containsNode(st.Init, child):
				// The init runs before the condition is evaluated, so the
				// `if x, err := f(); err != nil` idiom is not f gating itself.
				continue
			case st.Else != nil && containsNode(st.Else, child):
				// Polarity matters: reporting the condition verbatim for a call
				// in the ELSE branch would invert the meaning of the pin.
				gates = append(gates, "!("+text(st.Cond)+")")
			default:
				gates = append(gates, text(st.Cond))
			}
		}
		out[name] = append(out[name], callSite{gates: gates})
		return true
	})
	return out
}
