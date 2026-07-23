package secrets_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/secrets"
)

var testSeq int

func openManager(t *testing.T) *secrets.Manager {
	t.Helper()
	// M6 (audit): secrets.Open now fails closed unless VULOS_DEV=1 when
	// VULOS_SECRET_KMS_KEY is absent. Set VULOS_DEV=1 for test isolation.
	t.Setenv("VULOS_DEV", "1")
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	m, err := secrets.Open(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// ---------------------------------------------------------------------------
// Test 1: basic rotation — current changes, previous retains old value
// ---------------------------------------------------------------------------

func TestRotation_CurrentChanges(t *testing.T) {
	ctx := context.Background()
	m := openManager(t)

	// First rotation: generates the initial value.
	if err := m.Rotate(ctx, "SESSION_SECRET"); err != nil {
		t.Fatalf("first Rotate: %v", err)
	}
	first := m.Current("SESSION_SECRET")
	if len(first) == 0 {
		t.Fatal("current should be non-empty after rotation")
	}

	// Second rotation: current must change.
	if err := m.Rotate(ctx, "SESSION_SECRET"); err != nil {
		t.Fatalf("second Rotate: %v", err)
	}
	second := m.Current("SESSION_SECRET")
	if string(first) == string(second) {
		t.Error("current did not change after second rotation")
	}
}

// ---------------------------------------------------------------------------
// Test 2: grace-window verify-old — previous value accepted within grace
// ---------------------------------------------------------------------------

func TestVerify_GraceWindow(t *testing.T) {
	ctx := context.Background()
	m := openManager(t)
	m.SetGrace(time.Hour) // wide grace window

	if err := m.Rotate(ctx, "SESSION_SECRET"); err != nil {
		t.Fatalf("first Rotate: %v", err)
	}
	original := m.Current("SESSION_SECRET")

	// Second rotation: original becomes "previous".
	if err := m.Rotate(ctx, "SESSION_SECRET"); err != nil {
		t.Fatalf("second Rotate: %v", err)
	}

	// original (now previous) must still be valid within grace.
	if !m.Verify("SESSION_SECRET", original) {
		t.Error("Verify: previous value should be accepted within grace window")
	}

	// Current value must also be valid.
	current := m.Current("SESSION_SECRET")
	if !m.Verify("SESSION_SECRET", current) {
		t.Error("Verify: current value should be accepted")
	}

	// Old value should be rejected after grace expires.
	m.SetGrace(time.Nanosecond)
	time.Sleep(2 * time.Millisecond)
	if m.Verify("SESSION_SECRET", original) {
		t.Error("Verify: previous value should be rejected after grace expires")
	}
}

// ---------------------------------------------------------------------------
// Test 3: prod fail-closed when VULOS_SECRET_KMS_KEY is missing
// ---------------------------------------------------------------------------

func TestProdFailClosed_MissingMasterKey(t *testing.T) {
	t.Setenv("VULOS_ENV", "prod")
	t.Setenv("VULOS_SECRET_KMS_KEY", "") // explicitly unset

	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	defer db.Close()
	_, err = secrets.Open(db)
	if err == nil {
		t.Fatal("expected error when VULOS_SECRET_KMS_KEY is missing in prod")
	}
	if !errors.Is(err, secrets.ErrMissingMasterKey) {
		t.Errorf("expected ErrMissingMasterKey, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test 4: encrypted-at-rest round-trip — persist + reload yields same value
// ---------------------------------------------------------------------------

func TestEncryptedAtRest_RoundTrip(t *testing.T) {
	t.Setenv("VULOS_SECRET_KMS_KEY", "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")

	// Use a unique in-memory DSN; keep m1 open while we open m2 so the
	// in-memory shared-cache database is not destroyed on Close.
	testSeq++
	dsn := "file:secrets_enc_rt_" + strconv.Itoa(testSeq) + "?mode=memory&cache=shared"

	db1, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		t.Fatalf("cpdb open db1: %v", err)
	}
	m1, err := secrets.Open(db1)
	if err != nil {
		t.Fatalf("Open m1: %v", err)
	}

	ctx := context.Background()
	if err := m1.Rotate(ctx, "JWT_SIGNING_KEY"); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	value1 := m1.Current("JWT_SIGNING_KEY")

	// Open a second handle (m1 still open, so the DB is not freed).
	db2, err := cpdb.OpenSQLiteDSN(dsn)
	if err != nil {
		t.Fatalf("cpdb open db2: %v", err)
	}
	m2, err := secrets.Open(db2)
	if err != nil {
		t.Fatalf("Open m2: %v", err)
	}
	defer func() {
		_ = m1.Close()
		_ = m2.Close()
	}()

	value2 := m2.Current("JWT_SIGNING_KEY")
	if string(value1) != string(value2) {
		t.Errorf("round-trip mismatch: persisted %q, reloaded %q", value1, value2)
	}
}

// ---------------------------------------------------------------------------
// Test 5: unknown name returns ErrUnknownSecret
// ---------------------------------------------------------------------------

func TestRotation_UnknownName(t *testing.T) {
	ctx := context.Background()
	m := openManager(t)

	err := m.Rotate(ctx, "NONEXISTENT_SECRET")
	if !errors.Is(err, secrets.ErrUnknownSecret) {
		t.Errorf("expected ErrUnknownSecret, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test 6: concurrent Verify during rotation — no data races
// ---------------------------------------------------------------------------

func TestConcurrentVerify(t *testing.T) {
	ctx := context.Background()
	m := openManager(t)

	if err := m.Rotate(ctx, "SESSION_SECRET"); err != nil {
		t.Fatalf("initial Rotate: %v", err)
	}

	var wg sync.WaitGroup
	const goroutines = 20
	const iters = 50

	// Writers: rotate concurrently.
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				_ = m.Rotate(ctx, "SESSION_SECRET")
			}
		}()
	}

	// Readers: verify concurrently (we test for no panic/race, not result).
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			candidate := []byte("some-value")
			for j := 0; j < iters; j++ {
				_ = m.Verify("SESSION_SECRET", candidate)
				_ = m.Current("SESSION_SECRET")
			}
		}()
	}

	wg.Wait()
}
