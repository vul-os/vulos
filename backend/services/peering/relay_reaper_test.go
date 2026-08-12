// relay_reaper_test.go — the background blob reaper must actually run and
// actually delete expired blobs. Regression guard: RelayStore.Start was
// constructed-but-never-called in cmd/server, so reapLoop never launched and
// expired relay blobs accumulated on disk without bound.
package peering

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeRelayBlobAt plants a blob file directly in the store with an explicit
// expiry, bypassing the deposit path so the test can create already-expired
// state without waiting.
func writeRelayBlobAt(t *testing.T, rs *RelayStore, recipient, blobID string, expiresAt time.Time) string {
	t.Helper()
	dir := filepath.Join(rs.storeDir, sanitizePath(recipient))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	blob := RelayBlob{
		ID:               blobID,
		RecipientVulosID: recipient,
		SenderVulosID:    "sender",
		BlobB64:          base64.StdEncoding.EncodeToString([]byte("ciphertext")),
		BlobSize:         int64(len("ciphertext")),
		DepositedAt:      expiresAt.Add(-time.Hour),
		ExpiresAt:        expiresAt,
	}
	data, err := json.Marshal(blob)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	path := filepath.Join(dir, sanitizePath(blobID)+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// TestRelayReaper_RemovesExpiredKeepsLive is the core contract: expired blobs
// are deleted, unexpired ones survive.
func TestRelayReaper_RemovesExpiredKeepsLive(t *testing.T) {
	rs, _, _ := relayTestSetup(t)

	now := time.Now().UTC()
	expired := writeRelayBlobAt(t, rs, "recipient-a", "blob-expired", now.Add(-time.Minute))
	live := writeRelayBlobAt(t, rs, "recipient-a", "blob-live", now.Add(time.Hour))
	// A second recipient directory: the reaper must walk all of them.
	expired2 := writeRelayBlobAt(t, rs, "recipient-b", "blob-expired-2", now.Add(-24*time.Hour))

	rs.reapExpired()

	for _, p := range []string{expired, expired2} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expired blob still present after reap: %s (err=%v)", p, err)
		}
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("unexpired blob was wrongly reaped: %s: %v", live, err)
	}
}

// TestRelayReaper_StartSweepsImmediately proves Start actually launches the
// reaper and that it sweeps on startup rather than only after the first
// 15-minute tick (a box restarted more often than that would never reap).
func TestRelayReaper_StartSweepsImmediately(t *testing.T) {
	rs, _, _ := relayTestSetup(t)

	expired := writeRelayBlobAt(t, rs, "recipient-a", "blob-expired", time.Now().UTC().Add(-time.Minute))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rs.Start(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(expired); os.IsNotExist(err) {
			return // reaped
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Start did not reap the expired blob within 5s: %s", expired)
}

// TestRelayReaper_StopsOnContextCancel guards against a goroutine leak: the
// reaper must exit when its context is cancelled at shutdown.
func TestRelayReaper_StopsOnContextCancel(t *testing.T) {
	rs, _, _ := relayTestSetup(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		rs.reapLoop(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reapLoop did not exit after context cancellation (goroutine leak)")
	}
}
