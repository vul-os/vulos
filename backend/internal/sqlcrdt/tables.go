package sqlcrdt

import (
	"context"
	"fmt"
	"log"
	"time"

	"vulos/backend/internal/crdtsync"
)

// ReplicatedTable binds an APPROVED crdtsync domain to the SQLite file and
// table that actually hold it.
//
// crdtsync/policy.go decides WHETHER a domain replicates and why; this decides
// WHERE it lives and which columns are in scope. The two are kept in step by a
// test that fails if either side gains an entry the other lacks — a domain
// approved in the policy but never bound here would be a sync that silently
// carries nothing, which is exactly the failure mode this whole exercise is
// meant to avoid.
type ReplicatedTable struct {
	// Domain is the crdtsync domain. Must equal Domain(Spec.Name).
	Domain string
	// DBFile is the SQLite filename within the box's db directory.
	DBFile string
	// Spec declares the table and its column policy.
	Spec TableSpec
	// Why records what this table buys by replicating.
	Why string
}

// ReplicatedTables is the set of SQL tables wired to the CRDT engine.
func ReplicatedTables() []ReplicatedTable {
	return []ReplicatedTable{
		{
			Domain: crdtsync.DomainReminders,
			DBFile: "reminders.db",
			Spec: TableSpec{
				Name: "reminders",
				// Explicit allow-list rather than "everything except": a column
				// added to this table later must be considered on purpose
				// before it starts leaving the box.
				Columns: []string{"id", "user_id", "text", "remind_at", "created_at", "done"},
			},
			Why: "A reminder is only useful if it fires wherever you are. Column granularity matters here: marking one done on " +
				"one box while editing its text on another must keep both edits.",
		},
		{
			Domain: "sql:acctsec_sensitive_actions",
			DBFile: "accountsecurity.db",
			Spec: TableSpec{
				Name: "acctsec_sensitive_actions",
				// event_id is the PRIMARY KEY (migration 0003) and so the key the
				// session extension captures on. It is deliberately NOT listed as a
				// replicated column: it identifies the row rather than describing it,
				// and a column that also carried the key would let a peer propose a
				// change to it.
				Columns: []string{"ts", "user_id", "action", "client_ip", "user_agent"},
			},
			Why: "The audit trail is worth most on the box an attacker did NOT compromise. Replicated grow-only (crdtsync policy), so an entry " +
				"can be added everywhere and edited or deleted nowhere — an attacker who erases the local log no longer erases the evidence.",
		},
		{
			Domain: "sql:profiles",
			DBFile: "auth.db",
			Spec: TableSpec{
				Name: "profiles",
				// The whole row replicates. Credentials are not excluded HERE —
				// they are not in this table at all: auth/profile_secrets.go
				// writes AIAPIKey, PinHash and secret-named Settings keys to a
				// separate profile_secrets table, which is never bound. Column
				// exclusion could not have done this job, because the row is one
				// JSON `data` blob and Exclude cannot reach inside one.
				Columns: []string{"data"},
			},
			Why: "Settings are what people most expect to follow them between their own boxes. The credential half is a different table and is " +
				"not replicated; see crdtsync policy for which fields stay local and why.",
		},
		{
			Domain: "sql:users",
			DBFile: "auth.db",
			Spec: TableSpec{
				Name: "users",
				// The whole row, password hash included — that hash is what makes
				// the account usable on another of the owner's boxes. Free-form
				// Preferences are filtered before they reach this table: secret-
				// named keys are written to profile_secrets instead, which is
				// never bound (auth/user_secrets.go).
				Columns: []string{"data"},
			},
			Why: "One account across your own machines is what makes a fleet feel like one computer. Without the hash the account exists on the " +
				"second box and cannot be logged into, so people make a second account with a different password and the two drift apart.",
		},
	}
}

// LiveDBPath is the full path to a replicated table's database.
func (rt ReplicatedTable) LiveDBPath(dbDir string) string {
	return joinPath(dbDir, rt.DBFile)
}

// DefaultCycleInterval is how often a bridge captures local SQL writes and
// materialises merged state back. It is deliberately faster than the network
// sync interval: capture is a local diff against a baseline file, and a change
// that has not been captured yet cannot be replicated at all.
const DefaultCycleInterval = 5 * time.Second

// Run cycles the bridge until ctx is cancelled.
//
// onCaptured fires after a cycle that recorded at least one local op, so the
// wiring can Nudge the network syncer instead of waiting for its tick. A cycle
// error is logged and the loop continues: a transient SQLite busy error must
// not permanently stop replication for the life of the process.
func (b *Bridge) Run(ctx context.Context, interval time.Duration, onCaptured func()) {
	if interval <= 0 {
		interval = DefaultCycleInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			captured, applied, err := b.Cycle()
			if err != nil {
				log.Printf("[sqlcrdt] cycle: %v", err)
				continue
			}
			if captured > 0 && onCaptured != nil {
				onCaptured()
			}
			if applied > 0 {
				b.notifyApplied(applied)
			}
		}
	}
}

// joinPath avoids importing path/filepath into this file's public surface for
// one call; it keeps the separator handling in one place.
func joinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	if dir[len(dir)-1] == '/' {
		return dir + name
	}
	return fmt.Sprintf("%s/%s", dir, name)
}
