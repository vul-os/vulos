// drop_media.go — DropTransfer bridges the Drop feature to the existing media
// pipeline (media.go). It satisfies DropMediaSender so that POST
// /api/peering/drop/send actually moves bytes instead of returning
// "media service unavailable".
//
// It reinvents nothing about the wire protocol:
//
//   - Send: the local file is ingested into the same content-addressed
//     MediaStore used by every other media transfer (SHA-256 bucket + signed,
//     HMAC-authenticated fetch URL). We then POST the sender-side
//     dropInboundRequest to the peer's existing /api/peering/inbound/drop
//     handler. The peer fetches over the same signed-URL scheme.
//
//   - Receive (on accept): we reuse MediaStore.FetchFromPeer to download and
//     hash-verify the blob, then land a user-visible copy under the recipient's
//     Downloads directory so it appears in the Files app.
//
// Reachability note: the signed fetch URL is built against selfBaseURL, this
// node's externally reachable base URL. On a self-host box that is the LAN
// address; behind a relay it is the relay-advertised URL. When selfBaseURL is
// empty the send path fails fast with a clear error rather than emitting an
// unfetchable URL.
package peering

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DropTransfer implements DropMediaSender using the peering MediaStore.
type DropTransfer struct {
	store       *MediaStore
	peerClient  *PeerClient
	selfVulosID string
	selfName    string
	selfBaseURL string
	downloadDir string
}

// NewDropTransfer constructs a DropTransfer.
//
// store and peerClient are the shared peering services. selfBaseURL is this
// node's externally reachable base URL used to build signed fetch URLs; it may
// be empty (send will then return a descriptive error). downloadDir is where
// accepted inbound files are landed (e.g. ~/Downloads).
func NewDropTransfer(store *MediaStore, peerClient *PeerClient, selfVulosID, selfName, selfBaseURL, downloadDir string) *DropTransfer {
	return &DropTransfer{
		store:       store,
		peerClient:  peerClient,
		selfVulosID: selfVulosID,
		selfName:    selfName,
		selfBaseURL: strings.TrimRight(selfBaseURL, "/"),
		downloadDir: downloadDir,
	}
}

// dropNotifyPath is the inbound endpoint on the recipient that announces a
// pending Drop (handled by DropService.dropHandleInbound).
const dropNotifyPath = "/api/peering/inbound/drop"

// SendFile ingests a local file into the media store, then notifies the peer
// at targetAddr that a transfer is pending. It returns the transfer ID.
func (d *DropTransfer) SendFile(ctx context.Context, targetAddr, mediaPath, mimeType string) (string, error) {
	if d.store == nil {
		return "", fmt.Errorf("peering/drop: media store unavailable")
	}
	if d.selfBaseURL == "" {
		return "", fmt.Errorf("peering/drop: this node has no reachable base URL configured (set VULOS_PUBLIC_BASE_URL)")
	}
	if mediaPath == "" {
		return "", fmt.Errorf("peering/drop: media_path required")
	}

	info, err := os.Stat(mediaPath)
	if err != nil {
		return "", fmt.Errorf("peering/drop: stat %s: %w", mediaPath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("peering/drop: media_path is a directory; archive it first")
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	fileName := filepath.Base(mediaPath)
	hexHash, size, err := d.ingestFile(mediaPath, mimeType, fileName)
	if err != nil {
		return "", err
	}

	signedURL := d.store.buildSignedURL(d.selfBaseURL, hexHash, mediaSignedURLTTL)

	transferID, err := randomTransferID()
	if err != nil {
		return "", err
	}

	notice := dropInboundRequest{
		TransferID:  transferID,
		FromVulosID: d.selfVulosID,
		DisplayName: d.selfName,
		FileName:    fileName,
		FileSize:    size,
		MimeType:    mimeType,
		DownloadURL: signedURL,
	}
	if err := d.notifyPeer(ctx, targetAddr, notice); err != nil {
		return "", err
	}

	log.Printf("[drop] sent notice %s to %s (%s, %d bytes)", transferID, targetAddr, fileName, size)
	return transferID, nil
}

// ReceiveFile downloads an accepted transfer from the sender's signed URL and
// lands a copy under downloadDir.
func (d *DropTransfer) ReceiveFile(ctx context.Context, downloadURL, fileName, mimeType string) error {
	if d.store == nil {
		return fmt.Errorf("peering/drop: media store unavailable")
	}
	if downloadURL == "" {
		return fmt.Errorf("peering/drop: download URL required")
	}

	hexHash, err := hexHashFromFetchURL(downloadURL)
	if err != nil {
		return err
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	ref := MediaRefPayload{
		Hash:         canonicalHash(hexHash),
		SignedURL:    downloadURL,
		MIMEType:     mimeType,
		OriginalName: fileName,
	}
	if err := d.store.FetchFromPeer(ctx, ref); err != nil {
		return err
	}

	if d.downloadDir == "" {
		return nil // stored content-addressed only; nothing more to do
	}
	if err := d.landInDownloads(hexHash, fileName); err != nil {
		// Non-fatal: the blob is still in the content store.
		log.Printf("[drop] land in downloads failed: %v", err)
	}
	return nil
}

// ingestFile hashes and stores a local file into the media store, returning the
// hex hash and byte size. Idempotent for already-stored content.
func (d *DropTransfer) ingestFile(path, mimeType, originalName string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("peering/drop: open %s: %w", path, err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		f.Close()
		return "", 0, fmt.Errorf("peering/drop: hash %s: %w", path, err)
	}
	f.Close()
	hexHash := hex.EncodeToString(h.Sum(nil))

	f2, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("peering/drop: reopen %s: %w", path, err)
	}
	defer f2.Close()

	size, err := d.store.storeBlob(hexHash, f2, mimeType, originalName)
	if err != nil {
		return "", 0, err
	}
	return hexHash, size, nil
}

// notifyPeer POSTs the pending-drop notice to the recipient's inbound handler.
// The notice is a plain JSON body (the inbound/drop route is not envelope-gated
// in DropService); we reuse the peer client's SSRF-guarded HTTP transport.
func (d *DropTransfer) notifyPeer(ctx context.Context, targetAddr string, notice dropInboundRequest) error {
	target := strings.TrimRight(targetAddr, "/") + dropNotifyPath
	body, err := json.Marshal(notice)
	if err != nil {
		return fmt.Errorf("peering/drop: marshal notice: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("peering/drop: build notice request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "VulaOS/1.0 peering-drop")

	client := d.notifyClient()
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("peering/drop: notify %s: %w", target, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("peering/drop: peer %s returned %d", target, resp.StatusCode)
	}
	return nil
}

// notifyClient returns the SSRF-guarded HTTP client (falls back to a default
// client in tests where no peer client is wired).
func (d *DropTransfer) notifyClient() *http.Client {
	if d.peerClient != nil && d.peerClient.http != nil {
		return d.peerClient.http
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// landInDownloads copies the stored blob to downloadDir under a safe,
// collision-free filename.
func (d *DropTransfer) landInDownloads(hexHash, fileName string) error {
	if err := os.MkdirAll(d.downloadDir, 0o755); err != nil {
		return fmt.Errorf("peering/drop: mkdir downloads: %w", err)
	}
	name := safeFileName(fileName)
	if name == "" {
		name = hexHash
	}
	dst := uniquePath(filepath.Join(d.downloadDir, name))

	src, err := os.Open(d.store.blobPath(hexHash))
	if err != nil {
		return fmt.Errorf("peering/drop: open blob: %w", err)
	}
	defer src.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("peering/drop: create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.Remove(dst)
		return fmt.Errorf("peering/drop: copy to %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		return err
	}
	log.Printf("[drop] landed %s", dst)
	return nil
}

// safeFileName reduces an arbitrary sender-declared name to a base filename
// without path separators or traversal.
func safeFileName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == ".." || name == "/" {
		return ""
	}
	return name
}

// uniquePath returns path unchanged if free, else inserts " (n)" before the
// extension until a free path is found.
func uniquePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := filepath.Ext(path)
	stem := strings.TrimSuffix(path, ext)
	for n := 2; n < 10000; n++ {
		cand := fmt.Sprintf("%s (%d)%s", stem, n, ext)
		if _, err := os.Stat(cand); os.IsNotExist(err) {
			return cand
		}
	}
	return path
}

// hexHashFromFetchURL extracts the bare hex digest from a media fetch URL of
// the form .../api/peering/media/fetch/sha256:<hex>?sig=...&exp=...
func hexHashFromFetchURL(url string) (string, error) {
	const marker = "/api/peering/media/fetch/"
	i := strings.Index(url, marker)
	if i < 0 {
		return "", fmt.Errorf("peering/drop: not a media fetch URL: %q", url)
	}
	rest := url[i+len(marker):]
	if q := strings.IndexByte(rest, '?'); q >= 0 {
		rest = rest[:q]
	}
	return hexFromCanonical(rest)
}

// randomTransferID returns a 128-bit random hex transfer ID.
func randomTransferID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("peering/drop: random id: %w", err)
	}
	return "drop-" + hex.EncodeToString(b), nil
}
