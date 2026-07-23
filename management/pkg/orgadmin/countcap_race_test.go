// countcap_race_test.go — regression coverage for the multi-replica org-creation
// count-cap TOCTOU (sec/cloud-countcaps, COUNTCAP-TOCTOU-01).
//
// createOrg used to read CountOrgsOwnedByAccount in one query and then insert the
// org in a SEPARATE transaction. On shared Postgres with more than one CP replica
// the process mutex does not serialise across processes, so two concurrent creates
// from a FRESH account could each observe owned==0 and each (a) mint a free root
// mailbox and (b) slip past the per-account cap — an anti-farming / free-mailbox
// abuse. The fix moves the count → decide → insert sequence into ONE per-account
// locked transaction (CreateOrgAtomic), so exactly one first-org free mailbox is
// granted and the cap holds under any interleaving.
//
// The tests open TWO independent Store handles (each with its own mutex, exactly
// like two CP replicas) over the SAME database and race a pool of goroutines.
// Distinct org NAMES are used per goroutine so slug-uniqueness does not mask the
// count race — every insert can succeed on its own merits.
package orgadmin_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/orgadmin"
)

// twoReplicaOrgServices builds two OrgService "replicas" — each backed by its own
// SQLStore with an INDEPENDENT mutex, exactly like two CP processes — over a shared
// database.
//
//   - Postgres (VULOS_TEST_POSTGRES set): two SEPARATE connection pools against one
//     schema. This is the true cross-replica race the fix targets; the per-account
//     advisory lock provides the serialisation.
//   - SQLite (default): both stores wrap ONE *cpdb.DB handle. Self-host SQLite is
//     single-process anyway, and a single shared in-memory DB reached over two
//     independent connections deadlocks under concurrent write transactions. Sharing
//     the handle keeps the two Store mutexes independent (so the pre-fix split
//     read/insert still interleaves and would race) while the single-writer pool
//     serialises the actual SQL — no deadlock, and the atomic path is exercised.
func twoReplicaOrgServices(t *testing.T, name string) (*orgadmin.OrgService, *orgadmin.OrgService) {
	t.Helper()

	newStore := func(db *cpdb.DB) *orgadmin.SQLStore {
		st, err := orgadmin.OpenSQLStore(db)
		if err != nil {
			_ = db.Close()
			t.Fatalf("OpenSQLStore: %v", err)
		}
		return st
	}

	var st1, st2 *orgadmin.SQLStore
	if dsn := os.Getenv("VULOS_TEST_POSTGRES"); dsn != "" {
		t.Setenv("DATABASE_URL", dsn)
		db1, err := cpdb.Open(name)
		if err != nil {
			t.Fatalf("open db1: %v", err)
		}
		db2, err := cpdb.Open(name)
		if err != nil {
			t.Fatalf("open db2: %v", err)
		}
		st1, st2 = newStore(db1), newStore(db2)
		t.Cleanup(func() {
			_, _ = db1.Exec(`DROP SCHEMA IF EXISTS ` + name + ` CASCADE`)
			_ = st1.Close()
			_ = st2.Close()
		})
	} else {
		db, err := cpdb.OpenSQLiteDSN("file:" + name + "?mode=memory&cache=shared")
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		st1, st2 = newStore(db), newStore(db)
		t.Cleanup(func() { _ = db.Close() })
	}

	svc1 := orgadmin.NewOrgService(st1, nil, "example.com")
	svc2 := orgadmin.NewOrgService(st2, nil, "example.com")
	// Raise caps so the FREE-MAILBOX test is not masked by the per-account cap /
	// rate-limit window; the cap test lowers them explicitly.
	for _, s := range []*orgadmin.OrgService{svc1, svc2} {
		s.MaxOrgsPerAccount = 1000
		s.MaxOrgsPerAccountPaid = 1000
		s.MaxOrgsPerWindow = 1000
	}
	return svc1, svc2
}

// countGrantedMailboxes returns how many of the account's orgs carry a root
// mailbox (the once-per-free-account entitlement).
func countGrantedMailboxes(t *testing.T, svc *orgadmin.OrgService, account string) (mailboxes, orgs int) {
	t.Helper()
	list, err := svc.Store.ListOrgsForAccount(context.Background(), account)
	if err != nil {
		t.Fatalf("ListOrgsForAccount: %v", err)
	}
	for _, m := range list {
		if m.RootMailbox != "" {
			mailboxes++
		}
	}
	return mailboxes, len(list)
}

// TestCreateOrg_ExactlyOneFreeMailboxAcrossReplicas is the free-mailbox race guard.
// A fresh account fires N concurrent creates across two replicas; exactly ONE org
// may be granted the free root mailbox. Pre-fix, several goroutines each read
// owned==0 and each stamped a mailbox.
func TestCreateOrg_ExactlyOneFreeMailboxAcrossReplicas(t *testing.T) {
	svc1, svc2 := twoReplicaOrgServices(t, "orgcap_mbx")
	replicas := []*orgadmin.OrgService{svc1, svc2}
	const account = "acct-mbx-race"
	const goroutines = 12

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		svc := replicas[i%len(replicas)]
		name := fmt.Sprintf("Org Number %02d", i) // distinct → distinct slug
		go func(idx int) {
			defer wg.Done()
			<-start
			_, errs[idx] = svc.CreateOrg(context.Background(), account, name)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("CreateOrg[%d] unexpected error: %v", i, err)
		}
	}
	mailboxes, orgs := countGrantedMailboxes(t, svc1, account)
	if orgs != goroutines {
		t.Fatalf("expected %d orgs created, got %d", goroutines, orgs)
	}
	if mailboxes != 1 {
		t.Fatalf("free-mailbox race: %d orgs were granted a free mailbox, want exactly 1", mailboxes)
	}
}

// TestCreateOrg_PerAccountCapHoldsAcrossReplicas is the anti-farming cap guard.
// With a per-account cap of 3, a fresh account fires N concurrent creates across
// two replicas; no more than 3 orgs may ever be persisted. Pre-fix, concurrent
// creates each read owned < cap and all inserted, blowing past the cap.
func TestCreateOrg_PerAccountCapHoldsAcrossReplicas(t *testing.T) {
	svc1, svc2 := twoReplicaOrgServices(t, "orgcap_cap")
	const cap = 3
	// Lower the per-account cap; keep the window wide so only the cap gates.
	for _, s := range []*orgadmin.OrgService{svc1, svc2} {
		s.MaxOrgsPerAccount = cap
		s.MaxOrgsPerAccountPaid = cap
		s.MaxOrgsPerWindow = 1000
	}
	replicas := []*orgadmin.OrgService{svc1, svc2}
	const account = "acct-cap-race"
	const goroutines = 12

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
		badErrs []error
	)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		svc := replicas[i%len(replicas)]
		name := fmt.Sprintf("Capped Org %02d", i)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.CreateOrg(context.Background(), account, name)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case errors.Is(err, orgadmin.ErrOrgLimitReached):
				// expected loser
			default:
				badErrs = append(badErrs, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(badErrs) > 0 {
		t.Fatalf("unexpected errors: %v", badErrs)
	}
	_, orgs := countGrantedMailboxes(t, svc1, account)
	if orgs > cap {
		t.Fatalf("per-account cap breached: %d orgs persisted, cap is %d", orgs, cap)
	}
	if created != orgs {
		t.Fatalf("created (%d) and persisted (%d) disagree", created, orgs)
	}
	if orgs != cap {
		t.Fatalf("expected exactly %d orgs at the cap, got %d", cap, orgs)
	}
}
