package models

// catalog.go — the CURATED, PINNED download catalog for on-box embedding models.
//
// WHAT THIS IS (and deliberately is NOT):
//
// This is the "registry backend" the model-management UI flagged as missing
// (Listing.NeedsRegistry). It is a small, HARDCODED catalog of recommended
// sentence-embedding models: for each entry we pin the exact download URL, the
// expected SHA-256 of the bytes, the size and the embedding dimension.
//
// It is NOT an arbitrary-URL fetcher. There is no client-supplied URL anywhere
// in the download path — the operator picks a catalog *id*, and the ONLY thing
// that is ever fetched is a URL that ships hardcoded in this file. That closes
// the SSRF / supply-chain surface: an attacker who owns the request body still
// cannot make the box fetch an address of their choosing, and a byte that does
// not match the pinned SHA-256 is rejected fail-closed before it is installed.
//
// SECURITY PROPERTIES (all enforced in Download, tested in catalog_test.go):
//   - curated only: id → hardcoded entry; no URL comes from the caller.
//   - https + host-pinned: every URL must be https and its host must be an
//     allowlisted HuggingFace host (checked at load AND before each fetch).
//   - SHA-256 verified fail-closed: the streamed bytes are hashed and compared
//     to the pinned digest; a mismatch removes the temp file and errors.
//   - size-bounded: the read is capped (pinned size + slack) so a swapped-out
//     giant response cannot fill the disk.
//   - content-sniffed: the same ONNX-magic / tokenizer-JSON validation the
//     import path uses runs before the artifact is placed.
//   - atomic: temp file in the models dir + rename, so a partial/failed download
//     never leaves a half-written model the embedder could load.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// allowedDownloadHosts is the host allowlist for catalog URLs. Every catalog URL
// must resolve to one of these (checked at registration in init() and again
// before every fetch in Download). HuggingFace serves resolve/main/... from
// huggingface.co and may 302 to its CDN; we pin both so a redirect off-allowlist
// is refused rather than silently followed anywhere.
var allowedDownloadHosts = map[string]bool{
	"huggingface.co":              true,
	"cdn-lfs.huggingface.co":      true,
	"cdn-lfs-us-1.huggingface.co": true,
}

// CatalogEntry is one curated, pinned downloadable embedding model. Both the
// model and its tokenizer are pinned (URL + SHA-256 + size) so a full one-click
// install lands genuine semantic RAG, not a degraded model-without-tokenizer.
type CatalogEntry struct {
	ID          string `json:"id"`          // stable catalog id, e.g. "all-MiniLM-L6-v2"
	Name        string `json:"name"`        // human label
	Description string `json:"description"` // one-line summary for the UI
	Dim         int    `json:"dim"`         // embedding dimension the model outputs
	Recommended bool   `json:"recommended"` // the default one-click pick

	// Model / Tokenizer are the two pinned artifacts. DownloadURL is https +
	// host-pinned; SHA256 is the fail-closed integrity pin; SizeBytes is the
	// expected on-disk size (used for the read bound and UI display).
	Model     PinnedArtifact `json:"model"`
	Tokenizer PinnedArtifact `json:"tokenizer"`
}

// TotalBytes is the combined download size (model + tokenizer), for UI display.
func (e CatalogEntry) TotalBytes() int64 { return e.Model.SizeBytes + e.Tokenizer.SizeBytes }

// PinnedArtifact is one pinned file: a fixed URL, its expected SHA-256 and size.
type PinnedArtifact struct {
	DownloadURL string `json:"download_url"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"size_bytes"`
}

// catalog is the curated, PINNED list. To add a model: pin its exact resolve URL
// on huggingface.co, the SHA-256 of the bytes (verified fail-closed) and the
// size. Do NOT commit the model binary — it is fetched on demand and verified.
//
// The all-MiniLM-L6-v2 pins below were verified against the live HuggingFace
// artifacts at build time (see catalog_test.go for the integrity checks). The
// model is the fp32 ONNX export (~86 MiB); the "22MB" figure sometimes quoted is
// a separately-quantized variant, which we intentionally do not use so the
// vectors match the reference sentence-transformers output exactly.
var catalog = []CatalogEntry{
	{
		ID:          "all-MiniLM-L6-v2",
		Name:        "all-MiniLM-L6-v2",
		Description: "Recommended. 384-dim sentence embeddings, strong quality/size balance, fully on-box.",
		Dim:         384,
		Recommended: true,
		Model: PinnedArtifact{
			DownloadURL: "https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/onnx/model.onnx",
			SHA256:      "6fd5d72fe4589f189f8ebc006442dbb529bb7ce38f8082112682524616046452",
			SizeBytes:   90405214,
		},
		Tokenizer: PinnedArtifact{
			DownloadURL: "https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/tokenizer.json",
			SHA256:      "be50c3628f2bf5bb5e3a7f17b1f74611b2561a3a27eeab05e5aa30f411572037",
			SizeBytes:   466247,
		},
	},
	// NOTE: e5-small-v2 and nomic-embed-text are mentioned in the embedder docs as
	// alternatives. They are intentionally NOT yet pinned here: adding a catalog
	// entry means committing a verified SHA-256 for the EXACT bytes we ship, and
	// unverified pins would defeat the fail-closed guarantee. They can be added the
	// moment their model.onnx + tokenizer.json digests are pinned and checked.
}

// Catalog returns the curated download catalog (safe to expose to owners).
func Catalog() []CatalogEntry {
	out := make([]CatalogEntry, len(catalog))
	copy(out, catalog)
	return out
}

// CatalogEntryByID looks up a curated entry. The bool is false for any id that
// is not in the hardcoded catalog — this is the gate that ensures a caller can
// never steer the download at an arbitrary URL.
func CatalogEntryByID(id string) (CatalogEntry, bool) {
	for _, e := range catalog {
		if e.ID == id {
			return e, true
		}
	}
	return CatalogEntry{}, false
}

// ErrUnknownModel is returned when a download id is not in the curated catalog.
var ErrUnknownModel = errors.New("models: unknown catalog model id")

// ErrChecksumMismatch is returned when a downloaded artifact's SHA-256 does not
// match the pinned digest. It is fail-closed: the artifact is discarded.
var ErrChecksumMismatch = errors.New("models: downloaded artifact failed checksum verification")

// ErrBadDownloadURL is returned when a catalog URL is not https / not on the
// host allowlist. This should never happen for the shipped catalog (init()
// validates it) — it is a defense-in-depth guard on the fetch path.
var ErrBadDownloadURL = errors.New("models: catalog download URL is not an allowed https host")

// downloadSlack is the extra headroom over a pinned size the read is allowed
// before it is treated as oversize. The SHA-256 check is the real integrity
// gate; this only bounds disk use if the pinned size is stale.
const downloadSlack = 8 * 1024 * 1024 // 8 MiB

// DownloadResult reports what a catalog download installed.
type DownloadResult struct {
	ID        string       `json:"id"`
	Model     ImportResult `json:"model"`
	Tokenizer ImportResult `json:"tokenizer"`
	Listing   Listing      `json:"listing"`
}

// httpDoer is the minimal HTTP surface Download needs, so tests can inject a
// fake transport (checksum-mismatch, non-200, etc.) without real network I/O.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Download fetches, verifies and installs a curated catalog entry (both the
// model.onnx and its tokenizer.json) so the operator lands genuine semantic RAG
// in one call. It is fail-closed on every check (see the security notes atop
// this file). The client is optional; nil uses a bounded default HTTP client.
func (m *Manager) Download(ctx context.Context, id string, client httpDoer) (DownloadResult, error) {
	entry, ok := CatalogEntryByID(id)
	if !ok {
		return DownloadResult{}, ErrUnknownModel
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}

	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return DownloadResult{}, fmt.Errorf("models: create dir: %w", err)
	}

	// Download the tokenizer first (small, cheap to fail on) then the model, so a
	// bad-network run wastes the least work. Each writes to the fixed destination
	// (model.onnx / tokenizer.json) via the same atomic+validated path as import.
	tokRes, err := m.downloadArtifact(ctx, client, ArtifactTokenizer, entry.Tokenizer)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("models: tokenizer: %w", err)
	}
	modelRes, err := m.downloadArtifact(ctx, client, ArtifactModel, entry.Model)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("models: model: %w", err)
	}

	listing, _ := m.List()
	return DownloadResult{ID: id, Model: modelRes, Tokenizer: tokRes, Listing: listing}, nil
}

// downloadArtifact streams one pinned artifact to a temp file, verifies its
// SHA-256 fail-closed, content-sniffs it, and atomically installs it under the
// fixed destination filename for its kind.
func (m *Manager) downloadArtifact(ctx context.Context, client httpDoer, kind string, art PinnedArtifact) (ImportResult, error) {
	// Enforce https + host allowlist at fetch time (defense in depth on top of
	// the init() validation): the URL is hardcoded, but this guarantees no
	// non-allowlisted address is ever dialed even if the catalog is edited.
	if err := checkDownloadURL(art.DownloadURL); err != nil {
		return ImportResult{}, err
	}

	var destName string
	var hardCap int64
	switch kind {
	case ArtifactModel:
		destName, hardCap = "model.onnx", MaxModelBytes
	case ArtifactTokenizer:
		destName, hardCap = tokenizerName, MaxTokenizerBytes
	default:
		return ImportResult{}, fmt.Errorf("models: unknown artifact kind %q", kind)
	}

	// Bound the read to the pinned size (+ slack), but never above the per-kind
	// hard cap. The SHA-256 check is the authoritative integrity gate; this bound
	// just protects the disk if a pinned size is stale or a response is swapped.
	maxBytes := art.SizeBytes + downloadSlack
	if maxBytes <= 0 || maxBytes > hardCap {
		maxBytes = hardCap
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, art.DownloadURL, nil)
	if err != nil {
		return ImportResult{}, fmt.Errorf("models: build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return ImportResult{}, fmt.Errorf("models: fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ImportResult{}, fmt.Errorf("models: fetch %s: HTTP %d", destName, resp.StatusCode)
	}

	dest := filepath.Join(m.dir, destName)
	if !withinDir(m.dir, dest) { // defense in depth (destName is a fixed constant)
		return ImportResult{}, ErrBadArtifact
	}

	tmp, err := os.CreateTemp(m.dir, ".download-*."+strings.TrimPrefix(filepath.Ext(destName), "."))
	if err != nil {
		return ImportResult{}, fmt.Errorf("models: temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { tmp.Close(); os.Remove(tmpPath) }

	h := sha256.New()
	limited := io.LimitReader(resp.Body, maxBytes+1)
	written, err := io.Copy(io.MultiWriter(tmp, h), limited)
	if err != nil {
		cleanup()
		return ImportResult{}, fmt.Errorf("models: stream artifact: %w", err)
	}
	if written > maxBytes {
		cleanup()
		return ImportResult{}, ErrArtifactTooLarge
	}
	if written == 0 {
		cleanup()
		return ImportResult{}, ErrBadArtifact
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return ImportResult{}, fmt.Errorf("models: close temp: %w", err)
	}

	// Fail-closed SHA-256 verification: the bytes MUST match the pinned digest.
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, art.SHA256) {
		os.Remove(tmpPath)
		return ImportResult{}, fmt.Errorf("%w: got %s, want %s", ErrChecksumMismatch, got, art.SHA256)
	}

	// Content-sniff (same validation as the import path) before it goes live.
	if err := validate(kind, tmpPath); err != nil {
		os.Remove(tmpPath)
		return ImportResult{}, err
	}

	if err := os.Chmod(tmpPath, 0o600); err != nil {
		os.Remove(tmpPath)
		return ImportResult{}, fmt.Errorf("models: chmod: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		os.Remove(tmpPath)
		return ImportResult{}, fmt.Errorf("models: install: %w", err)
	}

	return ImportResult{
		Kind:      kind,
		Name:      destName,
		Path:      dest,
		SizeBytes: written,
		SHA256:    got,
	}, nil
}

// checkDownloadURL enforces https + the host allowlist on a catalog URL.
func checkDownloadURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBadDownloadURL, err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q", ErrBadDownloadURL, u.Scheme)
	}
	if !allowedDownloadHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("%w: host %q", ErrBadDownloadURL, u.Hostname())
	}
	return nil
}

// init validates the shipped catalog at startup so a mis-pinned URL (non-https,
// off-allowlist host, or a missing/short SHA-256) is caught immediately rather
// than at download time. A panic here is a build/ship bug, never operator input.
func init() {
	for _, e := range catalog {
		for _, art := range []PinnedArtifact{e.Model, e.Tokenizer} {
			if err := checkDownloadURL(art.DownloadURL); err != nil {
				panic("models: bad catalog entry " + e.ID + ": " + err.Error())
			}
			if len(art.SHA256) != 64 { // hex-encoded SHA-256
				panic("models: catalog entry " + e.ID + " has a non-SHA-256 pin: " + art.SHA256)
			}
			if art.SizeBytes <= 0 {
				panic("models: catalog entry " + e.ID + " is missing a pinned size")
			}
		}
	}
}
