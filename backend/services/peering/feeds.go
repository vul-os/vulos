// feeds.go — signed append-only feed log with pub/sub (PEER-41).
//
// A feed is a hash-chained, Ed25519-signed append-only log owned by a Vula
// identity.  Subscribers pull new entries since a given sequence number.
// Approved contacts receive push notifications over the existing peering layer.
//
// # Storage layout
//
//	~/.vulos/peering/feeds/
//	  └── own/
//	      └── <feed_id>/
//	          ├── meta.json          (FeedMeta — title, access, description)
//	          └── entries/
//	              └── <seq>.json     (FeedEntry — signed, hash-chained)
//
// # Access levels
//
//	public — anyone with the feed URL; no authentication required
//	link   — anyone with the direct feed link; no authentication but not discoverable
//	peers  — approved contacts only (checked via ContactStore.IsApproved)
//
// # Chain integrity
//
// Each entry records prev_hash = sha256(<canonical-JSON of the previous entry>)
// and content_hash = sha256(<canonical-JSON of the entry body fields without
// the "signature" field>).  Verifying the chain means re-computing hashes and
// checking each entry's Ed25519 signature with the author's public key from
// their Vula ID.
//
// # HTTP API (exported via RegisterFeedHandlers)
//
//	POST   /api/feeds                         → create a new feed
//	GET    /api/feeds                         → list own feeds
//	GET    /api/feeds/{feed_id}               → feed metadata
//	POST   /api/feeds/{feed_id}/publish       → publish a new entry
//	GET    /api/feeds/{feed_id}/entries       → list entries (?since=<seq>)
//
// Public/link feeds are served without authentication on the entries endpoint.
// Peers-access feeds require the caller to be in the approved contacts list.
package peering

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ─── Access level constants ────────────────────────────────────────────────────

// FeedAccess defines who can read a feed's entries.
type FeedAccess string

const (
	// FeedAccessPublic makes the feed readable by anyone with the URL.
	FeedAccessPublic FeedAccess = "public"

	// FeedAccessLink makes the feed readable by anyone who has the direct link,
	// but it is not discoverable via the list endpoint.
	FeedAccessLink FeedAccess = "link"

	// FeedAccessPeers restricts the feed to approved contacts only.
	FeedAccessPeers FeedAccess = "peers"
)

// feedValidAccess reports whether a is a valid access level.
func feedValidAccess(a FeedAccess) bool {
	return a == FeedAccessPublic || a == FeedAccessLink || a == FeedAccessPeers
}

// ─── FeedMeta ─────────────────────────────────────────────────────────────────

// FeedMeta holds the descriptor for a feed persisted to meta.json.
type FeedMeta struct {
	// FeedID is the unique identifier for this feed (format: "<vulos_id>/<slug>").
	FeedID string `json:"feed_id"`

	// Author is the owner's Vula ID.
	Author string `json:"author"`

	// Title is a short human-readable name for the feed.
	Title string `json:"title"`

	// Description is an optional longer description.
	Description string `json:"description,omitempty"`

	// Access controls who may read entries.
	Access FeedAccess `json:"access"`

	// CreatedAt is the RFC 3339 UTC timestamp of feed creation.
	CreatedAt string `json:"created_at"`

	// Archived is true after the feed owner has archived the feed.
	// An archived feed accepts no new entries but existing entries remain readable.
	Archived bool `json:"archived,omitempty"`
}

// ─── FeedEntry ────────────────────────────────────────────────────────────────

// FeedEntry is a single signed, hash-chained log entry.
type FeedEntry struct {
	// ID is a unique entry identifier (UUIDv7 or similar).
	ID string `json:"id"`

	// FeedID is the owning feed's identifier.
	FeedID string `json:"feed_id"`

	// Author is the publisher's Vula ID.
	Author string `json:"author"`

	// Sequence is the 1-based monotonically increasing position in the log.
	Sequence int64 `json:"sequence"`

	// Timestamp is the RFC 3339 UTC timestamp of publication.
	Timestamp string `json:"timestamp"`

	// EntryType is a publisher-defined type tag (e.g. "post", "retraction").
	EntryType string `json:"entry_type"`

	// Body is the type-specific content as an opaque JSON blob.
	Body json.RawMessage `json:"body"`

	// PrevHash is the hex-encoded SHA-256 hash of the previous entry's canonical
	// JSON bytes (with "signature" excluded).  Empty for the first entry.
	PrevHash string `json:"prev_hash,omitempty"`

	// ContentHash is the hex-encoded SHA-256 hash of this entry's canonical JSON
	// bytes (with "signature" excluded).  Set by feedComputeContentHash.
	ContentHash string `json:"content_hash"`

	// Signature is the base64url (no-padding) Ed25519 signature of the canonical
	// JSON bytes of this entry with the "signature" field excluded.
	Signature string `json:"signature,omitempty"`
}

// ─── FeedStore ────────────────────────────────────────────────────────────────

// FeedStore manages the on-disk feed storage under ~/.vulos/peering/feeds/own/.
// All public methods are safe for concurrent use.
type FeedStore struct {
	mu       sync.RWMutex
	root     string // ~/.vulos/peering/feeds/own
	priv     ed25519.PrivateKey
	vulosID  string
	contacts *ContactStore // for IsApproved gating on peers-access feeds
}

// feedSubDirName is the subdirectory used for own feeds.
const feedSubDirName = "own"

// NewFeedStore creates a FeedStore rooted at <peeringDir>/feeds/own.
//
// priv is the node's Ed25519 private key used to sign new entries.
// vulosID is the node's own Vula ID (the author identity for published entries).
// contacts is used to gate access to peers-level feeds; may be nil (all peers
// access checks will fail as if no contacts are approved).
func NewFeedStore(peeringDir string, priv ed25519.PrivateKey, vulosID string, contacts *ContactStore) (*FeedStore, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, errors.New("feeds: invalid private key")
	}
	root := filepath.Join(peeringDir, "feeds", feedSubDirName)
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("feeds: mkdir %s: %w", root, err)
	}
	return &FeedStore{
		root:     root,
		priv:     priv,
		vulosID:  vulosID,
		contacts: contacts,
	}, nil
}

// ─── Feed CRUD ────────────────────────────────────────────────────────────────

// FeedCreate creates a new feed with the given metadata.  feedID is generated
// as "<vulosID>/<slug>" where slug is derived from the title.
func (fs *FeedStore) FeedCreate(title, description string, access FeedAccess) (*FeedMeta, error) {
	if title == "" {
		return nil, errors.New("feeds: title must not be empty")
	}
	if !feedValidAccess(access) {
		return nil, fmt.Errorf("feeds: invalid access level %q", access)
	}

	slug := feedSlugify(title)
	feedID := fs.vulosID + "/" + slug

	fs.mu.Lock()
	defer fs.mu.Unlock()

	// Ensure uniqueness by appending a counter if the directory exists.
	base := slug
	for i := 1; ; i++ {
		dir := fs.feedDir(feedID)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		slug = fmt.Sprintf("%s-%d", base, i)
		feedID = fs.vulosID + "/" + slug
	}

	meta := &FeedMeta{
		FeedID:      feedID,
		Author:      fs.vulosID,
		Title:       title,
		Description: description,
		Access:      access,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}

	dir := fs.feedDir(feedID)
	if err := os.MkdirAll(filepath.Join(dir, "entries"), 0700); err != nil {
		return nil, fmt.Errorf("feeds: mkdir entries: %w", err)
	}
	if err := feedWriteMeta(dir, meta); err != nil {
		return nil, err
	}
	return meta, nil
}

// FeedList returns metadata for all own feeds.  Archived feeds are included.
func (fs *FeedStore) FeedList() ([]*FeedMeta, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	entries, err := os.ReadDir(fs.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("feeds: readdir %s: %w", fs.root, err)
	}

	var out []*FeedMeta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// The directory name is the slug (feedSafeID of the feedID).
		// Read meta.json directly from the subdirectory — it contains the full feedID.
		dir := filepath.Join(fs.root, e.Name())
		meta, err := feedReadMeta(dir)
		if err != nil {
			continue // skip corrupt entries
		}
		out = append(out, meta)
	}
	return out, nil
}

// FeedGet returns the metadata for a specific feed by feedID.
func (fs *FeedStore) FeedGet(feedID string) (*FeedMeta, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	dir := fs.feedDir(feedID)
	meta, err := feedReadMeta(dir)
	if err != nil {
		return nil, err
	}
	return meta, nil
}

// ─── Publish ──────────────────────────────────────────────────────────────────

// FeedPublish appends a new signed, hash-chained entry to the feed.
//
// entryType is a publisher-defined type tag (e.g. "post", "retraction").
// body is the entry payload; it must be valid JSON.
//
// The entry is signed with the store's private key and the chain is extended
// by setting PrevHash to the ContentHash of the most recent entry.
func (fs *FeedStore) FeedPublish(feedID, entryType string, body json.RawMessage) (*FeedEntry, error) {
	if feedID == "" {
		return nil, errors.New("feeds: feedID must not be empty")
	}
	if entryType == "" {
		return nil, errors.New("feeds: entryType must not be empty")
	}
	if len(body) == 0 {
		body = json.RawMessage("null")
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()

	meta, err := feedReadMeta(fs.feedDir(feedID))
	if err != nil {
		return nil, err
	}
	if meta.Archived {
		return nil, errors.New("feeds: feed is archived; no new entries accepted")
	}

	// Determine next sequence and previous hash.
	prev, seq, err := fs.feedLastEntry(feedID)
	if err != nil {
		return nil, err
	}
	var prevHash string
	if prev != nil {
		prevHash = prev.ContentHash
	}
	nextSeq := seq + 1

	entry := &FeedEntry{
		ID:        feedNewID(),
		FeedID:    feedID,
		Author:    fs.vulosID,
		Sequence:  nextSeq,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		EntryType: entryType,
		Body:      body,
		PrevHash:  prevHash,
	}

	// Compute content hash (canonical JSON without "signature").
	contentHash, err := feedComputeContentHash(entry)
	if err != nil {
		return nil, fmt.Errorf("feeds: content hash: %w", err)
	}
	entry.ContentHash = contentHash

	// Sign the entry.
	if err := feedSignEntry(entry, fs.priv); err != nil {
		return nil, fmt.Errorf("feeds: sign: %w", err)
	}

	// Persist.
	if err := fs.feedWriteEntry(feedID, entry); err != nil {
		return nil, err
	}

	return entry, nil
}

// ─── Entry retrieval ──────────────────────────────────────────────────────────

// FeedGetEntries returns all entries in the feed with sequence > since.
// since=0 returns all entries.  Entries are returned in ascending sequence order.
func (fs *FeedStore) FeedGetEntries(feedID string, since int64) ([]*FeedEntry, error) {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	return fs.feedLoadEntriesSince(feedID, since)
}

// ─── Chain verification ───────────────────────────────────────────────────────

// FeedVerifyChain re-verifies every entry in the feed: signatures and the
// hash chain.  Returns a non-nil error if any entry is tampered or the chain
// is broken.
func (fs *FeedStore) FeedVerifyChain(feedID string) error {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	entries, err := fs.feedLoadEntriesSince(feedID, 0)
	if err != nil {
		return err
	}

	var prevHash string
	for i, e := range entries {
		// Check sequence is consecutive.
		if e.Sequence != int64(i+1) {
			return fmt.Errorf("feeds: chain broken: sequence gap at position %d (got %d)", i+1, e.Sequence)
		}

		// Check prev_hash links.
		if e.PrevHash != prevHash {
			return fmt.Errorf("feeds: chain broken: entry %d prev_hash mismatch", e.Sequence)
		}

		// Re-compute content hash.
		wantHash, err := feedComputeContentHash(e)
		if err != nil {
			return fmt.Errorf("feeds: chain broken: entry %d hash compute: %w", e.Sequence, err)
		}
		if e.ContentHash != wantHash {
			return fmt.Errorf("feeds: chain broken: entry %d content_hash mismatch", e.Sequence)
		}

		// Verify signature.
		if err := feedVerifyEntry(e); err != nil {
			return fmt.Errorf("feeds: chain broken: entry %d signature invalid: %w", e.Sequence, err)
		}

		prevHash = e.ContentHash
	}
	return nil
}

// ─── Access gating ────────────────────────────────────────────────────────────

// feedCheckAccess returns nil if callerVulosID may read the feed.
//
// public/link feeds: always allowed (callerVulosID may be empty).
// peers feeds: callerVulosID must be an approved contact.
func (fs *FeedStore) feedCheckAccess(meta *FeedMeta, callerVulosID string) error {
	switch meta.Access {
	case FeedAccessPublic, FeedAccessLink:
		return nil
	case FeedAccessPeers:
		if fs.contacts == nil || !fs.contacts.IsApproved(callerVulosID) {
			return fmt.Errorf("feeds: access denied: peers-only feed")
		}
		return nil
	default:
		return fmt.Errorf("feeds: unknown access level %q", meta.Access)
	}
}

// ─── Subscriber push ──────────────────────────────────────────────────────────

// FeedNotifySubscribers signals push to approved-contact subscribers.
// It calls pushFn for every approved contact, passing the new entry.
// Non-contact subscribers poll via the HTTP entries endpoint.
//
// pushFn may be nil (no-op) — this function is safe to call in either case.
func (fs *FeedStore) FeedNotifySubscribers(entry *FeedEntry, pushFn func(contactVulosID string, e *FeedEntry)) {
	if pushFn == nil || fs.contacts == nil {
		return
	}
	approved := fs.contacts.ListByState(StateApproved)
	for _, c := range approved {
		pushFn(c.VulosID, entry)
	}
}

// ─── HTTP handlers ────────────────────────────────────────────────────────────

// feedHandler is the HTTP handler wrapper for the FeedStore.
type feedHandler struct {
	store *FeedStore
}

// RegisterFeedHandlers mounts the feeds API onto mux.
//
// Routes:
//
//	POST   /api/feeds                         → create a new feed
//	GET    /api/feeds                         → list own feeds (public+link visible to all;
//	                                            peers feeds require approved contact)
//	GET    /api/feeds/{feed_id}               → feed metadata
//	POST   /api/feeds/{feed_id}/publish       → publish a new entry
//	GET    /api/feeds/{feed_id}/entries       → list entries (?since=<seq>)
//
// callerVulosID is a function that extracts the caller's Vula ID from an
// authenticated request.  It may return an empty string for unauthenticated
// callers (public/link feeds do not require authentication).
func RegisterFeedHandlers(mux *http.ServeMux, store *FeedStore, callerVulosID func(*http.Request) string) {
	h := &feedHandler{store: store}

	mux.HandleFunc("POST /api/feeds", func(w http.ResponseWriter, r *http.Request) {
		h.handleFeedCreate(w, r)
	})
	mux.HandleFunc("GET /api/feeds", func(w http.ResponseWriter, r *http.Request) {
		h.handleFeedList(w, r, callerVulosID(r))
	})
	mux.HandleFunc("GET /api/feeds/{feed_id}", func(w http.ResponseWriter, r *http.Request) {
		h.handleFeedGet(w, r, callerVulosID(r))
	})
	mux.HandleFunc("POST /api/feeds/{feed_id}/publish", func(w http.ResponseWriter, r *http.Request) {
		h.handleFeedPublish(w, r)
	})
	mux.HandleFunc("GET /api/feeds/{feed_id}/entries", func(w http.ResponseWriter, r *http.Request) {
		h.handleFeedEntries(w, r, callerVulosID(r))
	})
}

// feedCreateRequest is the request body for creating a feed.
type feedCreateRequest struct {
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	Access      FeedAccess `json:"access"`
}

func (h *feedHandler) handleFeedCreate(w http.ResponseWriter, r *http.Request) {
	var req feedCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		feedWriteErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Title == "" {
		feedWriteErr(w, http.StatusBadRequest, "title is required")
		return
	}
	if !feedValidAccess(req.Access) {
		feedWriteErr(w, http.StatusBadRequest, "access must be one of: public, link, peers")
		return
	}
	meta, err := h.store.FeedCreate(req.Title, req.Description, req.Access)
	if err != nil {
		feedWriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, meta)
}

func (h *feedHandler) handleFeedList(w http.ResponseWriter, _ *http.Request, callerID string) {
	all, err := h.store.FeedList()
	if err != nil {
		feedWriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Filter: link/public always visible; peers requires approved contact.
	var visible []*FeedMeta
	for _, m := range all {
		if m.Access == FeedAccessPeers {
			if h.store.contacts == nil || !h.store.contacts.IsApproved(callerID) {
				continue
			}
		}
		visible = append(visible, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"feeds": visible})
}

func (h *feedHandler) handleFeedGet(w http.ResponseWriter, r *http.Request, callerID string) {
	feedID := h.store.feedIDFromPath(r)
	if feedID == "" {
		feedWriteErr(w, http.StatusBadRequest, "feed_id required")
		return
	}
	meta, err := h.store.FeedGet(feedID)
	if err != nil {
		feedWriteErr(w, http.StatusNotFound, err.Error())
		return
	}
	if err := h.store.feedCheckAccess(meta, callerID); err != nil {
		feedWriteErr(w, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

// feedPublishRequest is the request body for publishing a feed entry.
type feedPublishRequest struct {
	EntryType string          `json:"entry_type"`
	Body      json.RawMessage `json:"body"`
}

func (h *feedHandler) handleFeedPublish(w http.ResponseWriter, r *http.Request) {
	feedID := h.store.feedIDFromPath(r)
	if feedID == "" {
		feedWriteErr(w, http.StatusBadRequest, "feed_id required")
		return
	}
	var req feedPublishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		feedWriteErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.EntryType == "" {
		req.EntryType = "post"
	}
	entry, err := h.store.FeedPublish(feedID, req.EntryType, req.Body)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, os.ErrNotExist) {
			code = http.StatusNotFound
		}
		feedWriteErr(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

func (h *feedHandler) handleFeedEntries(w http.ResponseWriter, r *http.Request, callerID string) {
	feedID := h.store.feedIDFromPath(r)
	if feedID == "" {
		feedWriteErr(w, http.StatusBadRequest, "feed_id required")
		return
	}

	meta, err := h.store.FeedGet(feedID)
	if err != nil {
		feedWriteErr(w, http.StatusNotFound, err.Error())
		return
	}
	if err := h.store.feedCheckAccess(meta, callerID); err != nil {
		feedWriteErr(w, http.StatusForbidden, err.Error())
		return
	}

	since := int64(0)
	if s := r.URL.Query().Get("since"); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			feedWriteErr(w, http.StatusBadRequest, "since must be an integer sequence number")
			return
		}
		if v < 0 {
			// Sequence is 1-based (see FeedEntry.Sequence); a negative cursor is
			// not a value any legitimate entry can carry, so reject it explicitly
			// rather than silently treating it the same as since=0.
			feedWriteErr(w, http.StatusBadRequest, "since must not be negative")
			return
		}
		since = v
	}

	entries, err := h.store.FeedGetEntries(feedID, since)
	if err != nil {
		feedWriteErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// ─── Disk helpers ─────────────────────────────────────────────────────────────

// feedDir returns the absolute directory for a feed given its feedID.
// feedID format: "<vulos_id>/<slug>" — we store only the slug portion as the
// directory name to keep paths filesystem-safe (no colons, short names).
func (fs *FeedStore) feedDir(feedID string) string {
	return filepath.Join(fs.root, feedSafeID(feedID))
}

// feedSafeID converts a feedID to a filesystem-safe directory name.
// For feedIDs of the form "<vulos_id>/<slug>", only the slug portion after the
// last "/" is used.  If there is no "/" the whole feedID is returned.
func feedSafeID(feedID string) string {
	if idx := strings.LastIndex(feedID, "/"); idx >= 0 {
		return feedID[idx+1:]
	}
	return feedID
}

// feedWriteMeta serialises meta to <dir>/meta.json atomically.
func feedWriteMeta(dir string, meta *FeedMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("feeds: marshal meta: %w", err)
	}
	return atomicWrite(dir, filepath.Join(dir, "meta.json"), data)
}

// feedReadMeta deserialises meta.json from dir.
func feedReadMeta(dir string) (*FeedMeta, error) {
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, fmt.Errorf("feeds: read meta: %w", err)
	}
	var meta FeedMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("feeds: parse meta: %w", err)
	}
	return &meta, nil
}

// feedWriteEntry persists an entry to <feedDir>/entries/<seq>.json atomically.
func (fs *FeedStore) feedWriteEntry(feedID string, entry *FeedEntry) error {
	dir := filepath.Join(fs.feedDir(feedID), "entries")
	name := fmt.Sprintf("%020d.json", entry.Sequence)
	dst := filepath.Join(dir, name)
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("feeds: marshal entry: %w", err)
	}
	return atomicWrite(dir, dst, data)
}

// feedLoadEntriesSince reads all entry files in ascending sequence order,
// returning those with Sequence > since.  Caller must hold fs.mu (at least
// for reading).
func (fs *FeedStore) feedLoadEntriesSince(feedID string, since int64) ([]*FeedEntry, error) {
	dir := filepath.Join(fs.feedDir(feedID), "entries")
	infos, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("feeds: readdir entries: %w", err)
	}

	// Sort lexicographically (filenames are zero-padded sequence numbers).
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Name() < infos[j].Name()
	})

	var out []*FeedEntry
	for _, info := range infos {
		if info.IsDir() || filepath.Ext(info.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, info.Name()))
		if err != nil {
			continue
		}
		var e FeedEntry
		if err := json.Unmarshal(data, &e); err != nil {
			continue
		}
		// Sequence is documented as the 1-based monotonically increasing
		// position in the log (see FeedEntry.Sequence). encoding/json decodes
		// it as a signed int64, so a corrupt or tampered entry file could in
		// principle carry a non-positive value; treat that the same as any
		// other malformed entry (skip) rather than let it flow into ordering
		// comparisons as if it were a legitimate position. This mirrors the
		// §22.4 ordered-domain invariant: the anti-rollback/monotonicity rule
		// is only sound over the domain FeedEntry.Sequence is documented to
		// occupy (1-based, unsigned in spirit), not whatever encoding/json's
		// signed int64 happens to admit.
		if e.Sequence < 1 {
			continue
		}
		if e.Sequence > since {
			out = append(out, &e)
		}
	}
	return out, nil
}

// feedLastEntry returns the last (highest-sequence) entry in the feed and the
// highest sequence number seen (0 if no entries exist).  Caller must hold
// fs.mu (at least for reading).
func (fs *FeedStore) feedLastEntry(feedID string) (*FeedEntry, int64, error) {
	entries, err := fs.feedLoadEntriesSince(feedID, 0)
	if err != nil {
		return nil, 0, err
	}
	if len(entries) == 0 {
		return nil, 0, nil
	}
	last := entries[len(entries)-1]
	return last, last.Sequence, nil
}

// ─── Cryptographic helpers ────────────────────────────────────────────────────

// feedEntryCanonicalBytes returns the canonical JSON bytes of the entry with
// the "signature" and "content_hash" fields omitted.
//
// Both fields are excluded so that:
//   - "signature" is excluded because the signature is computed over (and
//     stored in) the same object — standard Ed25519 sign pattern.
//   - "content_hash" is excluded because it is derived FROM these canonical
//     bytes; including it would create a circular dependency.
//
// This byte string is the input to both feedComputeContentHash and
// feedSignEntry/feedVerifyEntry.
func feedEntryCanonicalBytes(entry *FeedEntry) ([]byte, error) {
	raw, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	delete(m, "signature")
	delete(m, "content_hash")
	return canonicaliseObject(m)
}

// feedComputeContentHash computes SHA-256(canonical bytes without "signature")
// and returns the hex-encoded digest.
func feedComputeContentHash(entry *FeedEntry) (string, error) {
	canonical, err := feedEntryCanonicalBytes(entry)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// feedSignEntry signs the entry's canonical bytes (signature excluded) with priv
// and stores the base64url-encoded signature in entry.Signature.
func feedSignEntry(entry *FeedEntry, priv ed25519.PrivateKey) error {
	canonical, err := feedEntryCanonicalBytes(entry)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(priv, canonical)
	entry.Signature = base64.RawURLEncoding.EncodeToString(sig)
	return nil
}

// feedVerifyEntry verifies the Ed25519 signature of a FeedEntry.
// The author's public key is extracted from entry.Author (a Vula ID).
func feedVerifyEntry(entry *FeedEntry) error {
	if entry.Signature == "" {
		return errors.New("feeds: signature absent")
	}
	pub, err := decodeVulosID(entry.Author)
	if err != nil {
		return fmt.Errorf("feeds: decode author vulos_id: %w", err)
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(entry.Signature)
	if err != nil {
		return fmt.Errorf("feeds: decode signature: %w", err)
	}
	if len(sigBytes) != ed25519.SignatureSize {
		return fmt.Errorf("feeds: signature length %d, want %d", len(sigBytes), ed25519.SignatureSize)
	}
	canonical, err := feedEntryCanonicalBytes(entry)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, canonical, sigBytes) {
		return errors.New("feeds: signature mismatch")
	}
	return nil
}

// ─── Miscellaneous helpers ────────────────────────────────────────────────────

// feedSlugify converts a title into a URL-safe slug.
func feedSlugify(title string) string {
	out := make([]byte, 0, len(title))
	for _, b := range []byte(title) {
		switch {
		case b >= 'a' && b <= 'z':
			out = append(out, b)
		case b >= 'A' && b <= 'Z':
			out = append(out, b+('a'-'A'))
		case b >= '0' && b <= '9':
			out = append(out, b)
		default:
			// Replace non-alphanumeric chars with '-', collapsing runs.
			if len(out) > 0 && out[len(out)-1] != '-' {
				out = append(out, '-')
			}
		}
	}
	// Trim trailing dash.
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	if len(out) == 0 {
		out = []byte("feed")
	}
	return string(out)
}

// feedNewID generates a time-based pseudo-unique entry ID.
// Format: "feed-<unix-nano-hex>".
func feedNewID() string {
	now := time.Now().UnixNano()
	return fmt.Sprintf("feed-%016x", now)
}

// feedIDFromPath extracts the feed_id path segment from the request and
// returns the full feedID by prepending the store owner's vulosID.
//
// Feed URLs use only the slug (last segment of the feedID) to avoid "/" in
// URL path segments.  The handler reconstructs the full feedID as
// "<vulosID>/<slug>".
func (fs *FeedStore) feedIDFromPath(r *http.Request) string {
	slug := r.PathValue("feed_id")
	if slug == "" {
		return ""
	}
	return fs.vulosID + "/" + slug
}

// feedWriteErr writes a JSON error response for the feeds API.
func feedWriteErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
