package syncrz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

// DefaultPullLimit is the default cap on /pull response size.
const DefaultPullLimit = 200

// Service is the syncrz orchestrator. It composes a Store (metadata,
// pure-Go SQLite) with an ObjectStore (opaque bytes in Tigris/Mem) and
// applies the opacity + content-address invariants documented in the
// package doc.
//
// IMPORTANT: the Service NEVER decodes a delta Payload. It only:
//   - hashes payload bytes to derive the dedup/content-address key, AND
//   - hands payload bytes to the ObjectStore opaque Put/Get.
//
// For BlobBytes, the Service additionally VERIFIES that
// sha256(bytes) == declared hash (R-CONTENTADDR) before committing.
type Service struct {
	Store        Store
	Objects      ObjectStore
	BucketPrefix string // default ""; bucket = BucketPrefix + account_id (see BucketForAccount)
	Now          func() time.Time
}

// NewService builds a Service with sensible defaults.
func NewService(st Store, obj ObjectStore) *Service {
	return &Service{
		Store:        st,
		Objects:      obj,
		BucketPrefix: "vulos-",
		Now:          func() time.Time { return time.Now().UTC() },
	}
}

// BucketForAccount returns the per-account Tigris bucket name. Mirrors the
// rest of cp's "vulos-{account_id_lower}" convention (see storage.managedBucketName).
func (s *Service) BucketForAccount(accountID string) string {
	return s.BucketPrefix + lower(accountID)
}

// deltaKey returns the s3 key for a delta payload.
func deltaKey(accountID, syncKey, sha256Hex string) string {
	return "accounts/" + accountID + "/sync/" + syncKey + "/deltas/" + sha256Hex + ".bin"
}

// blobKey returns the s3 key for a blob payload.
func blobKey(accountID, contentHash string) string {
	return "accounts/" + accountID + "/sync/blobs/" + contentHash + ".bin"
}

// EncodeCursor returns the opaque cursor representation handed to the client.
// The current scheme is hex(uint64); a client MUST treat it as opaque.
func EncodeCursor(id int64) string {
	if id < 0 {
		id = 0
	}
	return strconv.FormatInt(id, 16)
}

// DecodeCursor parses a cursor handed back by the client. An empty string
// decodes to 0 (pull from the beginning).
func DecodeCursor(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 16, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: bad cursor %q", ErrInvalidInput, s)
	}
	return n, nil
}

// PushInput is one delta a box submits via Push. The box has computed the
// content hash itself; the coordinator re-verifies on commit.
type PushInput struct {
	BoxID        string   // pusher's stable identity
	CodecVersion int      // opaque, round-tripped
	Payload      []byte   // OPAQUE bytes; coordinator never decodes
	Refs         []string // content-addressed blob hashes referenced by Payload
}

// BlobInput is one blob a box submits alongside the deltas.
type BlobInput struct {
	ContentHash string
	Data        []byte
}

// Push commits one batch of deltas + blobs for (account, syncKey).
// Idempotent: re-pushing the same (account, syncKey, payload_sha256) is a
// no-op for that delta; re-pushing the same (account, content_hash) is a
// no-op for that blob. Returns the new cursor (max id after the commit).
func (s *Service) Push(ctx context.Context, accountID, syncKey string, deltas []PushInput, blobs []BlobInput) (PushResult, error) {
	if accountID == "" || syncKey == "" {
		return PushResult{}, ErrInvalidInput
	}
	now := s.Now()
	bucket := s.BucketForAccount(accountID)
	var res PushResult

	// 1. Commit blobs FIRST so that any delta whose refs land on the same call
	//    has its blobs resolvable. Content-address verification on every blob.
	for _, b := range blobs {
		if b.ContentHash == "" {
			return res, fmt.Errorf("%w: blob missing content_hash", ErrInvalidInput)
		}
		if int64(len(b.Data)) > MaxBlobBytes {
			return res, fmt.Errorf("%w: blob exceeds %d bytes", ErrTooLarge, MaxBlobBytes)
		}
		// R-CONTENTADDR — coordinator MUST verify hash on commit.
		got := sha256Hex(b.Data)
		if got != lower(b.ContentHash) {
			return res, fmt.Errorf("%w: blob declared %s, hashed %s", ErrHashMismatch, b.ContentHash, got)
		}
		key := blobKey(accountID, got)
		if err := s.Objects.Put(ctx, bucket, key, b.Data); err != nil {
			return res, fmt.Errorf("syncrz: blob put: %w", err)
		}
		ins, err := s.Store.AppendBlob(ctx, accountID, got, int64(len(b.Data)), key, now)
		if err != nil {
			return res, err
		}
		if ins {
			res.AcceptedBlobs++
		} else {
			res.DedupedBlobs++
		}
	}

	// 2. Commit deltas. The coordinator NEVER decodes Payload — it only hashes
	//    the bytes (for dedup + addressing) and hands them to ObjectStore.
	var maxID int64
	var boxID string
	for _, d := range deltas {
		if d.BoxID == "" {
			return res, fmt.Errorf("%w: delta missing box_id", ErrInvalidInput)
		}
		boxID = d.BoxID
		if int64(len(d.Payload)) > MaxPayloadBytes {
			return res, fmt.Errorf("%w: delta exceeds %d bytes", ErrTooLarge, MaxPayloadBytes)
		}
		sum := sha256Hex(d.Payload)
		key := deltaKey(accountID, syncKey, sum)
		// Object put is idempotent on (bucket, key): same sha256 → same bytes.
		if err := s.Objects.Put(ctx, bucket, key, d.Payload); err != nil {
			return res, fmt.Errorf("syncrz: delta put: %w", err)
		}
		id, inserted, err := s.Store.AppendDelta(ctx, accountID, syncKey, d.BoxID, d.CodecVersion, int64(len(d.Payload)), sum, key, d.Refs, now)
		if err != nil {
			return res, err
		}
		if inserted {
			res.AcceptedDeltas++
		} else {
			res.DedupedDeltas++
		}
		if id > maxID {
			maxID = id
		}
	}

	// 3. Advance the per-box cursor if we know who pushed.
	if boxID != "" && maxID > 0 {
		if err := s.Store.UpsertCursor(ctx, accountID, syncKey, boxID, maxID, now); err != nil {
			return res, err
		}
	}
	res.Cursor = EncodeCursor(maxID)
	return res, nil
}

// Pull returns the deltas (account, syncKey) holds with id > since, skipping
// any pushed by boxID (no echo). It hydrates each delta's Payload from the
// object store so the caller has one round-trip per pull.
//
// limit ≤ 0 → DefaultPullLimit.
func (s *Service) Pull(ctx context.Context, accountID, syncKey, boxID, cursor string, limit int) (PullResult, error) {
	if accountID == "" || syncKey == "" || boxID == "" {
		return PullResult{}, ErrInvalidInput
	}
	since, err := DecodeCursor(cursor)
	if err != nil {
		return PullResult{}, err
	}
	if limit <= 0 {
		limit = DefaultPullLimit
	}
	bucket := s.BucketForAccount(accountID)
	deltas, err := s.Store.PullDeltas(ctx, accountID, syncKey, boxID, since, limit)
	if err != nil {
		return PullResult{}, err
	}
	manifestSet := make(map[string]struct{})
	maxID := since
	for i := range deltas {
		// Hydrate opaque payload bytes from object store.
		data, err := s.Objects.Get(ctx, bucket, deltaKey(accountID, syncKey, deltas[i].PayloadSHA256))
		if err != nil {
			return PullResult{}, fmt.Errorf("syncrz: pull get %s: %w", deltas[i].PayloadSHA256, err)
		}
		deltas[i].Payload = data
		for _, h := range deltas[i].Refs {
			manifestSet[h] = struct{}{}
		}
		if deltas[i].ID > maxID {
			maxID = deltas[i].ID
		}
	}
	now := s.Now()
	if maxID > since {
		if err := s.Store.UpsertCursor(ctx, accountID, syncKey, boxID, maxID, now); err != nil {
			return PullResult{}, err
		}
	}
	manifest := make([]string, 0, len(manifestSet))
	for h := range manifestSet {
		manifest = append(manifest, h)
	}
	return PullResult{
		Cursor:   EncodeCursor(maxID),
		Deltas:   deltas,
		Manifest: manifest,
	}, nil
}

// FetchBlob returns the bytes for (account, contentHash) — content-address
// verification on the way out. Used by the /api/sync/blob/<hash> route.
func (s *Service) FetchBlob(ctx context.Context, accountID, contentHash string) ([]byte, error) {
	if accountID == "" || contentHash == "" {
		return nil, ErrInvalidInput
	}
	key, err := s.Store.GetBlobKey(ctx, accountID, lower(contentHash))
	if err != nil {
		return nil, err
	}
	bucket := s.BucketForAccount(accountID)
	data, err := s.Objects.Get(ctx, bucket, key)
	if err != nil {
		return nil, err
	}
	// Defence in depth: verify on the way out so a corrupted Tigris object
	// cannot poison a peer's CAS verification.
	if sha256Hex(data) != lower(contentHash) {
		return nil, ErrHashMismatch
	}
	return data, nil
}

// HasBlob reports whether the account currently holds contentHash.
func (s *Service) HasBlob(ctx context.Context, accountID, contentHash string) (bool, error) {
	_, err := s.Store.GetBlobKey(ctx, accountID, lower(contentHash))
	switch err {
	case nil:
		return true, nil
	case ErrUnknown:
		return false, nil
	default:
		return false, err
	}
}

// sha256Hex returns lowercase hex sha256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// lower is a tiny helper that avoids importing strings just for ToLower; the
// inputs (account ids, sha256 hex) are ASCII.
func lower(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c >= 'A' && c <= 'Z' {
			out[i] = c + ('a' - 'A')
		}
	}
	return string(out)
}
