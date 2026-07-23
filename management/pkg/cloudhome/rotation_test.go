// rotation_test.go — KEK rotation round-trip + key-access audit emission.
package cloudhome

import (
	"context"
	"sync"
	"testing"
)

// recorderAuditor captures KeyAuditor calls.
type recorderAuditor struct {
	mu     sync.Mutex
	events []auditEvent
}

type auditEvent struct {
	Op        string
	AccountID string
	VulaID    string
	Detail    map[string]string
}

func (r *recorderAuditor) RecordKeyAccess(_ context.Context, op, accountID, vulaID string, detail map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, auditEvent{Op: op, AccountID: accountID, VulaID: vulaID, Detail: detail})
}

func (r *recorderAuditor) opCount(op string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.Op == op {
			n++
		}
	}
	return n
}

func kekOf(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

func mustService(t *testing.T, store Store, cur []byte, version int, prior map[int][]byte, aud KeyAuditor) *Service {
	t.Helper()
	svc, err := NewService(Config{
		Store:      store,
		KEK:        cur,
		KEKVersion: version,
		PriorKEKs:  prior,
		Server:     "https://cell.vulos.org",
		Account:    fakeAccounts{"bob@vulos.org": "acct-bob"},
		Auditor:    aud,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

// TestKEKRotationRoundTrip: a key provisioned under v1 is re-sealed under v2 by
// RotateKEK; afterwards a service holding ONLY v2 can still sign for it, while a
// service holding only v1 cannot (proving the ciphertext was actually re-keyed).
func TestKEKRotationRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	k1, k2 := kekOf(0x11), kekOf(0x22)

	svc := mustService(t, store, k1, 1, nil, nil)
	id, err := svc.Provision(ctx, "acct-bob")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	// Sign before rotation works.
	if _, err := svc.SignForVulaID(ctx, id.VulaID, []byte("msg")); err != nil {
		t.Fatalf("sign v1: %v", err)
	}

	n, err := svc.RotateKEK(ctx, k2, 2)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if n != 1 {
		t.Fatalf("rotate count: want 1, got %d", n)
	}
	// The persisted row is now version 2.
	r, _ := store.GetByVulaID(ctx, id.VulaID)
	if r.KEKVersion != 2 {
		t.Fatalf("row kek_version: want 2, got %d", r.KEKVersion)
	}
	// Rotation is idempotent: a second rotate re-seals nothing.
	if n2, _ := svc.RotateKEK(ctx, k2, 2); n2 != 0 {
		t.Fatalf("idempotent rotate: want 0, got %d", n2)
	}

	// A FRESH service holding only v2 signs the rotated record fine.
	v2only := mustService(t, store, k2, 2, nil, nil)
	if _, err := v2only.SignForVulaID(ctx, id.VulaID, []byte("msg")); err != nil {
		t.Fatalf("sign with v2-only after rotation: %v", err)
	}
	// A service holding only v1 CANNOT decrypt the re-keyed record (fail closed).
	v1only := mustService(t, store, k1, 1, nil, nil)
	if _, err := v1only.SignForVulaID(ctx, id.VulaID, []byte("msg")); err == nil {
		t.Fatal("v1-only service must NOT be able to sign a v2-sealed record")
	}
}

// TestKeyAccessAudited: every sign and rotate emits a key-access audit event.
func TestKeyAccessAudited(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	aud := &recorderAuditor{}
	svc := mustService(t, store, kekOf(0x11), 1, nil, aud)

	id, err := svc.Provision(ctx, "acct-bob")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if _, err := svc.SignForVulaID(ctx, id.VulaID, []byte("m1")); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := svc.SignForAccount(ctx, "acct-bob", []byte("m2")); err != nil {
		t.Fatalf("sign acct: %v", err)
	}
	if c := aud.opCount("sign"); c != 2 {
		t.Fatalf("sign audit events: want 2, got %d", c)
	}

	if _, err := svc.RotateKEK(ctx, kekOf(0x22), 2); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if c := aud.opCount("rotate"); c != 1 {
		t.Fatalf("rotate audit events: want 1, got %d", c)
	}
	// The rotate event carries from/to versions and binds to the account+vulaID.
	for _, e := range aud.events {
		if e.Op == "rotate" {
			if e.AccountID != "acct-bob" || e.VulaID != id.VulaID {
				t.Fatalf("rotate event not bound to subject: %+v", e)
			}
			if e.Detail["from_version"] != "1" || e.Detail["to_version"] != "2" {
				t.Fatalf("rotate event missing version detail: %+v", e.Detail)
			}
		}
	}
}
