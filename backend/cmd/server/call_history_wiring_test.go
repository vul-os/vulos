package main

// PEER-24: the peer call log must be CONNECTED, not merely constructed.
//
// The defect this guards against is one main.go actually shipped for months:
// RegisterCallHistoryHandlers built a CallHistStore and registered its two
// routes, and nothing was ever given the store to write to. CallHistRecord had
// zero non-test callers, so GET /api/peering/call/history answered [] in
// production forever — while the Phone app's Recents tab fetched it on every
// load and rendered the empty result as a feature.
//
// services/peering's TestCallHistory_* suite proves the relay records when it
// HAS a store. That is the behaviour; this is the wiring, and the two fail
// independently. Deleting the SetCallHistory line from main.go leaves every one
// of those tests green — they wire the store themselves — and puts the box
// straight back to a log nothing writes. A unit test cannot see that, and
// peer42_wiring_test.go cannot either: it builds a REPLICA of this block rather
// than reading this file.
//
// Construction is not connection, the same way collection is not execution.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestCallRelayIsGivenTheCallHistoryStore pins that main.go hands the relay
// somewhere to log finished calls.
func TestCallRelayIsGivenTheCallHistoryStore(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}

	calls := map[string]int{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			calls[fun.Name]++
		case *ast.SelectorExpr:
			calls[fun.Sel.Name]++
		}
		return true
	})

	if calls["RegisterCallHistoryHandlers"] == 0 {
		t.Fatal("main.go never calls RegisterCallHistoryHandlers — there would be no call log at all")
	}
	if calls["NewCallRelay"] == 0 {
		t.Fatal("main.go never calls NewCallRelay — there would be no call relay at all")
	}
	if calls["SetCallHistory"] == 0 {
		t.Error("main.go constructs a CallHistStore and a CallRelay but never calls SetCallHistory, " +
			"so nothing on the box would ever write a call log entry. " +
			"GET /api/peering/call/history would answer [] forever while the Phone app's Recents tab " +
			"kept fetching it — the exact defect this wiring was added to fix.")
	}
}
