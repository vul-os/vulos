package enroll_test

import (
	"os"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/enroll"
)

func openPGTestStore(t *testing.T) *enroll.SQLStore {
	t.Helper()
	dsn := os.Getenv("VULOS_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set VULOS_TEST_POSTGRES to run Postgres integration tests")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("VULOS_DATABASE_URL", "")
	db, err := cpdb.Open("enroll_pgtest")
	if err != nil {
		t.Fatalf("cpdb.Open (postgres): %v", err)
	}
	st, err := enroll.OpenSQLStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("enroll.OpenSQLStore (postgres): %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP SCHEMA IF EXISTS enroll_pgtest CASCADE`)
		_ = st.Close()
	})
	return st
}

func TestPG_EnrollHappyPath(t *testing.T) {
	st := openPGTestStore(t)
	// Run conformance: start → approve → poll
	ctx := t.Context()
	signer := enroll.NewMemCertSigner()
	pubKey := signer.PublicKeyBytes()
	g := enroll.EnrollGrant{DeviceCode: "dc_pg_1", UserCode: "UC-PG1", ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.StartGrant(ctx, g, pubKey); err != nil {
		t.Fatalf("StartGrant: %v", err)
	}
	if _, err := st.PollGrant(ctx, g.DeviceCode); err != enroll.ErrAuthPending {
		t.Errorf("want ErrAuthPending, got %v", err)
	}
	cert, _ := signer.SignDeviceCert(pubKey, "acc1", "01ULID00000000000000000001")
	if err := st.ApproveGrant(ctx, g.UserCode, "acc1", "01ULID00000000000000000001", cert); err != nil {
		t.Fatalf("ApproveGrant: %v", err)
	}
	res, err := st.PollGrant(ctx, g.DeviceCode)
	if err != nil {
		t.Fatalf("PollGrant after approve: %v", err)
	}
	if res.ULID != "01ULID00000000000000000001" {
		t.Errorf("ULID mismatch: %v", res.ULID)
	}
}
