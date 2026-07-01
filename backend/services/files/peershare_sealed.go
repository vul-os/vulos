package files

// peershare_sealed.go — WAVE-3 CONTENT-BLIND peer-share: sealed capabilities.
//
// When a self-hosted user shares a file with an account-only (cloud) recipient, the
// bytes are relayed by the Vulos cell (RedeemCapability → DriveStager). To keep the
// cell content-blind the SHARER's client SEALS the file first (src/lib/contentSeal.js
// → a VSEAL1 envelope wrapped to the recipient's published content key) and issues a
// SEALED capability whose served bytes are that pre-sealed artifact — never the live
// plaintext node. ServeCapability streams the artifact; the cell verifies it is
// sealed (contentseal) and can decrypt nothing; the recipient's client opens it.
//
// The sealed artifact is staged on local disk under <stagingDir>/sealed/<capID> at
// issue time and removed on revoke. Only ONE opaque artifact is stored per
// capability (a sealed folder is a sealed tar the recipient untars after decrypt),
// so IsDir is forced false on the wire — the cell treats the payload as a single
// opaque node.
//
// Metadata note (WAVE-7): Name / ContentType / IsDir are now SEALED INSIDE the
// envelope (a VMETA1 container the sharer's client packs ahead of the payload —
// contentseal.PackSealedPayload). The capability therefore carries only an opaque
// placeholder name, an empty content-type, and IsDir=false, so the relaying cell
// and any observer see no filename/type. The recipient recovers the real metadata
// on decrypt (UnpackSealedPayload). Size stays on the capability (it is the sealed
// artifact's byte length — a coarse ciphertext size, not the plaintext size).

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vulos/backend/internal/ulid"
)

// SealedCapPlaceholderName is the opaque, non-identifying name a WAVE-7 sealed
// capability carries in place of the real filename (which is sealed inside the
// envelope). It must be non-empty (the recipient's Drive requires a node name until
// the client decrypts and recovers the true name).
const SealedCapPlaceholderName = "Encrypted item"

// ShareSealedByEmail is the CONTENT-BLIND sibling of ShareByEmail for REMOTE
// (account-only / cross-instance) recipients. The sharer's client has already
// sealed the file to the recipient's published content key; this resolves the
// recipient, verifies the seal is addressed to their published key, mints a SEALED
// capability, and delivers it to the recipient's server intake (the cell redeems it
// on the account's behalf, seeing only ciphertext).
//
// FAIL CLOSED:
//   - a co-cloud (local) recipient is rejected — sealing is for the cell-relay path;
//     callers should use the ordinary co-cloud ACL share instead;
//   - a remote recipient with NO published content key yields
//     ErrRecipientNoContentKey (never a plaintext fallback);
//   - sealed bytes not addressed to the recipient's key are refused.
func (s *Service) ShareSealedByEmail(ctx context.Context, actorID, nodeID, email string, role Role, ownerAddr string, ttl time.Duration, sealed io.Reader) (*ShareByEmailResult, error) {
	if !validShareRole(role) {
		return nil, fmt.Errorf("%w: role must be editor or viewer", ErrInvalid)
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, fmt.Errorf("%w: email required", ErrInvalid)
	}
	if s.shareResolver == nil {
		return nil, ErrShareResolveUnavailable
	}
	n, err := s.getNode(nodeID)
	if err != nil {
		return nil, err
	}
	if !s.authorize(actorID, n, RoleOwner) {
		return nil, ErrForbidden
	}
	rec, err := s.shareResolver.ResolveRecipient(ctx, email)
	if err != nil {
		return nil, err
	}
	if rec.IsLocal() {
		return nil, fmt.Errorf("%w: recipient is co-cloud; use the ordinary share (no cell relay involved)", ErrInvalid)
	}
	if !rec.IsRemote() {
		return nil, ErrRecipientNotFound
	}
	if strings.TrimSpace(rec.ContentPubKey) == "" {
		return nil, ErrRecipientNoContentKey
	}

	// Read the sealed bytes once and verify they are addressed to the recipient's
	// published content key (defense in depth — the client wrapped to it, but the
	// server confirms before minting a capability that promises deliverability).
	data, err := io.ReadAll(sealed)
	if err != nil {
		return nil, fmt.Errorf("files: read sealed bytes: %w", err)
	}
	if !SealedTargets(data, rec.ContentPubKey) {
		return nil, fmt.Errorf("%w: sealed bytes are not addressed to the recipient's content key", ErrInvalid)
	}

	cap, link, err := s.IssueSealedCapability(actorID, nodeID, role, rec.VulaID, ownerAddr, ttl, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	res := &ShareByEmailResult{
		Mode:        "remote",
		VulaID:      rec.VulaID,
		Server:      rec.Server,
		DisplayName: rec.DisplayName,
		Capability:  cap,
		Link:        link,
	}
	if s.capDeliverer != nil && rec.Server != "" {
		if derr := s.capDeliverer.DeliverCapability(ctx, rec.Server, CapabilityDelivery{
			RecipientVulaID: rec.VulaID,
			Link:            link,
			Capability:      cap,
		}); derr != nil {
			log.Printf("[files] sealed capability delivery to %s failed (link returned to sharer): %v", rec.Server, derr)
		} else {
			res.Delivered = true
		}
	}
	s.audit(actorID, "share.email.sealed", nodeID, email+"="+string(role)+" recipient="+rec.VulaID)
	return res, nil
}

// sealedArtifactDir returns the directory sealed artifacts are staged under.
func (s *Service) sealedArtifactDir() string {
	dir := s.stagingDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "vulos-peer-received")
	}
	return filepath.Join(dir, "sealed")
}

// sealedArtifactPath returns the on-disk path for a capability's sealed artifact.
// capID is a ULID (safe filename); we defensively strip any path separators.
func (s *Service) sealedArtifactPath(capID string) string {
	safe := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == '.' {
			return '_'
		}
		return r
	}, capID)
	return filepath.Join(s.sealedArtifactDir(), safe)
}

// stashSealedArtifact writes a sealed envelope to disk for capID and returns its
// byte size. It refuses to store bytes that are not a VSEAL1 sealed envelope, so a
// sealed capability can never accidentally serve plaintext (fail closed).
func (s *Service) stashSealedArtifact(capID string, r io.Reader) (int64, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, fmt.Errorf("files: read sealed artifact: %w", err)
	}
	if !IsSealed(data) {
		return 0, fmt.Errorf("%w: refusing to store a non-sealed peer-share artifact", ErrInvalid)
	}
	if _, perr := ParseSealed(data); perr != nil {
		return 0, fmt.Errorf("%w: malformed sealed artifact: %v", ErrInvalid, perr)
	}
	dir := s.sealedArtifactDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, fmt.Errorf("files: sealed dir: %w", err)
	}
	path := s.sealedArtifactPath(capID)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return 0, fmt.Errorf("files: write sealed artifact: %w", err)
	}
	return int64(len(data)), nil
}

// openSealedArtifact opens a capability's staged sealed artifact for streaming.
func (s *Service) openSealedArtifact(capID string) (io.ReadCloser, int64, error) {
	f, err := os.Open(s.sealedArtifactPath(capID))
	if err != nil {
		return nil, 0, ErrNotFound
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, fi.Size(), nil
}

// removeSealedArtifact deletes a capability's sealed artifact (best-effort).
func (s *Service) removeSealedArtifact(capID string) {
	_ = os.Remove(s.sealedArtifactPath(capID))
}

// IssueSealedCapability mints a CONTENT-BLIND peer-share capability over nodeID
// whose served bytes are the caller-supplied pre-sealed VSEAL1 artifact (sealed by
// the sharer's client and wrapped to the recipient's published content key). Only
// the node's owner may issue. recipient is the bound peer's VulaID (required for a
// sealed share — a content-blind share must name who can open it). ownerAddr is
// THIS box's reachable base URL. ttl is clamped like IssueCapability.
//
// Returns the capability and its paste-anywhere link. The sealed bytes are staged
// on disk and served (unchanged) by ServeCapability; the sharer's own plaintext
// node is never read for this share.
func (s *Service) IssueSealedCapability(actorID, nodeID string, access Role, recipient, ownerAddr string, ttl time.Duration, sealed io.Reader) (*Capability, string, error) {
	if s.signer == nil {
		return nil, "", ErrPeerUnavailable
	}
	if !validShareRole(access) {
		return nil, "", fmt.Errorf("%w: access must be editor or viewer", ErrInvalid)
	}
	if strings.TrimSpace(recipient) == "" {
		return nil, "", fmt.Errorf("%w: a sealed (content-blind) share must name a recipient", ErrInvalid)
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

	capID := ulid.NewULID()
	sealedSize, err := s.stashSealedArtifact(capID, sealed)
	if err != nil {
		return nil, "", err
	}

	now := time.Now()
	cap := &Capability{
		ID:     capID,
		NodeID: n.ID,
		// Content-blind metadata (WAVE-7): the real Name/ContentType/IsDir are sealed
		// inside the VMETA1 envelope; the capability leaks only an opaque placeholder.
		Name:        SealedCapPlaceholderName,
		IsDir:       false, // sealed payload is a single opaque artifact (file OR tar)
		Size:        sealedSize,
		ContentType: "",
		Access:      access,
		OwnerVulaID: s.signer.SelfID(),
		OwnerAddr:   strings.TrimRight(ownerAddr, "/"),
		Recipient:   recipient,
		IssuedAt:    now.UTC(),
		ExpiresAt:   now.Add(ttl).UTC(),
		Sealed:      true,
	}
	msg, err := cap.signingBytes()
	if err != nil {
		s.removeSealedArtifact(capID)
		return nil, "", fmt.Errorf("files: sign sealed capability: %w", err)
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
		s.removeSealedArtifact(capID)
		return nil, "", err
	}
	link, err := encodeCapabilityLink(cap)
	if err != nil {
		s.removeSealedArtifact(capID)
		return nil, "", err
	}
	s.audit(actorID, "peer.issue.sealed", n.ID, string(access)+" recipient="+recipient)
	return cap, link, nil
}
