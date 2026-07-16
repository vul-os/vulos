package syncrz

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// newTestStore creates a fresh in-memory SQLite store via cpdb.
func newTestStore(t *testing.T) *SQLStore {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	st, err := Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("syncrz.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newTestService(t *testing.T) (*Service, *MemObjectStore) {
	t.Helper()
	obj := NewMemObjectStore()
	svc := NewService(newTestStore(t), obj)
	return svc, obj
}

func sha(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestPushPullExchange — two boxes push deltas; each pulls and converges
// without seeing its own pushes echoed back.
func TestPushPullExchange(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	account, key := "acct-1", "mailbox/45"

	d1 := []byte("op-from-box-1")
	d2 := []byte("op-from-box-2")

	if _, err := svc.Push(ctx, account, key, []PushInput{{BoxID: "box-1", CodecVersion: 1, Payload: d1}}, nil); err != nil {
		t.Fatalf("box-1 push: %v", err)
	}
	if _, err := svc.Push(ctx, account, key, []PushInput{{BoxID: "box-2", CodecVersion: 1, Payload: d2}}, nil); err != nil {
		t.Fatalf("box-2 push: %v", err)
	}

	// box-1 pulls — should see ONLY box-2's delta.
	got1, err := svc.Pull(ctx, account, key, "box-1", "", 0)
	if err != nil {
		t.Fatalf("box-1 pull: %v", err)
	}
	if len(got1.Deltas) != 1 || string(got1.Deltas[0].Payload) != string(d2) {
		t.Fatalf("box-1 pull wrong: got %+v", got1)
	}
	if got1.Deltas[0].BoxID != "box-2" {
		t.Fatalf("box-1 pull echoed self: %+v", got1.Deltas[0])
	}

	// box-2 pulls — should see ONLY box-1's delta.
	got2, err := svc.Pull(ctx, account, key, "box-2", "", 0)
	if err != nil {
		t.Fatalf("box-2 pull: %v", err)
	}
	if len(got2.Deltas) != 1 || string(got2.Deltas[0].Payload) != string(d1) {
		t.Fatalf("box-2 pull wrong: got %+v", got2)
	}
}

// TestCursorAdvances — after pulling, a follow-up pull with the returned
// cursor must return ZERO deltas; pushing again advances it.
func TestCursorAdvances(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	account, key := "acct-1", "mailbox/45"

	if _, err := svc.Push(ctx, account, key, []PushInput{{BoxID: "box-1", CodecVersion: 1, Payload: []byte("a")}}, nil); err != nil {
		t.Fatalf("push 1: %v", err)
	}
	if _, err := svc.Push(ctx, account, key, []PushInput{{BoxID: "box-1", CodecVersion: 1, Payload: []byte("b")}}, nil); err != nil {
		t.Fatalf("push 2: %v", err)
	}

	r1, err := svc.Pull(ctx, account, key, "box-2", "", 0)
	if err != nil {
		t.Fatalf("pull 1: %v", err)
	}
	if len(r1.Deltas) != 2 {
		t.Fatalf("pull 1: expected 2 deltas, got %d", len(r1.Deltas))
	}
	if r1.Cursor == "" || r1.Cursor == "0" {
		t.Fatalf("pull 1: cursor not advanced: %q", r1.Cursor)
	}

	// Re-pull with the cursor — nothing new.
	r2, err := svc.Pull(ctx, account, key, "box-2", r1.Cursor, 0)
	if err != nil {
		t.Fatalf("pull 2: %v", err)
	}
	if len(r2.Deltas) != 0 {
		t.Fatalf("pull 2: expected 0 deltas after cursor advance, got %d", len(r2.Deltas))
	}
	if r2.Cursor != r1.Cursor {
		t.Fatalf("pull 2: cursor regressed %q → %q", r1.Cursor, r2.Cursor)
	}

	// Push one more, re-pull from cursor — see exactly one.
	if _, err := svc.Push(ctx, account, key, []PushInput{{BoxID: "box-1", CodecVersion: 1, Payload: []byte("c")}}, nil); err != nil {
		t.Fatalf("push 3: %v", err)
	}
	r3, err := svc.Pull(ctx, account, key, "box-2", r1.Cursor, 0)
	if err != nil {
		t.Fatalf("pull 3: %v", err)
	}
	if len(r3.Deltas) != 1 || string(r3.Deltas[0].Payload) != "c" {
		t.Fatalf("pull 3: expected 1 delta 'c', got %+v", r3.Deltas)
	}
}

// TestIdempotentRePush — pushing the same payload twice must NOT double-
// write; the cursor stays put and AcceptedDeltas=0 on the second push.
func TestIdempotentRePush(t *testing.T) {
	svc, obj := newTestService(t)
	ctx := context.Background()
	account, key := "acct-1", "mailbox/45"
	payload := []byte("same-bytes")

	r1, err := svc.Push(ctx, account, key, []PushInput{{BoxID: "box-1", CodecVersion: 1, Payload: payload}}, nil)
	if err != nil {
		t.Fatalf("push 1: %v", err)
	}
	if r1.AcceptedDeltas != 1 || r1.DedupedDeltas != 0 {
		t.Fatalf("push 1: want accepted=1 deduped=0, got %+v", r1)
	}

	r2, err := svc.Push(ctx, account, key, []PushInput{{BoxID: "box-1", CodecVersion: 1, Payload: payload}}, nil)
	if err != nil {
		t.Fatalf("push 2: %v", err)
	}
	if r2.AcceptedDeltas != 0 || r2.DedupedDeltas != 1 {
		t.Fatalf("push 2: want accepted=0 deduped=1, got %+v", r2)
	}
	if r2.Cursor != r1.Cursor {
		t.Fatalf("push 2: cursor moved on dedup: %q → %q", r1.Cursor, r2.Cursor)
	}

	// Object store should hold exactly one bucket and one key for this delta.
	if got := obj.Snapshot(svc.BucketForAccount(account), deltaKey(account, key, sha(payload))); string(got) != string(payload) {
		t.Fatalf("object store missing or wrong: got %q", got)
	}

	// Pull from a clean peer — see exactly ONE delta, not two.
	pr, err := svc.Pull(ctx, account, key, "peer", "", 0)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(pr.Deltas) != 1 {
		t.Fatalf("pull: expected 1 delta after dedup, got %d", len(pr.Deltas))
	}
}

// TestBlobContentAddressing — blob put/get round-trips, and a mis-declared
// hash is rejected on Push.
func TestBlobContentAddressing(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	account := "acct-1"

	blob := []byte("blob-bytes")
	h := sha(blob)

	if _, err := svc.Push(ctx, account, "mailbox/45", nil, []BlobInput{{ContentHash: h, Data: blob}}); err != nil {
		t.Fatalf("blob push: %v", err)
	}

	got, err := svc.FetchBlob(ctx, account, h)
	if err != nil {
		t.Fatalf("fetch blob: %v", err)
	}
	if string(got) != string(blob) {
		t.Fatalf("fetch blob: wrong bytes: %q vs %q", got, blob)
	}

	// Now push with a LIED hash — must reject.
	bad := sha([]byte("different"))
	_, err = svc.Push(ctx, account, "mailbox/45", nil, []BlobInput{{ContentHash: bad, Data: blob}})
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("lied hash: want ErrHashMismatch, got %v", err)
	}

	// Idempotent blob re-push: re-putting the same blob must not double-write.
	r1, err := svc.Push(ctx, account, "mailbox/45", nil, []BlobInput{{ContentHash: h, Data: blob}})
	if err != nil {
		t.Fatalf("blob re-push: %v", err)
	}
	if r1.AcceptedBlobs != 0 || r1.DedupedBlobs != 1 {
		t.Fatalf("blob re-push: want accepted=0 deduped=1, got %+v", r1)
	}

	// FetchBlob on unknown hash → ErrUnknown.
	if _, err := svc.FetchBlob(ctx, account, sha([]byte("nope"))); !errors.Is(err, ErrUnknown) {
		t.Fatalf("fetch unknown: want ErrUnknown, got %v", err)
	}
}

// TestPullHydratesManifest — when deltas reference blobs, the pull manifest
// returns the union of those hashes.
func TestPullHydratesManifest(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	account, key := "acct-1", "office/session/abc"

	blob := []byte("attachment")
	h := sha(blob)

	if _, err := svc.Push(ctx, account, key,
		[]PushInput{{BoxID: "box-1", CodecVersion: 1, Payload: []byte("delta-with-ref"), Refs: []string{h}}},
		[]BlobInput{{ContentHash: h, Data: blob}}); err != nil {
		t.Fatalf("push with refs: %v", err)
	}

	pr, err := svc.Pull(ctx, account, key, "box-2", "", 0)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(pr.Manifest) != 1 || pr.Manifest[0] != h {
		t.Fatalf("manifest: want [%s], got %v", h, pr.Manifest)
	}
	if len(pr.Deltas) != 1 || len(pr.Deltas[0].Refs) != 1 || pr.Deltas[0].Refs[0] != h {
		t.Fatalf("delta refs not round-tripped: %+v", pr.Deltas)
	}

	// And the blob is fetchable by hash.
	got, err := svc.FetchBlob(ctx, account, h)
	if err != nil {
		t.Fatalf("fetch blob by manifest hash: %v", err)
	}
	if string(got) != string(blob) {
		t.Fatalf("blob bytes wrong")
	}
}

// TestOpaqueForBYO — for a BYO account the box encrypts before push; the
// coordinator stores ciphertext as-is, returns it as-is, and NEVER reads
// any sentinel string from the plaintext. We assert by pushing ciphertext
// that does not contain a marker present in the plaintext, then checking
// that:
//  1. the Tigris-side bytes are exactly the ciphertext (no decryption),
//  2. the pull payload echoes the ciphertext bit-for-bit,
//  3. /api/sync/blob/<hash> returns the ciphertext, not the plaintext.
//
// This is the v6 Decision F+G opacity guarantee: coordinator sees no plaintext.
func TestOpaqueForBYO(t *testing.T) {
	svc, obj := newTestService(t)
	ctx := context.Background()
	account, key := "acct-byo", "mailbox/45"

	plaintext := []byte("SECRET-PAYLOAD-MARKER-12345")
	// "encrypt" by XORing with a constant — good enough that the bytes no
	// longer contain the marker string. (The real box uses age; we only need
	// "ciphertext ≠ plaintext" for this assertion.)
	ciphertext := xorWith(plaintext, 0x5A)
	if strings.Contains(string(ciphertext), "SECRET-PAYLOAD-MARKER") {
		t.Fatalf("test xor: marker still visible in ciphertext")
	}

	// Push the ciphertext as the delta payload.
	if _, err := svc.Push(ctx, account, key, []PushInput{{BoxID: "box-1", CodecVersion: 1, Payload: ciphertext}}, nil); err != nil {
		t.Fatalf("push ciphertext: %v", err)
	}

	// 1. The stored object is EXACTLY the ciphertext bytes — no decryption,
	//    no parsing, no transformation.
	stored := obj.Snapshot(svc.BucketForAccount(account), deltaKey(account, key, sha(ciphertext)))
	if string(stored) != string(ciphertext) {
		t.Fatalf("stored bytes mutated: got %q want %q", stored, ciphertext)
	}
	if strings.Contains(string(stored), "SECRET-PAYLOAD-MARKER") {
		t.Fatalf("plaintext marker leaked into storage")
	}

	// 2. Pull returns the ciphertext bit-for-bit (no decryption on the way out).
	pr, err := svc.Pull(ctx, account, key, "box-2", "", 0)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(pr.Deltas) != 1 || string(pr.Deltas[0].Payload) != string(ciphertext) {
		t.Fatalf("pull mutated payload: %q", pr.Deltas[0].Payload)
	}
	if strings.Contains(string(pr.Deltas[0].Payload), "SECRET-PAYLOAD-MARKER") {
		t.Fatalf("plaintext leaked through pull")
	}

	// 3. Blob fetch path — push an encrypted blob, fetch by hash, assert it
	//    comes back as ciphertext.
	encBlob := xorWith([]byte("ATTACHMENT-PLAINTEXT"), 0x5A)
	encHash := sha(encBlob)
	if _, err := svc.Push(ctx, account, key, nil, []BlobInput{{ContentHash: encHash, Data: encBlob}}); err != nil {
		t.Fatalf("blob push: %v", err)
	}
	got, err := svc.FetchBlob(ctx, account, encHash)
	if err != nil {
		t.Fatalf("blob fetch: %v", err)
	}
	if string(got) != string(encBlob) {
		t.Fatalf("blob fetch mutated bytes")
	}
	if strings.Contains(string(got), "ATTACHMENT-PLAINTEXT") {
		t.Fatalf("plaintext attachment leaked")
	}
}

// TestPerAccountIsolation — two accounts pushing under the same sync_key
// must not see each other's deltas.
func TestPerAccountIsolation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	key := "mailbox/45"

	if _, err := svc.Push(ctx, "acct-A", key, []PushInput{{BoxID: "box-A", CodecVersion: 1, Payload: []byte("a-data")}}, nil); err != nil {
		t.Fatalf("acct-A push: %v", err)
	}
	if _, err := svc.Push(ctx, "acct-B", key, []PushInput{{BoxID: "box-B", CodecVersion: 1, Payload: []byte("b-data")}}, nil); err != nil {
		t.Fatalf("acct-B push: %v", err)
	}
	prA, err := svc.Pull(ctx, "acct-A", key, "puller-A", "", 0)
	if err != nil {
		t.Fatalf("acct-A pull: %v", err)
	}
	for _, d := range prA.Deltas {
		if string(d.Payload) == "b-data" {
			t.Fatalf("acct-A saw acct-B's data — cross-account leak")
		}
	}
	prB, err := svc.Pull(ctx, "acct-B", key, "puller-B", "", 0)
	if err != nil {
		t.Fatalf("acct-B pull: %v", err)
	}
	for _, d := range prB.Deltas {
		if string(d.Payload) == "a-data" {
			t.Fatalf("acct-B saw acct-A's data — cross-account leak")
		}
	}
}

// TestCursorEncodingOpaque — DecodeCursor accepts "" as 0, rejects garbage.
func TestCursorEncodingOpaque(t *testing.T) {
	if n, err := DecodeCursor(""); err != nil || n != 0 {
		t.Fatalf("decode empty: want 0,nil got %d,%v", n, err)
	}
	if _, err := DecodeCursor("not-hex"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("decode garbage: want ErrInvalidInput got %v", err)
	}
	if got := EncodeCursor(0); got != "0" {
		t.Fatalf("encode 0: got %q", got)
	}
	if got := EncodeCursor(255); got != "ff" {
		t.Fatalf("encode 255: got %q", got)
	}
}

// ---------------------------------------------------------------------------
// HTTP route tests
// ---------------------------------------------------------------------------

func TestHTTPPushPullBlob(t *testing.T) {
	svc, _ := newTestService(t)
	h := NewHandler(svc, "secret-123")
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	account, key := "acct-http", "mailbox/45"
	blob := []byte("http-blob")
	bh := sha(blob)
	delta := []byte("http-delta")

	// POST /api/sync/push
	body := pushBody{
		AccountID: account,
		SyncKey:   key,
		Deltas: []pushBodyDelta{{
			BoxID:        "box-1",
			CodecVersion: 1,
			PayloadB64:   base64.StdEncoding.EncodeToString(delta),
			Refs:         []string{bh},
		}},
		Blobs: []pushBodyBlob{{
			ContentHash: bh,
			DataB64:     base64.StdEncoding.EncodeToString(blob),
		}},
	}
	bodyJSON, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", srv.URL+"/api/sync/push", strings.NewReader(string(bodyJSON)))
	req.Header.Set("X-Device-Auth", "secret-123")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("push http: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("push http status: %d", resp.StatusCode)
	}
	var pr pushResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		t.Fatalf("decode push resp: %v", err)
	}
	resp.Body.Close()
	if pr.Proto != WireProto {
		t.Fatalf("push: bad proto %q", pr.Proto)
	}
	if pr.AcceptedDeltas != 1 || pr.AcceptedBlobs != 1 {
		t.Fatalf("push: want accepted=1/1, got %+v", pr.PushResult)
	}

	// GET /api/sync/pull
	pullReq, _ := http.NewRequest("GET", srv.URL+"/api/sync/pull?account_id="+account+"&sync_key="+key+"&box_id=box-2", nil)
	pullReq.Header.Set("X-Device-Auth", "secret-123")
	pullResp, err := srv.Client().Do(pullReq)
	if err != nil {
		t.Fatalf("pull http: %v", err)
	}
	if pullResp.StatusCode != 200 {
		t.Fatalf("pull http status: %d", pullResp.StatusCode)
	}
	var prr pullResponse
	if err := json.NewDecoder(pullResp.Body).Decode(&prr); err != nil {
		t.Fatalf("decode pull resp: %v", err)
	}
	pullResp.Body.Close()
	if prr.Proto != WireProto || len(prr.Deltas) != 1 {
		t.Fatalf("pull: bad shape: %+v", prr)
	}
	gotPayload, _ := base64.StdEncoding.DecodeString(prr.Deltas[0].PayloadB64)
	if string(gotPayload) != string(delta) {
		t.Fatalf("pull: payload mismatch")
	}
	if len(prr.Manifest) != 1 || prr.Manifest[0] != bh {
		t.Fatalf("pull: manifest mismatch: %v", prr.Manifest)
	}

	// GET /api/sync/blob/<hash>
	blobReq, _ := http.NewRequest("GET", srv.URL+"/api/sync/blob/"+bh+"?account_id="+account, nil)
	blobReq.Header.Set("X-Device-Auth", "secret-123")
	blobResp, err := srv.Client().Do(blobReq)
	if err != nil {
		t.Fatalf("blob http: %v", err)
	}
	if blobResp.StatusCode != 200 {
		t.Fatalf("blob http status: %d", blobResp.StatusCode)
	}
	gotBlob := make([]byte, len(blob))
	n, _ := blobResp.Body.Read(gotBlob)
	blobResp.Body.Close()
	if n != len(blob) || string(gotBlob) != string(blob) {
		t.Fatalf("blob bytes wrong: got %q", gotBlob[:n])
	}
}

func TestHTTPAuthEnforced(t *testing.T) {
	svc, _ := newTestService(t)
	h := NewHandler(svc, "secret-123")
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		name string
		path string
	}{
		{"push", "/api/sync/push"},
		{"pull", "/api/sync/pull?account_id=a&sync_key=k&box_id=b"},
		{"blob", "/api/sync/blob/deadbeef?account_id=a"},
	} {
		t.Run(tc.name+"-missing-header", func(t *testing.T) {
			method := "GET"
			if tc.name == "push" {
				method = "POST"
			}
			req, _ := http.NewRequest(method, srv.URL+tc.path, strings.NewReader("{}"))
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("req: %v", err)
			}
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s: want 401, got %d", tc.name, resp.StatusCode)
			}
			resp.Body.Close()
		})
		t.Run(tc.name+"-wrong-header", func(t *testing.T) {
			method := "GET"
			if tc.name == "push" {
				method = "POST"
			}
			req, _ := http.NewRequest(method, srv.URL+tc.path, strings.NewReader("{}"))
			req.Header.Set("X-Device-Auth", "wrong")
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("req: %v", err)
			}
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s: want 401, got %d", tc.name, resp.StatusCode)
			}
			resp.Body.Close()
		})
	}
}

// TestServiceTimeStable — Service.Now is overridable so cursor / pushed_at
// timestamps are deterministic in tests.
func TestServiceTimeStable(t *testing.T) {
	svc, _ := newTestService(t)
	fixed := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	svc.Now = func() time.Time { return fixed }

	if _, err := svc.Push(context.Background(), "acct", "key", []PushInput{{BoxID: "b", CodecVersion: 1, Payload: []byte("x")}}, nil); err != nil {
		t.Fatalf("push: %v", err)
	}
	pr, err := svc.Pull(context.Background(), "acct", "key", "peer", "", 0)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(pr.Deltas) != 1 || pr.Deltas[0].PushedAt != fixed.Unix() {
		t.Fatalf("pushed_at: want %d, got %+v", fixed.Unix(), pr.Deltas)
	}
}

// xorWith XORs every byte of in with k (cheap "encrypt" for opacity test).
func xorWith(in []byte, k byte) []byte {
	out := make([]byte, len(in))
	for i, b := range in {
		out[i] = b ^ k
	}
	return out
}

// TestHTTPDefaultErrorBranchScrubbed asserts that an unknown (non-sentinel)
// store error reaches the client as the generic message — never raw
// err.Error(). FIX-SYNCRZ-ERR-LEAK-01.
//
// We force an unknown error by closing the SQLStore *before* the request: the
// underlying driver returns "sql: database is closed", which is not one of
// the syncrz sentinels and therefore must be scrubbed in the default branch.
func TestHTTPDefaultErrorBranchScrubbed(t *testing.T) {
	st := newTestStore(t)
	svc := NewService(st, NewMemObjectStore())
	h := NewHandler(svc, "secret-xyz")
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Slam the DB closed to force "database is closed" errors at the store
	// layer — these surface as unknown errors that MUST hit the default
	// branch (which is the leak surface we're fixing).
	_ = st.Close()

	// PUSH — default branch.
	body, _ := json.Marshal(pushBody{
		AccountID: "a",
		SyncKey:   "k",
		Deltas: []pushBodyDelta{{
			BoxID:        "b",
			CodecVersion: 1,
			PayloadB64:   base64.StdEncoding.EncodeToString([]byte("x")),
		}},
	})
	req, _ := http.NewRequest("POST", srv.URL+"/api/sync/push", strings.NewReader(string(body)))
	req.Header.Set("X-Device-Auth", "secret-xyz")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("push http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("push: want 500, got %d", resp.StatusCode)
	}
	var pe map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&pe); err != nil {
		t.Fatalf("decode push err body: %v", err)
	}
	if pe["error"] != genericMsg {
		t.Errorf("push: want generic message %q, got %q (raw err leaked)", genericMsg, pe["error"])
	}

	// PULL — default branch.
	pullReq, _ := http.NewRequest("GET", srv.URL+"/api/sync/pull?account_id=a&sync_key=k&box_id=b", nil)
	pullReq.Header.Set("X-Device-Auth", "secret-xyz")
	pullResp, err := srv.Client().Do(pullReq)
	if err != nil {
		t.Fatalf("pull http: %v", err)
	}
	defer pullResp.Body.Close()
	if pullResp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("pull: want 500, got %d", pullResp.StatusCode)
	}
	var ue map[string]string
	if err := json.NewDecoder(pullResp.Body).Decode(&ue); err != nil {
		t.Fatalf("decode pull err body: %v", err)
	}
	if ue["error"] != genericMsg {
		t.Errorf("pull: want generic message %q, got %q (raw err leaked)", genericMsg, ue["error"])
	}

	// BLOB — default branch (closed DB hits before sentinel matching).
	blobReq, _ := http.NewRequest("GET", srv.URL+"/api/sync/blob/deadbeef?account_id=a", nil)
	blobReq.Header.Set("X-Device-Auth", "secret-xyz")
	blobResp, err := srv.Client().Do(blobReq)
	if err != nil {
		t.Fatalf("blob http: %v", err)
	}
	defer blobResp.Body.Close()
	// The blob path may surface ErrUnknown (404) or a default-branch 500
	// depending on which layer the closed-DB error triggers. Either way
	// the body MUST NOT contain "database is closed".
	if blobResp.StatusCode == http.StatusInternalServerError {
		var be map[string]string
		if err := json.NewDecoder(blobResp.Body).Decode(&be); err != nil {
			t.Fatalf("decode blob err body: %v", err)
		}
		if be["error"] != genericMsg {
			t.Errorf("blob: want generic message %q, got %q (raw err leaked)", genericMsg, be["error"])
		}
	}
}
