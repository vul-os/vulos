package cloudhome

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func testKEK() []byte { return make([]byte, 32) } // all-zeros is fine for tests

// ── interop fixtures ────────────────────────────────────────────────────────

// buildCapJSON builds a minimal opaque capability blob (the fields the cell
// reads) plus its base64url link, mirroring what vulos IssueCapability emits.
func buildCapJSON(t *testing.T, capID, ownerVulaID, ownerAddr, recipient string) (json.RawMessage, string) {
	t.Helper()
	cap := map[string]any{
		"id":            capID,
		"name":          "report.pdf",
		"is_dir":        false,
		"content_type":  "application/pdf",
		"owner_vula_id": ownerVulaID,
		"owner_addr":    ownerAddr,
		"recipient":     recipient,
	}
	raw, err := json.Marshal(cap)
	if err != nil {
		t.Fatal(err)
	}
	return raw, base64.RawURLEncoding.EncodeToString(raw)
}

// fakeSharer stands in for a vulos sharer box's /api/files/peer/serve. It
// verifies the recipient-signed fetch proof EXACTLY as vulos ServeCapability does
// — signer.Verify(RequesterID, fetchProofMessage(capID, ts), proof) with the
// pubkey decoded from the requester's VulaID — then serves the document bytes.
// This is the interop check: a proof the cell produces must verify for the bound
// recipient and fail for any other.
type fakeSharer struct {
	docBytes []byte
	capID    string

	lastReq    PeerFetchRequest
	verifiedAs string // RequesterID that passed verification
}

func (f *fakeSharer) Serve(_ context.Context, _ string, req PeerFetchRequest) (io.ReadCloser, error) {
	f.lastReq = req
	pub, err := ParseVulaID(req.RequesterID)
	if err != nil {
		return nil, fmt.Errorf("fakeSharer: bad requester VulaID: %w", err)
	}
	proof, err := base64.RawURLEncoding.DecodeString(req.Proof)
	if err != nil {
		return nil, fmt.Errorf("fakeSharer: bad proof b64: %w", err)
	}
	// EXACT vulos ServeCapability binding check.
	if !ed25519.Verify(pub, fetchProofMessage(f.capID, req.Timestamp), proof) {
		return nil, fmt.Errorf("fakeSharer: fetch proof failed recipient binding")
	}
	f.verifiedAs = req.RequesterID
	return io.NopCloser(bytes.NewReader(f.docBytes)), nil
}

// fakeAccounts implements AccountLookup.
type fakeAccounts map[string]string // email -> accountID

func (f fakeAccounts) LookupByEmail(_ context.Context, email string) (string, string, error) {
	id, ok := f[strings.ToLower(email)]
	if !ok {
		return "", "", ErrNoAccount
	}
	return id, "", nil
}

// fakeBox implements BoxLookup.
type fakeBox map[string][2]string // accountID -> {vulaID, server}

func (f fakeBox) BoxIdentity(_ context.Context, accountID string) (string, string, bool, error) {
	v, ok := f[accountID]
	if !ok {
		return "", "", false, nil
	}
	return v[0], v[1], true, nil
}

// capturingStager implements DriveStager.
type capturingStager struct {
	calls []struct {
		acct string
		doc  ReceivedDoc
	}
	err error
}

func (c *capturingStager) SaveReceivedToDrive(_ context.Context, accountID string, doc ReceivedDoc) error {
	if c.err != nil {
		return c.err
	}
	c.calls = append(c.calls, struct {
		acct string
		doc  ReceivedDoc
	}{accountID, doc})
	return nil
}

func newSvc(t *testing.T, store Store, box BoxLookup, stager DriveStager, puller PeerServeClient) *Service {
	t.Helper()
	svc, err := NewService(Config{
		Store:   store,
		KEK:     testKEK(),
		Server:  "https://cell.vulos.org",
		Account: fakeAccounts{"bob@vulos.org": "acct-bob", "alice@vulos.org": "acct-alice"},
		Box:     box,
		Stager:  stager,
		Puller:  puller,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func TestVulaIDRoundTrip(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	id := FormatVulaID(pub)
	if !strings.HasPrefix(id, VulaIDPrefix) {
		t.Fatalf("missing prefix: %q", id)
	}
	got, err := ParseVulaID(id)
	if err != nil {
		t.Fatalf("ParseVulaID: %v", err)
	}
	if !got.Equal(pub) {
		t.Fatal("round-trip mismatch")
	}
	if _, err := ParseVulaID("vula:rsa:abc"); err == nil {
		t.Fatal("expected error for non-ed25519 VulaID")
	}
}

func TestProvisionIsLazyAndIdempotent(t *testing.T) {
	svc := newSvc(t, NewMemStore(), nil, nil, nil)
	ctx := context.Background()

	a, err := svc.Provision(ctx, "acct-bob")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if a.VulaID == "" || !strings.HasPrefix(a.VulaID, VulaIDPrefix) {
		t.Fatalf("bad VulaID: %q", a.VulaID)
	}
	if a.Server != "https://cell.vulos.org" {
		t.Fatalf("server: %q", a.Server)
	}
	// Provision again → SAME identity (provisioned once).
	b, err := svc.Provision(ctx, "acct-bob")
	if err != nil {
		t.Fatalf("provision 2: %v", err)
	}
	if a.VulaID != b.VulaID {
		t.Fatalf("provisioned twice: %q != %q", a.VulaID, b.VulaID)
	}
}

func TestResolveAccountOnlyProvisions(t *testing.T) {
	store := NewMemStore()
	svc := newSvc(t, store, fakeBox{}, nil, nil)
	ctx := context.Background()

	res, err := svc.Resolve(ctx, "Bob@Vulos.org")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.HasPrefix(res.VulaID, VulaIDPrefix) {
		t.Fatalf("bad VulaID: %q", res.VulaID)
	}
	if res.Server != "https://cell.vulos.org" {
		t.Fatalf("server: %q", res.Server)
	}
	if res.DisplayName != "bob@vulos.org" {
		t.Fatalf("display name fallback: %q", res.DisplayName)
	}
	// Identity must now be persisted (resolvable by VulaID).
	if _, err := store.GetByVulaID(ctx, res.VulaID); err != nil {
		t.Fatalf("identity not persisted: %v", err)
	}
}

func TestResolveBoxUserSkipsCloudHome(t *testing.T) {
	store := NewMemStore()
	box := fakeBox{"acct-alice": {"vula:ed25519:BOXKEY", "https://alice-box.example"}}
	svc := newSvc(t, store, box, nil, nil)

	res, err := svc.Resolve(context.Background(), "alice@vulos.org")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.VulaID != "vula:ed25519:BOXKEY" || res.Server != "https://alice-box.example" {
		t.Fatalf("box user resolved wrong: %+v", res)
	}
	// No cloud-home identity should have been minted for a box user.
	if _, err := store.GetByAccount(context.Background(), "acct-alice"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("box user should not get a cloud-home identity, got err=%v", err)
	}
}

func TestResolveUnknownEmail(t *testing.T) {
	svc := newSvc(t, NewMemStore(), nil, nil, nil)
	if _, err := svc.Resolve(context.Background(), "nobody@vulos.org"); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("want ErrNoAccount, got %v", err)
	}
}

func TestSignAndVerify(t *testing.T) {
	store := NewMemStore()
	svc := newSvc(t, store, nil, nil, nil)
	ctx := context.Background()
	id, _ := svc.Provision(ctx, "acct-bob")

	sig, err := svc.SignForVulaID(ctx, id.VulaID, []byte("hello"))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	pub, _ := ParseVulaID(id.VulaID)
	if !ed25519.Verify(pub, []byte("hello"), sig) {
		t.Fatal("signature does not verify against the VulaID public key")
	}
	if _, err := svc.SignForVulaID(ctx, "vula:ed25519:unknown", []byte("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound for unknown VulaID, got %v", err)
	}
}

func TestRedeemCapabilityPullsStagesAndBinds(t *testing.T) {
	store := NewMemStore()
	stager := &capturingStager{}
	ctx := context.Background()

	// Provision Bob's cloud-home identity first so we know the bound recipient.
	pre := newSvc(t, store, nil, stager, nil)
	id, _ := pre.Provision(ctx, "acct-bob")

	// Wave 3: the sharer serves a CONTENT-BLIND VSEAL1 sealed envelope, not
	// plaintext. The cell can only transport it (it holds no content key).
	sealed := sealedBlobFor("recipient-content-pubkey-b64", "opaque-ciphertext-body")
	sharer := &fakeSharer{docBytes: sealed, capID: "cap-1"}
	svc := newSvc(t, store, nil, stager, sharer)

	capRaw, link := buildCapJSON(t, "cap-1", "vula:ed25519:ALICE", "https://alice-box.example", id.VulaID)
	rcpt, err := svc.RedeemCapability(ctx, InboundShare{
		Recipient:  id.VulaID,
		Link:       link,
		Capability: capRaw,
	})
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	// The sharer verified the cell's fetch proof for the BOUND recipient.
	if sharer.verifiedAs != id.VulaID {
		t.Fatalf("sharer verified proof as %q, want bound recipient %q", sharer.verifiedAs, id.VulaID)
	}
	if sharer.lastReq.RequesterID != id.VulaID {
		t.Fatalf("requester id %q, want %q", sharer.lastReq.RequesterID, id.VulaID)
	}
	// The capability was forwarded opaquely (unchanged bytes).
	if !bytes.Equal(sharer.lastReq.Capability, capRaw) {
		t.Fatalf("forwarded capability mutated:\n got %s\nwant %s", sharer.lastReq.Capability, capRaw)
	}
	// The pulled bytes were staged into the account's Drive with capability metadata.
	if len(stager.calls) != 1 || stager.calls[0].acct != "acct-bob" {
		t.Fatalf("stager not called for account: %+v", stager.calls)
	}
	doc := stager.calls[0].doc
	// The staged bytes are the SEALED envelope, verbatim — and marked content-blind.
	if !bytes.Equal(doc.Bytes, sealed) {
		t.Fatalf("staged bytes were altered by the cell")
	}
	if !doc.Sealed {
		t.Fatal("staged doc must be marked Sealed (content-blind)")
	}
	if !bytes.HasPrefix(doc.Bytes, []byte("VSEAL1\n")) {
		t.Fatal("staged doc must be a VSEAL1 envelope, never plaintext")
	}
	if doc.Name != "report.pdf" || doc.ContentType != "application/pdf" || doc.IsDir {
		t.Fatalf("staged metadata mismatch: %+v", doc)
	}
	if rcpt.DocID != "cap-1" || rcpt.AccountID != "acct-bob" {
		t.Fatalf("receipt mismatch: %+v", rcpt)
	}
}

// TestRedeemProofRejectedForWrongRecipient is the negative half of the interop
// binding check: a proof the cell signs for one cloud-home account must NOT
// verify under a different recipient's VulaID (vulos ServeCapability would
// reject it). We sign for Bob but present Carol's VulaID to the sharer's check.
func TestRedeemProofRejectedForWrongRecipient(t *testing.T) {
	store := NewMemStore()
	ctx := context.Background()
	svc := newSvc(t, store, nil, &capturingStager{}, &fakeSharer{capID: "cap-1"})
	bob, _ := svc.Provision(ctx, "acct-bob")

	// Carol is a different account / different cloud-home key.
	carolPub, _, _ := ed25519.GenerateKey(nil)
	carol := FormatVulaID(carolPub)

	// The cell signs the fetch proof with BOB's key (ts/cap from Bob's redemption).
	ts := int64(1700000000)
	rBob, _ := store.GetByVulaID(ctx, bob.VulaID)
	proof, err := svc.signRecord(ctx, rBob, fetchProofMessage("cap-1", ts), "sign")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	// Verified against Bob → passes; against Carol → must fail.
	if !ed25519.Verify(mustPub(t, bob.VulaID), fetchProofMessage("cap-1", ts), proof) {
		t.Fatal("proof should verify for the bound recipient (Bob)")
	}
	if ed25519.Verify(carolPub, fetchProofMessage("cap-1", ts), proof) {
		t.Fatal("proof MUST NOT verify for a different recipient (Carol)")
	}
	_ = carol
}

func mustPub(t *testing.T, vulaID string) ed25519.PublicKey {
	t.Helper()
	pub, err := ParseVulaID(vulaID)
	if err != nil {
		t.Fatalf("ParseVulaID: %v", err)
	}
	return pub
}

// sealedBlobFor builds a syntactically valid VSEAL1 envelope wrapped to kidB64.
// The cell only PARSES it (it holds no content key), so the body can be arbitrary
// opaque bytes — this stands in for real ciphertext produced by a client.
func sealedBlobFor(kidB64, body string) []byte {
	hdr := map[string]any{
		"v": 1, "aead": "AES-256-GCM", "kdf": "HKDF-SHA256", "kem": "X25519",
		"cnonce": base64.StdEncoding.EncodeToString(make([]byte, 12)),
		"wraps": []map[string]string{{
			"kid":    kidB64,
			"epk":    base64.StdEncoding.EncodeToString(make([]byte, 32)),
			"wnonce": base64.StdEncoding.EncodeToString(make([]byte, 12)),
			"wct":    base64.StdEncoding.EncodeToString(make([]byte, 48)),
		}},
	}
	hj, _ := json.Marshal(hdr)
	out := []byte("VSEAL1\n")
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(hj)))
	out = append(out, l[:]...)
	out = append(out, hj...)
	out = append(out, []byte(body)...)
	return out
}

// fakeContentKeys implements ContentKeyLookup.
type fakeContentKeys map[string]string // accountID -> content pubkey b64

func (f fakeContentKeys) ContentPubKey(_ context.Context, accountID string) (string, error) {
	return f[accountID], nil
}

// TestRedeemRejectsPlaintextFailClosed is THE wave-3 guarantee: if a sharer serves
// PLAINTEXT (not a VSEAL1 envelope), the cell REFUSES to stage it and never writes
// it into the recipient's Drive. The cell must only ever handle ciphertext.
func TestRedeemRejectsPlaintextFailClosed(t *testing.T) {
	store := NewMemStore()
	stager := &capturingStager{}
	ctx := context.Background()
	pre := newSvc(t, store, nil, stager, nil)
	id, _ := pre.Provision(ctx, "acct-bob")

	sharer := &fakeSharer{docBytes: []byte("plaintext the cell must not stage"), capID: "cap-1"}
	svc := newSvc(t, store, nil, stager, sharer)
	capRaw, link := buildCapJSON(t, "cap-1", "vula:ed25519:ALICE", "https://alice-box.example", id.VulaID)

	_, err := svc.RedeemCapability(ctx, InboundShare{Recipient: id.VulaID, Link: link, Capability: capRaw})
	if !errors.Is(err, ErrNotContentBlind) {
		t.Fatalf("plaintext must fail closed with ErrNotContentBlind, got %v", err)
	}
	if len(stager.calls) != 0 {
		t.Fatal("cell must NOT stage anything when the bytes are not sealed")
	}
}

// TestRedeemContentKeyMismatchFailClosed: when the recipient's published content
// key is known, a seal NOT wrapped to it is refused (the recipient could not
// decrypt it, and it may be a griefing / misaddressed share).
func TestRedeemContentKeyMismatchFailClosed(t *testing.T) {
	store := NewMemStore()
	stager := &capturingStager{}
	ctx := context.Background()
	pre := newSvc(t, store, nil, stager, nil)
	id, _ := pre.Provision(ctx, "acct-bob")

	sealedToOther := sealedBlobFor("some-other-recipients-key", "ciphertext")
	sharer := &fakeSharer{docBytes: sealedToOther, capID: "cap-1"}
	svc, err := NewService(Config{
		Store:       store,
		KEK:         testKEK(),
		Server:      "https://cell.vulos.org",
		Account:     fakeAccounts{"bob@vulos.org": "acct-bob"},
		Stager:      stager,
		Puller:      sharer,
		ContentKeys: fakeContentKeys{"acct-bob": "bobs-real-content-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	capRaw, link := buildCapJSON(t, "cap-1", "vula:ed25519:ALICE", "https://alice-box.example", id.VulaID)
	_, rerr := svc.RedeemCapability(ctx, InboundShare{Recipient: id.VulaID, Link: link, Capability: capRaw})
	if !errors.Is(rerr, ErrNotContentBlind) {
		t.Fatalf("seal not addressed to recipient must fail closed, got %v", rerr)
	}
	if len(stager.calls) != 0 {
		t.Fatal("cell must NOT stage a seal the recipient cannot open")
	}

	// Sanity: the SAME sharer serving a seal addressed to Bob's real key is accepted.
	sharer.docBytes = sealedBlobFor("bobs-real-content-key", "ciphertext")
	if _, err := svc.RedeemCapability(ctx, InboundShare{Recipient: id.VulaID, Link: link, Capability: capRaw}); err != nil {
		t.Fatalf("seal addressed to recipient should be accepted, got %v", err)
	}
	if len(stager.calls) != 1 {
		t.Fatalf("expected exactly one staged doc, got %d", len(stager.calls))
	}
}

// erroringContentKeys implements ContentKeyLookup and always fails the lookup — it
// stands in for an unreachable content-key endpoint.
type erroringContentKeys struct{}

func (erroringContentKeys) ContentPubKey(_ context.Context, _ string) (string, error) {
	return "", errors.New("content-key endpoint unreachable")
}

// TestRedeemContentKeyUnreachableFailsClosed (WAVE-7 F2): when the content-key
// lookup is wired but UNREACHABLE, redemption must FAIL CLOSED — the cell must not
// fall open and accept a possibly-untargeted seal.
func TestRedeemContentKeyUnreachableFailsClosed(t *testing.T) {
	store := NewMemStore()
	stager := &capturingStager{}
	ctx := context.Background()
	pre := newSvc(t, store, nil, stager, nil)
	id, _ := pre.Provision(ctx, "acct-bob")

	sealed := sealedBlobFor("recipient-content-pubkey-b64", "ciphertext")
	sharer := &fakeSharer{docBytes: sealed, capID: "cap-1"}
	svc, err := NewService(Config{
		Store:       store,
		KEK:         testKEK(),
		Server:      "https://cell.vulos.org",
		Account:     fakeAccounts{"bob@vulos.org": "acct-bob"},
		Stager:      stager,
		Puller:      sharer,
		ContentKeys: erroringContentKeys{},
	})
	if err != nil {
		t.Fatal(err)
	}
	capRaw, link := buildCapJSON(t, "cap-1", "vula:ed25519:ALICE", "https://alice-box.example", id.VulaID)
	_, rerr := svc.RedeemCapability(ctx, InboundShare{Recipient: id.VulaID, Link: link, Capability: capRaw})
	if !errors.Is(rerr, ErrNotContentBlind) {
		t.Fatalf("unreachable content-key lookup must fail closed, got %v", rerr)
	}
	if len(stager.calls) != 0 {
		t.Fatal("cell must NOT stage when the content-key lookup is unreachable")
	}
}

// TestRedeemNoPublishedContentKeyFailsClosed (WAVE-7 F2): a wired lookup that
// resolves to NO published key (empty) also fails closed — a content-blind share
// requires the recipient's published key, so an empty result is anomalous.
func TestRedeemNoPublishedContentKeyFailsClosed(t *testing.T) {
	store := NewMemStore()
	stager := &capturingStager{}
	ctx := context.Background()
	pre := newSvc(t, store, nil, stager, nil)
	id, _ := pre.Provision(ctx, "acct-bob")

	sealed := sealedBlobFor("recipient-content-pubkey-b64", "ciphertext")
	sharer := &fakeSharer{docBytes: sealed, capID: "cap-1"}
	svc, err := NewService(Config{
		Store:       store,
		KEK:         testKEK(),
		Server:      "https://cell.vulos.org",
		Account:     fakeAccounts{"bob@vulos.org": "acct-bob"},
		Stager:      stager,
		Puller:      sharer,
		ContentKeys: fakeContentKeys{}, // acct-bob absent => "" (no published key)
	})
	if err != nil {
		t.Fatal(err)
	}
	capRaw, link := buildCapJSON(t, "cap-1", "vula:ed25519:ALICE", "https://alice-box.example", id.VulaID)
	_, rerr := svc.RedeemCapability(ctx, InboundShare{Recipient: id.VulaID, Link: link, Capability: capRaw})
	if !errors.Is(rerr, ErrNotContentBlind) {
		t.Fatalf("empty content key must fail closed, got %v", rerr)
	}
	if len(stager.calls) != 0 {
		t.Fatal("cell must NOT stage when the recipient has no published content key")
	}
}

func TestRedeemUnknownRecipientFailsClosed(t *testing.T) {
	svc := newSvc(t, NewMemStore(), nil, &capturingStager{}, &fakeSharer{capID: "cap-1"})
	_, capLink := buildCapJSON(t, "cap-1", "vula:ed25519:ALICE", "https://alice-box.example", "vula:ed25519:NOTOURS")
	_, err := svc.RedeemCapability(context.Background(), InboundShare{
		Recipient: "vula:ed25519:NOTOURS",
		Link:      capLink,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound (fail closed), got %v", err)
	}
}

func TestRedeemNoStagerFailsClosed(t *testing.T) {
	store := NewMemStore()
	svc := newSvc(t, store, nil, nil, &fakeSharer{capID: "cap-1"}) // no stager
	ctx := context.Background()
	id, _ := svc.Provision(ctx, "acct-bob")
	capRaw, _ := buildCapJSON(t, "cap-1", "vula:ed25519:ALICE", "https://alice-box.example", id.VulaID)
	_, err := svc.RedeemCapability(ctx, InboundShare{Recipient: id.VulaID, Capability: capRaw})
	if err == nil || !strings.Contains(err.Error(), "no Drive stager") {
		t.Fatalf("expected fail-closed without stager, got %v", err)
	}
}

func TestRedeemNoPullerFailsClosed(t *testing.T) {
	store := NewMemStore()
	svc := newSvc(t, store, nil, &capturingStager{}, nil) // no puller
	ctx := context.Background()
	id, _ := svc.Provision(ctx, "acct-bob")
	capRaw, _ := buildCapJSON(t, "cap-1", "vula:ed25519:ALICE", "https://alice-box.example", id.VulaID)
	_, err := svc.RedeemCapability(ctx, InboundShare{Recipient: id.VulaID, Capability: capRaw})
	if err == nil || !strings.Contains(err.Error(), "no peer-serve client") {
		t.Fatalf("expected fail-closed without puller, got %v", err)
	}
}

// TestVulaIDMatchesVulosVector pins the base58 VulaID encoding to be
// BYTE-IDENTICAL to what vulos's base58Encode/encodeVulaID produce for a fixed
// 32-byte key. The expected strings were computed directly from vulos
// backend/services/peering/identity.go's base58Encode. If this fails, a
// cloud-home VulaID is unparseable by vulos peers and no peer resolves.
func TestVulaIDMatchesVulosVector(t *testing.T) {
	// Key = bytes 0x00..0x1f.
	k1 := make([]byte, 32)
	for i := range k1 {
		k1[i] = byte(i)
	}
	const want1 = "vula:ed25519:1thX6LZfHDZZKUs92febYZhYRcXddmzfzF2NvTkPNE"
	if got := FormatVulaID(k1); got != want1 {
		t.Fatalf("VulaID(0x00..1f) = %q, want %q (must byte-match vulos)", got, want1)
	}

	// Key = 32 × 0xAA.
	k2 := bytes.Repeat([]byte{0xAA}, 32)
	const want2 = "vula:ed25519:CVDFLCAjXhVWiPXH9nTCTpCgVzmDVoiPzNJYuccr1dqB"
	if got := FormatVulaID(k2); got != want2 {
		t.Fatalf("VulaID(0xAA*32) = %q, want %q (must byte-match vulos)", got, want2)
	}

	// Round-trips back to the exact key.
	back, err := ParseVulaID(want1)
	if err != nil {
		t.Fatalf("ParseVulaID: %v", err)
	}
	if !bytes.Equal(back, k1) {
		t.Fatalf("ParseVulaID round-trip mismatch: %x", back)
	}
}

func TestLoadKEKFailsClosedInProd(t *testing.T) {
	t.Setenv("CLOUDHOME_KEK", "")
	t.Setenv("VULOS_DEV", "")
	if _, err := LoadKEK(); err == nil {
		t.Fatal("LoadKEK must fail closed when CLOUDHOME_KEK unset and not dev")
	}
	// WAVE37: dev fallback now requires BOTH VULOS_DEV truthy AND an EXPLICIT
	// non-prod env. Explicit local → fallback works.
	t.Setenv("VULOS_DEV", "true")
	t.Setenv("VULOS_ENV", "local")
	if _, err := LoadKEK(); err != nil {
		t.Fatalf("dev fallback should work with VULOS_ENV=local: %v", err)
	}
	// WAVE37 fail-safe: dev flag but UNSET env → refused (unset resolves to prod).
	t.Setenv("VULOS_ENV", "")
	if _, err := LoadKEK(); err == nil {
		t.Fatal("LoadKEK must fail closed when VULOS_ENV is unset — an unconfigured box resolves to prod")
	}
	// A stray VULOS_DEV on a prod box must NOT activate the zero KEK.
	t.Setenv("VULOS_ENV", "prod")
	if _, err := LoadKEK(); err == nil {
		t.Fatal("LoadKEK must fail closed when VULOS_ENV=prod even with VULOS_DEV=true")
	}
}

func TestSQLStoreRoundTrip(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	store, err := OpenSQLStore(db)
	if err != nil {
		t.Fatalf("OpenSQLStore: %v", err)
	}
	svc, err := NewService(Config{
		Store: store, KEK: testKEK(), Server: "https://cell.vulos.org",
		Account: fakeAccounts{"bob@vulos.org": "acct-bob"},
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	ctx := context.Background()
	id, err := svc.Provision(ctx, "acct-bob")
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	got, err := store.GetByVulaID(ctx, id.VulaID)
	if err != nil {
		t.Fatalf("GetByVulaID: %v", err)
	}
	if got.AccountID != "acct-bob" {
		t.Fatalf("account mismatch: %q", got.AccountID)
	}
	if strings.Contains(got.EncPrivKey, "vula") || len(got.EncPrivKey) == 0 {
		t.Fatal("private key should be stored as opaque ciphertext")
	}
	// Re-provision returns the same identity (idempotent, unique constraint).
	again, err := svc.Provision(ctx, "acct-bob")
	if err != nil {
		t.Fatalf("re-provision: %v", err)
	}
	if again.VulaID != id.VulaID {
		t.Fatal("re-provision minted a new identity")
	}
}
