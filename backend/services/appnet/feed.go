package appnet

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"vulos/backend/services/signing"
)

// feed.go — registry-as-feed, phase 1 (design note in roadmap/APP-STORE.md,
// "Forward plan: registry-as-feed (anti-rollback distribution)").
//
// registry.json today is signed and distributed as a single object: a box that
// only ever fetches "the current registry.json" has no built-in way to detect
// that it has been handed a stale, previously-superseded copy (a rollback to a
// version with since-patched or since-revoked entries). This file adds — QUITE
// DELIBERATELY ADDITIVE, not a replacement — a signed, append-only feed of
// registry.json publications, modeled on DMTAP-PUB's author feed (dmtap
// §22.4: FeedEntry / FeedHead, seq + prev-hash + signed head), re-expressed in
// plain JSON (not CBOR) to match this repo's existing registry.json shape,
// with sha256 in place of BLAKE3 (matching signing.go / the rest of this
// repo's hashing) and Ed25519 in place of the general dmtap signature suite.
//
// PHASE 1 SCOPE: publish only. This package can build and verify a feed, and
// cmd/sign gets `publish-feed` / `verify-feed` subcommands so a release can
// append an entry after `sign-registry`. Nothing here changes install-time
// verification (LoadRegistry / VerifyEntrySignature / resolveRegistryTrust are
// untouched) — a box that has never heard of the feed keeps working exactly as
// before. Consuming the feed for anti-rollback checks at install time is a
// later phase.
//
// Shape (mirrors dmtap §22.4.1's FeedEntry/FeedHead, JSON instead of CBOR):
//
//	FeedEntry{seq, registry_hash, prev, ts}  — registry_hash is this repo's
//	  analogue of dmtap's "announce": the content address (sha256) of the
//	  registry.json bytes published at this position. prev is the entry_id of
//	  the FeedEntry at seq-1; absent iff seq=0 (genesis), exactly as dmtap
//	  requires (ERR_PUB_FEED_CHAIN_BROKEN if violated).
//	FeedHead{v, seq, tip, ts, signer, sig} — the tip transitively commits to
//	  the whole chain via prev, so (as in dmtap) only the head is signed, never
//	  individual entries.

// FeedEntry is one append-only position in the registry publication feed.
// entry_id (the value a successor's Prev carries, and FeedHead.Tip) is
// entryID(e) below — the content address of the *complete* entry, not stored
// inside the entry itself (mirrors dmtap: a FeedEntry carries no signature or
// self-reference of its own).
type FeedEntry struct {
	Seq          uint64 `json:"seq"`            // strictly increasing, genesis = 0
	RegistryHash string `json:"registry_hash"`  // sha256:<hex> of registry.json at this position
	Prev         string `json:"prev,omitempty"` // entry_id of the FeedEntry at seq-1; absent iff seq=0
	Ts           string `json:"ts"`             // RFC3339 entry time
}

// FeedHead is the signed tip of the feed. Signing the head authenticates
// every entry reachable from it through the Prev chain (dmtap §22.4.1).
type FeedHead struct {
	V      int    `json:"v"`             // format version, = 0
	Seq    uint64 `json:"seq"`           // highest seq this head commits to (the tip)
	Tip    string `json:"tip"`           // entry_id of the FeedEntry at Seq
	Ts     string `json:"ts"`            // head publication time, RFC3339
	Signer string `json:"signer"`        // base64 Ed25519 public key that signed this head
	Sig    string `json:"sig,omitempty"` // base64 signature over Canonical(head with Sig="")
}

// Feed is the on-disk file: the full entry log plus its current signed head.
// Stored as one JSON file (registry-feed.json) alongside registry.json —
// simpler than dmtap's content-addressed object-per-entry model, appropriate
// for a single small registry with one publisher, and just as verifiable: the
// chain is walked and every hash checked exactly as a fetcher would.
type Feed struct {
	Entries []FeedEntry `json:"entries"`
	Head    FeedHead    `json:"head"`
}

// entryID computes the content address of a FeedEntry: sha256 of its
// canonical JSON encoding, hex-encoded with a "sha256:" prefix (matching the
// checksum convention already used in registry.json recipes). No DS-tag fold
// is needed here (unlike dmtap's multi-object-type wire format) because this
// file has exactly one object shape that ever gets hashed this way.
func entryID(e FeedEntry) (string, error) {
	data, err := signing.Canonical(e)
	if err != nil {
		return "", fmt.Errorf("appnet: canonical-encode feed entry: %w", err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// registryContentHash returns the sha256 content address ("sha256:<hex>") of
// the raw bytes at registryPath. SaveRegistry writes registry.json with
// encoding/json's map-key-sorted, indented output, so the same logical
// registry always produces the same bytes and therefore the same hash.
func registryContentHash(registryPath string) (string, error) {
	data, err := os.ReadFile(registryPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", registryPath, err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// LoadFeed reads a feed file. A missing file is NOT an error — it is the
// state of a feed that has never been published, i.e. an empty Feed ready to
// receive its genesis (seq=0) entry.
func LoadFeed(path string) (*Feed, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Feed{}, nil
		}
		return nil, fmt.Errorf("appnet: read feed %s: %w", path, err)
	}
	var f Feed
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("appnet: parse feed %s: %w", path, err)
	}
	return &f, nil
}

// SaveFeed writes a feed file, matching SaveRegistry's formatting.
func SaveFeed(path string, f *Feed) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("appnet: marshal feed: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

// AppendFeedEntry publishes the current state of registryPath as a new feed
// entry, signs a new head with priv, and writes feedPath. If the registry's
// content hash is unchanged since the last published entry, this is a no-op
// (idempotent re-publish — mirrors dmtap §22.4.2's "re-presenting the current
// tip must be idempotent, not an error") and the existing feed is returned
// untouched rather than growing the log for nothing.
func AppendFeedEntry(feedPath, registryPath string, pub ed25519.PublicKey, priv ed25519.PrivateKey) (*Feed, error) {
	feed, err := LoadFeed(feedPath)
	if err != nil {
		return nil, err
	}

	hash, err := registryContentHash(registryPath)
	if err != nil {
		return nil, err
	}

	if n := len(feed.Entries); n > 0 && feed.Entries[n-1].RegistryHash == hash {
		return feed, nil // nothing changed since the last publish
	}

	var (
		seq  uint64
		prev string
	)
	if n := len(feed.Entries); n > 0 {
		last := feed.Entries[n-1]
		seq = last.Seq + 1
		id, err := entryID(last)
		if err != nil {
			return nil, err
		}
		prev = id
	}

	entry := FeedEntry{
		Seq:          seq,
		RegistryHash: hash,
		Prev:         prev,
		Ts:           time.Now().UTC().Format(time.RFC3339),
	}
	feed.Entries = append(feed.Entries, entry)

	tip, err := entryID(entry)
	if err != nil {
		return nil, err
	}

	head := FeedHead{
		V:      0,
		Seq:    entry.Seq,
		Tip:    tip,
		Ts:     entry.Ts,
		Signer: base64.StdEncoding.EncodeToString(pub),
	}
	sigData, err := signing.Canonical(head) // head.Sig is empty at this point ⇒ excluded from the payload below
	if err != nil {
		return nil, fmt.Errorf("appnet: canonical-encode feed head: %w", err)
	}
	head.Sig = base64.StdEncoding.EncodeToString(signing.Sign(priv, sigData))
	feed.Head = head

	if err := SaveFeed(feedPath, feed); err != nil {
		return nil, err
	}
	return feed, nil
}

// VerifyFeed walks a feed end-to-end and reports the first problem found:
//
//   - each entry's computed entry_id must equal the next entry's Prev
//     (genesis, seq=0, must have an EMPTY Prev)
//   - the Head's Tip must equal the last entry's entry_id, and Head.Seq must
//     equal the last entry's Seq
//   - the Head's signature must verify against pub
//
// An empty feed (never published) verifies trivially (nil error).
func VerifyFeed(f *Feed, pub ed25519.PublicKey) error {
	if len(f.Entries) == 0 {
		return nil
	}

	for i, e := range f.Entries {
		if i == 0 {
			if e.Seq != 0 {
				return fmt.Errorf("appnet: feed: genesis entry has seq=%d, want 0", e.Seq)
			}
			if e.Prev != "" {
				return fmt.Errorf("appnet: feed: genesis entry (seq=0) must not carry a prev hash (ERR_PUB_FEED_CHAIN_BROKEN analogue)")
			}
			continue
		}
		prevEntry := f.Entries[i-1]
		if e.Seq != prevEntry.Seq+1 {
			return fmt.Errorf("appnet: feed: entry seq=%d does not follow seq=%d (must increase by exactly 1)", e.Seq, prevEntry.Seq)
		}
		wantPrev, err := entryID(prevEntry)
		if err != nil {
			return err
		}
		if e.Prev != wantPrev {
			return fmt.Errorf("appnet: feed: chain broken at seq=%d — prev=%q does not match entry_id(seq=%d)=%q (ERR_PUB_FEED_CHAIN_BROKEN analogue)",
				e.Seq, e.Prev, prevEntry.Seq, wantPrev)
		}
	}

	last := f.Entries[len(f.Entries)-1]
	lastID, err := entryID(last)
	if err != nil {
		return err
	}
	if f.Head.Seq != last.Seq {
		return fmt.Errorf("appnet: feed: head seq=%d does not match last entry seq=%d", f.Head.Seq, last.Seq)
	}
	if f.Head.Tip != lastID {
		return fmt.Errorf("appnet: feed: head tip=%q does not match entry_id of last entry=%q", f.Head.Tip, lastID)
	}

	sigB64 := f.Head.Sig
	unsigned := f.Head
	unsigned.Sig = ""
	payload, err := signing.Canonical(unsigned)
	if err != nil {
		return fmt.Errorf("appnet: canonical-encode feed head for verification: %w", err)
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return fmt.Errorf("appnet: feed head signature is not valid base64: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("appnet: feed head signature is %d bytes, want %d", len(sig), ed25519.SignatureSize)
	}
	if !signing.Verify(pub, payload, sig) {
		return fmt.Errorf("appnet: feed head signature verification failed (ERR_PUB_FEED_SIG_INVALID analogue)")
	}
	return nil
}
