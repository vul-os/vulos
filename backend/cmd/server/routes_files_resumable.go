package main

// routes_files_resumable.go — RESUMABLE (tus-style) chunked upload control plane
// for the OS Files service. See vulos-relay/docs/CONSOLIDATION.md §A-2.
//
// Motivation: a single upload that rides the relay is capped at the relay's
// MaxRequestBytes. Instead of raising that cap without bound, the BOX chunks
// large files: the client creates an upload here, then PATCHes bounded chunks
// (each ≤ the relay cap) that each pass the relay as an ordinary request with
// full rate-limit/timeout coverage. The relay needs NO changes — every chunk is
// a normal ≤-cap HTTP request. The box reassembles into its OWN /data + bucket,
// tracks the committed offset for resume-after-interruption, verifies per-chunk
// and whole-file integrity, and sweeps abandoned partials.
//
// This is ADDITIVE: the existing single-shot upload-grant / PUT /content path is
// untouched; large files opt into this endpoint and small files keep the old
// path. Identity ALWAYS comes from the session-derived X-User-ID header (never
// the body); every HEAD/PATCH/DELETE is owner-checked in the manager so one user
// can never touch another's partial upload.
//
// Protocol (tus.io 1.0.0 core, minus deferred-length + extensions we don't need):
//   POST   /api/files/upload/resumable            create; Upload-Length + Upload-Metadata → 201 + Location + upload id
//   HEAD   /api/files/upload/resumable/{id}        query committed offset → Upload-Offset / Upload-Length
//   PATCH  /api/files/upload/resumable/{id}        append a chunk at Upload-Offset → new Upload-Offset (204)
//   DELETE /api/files/upload/resumable/{id}        cancel + discard the partial
//
// Metadata (tus Upload-Metadata, comma-separated key base64(value)) carries:
//   filename       (required) target file name
//   parent         (optional) Files parent node id; "" ⇒ Drive root
//   content_type   (optional) MIME type
//   sha256         (optional) hex whole-file digest; verified before promotion
// A per-chunk Upload-Checksum: sha256 <base64digest> header (tus checksum
// extension) is verified before the chunk is committed to disk.

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"vulos/backend/services/files"
	"vulos/backend/services/upload"
)

// filesUploadSink adapts *files.Service to the upload.Sink seam: on completion
// the resumable manager promotes the assembled bytes into the owner's Drive via
// the Files service's ACL-gated data plane (StoreContent = create/locate node →
// stream bytes into the owner's bucket → record a version). The Files service
// enforces write authorization on the parent, so the manager never bypasses the
// ACL.
type filesUploadSink struct{ svc *files.Service }

func (s *filesUploadSink) Store(ctx context.Context, userID, parentID, name, contentType string, r io.Reader, size int64) (string, error) {
	return s.svc.StoreContent(ctx, userID, parentID, name, contentType, r, size)
}

// registerFilesResumableRoutes wires the resumable upload endpoints. mgr may be
// nil (upload manager init failed); in that case every endpoint returns 503 so
// the rest of the OS still boots and small single-shot uploads keep working.
func registerFilesResumableRoutes(mux *http.ServeMux, mgr *upload.Manager) {
	guard := func(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Tus-Resumable", upload.TusVersion)
			if mgr == nil {
				writeErr(w, 503, "resumable upload unavailable")
				return
			}
			userID := r.Header.Get("X-User-ID")
			if userID == "" {
				writeErr(w, 401, "unauthorized")
				return
			}
			h(w, r, userID)
		}
	}

	// OPTIONS advertises server capabilities (tus discovery). Cheap, unauthed-ish
	// (still behind the OS auth middleware for the /api prefix).
	mux.HandleFunc("OPTIONS /api/files/upload/resumable", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Tus-Resumable", upload.TusVersion)
		w.Header().Set("Tus-Version", upload.TusVersion)
		w.Header().Set("Tus-Extension", "creation,checksum,termination")
		w.Header().Set("Tus-Checksum-Algorithm", "sha256")
		if mgr != nil {
			w.Header().Set("Tus-Max-Size", strconv.FormatInt(upload.DefaultMaxUploadBytes, 10))
		}
		w.WriteHeader(http.StatusNoContent)
	})

	// POST create.
	mux.HandleFunc("POST /api/files/upload/resumable", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		length, err := parseUploadLength(r)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		meta := parseUploadMetadata(r.Header.Get("Upload-Metadata"))
		name := meta["filename"]
		if name == "" {
			name = meta["name"] // tolerate either key
		}
		u, err := mgr.Create(uid, upload.CreateParams{
			Length:      length,
			ParentID:    meta["parent"],
			Name:        name,
			ContentType: meta["content_type"],
			SHA256:      strings.ToLower(meta["sha256"]),
		})
		if err != nil {
			writeUploadErr(w, err)
			return
		}
		w.Header().Set("Location", "/api/files/upload/resumable/"+u.ID)
		w.Header().Set("Upload-Offset", "0")
		w.Header().Set("Upload-Length", strconv.FormatInt(u.Length, 10))
		// Echo the id in JSON too so a fetch()-based client that can't read the
		// Location header cross-origin still gets it.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"` + u.ID + `","offset":0,"length":` + strconv.FormatInt(u.Length, 10) + `}`))
	}))

	// HEAD offset query.
	mux.HandleFunc("HEAD /api/files/upload/resumable/{id}", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		u, err := mgr.Head(uid, r.PathValue("id"))
		if err != nil {
			writeUploadErr(w, err)
			return
		}
		// tus requires Cache-Control: no-store on HEAD so a proxy never serves a
		// stale offset.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Upload-Offset", strconv.FormatInt(u.Offset, 10))
		w.Header().Set("Upload-Length", strconv.FormatInt(u.Length, 10))
		if u.Complete {
			w.Header().Set("Upload-Complete", "1")
			if u.NodeID != "" {
				w.Header().Set("Vulos-Node-Id", u.NodeID)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))

	// PATCH append a chunk.
	mux.HandleFunc("PATCH /api/files/upload/resumable/{id}", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		// tus requires this content type on PATCH.
		if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/offset+octet-stream") {
			writeErr(w, 415, "Content-Type must be application/offset+octet-stream")
			return
		}
		offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
		if err != nil || offset < 0 {
			writeErr(w, 400, "invalid Upload-Offset")
			return
		}
		chunkSHA, err := parseChecksum(r.Header.Get("Upload-Checksum"))
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		// Bound the chunk body to the manager's per-chunk cap so a single PATCH can
		// never trip the relay request cap or buffer an unbounded body. The manager
		// additionally bounds by the upload's remaining declared length.
		body := http.MaxBytesReader(w, r.Body, mgr.MaxChunkBytes())
		defer body.Close()
		res, err := mgr.Patch(r.Context(), uid, r.PathValue("id"), offset, body, chunkSHA)
		if err != nil {
			writeUploadErr(w, err)
			return
		}
		w.Header().Set("Upload-Offset", strconv.FormatInt(res.Offset, 10))
		if res.Complete {
			w.Header().Set("Upload-Complete", "1")
			if res.NodeID != "" {
				w.Header().Set("Vulos-Node-Id", res.NodeID)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	// DELETE cancel.
	mux.HandleFunc("DELETE /api/files/upload/resumable/{id}", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		if err := mgr.Delete(uid, r.PathValue("id")); err != nil {
			writeUploadErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

// parseUploadLength reads the tus Upload-Length header, rejecting missing,
// negative, or non-numeric values. We do not support Upload-Defer-Length.
func parseUploadLength(r *http.Request) (int64, error) {
	raw := r.Header.Get("Upload-Length")
	if raw == "" {
		return 0, errors.New("Upload-Length required")
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("invalid Upload-Length")
	}
	return n, nil
}

// parseUploadMetadata decodes a tus Upload-Metadata header: a comma-separated
// list of "key base64(value)" (or bare "key" for a valueless flag). Unknown or
// undecodable pairs are skipped rather than failing the whole request.
func parseUploadMetadata(h string) map[string]string {
	out := map[string]string{}
	if h == "" {
		return out
	}
	for _, pair := range strings.Split(h, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, " ", 2)
		key := strings.TrimSpace(parts[0])
		if key == "" {
			continue
		}
		if len(parts) == 1 {
			out[key] = ""
			continue
		}
		val, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[1]))
		if err != nil {
			continue
		}
		out[key] = string(val)
	}
	return out
}

// parseChecksum reads a tus Upload-Checksum header ("sha256 <base64digest>") and
// returns the digest as lowercase hex (the form the manager verifies against).
// An empty header means "no per-chunk checksum". Only sha256 is supported.
func parseChecksum(h string) (string, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return "", nil
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "sha256") {
		return "", errors.New("unsupported checksum algorithm (want sha256)")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(parts[1]))
	if err != nil {
		return "", errors.New("invalid checksum encoding")
	}
	return hex.EncodeToString(raw), nil
}

// writeUploadErr maps upload sentinel errors to HTTP status codes.
func writeUploadErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, upload.ErrNotFound), errors.Is(err, upload.ErrForbidden):
		// Collapse forbidden→404 so a partial upload's existence never leaks to a
		// non-owner (anti-enumeration, matching the Files IDOR convention).
		writeErr(w, 404, "not found")
	case errors.Is(err, upload.ErrOffsetConflict):
		writeErr(w, 409, err.Error())
	case errors.Is(err, upload.ErrChecksum):
		// tus checksum-mismatch status.
		writeErr(w, 460, err.Error())
	case errors.Is(err, upload.ErrTooLarge):
		writeErr(w, 413, err.Error())
	case errors.Is(err, upload.ErrAlreadyComplete):
		writeErr(w, 409, "upload already complete")
	case errors.Is(err, upload.ErrSinkUnavailable):
		writeErr(w, 503, "files service unavailable")
	case errors.Is(err, upload.ErrInvalid):
		writeErr(w, 400, err.Error())
	default:
		// Files-service authorization failures (parent not writable, etc.) and
		// unexpected errors both land here; 400 keeps enumeration low-signal.
		writeErr(w, 400, "upload failed")
	}
}
