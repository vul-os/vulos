// Package upload implements resumable, chunked file upload for the OS box using
// tus.io-style protocol semantics (Upload-Length / Upload-Offset /
// Upload-Metadata / POST-create / HEAD-offset / PATCH-append).
//
// Why this exists (see vulos-relay/docs/CONSOLIDATION.md §A-2): a single HTTP
// upload that rides the relay is capped at the relay's MaxRequestBytes. Rather
// than raise that cap without bound, the BOX APP chunks large files: the client
// creates an upload, then PATCHes bounded chunks (each ≤ the relay cap) that
// each pass the relay as an ordinary request with full rate-limit/timeout
// coverage. The relay stays a dumb pipe and needs NO changes.
//
// Design:
//   - An upload is created with a declared total Length and Metadata (filename,
//     content-type, target parent, optional whole-file sha256). It is OWNED by
//     the creating user; every subsequent HEAD/PATCH is owner-checked so one
//     user can never read or extend another user's partial upload (per-owner
//     isolation, matching the rest of the Files plane).
//   - Chunk bytes are appended to a staging file under <stateDir>/uploads/ on
//     the box's own /data. The committed offset is persisted in SQLite so a
//     dropped connection resumes exactly where it left off (the client HEADs to
//     learn the offset, then PATCHes from there).
//   - All client-supplied offsets/lengths are bounded and validated against the
//     persisted state (untrusted input): a PATCH whose offset != the stored
//     offset is a 409 (tus conflict); a chunk that would exceed the declared
//     Length is rejected; a negative/oversized Length at create is rejected.
//   - Integrity: an optional per-chunk Upload-Checksum (sha256) is verified
//     before the chunk is committed to disk; an optional whole-file sha256 in
//     the create Metadata is verified over the fully-assembled staging file
//     before it is promoted into the Files Drive. A mismatch fails the upload
//     without touching the user's Drive.
//   - On completion the assembled bytes are streamed through the Files service
//     (UploadGrant → PutContent → Commit), so the object lands in the user's OWN
//     bucket/Drive with the same ACL-gated data plane a single-shot upload uses.
//   - Abandoned partials are swept: an upload not touched within TTL is deleted
//     (row + staging file), so a client that walks away never leaks disk.
package upload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"vulos/backend/internal/ulid"
)

// Protocol version this box speaks. Clients send/expect Tus-Resumable: 1.0.0.
const TusVersion = "1.0.0"

// Sentinel errors mapped to HTTP status by the route layer.
var (
	// ErrNotFound: no such upload (or it belongs to another owner — we 404
	// cross-owner so a partial upload's existence never leaks).
	ErrNotFound = errors.New("upload: not found")
	// ErrForbidden: the upload exists but is owned by someone else. The route
	// layer collapses this to 404 for anti-enumeration; kept distinct for tests.
	ErrForbidden = errors.New("upload: forbidden")
	// ErrOffsetConflict: PATCH offset does not match the server's committed
	// offset (tus 409 Conflict). The client must HEAD and resume from there.
	ErrOffsetConflict = errors.New("upload: offset conflict")
	// ErrTooLarge: the chunk would push the upload past its declared Length, or
	// the declared Length exceeds MaxUploadBytes.
	ErrTooLarge = errors.New("upload: exceeds declared length")
	// ErrChecksum: a per-chunk or whole-file integrity check failed.
	ErrChecksum = errors.New("upload: checksum mismatch")
	// ErrInvalid: a malformed/negative length, bad metadata, etc.
	ErrInvalid = errors.New("upload: invalid request")
	// ErrAlreadyComplete: PATCH against an already-finalized upload.
	ErrAlreadyComplete = errors.New("upload: already complete")
	// ErrSinkUnavailable: no Files sink wired (upload cannot be promoted).
	ErrSinkUnavailable = errors.New("upload: files sink unavailable")
	// ErrTooManyUploads: the owner already holds MaxIncompletePerOwner in-flight
	// uploads; they must finish or cancel one before creating another (disk-fill
	// DoS guard on a multi-user box).
	ErrTooManyUploads = errors.New("upload: too many concurrent uploads")
)

// Sink is the seam to the Files service. On completion the manager creates the
// target node (owner-scoped, ACL-gated inside the Files service), streams the
// assembled bytes into the owner's bucket, and records a version. Implemented in
// production by an adapter over files.Service (UploadGrant→PutContent→Commit).
// It is an interface so the manager is testable without a bucket.
type Sink interface {
	// Store places the fully-assembled bytes for userID into parentID under name
	// with contentType, reading exactly size bytes from r. It returns the created
	// node id. It MUST enforce the user's write authorization on parentID; the
	// manager never bypasses the Files ACL.
	Store(ctx context.Context, userID, parentID, name, contentType string, r io.Reader, size int64) (nodeID string, err error)
}

// Config tunes the manager. Zero values fall back to sane defaults.
type Config struct {
	// StateDir is the box's own data dir; staging files live under
	// StateDir/uploads/. Created if missing.
	StateDir string
	// MaxUploadBytes caps a single resumable upload's declared total size. 0 ⇒
	// DefaultMaxUploadBytes. A create declaring more is rejected (ErrTooLarge).
	MaxUploadBytes int64
	// MaxChunkBytes caps one PATCH body. It should track the relay's
	// MaxRequestBytes so a chunk never trips the relay cap. 0 ⇒ DefaultMaxChunk.
	MaxChunkBytes int64
	// TTL bounds how long an untouched partial upload survives before the sweeper
	// deletes it. 0 ⇒ DefaultTTL.
	TTL time.Duration
	// now is injectable for tests; nil ⇒ time.Now.
	now func() time.Time
}

const (
	// DefaultMaxUploadBytes — a generous single-upload ceiling (5 GiB). Chunking
	// removes the per-request ceiling; this only bounds one logical upload so a
	// single client cannot declare an unbounded reservation.
	DefaultMaxUploadBytes = 5 << 30
	// DefaultMaxChunk — one PATCH body cap. 64 MiB stays comfortably under the
	// relay's raised 256 MiB request cap (§A-1) with headroom for headers.
	DefaultMaxChunk = 64 << 20
	// DefaultTTL — abandoned-partial expiry.
	DefaultTTL = 24 * time.Hour
	// MaxIncompletePerOwner caps how many in-flight (incomplete) resumable uploads
	// one owner may hold at once. It bounds staging-disk reservation per user so a
	// single account on a multi-user box cannot open unbounded uploads and fill
	// /data before the sweeper reaps them. Completing or cancelling an upload frees
	// a slot immediately.
	MaxIncompletePerOwner = 32
)

// Manager owns the resumable-upload lifecycle: create, HEAD, PATCH, finalize,
// and sweep. Safe for concurrent use. Per-upload appends are serialized by an
// in-process lock so two racing PATCHes on the same upload cannot interleave
// writes to the staging file (offset checks + disk append are atomic per id).
type Manager struct {
	cfg     Config
	store   *uStore
	sink    Sink
	stageIn string // StateDir/uploads

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per-upload append lock
}

// New opens (creating+migrating) the upload state DB at dbPath and returns a
// Manager. sink may be nil in tests that don't finalize; a nil sink makes a
// completing PATCH return ErrSinkUnavailable (the bytes stay staged, resumable).
func New(dbPath string, sink Sink, cfg Config) (*Manager, error) {
	if cfg.MaxUploadBytes <= 0 {
		cfg.MaxUploadBytes = DefaultMaxUploadBytes
	}
	if cfg.MaxChunkBytes <= 0 {
		cfg.MaxChunkBytes = DefaultMaxChunk
	}
	if cfg.TTL <= 0 {
		cfg.TTL = DefaultTTL
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	if cfg.StateDir == "" {
		return nil, fmt.Errorf("upload: StateDir required")
	}
	stageIn := filepath.Join(cfg.StateDir, "uploads")
	if err := os.MkdirAll(stageIn, 0o700); err != nil {
		return nil, fmt.Errorf("upload: mkdir staging: %w", err)
	}
	st, err := openStore(dbPath)
	if err != nil {
		return nil, err
	}
	return &Manager{cfg: cfg, store: st, sink: sink, stageIn: stageIn, locks: map[string]*sync.Mutex{}}, nil
}

// Close releases the state DB.
func (m *Manager) Close() error { return m.store.close() }

// MaxChunkBytes is the per-PATCH body cap; the route layer uses it to bound the
// MaxBytesReader on a chunk body.
func (m *Manager) MaxChunkBytes() int64 { return m.cfg.MaxChunkBytes }

// Upload is the public view of a resumable upload's state.
type Upload struct {
	ID          string
	OwnerID     string
	Length      int64  // declared total size
	Offset      int64  // committed bytes so far
	ParentID    string // Files parent to promote into
	Name        string
	ContentType string
	SHA256      string // optional whole-file checksum (hex); "" if not provided
	Complete    bool
	NodeID      string // set once promoted into the Drive
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// CreateParams is the validated input to Create.
type CreateParams struct {
	Length      int64
	ParentID    string
	Name        string
	ContentType string
	SHA256      string // optional hex sha256 of the whole file
}

// Create reserves a new upload for userID. It validates the declared Length
// against MaxUploadBytes, requires a name, and (if given) validates the whole-
// file checksum is well-formed hex. The staging file is created empty. The
// returned Upload has Offset 0.
func (m *Manager) Create(userID string, p CreateParams) (*Upload, error) {
	if userID == "" {
		return nil, ErrForbidden
	}
	if p.Length < 0 || p.Length > m.cfg.MaxUploadBytes {
		return nil, fmt.Errorf("%w: length %d out of range (0..%d)", ErrTooLarge, p.Length, m.cfg.MaxUploadBytes)
	}
	if p.Name == "" {
		return nil, fmt.Errorf("%w: name required", ErrInvalid)
	}
	if p.SHA256 != "" && !validHexSHA(p.SHA256) {
		return nil, fmt.Errorf("%w: bad sha256", ErrInvalid)
	}
	// Cap concurrent in-flight uploads per owner so one user cannot pin unbounded
	// staging files (disk-fill DoS on a multi-user box). Completing/cancelling frees
	// a slot; abandoned partials also age out via the sweeper.
	if n, err := m.store.countIncompleteByOwner(userID); err != nil {
		return nil, err
	} else if n >= MaxIncompletePerOwner {
		return nil, fmt.Errorf("%w: %d in flight (max %d)", ErrTooManyUploads, n, MaxIncompletePerOwner)
	}
	now := m.cfg.now()
	u := &Upload{
		ID:          ulid.NewULID(),
		OwnerID:     userID,
		Length:      p.Length,
		Offset:      0,
		ParentID:    p.ParentID,
		Name:        p.Name,
		ContentType: p.ContentType,
		SHA256:      p.SHA256,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	// Create the empty staging file up front so a PATCH never has to guess the
	// path and a sweep can always find bytes to remove.
	f, err := os.OpenFile(m.stagePath(u.ID), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("upload: create staging: %w", err)
	}
	f.Close()
	if err := m.store.insert(u); err != nil {
		os.Remove(m.stagePath(u.ID))
		return nil, err
	}
	return u, nil
}

// Head returns the current state of an upload owned by userID. A cross-owner or
// missing id yields ErrNotFound (existence is not leaked to other owners).
func (m *Manager) Head(userID, id string) (*Upload, error) {
	u, err := m.store.get(id)
	if err != nil {
		return nil, err
	}
	if u.OwnerID != userID {
		return nil, ErrNotFound
	}
	return u, nil
}

// PatchResult reports the new committed offset and whether the upload finished.
type PatchResult struct {
	Offset   int64
	Complete bool
	NodeID   string // set when Complete
}

// Patch appends a chunk to the upload identified by id, owned by userID. offset
// is the client's claimed write position and MUST equal the server's committed
// offset (tus), else ErrOffsetConflict — the guard against gaps/overlaps from a
// retried or misordered chunk. chunkSHA (optional hex sha256 of THIS chunk) is
// verified before the bytes are committed. The chunk is bounded to MaxChunkBytes
// by the route layer; here we additionally refuse any bytes that would exceed
// the declared Length. When the committed offset reaches Length the upload is
// finalized: the whole-file checksum (if any) is verified and the assembled
// bytes are promoted into the Files Drive via the Sink.
func (m *Manager) Patch(ctx context.Context, userID, id string, offset int64, r io.Reader, chunkSHA string) (*PatchResult, error) {
	// Serialize per-upload so concurrent PATCHes can't interleave appends.
	lk := m.lockFor(id)
	lk.Lock()
	defer lk.Unlock()

	u, err := m.store.get(id)
	if err != nil {
		return nil, err
	}
	if u.OwnerID != userID {
		return nil, ErrNotFound
	}
	if u.Complete {
		return nil, ErrAlreadyComplete
	}
	if offset != u.Offset {
		return nil, fmt.Errorf("%w: have %d, got %d", ErrOffsetConflict, u.Offset, offset)
	}
	if chunkSHA != "" && !validHexSHA(chunkSHA) {
		return nil, fmt.Errorf("%w: bad chunk checksum", ErrInvalid)
	}
	remaining := u.Length - u.Offset
	if remaining <= 0 {
		// Already full but not marked complete (e.g. a prior finalize failed):
		// re-attempt finalize below with no new bytes.
		remaining = 0
	}

	// Buffer the chunk to a temp file first when a per-chunk checksum is present,
	// so a corrupt chunk never lands in the staging file. Without a checksum we
	// stream straight to the staging file (bounded by remaining) to avoid a copy.
	written, err := m.appendChunk(u, r, remaining, chunkSHA)
	if err != nil {
		return nil, err
	}
	u.Offset += written
	if err := m.store.setOffset(u.ID, u.Offset, m.cfg.now()); err != nil {
		return nil, err
	}

	res := &PatchResult{Offset: u.Offset}
	if u.Offset >= u.Length {
		nodeID, ferr := m.finalize(ctx, u)
		if ferr != nil {
			return res, ferr
		}
		res.Complete = true
		res.NodeID = nodeID
	}
	return res, nil
}

// appendChunk writes at most `remaining` bytes from r to u's staging file,
// verifying chunkSHA over exactly the bytes written when provided. It returns
// the number of bytes committed. It refuses to write past `remaining` (declared
// Length): reading more than that is a client protocol violation (ErrTooLarge).
func (m *Manager) appendChunk(u *Upload, r io.Reader, remaining int64, chunkSHA string) (int64, error) {
	// Bound the reader so a client can never write past the declared Length even
	// if the route-level MaxBytesReader is larger than what remains.
	// +1 so we can DETECT an over-long body (read returns the extra byte).
	lr := io.LimitReader(r, remaining+1)

	f, err := os.OpenFile(m.stagePath(u.ID), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fmt.Errorf("upload: open staging: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	// Tee through the hasher only when we need to verify.
	var src io.Reader = lr
	if chunkSHA != "" {
		src = io.TeeReader(lr, h)
	}

	// When verifying, we must not commit corrupt bytes: write to a temp file,
	// verify, then append. Otherwise stream directly (append is the commit).
	if chunkSHA != "" {
		tmp, terr := os.CreateTemp(m.stageIn, "chunk-*.tmp")
		if terr != nil {
			return 0, fmt.Errorf("upload: temp chunk: %w", terr)
		}
		tmpName := tmp.Name()
		defer os.Remove(tmpName)
		n, cerr := io.Copy(tmp, src)
		tmp.Close()
		if cerr != nil {
			return 0, fmt.Errorf("upload: buffer chunk: %w", cerr)
		}
		if n > remaining {
			return 0, fmt.Errorf("%w: chunk overruns declared length", ErrTooLarge)
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != chunkSHA {
			return 0, fmt.Errorf("%w: chunk sha256 %s != %s", ErrChecksum, got, chunkSHA)
		}
		tf, oerr := os.Open(tmpName)
		if oerr != nil {
			return 0, oerr
		}
		defer tf.Close()
		if _, werr := io.Copy(f, tf); werr != nil {
			return 0, fmt.Errorf("upload: commit chunk: %w", werr)
		}
		if serr := f.Sync(); serr != nil {
			return 0, serr
		}
		return n, nil
	}

	n, cerr := io.Copy(f, src)
	if cerr != nil {
		// Roll the offset forward only by what actually hit disk: truncate any
		// partial tail so the staging length always equals the committed offset.
		_ = f.Truncate(u.Offset + n)
		return 0, fmt.Errorf("upload: write chunk: %w", cerr)
	}
	if n > remaining {
		// Over-long body: truncate back to the declared length and reject.
		_ = f.Truncate(u.Offset + remaining)
		return 0, fmt.Errorf("%w: chunk overruns declared length", ErrTooLarge)
	}
	if serr := f.Sync(); serr != nil {
		return 0, serr
	}
	return n, nil
}

// finalize verifies the whole-file checksum (if declared) over the assembled
// staging file, promotes the bytes into the owner's Drive via the Sink, records
// the resulting node id, marks the upload complete, and removes the staging
// file. It is idempotent-ish: a re-entry after a failed promote re-verifies and
// re-promotes (the Sink creates/locates the same node by name).
func (m *Manager) finalize(ctx context.Context, u *Upload) (string, error) {
	if m.sink == nil {
		return "", ErrSinkUnavailable
	}
	// Verify whole-file integrity before touching the user's Drive.
	if u.SHA256 != "" {
		got, err := hashFile(m.stagePath(u.ID))
		if err != nil {
			return "", err
		}
		if got != u.SHA256 {
			return "", fmt.Errorf("%w: file sha256 %s != %s", ErrChecksum, got, u.SHA256)
		}
	}
	f, err := os.Open(m.stagePath(u.ID))
	if err != nil {
		return "", fmt.Errorf("upload: open assembled: %w", err)
	}
	defer f.Close()
	nodeID, err := m.sink.Store(ctx, u.OwnerID, u.ParentID, u.Name, u.ContentType, f, u.Length)
	if err != nil {
		return "", err
	}
	if err := m.store.complete(u.ID, nodeID, m.cfg.now()); err != nil {
		return nodeID, err
	}
	// Staging bytes are now redundant; a failed remove is non-fatal (the sweeper
	// will reap it and complete rows are skipped by the sweeper only after this).
	_ = os.Remove(m.stagePath(u.ID))
	return nodeID, nil
}

// Delete removes an upload owned by userID (row + staging file). Used for an
// explicit client cancel (tus DELETE) and by the sweeper for expiry.
func (m *Manager) Delete(userID, id string) error {
	u, err := m.store.get(id)
	if err != nil {
		return err
	}
	if u.OwnerID != userID {
		return ErrNotFound
	}
	return m.deleteInternal(id)
}

func (m *Manager) deleteInternal(id string) error {
	_ = os.Remove(m.stagePath(id))
	return m.store.delete(id)
}

// Sweep deletes partial (incomplete) uploads whose UpdatedAt is older than TTL,
// plus any completed rows older than TTL (bookkeeping). Returns the count
// removed. Safe to call periodically; it takes each upload's per-id lock so it
// never races an in-flight PATCH.
func (m *Manager) Sweep() (int, error) {
	cutoff := m.cfg.now().Add(-m.cfg.TTL)
	ids, err := m.store.staleIDs(cutoff)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		lk := m.lockFor(id)
		lk.Lock()
		// Re-check under the lock: a PATCH may have touched it since staleIDs.
		u, gerr := m.store.get(id)
		if gerr == nil && u.UpdatedAt.Before(cutoff) {
			if derr := m.deleteInternal(id); derr == nil {
				n++
			}
		}
		lk.Unlock()
	}
	return n, nil
}

// SweepLoop runs Sweep every interval until ctx is cancelled. Intended to be
// started in a goroutine by the server wiring.
func (m *Manager) SweepLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = m.Sweep()
		}
	}
}

func (m *Manager) stagePath(id string) string {
	// id is a ULID (Crockford base32) so it is path-safe by construction; guard
	// anyway by stripping any separators a caller could sneak in.
	return filepath.Join(m.stageIn, filepath.Base(id)+".part")
}

func (m *Manager) lockFor(id string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	lk := m.locks[id]
	if lk == nil {
		lk = &sync.Mutex{}
		m.locks[id] = lk
	}
	return lk
}

// hashFile returns the hex sha256 of a file's full contents.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// validHexSHA reports whether s is a lowercase 64-char hex sha256 digest.
func validHexSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
