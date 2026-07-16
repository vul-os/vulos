// webhooks_pg_test.go — Postgres integration tests for the webhooks store.
//
// Skipped unless VULOS_TEST_POSTGRES is set to a valid Postgres DSN, e.g.:
//
//	VULOS_TEST_POSTGRES=postgres://postgres:postgres@localhost:5432/cp_test?sslmode=disable \
//	  go test ./internal/webhooks/...
package webhooks_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/webhooks"
)

func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	return dsn
}

func openPGTestStore(t *testing.T) *webhooks.Store {
	t.Helper()
	dsn := pgDSN(t)
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")

	db, err := cpdb.Open("webhooks_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := webhooks.OpenForTest(db)
	if err != nil {
		db.Close()
		t.Fatalf("webhooks.OpenForTest (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS webhooks_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_CreateAndGetSubscription(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	sub, err := st.CreateSubscription(ctx, "pg-acc1", "https://example.com/hook", []string{"mail.delivered"})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if sub.ID == "" {
		t.Fatal("expected non-empty subscription ID")
	}

	got, err := st.GetSubscription(ctx, "pg-acc1", sub.ID)
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if got.AccountID != "pg-acc1" {
		t.Errorf("account = %q, want pg-acc1", got.AccountID)
	}
	if len(got.Topics) != 1 || got.Topics[0] != "mail.delivered" {
		t.Errorf("topics = %v, want [mail.delivered]", got.Topics)
	}
}

func TestPG_DeleteSubscription(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	sub, err := st.CreateSubscription(ctx, "pg-acc2", "https://example.com/hook2", []string{"billing.topup"})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}
	if err := st.DeleteSubscription(ctx, "pg-acc2", sub.ID); err != nil {
		t.Fatalf("DeleteSubscription: %v", err)
	}
	if err := st.DeleteSubscription(ctx, "pg-acc2", sub.ID); err != webhooks.ErrSubNotFound {
		t.Errorf("second delete: got %v, want ErrSubNotFound", err)
	}
}

func TestPG_EnqueueAndDeliveryStatus(t *testing.T) {
	st := openPGTestStore(t)
	ctx := context.Background()

	sub, err := st.CreateSubscription(ctx, "pg-acc3", "https://example.com/hook3", []string{"device.enrolled"})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	d := webhooks.NewTestDispatcher(st)
	if err := d.Emit(ctx, "device.enrolled", json.RawMessage(`{"id":"d1"}`)); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	deliveries, err := st.PendingDeliveries(ctx, sub.ID)
	if err != nil {
		t.Fatalf("PendingDeliveries: %v", err)
	}
	if len(deliveries) == 0 {
		t.Fatal("expected at least one delivery")
	}
}
