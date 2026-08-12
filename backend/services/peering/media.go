// media.go — peer media transfer: upload, content-hash store, signed URLs,
// server-to-server fetch, and image/video thumbnail generation (PEER-16).
//
// # Overview
//
// MediaStore backs all media operations for the peering package.  It lives at
// ~/.vulos/peering/media/ and stores every file by its SHA-256 content hash,
// eliminating duplicates and making the storage layout self-describing.
//
// ## Upload flow
//
//  1. Sender POSTs a binary file to POST /api/peering/media/upload
//     (multipart/form-data field "file").
//  2. Server computes SHA-256, stores the file as
//     ~/.vulos/peering/media/<hash>/blob (with a sidecar <hash>/meta.json).
//  3. Returns the content hash and a short-lived signed URL that the peer can
//     use to fetch the blob.
//  4. Thumbnails for images and videos are generated asynchronously via
//     ffmpeg / convert; stored as <hash>/thumb.jpg.
//
// ## Signed URL scheme
//
//	GET /api/peering/media/fetch/<hash>?sig=<hmac>&exp=<unix-sec>
//
// The URL is valid until exp (default: 1 hour).  The HMAC-SHA256 signature
// is computed over "<hash>:<exp>" using the node's private signing key
// (first 32 bytes used as an HMAC secret — these are the seed bytes of the
// Ed25519 key, never transmitted).  Tampering with either the hash or the
// expiry invalidates the signature.
//
// ## S2S fetch
//
// When the inbound message handler delivers a message containing an
// Attachment with a non-empty Hash field, the recipient calls
// MediaStore.FetchFromPeer, which:
//  1. Checks whether the blob is already in the local store (dedup).
//  2. If absent, GETs the signed URL from the sender's Vulos server.
//  3. Verifies the downloaded blob matches the expected hash.
//  4. Stores the blob and kicks off thumbnail generation.
//
// ## Routes registered by RegisterMediaHandlers
//
//	POST /api/peering/media/upload                    — upload a file
//	GET  /api/peering/media/fetch/{hash}              — serve a file (signed URL required)
//	GET  /api/peering/media/thumb/{hash}              — serve the thumbnail
//	POST /api/peering/inbound/media                   — inbound media-ref envelope (S2S trigger)
//
// Only RegisterMediaHandlers is exported.  The inbound route must be wrapped
// with InboundMiddleware by the orchestrator (same pattern as messages.go).
//
// # Usage
//
//	ms, err := peering.NewMediaStore(home, signingKey)
//	if err != nil { ... }
//	ms.RegisterMediaHandlers(mux)
package peering

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ─── constants ────────────────────────────────────────────────────────────────

// mediaBlobName is the fixed filename for the stored blob within a hash bucket.
const mediaBlobName = "blob"

// mediaMetaName is the fixed filename for the JSON sidecar.
const mediaMetaName = "meta.json"

// mediaThumbName is the fixed filename for the generated thumbnail.
const mediaThumbName = "thumb.jpg"

// mediaSignedURLTTL is the default signed-URL lifetime.
const mediaSignedURLTTL = 1 * time.Hour

// mediaMaxUploadBytes is the maximum accepted upload size (100 MiB).
const mediaMaxUploadBytes = 100 << 20

// mediaFetchTimeout is the total deadline for an S2S blob fetch.
const mediaFetchTimeout = 60 * time.Second

// ─── MediaMeta (peering) ─────────────────────────────────────────────────────

// PeeringMediaMeta is the JSON sidecar stored alongside every media blob.
// Named with a "Peering" prefix to avoid collision with recall.MediaMeta.
type PeeringMediaMeta struct {
	// Hash is the hex-encoded SHA-256 of the blob content (without prefix).
	Hash string `json:"hash"`

	// MIMEType is the detected or declared MIME type.
	MIMEType string `json:"mime_type"`

	// OriginalName is the original filename supplied by the uploader (may be empty).
	OriginalName string `json:"original_name,omitempty"`

	// Size is the blob length in bytes.
	Size int64 `json:"size"`

	// StoredAt is the UTC time the blob was first stored locally.
	StoredAt time.Time `json:"stored_at"`

	// HasThumb is true when thumb.jpg has been generated successfully.
	HasThumb bool `json:"has_thumb"`
}

// canonicalHash formats a raw hex digest as the canonical "sha256:<hex>" ref.
func canonicalHash(hexDigest string) string {
	return "sha256:" + hexDigest
}

// hexFromCanonical strips the "sha256:" prefix and returns the bare hex string.
// Returns an error when the input does not start with "sha256:", when the hex
// portion is not exactly 64 characters, or when it contains non-hex characters.
// The character validation prevents path-traversal: a bare "../"-filled 64-char
// string would otherwise escape the media root via filepath.Join.
func hexFromCanonical(ref string) (string, error) {
	const pfx = "sha256:"
	if !strings.HasPrefix(ref, pfx) {
		return "", fmt.Errorf("peering/media: invalid hash ref %q: expected sha256: prefix", ref)
	}
	h := strings.TrimPrefix(ref, pfx)
	if len(h) != 64 {
		return "", fmt.Errorf("peering/media: hash ref %q has wrong hex length", ref)
	}
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return "", fmt.Errorf("peering/media: hash ref %q contains non-hex character %q", ref, c)
		}
	}
	return h, nil
}

// ─── MediaStore ──────────────────────────────────────────────────────────────

// MediaStore manages on-disk storage of peering media blobs, signed URL
// issuance, and thumbnail generation.
//
// Obtain one via NewMediaStore; the zero value is not usable.
type MediaStore struct {
	// root is ~/.vulos/peering/media/
	root string

	// hmacSecret is derived from the first 32 bytes of the Ed25519 private key
	// (the seed).  It is used exclusively for signing and verifying URLs and
	// is never transmitted over the network.
	hmacSecret []byte

	// httpClient is the outbound HTTP client used for S2S blob fetches.
	// Uses the same SSRF-guarded transport as PeerClient when peerClient != nil.
	httpClient *http.Client

	// peerClient is the shared outbound client (provides SSRF guard).
	// May be nil in tests; in that case a plain http.Client is used.
	peerClient *PeerClient
}

// NewMediaStore creates a MediaStore backed by
// filepath.Join(root, "peering", "media").
//
// signingKey is the local Ed25519 private key.  The first 32 bytes (the seed)
// are used as the HMAC-SHA256 secret for signed URL generation.  The key is
// not stored to disk by this package.
//
// peerClient may be nil in tests; production callers should pass the shared
// PeerClient to inherit its SSRF guard.
func NewMediaStore(home string, signingKey []byte, peerClient *PeerClient) (*MediaStore, error) {
	if len(signingKey) < 32 {
		return nil, fmt.Errorf("peering/media: signing key too short (%d bytes)", len(signingKey))
	}
	root := filepath.Join(home, "peering", "media")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("peering/media: mkdir %s: %w", root, err)
	}

	secret := make([]byte, 32)
	copy(secret, signingKey[:32])

	var hc *http.Client
	if peerClient != nil {
		hc = peerClient.http
	} else {
		hc = &http.Client{Timeout: mediaFetchTimeout}
	}

	return &MediaStore{
		root:       root,
		hmacSecret: secret,
		httpClient: hc,
		peerClient: peerClient,
	}, nil
}

// ─── RegisterMediaHandlers ───────────────────────────────────────────────────

// RegisterMediaHandlers wires all media-related routes onto mux.
//
// Routes registered:
//
//	POST /api/peering/media/upload          — upload a file; returns hash + signed URL
//	GET  /api/peering/media/fetch/{hash}    — serve blob (requires valid signed URL params)
//	GET  /api/peering/media/thumb/{hash}    — serve thumbnail JPEG (if available)
//	POST /api/peering/inbound/media         — S2S: receive a media-ref envelope
//
// The caller must wrap POST /api/peering/inbound/media with InboundMiddleware.
func (ms *MediaStore) RegisterMediaHandlers(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/peering/media/upload", ms.handleMediaUpload)
	mux.HandleFunc("GET /api/peering/media/fetch/{hash}", ms.handleMediaFetch)
	mux.HandleFunc("GET /api/peering/media/thumb/{hash}", ms.handleMediaThumb)
	mux.HandleFunc("POST /api/peering/inbound/media", ms.HandleInboundMedia)
}

// ─── Signed URL helpers ───────────────────────────────────────────────────────

// signedURLParams computes the "sig" and "exp" query parameters for a
// content-addressed fetch URL.
//
// The signature is HMAC-SHA256("<hexhash>:<expUnix>") encoded as lowercase hex.
// expUnix is seconds since Unix epoch.
func (ms *MediaStore) signedURLParams(hexHash string, expUnix int64) (sig, exp string) {
	exp = strconv.FormatInt(expUnix, 10)
	mac := hmac.New(sha256.New, ms.hmacSecret)
	mac.Write([]byte(hexHash + ":" + exp))
	sig = hex.EncodeToString(mac.Sum(nil))
	return
}

// verifySignedURLParams checks that sig is a valid HMAC-SHA256 over
// "<hexHash>:<expStr>" and that the URL has not expired.
//
// Returns a non-nil error when verification fails.
func (ms *MediaStore) verifySignedURLParams(hexHash, sig, expStr string) error {
	expUnix, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return fmt.Errorf("peering/media: invalid exp parameter: %w", err)
	}
	if time.Now().Unix() > expUnix {
		return fmt.Errorf("peering/media: signed URL has expired")
	}
	wantSig, _ := ms.signedURLParams(hexHash, expUnix)
	if !hmac.Equal([]byte(sig), []byte(wantSig)) {
		return fmt.Errorf("peering/media: signed URL signature mismatch")
	}
	return nil
}

// buildSignedURL constructs the full fetch URL for a blob.
//
// baseURL is the local server's root, e.g. "https://alice.vulos.org:8080".
// hexHash is the bare hex digest.
func (ms *MediaStore) buildSignedURL(baseURL, hexHash string, ttl time.Duration) string {
	expUnix := time.Now().Add(ttl).Unix()
	sig, exp := ms.signedURLParams(hexHash, expUnix)
	return strings.TrimRight(baseURL, "/") +
		"/api/peering/media/fetch/" + canonicalHash(hexHash) +
		"?sig=" + sig + "&exp=" + exp
}

// ─── Blob storage helpers ─────────────────────────────────────────────────────

// bucketDir returns the directory for a given hex hash.
func (ms *MediaStore) bucketDir(hexHash string) string {
	return filepath.Join(ms.root, hexHash)
}

// blobPath returns the path to the raw blob file.
func (ms *MediaStore) blobPath(hexHash string) string {
	return filepath.Join(ms.bucketDir(hexHash), mediaBlobName)
}

// metaPath returns the path to the JSON sidecar.
func (ms *MediaStore) metaPath(hexHash string) string {
	return filepath.Join(ms.bucketDir(hexHash), mediaMetaName)
}

// thumbPath returns the path to the thumbnail.
func (ms *MediaStore) thumbPath(hexHash string) string {
	return filepath.Join(ms.bucketDir(hexHash), mediaThumbName)
}

// HasBlob returns true when the blob for hexHash is already stored locally.
func (ms *MediaStore) HasBlob(hexHash string) bool {
	_, err := os.Stat(ms.blobPath(hexHash))
	return err == nil
}

// storeBlob writes data into the bucket directory for hexHash, verifying
// that the content hash matches.  Returns the byte count on success.
//
// The write is atomic (temp file + rename).  If the blob already exists,
// storeBlob is a no-op (idempotent).
func (ms *MediaStore) storeBlob(hexHash string, r io.Reader, mimeType, originalName string) (int64, error) {
	dir := ms.bucketDir(hexHash)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return 0, fmt.Errorf("peering/media: mkdir bucket %s: %w", dir, err)
	}

	dst := ms.blobPath(hexHash)
	if _, err := os.Stat(dst); err == nil {
		// Already stored — read size from meta.
		m, merr := ms.readMeta(hexHash)
		if merr == nil {
			return m.Size, nil
		}
		info, serr := os.Stat(dst)
		if serr == nil {
			return info.Size(), nil
		}
		return 0, nil
	}

	// Write to a temp file while computing the SHA-256 simultaneously.
	tmp, err := os.CreateTemp(dir, ".blob-*.tmp")
	if err != nil {
		return 0, fmt.Errorf("peering/media: create temp: %w", err)
	}
	tmpName := tmp.Name()

	h := sha256.New()
	mw := io.MultiWriter(tmp, h)
	written, cpErr := io.Copy(mw, r)
	closeErr := tmp.Close()

	if cpErr != nil || closeErr != nil {
		os.Remove(tmpName)
		if cpErr != nil {
			return 0, fmt.Errorf("peering/media: write blob: %w", cpErr)
		}
		return 0, fmt.Errorf("peering/media: close temp: %w", closeErr)
	}

	// Verify hash integrity.
	gotHex := hex.EncodeToString(h.Sum(nil))
	if gotHex != hexHash {
		os.Remove(tmpName)
		return 0, fmt.Errorf("peering/media: hash mismatch: expected %s got %s", hexHash, gotHex)
	}

	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return 0, fmt.Errorf("peering/media: rename blob: %w", err)
	}

	// Write meta sidecar.
	meta := PeeringMediaMeta{
		Hash:         canonicalHash(hexHash),
		MIMEType:     mimeType,
		OriginalName: originalName,
		Size:         written,
		StoredAt:     time.Now().UTC(),
	}
	if merr := ms.writeMeta(hexHash, &meta); merr != nil {
		log.Printf("[peering/media] writeMeta %s: %v", hexHash, merr)
	}

	return written, nil
}

// readMeta loads and decodes the JSON sidecar for hexHash.
func (ms *MediaStore) readMeta(hexHash string) (*PeeringMediaMeta, error) {
	data, err := os.ReadFile(ms.metaPath(hexHash))
	if err != nil {
		return nil, err
	}
	var m PeeringMediaMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// writeMeta atomically writes a JSON sidecar to disk.
func (ms *MediaStore) writeMeta(hexHash string, m *PeeringMediaMeta) error {
	dir := ms.bucketDir(hexHash)
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".meta-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmpName)
		if werr != nil {
			return werr
		}
		return cerr
	}
	if err := os.Rename(tmpName, ms.metaPath(hexHash)); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// ─── Thumbnail generation ─────────────────────────────────────────────────────

// generateThumbnail attempts to produce a 320×320 JPEG thumbnail for the blob
// at hexHash.  Supports images and video.
//
// For images: ffmpeg first; falls back to convert (ImageMagick).
// For video:  ffmpeg only (extracts frame at 1 s).
//
// If neither tool is available the function is a no-op (thumbnails are
// optional — their absence degrades gracefully).
func (ms *MediaStore) generateThumbnail(hexHash, mimeType string) {
	src := ms.blobPath(hexHash)
	dst := ms.thumbPath(hexHash)

	if _, err := os.Stat(dst); err == nil {
		return // already generated
	}

	dir := ms.bucketDir(hexHash)
	tmp, err := os.CreateTemp(dir, ".thumb-*.jpg.tmp")
	if err != nil {
		log.Printf("[peering/media] generateThumbnail: create temp: %v", err)
		return
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName) // clean up on failure

	var thumbErr error
	if isMediaVideo(mimeType) {
		thumbErr = generateVideoThumb(src, tmpName)
	} else {
		thumbErr = generateImageThumb(src, tmpName)
	}

	if thumbErr != nil {
		log.Printf("[peering/media] generateThumbnail %s: %v", hexHash, thumbErr)
		return
	}

	if err := os.Rename(tmpName, dst); err != nil {
		log.Printf("[peering/media] generateThumbnail rename: %v", err)
		return
	}

	// Update meta to record thumbnail existence.
	if m, err := ms.readMeta(hexHash); err == nil {
		m.HasThumb = true
		if werr := ms.writeMeta(hexHash, m); werr != nil {
			log.Printf("[peering/media] updateMeta hasThumb: %v", werr)
		}
	}
}

// generateImageThumb produces a thumbnail for an image file.
// Tries ffmpeg first, then convert (ImageMagick).
func generateImageThumb(src, dst string) error {
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		cmd := exec.Command(path,
			"-y", "-i", src,
			"-vf", "scale=320:320:force_original_aspect_ratio=decrease",
			"-frames:v", "1",
			"-f", "image2", dst,
		)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	if path, err := exec.LookPath("convert"); err == nil {
		cmd := exec.Command(path,
			src+"[0]",
			"-thumbnail", "320x320>",
			"-quality", "80",
			dst,
		)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("peering/media: no thumbnail tool available (ffmpeg or convert)")
}

// generateVideoThumb extracts the frame at 1 second and scales it to 320×320.
// Requires ffmpeg.
func generateVideoThumb(src, dst string) error {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		return fmt.Errorf("peering/media: ffmpeg not found for video thumbnail")
	}
	cmd := exec.Command(path,
		"-y", "-ss", "00:00:01",
		"-i", src,
		"-vf", "scale=320:320:force_original_aspect_ratio=decrease",
		"-frames:v", "1",
		"-f", "image2", dst,
	)
	if err := cmd.Run(); err != nil {
		// Retry at 0 s if 1 s is past the end of the video.
		cmd2 := exec.Command(path,
			"-y", "-ss", "00:00:00",
			"-i", src,
			"-vf", "scale=320:320:force_original_aspect_ratio=decrease",
			"-frames:v", "1",
			"-f", "image2", dst,
		)
		if err2 := cmd2.Run(); err2 != nil {
			return fmt.Errorf("peering/media: ffmpeg thumbnail failed: %w", err2)
		}
	}
	return nil
}

// isMediaVideo returns true for common video MIME types.
func isMediaVideo(mimeType string) bool {
	switch {
	case strings.HasPrefix(mimeType, "video/"):
		return true
	}
	return false
}

// isMediaImage returns true for common image MIME types.
func isMediaImage(mimeType string) bool {
	return strings.HasPrefix(mimeType, "image/")
}

// ─── Upload handler ───────────────────────────────────────────────────────────

// uploadMediaResponse is the JSON body returned by POST /api/peering/media/upload.
type uploadMediaResponse struct {
	// Hash is the canonical "sha256:<hex>" content reference.
	Hash string `json:"hash"`

	// SignedURL is a short-lived URL the peer can use to fetch the blob.
	SignedURL string `json:"signed_url"`

	// Size is the blob byte count.
	Size int64 `json:"size"`

	// MIMEType is the detected or declared content type.
	MIMEType string `json:"mime_type"`

	// HasThumb is true when a thumbnail has already been generated (may be
	// false immediately after upload if generation is still in progress).
	HasThumb bool `json:"has_thumb"`
}

// handleMediaUpload implements POST /api/peering/media/upload.
//
// Accepts multipart/form-data with a "file" field.  The server base URL is
// inferred from the incoming request (Host header) so the signed URL is
// usable from the caller's perspective.
func (ms *MediaStore) handleMediaUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(mediaMaxUploadBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "parse multipart form: " + err.Error(),
		})
		return
	}

	f, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "file field missing or unreadable: " + err.Error(),
		})
		return
	}
	defer f.Close()

	mimeType := header.Header.Get("Content-Type")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	// Compute hash inline while storing.  We need to hash-then-store, but
	// since the reader is a single-pass stream we first buffer into a temp
	// file in the media root, compute the hash, then rename into the bucket.
	tmpDir := ms.root
	tmp, err := os.CreateTemp(tmpDir, ".upload-*.tmp")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "could not create temp file",
		})
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	h := sha256.New()
	mw := io.MultiWriter(tmp, h)
	written, cpErr := io.Copy(mw, io.LimitReader(f, mediaMaxUploadBytes+1))
	closeErr := tmp.Close()

	if cpErr != nil || closeErr != nil {
		if cpErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "upload read error: " + cpErr.Error(),
			})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "upload close error: " + closeErr.Error(),
			})
		}
		return
	}
	if written > mediaMaxUploadBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
			"error": "upload exceeds 100 MiB limit",
		})
		return
	}

	hexHash := hex.EncodeToString(h.Sum(nil))
	hashRef := canonicalHash(hexHash)

	// Move temp file into bucket if blob not already stored.
	bucketDir := ms.bucketDir(hexHash)
	if err := os.MkdirAll(bucketDir, 0700); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "bucket dir error",
		})
		return
	}
	blobDst := ms.blobPath(hexHash)
	if _, statErr := os.Stat(blobDst); os.IsNotExist(statErr) {
		if err := os.Rename(tmpName, blobDst); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "store blob error: " + err.Error(),
			})
			return
		}
		// Write meta sidecar.
		meta := PeeringMediaMeta{
			Hash:         hashRef,
			MIMEType:     mimeType,
			OriginalName: header.Filename,
			Size:         written,
			StoredAt:     time.Now().UTC(),
		}
		if merr := ms.writeMeta(hexHash, &meta); merr != nil {
			log.Printf("[peering/media] writeMeta on upload %s: %v", hexHash, merr)
		}
	}
	// If blob already exists, just remove the temp (defer handles it).

	// Generate thumbnail asynchronously.
	if isMediaImage(mimeType) || isMediaVideo(mimeType) {
		go ms.generateThumbnail(hexHash, mimeType)
	}

	// Build signed URL using the request's Host.
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") == "" {
		scheme = "http"
	}
	baseURL := scheme + "://" + r.Host
	signedURL := ms.buildSignedURL(baseURL, hexHash, mediaSignedURLTTL)

	meta, _ := ms.readMeta(hexHash)
	hasThumb := meta != nil && meta.HasThumb

	writeJSON(w, http.StatusCreated, uploadMediaResponse{
		Hash:      hashRef,
		SignedURL: signedURL,
		Size:      written,
		MIMEType:  mimeType,
		HasThumb:  hasThumb,
	})
}

// ─── Fetch handler ────────────────────────────────────────────────────────────

// handleMediaFetch implements GET /api/peering/media/fetch/{hash}.
//
// Query parameters:
//
//	sig — HMAC-SHA256 hex signature
//	exp — Unix timestamp (seconds) at which the URL expires
//
// Returns the raw blob with the stored MIME type as Content-Type.
func (ms *MediaStore) handleMediaFetch(w http.ResponseWriter, r *http.Request) {
	hashParam := r.PathValue("hash")
	if hashParam == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hash is required"})
		return
	}

	hexHash, err := hexFromCanonical(hashParam)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	sig := r.URL.Query().Get("sig")
	exp := r.URL.Query().Get("exp")
	if sig == "" || exp == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "signed URL parameters sig and exp are required",
		})
		return
	}

	if err := ms.verifySignedURLParams(hexHash, sig, exp); err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}

	blobPath := ms.blobPath(hexHash)
	f, err := os.Open(blobPath)
	if os.IsNotExist(err) {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "blob not found",
			"hash":  hashParam,
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "open blob: " + err.Error(),
		})
		return
	}
	defer f.Close()

	// Set Content-Type from stored meta.
	if m, merr := ms.readMeta(hexHash); merr == nil && m.MIMEType != "" {
		w.Header().Set("Content-Type", m.MIMEType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")

	io.Copy(w, f) //nolint:errcheck
}

// ─── Thumb handler ────────────────────────────────────────────────────────────

// handleMediaThumb implements GET /api/peering/media/thumb/{hash}.
//
// Returns the thumbnail JPEG.  Returns 404 if no thumbnail is available.
// No signed URL is required for thumbnails — they are non-reversible
// representations and carry no sensitive information.
func (ms *MediaStore) handleMediaThumb(w http.ResponseWriter, r *http.Request) {
	hashParam := r.PathValue("hash")
	if hashParam == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hash is required"})
		return
	}

	hexHash, err := hexFromCanonical(hashParam)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	thumbPath := ms.thumbPath(hexHash)
	f, err := os.Open(thumbPath)
	if os.IsNotExist(err) {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "thumbnail not available",
			"hash":  hashParam,
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "open thumbnail: " + err.Error(),
		})
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	io.Copy(w, f) //nolint:errcheck
}

// ─── S2S fetch ────────────────────────────────────────────────────────────────

// MediaRefPayload is the envelope payload sent server-to-server when a sender
// notifies the recipient that a media blob is ready to be fetched.
//
// The recipient's inbound handler calls FetchFromPeer to download the blob.
type MediaRefPayload struct {
	// Hash is the canonical "sha256:<hex>" content reference.
	Hash string `json:"hash"`

	// SignedURL is the sender's pre-computed signed fetch URL.
	SignedURL string `json:"signed_url"`

	// Size is the expected byte count (used for pre-validation).
	Size int64 `json:"size"`

	// MIMEType is the content type declared by the sender.
	MIMEType string `json:"mime_type"`

	// OriginalName is the uploader-supplied filename (optional).
	OriginalName string `json:"original_name,omitempty"`
}

// FetchFromPeer downloads a media blob from a remote peer's signed URL and
// stores it in the local media store.
//
// If the blob is already present locally (checked by hash), FetchFromPeer is
// a no-op.  The downloaded content is verified against the expected hash; a
// mismatch returns an error without storing corrupted data.
//
// Thumbnail generation is kicked off asynchronously on success.
func (ms *MediaStore) FetchFromPeer(ctx context.Context, ref MediaRefPayload) error {
	hexHash, err := hexFromCanonical(ref.Hash)
	if err != nil {
		return err
	}

	// Skip if already stored (idempotent).
	if ms.HasBlob(hexHash) {
		return nil
	}

	// Validate the URL looks sane before making a network call.
	if ref.SignedURL == "" {
		return fmt.Errorf("peering/media: FetchFromPeer: signed_url is required")
	}

	fetchCtx, cancel := context.WithTimeout(ctx, mediaFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, ref.SignedURL, nil)
	if err != nil {
		return fmt.Errorf("peering/media: FetchFromPeer: build request: %w", err)
	}
	req.Header.Set("User-Agent", "VulosOS/1.0 peering-media")

	resp, err := ms.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("peering/media: FetchFromPeer: GET %s: %w", ref.SignedURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("peering/media: FetchFromPeer: remote returned %d", resp.StatusCode)
	}

	// Stream into local store with hash verification.
	limitedBody := io.LimitReader(resp.Body, mediaMaxUploadBytes+1)
	_, err = ms.storeBlob(hexHash, limitedBody, ref.MIMEType, ref.OriginalName)
	if err != nil {
		return fmt.Errorf("peering/media: FetchFromPeer: store: %w", err)
	}

	log.Printf("[peering/media] fetched blob %s from %s (%d bytes)", ref.Hash, ref.SignedURL, ref.Size)

	// Generate thumbnail asynchronously.
	if isMediaImage(ref.MIMEType) || isMediaVideo(ref.MIMEType) {
		go ms.generateThumbnail(hexHash, ref.MIMEType)
	}

	return nil
}

// HandleInboundMedia implements POST /api/peering/inbound/media.
//
// InboundMiddleware has already:
//  1. Verified the envelope's Ed25519 signature.
//  2. Confirmed the sender is in the approved contacts list.
//
// This handler decodes the MediaRefPayload from the envelope and calls
// FetchFromPeer to download the blob in a background goroutine.
//
// The handler returns 202 Accepted immediately; the fetch happens
// asynchronously so the remote server is not blocked on our download speed.
func (ms *MediaStore) HandleInboundMedia(w http.ResponseWriter, r *http.Request) {
	env, ok := r.Context().Value(EnvelopeKey).(*Envelope)
	if !ok || env == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "envelope not found in context — ensure InboundMiddleware is applied",
		})
		return
	}

	var ref MediaRefPayload
	if err := json.Unmarshal(env.Payload, &ref); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid media ref payload: " + err.Error(),
		})
		return
	}
	if ref.Hash == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "payload missing required field: hash",
		})
		return
	}
	if ref.SignedURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "payload missing required field: signed_url",
		})
		return
	}

	// Kick off the download asynchronously.
	go func() {
		if err := ms.FetchFromPeer(context.Background(), ref); err != nil {
			log.Printf("[peering/media] HandleInboundMedia: fetch from %s failed: %v",
				env.From, err)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "accepted",
		"hash":   ref.Hash,
	})
}
