package files

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ── test signer ──────────────────────────────────────────────────────────────

// testSigner implements PeerSigner with a fixed Ed25519 keypair, encoding the
// identity as a Vula-ID-shaped string. Verify decodes the embedded public key
// from the ID, mirroring the production peering.VerifyVulaSignature contract.
type testSigner struct {
	id   string
	priv ed25519.PrivateKey
}

func newTestSigner(t *testing.T) testSigner {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return testSigner{id: "k:" + base64.RawURLEncoding.EncodeToString(pub), priv: priv}
}

func (s testSigner) SelfID() string         { return s.id }
func (s testSigner) Sign(msg []byte) []byte { return ed25519.Sign(s.priv, msg) }

// Verify decodes the public key from ANY peer's id of the form "k:<b64url(pub)>".
func (s testSigner) Verify(id string, msg, sig []byte) error {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, "k:"))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return errCapTest
	}
	if !ed25519.Verify(ed25519.PublicKey(raw), msg, sig) {
		return errCapTest
	}
	return nil
}

var errCapTest = ErrCapability

// loopbackTransport routes Fetch straight into an owner Service's ServeCapability
// — an in-process "fake peer" so the end-to-end redeem path is exercised without
// real networking.
type loopbackTransport struct{ owner *Service }

func (l loopbackTransport) Fetch(ctx context.Context, _ string, req PeerFetchRequest) (io.ReadCloser, int64, error) {
	rc, size, _, err := l.owner.ServeCapability(ctx, req)
	if err != nil {
		return nil, 0, err
	}
	return rc, size, nil
}

// peerServices builds two Services (owner + recipient) sharing a fake broker each,
// each with its own identity. The recipient's transport loops back to the owner.
func peerServices(t *testing.T) (ownerSvc *Service, recipSvc *Service, ownerSigner, recipSigner testSigner) {
	t.Helper()
	ownerSvc, _ = newTestService(t)
	recipSvc, _ = newTestService(t)
	ownerSigner = newTestSigner(t)
	recipSigner = newTestSigner(t)
	ownerSvc.WithPeer(ownerSigner, nil, t.TempDir())
	recipSvc.WithPeer(recipSigner, loopbackTransport{owner: ownerSvc}, t.TempDir())
	return
}

// seedFile creates a committed file with body in owner's Drive root.
func seedFile(t *testing.T, svc *Service, ownerID, name, body string) *Node {
	t.Helper()
	ctx := context.Background()
	n, _, err := svc.UploadGrant(ctx, ownerID, "", name, "text/plain", ttl)
	if err != nil {
		t.Fatalf("UploadGrant: %v", err)
	}
	if _, err := svc.PutContent(ctx, ownerID, n.ID, strings.NewReader(body), int64(len(body)), "text/plain"); err != nil {
		t.Fatalf("PutContent: %v", err)
	}
	if _, err := svc.Commit(ownerID, n.ID, int64(len(body)), "text/plain", "etag"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return mustNode(t, svc, n.ID)
}

func mustNode(t *testing.T, svc *Service, id string) *Node {
	t.Helper()
	n, err := svc.getNode(id)
	if err != nil {
		t.Fatalf("getNode: %v", err)
	}
	return n
}

// ── capability issue / verify / expiry / revoke ─────────────────────────────

func TestCapabilityIssueAndVerify(t *testing.T) {
	owner, _, _, _ := peerServices(t)
	n := seedFile(t, owner, "userOwner", "doc.txt", "hello peer")

	cap, link, err := owner.IssueCapability("userOwner", n.ID, RoleViewer, "", "https://owner.example", time.Hour)
	if err != nil {
		t.Fatalf("IssueCapability: %v", err)
	}
	if cap.Signature == "" || cap.OwnerVulosID != owner.signer.SelfID() {
		t.Fatalf("capability not properly signed: %+v", cap)
	}
	if err := owner.VerifyCapability(cap); err != nil {
		t.Fatalf("VerifyCapability(fresh): %v", err)
	}
	// The link round-trips and verifies.
	dec, err := DecodeCapabilityLink(link)
	if err != nil {
		t.Fatalf("DecodeCapabilityLink: %v", err)
	}
	if err := owner.VerifyCapability(dec); err != nil {
		t.Fatalf("VerifyCapability(decoded link): %v", err)
	}
	if dec.ID != cap.ID || dec.NodeID != n.ID {
		t.Fatalf("decoded link mismatch: %+v", dec)
	}
}

func TestCapabilityTamperRejected(t *testing.T) {
	owner, _, _, _ := peerServices(t)
	n := seedFile(t, owner, "userOwner", "doc.txt", "data")
	cap, _, err := owner.IssueCapability("userOwner", n.ID, RoleViewer, "", "https://owner.example", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// Escalate the access level after signing — verification must fail.
	cap.Access = RoleEditor
	if err := owner.VerifyCapability(cap); err == nil {
		t.Fatal("tampered capability verified; want failure")
	}
}

func TestCapabilityExpiry(t *testing.T) {
	owner, _, _, _ := peerServices(t)
	n := seedFile(t, owner, "userOwner", "doc.txt", "data")
	cap, _, err := owner.IssueCapability("userOwner", n.ID, RoleViewer, "", "https://owner.example", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// Force expiry in the past and re-sign so only the clock is "wrong".
	cap.ExpiresAt = time.Now().Add(-time.Minute)
	msg, _ := cap.signingBytes()
	cap.Signature = base64.RawURLEncoding.EncodeToString(owner.signer.Sign(msg))
	if err := owner.VerifyCapability(cap); err == nil {
		t.Fatal("expired capability verified; want failure")
	}
}

func TestCapabilityRevoke(t *testing.T) {
	owner, recip, _, recipSigner := peerServices(t)
	n := seedFile(t, owner, "userOwner", "doc.txt", "secret bytes")
	_, link, err := owner.IssueCapability("userOwner", n.ID, RoleViewer, "", "https://owner.example", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	cap, _ := DecodeCapabilityLink(link)

	// Before revoke: a proof-bearing fetch succeeds.
	if _, _, _, err := owner.ServeCapability(context.Background(), fetchReq(cap, recipSigner)); err != nil {
		t.Fatalf("ServeCapability before revoke: %v", err)
	}
	// Revoke, then the same fetch must be refused.
	if err := owner.RevokePeerShare("userOwner", cap.ID); err != nil {
		t.Fatalf("RevokePeerShare: %v", err)
	}
	if _, _, _, err := owner.ServeCapability(context.Background(), fetchReq(cap, recipSigner)); err == nil {
		t.Fatal("ServeCapability after revoke succeeded; want failure")
	}
	// And the recipient redeem fails too.
	if _, err := recip.RedeemCapability(context.Background(), "userRecip", link); err == nil {
		t.Fatal("RedeemCapability after revoke succeeded; want failure")
	}
}

// fetchReq builds a signed PeerFetchRequest from a recipient signer.
func fetchReq(cap *Capability, signer testSigner) PeerFetchRequest {
	ts := time.Now().Unix()
	return PeerFetchRequest{
		Capability:  cap,
		RequesterID: signer.SelfID(),
		Timestamp:   ts,
		Proof:       base64.RawURLEncoding.EncodeToString(signer.Sign(fetchProofMessage(cap.ID, ts))),
	}
}

// ── recipient binding ───────────────────────────────────────────────────────

func TestCapabilityRecipientBinding(t *testing.T) {
	owner, _, _, recipSigner := peerServices(t)
	n := seedFile(t, owner, "userOwner", "doc.txt", "bound bytes")

	// Bind to the recipient's identity.
	_, link, err := owner.IssueCapability("userOwner", n.ID, RoleViewer, recipSigner.SelfID(), "https://owner.example", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	cap, _ := DecodeCapabilityLink(link)

	// The bound recipient can fetch.
	if _, _, _, err := owner.ServeCapability(context.Background(), fetchReq(cap, recipSigner)); err != nil {
		t.Fatalf("bound recipient fetch: %v", err)
	}
	// A DIFFERENT box presenting a valid proof for itself is refused.
	stranger := newTestSigner(t)
	if _, _, _, err := owner.ServeCapability(context.Background(), fetchReq(cap, stranger)); err == nil {
		t.Fatal("stranger redeemed a bound capability; want failure")
	}
}

// ── end-to-end transfer over the loopback peer ──────────────────────────────

func TestEndToEndFileTransfer(t *testing.T) {
	owner, recip, _, _ := peerServices(t)
	const body = "the quick brown fox jumps over the lazy dog"
	n := seedFile(t, owner, "userOwner", "report.txt", body)

	_, link, err := owner.IssueCapability("userOwner", n.ID, RoleViewer, "", "https://owner.example", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// Recipient redeems → bytes stream owner→recipient and stage locally.
	item, err := recip.RedeemCapability(context.Background(), "userRecip", link)
	if err != nil {
		t.Fatalf("RedeemCapability: %v", err)
	}
	if item.Size != int64(len(body)) {
		t.Fatalf("staged size = %d, want %d", item.Size, len(body))
	}

	// Preview the staged bytes.
	rc, got, err := recip.GetReceived("userRecip", item.ID)
	if err != nil {
		t.Fatalf("GetReceived: %v", err)
	}
	defer rc.Close()
	gotBytes, _ := io.ReadAll(rc)
	if string(gotBytes) != body {
		t.Fatalf("staged bytes = %q, want %q", gotBytes, body)
	}
	if got.RecipientID != "userRecip" {
		t.Fatalf("received item recipient = %q", got.RecipientID)
	}

	// Save to Drive (B→A bridge) → a real node in the recipient's Drive.
	saved, err := recip.SaveReceivedToDrive(context.Background(), "userRecip", item.ID, "", "report.txt")
	if err != nil {
		t.Fatalf("SaveReceivedToDrive: %v", err)
	}
	if saved.OwnerID != "userRecip" || saved.IsDir {
		t.Fatalf("saved node wrong: %+v", saved)
	}
	drc, _, _, err := recip.GetContent(context.Background(), "userRecip", saved.ID)
	if err != nil {
		t.Fatalf("GetContent(saved): %v", err)
	}
	defer drc.Close()
	savedBytes, _ := io.ReadAll(drc)
	if string(savedBytes) != body {
		t.Fatalf("saved Drive bytes = %q, want %q", savedBytes, body)
	}
}

// TestEndToEndOverHTTP exercises the real HTTPPeerTransport + the public serve
// handler shape (an httptest server fronting the owner's ServeCapability),
// confirming the bytes flow over an actual HTTP request/response.
func TestEndToEndOverHTTP(t *testing.T) {
	owner, recip, _, _ := peerServices(t)
	const body = "streamed over http with chunks and bytes"
	n := seedFile(t, owner, "userOwner", "http.txt", body)

	// Stand up an HTTP serve endpoint backed by the owner service.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PeerServePath {
			w.WriteHeader(404)
			return
		}
		var req PeerFetchRequest
		if err := decodeJSONForTest(r, &req); err != nil {
			w.WriteHeader(400)
			return
		}
		rc, size, _, err := owner.ServeCapability(r.Context(), req)
		if err != nil {
			w.WriteHeader(403)
			return
		}
		defer rc.Close()
		if size >= 0 {
			w.Header().Set("Content-Length", itoa(size))
		}
		io.Copy(w, rc)
	}))
	defer srv.Close()

	// Point the recipient at the real HTTP transport instead of the loopback.
	// Bypass the SSRF guard: the test server binds to 127.0.0.1, which is
	// intentionally a loopback address (a stand-in for a REMOTE owner box).
	// Production code uses validateOwnerAddr AND a safedial dial-time Control hook
	// baked into the client's transport, both of which block loopback — so this
	// E2E test must opt out of BOTH: the string validator (addrValidator) and the
	// dial-time guard (by swapping in a plain client). Both fields are unexported
	// and not settable via the public API, so this bypass is test-only.
	transport := NewHTTPPeerTransport()
	transport.addrValidator = func(string) error { return nil } // test-only bypass
	transport.http = &http.Client{}                             // test-only: plain client so loopback (stand-in remote) is dialable
	recip.WithPeer(recip.signer, transport, t.TempDir())

	cap, _, err := owner.IssueCapability("userOwner", n.ID, RoleViewer, "", srv.URL, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	link, _ := encodeCapabilityLink(cap)

	item, err := recip.RedeemCapability(context.Background(), "userRecip", link)
	if err != nil {
		t.Fatalf("RedeemCapability over http: %v", err)
	}
	rc, _, err := recip.GetReceived("userRecip", item.ID)
	if err != nil {
		t.Fatalf("GetReceived: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != body {
		t.Fatalf("http-transferred bytes = %q, want %q", got, body)
	}
}

// ── folder transfer (tar) ───────────────────────────────────────────────────

func TestEndToEndFolderTransfer(t *testing.T) {
	owner, recip, _, _ := peerServices(t)
	ctx := context.Background()

	// Build owner/dir/{a.txt, sub/b.txt}.
	dir, err := owner.CreateFolder("userOwner", "", "shared")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	mkFile(t, owner, "userOwner", dir.ID, "a.txt", "alpha")
	sub, err := owner.CreateFolder("userOwner", dir.ID, "sub")
	if err != nil {
		t.Fatalf("CreateFolder sub: %v", err)
	}
	mkFile(t, owner, "userOwner", sub.ID, "b.txt", "beta")

	_, link, err := owner.IssueCapability("userOwner", dir.ID, RoleViewer, "", "https://owner.example", time.Hour)
	if err != nil {
		t.Fatalf("issue folder cap: %v", err)
	}
	item, err := recip.RedeemCapability(ctx, "userRecip", link)
	if err != nil {
		t.Fatalf("RedeemCapability folder: %v", err)
	}
	if !item.IsDir {
		t.Fatal("received item should be a directory")
	}

	root, err := recip.SaveReceivedToDrive(ctx, "userRecip", item.ID, "", "shared")
	if err != nil {
		t.Fatalf("SaveReceivedToDrive folder: %v", err)
	}
	// Verify the recreated tree: shared/a.txt and shared/sub/b.txt.
	kids, _ := recip.listChildren(root.ID)
	names := map[string]*Node{}
	for _, k := range kids {
		names[k.Name] = k
	}
	if _, ok := names["a.txt"]; !ok {
		t.Fatalf("a.txt missing in saved folder; got %v", names)
	}
	subNode, ok := names["sub"]
	if !ok || !subNode.IsDir {
		t.Fatalf("sub folder missing; got %v", names)
	}
	subKids, _ := recip.listChildren(subNode.ID)
	if len(subKids) != 1 || subKids[0].Name != "b.txt" {
		t.Fatalf("sub/b.txt missing; got %v", subKids)
	}
	// Spot-check bytes of the nested file.
	brc, _, _, err := recip.GetContent(ctx, "userRecip", subKids[0].ID)
	if err != nil {
		t.Fatalf("GetContent nested: %v", err)
	}
	defer brc.Close()
	bb, _ := io.ReadAll(brc)
	if string(bb) != "beta" {
		t.Fatalf("nested bytes = %q, want beta", bb)
	}
}

// ── guards: non-owner cannot issue; unwired seam returns ErrPeerUnavailable ──

func TestIssueRequiresOwner(t *testing.T) {
	owner, _, _, _ := peerServices(t)
	n := seedFile(t, owner, "userOwner", "doc.txt", "x")
	if _, _, err := owner.IssueCapability("userOther", n.ID, RoleViewer, "", "https://o", time.Hour); err != ErrNoAccess {
		t.Fatalf("non-owner issue err = %v, want ErrNoAccess", err)
	}
}

func TestPeerUnavailableWithoutSeam(t *testing.T) {
	svc, _ := newTestService(t) // no WithPeer
	n := seedFile(t, svc, "userOwner", "doc.txt", "x")
	if _, _, err := svc.IssueCapability("userOwner", n.ID, RoleViewer, "", "https://o", time.Hour); err != ErrPeerUnavailable {
		t.Fatalf("issue without seam err = %v, want ErrPeerUnavailable", err)
	}
	if _, err := svc.RedeemCapability(context.Background(), "userOwner", "x"); err != ErrPeerUnavailable {
		t.Fatalf("redeem without seam err = %v, want ErrPeerUnavailable", err)
	}
}

// ── test helpers ────────────────────────────────────────────────────────────

func mkFile(t *testing.T, svc *Service, ownerID, parent, name, body string) {
	t.Helper()
	ctx := context.Background()
	n, _, err := svc.UploadGrant(ctx, ownerID, parent, name, "text/plain", ttl)
	if err != nil {
		t.Fatalf("UploadGrant(%s): %v", name, err)
	}
	if _, err := svc.PutContent(ctx, ownerID, n.ID, strings.NewReader(body), int64(len(body)), "text/plain"); err != nil {
		t.Fatalf("PutContent(%s): %v", name, err)
	}
	if _, err := svc.Commit(ownerID, n.ID, int64(len(body)), "text/plain", "etag"); err != nil {
		t.Fatalf("Commit(%s): %v", name, err)
	}
}

func decodeJSONForTest(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
