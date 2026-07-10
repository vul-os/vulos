// manager_test.go — UNIFIED-SIGNIN: async enrollment state machine.
package cloudenroll

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"
)

func TestManager_BeginReturnsUserCode_ThenApproves(t *testing.T) {
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	const account, ulid = "acct-mgr", "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	srv := enrollStub(t, caPriv, account, ulid)
	defer srv.Close()

	dir := t.TempDir()
	e := New(srv.URL, dir, xorSealer{})
	e.intervalOverride = 2 * time.Millisecond

	enrolledCh := make(chan string, 1)
	m := NewManager(e, func(id *Identity) { enrolledCh <- id.ULID })

	// Not enrolled yet.
	if u, _, err := m.Identity(); err != nil || u != "" {
		t.Fatalf("Identity before enroll: ulid=%q err=%v", u, err)
	}

	uc, uri, err := m.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if uc != "ABCD-1234" || uri == "" {
		t.Fatalf("Begin returned uc=%q uri=%q", uc, uri)
	}

	// A second Begin while pending joins the same grant (idempotent).
	uc2, _, err := m.Begin(context.Background())
	if err != nil || uc2 != uc {
		t.Fatalf("second Begin: uc=%q err=%v (want same code)", uc2, err)
	}

	// The stub approves on the second poll; wait for the state machine.
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := m.Status()
		if st.State == "approved" {
			if st.ULID != ulid {
				t.Fatalf("approved ULID = %q, want %q", st.ULID, ulid)
			}
			break
		}
		if st.State == "error" {
			t.Fatalf("enrollment errored: %s", st.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for approval; state=%q", st.State)
		}
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case enrolledULID := <-enrolledCh:
		if enrolledULID != ulid {
			t.Fatalf("OnEnrolled ULID = %q, want %q", enrolledULID, ulid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnEnrolled callback never fired")
	}
	if u, a, err := m.Identity(); err != nil || u != ulid || a != account {
		t.Fatalf("Identity after enroll: (%q,%q,%v)", u, a, err)
	}

	// Begin after enrollment refuses.
	if _, _, err := m.Begin(context.Background()); !errors.Is(err, ErrAlreadyEnrolled) {
		t.Fatalf("Begin after enroll: want ErrAlreadyEnrolled, got %v", err)
	}
}

func TestManager_IdentityLoadsFromDisk(t *testing.T) {
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	const account, ulid = "acct-disk", "01BX5ZZKBKACTAV9WEVGEMMVS0"
	srv := enrollStub(t, caPriv, account, ulid)
	defer srv.Close()

	dir := t.TempDir()
	e := New(srv.URL, dir, xorSealer{})
	e.intervalOverride = 2 * time.Millisecond
	if _, err := e.Enroll(context.Background(), nil); err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	// A FRESH manager over the same dir sees the persisted identity.
	m := NewManager(New(srv.URL, dir, xorSealer{}), nil)
	u, a, err := m.Identity()
	if err != nil || u != ulid || a != account {
		t.Fatalf("Identity from disk: (%q,%q,%v)", u, a, err)
	}
}

func TestManager_BeginSurfacesStartFailure(t *testing.T) {
	dir := t.TempDir()
	// Point at a dead endpoint: /enroll/start fails immediately.
	e := New("http://127.0.0.1:1", dir, xorSealer{})
	m := NewManager(e, nil)
	m.beginTimeout = 3 * time.Second

	if _, _, err := m.Begin(context.Background()); err == nil {
		t.Fatal("Begin against dead CP should fail")
	}
	if st := m.Status(); st.State != "error" {
		t.Fatalf("state = %q, want error", st.State)
	}
}
