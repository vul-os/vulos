package files

// peershare.go — FILES PHASE 2B: OS Peer-Share (Mechanism B, bucket-less).
//
// The always-works base layer: a user who hosts ONLY the OS (no bucket, no
// cloud) can share a file/folder with another user's OS box, box-to-box, over
// the existing peering transport. Where Mechanism A (Drive) mints object-scoped
// storage grants against a shared bucket, Mechanism B issues a self-contained
// CAPABILITY that the recipient's box redeems by streaming the bytes directly
// from the owner's box.
//
// # Capability model
//
// The owner's OS signs a Capability with its box identity (the peering Ed25519
// key, surfaced here through the PeerSigner seam). A Capability is:
//
//   - SCOPED   — names exactly one node and one access level (viewer|editor);
//                it grants nothing the owner did not put in it.
//   - EXPIRING — carries an absolute ExpiresAt; verification fails past it.
//   - BOUND or OPEN — Recipient set ⇒ only that peer's box may redeem it;
//                Recipient empty ⇒ anyone-with-the-capability-link may redeem.
//   - REVOCABLE — the owner keeps a files_peer_shares row per capability; a
//                revoked row makes the owner refuse to serve the bytes even
//                though the (still validly signed) token is out in the world.
//   - VERIFIED  — the recipient verifies the signature against the owner's Vula
//                ID BEFORE contacting anyone; the owner re-verifies on serve and
//                additionally enforces the recipient binding via a signed fetch
//                proof, so a leaked bound token cannot be redeemed by a third
//                box.
//
// The capability LINK is just the base64url(JSON) of the signed capability —
// fully self-describing, paste-anywhere. The owner address it carries is where
// the recipient streams the bytes from.
//
// # Transport
//
// Byte transfer rides a PeerTransport (implemented over the peering package's
// box-to-box HTTP in production; a loopback fake in tests). It is a streaming
// PULL: the recipient proves identity with a signed fetch proof and the owner
// streams the file bytes (or, for a folder, a tar archive) chunked over the
// response body — tolerating large files without buffering. No central bucket
// is ever involved; the owner reads the bytes through its own (possibly
// local-FS / standalone) storage broker.
//
// # B→A bridge
//
// Redeemed bytes are STAGED on the recipient's local disk. "Save to Drive"
// promotes a staged item into the recipient's own Drive (Mechanism A), the
// bridge from the bucket-less peer-share world into structured Drive storage.

import (
	"archive/tar"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vulos/backend/internal/ulid"
)

// ErrCapability is returned when a capability is malformed, expired, revoked,
// fails signature verification, or is presented by the wrong recipient. It is
// deliberately coarse so the redeem/serve paths never leak WHY a capability was
// refused (anti-enumeration / anti-oracle).
var ErrCapability = errors.New("files: invalid or expired capability")

// ErrPeerUnavailable is returned when the peer-share seam (signer/transport) is
// not wired — e.g. a build with no peering identity. Callers map it to 503.
var ErrPeerUnavailable = errors.New("files: peer-share unavailable")

const (
	// maxCapTTL caps how long a peer-share capability may live.
	maxCapTTL = 7 * 24 * time.Hour
	// defaultCapTTL is used when an issue request omits a TTL.
	defaultCapTTL = 24 * time.Hour
	// fetchProofSkew bounds clock skew / replay window on a fetch proof.
	fetchProofSkew = 5 * time.Minute
)

// ── seams ─────────────────────────────────────────────────────────────────

// PeerSigner abstracts THIS box's identity for capability signing/verification.
// In production it is backed by the peering service's Ed25519 box key and Vula
// ID; tests supply a trivial Ed25519 implementation. Keeping it an interface
// keeps the files package free of a peering import (and lets the capability
// crypto be tested in isolation).
type PeerSigner interface {
	// SelfID returns this box's identity (a Vula ID).
	SelfID() string
	// Sign returns an Ed25519 signature by this box's key over msg.
	Sign(msg []byte) []byte
	// Verify reports whether sig is a valid signature by vulaID over msg.
	Verify(vulaID string, msg, sig []byte) error
}

// PeerTransport streams the bytes for a capability from a remote owner box. The
// production implementation (HTTPPeerTransport) POSTs the signed fetch request
// to <ownerAddr>/api/files/peer/serve and returns the streaming response body;
// tests can substitute an in-process loopback transport.
type PeerTransport interface {
	Fetch(ctx context.Context, ownerAddr string, req PeerFetchRequest) (io.ReadCloser, int64, error)
}

// WithPeer wires the peer-share seam onto the Service. signer and transport may
// be nil, in which case the peer-share methods return ErrPeerUnavailable.
// stagingDir is where redeemed bytes are staged on local disk before they are
// promoted into the recipient's Drive; it is created on first use.
func (s *Service) WithPeer(signer PeerSigner, transport PeerTransport, stagingDir string) *Service {
	s.signer = signer
	s.transport = transport
	s.stagingDir = stagingDir
	return s
}

// ── types ───────────────────────────────────────────────────────────────────

// Capability is the signed, scoped, expiring grant the owner box issues over a
// node. Its canonical JSON (signature field excluded) is what gets signed.
type Capability struct {
	ID          string    `json:"id"`
	NodeID      string    `json:"node_id"`
	Name        string    `json:"name"`
	IsDir       bool      `json:"is_dir"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type,omitempty"`
	Access      Role      `json:"access"`
	OwnerVulaID string    `json:"owner_vula_id"`
	OwnerAddr   string    `json:"owner_addr"`
	Recipient   string    `json:"recipient,omitempty"`
	IssuedAt    time.Time `json:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	// Sealed marks a WAVE-3 CONTENT-BLIND capability: the served bytes are a VSEAL1
	// sealed envelope (see contentseal.go) staged as an opaque artifact at issue
	// time, NOT the live plaintext node content. Used when sharing to an
	// account-only (cloud) recipient so the relaying cell only ever sees ciphertext.
	// Part of the signed canonical bytes (older unsealed caps omit it).
	Sealed bool `json:"sealed,omitempty"`
	// Signature is excluded from the signed canonical bytes (see signingBytes).
	Signature string `json:"signature,omitempty"`
}

// PeerShare is the owner-side record of an issued capability (for revoke/audit).
type PeerShare struct {
	ID        string    `json:"id"`
	NodeID    string    `json:"node_id"`
	OwnerID   string    `json:"owner_id"`
	Access    Role      `json:"access"`
	Recipient string    `json:"recipient,omitempty"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Revoked   bool      `json:"revoked"`
}

// ReceivedItem is the recipient-side record of a redeemed capability.
type ReceivedItem struct {
	ID          string    `json:"id"`
	RecipientID string    `json:"recipient_id"`
	CapID       string    `json:"cap_id"`
	Name        string    `json:"name"`
	IsDir       bool      `json:"is_dir"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type,omitempty"`
	OwnerVulaID string    `json:"owner_vula_id"`
	SavedNodeID string    `json:"saved_node_id,omitempty"`
	ReceivedAt  time.Time `json:"received_at"`

	stagingPath string // not serialized
}

// PeerFetchRequest is what a recipient box presents to an owner box to redeem a
// capability: the full signed token plus a fresh, recipient-signed proof that
// binds the requester identity to this fetch (anti-replay + recipient binding).
type PeerFetchRequest struct {
	Capability  *Capability `json:"capability"`
	RequesterID string      `json:"requester_id"`
	Timestamp   int64       `json:"timestamp"`
	Proof       string      `json:"proof"` // base64url Ed25519 sig over fetchProofMessage
}

// ── capability crypto ─────────────────────────────────────────────────────

// signingBytes returns the deterministic bytes signed/verified for a capability
// (the canonical JSON with the signature field excluded). Go marshals struct
// fields in declaration order, so this is stable across processes.
func (c *Capability) signingBytes() ([]byte, error) {
	clone := *c
	clone.Signature = ""
	return json.Marshal(&clone)
}

// fetchProofMessage is the message a recipient signs to prove it is initiating a
// specific capability fetch at a specific time.
func fetchProofMessage(capID string, ts int64) []byte {
	return []byte(fmt.Sprintf("files-peer-fetch:%s:%d", capID, ts))
}

// encodeCapabilityLink renders a signed capability as a paste-anywhere link
// token: base64url(JSON). The Drive UI prefixes it with a route for redemption.
func encodeCapabilityLink(c *Capability) (string, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeCapabilityLink parses a capability link token back into a Capability. It
// does NOT verify the signature — call VerifyCapability for that.
func DecodeCapabilityLink(link string) (*Capability, error) {
	link = strings.TrimSpace(link)
	// Tolerate a full URL ("…/redeem#<token>" or "?cap=<token>") by taking the
	// last path/query/fragment segment that looks like the token.
	if i := strings.LastIndexAny(link, "#=/"); i >= 0 && i < len(link)-1 {
		link = link[i+1:]
	}
	raw, err := base64.RawURLEncoding.DecodeString(link)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed link", ErrCapability)
	}
	var c Capability
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("%w: malformed capability", ErrCapability)
	}
	return &c, nil
}

// VerifyCapability checks a capability's structure, signature (against the
// owner's Vula ID), and expiry. It does NOT consult the revocation DB — that is
// the owner box's job on serve. Safe to call on the recipient side before
// contacting anyone.
func (s *Service) VerifyCapability(c *Capability) error {
	if s.signer == nil {
		return ErrPeerUnavailable
	}
	if c == nil || c.ID == "" || c.NodeID == "" || c.OwnerVulaID == "" || c.Signature == "" {
		return ErrCapability
	}
	if !validShareRole(c.Access) {
		return ErrCapability
	}
	if time.Now().After(c.ExpiresAt) {
		return ErrCapability
	}
	msg, err := c.signingBytes()
	if err != nil {
		return ErrCapability
	}
	sig, err := base64.RawURLEncoding.DecodeString(c.Signature)
	if err != nil {
		return ErrCapability
	}
	if err := s.signer.Verify(c.OwnerVulaID, msg, sig); err != nil {
		return ErrCapability
	}
	return nil
}

// ── owner side: issue / revoke / list ──────────────────────────────────────

// IssueCapability mints a signed peer-share capability over nodeID. Only the
// node's owner may issue. access must be editor or viewer. recipient is the
// bound peer's Vula ID, or "" for an anyone-with-the-link capability. ownerAddr
// is THIS box's reachable base URL (derived from the issuing request) and is
// embedded so the recipient knows where to stream from. ttl is clamped to
// (0, maxCapTTL]; 0 uses defaultCapTTL.
//
// Returns the capability and its paste-anywhere link token.
func (s *Service) IssueCapability(actorID, nodeID string, access Role, recipient, ownerAddr string, ttl time.Duration) (*Capability, string, error) {
	if s.signer == nil {
		return nil, "", ErrPeerUnavailable
	}
	if !validShareRole(access) {
		return nil, "", fmt.Errorf("%w: access must be editor or viewer", ErrInvalid)
	}
	n, err := s.getNode(nodeID)
	if err != nil {
		return nil, "", err
	}
	if !s.authorize(actorID, n, RoleOwner) {
		return nil, "", ErrForbidden
	}
	if ownerAddr == "" {
		return nil, "", fmt.Errorf("%w: owner address required", ErrInvalid)
	}
	if ttl <= 0 {
		ttl = defaultCapTTL
	}
	if ttl > maxCapTTL {
		ttl = maxCapTTL
	}
	now := time.Now()
	cap := &Capability{
		ID:          ulid.NewULID(),
		NodeID:      n.ID,
		Name:        n.Name,
		IsDir:       n.IsDir,
		Size:        n.Size,
		ContentType: n.ContentType,
		Access:      access,
		OwnerVulaID: s.signer.SelfID(),
		OwnerAddr:   strings.TrimRight(ownerAddr, "/"),
		Recipient:   recipient,
		IssuedAt:    now.UTC(),
		ExpiresAt:   now.Add(ttl).UTC(),
	}
	msg, err := cap.signingBytes()
	if err != nil {
		return nil, "", fmt.Errorf("files: sign capability: %w", err)
	}
	cap.Signature = base64.RawURLEncoding.EncodeToString(s.signer.Sign(msg))

	ps := PeerShare{
		ID:        cap.ID,
		NodeID:    n.ID,
		OwnerID:   n.OwnerID,
		Access:    access,
		Recipient: recipient,
		CreatedBy: actorID,
		CreatedAt: now,
		ExpiresAt: cap.ExpiresAt,
	}
	if err := s.insertPeerShare(ps); err != nil {
		return nil, "", err
	}
	link, err := encodeCapabilityLink(cap)
	if err != nil {
		return nil, "", err
	}
	s.audit(actorID, "peer.issue", n.ID, string(access)+" recipient="+orAny(recipient))
	return cap, link, nil
}

// RevokePeerShare revokes a previously issued capability. Owner only. After this
// the owner box refuses to serve the bytes even for an unexpired signed token.
func (s *Service) RevokePeerShare(actorID, capID string) error {
	ps, err := s.getPeerShare(capID)
	if err != nil {
		return err
	}
	n, err := s.getNode(ps.NodeID)
	if err == nil {
		if !s.authorize(actorID, n, RoleOwner) {
			return ErrForbidden
		}
	} else if ps.OwnerID != actorID {
		// Node gone but the issuer can still revoke their own capability.
		return ErrForbidden
	}
	if err := s.revokePeerShare(capID); err != nil {
		return err
	}
	// Best-effort: drop any staged sealed artifact for this capability.
	s.removeSealedArtifact(capID)
	s.audit(actorID, "peer.revoke", ps.NodeID, capID)
	return nil
}

// ListPeerShares returns the peer-share capabilities issued on nodeID. Owner only.
func (s *Service) ListPeerShares(actorID, nodeID string) ([]PeerShare, error) {
	n, err := s.getNode(nodeID)
	if err != nil {
		return nil, err
	}
	if !s.authorize(actorID, n, RoleOwner) {
		return nil, ErrForbidden
	}
	return s.listPeerShares(nodeID)
}

// ServeCapability is the owner-side fetch handler core: it fully re-verifies a
// fetch request and, if authorized, returns a streaming reader over the bytes.
// This runs UNAUTHENTICATED (no OS session) — the capability + fetch proof ARE
// the authentication. It enforces, in order: capability signature + expiry; a
// valid, fresh, recipient-signed fetch proof; that the capability is recorded
// and NOT revoked here; recipient binding; and that the node still exists and is
// still owned by the issuer. Any failure returns ErrCapability with no detail.
//
// The caller MUST Close the returned reader. For a folder the reader yields a
// tar archive of the subtree.
func (s *Service) ServeCapability(ctx context.Context, req PeerFetchRequest) (io.ReadCloser, int64, *Capability, error) {
	if s.signer == nil || s.broker == nil {
		return nil, 0, nil, ErrPeerUnavailable
	}
	c := req.Capability
	if err := s.VerifyCapability(c); err != nil {
		return nil, 0, nil, ErrCapability
	}
	// The token must have been signed by THIS box (we are the owner serving it).
	if c.OwnerVulaID != s.signer.SelfID() {
		return nil, 0, nil, ErrCapability
	}
	// Fresh, recipient-signed proof — binds the requester identity to this fetch.
	if req.RequesterID == "" || req.Proof == "" {
		return nil, 0, nil, ErrCapability
	}
	if d := time.Since(time.Unix(req.Timestamp, 0)); d < -fetchProofSkew || d > fetchProofSkew {
		return nil, 0, nil, ErrCapability
	}
	proof, err := base64.RawURLEncoding.DecodeString(req.Proof)
	if err != nil {
		return nil, 0, nil, ErrCapability
	}
	if err := s.signer.Verify(req.RequesterID, fetchProofMessage(c.ID, req.Timestamp), proof); err != nil {
		return nil, 0, nil, ErrCapability
	}
	// Authoritative server-side state: the capability must be recorded here and
	// not revoked, and its scope must match the (signed) token.
	ps, err := s.getPeerShare(c.ID)
	if err != nil || ps.Revoked || ps.NodeID != c.NodeID || ps.Access != c.Access {
		return nil, 0, nil, ErrCapability
	}
	// Recipient binding: a bound capability may only be redeemed by that peer.
	if ps.Recipient != "" && ps.Recipient != req.RequesterID {
		return nil, 0, nil, ErrCapability
	}
	n, err := s.getNode(c.NodeID)
	if err != nil || n.OwnerID != ps.OwnerID {
		return nil, 0, nil, ErrCapability
	}
	s.audit(req.RequesterID, "peer.serve", n.ID, c.ID)
	// WAVE-3 sealed capability: serve the pre-sealed ciphertext artifact (staged at
	// issue time), never the live plaintext node. The recipient decrypts client-side.
	if c.Sealed {
		rc, size, err := s.openSealedArtifact(c.ID)
		if err != nil {
			return nil, 0, nil, ErrCapability
		}
		return rc, size, c, nil
	}
	if n.IsDir {
		rc := s.streamFolderTar(ctx, n)
		return rc, -1, c, nil
	}
	rc, size, err := s.broker.GetContent(ctx, n.OwnerID, n.Bucket, n.ObjectKey)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("files: read object: %w", err)
	}
	return rc, size, c, nil
}

// FolderTar streams a tar archive of the folder subtree rooted at nodeID for an
// AUTHORIZED actor. It is the owner-side source the sharer's CLIENT uses to obtain
// a folder's bytes so it can SEAL them (VSEAL1) for a content-blind remote share:
// the client fetches this tar, packs it with metadata (is_dir=true), seals it to
// the recipient's content key, and posts the ciphertext to issue-sealed. The cell
// therefore never sees the tar plaintext. The caller MUST Close the reader.
//
// Fail-closed: the actor must have at least viewer access to the node, and the node
// must be a directory (a file has no tar form here — use downloadNodeBytes).
func (s *Service) FolderTar(ctx context.Context, actorID, nodeID string) (io.ReadCloser, error) {
	if s.broker == nil {
		return nil, ErrPeerUnavailable
	}
	n, err := s.getNode(nodeID)
	if err != nil {
		return nil, err
	}
	if !s.authorize(actorID, n, RoleViewer) {
		return nil, ErrForbidden
	}
	if !n.IsDir {
		return nil, fmt.Errorf("%w: node is not a folder", ErrInvalid)
	}
	return s.streamFolderTar(ctx, n), nil
}

// streamFolderTar streams a tar archive of the folder subtree rooted at n,
// reading each file's bytes through the broker. It uses an io.Pipe so nothing is
// buffered in full — large trees stream chunked. Read errors abort the stream
// (the reader observes them via CloseWithError).
func (s *Service) streamFolderTar(ctx context.Context, root *Node) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		err := s.tarWalk(ctx, tw, root, "")
		if err == nil {
			err = tw.Close()
		} else {
			_ = tw.Close()
		}
		_ = pw.CloseWithError(err)
	}()
	return pr
}

// tarWalk writes n (and, for a folder, its descendants) into tw under relPath.
func (s *Service) tarWalk(ctx context.Context, tw *tar.Writer, n *Node, relPath string) error {
	name := n.Name
	if relPath != "" {
		name = relPath + "/" + n.Name
	}
	if n.IsDir {
		if err := tw.WriteHeader(&tar.Header{Name: name + "/", Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
			return err
		}
		kids, err := s.listChildren(n.ID)
		if err != nil {
			return err
		}
		for _, k := range kids {
			if err := s.tarWalk(ctx, tw, k, name); err != nil {
				return err
			}
		}
		return nil
	}
	if n.ObjectKey == "" { // pending / never-uploaded file
		return tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 0, Typeflag: tar.TypeReg})
	}
	rc, size, err := s.broker.GetContent(ctx, n.OwnerID, n.Bucket, n.ObjectKey)
	if err != nil {
		return err
	}
	defer rc.Close()
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: size, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err = io.Copy(tw, rc)
	return err
}

// ── recipient side: redeem / list / preview / save ─────────────────────────

// RedeemCapability redeems a capability link on behalf of recipientUserID:
// verify → recipient binding → stream the bytes from the owner box over the
// transport → stage them on local disk → record a ReceivedItem. It does NOT
// write into the recipient's Drive; "Save to Drive" (SaveReceivedToDrive) is the
// explicit promotion step.
func (s *Service) RedeemCapability(ctx context.Context, recipientUserID, link string) (*ReceivedItem, error) {
	if s.signer == nil || s.transport == nil {
		return nil, ErrPeerUnavailable
	}
	c, err := DecodeCapabilityLink(link)
	if err != nil {
		return nil, err
	}
	if err := s.VerifyCapability(c); err != nil {
		return nil, err
	}
	// Recipient binding: a bound capability must name THIS box.
	if c.Recipient != "" && c.Recipient != s.signer.SelfID() {
		return nil, ErrCapability
	}

	ts := time.Now().Unix()
	proof := s.signer.Sign(fetchProofMessage(c.ID, ts))
	freq := PeerFetchRequest{
		Capability:  c,
		RequesterID: s.signer.SelfID(),
		Timestamp:   ts,
		Proof:       base64.RawURLEncoding.EncodeToString(proof),
	}
	rc, _, err := s.transport.Fetch(ctx, c.OwnerAddr, freq)
	if err != nil {
		return nil, fmt.Errorf("files: peer fetch: %w", err)
	}
	defer rc.Close()

	recvID := ulid.NewULID()
	stagePath, n, err := s.stageBytes(recvID, rc)
	if err != nil {
		return nil, err
	}
	item := ReceivedItem{
		ID:          recvID,
		RecipientID: recipientUserID,
		CapID:       c.ID,
		Name:        c.Name,
		IsDir:       c.IsDir,
		Size:        n,
		ContentType: c.ContentType,
		OwnerVulaID: c.OwnerVulaID,
		ReceivedAt:  time.Now(),
		stagingPath: stagePath,
	}
	if err := s.insertReceived(item); err != nil {
		_ = os.Remove(stagePath)
		return nil, err
	}
	s.audit(recipientUserID, "peer.redeem", "", c.ID+" from="+c.OwnerVulaID)
	return &item, nil
}

// stageBytes writes a redeemed stream to a file under the staging dir and
// returns the path and the number of bytes written.
func (s *Service) stageBytes(recvID string, r io.Reader) (string, int64, error) {
	dir := s.stagingDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "vulos-peer-received")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, fmt.Errorf("files: staging dir: %w", err)
	}
	path := filepath.Join(dir, recvID)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", 0, fmt.Errorf("files: stage create: %w", err)
	}
	n, err := io.Copy(f, r)
	cerr := f.Close()
	if err != nil {
		_ = os.Remove(path)
		return "", 0, fmt.Errorf("files: stage write: %w", err)
	}
	if cerr != nil {
		_ = os.Remove(path)
		return "", 0, cerr
	}
	return path, n, nil
}

// ListReceived returns the items recipientUserID has redeemed (newest first).
func (s *Service) ListReceived(recipientUserID string) ([]ReceivedItem, error) {
	return s.listReceived(recipientUserID)
}

// GetReceived opens the staged bytes of a received item for preview/download.
// For a folder the bytes are the tar archive. The caller MUST Close the reader.
func (s *Service) GetReceived(recipientUserID, recvID string) (io.ReadCloser, *ReceivedItem, error) {
	item, err := s.getReceived(recvID)
	if err != nil {
		return nil, nil, err
	}
	if item.RecipientID != recipientUserID {
		return nil, nil, ErrForbidden
	}
	f, err := os.Open(item.stagingPath)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	return f, item, nil
}

// SaveReceivedToDrive promotes a redeemed item into recipientUserID's own Drive
// (the B→A bridge): a file becomes a Drive node under parentID; a folder is
// extracted from its staged tar into a new Drive subtree. Returns the created
// root node. Requires the recipient's Drive broker (local-FS in standalone is
// fine — no bucket needed).
func (s *Service) SaveReceivedToDrive(ctx context.Context, recipientUserID, recvID, parentID, name string) (*Node, error) {
	if s.broker == nil {
		return nil, ErrPeerUnavailable
	}
	item, err := s.getReceived(recvID)
	if err != nil {
		return nil, err
	}
	if item.RecipientID != recipientUserID {
		return nil, ErrForbidden
	}
	if name == "" {
		name = item.Name
	}
	var root *Node
	if item.IsDir {
		root, err = s.extractTarToDrive(ctx, recipientUserID, parentID, name, item.stagingPath)
	} else {
		root, err = s.saveFileToDrive(ctx, recipientUserID, parentID, name, item.ContentType, item.stagingPath)
	}
	if err != nil {
		return nil, err
	}
	if err := s.setReceivedSaved(recvID, root.ID); err != nil {
		return nil, err
	}
	s.audit(recipientUserID, "peer.save", root.ID, item.CapID)
	return root, nil
}

// SaveBytesToDrive writes already-redeemed document bytes DIRECTLY into
// recipientUserID's own Drive, without first creating a staged ReceivedItem. It
// is the entry point of the B→A bridge for a recipient whose redemption happened
// OFF-BOX: the Vulos cell pulls a capability's bytes on an account's behalf (it
// custodies the account's cloud-home key and signs the fetch proof) and then
// hands the bytes here so they land in the account's Drive on the cell's own
// Files service. A file (isDir=false) becomes one Drive node under parentID; a
// folder (isDir=true) expects r to be the same tar archive ServeCapability
// streams for a directory and is extracted into a new Drive subtree. parentID ""
// means the recipient's Drive root. Returns the created root node.
//
// This mirrors SaveReceivedToDrive's bytes→Drive promotion but sources the bytes
// from a reader instead of a previously-staged ReceivedItem, so the cell never
// needs a local RedeemCapability round-trip.
func (s *Service) SaveBytesToDrive(ctx context.Context, recipientUserID, parentID, name, contentType string, isDir bool, r io.Reader) (*Node, error) {
	if s.broker == nil {
		return nil, ErrPeerUnavailable
	}
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("%w: name required", ErrInvalid)
	}
	// Stage the reader to a temp file so we can reuse the same Drive-promotion
	// helpers as SaveReceivedToDrive (they read from a path).
	tmpID := ulid.NewULID()
	stagePath, _, err := s.stageBytes(tmpID, r)
	if err != nil {
		return nil, err
	}
	defer os.Remove(stagePath)
	if isDir {
		return s.extractTarToDrive(ctx, recipientUserID, parentID, name, stagePath)
	}
	return s.saveFileToDrive(ctx, recipientUserID, parentID, name, contentType, stagePath)
}

// saveFileToDrive copies a staged file into a new Drive node owned by the
// recipient and commits a version.
func (s *Service) saveFileToDrive(ctx context.Context, userID, parentID, name, contentType, stagePath string) (*Node, error) {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	n, _, err := s.UploadGrant(ctx, userID, parentID, name, contentType, filesPeerGrantTTL)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(stagePath)
	if err != nil {
		return nil, ErrNotFound
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	etag, err := s.PutContent(ctx, userID, n.ID, f, fi.Size(), contentType)
	if err != nil {
		return nil, err
	}
	if _, err := s.Commit(userID, n.ID, fi.Size(), contentType, etag); err != nil {
		return nil, err
	}
	return s.getNode(n.ID)
}

// extractTarToDrive unpacks a staged tar archive into a new Drive folder named
// name under parentID, recreating the directory tree and file bytes as Drive
// nodes owned by the recipient.
func (s *Service) extractTarToDrive(ctx context.Context, userID, parentID, name, stagePath string) (*Node, error) {
	root, err := s.CreateFolder(userID, parentID, name)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(stagePath)
	if err != nil {
		return nil, ErrNotFound
	}
	defer f.Close()

	// dirs maps a tar-relative directory path to its created Drive node ID.
	dirs := map[string]string{"": root.ID}
	ensureDir := func(rel string) (string, error) {
		rel = strings.Trim(rel, "/")
		if id, ok := dirs[rel]; ok {
			return id, nil
		}
		// Create any missing ancestors top-down.
		parts := strings.Split(rel, "/")
		curRel := ""
		curID := root.ID
		for _, p := range parts {
			next := p
			if curRel != "" {
				next = curRel + "/" + p
			}
			if id, ok := dirs[next]; ok {
				curID, curRel = id, next
				continue
			}
			fn, err := s.CreateFolder(userID, curID, p)
			if err != nil {
				return "", err
			}
			dirs[next] = fn.ID
			curID, curRel = fn.ID, next
		}
		return curID, nil
	}

	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("files: read tar: %w", err)
		}
		clean := strings.Trim(filepath.ToSlash(filepath.Clean("/"+hdr.Name)), "/")
		if clean == "" || strings.HasPrefix(clean, "../") {
			continue // skip traversal / root entries
		}
		// The tar is rooted at the shared folder's own name; strip that prefix so
		// the contents land directly under our new root.
		if i := strings.IndexByte(clean, '/'); i >= 0 {
			clean = clean[i+1:]
		} else {
			continue // the top-level folder entry itself
		}
		if hdr.Typeflag == tar.TypeDir {
			if _, err := ensureDir(clean); err != nil {
				return nil, err
			}
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		dirRel, base := splitDir(clean)
		parent, err := ensureDir(dirRel)
		if err != nil {
			return nil, err
		}
		ct := "application/octet-stream"
		n, _, err := s.UploadGrant(ctx, userID, parent, base, ct, filesPeerGrantTTL)
		if err != nil {
			return nil, err
		}
		etag, err := s.PutContent(ctx, userID, n.ID, tr, hdr.Size, ct)
		if err != nil {
			return nil, err
		}
		if _, err := s.Commit(userID, n.ID, hdr.Size, ct, etag); err != nil {
			return nil, err
		}
	}
	return root, nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

// filesPeerGrantTTL bounds the (discarded) write grant minted while saving a
// received item into the Drive data plane.
const filesPeerGrantTTL = 15 * time.Minute

// splitDir splits a slash path into (dir, base).
func splitDir(p string) (string, string) {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i], p[i+1:]
	}
	return "", p
}

// orAny renders an empty recipient as "anyone" for audit detail.
func orAny(recipient string) string {
	if recipient == "" {
		return "anyone"
	}
	return recipient
}
