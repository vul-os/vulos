package appnet

// feed_test.go — registry-as-feed phase 1 (see feed.go, roadmap/APP-STORE.md's
// "Forward plan: registry-as-feed" note). Pins the chain-integrity and
// signature checks a fetcher (or, in a later phase, an installing box) would
// rely on: genesis shape, monotonic seq + prev linkage, idempotent re-publish,
// and fail-closed detection of a broken chain or a tampered signature.

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
)

func writeRegistryFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestAppendFeedEntry_Genesis(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.json")
	feedPath := filepath.Join(dir, "registry-feed.json")
	writeRegistryFile(t, regPath, `{"apps":{}}`)

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	feed, err := AppendFeedEntry(feedPath, regPath, pub, priv)
	if err != nil {
		t.Fatalf("AppendFeedEntry: %v", err)
	}
	if len(feed.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(feed.Entries))
	}
	genesis := feed.Entries[0]
	if genesis.Seq != 0 {
		t.Errorf("genesis seq = %d, want 0", genesis.Seq)
	}
	if genesis.Prev != "" {
		t.Errorf("genesis prev = %q, want empty", genesis.Prev)
	}
	if feed.Head.Seq != 0 {
		t.Errorf("head seq = %d, want 0", feed.Head.Seq)
	}
	if feed.Head.Sig == "" {
		t.Fatal("head has no signature")
	}

	if err := VerifyFeed(feed, pub); err != nil {
		t.Fatalf("VerifyFeed on freshly-published genesis: %v", err)
	}

	// Round-trip through disk.
	loaded, err := LoadFeed(feedPath)
	if err != nil {
		t.Fatalf("LoadFeed: %v", err)
	}
	if err := VerifyFeed(loaded, pub); err != nil {
		t.Fatalf("VerifyFeed on reloaded feed: %v", err)
	}
}

func TestAppendFeedEntry_GrowsOnChange(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.json")
	feedPath := filepath.Join(dir, "registry-feed.json")
	pub, priv, _ := ed25519.GenerateKey(nil)

	writeRegistryFile(t, regPath, `{"apps":{}}`)
	if _, err := AppendFeedEntry(feedPath, regPath, pub, priv); err != nil {
		t.Fatalf("first publish: %v", err)
	}

	writeRegistryFile(t, regPath, `{"apps":{"cinny":{}}}`)
	feed, err := AppendFeedEntry(feedPath, regPath, pub, priv)
	if err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if len(feed.Entries) != 2 {
		t.Fatalf("expected 2 entries after a content change, got %d", len(feed.Entries))
	}
	if feed.Entries[1].Seq != 1 {
		t.Errorf("second entry seq = %d, want 1", feed.Entries[1].Seq)
	}
	if feed.Entries[1].Prev == "" {
		t.Error("second entry must carry a non-empty prev")
	}
	wantPrev, err := entryID(feed.Entries[0])
	if err != nil {
		t.Fatalf("entryID: %v", err)
	}
	if feed.Entries[1].Prev != wantPrev {
		t.Errorf("second entry prev = %q, want entry_id(genesis) = %q", feed.Entries[1].Prev, wantPrev)
	}
	if err := VerifyFeed(feed, pub); err != nil {
		t.Fatalf("VerifyFeed: %v", err)
	}
}

func TestAppendFeedEntry_NoOpWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.json")
	feedPath := filepath.Join(dir, "registry-feed.json")
	pub, priv, _ := ed25519.GenerateKey(nil)

	writeRegistryFile(t, regPath, `{"apps":{}}`)
	first, err := AppendFeedEntry(feedPath, regPath, pub, priv)
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}

	// Re-publish with byte-identical registry.json content: must not grow the log
	// (dmtap §22.4.2: re-presenting the current tip is idempotent, not an error).
	second, err := AppendFeedEntry(feedPath, regPath, pub, priv)
	if err != nil {
		t.Fatalf("second (no-op) publish: %v", err)
	}
	if len(second.Entries) != len(first.Entries) {
		t.Fatalf("no-op republish grew the log: %d -> %d entries", len(first.Entries), len(second.Entries))
	}
	if second.Head.Tip != first.Head.Tip {
		t.Errorf("no-op republish changed the tip: %q -> %q", first.Head.Tip, second.Head.Tip)
	}
}

func TestVerifyFeed_RejectsBrokenChain(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.json")
	feedPath := filepath.Join(dir, "registry-feed.json")
	pub, priv, _ := ed25519.GenerateKey(nil)

	writeRegistryFile(t, regPath, `{"apps":{}}`)
	AppendFeedEntry(feedPath, regPath, pub, priv)
	writeRegistryFile(t, regPath, `{"apps":{"cinny":{}}}`)
	feed, err := AppendFeedEntry(feedPath, regPath, pub, priv)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Tamper: corrupt the prev hash of the second entry.
	feed.Entries[1].Prev = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if err := VerifyFeed(feed, pub); err == nil {
		t.Fatal("expected VerifyFeed to reject a broken prev chain")
	}
}

func TestVerifyFeed_RejectsBadSequence(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.json")
	feedPath := filepath.Join(dir, "registry-feed.json")
	pub, priv, _ := ed25519.GenerateKey(nil)

	writeRegistryFile(t, regPath, `{"apps":{}}`)
	AppendFeedEntry(feedPath, regPath, pub, priv)
	writeRegistryFile(t, regPath, `{"apps":{"cinny":{}}}`)
	feed, _ := AppendFeedEntry(feedPath, regPath, pub, priv)

	feed.Entries[1].Seq = 5 // skip ahead — must not increase by exactly 1
	if err := VerifyFeed(feed, pub); err == nil {
		t.Fatal("expected VerifyFeed to reject a non-monotonic seq jump")
	}
}

func TestVerifyFeed_RejectsGenesisWithPrev(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.json")
	feedPath := filepath.Join(dir, "registry-feed.json")
	pub, priv, _ := ed25519.GenerateKey(nil)

	writeRegistryFile(t, regPath, `{"apps":{}}`)
	feed, _ := AppendFeedEntry(feedPath, regPath, pub, priv)

	feed.Entries[0].Prev = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	if err := VerifyFeed(feed, pub); err == nil {
		t.Fatal("expected VerifyFeed to reject a genesis entry carrying a prev hash")
	}
}

func TestVerifyFeed_RejectsTamperedSignature(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.json")
	feedPath := filepath.Join(dir, "registry-feed.json")
	pub, priv, _ := ed25519.GenerateKey(nil)

	writeRegistryFile(t, regPath, `{"apps":{}}`)
	feed, _ := AppendFeedEntry(feedPath, regPath, pub, priv)

	feed.Entries[0].RegistryHash = "sha256:deadbeef00000000000000000000000000000000000000000000000000000000"
	if err := VerifyFeed(feed, pub); err == nil {
		t.Fatal("expected VerifyFeed to reject a tampered entry whose hash the signed head no longer commits to")
	}
}

func TestVerifyFeed_RejectsWrongKey(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.json")
	feedPath := filepath.Join(dir, "registry-feed.json")
	pub, priv, _ := ed25519.GenerateKey(nil)
	otherPub, _, _ := ed25519.GenerateKey(nil)

	writeRegistryFile(t, regPath, `{"apps":{}}`)
	feed, _ := AppendFeedEntry(feedPath, regPath, pub, priv)

	if err := VerifyFeed(feed, otherPub); err == nil {
		t.Fatal("expected VerifyFeed to reject a head signed by a different key")
	}
}

func TestLoadFeed_MissingFileIsEmptyNotError(t *testing.T) {
	dir := t.TempDir()
	feed, err := LoadFeed(filepath.Join(dir, "does-not-exist.json"))
	if err != nil {
		t.Fatalf("LoadFeed on a missing file must not error, got: %v", err)
	}
	if len(feed.Entries) != 0 {
		t.Fatalf("expected an empty feed, got %d entries", len(feed.Entries))
	}
	if err := VerifyFeed(feed, ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))); err != nil {
		t.Fatalf("an empty feed must verify trivially, got: %v", err)
	}
}
