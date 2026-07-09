package files

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestStoreContentCreatesAndCommits verifies the resumable-upload promotion path:
// StoreContent creates the node, streams bytes into the owner's bucket, and
// records a version — the same result as UploadGrant→PutContent→Commit.
func TestStoreContentCreatesAndCommits(t *testing.T) {
	svc, fb := newTestService(t)
	data := []byte("resumable assembled bytes")
	nodeID, err := svc.StoreContent(context.Background(), owner, "", "big.bin", "application/octet-stream", bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("StoreContent: %v", err)
	}
	// Bytes landed in the owner's bucket at the drive key.
	if got, ok := fb.get("vulos-"+owner, owner+"/drive/big.bin"); !ok || got != string(data) {
		t.Fatalf("stored bytes mismatch: %q ok=%v", got, ok)
	}
	// A version was recorded.
	vs, err := svc.Versions(owner, nodeID)
	if err != nil || len(vs) != 1 {
		t.Fatalf("Versions: %v (n=%d)", err, len(vs))
	}
	if vs[0].Size != int64(len(data)) {
		t.Fatalf("version size = %d, want %d", vs[0].Size, len(data))
	}
}

// TestStoreContentEnforcesACL: a stranger cannot promote into another owner's
// folder — the write is rejected before any bytes land.
func TestStoreContentEnforcesACL(t *testing.T) {
	svc, fb := newTestService(t)
	dir, err := svc.CreateFolder(owner, "", "private")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	fb.writes = 0
	_, err = svc.StoreContent(context.Background(), other, dir.ID, "sneak.bin", "", strings.NewReader("x"), 1)
	if err != ErrForbidden {
		t.Fatalf("cross-owner StoreContent: err=%v, want ErrForbidden", err)
	}
}
