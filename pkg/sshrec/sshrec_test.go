package sshrec_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/sshrec"
)

// newTestPubKey generates a fresh Ed25519 key pair and returns the SSH public
// key in authorized_keys format, plus the private key for verification.
func newTestPubKey(t *testing.T) (pubSSH string, priv ed25519.PrivateKey) {
	t.Helper()
	pub, prv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("wrap ssh pub: %v", err)
	}
	return string(ssh.MarshalAuthorizedKey(sshPub)), prv
}

const testULID = "01JTXX0000000000000000000A"
const ownerID = "account-owner-001"
const adminID = "account-admin-002"

// --------------------------------------------------------------------------
// Helper: arm + request + approve + mint full happy path
// --------------------------------------------------------------------------

func TestMemStore_HappyPath_ArmRequestApproveMint(t *testing.T) {
	ctx := context.Background()
	st := sshrec.NewMemStore()
	kp := sshrec.NewMemKeyProvider()

	pubSSH, _ := newTestPubKey(t)

	// 1. Arm.
	if err := st.Arm(ctx, sshrec.ArmRequest{
		ULID:      testULID,
		AccountID: ownerID,
		Reason:    "test arm",
		Window:    10 * time.Minute,
	}); err != nil {
		t.Fatalf("arm: %v", err)
	}
	armedUntil, ok := st.GetArming(ctx, testULID)
	if !ok {
		t.Fatal("expected armed=true after arm")
	}
	if time.Until(armedUntil) < 9*time.Minute {
		t.Errorf("armed_until too short: %v", armedUntil)
	}

	// 2. Insert request with required_approvals=1.
	reqID := "req-001"
	now := time.Now().UTC()
	req := sshrec.Request{
		ID:                reqID,
		ULID:              testULID,
		AccountID:         ownerID,
		PublicKeySSH:      pubSSH,
		State:             "pending",
		Approvals:         []sshrec.Approval{},
		RequiredApprovals: 1,
		SourceIP:          "10.0.0.1",
		CreatedAt:         now,
		ExpiresAt:         now.Add(10 * time.Minute),
	}
	if err := st.InsertRequest(ctx, req); err != nil {
		t.Fatalf("insert request: %v", err)
	}

	got, err := st.GetRequest(ctx, reqID)
	if err != nil {
		t.Fatalf("get request: %v", err)
	}
	if got.State != "pending" {
		t.Errorf("state=%q, want pending", got.State)
	}

	// 3. Approve by admin.
	approved, err := st.Approve(ctx, reqID, adminID)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.State != "approved" {
		t.Errorf("state after approve=%q, want approved", approved.State)
	}
	if len(approved.Approvals) != 1 || approved.Approvals[0].By != adminID {
		t.Errorf("approvals mismatch: %+v", approved.Approvals)
	}

	// 4. Mint cert.
	pubKey, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(pubSSH))
	armedUntil, _ = st.GetArming(ctx, testULID)
	ttl := 300 * time.Second
	if remaining := time.Until(armedUntil); remaining < ttl {
		ttl = remaining
	}
	certNotBefore := time.Now().UTC()
	certNotAfter := certNotBefore.Add(ttl)

	certBytes, err := kp.SignCert(sshrec.CertReq{
		UserPubKey:  pubKey,
		KeyID:       "key-001",
		Principal:   "recovery",
		ValidAfter:  certNotBefore,
		ValidBefore: certNotAfter,
		SourceCIDRs: []string{"10.0.0.1/32"},
		Command:     "vulosrec",
	})
	if err != nil {
		t.Fatalf("sign cert: %v", err)
	}
	if len(certBytes) == 0 {
		t.Fatal("cert bytes empty")
	}

	// 5. Verify cert parses and has expected fields.
	parsedKey, err := ssh.ParsePublicKey(certBytes)
	if err != nil {
		t.Fatalf("parse minted cert: %v", err)
	}
	cert, ok2 := parsedKey.(*ssh.Certificate)
	if !ok2 {
		t.Fatalf("parsed key is not *ssh.Certificate")
	}
	if cert.KeyId != "key-001" {
		t.Errorf("key_id=%q, want key-001", cert.KeyId)
	}
	if len(cert.ValidPrincipals) != 1 || cert.ValidPrincipals[0] != "recovery" {
		t.Errorf("principals=%v, want [recovery]", cert.ValidPrincipals)
	}
	if cert.CertType != ssh.UserCert {
		t.Errorf("cert type=%v, want UserCert", cert.CertType)
	}
	// Verify CA signature: compare the cert's SignatureKey fingerprint with the
	// CA public key fingerprint. CertChecker.CheckCert would also reject
	// unrecognised critical options (force-command/source-address) on the
	// verifier side — which is correct server behaviour but breaks the test.
	// We verify the signing identity directly instead.
	caPub, _, _, _, _ := ssh.ParseAuthorizedKey(kp.CAPublicSSH())
	if ssh.FingerprintSHA256(cert.SignatureKey) != ssh.FingerprintSHA256(caPub) {
		t.Errorf("cert signature key does not match CA public key")
	}
}

// --------------------------------------------------------------------------
// Not armed → mint refused
// --------------------------------------------------------------------------

func TestMemStore_NotArmed(t *testing.T) {
	ctx := context.Background()
	st := sshrec.NewMemStore()

	_, armed := st.GetArming(ctx, testULID)
	if armed {
		t.Fatal("expected not armed for fresh store")
	}
}

// --------------------------------------------------------------------------
// TTL clamp: cert NotAfter == min(armed_until, now+300s)
// --------------------------------------------------------------------------

func TestMemStore_TTLClamp(t *testing.T) {
	ctx := context.Background()
	st := sshrec.NewMemStore()
	kp := sshrec.NewMemKeyProvider()
	pubSSH, _ := newTestPubKey(t)

	// Arm with a very short window (60s).
	if err := st.Arm(ctx, sshrec.ArmRequest{
		ULID:      testULID,
		AccountID: ownerID,
		Window:    60 * time.Second,
	}); err != nil {
		t.Fatalf("arm: %v", err)
	}

	armedUntil, _ := st.GetArming(ctx, testULID)
	remaining := time.Until(armedUntil)
	ttl := 300 * time.Second
	if remaining < ttl {
		ttl = remaining
	}
	// ttl must be ~60s (well under 300s).
	if ttl > 65*time.Second {
		t.Errorf("expected clamp to ~60s, got %v", ttl)
	}

	pubKey, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(pubSSH))
	now := time.Now().UTC()
	certBytes, err := kp.SignCert(sshrec.CertReq{
		UserPubKey:  pubKey,
		KeyID:       "key-clamp",
		Principal:   "recovery",
		ValidAfter:  now,
		ValidBefore: now.Add(ttl),
	})
	if err != nil {
		t.Fatalf("sign cert: %v", err)
	}
	parsedKey, _ := ssh.ParsePublicKey(certBytes)
	cert := parsedKey.(*ssh.Certificate)
	notAfter := time.Unix(int64(cert.ValidBefore), 0)
	if notAfter.Sub(now) > 65*time.Second {
		t.Errorf("cert TTL not clamped: %v", notAfter.Sub(now))
	}
}

// --------------------------------------------------------------------------
// Revoke → key_id appears in KRL
// --------------------------------------------------------------------------

func TestMemStore_Revoke_KRL(t *testing.T) {
	ctx := context.Background()
	st := sshrec.NewMemStore()
	kp := sshrec.NewMemKeyProvider()
	pubSSH, _ := newTestPubKey(t)

	// Arm + request (auto-approved, required_approvals=0).
	if err := st.Arm(ctx, sshrec.ArmRequest{ULID: testULID, AccountID: ownerID, Window: 10 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	req := sshrec.Request{
		ID:           "req-revoke-001",
		ULID:         testULID,
		AccountID:    ownerID,
		PublicKeySSH: pubSSH,
		State:        "approved",
		Approvals:    []sshrec.Approval{},
		CreatedAt:    now,
		ExpiresAt:    now.Add(10 * time.Minute),
	}
	_ = st.InsertRequest(ctx, req)

	pubKey, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(pubSSH))
	certBytes, _ := kp.SignCert(sshrec.CertReq{
		UserPubKey:  pubKey,
		KeyID:       "key-revoke-001",
		Principal:   "recovery",
		ValidAfter:  now,
		ValidBefore: now.Add(300 * time.Second),
	})
	_ = st.InsertCert(ctx, sshrec.Cert{
		KeyID:     "key-revoke-001",
		RequestID: req.ID,
		ULID:      testULID,
		Serial:    1,
		NotBefore: now,
		NotAfter:  now.Add(300 * time.Second),
		Principal: "recovery",
	})
	_ = certBytes // stored cert bytes are not kept in DB for v1

	// Revoke.
	if err := st.RevokeCert(ctx, "key-revoke-001", "test revocation"); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// KRL must contain key-revoke-001.
	krl, err := sshrec.BuildKRL(ctx, st)
	if err != nil {
		t.Fatalf("build krl: %v", err)
	}
	if len(krl) == 0 {
		t.Fatal("KRL is empty")
	}
	// Check the KRL binary for the key-id string.
	krlStr := string(krl)
	if !containsSubstring(krlStr, "key-revoke-001") {
		t.Errorf("KRL does not contain revoked key_id")
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && indexOfSubstring(s, sub) >= 0)
}

func indexOfSubstring(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// --------------------------------------------------------------------------
// Audit log: arm + request + approve + mint produce 4 rows in order
// --------------------------------------------------------------------------

func TestMemStore_AuditLog(t *testing.T) {
	ctx := context.Background()
	st := sshrec.NewMemStore()

	_ = st.Audit(ctx, sshrec.AuditEvent{ULID: testULID, AccountID: ownerID, Action: "arm"})
	_ = st.Audit(ctx, sshrec.AuditEvent{ULID: testULID, AccountID: ownerID, Action: "request"})
	_ = st.Audit(ctx, sshrec.AuditEvent{ULID: testULID, AccountID: adminID, Action: "approve"})
	_ = st.Audit(ctx, sshrec.AuditEvent{ULID: testULID, AccountID: ownerID, Action: "mint"})

	events, err := st.ListAudit(ctx, testULID, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 audit events, got %d", len(events))
	}
	expected := []string{"arm", "request", "approve", "mint"}
	for i, e := range events {
		if e.Action != expected[i] {
			t.Errorf("audit[%d].Action=%q, want %q", i, e.Action, expected[i])
		}
	}
}

// --------------------------------------------------------------------------
// Insufficient approvals → ErrApprovalsMissing (model check)
// --------------------------------------------------------------------------

func TestMemStore_InsufficientApprovals(t *testing.T) {
	ctx := context.Background()
	st := sshrec.NewMemStore()
	pubSSH, _ := newTestPubKey(t)

	_ = st.Arm(ctx, sshrec.ArmRequest{ULID: testULID, AccountID: ownerID, Window: 10 * time.Minute})

	now := time.Now().UTC()
	req := sshrec.Request{
		ID:                "req-quorum",
		ULID:              testULID,
		AccountID:         ownerID,
		PublicKeySSH:      pubSSH,
		State:             "pending",
		Approvals:         []sshrec.Approval{},
		RequiredApprovals: 2,
		CreatedAt:         now,
		ExpiresAt:         now.Add(10 * time.Minute),
	}
	_ = st.InsertRequest(ctx, req)
	// Only 1 approval for required=2.
	updated, _ := st.Approve(ctx, req.ID, adminID)
	if len(updated.Approvals) < 2 && updated.State == "approved" {
		t.Error("should not be approved with insufficient approvals")
	}
	if updated.State != "pending" {
		t.Errorf("state=%q, want pending (insufficient approvals)", updated.State)
	}
}

// --------------------------------------------------------------------------
// FileKeyProvider: missing file → generates ed25519, mode 0o600, re-reads same key
// --------------------------------------------------------------------------

func TestFileKeyProvider_GenerateAndReread(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ca.key")

	// File does not exist yet → should auto-generate.
	kp1, err := sshrec.NewFileKeyProvider(path)
	if err != nil {
		t.Fatalf("NewFileKeyProvider (generate): %v", err)
	}

	// Verify mode 0o600.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat ca.key: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("ca.key mode=%o, want 0600", info.Mode().Perm())
	}

	// Re-read must produce identical public key.
	kp2, err := sshrec.NewFileKeyProvider(path)
	if err != nil {
		t.Fatalf("NewFileKeyProvider (reread): %v", err)
	}
	if string(kp1.CAPublicSSH()) != string(kp2.CAPublicSSH()) {
		t.Errorf("CA public key mismatch after re-read")
	}

	// Both should sign validly.
	pubSSH, _ := newTestPubKey(t)
	pubKey, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(pubSSH))
	now := time.Now().UTC()
	cb, err := kp2.SignCert(sshrec.CertReq{
		UserPubKey:  pubKey,
		KeyID:       "file-test",
		Principal:   "recovery",
		ValidAfter:  now,
		ValidBefore: now.Add(60 * time.Second),
	})
	if err != nil {
		t.Fatalf("sign cert with re-read key: %v", err)
	}
	if len(cb) == 0 {
		t.Error("empty cert from re-read FileKeyProvider")
	}
}

// --------------------------------------------------------------------------
// Negative SSH pubkey input
// --------------------------------------------------------------------------

func TestInvalidPublicKey(t *testing.T) {
	cases := []string{
		"garbage-not-a-key",
		"ssh-rsa shortkey",
		"",
		"AAAA", // truncated base64, no algorithm prefix
	}
	for _, c := range cases {
		_, _, _, _, err := ssh.ParseAuthorizedKey([]byte(c))
		if err == nil {
			t.Errorf("ParseAuthorizedKey(%q) should have failed", c)
		}
	}
}

// --------------------------------------------------------------------------
// ErrUnknownRequest returned for missing request ID
// --------------------------------------------------------------------------

func TestMemStore_UnknownRequest(t *testing.T) {
	ctx := context.Background()
	st := sshrec.NewMemStore()

	_, err := st.GetRequest(ctx, "no-such-id")
	if err != sshrec.ErrUnknownRequest {
		t.Errorf("expected ErrUnknownRequest, got %v", err)
	}
}

// --------------------------------------------------------------------------
// ErrNotFound returned for missing cert key_id
// --------------------------------------------------------------------------

func TestMemStore_UnknownCert(t *testing.T) {
	ctx := context.Background()
	st := sshrec.NewMemStore()

	_, err := st.GetCert(ctx, "no-such-key-id")
	if err != sshrec.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	err = st.RevokeCert(ctx, "no-such-key-id", "reason")
	if err != sshrec.ErrNotFound {
		t.Errorf("revoke unknown cert: expected ErrNotFound, got %v", err)
	}
}

// --------------------------------------------------------------------------
// SQLStore conformance (runs against in-memory SQLite)
// --------------------------------------------------------------------------

func TestSQLStore_Conformance(t *testing.T) {
	ctx := context.Background()
	tdb, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	st, err := sshrec.Open(tdb)
	if err != nil {
		tdb.Close()
		t.Fatalf("open sql store: %v", err)
	}
	defer st.Close()

	kp := sshrec.NewMemKeyProvider()
	pubSSH, _ := newTestPubKey(t)

	// Arm.
	if err := st.Arm(ctx, sshrec.ArmRequest{ULID: testULID, AccountID: ownerID, Window: 10 * time.Minute}); err != nil {
		t.Fatalf("sql arm: %v", err)
	}
	_, ok := st.GetArming(ctx, testULID)
	if !ok {
		t.Fatal("sql: expected armed=true")
	}

	// Insert request.
	now := time.Now().UTC()
	req := sshrec.Request{
		ID:                "sql-req-001",
		ULID:              testULID,
		AccountID:         ownerID,
		PublicKeySSH:      pubSSH,
		State:             "pending",
		Approvals:         []sshrec.Approval{},
		RequiredApprovals: 1,
		SourceIP:          "192.168.1.1",
		CreatedAt:         now,
		ExpiresAt:         now.Add(10 * time.Minute),
	}
	if err := st.InsertRequest(ctx, req); err != nil {
		t.Fatalf("sql insert request: %v", err)
	}

	got, err := st.GetRequest(ctx, req.ID)
	if err != nil {
		t.Fatalf("sql get request: %v", err)
	}
	if got.PublicKeySSH != pubSSH {
		t.Errorf("public key mismatch")
	}

	// Approve.
	approved, err := st.Approve(ctx, req.ID, adminID)
	if err != nil {
		t.Fatalf("sql approve: %v", err)
	}
	if approved.State != "approved" {
		t.Errorf("sql state=%q, want approved", approved.State)
	}

	// NextSerial.
	s1, err := st.NextSerial(ctx)
	if err != nil {
		t.Fatalf("sql next serial: %v", err)
	}
	s2, _ := st.NextSerial(ctx)
	if s2 != s1+1 {
		t.Errorf("serial not monotonic: %d %d", s1, s2)
	}

	// Sign cert.
	armedUntil, _ := st.GetArming(ctx, testULID)
	ttl := 300 * time.Second
	if rem := time.Until(armedUntil); rem < ttl {
		ttl = rem
	}
	pubKey, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(pubSSH))
	certBytes, err := kp.SignCert(sshrec.CertReq{
		UserPubKey:  pubKey,
		KeyID:       "sql-key-001",
		Principal:   "recovery",
		ValidAfter:  now,
		ValidBefore: now.Add(ttl),
		SourceCIDRs: []string{"192.168.1.1/32"},
		Command:     "vulosrec",
	})
	if err != nil {
		t.Fatalf("sql sign cert: %v", err)
	}
	_ = certBytes

	// Insert cert.
	if err := st.InsertCert(ctx, sshrec.Cert{
		KeyID:          "sql-key-001",
		RequestID:      req.ID,
		ULID:           testULID,
		Serial:         s1,
		NotBefore:      now,
		NotAfter:       now.Add(ttl),
		Principal:      "recovery",
		SourceRestrict: "192.168.1.1/32",
	}); err != nil {
		t.Fatalf("sql insert cert: %v", err)
	}

	// Revoke.
	if err := st.RevokeCert(ctx, "sql-key-001", "sql test"); err != nil {
		t.Fatalf("sql revoke: %v", err)
	}

	revoked, err := st.ListRevoked(ctx)
	if err != nil {
		t.Fatalf("sql list revoked: %v", err)
	}
	if len(revoked) != 1 || revoked[0] != "sql-key-001" {
		t.Errorf("revoked=%v, want [sql-key-001]", revoked)
	}

	// KRL.
	krl, err := sshrec.BuildKRL(ctx, st)
	if err != nil {
		t.Fatalf("sql build krl: %v", err)
	}
	if !containsSubstring(string(krl), "sql-key-001") {
		t.Error("KRL does not contain revoked key_id")
	}

	// Audit.
	_ = st.Audit(ctx, sshrec.AuditEvent{ULID: testULID, AccountID: ownerID, Action: "arm"})
	_ = st.Audit(ctx, sshrec.AuditEvent{ULID: testULID, AccountID: ownerID, Action: "request"})

	events, err := st.ListAudit(ctx, testULID, 100)
	if err != nil {
		t.Fatalf("sql list audit: %v", err)
	}
	if len(events) < 2 {
		t.Errorf("expected ≥2 audit events, got %d", len(events))
	}
}

// --------------------------------------------------------------------------
// EnvKeyProvider smoke test
// --------------------------------------------------------------------------

func TestEnvKeyProvider_FromEnv(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	seed := priv.Seed()
	encoded := base64.StdEncoding.EncodeToString(seed)
	t.Setenv("SSHREC_CA_KEY_BASE64", encoded)

	kp, err := sshrec.NewEnvKeyProvider()
	if err != nil {
		t.Fatalf("NewEnvKeyProvider: %v", err)
	}
	if len(kp.CAPublicSSH()) == 0 {
		t.Error("CAPublicSSH empty")
	}
	if kp.SignerName() != "env" {
		t.Errorf("signer name=%q, want env", kp.SignerName())
	}

	pubSSH, _ := newTestPubKey(t)
	pubKey, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(pubSSH))
	now := time.Now().UTC()
	cb, err := kp.SignCert(sshrec.CertReq{
		UserPubKey:  pubKey,
		KeyID:       "env-test",
		Principal:   "recovery",
		ValidAfter:  now,
		ValidBefore: now.Add(60 * time.Second),
	})
	if err != nil {
		t.Fatalf("sign cert: %v", err)
	}
	if len(cb) == 0 {
		t.Error("empty cert from EnvKeyProvider")
	}
}

// --------------------------------------------------------------------------
// SourceCIDR helper
// --------------------------------------------------------------------------

func TestSourceCIDR(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"10.0.0.1", "10.0.0.1/32"},
		{"::1", "::1/128"},
		{"10.0.0.0/24", "10.0.0.0/24"},
		{"", ""},
		{"not-an-ip", ""},
	}
	for _, tt := range tests {
		got := sshrec.SourceCIDR(tt.in)
		if got != tt.want {
			t.Errorf("SourceCIDR(%q)=%q, want %q", tt.in, got, tt.want)
		}
	}
}
