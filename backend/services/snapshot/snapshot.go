package snapshot

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"vulos/backend/internal/ulid"
)

// Snapshot kinds recorded on the index.
const (
	KindManual     = "manual"
	KindScheduled  = "scheduled"
	KindPreRestore = "pre-restore"
)

// artifactMarker is the sub-prefix (relative to the data prefix) under which
// snapshot artifacts live. It is EXCLUDED from capture so snapshots never
// snapshot themselves.
const artifactMarker = "_snapshots/"

// ManifestEntry describes one live object captured by a snapshot. Key is the
// object key RELATIVE to the snapshot's data prefix (no leading slash). SHA256
// is the lowercase-hex content hash and doubles as the blob id.
type ManifestEntry struct {
	Key    string `json:"key"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest is the full object list for one snapshot. It is stored gzip-
// compressed; every snapshot carries a complete list so it is independently
// restorable even though blob DATA is shared across snapshots.
type Manifest struct {
	SnapshotID   string          `json:"snapshot_id"`
	SourcePrefix string          `json:"source_prefix"`
	Entries      []ManifestEntry `json:"entries"`
}

// Index is the small metadata record for one snapshot (stored uncompressed so
// listing is cheap).
type Index struct {
	ID             string    `json:"id"`
	CreatedAt      time.Time `json:"created_at"`
	ParentID       string    `json:"parent_id,omitempty"`
	Kind           string    `json:"kind"`
	SourcePrefix   string    `json:"source_prefix"`
	ObjectCount    int       `json:"object_count"`
	LogicalBytes   int64     `json:"logical_bytes"`    // sum of live object sizes
	BlobBytesAdded int64     `json:"blob_bytes_added"` // compressed bytes newly uploaded
	ManifestKey    string    `json:"manifest_key"`
	ManifestSHA256 string    `json:"manifest_sha256"` // hash of the stored (gzipped) manifest
}

// Config configures a Snapshotter.
type Config struct {
	// DataPrefix is the key prefix under which the box's live data lives in the
	// bucket (e.g. "os/"). Normalised to a single trailing slash.
	DataPrefix string
	// AccountID is the billing account the box belongs to (for metering). May be
	// empty on a standalone box.
	AccountID string
}

// Snapshotter creates, lists, restores and prunes snapshots for one box's
// (bucket, data-prefix). All artifacts live within that same bucket+prefix, so a
// snapshotter can never touch another account's or box's data.
type Snapshotter struct {
	store     ObjectStore
	dataPfx   string // normalised, trailing slash (may be "")
	snapPfx   string // dataPfx + "_snapshots/"
	accountID string
	meter     Meterer
	now       func() time.Time
}

// New builds a Snapshotter over store for the given config.
func New(store ObjectStore, cfg Config) *Snapshotter {
	dp := normalizePrefix(cfg.DataPrefix)
	return &Snapshotter{
		store:     store,
		dataPfx:   dp,
		snapPfx:   dp + artifactMarker,
		accountID: cfg.AccountID,
		now:       time.Now,
	}
}

// WithMeter installs a usage meter (snapshot storage is reported through it).
func (s *Snapshotter) WithMeter(m Meterer) *Snapshotter { s.meter = m; return s }

// WithClock overrides the clock (test seam).
func (s *Snapshotter) WithClock(now func() time.Time) *Snapshotter { s.now = now; return s }

// blobKey returns the content-addressed key for a blob hash.
func (s *Snapshotter) blobKey(hash string) string {
	return s.snapPfx + "blobs/" + hash[:2] + "/" + hash
}

func (s *Snapshotter) manifestKey(id string) string {
	return s.snapPfx + "manifests/" + id + ".json.gz"
}
func (s *Snapshotter) indexKey(id string) string { return s.snapPfx + "index/" + id + ".json" }

// Create captures a point-in-time snapshot of every live object under the data
// prefix (excluding the reserved artifact area). Only blobs whose content is not
// already present are uploaded, so the snapshot is incremental + deduped. kind
// is one of the Kind* constants; parentID (optional) records lineage.
func (s *Snapshotter) Create(ctx context.Context, kind, parentID string) (*Index, error) {
	id := ulid.NewULID()
	objs, err := s.liveObjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot: list live objects: %w", err)
	}

	manifest := Manifest{SnapshotID: id, SourcePrefix: s.dataPfx}
	var logicalBytes, blobBytesAdded int64
	// De-dup within a single snapshot too: a hash written once this run is not
	// re-uploaded even if two live objects share content.
	writtenThisRun := map[string]bool{}

	for _, o := range objs {
		hash, raw, err := s.hashObject(ctx, o.Key)
		if err != nil {
			return nil, fmt.Errorf("snapshot: read %q: %w", o.Key, err)
		}
		rel := strings.TrimPrefix(o.Key, s.dataPfx)
		manifest.Entries = append(manifest.Entries, ManifestEntry{Key: rel, Size: int64(len(raw)), SHA256: hash})
		logicalBytes += int64(len(raw))

		if writtenThisRun[hash] {
			continue
		}
		bk := s.blobKey(hash)
		_, exists, serr := s.store.Stat(ctx, bk)
		if serr != nil {
			return nil, fmt.Errorf("snapshot: stat blob %q: %w", bk, serr)
		}
		if !exists {
			comp := gzipBytes(raw)
			if err := s.store.Put(ctx, bk, bytes.NewReader(comp), int64(len(comp)), "application/octet-stream"); err != nil {
				return nil, fmt.Errorf("snapshot: put blob %q: %w", bk, err)
			}
			blobBytesAdded += int64(len(comp))
		}
		writtenThisRun[hash] = true
	}

	// Manifest (deterministic order for stable hashing).
	sort.Slice(manifest.Entries, func(i, j int) bool { return manifest.Entries[i].Key < manifest.Entries[j].Key })
	mjson, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("snapshot: marshal manifest: %w", err)
	}
	mgz := gzipBytes(mjson)
	mhash := sha256hex(mgz)
	mkey := s.manifestKey(id)
	if err := s.store.Put(ctx, mkey, bytes.NewReader(mgz), int64(len(mgz)), "application/gzip"); err != nil {
		return nil, fmt.Errorf("snapshot: put manifest: %w", err)
	}

	idx := &Index{
		ID:             id,
		CreatedAt:      s.now().UTC(),
		ParentID:       parentID,
		Kind:           kind,
		SourcePrefix:   s.dataPfx,
		ObjectCount:    len(manifest.Entries),
		LogicalBytes:   logicalBytes,
		BlobBytesAdded: blobBytesAdded,
		ManifestKey:    mkey,
		ManifestSHA256: mhash,
	}
	ijson, err := json.Marshal(idx)
	if err != nil {
		return nil, fmt.Errorf("snapshot: marshal index: %w", err)
	}
	if err := s.store.Put(ctx, s.indexKey(id), bytes.NewReader(ijson), int64(len(ijson)), "application/json"); err != nil {
		return nil, fmt.Errorf("snapshot: put index: %w", err)
	}

	// Meter the bytes this snapshot added to storage (best-effort).
	if s.meter != nil && blobBytesAdded+int64(len(mgz)+len(ijson)) > 0 {
		s.meter.MeterSnapshotBytes(ctx, s.accountID, blobBytesAdded+int64(len(mgz)+len(ijson)))
	}
	return idx, nil
}

// List returns all snapshot indexes, newest first.
func (s *Snapshotter) List(ctx context.Context) ([]Index, error) {
	prefix := s.snapPfx + "index/"
	objs, err := s.store.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	var out []Index
	for _, o := range objs {
		if !strings.HasSuffix(o.Key, ".json") {
			continue
		}
		idx, err := s.loadIndex(ctx, o.Key)
		if err != nil {
			return nil, err
		}
		out = append(out, *idx)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// Get returns a single snapshot's index by id.
func (s *Snapshotter) Get(ctx context.Context, id string) (*Index, error) {
	if err := validSnapshotID(id); err != nil {
		return nil, err
	}
	return s.loadIndex(ctx, s.indexKey(id))
}

// liveObjects lists every object under the data prefix, EXCLUDING the reserved
// snapshot artifact area (so snapshots never capture themselves).
func (s *Snapshotter) liveObjects(ctx context.Context) ([]ObjectInfo, error) {
	all, err := s.store.List(ctx, s.dataPfx)
	if err != nil {
		return nil, err
	}
	out := all[:0:0]
	for _, o := range all {
		if strings.HasPrefix(o.Key, s.snapPfx) {
			continue
		}
		out = append(out, o)
	}
	return out, nil
}

// hashObject reads an object fully, returning its content hash and raw bytes.
func (s *Snapshotter) hashObject(ctx context.Context, key string) (string, []byte, error) {
	rc, err := s.store.Get(ctx, key)
	if err != nil {
		return "", nil, err
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return "", nil, err
	}
	return sha256hex(raw), raw, nil
}

func (s *Snapshotter) loadIndex(ctx context.Context, key string) (*Index, error) {
	rc, err := s.store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var idx Index
	if err := json.NewDecoder(rc).Decode(&idx); err != nil {
		return nil, fmt.Errorf("snapshot: decode index %q: %w", key, err)
	}
	return &idx, nil
}

// loadManifest fetches and integrity-checks the manifest for idx. It verifies
// the stored (gzipped) manifest hashes to idx.ManifestSHA256 BEFORE decoding —
// a corrupt or tampered manifest is rejected fail-closed.
func (s *Snapshotter) loadManifest(ctx context.Context, idx *Index) (*Manifest, error) {
	rc, err := s.store.Get(ctx, idx.ManifestKey)
	if err != nil {
		return nil, fmt.Errorf("snapshot: get manifest: %w", err)
	}
	defer rc.Close()
	mgz, err := io.ReadAll(rc)
	if err != nil {
		return nil, err
	}
	if got := sha256hex(mgz); got != idx.ManifestSHA256 {
		return nil, fmt.Errorf("%w: manifest hash mismatch (want %s got %s)", ErrIntegrity, idx.ManifestSHA256, got)
	}
	raw, err := gunzipBytes(mgz)
	if err != nil {
		return nil, fmt.Errorf("%w: manifest decompress: %v", ErrIntegrity, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%w: manifest decode: %v", ErrIntegrity, err)
	}
	return &m, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func normalizePrefix(p string) string {
	p = strings.Trim(p, "/")
	if p == "" {
		return ""
	}
	return p + "/"
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Bytes()
}

func gunzipBytes(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// validSnapshotID rejects ids that could escape the artifact prefix. ULIDs are
// alphanumeric; anything with a slash or dot-dot is refused.
func validSnapshotID(id string) error {
	if id == "" || strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") || strings.ContainsRune(id, 0) {
		return fmt.Errorf("snapshot: invalid snapshot id %q", id)
	}
	return nil
}

// safeLiveKey validates a manifest entry key and re-joins it under dataPfx,
// returning the absolute object key. It is the path-traversal guard: a crafted
// manifest can never cause a write outside the box's own data prefix, nor into
// the reserved snapshot area.
func safeLiveKey(dataPfx, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("snapshot: empty manifest key")
	}
	if strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("snapshot: NUL in manifest key")
	}
	if strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("snapshot: absolute manifest key %q", rel)
	}
	// Reject any "." / ".." / empty segment (traversal or non-canonical). Note
	// path.Clean("../x") == "../x", so an explicit segment scan is required in
	// addition to the Clean no-op check below.
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("snapshot: unsafe segment in manifest key %q", rel)
		}
	}
	// path.Clean must be a no-op — any redundant element is rejected.
	if path.Clean(rel) != rel {
		return "", fmt.Errorf("snapshot: unclean manifest key %q", rel)
	}
	if rel == artifactMarker || strings.HasPrefix(rel, artifactMarker) {
		return "", fmt.Errorf("snapshot: manifest key targets reserved snapshot area %q", rel)
	}
	full := dataPfx + rel
	if !strings.HasPrefix(full, dataPfx) { // defensive; always true given checks above
		return "", fmt.Errorf("snapshot: manifest key escapes data prefix %q", rel)
	}
	return full, nil
}

// validBlobHash rejects blob ids that are not lowercase hex of the right length.
func validBlobHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
