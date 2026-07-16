package sshrec_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/sshrec"
)

func openPGSSHRecStore(t *testing.T) *sshrec.SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("sshrec_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open: %v", err)
	}
	st, err := sshrec.Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("sshrec.Open: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS sshrec_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_SSHRec_Arm(t *testing.T) {
	st := openPGSSHRecStore(t)
	ctx := context.Background()

	if err := st.Arm(ctx, sshrec.ArmRequest{
		ULID:      "pg-ulid-001",
		AccountID: "pg-account-001",
		Reason:    "pg test",
		Window:    10 * time.Minute,
	}); err != nil {
		t.Fatalf("Arm: %v", err)
	}

	_, ok := st.GetArming(ctx, "pg-ulid-001")
	if !ok {
		t.Error("expected armed=true after Arm")
	}
}

func TestPG_SSHRec_InsertAndGetRequest(t *testing.T) {
	st := openPGSSHRecStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	req := sshrec.Request{
		ID:                "pg-req-001",
		ULID:              "pg-ulid-002",
		AccountID:         "pg-account-002",
		PublicKeySSH:      "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKeyForTestingOnly",
		State:             "pending",
		Approvals:         []sshrec.Approval{},
		RequiredApprovals: 1,
		SourceIP:          "10.0.0.1",
		CreatedAt:         now,
		ExpiresAt:         now.Add(10 * time.Minute),
	}
	if err := st.InsertRequest(ctx, req); err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}
	got, err := st.GetRequest(ctx, "pg-req-001")
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if got.State != "pending" {
		t.Errorf("state: %q, want 'pending'", got.State)
	}
	if got.AccountID != "pg-account-002" {
		t.Errorf("account_id: %q", got.AccountID)
	}
}

func TestPG_SSHRec_NextSerial(t *testing.T) {
	st := openPGSSHRecStore(t)
	ctx := context.Background()

	s1, err := st.NextSerial(ctx)
	if err != nil {
		t.Fatalf("NextSerial #1: %v", err)
	}
	s2, err := st.NextSerial(ctx)
	if err != nil {
		t.Fatalf("NextSerial #2: %v", err)
	}
	if s2 != s1+1 {
		t.Errorf("serial not monotonic: %d then %d", s1, s2)
	}
}

func TestPG_SSHRec_Audit(t *testing.T) {
	st := openPGSSHRecStore(t)
	ctx := context.Background()

	ulid := "pg-ulid-audit"
	_ = st.Audit(ctx, sshrec.AuditEvent{ULID: ulid, AccountID: "acc-pg", Action: "arm"})
	_ = st.Audit(ctx, sshrec.AuditEvent{ULID: ulid, AccountID: "acc-pg", Action: "request"})

	events, err := st.ListAudit(ctx, ulid, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 audit events, got %d", len(events))
	}
}
