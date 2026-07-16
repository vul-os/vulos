// consume_authcode_race_test.go — regression coverage for the multi-replica
// auth-code single-use claim (sec/cloud-triple).
//
// ConsumeAuthCode historically flipped the code with an UNCONDITIONAL
// `UPDATE ... SET consumed = 1 WHERE code_hash = ?`, relying on a pre-read of
// `consumed` plus the process-local s.mu to enforce single-use. On shared
// Postgres with more than one CP replica the mutex does not serialise across
// processes and, under READ COMMITTED, two concurrent ExchangeCode calls for the
// SAME code could each read consumed=0 and each mint a token set — an auth-code
// replay (RFC 6749 §4.1.2 / RFC 9700 single-use violation).
//
// The fix makes the UPDATE conditional (`AND consumed = 0`) and treats
// RowsAffected==0 as "already claimed" (ErrCodeNotFound), so the row lock is the
// authoritative gate. These tests pin that contract.
package oauthprovider

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// saveFutureCode stores a fresh, unconsumed auth code that expires well in the
// future and returns its code hash.
func saveFutureCode(t *testing.T, st *Store, codeHash string) {
	t.Helper()
	if err := st.SaveAuthCode(context.Background(), AuthCode{
		CodeHash:            codeHash,
		ClientID:            "client-abc",
		UserID:              "user-123",
		RedirectURI:         "https://rp.example/cb",
		Scope:               "openid",
		CodeChallenge:       "challenge",
		CodeChallengeMethod: PKCEMethodS256,
		ExpiresAt:           time.Now().Add(5 * time.Minute),
	}); err != nil {
		t.Fatalf("SaveAuthCode: %v", err)
	}
}

// TestConsumeAuthCode_SingleUse_Sequential pins the single-use contract on any
// backend: a second consume of the same code must fail closed.
func TestConsumeAuthCode_SingleUse_Sequential(t *testing.T) {
	t.Setenv("VULOS_DEV", "true") // all-zeros dev KEK; Open() runs loadKEK()
	st := newTestStore(t)
	const codeHash = "hash-single-use"
	saveFutureCode(t, st, codeHash)

	if _, err := st.ConsumeAuthCode(context.Background(), codeHash); err != nil {
		t.Fatalf("first consume: unexpected error %v", err)
	}
	_, err := st.ConsumeAuthCode(context.Background(), codeHash)
	if !errors.Is(err, ErrCodeNotFound) {
		t.Fatalf("second consume: want ErrCodeNotFound, got %v", err)
	}
}

// TestConsumeAuthCode_ConcurrentClaim_SingleWinner_PG is the multi-replica
// regression. It opens TWO independent Store handles against the SAME Postgres
// database (each with its own s.mu, exactly like two CP replicas), then races a
// pool of goroutines to consume ONE code. Exactly one goroutine may win; every
// other must get ErrCodeNotFound. Before the fix, the unconditional UPDATE let
// multiple winners through under READ COMMITTED.
//
// Skips unless VULOS_TEST_POSTGRES is set (the SQLite path serialises write
// transactions and so cannot exhibit the cross-replica race). CI's
// cp-build-test-pg job supplies the DSN.
func TestConsumeAuthCode_ConcurrentClaim_SingleWinner_PG(t *testing.T) {
	// openPGTestStore (oauthprovider_pg_test.go) sets the DSN env + a dev KEK and
	// opens store #1 against the shared "oauthprovider_pgtest" schema.
	st1 := openPGTestStore(t)

	// Store #2 shares the SAME schema/DSN but is a distinct handle with its own
	// mutex and connection pool — a second "replica".
	db2, err := cpdb.Open("oauthprovider_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (replica 2): %v", err)
	}
	st2, err := Open(db2)
	if err != nil {
		db2.Close()
		t.Fatalf("oauthprovider.Open (replica 2): %v", err)
	}
	t.Cleanup(func() { _ = st2.Close() })

	const codeHash = "hash-concurrent-claim"
	saveFutureCode(t, st1, codeHash)

	const goroutines = 16
	replicas := []*Store{st1, st2}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		wins    int
		badErrs []error
	)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		st := replicas[i%len(replicas)]
		go func() {
			defer wg.Done()
			<-start // release all at once to maximise contention
			_, err := st.ConsumeAuthCode(context.Background(), codeHash)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrCodeNotFound):
				// expected loser
			default:
				badErrs = append(badErrs, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(badErrs) > 0 {
		t.Fatalf("unexpected errors from ConsumeAuthCode: %v", badErrs)
	}
	if wins != 1 {
		t.Fatalf("auth-code single-use violated: got %d winners across replicas, want exactly 1", wins)
	}
}
