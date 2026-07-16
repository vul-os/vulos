package mobilepush_test

import (
	"context"
	"os"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/mobilepush"
)

func openPGTestSubscriber(t *testing.T) *mobilepush.Subscriber {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	db, err := cpdb.Open("mobilepush_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	sub, err := mobilepush.OpenSubscriber(db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("mobilepush.OpenSubscriber (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS mobilepush_pgtest CASCADE`)
		_ = sub.Close()
	})
	return sub
}

func TestPG_RegisterAndList(t *testing.T) {
	sub := openPGTestSubscriber(t)
	ctx := context.Background()

	dt, err := sub.Register(ctx, "pg-acc-001", "pg-tok-apns-aaa", mobilepush.PlatformAPNs)
	if err != nil {
		t.Fatalf("Register apns: %v", err)
	}
	if dt.AccountID != "pg-acc-001" || dt.Platform != mobilepush.PlatformAPNs {
		t.Errorf("unexpected token: %+v", dt)
	}

	if _, err := sub.Register(ctx, "pg-acc-001", "pg-tok-fcm-bbb", mobilepush.PlatformFCM); err != nil {
		t.Fatalf("Register fcm: %v", err)
	}

	tokens, err := sub.ListTokens(ctx, "pg-acc-001")
	if err != nil {
		t.Fatalf("ListTokens: %v", err)
	}
	if len(tokens) != 2 {
		t.Errorf("expected 2 tokens, got %d", len(tokens))
	}
}

func TestPG_RegisterIdempotent(t *testing.T) {
	sub := openPGTestSubscriber(t)
	ctx := context.Background()

	if _, err := sub.Register(ctx, "pg-acc-002", "pg-tok-idem", mobilepush.PlatformAPNs); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if _, err := sub.Register(ctx, "pg-acc-002", "pg-tok-idem", mobilepush.PlatformAPNs); err != nil {
		t.Fatalf("re-register: %v", err)
	}
	tokens, err := sub.ListTokens(ctx, "pg-acc-002")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tokens) != 1 {
		t.Errorf("idempotent re-register created duplicate: got %d tokens", len(tokens))
	}
}

func TestPG_DeleteToken(t *testing.T) {
	sub := openPGTestSubscriber(t)
	ctx := context.Background()

	if _, err := sub.Register(ctx, "pg-acc-003", "pg-tok-del", mobilepush.PlatformFCM); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := sub.DeleteToken(ctx, "pg-acc-003", "pg-tok-del", mobilepush.PlatformFCM); err != nil {
		t.Fatalf("delete: %v", err)
	}
	tokens, err := sub.ListTokens(ctx, "pg-acc-003")
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(tokens) != 0 {
		t.Errorf("expected 0 tokens after delete, got %d", len(tokens))
	}
}

func TestPG_InvalidPlatform(t *testing.T) {
	sub := openPGTestSubscriber(t)
	ctx := context.Background()

	_, err := sub.Register(ctx, "pg-acc-004", "pg-tok-bad", "bad-platform")
	if err == nil {
		t.Fatal("expected error for invalid platform, got nil")
	}
}
