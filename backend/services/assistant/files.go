package assistant

// files.go — READ-ONLY FILES awareness for the sovereign assistant (Wave 55).
//
// This adds the assistant's ability to answer "find my file about X",
// "what's in my notes doc", "summarize the Q3 plan" by reading the user's OWN
// Drive from the OS Files service (services/files, the ACL'd viewer<editor<owner
// store the assistant runs alongside as the same user). It is strictly ADDITIVE
// and READ-ONLY:
//
//   - find_file  → FindFiles: search the user's files by name/query.
//   - read_file  → ReadFile:  read a text file's content, SIZE-CAPPED, refusing
//                  binary/oversized files, returned to the model wrapped in the
//                  SAME wrapUntrusted delimiters (file content is untrusted data).
//
// SECURITY INVARIANTS (all preserved from earlier waves):
//   - The assistant acts AS THE USER: every read is scoped to auth.UserID and the
//     Files service enforces its ACL (owner/editor/viewer). We NEVER bypass the
//     ACL — FilesBackend.Search only returns the user's own + shared-with-them
//     nodes, and ReadContent 403s (ErrForbidden) any node the user can't read.
//     A crafted id/path cannot traverse outside the user's tree (ids are opaque
//     ULIDs resolved through the ACL; no filesystem path is walked here).
//   - File content is UNTRUSTED: read_file's result is wrapped in the wave-13
//     untrusted-content delimiters by the agent loop, so prompt-injection inside a
//     file is framed as data, never obeyed. The ledger + id-only /execute still
//     gate any mutating action; this seam adds NO mutating surface.
//   - The Guard/tier egress choke point is untouched: FILES reads are on-box and
//     the model call that consumes them is still Guarded up-front in AgentTurn —
//     an egress-blocked tier makes zero model calls and thus zero file reads reach
//     any endpoint (the tool loop never runs).
//   - Reads are SIZE-BOUNDED (maxFileReadBytes) so a huge file cannot OOM the box
//     or blow the model context.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// maxFileReadBytes caps how many bytes read_file will pull from a file. Content
// beyond this is truncated (with a marker) — the assistant reads the HEAD of a
// document, which is enough to identify/summarize it, and never streams an
// unbounded file into memory or the model context.
const maxFileReadBytes = 32 << 10 // 32 KiB

// maxFileCandidates caps how many files find_file returns.
const maxFileCandidates = 25

// FileRef is a read-only Drive entry the assistant surfaces to the model. It is
// the normalized, provenance-untrusted subset of a Files node (name/path/type/
// size/modified) — enough to identify a file and to pass its id to read_file.
// File names are user/other-people's text, so anything derived from a FileRef is
// still fed back through wrapUntrusted like every tool result.
type FileRef struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Path     string    `json:"path,omitempty"`
	IsDir    bool      `json:"is_dir,omitempty"`
	Size     int64     `json:"size,omitempty"`
	Modified time.Time `json:"modified,omitempty"`
}

// FileContent is the bounded result of reading a text file: the identifying
// metadata plus the (possibly truncated) UTF-8 text. Truncated reports whether
// the file was longer than maxFileReadBytes.
type FileContent struct {
	Ref       FileRef `json:"ref"`
	Text      string  `json:"text"`
	Truncated bool    `json:"truncated,omitempty"`
}

// FilesSource is the READ-ONLY seam over the user's Drive. It has NO write/
// delete/share method by design: the assistant can read files and may PROPOSE a
// (ledger-gated) action that references one, but never mutates files directly.
// The default implementation (OSFilesSource) reads the OS Files service
// in-process; a fixture backs offline demos and tests.
type FilesSource interface {
	// FindFiles searches the user's Drive for files/folders matching query (a name
	// substring), returning up to limit refs. An empty query lists recent files.
	// ACL-enforced by the backend: only the user's own + shared-with-them nodes.
	FindFiles(ctx context.Context, auth Auth, query string, limit int) ([]FileRef, error)
	// ReadFile reads a text file's content by node id, capped at maxBytes. It
	// refuses folders, oversized files, and non-text/binary content. ACL-enforced:
	// a node the user cannot read returns an error (never its bytes).
	ReadFile(ctx context.Context, auth Auth, id string, maxBytes int) (FileContent, error)
}

// FilesBackend is the minimal READ surface of *files.Service the assistant needs.
// Declaring it here (satisfied as-is by *files.Service) keeps services/assistant
// free of an import on services/files and makes the ACL enforcement mockable in
// tests. BOTH methods are ACL-gated inside files.Service; this seam never sees an
// unauthorized node. It is deliberately read-only — there is no write method.
type FilesBackend interface {
	// Search returns up to limit nodes VISIBLE to userID matching query (own +
	// shared-with-them). ACL-safe by construction in files.Service.
	Search(userID, query string, limit int) ([]FileNode, error)
	// ReadContent opens a node's bytes for reading, ACL-gated to viewer+. The
	// caller MUST Close the reader. Returns ErrForbidden-equivalent (a non-nil
	// error) for a node the user cannot read or that does not exist.
	ReadContent(ctx context.Context, userID, nodeID string) (rc io.ReadCloser, node FileNode, size int64, err error)
}

// FileNode is the provider-agnostic node shape the FilesBackend yields — the
// subset of files.Node the assistant reads. It exists so services/assistant does
// not depend on the concrete files.Node type.
type FileNode struct {
	ID          string
	Name        string
	Path        string
	IsDir       bool
	Size        int64
	ContentType string
	UpdatedAt   time.Time
}

func (n FileNode) toRef() FileRef {
	return FileRef{ID: n.ID, Name: n.Name, Path: n.Path, IsDir: n.IsDir, Size: n.Size, Modified: n.UpdatedAt}
}

// OSFilesSource reads the OS Files service in-process, acting AS THE USER
// (auth.UserID). It NEVER passes anything but the request's own user id to the
// backend, so the backend's ACL is the sole authority on what is visible — the
// assistant cannot read another user's files.
type OSFilesSource struct {
	backend FilesBackend
}

// NewOSFilesSource wraps a FilesBackend (production: *files.Service). backend may
// be nil, in which case the source degrades to "no files available" rather than
// crashing (mirrors the mail source's graceful degradation).
func NewOSFilesSource(backend FilesBackend) *OSFilesSource {
	return &OSFilesSource{backend: backend}
}

func (s *OSFilesSource) FindFiles(ctx context.Context, auth Auth, query string, limit int) ([]FileRef, error) {
	if s == nil || s.backend == nil {
		return nil, nil // files feature not wired ⇒ empty, never an error
	}
	uid := strings.TrimSpace(auth.UserID)
	if uid == "" {
		return nil, fmt.Errorf("files: no user identity")
	}
	if limit <= 0 || limit > maxFileCandidates {
		limit = maxFileCandidates
	}
	nodes, err := s.backend.Search(uid, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]FileRef, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.toRef())
	}
	return out, nil
}

func (s *OSFilesSource) ReadFile(ctx context.Context, auth Auth, id string, maxBytes int) (FileContent, error) {
	if s == nil || s.backend == nil {
		return FileContent{}, fmt.Errorf("files service not available")
	}
	uid := strings.TrimSpace(auth.UserID)
	if uid == "" {
		return FileContent{}, fmt.Errorf("files: no user identity")
	}
	if strings.TrimSpace(id) == "" {
		return FileContent{}, fmt.Errorf("file id is required")
	}
	if maxBytes <= 0 || maxBytes > maxFileReadBytes {
		maxBytes = maxFileReadBytes
	}
	// ACL is enforced HERE by the backend: ReadContent 403s a node the user cannot
	// read (or that is missing) — we get an error, never its bytes.
	rc, node, size, err := s.backend.ReadContent(ctx, uid, id)
	if err != nil {
		return FileContent{}, err
	}
	defer rc.Close()

	if node.IsDir {
		return FileContent{}, fmt.Errorf("that is a folder, not a readable file")
	}
	// Reject obviously-binary content by declared type before reading bytes.
	if isBinaryContentType(node.ContentType) {
		return FileContent{}, fmt.Errorf("file %q is not a text file (%s)", node.Name, node.ContentType)
	}

	// Read at most maxBytes+1 so we can tell whether the file was truncated.
	buf, err := io.ReadAll(io.LimitReader(rc, int64(maxBytes)+1))
	if err != nil {
		return FileContent{}, err
	}
	truncated := false
	if len(buf) > maxBytes {
		// A byte-boundary cut can split a trailing multi-byte UTF-8 rune. Left in
		// place, that dangling partial rune fails the utf8.Valid() sniff below and
		// a perfectly valid non-ASCII text file (CJK, accented text, emoji) larger
		// than the cap would be wrongly rejected as "binary". Drop the partial
		// trailing rune so only genuinely-binary content is refused.
		buf = trimPartialTrailingRune(buf[:maxBytes])
		truncated = true
	} else if size > int64(maxBytes) {
		// Declared size exceeds the cap even if the reader returned less.
		truncated = true
	}

	// Content-sniff: refuse binary payloads (NUL bytes / not-valid-UTF-8) that
	// slipped past the content-type check, so we never dump raw bytes into the
	// model context.
	if looksBinary(buf) {
		return FileContent{}, fmt.Errorf("file %q appears to be binary, not text", node.Name)
	}

	return FileContent{Ref: node.toRef(), Text: string(buf), Truncated: truncated}, nil
}

// isBinaryContentType reports whether a declared content-type is clearly a
// non-text (binary) format the assistant should not attempt to read as text.
// Text-ish types (text/*, and the common structured-text application/* like
// json/xml/yaml/javascript/csv) are allowed through; everything else is refused.
func isBinaryContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if ct == "" {
		return false // unknown ⇒ fall through to the content sniff
	}
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if strings.HasPrefix(ct, "text/") {
		return false
	}
	switch ct {
	case "application/json", "application/xml", "application/yaml", "application/x-yaml",
		"application/javascript", "application/ecmascript", "application/csv",
		"application/toml", "application/x-sh", "application/markdown", "application/x-tex":
		return false
	}
	return true
}

// trimPartialTrailingRune drops a dangling partial multi-byte UTF-8 rune that a
// byte-boundary cut may have split off the end of b (at most utf8.UTFMax-1
// bytes). It never removes more than one rune's worth of trailing bytes, so
// genuinely-binary content (invalid bytes elsewhere, or a NUL) is still detected
// by the looksBinary sniff — this only rescues valid UTF-8 text that happened to
// be cut mid-rune at the size cap.
func trimPartialTrailingRune(b []byte) []byte {
	for i := 0; i < utf8.UTFMax-1 && len(b) > 0; i++ {
		// DecodeLastRune returns (RuneError, 1) only for an invalid trailing
		// encoding — exactly a rune split by the cut. A cleanly-decoding final
		// rune (any length, incl. a real U+FFFD which decodes with size 3) stops us.
		if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size != 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return b
}

// looksBinary reports whether a byte slice is not safe to treat as UTF-8 text:
// it contains a NUL byte or is not valid UTF-8. Used as the last-line content
// sniff so no raw binary reaches the model context.
func looksBinary(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return !utf8.Valid(b)
}
