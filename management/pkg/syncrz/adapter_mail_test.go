// adapter_mail_test.go — tests for MailHTTPAdapter against a real
// httptest.Server running the real syncrz Handler. No stubs of the cloud
// side — only the OS/mail seam is mocked (by driving the adapter directly).

package syncrz

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// helper: spin up a real syncrz Handler-backed httptest.Server.
func newAdapterTestServer(t *testing.T) (*httptest.Server, *Service) {
	t.Helper()
	svc, _ := newTestService(t)
	h := NewHandler(svc, "adapter-secret")
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, svc
}

// TestAdapter_ExchangeDeltas_PushThenPull verifies that a single
// ExchangeDeltas call splits into push-then-pull against the real routes.
// Box-1 ships an outbound delta; Box-2's adapter then ships its own and
// receives Box-1's back (no echo for Box-1's own).
func TestAdapter_ExchangeDeltas_PushThenPull(t *testing.T) {
	srv, _ := newAdapterTestServer(t)
	account, key := "acct-mail", "mailbox/45"

	a1 := NewMailHTTPAdapter(srv.URL, account, "box-1", "adapter-secret")
	a2 := NewMailHTTPAdapter(srv.URL, account, "box-2", "adapter-secret")

	out1 := MailDelta{Version: 1, Bytes: []byte("op-from-box-1")}
	in1, _, err := a1.ExchangeDeltas(key, out1)
	if err != nil {
		t.Fatalf("box-1 exchange: %v", err)
	}
	// Box-1 pushed but nothing was waiting for it on /pull → in1 must be empty.
	if len(in1.Bytes) != 0 {
		t.Fatalf("box-1 should see nothing on first round, got %q", in1.Bytes)
	}

	out2 := MailDelta{Version: 1, Bytes: []byte("op-from-box-2")}
	in2, mf2, err := a2.ExchangeDeltas(key, out2)
	if err != nil {
		t.Fatalf("box-2 exchange: %v", err)
	}
	// Box-2 must see box-1's delta but NOT its own.
	if string(in2.Bytes) != "op-from-box-1" {
		t.Fatalf("box-2 inbound: want %q, got %q", "op-from-box-1", in2.Bytes)
	}
	if len(mf2.Hashes) != 0 {
		t.Errorf("box-2 manifest: want empty (no refs), got %v", mf2.Hashes)
	}

	// Box-1 now pulls and should see box-2's delta.
	in1b, _, err := a1.ExchangeDeltas(key, MailDelta{})
	if err != nil {
		t.Fatalf("box-1 second exchange: %v", err)
	}
	if string(in1b.Bytes) != "op-from-box-2" {
		t.Fatalf("box-1 second inbound: want %q, got %q", "op-from-box-2", in1b.Bytes)
	}
}

// TestAdapter_Idempotent_ReCall — calling ExchangeDeltas with the SAME
// outbound delta twice must not double-write on the server, and the held
// cursor must NOT regress.
func TestAdapter_Idempotent_ReCall(t *testing.T) {
	srv, svc := newAdapterTestServer(t)
	account, key := "acct-mail", "mailbox/45"

	a := NewMailHTTPAdapter(srv.URL, account, "box-1", "adapter-secret")

	// First call: push.
	if _, _, err := a.ExchangeDeltas(key, MailDelta{Version: 1, Bytes: []byte("dup-op")}); err != nil {
		t.Fatalf("exchange 1: %v", err)
	}
	c1 := a.CursorFor(key)

	// Second call with same payload: server must dedup (Push returns
	// AcceptedDeltas=0 / DedupedDeltas=1). Adapter must hold a non-
	// regressed cursor.
	if _, _, err := a.ExchangeDeltas(key, MailDelta{Version: 1, Bytes: []byte("dup-op")}); err != nil {
		t.Fatalf("exchange 2: %v", err)
	}
	c2 := a.CursorFor(key)
	cn1, _ := DecodeCursor(c1)
	cn2, _ := DecodeCursor(c2)
	if cn2 < cn1 {
		t.Fatalf("cursor regressed: %d → %d", cn1, cn2)
	}

	// And a pull from a separate puller-box must see EXACTLY one delta —
	// not two — proving the server dedup'd.
	other := NewMailHTTPAdapter(srv.URL, account, "puller", "adapter-secret")
	in, _, err := other.ExchangeDeltas(key, MailDelta{})
	if err != nil {
		t.Fatalf("puller exchange: %v", err)
	}
	if !bytes.Equal(in.Bytes, []byte("dup-op")) {
		t.Fatalf("puller saw wrong payload: %q", in.Bytes)
	}
	// Direct svc.Pull sanity-check that dedup actually happened on the server.
	pr, err := svc.Pull(context.Background(), account, key, "another", "", 0)
	if err != nil {
		t.Fatalf("svc.Pull: %v", err)
	}
	if len(pr.Deltas) != 1 {
		t.Fatalf("dedup failed at server: got %d deltas, want 1", len(pr.Deltas))
	}
}

// TestAdapter_CursorMonotonic_HLCTranslation — successive ExchangeDeltas
// calls must produce a strictly non-decreasing opaque cursor (the cloud
// side's autoinc id space). A later /pull with the held cursor must return
// only NEW deltas. Mirrors the HLC↔opaque translation invariant: even
// though mail uses an HLC cursor that is unrelated to the autoinc space,
// the cloud-side cursor the adapter holds must be ordered.
func TestAdapter_CursorMonotonic_HLCTranslation(t *testing.T) {
	srv, _ := newAdapterTestServer(t)
	account, key := "acct-mail", "mailbox/45"

	pusher := NewMailHTTPAdapter(srv.URL, account, "pusher", "adapter-secret")
	puller := NewMailHTTPAdapter(srv.URL, account, "puller", "adapter-secret")

	// Push three deltas via the adapter, each in its own ExchangeDeltas.
	payloads := [][]byte{[]byte("a"), []byte("b"), []byte("c")}
	for _, p := range payloads {
		if _, _, err := pusher.ExchangeDeltas(key, MailDelta{Version: 1, Bytes: p}); err != nil {
			t.Fatalf("push %q: %v", p, err)
		}
	}

	// Puller's first round: should see all three (folded into one
	// MailDelta with the framing). Cursor advances past all three.
	in1, _, err := puller.ExchangeDeltas(key, MailDelta{})
	if err != nil {
		t.Fatalf("puller round 1: %v", err)
	}
	// With multi-delta framing we expect the magic line first.
	if !bytes.HasPrefix(in1.Bytes, []byte("VULOS-MAIL-DELTAS/1\n")) {
		t.Fatalf("multi-delta framing missing: %q", in1.Bytes)
	}
	for _, p := range payloads {
		if !bytes.Contains(in1.Bytes, p) {
			t.Fatalf("expected to see %q in folded inbound: %q", p, in1.Bytes)
		}
	}
	cursorAfter1 := puller.CursorFor(key)
	n1, err := DecodeCursor(cursorAfter1)
	if err != nil || n1 == 0 {
		t.Fatalf("cursor after round 1 must be non-zero, got %q (err=%v)", cursorAfter1, err)
	}

	// Puller's second round (no new pushes in between): server has
	// nothing past the cursor → inbound empty, cursor unchanged.
	in2, _, err := puller.ExchangeDeltas(key, MailDelta{})
	if err != nil {
		t.Fatalf("puller round 2: %v", err)
	}
	if len(in2.Bytes) != 0 {
		t.Fatalf("puller round 2 should be empty, got %q", in2.Bytes)
	}
	if puller.CursorFor(key) != cursorAfter1 {
		t.Fatalf("cursor regressed/changed on empty pull: %q → %q",
			cursorAfter1, puller.CursorFor(key))
	}

	// One more push, then a final pull: cursor must strictly advance.
	if _, _, err := pusher.ExchangeDeltas(key, MailDelta{Version: 1, Bytes: []byte("d")}); err != nil {
		t.Fatalf("push d: %v", err)
	}
	in3, _, err := puller.ExchangeDeltas(key, MailDelta{})
	if err != nil {
		t.Fatalf("puller round 3: %v", err)
	}
	if !bytes.Equal(in3.Bytes, []byte("d")) {
		t.Fatalf("puller round 3: want %q, got %q", "d", in3.Bytes)
	}
	n3, _ := DecodeCursor(puller.CursorFor(key))
	if n3 <= n1 {
		t.Fatalf("cursor must strictly advance after new delta: %d → %d", n1, n3)
	}
}

// TestAdapter_BadServerCursor_DoesNotRegress — even if the server returns
// a corrupted cursor, the adapter must NOT regress its held value.
// Guarded by the DecodeCursor + max check in ExchangeDeltas.
func TestAdapter_BadServerCursor_DoesNotRegress(t *testing.T) {
	// We can't make the real Service emit a bad cursor, so we drive the
	// MalformedCursor path through a wrapping httptest.Server that returns
	// a tampered pull response.
	realSrv, _ := newAdapterTestServer(t)
	var firstPullDone bool
	wrap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync/pull" && firstPullDone {
			// Second pull: return a syntactically valid response but with
			// a cursor < the previously-issued one. ("0" decodes to 0.)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"proto":%q,"cursor":"0","deltas":[],"manifest":[]}`, WireProto)
			return
		}
		if r.URL.Path == "/api/sync/pull" {
			firstPullDone = true
		}
		// Proxy other paths to the real server by following an HTTP redirect.
		// (Simpler: just forward.)
		proxyURL := realSrv.URL + r.URL.Path
		if r.URL.RawQuery != "" {
			proxyURL += "?" + r.URL.RawQuery
		}
		req, _ := http.NewRequest(r.Method, proxyURL, r.Body)
		req.Header = r.Header.Clone()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(wrap.Close)

	account, key := "acct-mail", "mailbox/45"
	pusher := NewMailHTTPAdapter(realSrv.URL, account, "pusher", "adapter-secret")
	if _, _, err := pusher.ExchangeDeltas(key, MailDelta{Version: 1, Bytes: []byte("a")}); err != nil {
		t.Fatalf("seed push: %v", err)
	}

	a := NewMailHTTPAdapter(wrap.URL, account, "puller", "adapter-secret")
	if _, _, err := a.ExchangeDeltas(key, MailDelta{}); err != nil {
		t.Fatalf("round 1: %v", err)
	}
	c1 := a.CursorFor(key)
	if c1 == "" || c1 == "0" {
		t.Fatalf("round 1 cursor expected non-zero: %q", c1)
	}
	// Round 2 receives the tampered cursor="0" — adapter must NOT regress.
	if _, _, err := a.ExchangeDeltas(key, MailDelta{}); err != nil {
		t.Fatalf("round 2: %v", err)
	}
	c2 := a.CursorFor(key)
	if c2 != c1 {
		t.Fatalf("cursor regressed on bad server response: %q → %q", c1, c2)
	}
}

// TestAdapter_FetchBlob — blob fetch round-trips through /api/sync/blob/<hash>.
func TestAdapter_FetchBlob(t *testing.T) {
	srv, svc := newAdapterTestServer(t)
	account, key := "acct-mail", "mailbox/45"

	// Seed a blob directly via the Service (the OS-side mail glue pushes
	// blobs in the SAME /push call; see pushBody.Blobs — but the adapter
	// itself only carries deltas, so for this test we seed via svc.Push
	// and then prove adapter.FetchBlob can pull it back).
	blob := []byte("blob-bytes")
	bh := sha(blob)
	if _, err := svc.Push(context.Background(), account, key, nil,
		[]BlobInput{{ContentHash: bh, Data: blob}}); err != nil {
		t.Fatalf("seed blob: %v", err)
	}

	a := NewMailHTTPAdapter(srv.URL, account, "puller", "adapter-secret")
	got, err := a.FetchBlob(key, bh)
	if err != nil {
		t.Fatalf("FetchBlob: %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Fatalf("FetchBlob bytes: %q vs %q", got, blob)
	}

	// Unknown hash → ErrUnknown.
	if _, err := a.FetchBlob(key, sha([]byte("missing"))); err == nil {
		t.Fatalf("FetchBlob missing: want error, got nil")
	}
}

// keep strings imported even if test set changes
var _ = strings.TrimSpace
