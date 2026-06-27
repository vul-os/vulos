package files

// transport.go — HTTPPeerTransport: the production PeerTransport. It streams a
// capability's bytes from a remote owner box by POSTing the signed fetch request
// to <ownerAddr>/api/files/peer/serve and returning the streaming response body.
//
// Self-host note: box-to-box peer-share legitimately targets LAN / private
// addresses (two self-hosted OS boxes on the same network), so this transport
// does NOT apply the SSRF private-address guard that peering's outbound client
// uses for public federation. The request is authenticated end-to-end (the
// owner verifies the capability + a recipient-signed proof), and the redeem is
// always user-initiated by pasting a link, which bounds the exposure to the
// user's own redeem flow. Operators who expose a box publicly should front it
// with TLS; the owner address is taken from the (TLS-aware) issuing request.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// PeerServePath is the route an owner box exposes to serve capability bytes.
const PeerServePath = "/api/files/peer/serve"

// peerTransferTimeout bounds the whole streamed transfer. Generous to tolerate
// large files over slow links; the context can cancel sooner.
const peerTransferTimeout = 30 * time.Minute

// HTTPPeerTransport is the default PeerTransport over plain HTTP(S).
type HTTPPeerTransport struct {
	http *http.Client
}

// NewHTTPPeerTransport returns an HTTPPeerTransport with a streaming-friendly
// client (no overall client timeout — the per-request context governs the
// deadline so a large download is not cut off mid-stream).
func NewHTTPPeerTransport() *HTTPPeerTransport {
	return &HTTPPeerTransport{http: &http.Client{}}
}

// Fetch POSTs the fetch request to the owner box and returns the streaming body.
func (t *HTTPPeerTransport) Fetch(ctx context.Context, ownerAddr string, req PeerFetchRequest) (io.ReadCloser, int64, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, 0, fmt.Errorf("files/transport: marshal fetch: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, peerTransferTimeout)
	target := strings.TrimRight(ownerAddr, "/") + PeerServePath
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, 0, fmt.Errorf("files/transport: build request: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(hreq)
	if err != nil {
		cancel()
		return nil, 0, fmt.Errorf("files/transport: fetch %s: %w", target, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, 0, fmt.Errorf("files/transport: owner returned %d", resp.StatusCode)
	}
	size := int64(-1)
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if v, perr := strconv.ParseInt(cl, 10, 64); perr == nil {
			size = v
		}
	}
	// Wrap so closing the body also cancels the request context.
	return &ctxReadCloser{rc: resp.Body, cancel: cancel}, size, nil
}

// ctxReadCloser cancels the request context when the body is closed.
type ctxReadCloser struct {
	rc     io.ReadCloser
	cancel context.CancelFunc
}

func (c *ctxReadCloser) Read(p []byte) (int, error) { return c.rc.Read(p) }
func (c *ctxReadCloser) Close() error {
	err := c.rc.Close()
	c.cancel()
	return err
}
