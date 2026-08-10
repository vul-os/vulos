package crdtsync

import (
	"errors"
	"strings"
	"testing"
)

func TestSyncableDomainsIsDerivedFromDecisions(t *testing.T) {
	got := SyncableDomains()
	if len(got) == 0 {
		t.Fatal("no domain is approved for replication — the wiring would have nothing to open")
	}
	for _, d := range got {
		dec, ok := DecisionFor(d)
		if !ok || !dec.Sync {
			t.Errorf("%s is in SyncableDomains but has no Sync=true decision", d)
		}
	}
	// Every approved domain must be reachable from the list and vice versa.
	for _, dec := range Decisions {
		found := false
		for _, d := range got {
			if d == dec.Domain {
				found = true
			}
		}
		if dec.Sync != found {
			t.Errorf("%s: Sync=%v but present in SyncableDomains=%v", dec.Domain, dec.Sync, found)
		}
	}
}

func TestEveryDecisionHasReasoning(t *testing.T) {
	// A policy entry with no reason is an entry nobody can review. Refusals get
	// the same bar as approvals.
	seen := map[string]bool{}
	for _, d := range Decisions {
		if d.Domain == "" {
			t.Error("decision with empty domain")
		}
		if seen[d.Domain] {
			t.Errorf("duplicate decision for %s", d.Domain)
		}
		seen[d.Domain] = true
		if len(strings.TrimSpace(d.Reason)) < 40 {
			t.Errorf("%s: reason is too thin to be a decision: %q", d.Domain, d.Reason)
		}
	}
}

func TestCredentialDomainsAreRefused(t *testing.T) {
	// The domains it would be most damaging to replicate must be explicitly
	// refused, not merely absent — absence looks like an oversight and would
	// silently become allowed if someone widened the allow-list.
	mustRefuse := []string{
		"sql:sessions",
		"sql:users",
		"sql:recovery_blobs",
		"sql:master_key_blobs",
		"sql:local_api_keys",
		"sql:push_subscriptions",
	}
	for _, d := range mustRefuse {
		dec, ok := DecisionFor(d)
		if !ok {
			t.Errorf("%s has no recorded decision — auth material must be refused ON THE RECORD", d)
			continue
		}
		if dec.Sync {
			t.Errorf("%s is approved for replication; it carries auth material", d)
		}
	}
	// And none of them can be in the approved list by any path.
	approved := map[string]bool{}
	for _, d := range SyncableDomains() {
		approved[d] = true
	}
	for _, d := range mustRefuse {
		if approved[d] {
			t.Errorf("%s reached SyncableDomains", d)
		}
	}
}

func TestRefusedDomainCannotArriveByAnyRoute(t *testing.T) {
	// The engine replicates what it is handed, so the allow-list has to hold on
	// EVERY path that introduces state — not just the local write path.
	s := newTestStore(t, "A")
	const refused = "sql:sessions"

	t.Run("local set", func(t *testing.T) {
		if err := s.Set(refused, "k", "f", []byte("v")); err == nil {
			t.Fatal("Set into a refused domain must fail")
		}
	})
	t.Run("local delete", func(t *testing.T) {
		if err := s.Delete(refused, "k", "f"); err == nil {
			t.Fatal("Delete in a refused domain must fail")
		}
	})
	t.Run("counter add", func(t *testing.T) {
		if err := s.Add(refused, "k", "f", 1); err == nil {
			t.Fatal("Add into a refused domain must fail")
		}
	})
	t.Run("pushed delta", func(t *testing.T) {
		d := &Delta{Domain: refused, Ops: []Op{
			{Domain: refused, Actor: "B", Seq: 1, Key: "k", Field: "token", Kind: OpSet,
				Value: []byte("stolen"), Stamp: Stamp{Wall: 1, Actor: "B"}},
		}}
		if _, err := s.Merge(d); err == nil {
			t.Fatal("a peer must not be able to push a refused domain")
		}
	})
	t.Run("applied snapshot", func(t *testing.T) {
		snap := &Snapshot{Domain: refused, SchemaVers: SnapshotSchemaVersion, VV: VersionVector{"B": 1},
			Registers: []RegisterState{{Key: "k", Field: "token", Value: []byte("stolen"), Stamp: Stamp{Wall: 1, Actor: "B"}}}}
		if err := s.ApplySnapshot(snap); err == nil {
			t.Fatal("a peer must not be able to push a refused domain as a snapshot")
		}
	})
	t.Run("snapshot smuggled inside a delta", func(t *testing.T) {
		// The delta names an ALLOWED domain but carries a snapshot for a refused
		// one. The snapshot's own domain is what gets written, so that is what
		// must be checked.
		d := &Delta{Domain: dom, SnapshotRequired: true, Snapshot: &Snapshot{
			Domain: refused, SchemaVers: SnapshotSchemaVersion,
			Registers: []RegisterState{{Key: "k", Field: "token", Value: []byte("stolen"), Stamp: Stamp{Wall: 1, Actor: "B"}}},
		}}
		if _, err := s.Merge(d); err == nil {
			t.Fatal("a snapshot for a refused domain must not ride in on an allowed delta")
		}
	})
	t.Run("op smuggled into an allowed delta", func(t *testing.T) {
		d := &Delta{Domain: dom, Ops: []Op{
			{Domain: refused, Actor: "B", Seq: 1, Key: "k", Field: "token", Kind: OpSet,
				Value: []byte("stolen"), Stamp: Stamp{Wall: 1, Actor: "B"}},
		}}
		if _, err := s.Merge(d); err == nil {
			t.Fatal("an op naming a different domain must not ride in on an allowed delta")
		}
	})
	t.Run("served delta", func(t *testing.T) {
		if _, err := s.Delta(refused, VersionVector{}, 0); err == nil {
			t.Fatal("a peer must not be able to ask us to serve a refused domain")
		}
	})

	// Nothing from any of those attempts reached the database.
	domains, err := s.Domains()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range domains {
		if d == refused {
			t.Fatal("a refused domain reached the database")
		}
	}
}

func TestRefusalErrorCarriesTheReason(t *testing.T) {
	// A refusal that explains itself is one a developer fixes correctly instead
	// of working around.
	s := newTestStore(t, "A")
	err := s.Set("sql:users", "k", "f", []byte("v"))
	var notAllowed *ErrDomainNotAllowed
	if !errors.As(err, &notAllowed) {
		t.Fatalf("err = %v, want *ErrDomainNotAllowed", err)
	}
	if !strings.Contains(err.Error(), "password hash") {
		t.Fatalf("refusal does not explain itself: %v", err)
	}
	// An unknown domain is refused too, just without a recorded reason.
	err = s.Set("sql:never_heard_of_it", "k", "f", []byte("v"))
	if !errors.As(err, &notAllowed) {
		t.Fatalf("unknown domain: err = %v, want *ErrDomainNotAllowed", err)
	}
}

func TestApprovedDomainWorks(t *testing.T) {
	// The mirror of the refusal tests: a store opened with the production
	// allow-list can actually use the approved domain.
	s := newTestStoreWithDomains(t, "A", SyncableDomains())
	if err := s.Set(DomainReminders, "id:1", "text", []byte("buy milk")); err != nil {
		t.Fatalf("approved domain rejected: %v", err)
	}
	if v, ok, err := s.Get(DomainReminders, "id:1", "text"); err != nil || !ok || string(v) != "buy milk" {
		t.Fatalf("Get = %q ok=%v err=%v", v, ok, err)
	}
}
