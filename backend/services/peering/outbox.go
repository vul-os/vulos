// outbox.go — offline queue with retry/backoff, ACK, and reconnect sync (PEER-15).
//
// # Overview
//
// OutboxQueue persists outbound messages for peers that are temporarily
// unreachable. When delivery to a peer fails, the message is kept in
// ~/.vulos/peering/outbox/<peer-vulos-id>/<id>.json with an associated
// delivery-attempt record. A background worker retries on a schedule:
//
//	1s → 5s → 30s → 5m → 1h → periodic (1h)
//
// When the remote peer acknowledges receipt (ACK), the message is removed
// from the queue. When a peer reconnects and calls PullMissed, all messages
// queued since that peer's last-seen timestamp are returned for batch delivery.
//
// # Storage layout
//
//	~/.vulos/peering/outbox/
//	  └── <peer-vulos-id>/
//	      └── <queued-at-timestamp>_<msg-id>.json   (one file per queued item)
//
// Each file is a JSON-encoded QueuedEnvelope containing the full signed
// Envelope plus retry metadata.
//
// # Thread safety
//
// OutboxQueue is safe for concurrent use. File creation is atomic
// (temp-file + rename within the same directory).
//
// # Usage
//
//	q, err := peering.NewOutboxQueue(home, client)
//	if err != nil { ... }
//	q.Start(ctx)             // launch background retry worker
//
//	// Enqueue after a delivery failure:
//	if err := client.Post(ctx, baseURL, "message", env); err != nil {
//	    q.Enqueue(peerVulosID, env)
//	}
//
//	// ACK on confirmed delivery:
//	q.Acknowledge(peerVulosID, msgID)
//
//	// Reconnect sync — pull all messages not yet ACKed since lastSeen:
//	items := q.PullMissed(peerVulosID, lastSeen)
//	for _, item := range items {
//	    client.Post(ctx, peerBaseURL, item.Envelope.Type, item.Envelope)
//	}
package peering

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ─── Retry schedule ────────────────────────────────────────────────────────────

// outboxRetrySchedule defines the wait periods between successive delivery
// attempts. After the last entry is exhausted every subsequent attempt uses
// the final value (periodic retry).
var outboxRetrySchedule = []time.Duration{
	1 * time.Second,
	5 * time.Second,
	30 * time.Second,
	5 * time.Minute,
	1 * time.Hour,
}

// outboxPeriodicRetry is the interval used once all schedule steps are
// exhausted.
const outboxPeriodicRetry = 1 * time.Hour

// outboxWorkerInterval is how often the background worker scans the outbox
// for items that are due for a retry attempt.
const outboxWorkerInterval = 5 * time.Second

// ─── QueuedEnvelope ────────────────────────────────────────────────────────────

// QueuedEnvelope is the on-disk representation of a single queued outbound
// message. The Envelope field holds the complete, already-signed envelope ready
// to be delivered verbatim.
type QueuedEnvelope struct {
	// PeerVulosID is the recipient's Vula ID.
	PeerVulosID string `json:"peer_vulos_id"`

	// PeerServer is the last-known base URL of the recipient, e.g.
	// "https://bob.vulos.org:8080". May be updated by the caller between
	// retries if the peer's server address changes.
	PeerServer string `json:"peer_server"`

	// Envelope is the fully signed envelope to deliver.
	Envelope *Envelope `json:"envelope"`

	// QueuedAt is the time at which the message entered the queue.
	QueuedAt time.Time `json:"queued_at"`

	// Attempts is the number of delivery attempts made so far (0 = none yet).
	Attempts int `json:"attempts"`

	// NextAttempt is the earliest time at which the next delivery attempt
	// should be made.
	NextAttempt time.Time `json:"next_attempt"`
}

// ─── OutboxQueue ───────────────────────────────────────────────────────────────

// OutboxQueue manages persistent storage and background retry delivery of
// outbound messages for unreachable peers.
//
// Obtain one via NewOutboxQueue; the zero value is not usable.
type OutboxQueue struct {
	root   string      // absolute path to ~/.vulos/peering/outbox/
	client *PeerClient // outbound HTTP client (shared with messages.go)
	// mu is reserved for the in-memory NextAttempt updates the retry path will
	// need once the outbox is wired to a live sender. It guards nothing today --
	// stated plainly so nobody reads this struct as already thread-safe.
	//lint:ignore U1000 reserved for the retry path; see the note above
	mu       sync.Mutex
	workerID uint64 // atomic counter; each retryPeerDir call gets a unique ID
}

// NewOutboxQueue creates an OutboxQueue backed by
// filepath.Join(root, "peering", "outbox").
//
// The outbox directory is created idempotently. client is the shared
// PeerClient used for outbound HTTP delivery.
func NewOutboxQueue(home string, client *PeerClient) (*OutboxQueue, error) {
	if client == nil {
		return nil, fmt.Errorf("peering/outbox: client must not be nil")
	}
	root := filepath.Join(home, "peering", "outbox")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("peering/outbox: mkdir %s: %w", root, err)
	}
	return &OutboxQueue{root: root, client: client}, nil
}

// Start launches the background retry worker. It runs until ctx is cancelled.
// Call Start exactly once per OutboxQueue after construction.
func (q *OutboxQueue) Start(ctx context.Context) {
	go q.retryWorker(ctx)
}

// ─── Enqueue ──────────────────────────────────────────────────────────────────

// Enqueue persists a signed envelope in the outbox for peerVulosID, scheduling
// it for delivery. The first delivery attempt will be made after the first
// outboxRetrySchedule interval (1 s).
//
// peerServer is the last-known server base URL for the recipient; it may be
// empty if unknown (the worker will skip delivery but keep the item).
//
// Enqueue is idempotent for the same env.ID — a second call with the same
// message ID is a no-op.
func (q *OutboxQueue) Enqueue(peerVulosID, peerServer string, env *Envelope) error {
	if peerVulosID == "" {
		return fmt.Errorf("peering/outbox: Enqueue: peerVulosID must not be empty")
	}
	if env == nil {
		return fmt.Errorf("peering/outbox: Enqueue: envelope must not be nil")
	}
	if env.ID == "" {
		return fmt.Errorf("peering/outbox: Enqueue: envelope ID must not be empty")
	}
	if env.Signature == "" {
		return fmt.Errorf("peering/outbox: Enqueue: envelope must be signed")
	}

	now := time.Now().UTC()
	item := QueuedEnvelope{
		PeerVulosID: peerVulosID,
		PeerServer:  peerServer,
		Envelope:    env,
		QueuedAt:    now,
		Attempts:    0,
		NextAttempt: now.Add(outboxRetrySchedule[0]),
	}
	return q.writeItem(item)
}

// ─── Acknowledge ──────────────────────────────────────────────────────────────

// Acknowledge removes the message identified by msgID from the outbox for
// peerVulosID. Called after the remote peer has confirmed receipt (ACK). If
// the message is not in the queue (already removed or never queued),
// Acknowledge is a no-op.
func (q *OutboxQueue) Acknowledge(peerVulosID, msgID string) {
	peerDir := filepath.Join(q.root, sanitizePath(peerVulosID))
	entries, err := os.ReadDir(peerDir)
	if err != nil {
		return // directory absent — nothing to remove
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		// Extract the message ID from the filename suffix.
		if outboxFileMsgID(entry.Name()) == msgID {
			path := filepath.Join(peerDir, entry.Name())
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				log.Printf("[peering/outbox] Acknowledge: remove %s: %v", path, err)
			}
			return
		}
	}
}

// ─── PullMissed ───────────────────────────────────────────────────────────────

// PullMissed returns all queued messages for peerVulosID that were queued after
// lastSeen. This implements the reconnect-sync ("pull since last-seen") step:
// when a peer comes online, the local server calls PullMissed to get all
// messages that must be re-delivered.
//
// The returned slice is ordered oldest-first by QueuedAt. Items that have
// been ACKed (file removed) are not included.
//
// The caller is responsible for actually delivering the returned envelopes
// and calling Acknowledge on success.
func (q *OutboxQueue) PullMissed(peerVulosID string, lastSeen time.Time) []QueuedEnvelope {
	peerDir := filepath.Join(q.root, sanitizePath(peerVulosID))
	entries, err := os.ReadDir(peerDir)
	if err != nil {
		return nil
	}

	var result []QueuedEnvelope
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		item, err := q.readItem(filepath.Join(peerDir, entry.Name()))
		if err != nil {
			continue
		}
		if item.QueuedAt.After(lastSeen) {
			result = append(result, item)
		}
	}
	// Already in filesystem (alphabetical / timestamp) order; sort defensively.
	outboxSortByQueuedAt(result)
	return result
}

// ─── UpdatePeerServer ─────────────────────────────────────────────────────────

// UpdatePeerServer updates the PeerServer field on all queued items for
// peerVulosID. Call this when the peer's server address changes so the next
// retry attempt uses the correct URL.
func (q *OutboxQueue) UpdatePeerServer(peerVulosID, newServer string) {
	peerDir := filepath.Join(q.root, sanitizePath(peerVulosID))
	entries, err := os.ReadDir(peerDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		path := filepath.Join(peerDir, entry.Name())
		item, err := q.readItem(path)
		if err != nil {
			continue
		}
		if item.PeerServer == newServer {
			continue
		}
		item.PeerServer = newServer
		if werr := q.overwriteItem(path, item); werr != nil {
			log.Printf("[peering/outbox] UpdatePeerServer: %v", werr)
		}
	}
}

// ─── QueueDepth ───────────────────────────────────────────────────────────────

// QueueDepth returns the number of messages currently queued for peerVulosID.
// Returns 0 if no outbox directory exists for that peer.
func (q *OutboxQueue) QueueDepth(peerVulosID string) int {
	peerDir := filepath.Join(q.root, sanitizePath(peerVulosID))
	entries, err := os.ReadDir(peerDir)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") &&
			!strings.HasPrefix(entry.Name(), ".") {
			count++
		}
	}
	return count
}

// ─── Background worker ────────────────────────────────────────────────────────

// retryWorker runs in a goroutine, scanning the outbox every
// outboxWorkerInterval and attempting delivery for items whose NextAttempt
// time has passed.
func (q *OutboxQueue) retryWorker(ctx context.Context) {
	ticker := time.NewTicker(outboxWorkerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			q.runRetryPass(ctx)
		}
	}
}

// runRetryPass iterates all peer directories in the outbox root and attempts
// delivery for every item whose NextAttempt is in the past.
func (q *OutboxQueue) runRetryPass(ctx context.Context) {
	peerDirs, err := os.ReadDir(q.root)
	if err != nil {
		return
	}

	for _, pd := range peerDirs {
		if !pd.IsDir() {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		peerDir := filepath.Join(q.root, pd.Name())
		q.retryPeerDir(ctx, peerDir)
	}
}

// retryPeerDir processes all queued items for a single peer directory.
//
// Concurrent-worker safety (Medium fix #9): before reading + delivering an
// item, this method atomically renames the file to
// <file>.processing.<workerID>.  If the rename fails (another worker already
// grabbed it), the item is skipped.  On successful delivery the processing
// file is removed.  On failure it is renamed back to its original name so the
// next pass will retry it.
func (q *OutboxQueue) retryPeerDir(ctx context.Context, peerDir string) {
	wid := atomic.AddUint64(&q.workerID, 1)

	entries, err := os.ReadDir(peerDir)
	if err != nil {
		return
	}

	now := time.Now().UTC()

	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		// Skip files that are already being processed (or temp files).
		if strings.HasPrefix(name, ".") || strings.Contains(name, ".processing.") {
			continue
		}

		path := filepath.Join(peerDir, name)
		processingPath := fmt.Sprintf("%s.processing.%d", path, wid)

		// Atomic claim: rename original → processing.<wid>.
		// If another worker already claimed it, os.Rename fails and we skip.
		if err := os.Rename(path, processingPath); err != nil {
			// Either grabbed by another worker or already delivered.
			continue
		}

		item, err := q.readItem(processingPath)
		if err != nil {
			log.Printf("[peering/outbox] read item %s: %v", processingPath, err)
			// Restore so the next pass can retry.
			_ = os.Rename(processingPath, path)
			continue
		}

		if now.Before(item.NextAttempt) {
			// Not due yet — restore original filename.
			_ = os.Rename(processingPath, path)
			continue
		}

		if item.PeerServer == "" {
			// Cannot deliver without a server address; bump and restore.
			item = q.bumpAttempt(item)
			if werr := q.overwriteItem(processingPath, item); werr != nil {
				log.Printf("[peering/outbox] update item %s: %v", processingPath, werr)
			}
			_ = os.Rename(processingPath, path)
			continue
		}

		// Route through the single reachability seam (CONSOLIDATION B-0). PeerServer
		// is stored already-resolved (a full https:// base URL from the enqueue
		// site), so resolvePeerBaseURL is a pass-through here; keeping the call keeps
		// the retry path on the same primitive as first-attempt delivery.
		baseURL := resolvePeerBaseURL(item.PeerVulosID, item.PeerServer)
		deliverErr := q.client.Post(ctx, baseURL, item.Envelope.Type, item.Envelope)
		if deliverErr == nil {
			// Delivered successfully — remove the processing file.
			if rerr := os.Remove(processingPath); rerr != nil && !os.IsNotExist(rerr) {
				log.Printf("[peering/outbox] remove delivered item %s: %v", processingPath, rerr)
			}
			log.Printf("[peering/outbox] delivered %s to %s (after %d attempt(s))",
				item.Envelope.ID, item.PeerVulosID, item.Attempts+1)
			continue
		}

		// Delivery failed — bump attempt count, persist, then restore to pending name.
		log.Printf("[peering/outbox] delivery attempt %d for %s to %s failed: %v",
			item.Attempts+1, item.Envelope.ID, baseURL, deliverErr)
		item = q.bumpAttempt(item)
		if werr := q.overwriteItem(processingPath, item); werr != nil {
			log.Printf("[peering/outbox] update item %s: %v", processingPath, werr)
		}
		// Restore to original name so subsequent passes will retry.
		_ = os.Rename(processingPath, path)
	}
}

// bumpAttempt increments the attempt counter and sets the next retry time
// based on the retry schedule. Once the schedule is exhausted the periodic
// interval is used.
func (q *OutboxQueue) bumpAttempt(item QueuedEnvelope) QueuedEnvelope {
	item.Attempts++
	idx := item.Attempts
	var wait time.Duration
	if idx < len(outboxRetrySchedule) {
		wait = outboxRetrySchedule[idx]
	} else {
		wait = outboxPeriodicRetry
	}
	item.NextAttempt = time.Now().UTC().Add(wait)
	return item
}

// ─── Persistence helpers ──────────────────────────────────────────────────────

// writeItem writes a new item file for item.PeerVulosID. The filename encodes
// QueuedAt and envelope ID so directory listings are ordered chronologically
// and items are identifiable by message ID.
//
// Writes are atomic: temp file + rename within the same directory.
// If a file for the same message ID already exists, writeItem is a no-op
// (idempotent).
func (q *OutboxQueue) writeItem(item QueuedEnvelope) error {
	peerDir := filepath.Join(q.root, sanitizePath(item.PeerVulosID))
	if err := os.MkdirAll(peerDir, 0700); err != nil {
		return fmt.Errorf("peering/outbox: mkdir peer dir %s: %w", peerDir, err)
	}

	filename := outboxFilename(item.QueuedAt, item.Envelope.ID)
	dst := filepath.Join(peerDir, filename)

	// Idempotency: skip if already exists.
	if _, err := os.Stat(dst); err == nil {
		return nil
	}

	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("peering/outbox: marshal item %s: %w", item.Envelope.ID, err)
	}

	return atomicWrite(peerDir, dst, data)
}

// overwriteItem replaces an existing item file at path (used when updating
// Attempts / NextAttempt / PeerServer). The write is atomic.
func (q *OutboxQueue) overwriteItem(path string, item QueuedEnvelope) error {
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("peering/outbox: marshal item %s: %w", item.Envelope.ID, err)
	}
	dir := filepath.Dir(path)
	return atomicWrite(dir, path, data)
}

// readItem reads and decodes a QueuedEnvelope from path.
func (q *OutboxQueue) readItem(path string) (QueuedEnvelope, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return QueuedEnvelope{}, fmt.Errorf("peering/outbox: read %s: %w", path, err)
	}
	var item QueuedEnvelope
	if err := json.Unmarshal(data, &item); err != nil {
		return QueuedEnvelope{}, fmt.Errorf("peering/outbox: unmarshal %s: %w", path, err)
	}
	return item, nil
}

// ─── File naming ──────────────────────────────────────────────────────────────

// outboxFilename returns the filename for an outbox item.
//
// Format: <RFC3339 with '!' replacing ':'>_<msg-id>.json
// This mirrors the inbox.go convention so filenames sort chronologically.
func outboxFilename(queuedAt time.Time, msgID string) string {
	tsStr := strings.ReplaceAll(queuedAt.UTC().Format(time.RFC3339), ":", "!")
	return fmt.Sprintf("%s_%s.json", tsStr, sanitizePath(msgID))
}

// outboxFileMsgID extracts the message ID from an outbox filename.
//
// The filename format is: <timestamp>_<msg-id>.json. The first '_' separates
// the timestamp from the message ID.
func outboxFileMsgID(filename string) string {
	base := strings.TrimSuffix(filename, ".json")
	idx := strings.Index(base, "_")
	if idx < 0 {
		return base
	}
	return base[idx+1:]
}

// ─── Utility ──────────────────────────────────────────────────────────────────

// atomicWrite writes data to dst using a temp file in dir + rename.
func atomicWrite(dir, dst string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".qenv-*.json.tmp")
	if err != nil {
		return fmt.Errorf("peering/outbox: create temp: %w", err)
	}
	tmpName := tmp.Name()

	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmpName)
		if werr != nil {
			return fmt.Errorf("peering/outbox: write temp: %w", werr)
		}
		return fmt.Errorf("peering/outbox: close temp: %w", cerr)
	}

	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("peering/outbox: rename to %s: %w", dst, err)
	}
	return nil
}

// outboxSortByQueuedAt sorts a slice of QueuedEnvelope oldest-first in place.
func outboxSortByQueuedAt(items []QueuedEnvelope) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].QueuedAt.Before(items[j-1].QueuedAt); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}
