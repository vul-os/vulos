// adapter_mail.go — CLOUD-SIDE adapter bridging vulos-mail's
// store/sync.SyncTransport seam to vulos-cloud's split `POST /api/sync/push`
// + `GET /api/sync/pull` HTTP API.
//
// Why this lives in the CLOUD repo (not in vulos-mail):
//
//	vulos-mail (forked Mox) owns the SyncTransport interface as a clean seam
//	in store/sync/sync.go but, by design, does NOT depend on the cloud repo.
//	The cloud is the transport implementor; this file is that implementation.
//	The mail-repo seam is unchanged.
//
// What the seam looks like (mirrored locally, not imported):
//
//	mail's SyncTransport one-shot bidirectional method is:
//	  ExchangeDeltas(mailboxKey string, out Delta) (in Delta, manifest BlobManifest, err error)
//
//	the cloud's API is split:
//	  POST /api/sync/push   → pushes one batch of deltas, returns a new opaque cursor
//	  GET  /api/sync/pull   → returns deltas with id > since, plus blob manifest
//	  GET  /api/sync/blob/<hash> → fetches one blob by content-address
//
// The adapter therefore:
//
//  1. Calls /push with the outbound delta (mail's `out` is exactly ONE opaque
//     CRDT changeset blob → ONE PushInput).
//  2. Calls /pull with the per-(account, mailboxKey, box_id) cursor — which
//     the adapter stores opaque and persistent across calls.
//  3. Maps the cloud's opaque base16 autoinc cursor ↔ mail's HLC
//     {Wall, Logical, Replica} by FREEZING the HLC payload alongside the
//     opaque cursor. The mail repo treats the HLC cursor as opaque from this
//     adapter's perspective — it is just shipped through ExchangeDeltas as
//     part of the delta payload — so the adapter's job is purely
//     translating the COORDINATOR-SIDE cursor (autoinc id) into something
//     mail's transport plumbing can hold across calls.
//
// # Cursor-translation strategy (HLC ↔ opaque base16 autoinc)
//
// The two cursor spaces are NOT 1:1 isomorphic — that is fine.
//
//   - The cloud's opaque cursor is the high-water row id from /pull's
//     last response. It strictly monotonically advances per (account,
//     sync_key, box_id) within one cloud DB. The cursor is OPAQUE to all
//     callers (it is just the syncrz.EncodeCursor(int64) result, hex of
//     autoinc id) and re-decoding it via syncrz.DecodeCursor is the only
//     legal operation.
//
//   - vulos-mail's Cursor is an HLC {Wall, Logical, Replica}, set by the
//     mail store's crsqlite layer on every local op. The mail repo uses it
//     to drive its OWN ExportSince (which deltas to ship out). It is NEVER
//     interpreted by a transport (the transport just round-trips bytes).
//
// So the adapter holds TWO cursors, one per direction:
//
//   - outCursor (cloud-side): an opaque base16 string per (account, key, box)
//     used as `?since=` on the next /pull. Persisted by the adapter — a
//     fresh adapter passed an empty cursor pulls from the beginning.
//
//   - hlcCursor (mail-side): the HLC tuple that mail's Coordinator advances
//     locally; the adapter never modifies it, only round-trips it within
//     the Delta bytes the mail Coordinator passes in `out`.
//
// Monotonicity: cloud's autoinc id only ever goes up; pulling with a stale
// cursor returns 0 or more deltas, then bumps the cursor; pulling again
// with the new cursor returns ≤ the previous count. The adapter guarantees
// monotonicity by NEVER writing back a cursor that is "before" the one it
// already holds (DecodeCursor on both, take the max).
//
// Idempotency: re-calling ExchangeDeltas with the SAME `out` is a no-op on
// the cloud side — sha256(payload) is the dedup key in Service.Push and a
// repeat returns AcceptedDeltas=0 / DedupedDeltas=1. The adapter therefore
// has no work to do for re-calls; the cursor stays put, and the next /pull
// returns the same set of inbound deltas the previous call returned (because
// the cursor did not advance until the caller has consumed them — see the
// monotonicity guarantee).
//
// # What about the OS-side puller (vulos repo)?
//
// The OS-side mail box runs an HTTP client around this transport. The OS
// box embeds the SyncTransport, holds the cloud HTTP base URL, and feeds
// the SyncTransport's out → POST + the next /pull → in. This file is the
// CLOUD-side fixture the OS box talks to; the cross-repo wiring lives in
// the OS box's mail glue (out of scope here).
//
// Tests: see adapter_mail_test.go — exercises against a real `httptest.Server`
// running the real syncrz Handler routes (no stubs).

package syncrz

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Locally-mirrored mail seam types
//
// These mirror github.com/vul-os/vulos-mail/store/sync.{Cursor,Delta,BlobManifest}
// 1:1 so the adapter is API-compatible without importing the mail repo.
// FROZEN INVARIANT: NO Go-module dep on vulos-mail/relay/office.
// ---------------------------------------------------------------------------

// MailCursor mirrors vulos-mail/store/sync.Cursor — the HLC high-water mark
// vulos-mail's Coordinator advances per peer. The cloud adapter never
// inspects the fields; it round-trips the value through the Delta payload.
type MailCursor struct {
	Wall    int64
	Logical int64
	Replica string
}

// MailDelta mirrors vulos-mail/store/sync.Delta. Bytes is the opaque
// canonical-JSON cr-sqlite changeset; Version is the codec version.
type MailDelta struct {
	Version int
	Bytes   []byte
}

// MailBlobManifest mirrors vulos-mail/store/sync.BlobManifest. Hashes is the
// set of content-addressed blob refs the peer can serve.
type MailBlobManifest struct {
	Hashes []string
}

// MailSyncTransport mirrors vulos-mail/store/sync.SyncTransport, so an
// implementer can be passed wherever the mail package expects a transport
// (the OS wires it across as a thin shim). Method set is identical.
type MailSyncTransport interface {
	ExchangeDeltas(mailboxKey string, out MailDelta) (in MailDelta, manifest MailBlobManifest, err error)
	FetchBlob(mailboxKey string, hash string) ([]byte, error)
}

// ---------------------------------------------------------------------------
// MailHTTPAdapter — concrete MailSyncTransport that talks to the cloud
// syncrz routes over HTTP.
// ---------------------------------------------------------------------------

// MailHTTPAdapter is a SyncTransport-compatible cloud client that fronts
// the syncrz HTTP routes for ONE (accountID, boxID) pair.
//
// Concurrency: one adapter per (account, box). Methods are safe for
// concurrent use across different mailboxKey values because the per-key
// cursor state is held under a mutex.
type MailHTTPAdapter struct {
	// BaseURL is the cloud base, e.g. "https://cp.vulos.org". Required.
	BaseURL string

	// AccountID is the tenant the mailbox belongs to. Required.
	AccountID string

	// BoxID is the pusher's stable identity. Used both as the cloud
	// /push delta box_id and as the /pull box_id (so the cloud skips
	// echoing this box's own pushes back). Required.
	BoxID string

	// SharedSecret is the value put in X-Device-Auth, mirroring the
	// existing CP_SHARED_SECRET pattern (same header lancert and the
	// other CP↔box routes use).
	SharedSecret string

	// HTTPClient may be overridden in tests; otherwise http.DefaultClient.
	HTTPClient *http.Client

	// PullLimit caps the number of deltas a single /pull will return.
	// Zero → server default (DefaultPullLimit). Tests can lower this.
	PullLimit int

	// mu guards the per-mailboxKey cursor state. We keep one cursor per
	// (sync_key) because the cloud's /pull cursor is scoped per
	// (account, sync_key, box_id); the adapter is already scoped to
	// (account, box) so sync_key is the missing dimension.
	mu      sync.Mutex
	cursors map[string]string // mailboxKey → opaque base16 cursor
}

// NewMailHTTPAdapter constructs an adapter with sensible defaults.
func NewMailHTTPAdapter(baseURL, accountID, boxID, sharedSecret string) *MailHTTPAdapter {
	return &MailHTTPAdapter{
		BaseURL:      strings.TrimRight(baseURL, "/"),
		AccountID:    accountID,
		BoxID:        boxID,
		SharedSecret: sharedSecret,
		HTTPClient:   http.DefaultClient,
		cursors:      map[string]string{},
	}
}

// CursorFor returns the opaque cursor the adapter currently holds for
// mailboxKey, or "" if it has never pulled. Exported for tests / observability.
func (a *MailHTTPAdapter) CursorFor(mailboxKey string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cursors[mailboxKey]
}

// SetCursorFor seeds the per-mailboxKey cursor (e.g. after rehydrating from
// the box's local persisted-state). Cursor MUST be the opaque string the
// cloud returned on a prior /pull or /push; passing an arbitrary HLC tuple
// is not legal.
func (a *MailHTTPAdapter) SetCursorFor(mailboxKey, cursor string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cursors == nil {
		a.cursors = map[string]string{}
	}
	a.cursors[mailboxKey] = cursor
}

// ExchangeDeltas implements MailSyncTransport. It performs one /push (if
// `out` has any bytes) and then one /pull, returning the inbound delta and
// blob manifest.
//
// Cursor handling:
//
//   - The cursor used on /pull is the LATEST opaque cursor the adapter
//     holds for mailboxKey (or "" for a first call).
//
//   - The cursor returned by /pull replaces the held cursor IF AND ONLY IF
//     it is strictly greater than the current one (monotonic; never goes
//     backward even on a malformed server response).
//
// The mail repo's HLC cursor is round-tripped inside `out.Bytes` (mail's
// crsqlite changeset) and inside the returned `in.Bytes`. The adapter does
// not touch the HLC fields — only the cloud's opaque cursor space.
func (a *MailHTTPAdapter) ExchangeDeltas(mailboxKey string, out MailDelta) (MailDelta, MailBlobManifest, error) {
	if mailboxKey == "" {
		return MailDelta{}, MailBlobManifest{}, errors.New("syncrz: mailboxKey required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 1. Push the outbound delta (skip if mail had nothing to ship).
	if len(out.Bytes) > 0 {
		body := pushBody{
			AccountID: a.AccountID,
			SyncKey:   mailboxKey,
			Deltas: []pushBodyDelta{{
				BoxID:        a.BoxID,
				CodecVersion: out.Version,
				PayloadB64:   base64.StdEncoding.EncodeToString(out.Bytes),
			}},
		}
		if _, err := a.doPush(ctx, body); err != nil {
			return MailDelta{}, MailBlobManifest{}, fmt.Errorf("syncrz adapter: push: %w", err)
		}
		// Note: we DO NOT advance the held cursor off the push response.
		// The push response cursor is the high-water of OUR OWN pushes;
		// using it as the next /pull `?since=` would skip every inbound
		// delta that landed in the same id range. The pull below uses the
		// last-seen INBOUND cursor only.
	}

	// 2. Pull inbound deltas using the held cursor.
	since := a.CursorFor(mailboxKey)
	pr, err := a.doPull(ctx, mailboxKey, since)
	if err != nil {
		return MailDelta{}, MailBlobManifest{}, fmt.Errorf("syncrz adapter: pull: %w", err)
	}

	// 3. Advance the held cursor monotonically.
	if pr.Cursor != "" {
		newCursor := pr.Cursor
		curN, _ := DecodeCursor(since)
		newN, decErr := DecodeCursor(newCursor)
		// Defensive: a malformed server cursor must NOT regress us.
		if decErr == nil && newN >= curN {
			a.SetCursorFor(mailboxKey, newCursor)
		}
	}

	// 4. Re-encode all inbound deltas as ONE MailDelta. The mail
	// Coordinator's Apply step takes one opaque changeset blob, so we
	// concatenate the per-delta payloads using a tiny length-prefixed
	// framing — the OS-side mail glue is the symmetric framer (it owns
	// the actual cr-sqlite changeset merge). When there is only ONE
	// inbound delta (the common case during steady-state convergence)
	// the framing trivially flattens to the single payload.
	in, err := encodeInboundDeltas(pr.Deltas)
	if err != nil {
		return MailDelta{}, MailBlobManifest{}, fmt.Errorf("syncrz adapter: encode inbound: %w", err)
	}
	manifest := MailBlobManifest{Hashes: append([]string(nil), pr.Manifest...)}
	return in, manifest, nil
}

// FetchBlob implements MailSyncTransport by calling /api/sync/blob/<hash>.
// Returns the raw bytes; the cloud verifies content-addressing on the way
// out, but the caller (mail's Coordinator.reconcileBlobs) re-verifies.
func (a *MailHTTPAdapter) FetchBlob(mailboxKey string, hash string) ([]byte, error) {
	_ = mailboxKey // not part of the cloud blob path (per-account scoped, not per-mailbox)
	if hash == "" {
		return nil, errors.New("syncrz adapter: empty hash")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	u := fmt.Sprintf("%s/api/sync/blob/%s?account_id=%s", a.BaseURL, hash, a.AccountID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Device-Auth", a.SharedSecret)
	resp, err := a.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrUnknown
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("syncrz adapter: blob fetch status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// ---------------------------------------------------------------------------
// Internal HTTP helpers
// ---------------------------------------------------------------------------

func (a *MailHTTPAdapter) client() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return http.DefaultClient
}

func (a *MailHTTPAdapter) doPush(ctx context.Context, body pushBody) (pushResponse, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return pushResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+"/api/sync/push", bytes.NewReader(raw))
	if err != nil {
		return pushResponse{}, err
	}
	req.Header.Set("X-Device-Auth", a.SharedSecret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client().Do(req)
	if err != nil {
		return pushResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return pushResponse{}, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}
	var pr pushResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return pushResponse{}, err
	}
	return pr, nil
}

func (a *MailHTTPAdapter) doPull(ctx context.Context, mailboxKey, since string) (pullResponse, error) {
	u := fmt.Sprintf("%s/api/sync/pull?account_id=%s&sync_key=%s&box_id=%s",
		a.BaseURL, a.AccountID, mailboxKey, a.BoxID)
	if since != "" {
		u += "&since=" + since
	}
	if a.PullLimit > 0 {
		u += fmt.Sprintf("&limit=%d", a.PullLimit)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return pullResponse{}, err
	}
	req.Header.Set("X-Device-Auth", a.SharedSecret)
	resp, err := a.client().Do(req)
	if err != nil {
		return pullResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return pullResponse{}, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}
	var pr pullResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return pullResponse{}, err
	}
	return pr, nil
}

// encodeInboundDeltas folds N inbound deltas into a single MailDelta. With
// N=0 returns a zero MailDelta (no Bytes — mail's Coordinator.Sync treats
// this as "nothing to apply"). With N=1 returns the single payload as-is.
// With N≥2 returns a small length-prefixed framing that the OS-side mail
// glue knows how to split back into N changesets before merging.
//
// The frame is: ["VULOS-MAIL-DELTAS/1", <count uvarint>, [<len uvarint>, <bytes>] × count]
// — kept tiny and self-describing so a future format bump is detectable.
//
// NB: the version field returned is DeltaVersion=1 to match the mail repo's
// current codec; if multiple inbound deltas disagree on version, the
// highest one wins and the OS-side will reject mismatched payloads (per
// vulos-mail/store/sync.Sync) — same behaviour as today.
func encodeInboundDeltas(items []pullResponseItem) (MailDelta, error) {
	if len(items) == 0 {
		return MailDelta{}, nil
	}
	if len(items) == 1 {
		// Common case: pass through the single payload verbatim.
		raw, err := base64.StdEncoding.DecodeString(items[0].PayloadB64)
		if err != nil {
			return MailDelta{}, fmt.Errorf("decode payload: %w", err)
		}
		return MailDelta{Version: items[0].CodecVersion, Bytes: raw}, nil
	}
	// Multi-delta frame.
	var buf bytes.Buffer
	buf.WriteString("VULOS-MAIL-DELTAS/1\n")
	highestVer := 0
	for _, it := range items {
		raw, err := base64.StdEncoding.DecodeString(it.PayloadB64)
		if err != nil {
			return MailDelta{}, fmt.Errorf("decode payload: %w", err)
		}
		if it.CodecVersion > highestVer {
			highestVer = it.CodecVersion
		}
		fmt.Fprintf(&buf, "%d\n", len(raw))
		buf.Write(raw)
		buf.WriteByte('\n')
	}
	return MailDelta{Version: highestVer, Bytes: buf.Bytes()}, nil
}

// Compile-time assertion: MailHTTPAdapter satisfies MailSyncTransport.
var _ MailSyncTransport = (*MailHTTPAdapter)(nil)
