//go:build linux

package main

// verity_epoch_test.go — the VERITY-02 gate's persistent epoch floor.
//
// verityEpochFloor used to read the floor by hand and return 0 on any error.
// Two defects sat in those few lines, and this file pins both:
//
//   - Nothing ever RAISED the floor, so it stayed at 0 for the life of the
//     device and issuing a release cert with a higher -min-epoch revoked nothing
//     on the pre-boot path. The raise now happens inside
//     verify.VerifySquashfsBeforePivot, which is why this file hands it the
//     STORE rather than a number read out of it.
//   - Returning 0 on error is not conservative. Floor 0 accepts every epoch any
//     root has ever retired. NewEpochStore CREATES the record at 0 when it is
//     merely absent, so the error path means the record is unreadable or corrupt
//     — which is exactly how an attacker with local write access would try to
//     reset the floor.

import (
	"os"
	"path/filepath"
	"testing"
)

// withEpochPath points the gate's floor at a temp file for the duration of a
// test, restoring the production path afterwards.
func withEpochPath(t *testing.T, path string) {
	t.Helper()
	prev := verityEpochPath
	verityEpochPath = path
	t.Cleanup(func() { verityEpochPath = prev })
}

// A brand-new device has no floor file at all. That is NOT the error path: the
// store creates the record at 0 and the boot proceeds.
func TestOpenVerityEpochStore_NewDeviceStartsAtZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "epoch-floor.json")
	withEpochPath(t, path)

	es, err := openVerityEpochStore()
	if err != nil {
		t.Fatalf("a device with no floor file must boot: %v", err)
	}
	if got := es.Current(); got != 0 {
		t.Fatalf("new device floor should be 0, got %d", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the record should have been created at %s: %v", path, err)
	}
}

// An existing floor is read, not reset.
func TestOpenVerityEpochStore_ReadsPersistedFloor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "epoch-floor.json")
	if err := os.WriteFile(path, []byte(`{"floor":9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	withEpochPath(t, path)

	es, err := openVerityEpochStore()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := es.Current(); got != 9 {
		t.Fatalf("floor should be 9, got %d", got)
	}
}

// The headline: a corrupt record must be an ERROR, so the caller halts the boot.
// Returning floor 0 here would silently accept every retired release key.
func TestOpenVerityEpochStore_CorruptRecordFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "epoch-floor.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	withEpochPath(t, path)

	es, err := openVerityEpochStore()
	if err == nil {
		t.Fatalf("a corrupt epoch record must fail closed, got a store at floor %d", es.Current())
	}
}
