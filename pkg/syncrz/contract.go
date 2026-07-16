// Package syncrz is the cloud-side central-Tigris rendezvous sync
// coordinator for CRDT delta + content-addressed blob exchange between an
// org's boxes (SYNC-RENDEZVOUS-01).
//
// # Why this package exists
//
// Three OSS repos ship a per-store SyncTransport seam against which this
// package's HTTP API is the canonical central-bucket implementation:
//
//   - vulos-mail/store/sync          (mailbox CRDT + blob manifest)
//   - vulos-office/backend/crdt      (session op-log + content-addressed blobs)
//
// Each repo's interface is shaped slightly differently (mail does one-shot
// bidirectional ExchangeDeltas; office splits PushDeltas/PullDeltas;
// relay uses an opaque VersionVector). The wire format below is the
// natural UNION of the three so each repo can adapt to it with a thin
// client adapter without changing its store-side semantics:
//
//	mail.ExchangeDeltas(key, out)  ≡  POST /api/sync/push (out)
//	                                  + GET /api/sync/pull?since=<cursor>
//	office.PushDeltas / PullDeltas ≡  POST /api/sync/push
//	                                  + GET /api/sync/pull?since=<cursor>
//	relay.DeltasSince(remote)      ≡  GET /api/sync/pull?since=<remote-cursor>
//	relay.ApplyDeltas              ≡  client-side consumption of pull payload
//	*.GetBlob(hash)                ≡  GET /api/sync/blob/<hash>
//	*.PutBlob(data)                ≡  POST /api/sync/push (blobs field)
//
// # Wire format (VULOS-SYNC-RZ/1)
//
// Cursor is an OPAQUE base16 string the coordinator hands back on push and
// the box echoes on pull. Internally it is the auto-increment row id of the
// last delta seen by the caller for that (account, sync_key). Boxes never
// parse it; they store it per (sync_key, peer-relative scope).
//
// All payloads carried in /push/blob are OPAQUE bytes from the coordinator's
// perspective. The coordinator NEVER parses delta bytes (R-OPAQUE) and NEVER
// inspects blob bytes beyond verifying the content hash (R-CONTENTADDR).
//
// For BYO/local-MinIO accounts the box encrypts before push — see the
// "opaque" path in package doc; the coordinator stores ciphertext as-is and
// the GetBlob/Pull endpoints return the same ciphertext (a peer with the key
// decrypts client-side).
//
// # Bucket layout
//
//	<root>/accounts/<account_id>/sync/<sync_key>/deltas/<sha256>.bin
//	<root>/accounts/<account_id>/sync/<sync_key>/blobs/<sha256>.bin
//
// Same prefix the rest of cp uses ("vulos-{ulid_lower}" via storage.SnapshotURL).
// Per-account isolation is enforced at the HTTP layer (X-Device-Auth +
// account_id parameter), at the SQL layer (PRIMARY KEYs include account_id),
// and at the bucket layer (prefix per account).
//
// # HTTP API
//
// All routes require `X-Device-Auth: <CP_SHARED_SECRET>` (the same header
// every CP→box endpoint uses, see internal/lancert + routes_webapp).
//
//	POST /api/sync/push
//	  body: { account_id, sync_key, box_id, codec_version, deltas: [{payload_b64, refs:[hash]}], blobs: [{hash, data_b64}] }
//	  resp: { cursor: "<opaque>", accepted: N, dedup: N, blob_accepted: N }
//
//	GET  /api/sync/pull?account_id=...&sync_key=...&box_id=...&since=<cursor>
//	  resp: { cursor: "<new opaque>", deltas: [{codec_version, payload_b64, refs:[hash], box_id}], manifest: [hash] }
//
//	GET  /api/sync/blob/<hash>?account_id=...
//	  resp: 200 octet-stream (proxied from Tigris)
//	        404 if unknown
//
// # Invariants (FROZEN)
//
//   - Pure-Go modernc.org/sqlite. Never CGO.
//   - No Go-module dep on vulos-mail / vulos-relay / vulos-office.
//   - The coordinator NEVER decodes delta payloads (only metadata: size, hash).
//   - The coordinator NEVER decodes blob payloads beyond sha256(bytes) ==
//     declared hash (content-address verification on Put).
//   - Idempotent: pushing the same payload (account, key, sha256) twice writes
//     once. Returning the same response on the second push.
package syncrz

import (
	"context"
	"errors"
	"io"
	"time"
)

// WireProto is the wire-format protocol tag echoed in every response. Bump
// on a breaking change.
const WireProto = "VULOS-SYNC-RZ/1"

// MaxPayloadBytes is the upper bound on a single delta payload, used by the
// HTTP handler to reject oversized push bodies. Soft default — operators can
// raise via env if a tenant ships larger ops.
const MaxPayloadBytes = 16 << 20 // 16 MiB

// MaxBlobBytes is the upper bound on a single blob put.
const MaxBlobBytes = 64 << 20 // 64 MiB

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

var (
	// ErrInvalidInput — missing/blank required field or malformed cursor.
	ErrInvalidInput = errors.New("syncrz: invalid input")
	// ErrUnknown — no such delta / blob / account.
	ErrUnknown = errors.New("syncrz: not found")
	// ErrHashMismatch — bytes did not hash to the declared content address.
	ErrHashMismatch = errors.New("syncrz: content hash mismatch")
	// ErrTooLarge — payload exceeded MaxPayloadBytes / MaxBlobBytes.
	ErrTooLarge = errors.New("syncrz: payload too large")
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// DeltaRef is one opaque, versioned delta the box pushed (or that pull
// returns). The coordinator carries Payload bytes through Tigris verbatim
// and NEVER parses them.
type DeltaRef struct {
	// ID is the coordinator-assigned auto-increment row id; it is the value
	// that appears in Cursor. Stable across re-pushes (idempotent).
	ID int64 `json:"id"`
	// CodecVersion is opaque to the coordinator; round-tripped so a box that
	// receives a delta can refuse a codec it does not understand.
	CodecVersion int `json:"codec_version"`
	// BoxID identifies the pusher so /pull can skip echo back to the same
	// box. Opaque string.
	BoxID string `json:"box_id"`
	// PayloadSHA256 is the lowercase-hex sha256 of Payload. Used as the dedup
	// key (idempotent push) and the Tigris object name.
	PayloadSHA256 string `json:"payload_sha256"`
	// Payload is the OPAQUE delta bytes. Populated on pull/get; on push the
	// caller streams this through PushDelta.
	Payload []byte `json:"payload,omitempty"`
	// Refs are content-addressed blob hashes this delta references. The peer
	// uses them to know which blobs to fetch via GetBlob.
	Refs []string `json:"refs,omitempty"`
	// PushedAt is the coordinator's clock when the delta first landed (unix).
	PushedAt int64 `json:"pushed_at"`
}

// BlobMeta is the listing entry for a content-addressed blob held for the
// account. Bytes are NEVER carried in this struct — fetch via GetBlob.
type BlobMeta struct {
	ContentHash string `json:"content_hash"`
	SizeBytes   int64  `json:"size_bytes"`
}

// PushResult summarises what one /push call landed.
type PushResult struct {
	// Cursor is the new high-water cursor for this caller / sync_key. Echo on
	// the next /pull.
	Cursor string `json:"cursor"`
	// AcceptedDeltas is the count of NEW deltas (not duplicates) committed.
	AcceptedDeltas int `json:"accepted_deltas"`
	// DedupedDeltas is the count of deltas the coordinator already had under
	// the same content hash (idempotent re-push).
	DedupedDeltas int `json:"deduped_deltas"`
	// AcceptedBlobs is the count of NEW blobs committed.
	AcceptedBlobs int `json:"accepted_blobs"`
	// DedupedBlobs is the count of blobs already held under the same hash.
	DedupedBlobs int `json:"deduped_blobs"`
}

// PullResult is the answer to one /pull call. Deltas omit any pushed by
// BoxID (no echo). Manifest is the union of all blob hashes referenced by
// the returned deltas — convenient hint for the client.
type PullResult struct {
	Cursor   string     `json:"cursor"`
	Deltas   []DeltaRef `json:"deltas"`
	Manifest []string   `json:"manifest"`
}

// ---------------------------------------------------------------------------
// Object store seam — the coordinator persists opaque bytes via this
// interface. In production it is satisfied by storage.Provider (Tigris S3);
// in tests by an in-memory implementation.
// ---------------------------------------------------------------------------

// ObjectStore is the narrow seam over an opaque content-addressed object
// store. NEVER reused for any other purpose. ALL bytes flowing through it
// are OPAQUE to the coordinator (BYO accounts encrypt client-side; managed
// accounts may store plaintext but the coordinator never parses it).
type ObjectStore interface {
	// Put stores opaque bytes under key in the per-account bucket. Idempotent
	// on key (re-putting the same key with the same bytes is a no-op).
	Put(ctx context.Context, bucket, key string, data []byte) error
	// Get fetches bytes by key; returns ErrUnknown if not present.
	Get(ctx context.Context, bucket, key string) ([]byte, error)
	// Has reports whether key exists.
	Has(ctx context.Context, bucket, key string) (bool, error)
}

// ---------------------------------------------------------------------------
// Store — persistence contract (pure-Go SQLite impl in store.go)
// ---------------------------------------------------------------------------

// Store is the persistence contract. The SQLite implementation must match
// MemStore behaviourally. The Service depends ONLY on this interface.
type Store interface {
	// AppendDelta records one delta push. Idempotent on (account, sync_key,
	// payload_sha256): a re-push returns the existing row's id with
	// inserted=false. refs are the content hashes the delta references
	// (extracted and provided by the BOX, NOT parsed from payload — the
	// coordinator stays opaque).
	AppendDelta(ctx context.Context, accountID, syncKey, boxID string, codecVersion int, payloadBytes int64, payloadSHA256, tigrisKey string, refs []string, now time.Time) (id int64, inserted bool, err error)

	// PullDeltas returns deltas for (account, sync_key) with id > since,
	// excluding any pushed by excludeBoxID (no-echo). limit caps the response.
	PullDeltas(ctx context.Context, accountID, syncKey, excludeBoxID string, since int64, limit int) ([]DeltaRef, error)

	// UpsertCursor records last_seen_id for a (box, sync_key) — optional aid
	// for "pull since my last seen" without the box tracking the cursor.
	UpsertCursor(ctx context.Context, accountID, syncKey, boxID string, lastSeenID int64, now time.Time) error

	// LastSeen returns the last_seen_id recorded for (box, sync_key), or 0.
	LastSeen(ctx context.Context, accountID, syncKey, boxID string) (int64, error)

	// AppendBlob records one blob put. Idempotent on (account, content_hash).
	AppendBlob(ctx context.Context, accountID, contentHash string, sizeBytes int64, tigrisKey string, now time.Time) (inserted bool, err error)

	// GetBlobKey returns the tigris key for (account, content_hash), or
	// ErrUnknown.
	GetBlobKey(ctx context.Context, accountID, contentHash string) (string, error)

	// Close releases the underlying database.
	Close() error

	// PingDB satisfies the readyz storeHealth ping contract.
	PingDB(ctx context.Context) error
}

// ---------------------------------------------------------------------------
// Compile-time assertion the io package is referenced (kept for streaming
// blob fetch fallback when proxied directly from Tigris — see routes.go).
// ---------------------------------------------------------------------------

var _ = io.EOF
