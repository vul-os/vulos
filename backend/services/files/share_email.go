package files

// share_email.go — ACCOUNT-ONLY SHARING: share-by-email with locality routing.
//
// A sharer enters a recipient EMAIL (not a raw principal/account string). The
// email is resolved to a ShareRecipient (Contract 2, via the directory) and the
// share is routed by locality (Contract 3):
//
//   - CO-CLOUD (recipient is a principal on the SAME control plane): use the
//     per-node ACL grant path (Share) — no peering. This is the primary path for
//     free cloud accounts where Alice and Bob live on the same cell.
//   - REMOTE (recipient is a cloud-home VulaID or any cross-instance box): mint a
//     per-document, role-scoped, expiring, revocable peershare Capability bound to
//     the recipient's VulaID (IssueCapability) and deliver it to the recipient's
//     server. For an account-only user the recipient server is the cell, which
//     redeems on the account's behalf and stages the bytes into the account's
//     Drive (vulos-cloud owns that redemption).
//
// The resolver + deliverer are SEAMS implemented by cmd/server (it can import the
// peering directory and the auth store); the files package stays peering-free.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// Sentinel errors for the share-by-email flow.
var (
	// ErrShareResolveUnavailable is returned when ShareByEmail is called but no
	// resolver is wired (WithShareResolver not called). Callers map it to 503.
	ErrShareResolveUnavailable = errors.New("files: share resolver unavailable")
	// ErrRecipientNotFound is returned when the email resolves to neither a
	// co-cloud principal nor a directory entry. Callers map it to 404.
	ErrRecipientNotFound = errors.New("files: share recipient not found")
	// ErrRecipientNoContentKey is returned by ShareSealedByEmail when a REMOTE
	// recipient has published no X25519 content key, so a content-blind share to
	// them cannot be made. FAIL CLOSED: the share is refused rather than sent as
	// plaintext through the relaying cell. Callers surface it to the sharer.
	ErrRecipientNoContentKey = errors.New("files: recipient has not published a content key; cannot share content-blind")
)

// ShareRecipient is the resolved target of a share-by-email. Exactly one form is
// populated:
//
//   - CO-CLOUD: PrincipalID set (a local principal on the same control plane).
//   - REMOTE:   VulaID (and usually Server) set (a cloud-home / box peering id).
//
// A zero value (both PrincipalID and VulaID empty) means "not found".
type ShareRecipient struct {
	PrincipalID string // co-cloud local principal; "" when remote
	VulaID      string // remote cloud-home/box VulaID; "" when co-cloud
	Server      string // remote server address (for capability delivery)
	DisplayName string
	// ContentPubKey is the recipient's PUBLISHED X25519 content-encryption public
	// key (base64 std). For a REMOTE share the sharer's CLIENT wraps the file
	// content to this key so the relaying cell stays content-blind. Empty means the
	// recipient has published no content key: a content-blind share to them cannot
	// be made and must fail closed (see ErrRecipientNoContentKey).
	ContentPubKey string
}

// IsLocal reports whether the recipient is co-cloud (ACL grant path).
func (r ShareRecipient) IsLocal() bool { return r.PrincipalID != "" }

// IsRemote reports whether the recipient is remote (peershare capability path).
func (r ShareRecipient) IsRemote() bool { return r.PrincipalID == "" && r.VulaID != "" }

// ShareResolver resolves a recipient email to a ShareRecipient, deciding locality
// (Contract 2 + 3). The implementation (cmd/server) checks the local account
// store first (co-cloud) and falls back to the vulos.org directory
// (DiscoveryLookupByEmail) for remote recipients.
type ShareResolver interface {
	ResolveRecipient(ctx context.Context, email string) (ShareRecipient, error)
}

// CapabilityDeliverer delivers a minted peer-share capability to a REMOTE
// recipient's server intake so the recipient (or, for an account-only user, its
// cell) can redeem it on the account's behalf. A nil deliverer means "mint but do
// not auto-deliver" — the returned link can be handed over out of band.
type CapabilityDeliverer interface {
	DeliverCapability(ctx context.Context, server string, d CapabilityDelivery) error
}

// CapabilityDelivery is the payload handed to a CapabilityDeliverer.
type CapabilityDelivery struct {
	RecipientVulaID string      `json:"recipient"`
	Link            string      `json:"link"`
	Capability      *Capability `json:"capability,omitempty"`
}

// WithShareResolver wires the email→recipient resolver and (optionally) the
// remote capability deliverer used by ShareByEmail. Returns s for chaining.
func (s *Service) WithShareResolver(resolver ShareResolver, deliverer CapabilityDeliverer) *Service {
	s.shareResolver = resolver
	s.capDeliverer = deliverer
	return s
}

// ShareByEmailResult reports the outcome of a ShareByEmail call. Mode is
// "co-cloud" or "remote"; the populated fields depend on the mode.
type ShareByEmailResult struct {
	Mode        string      `json:"mode"` // "co-cloud" | "remote"
	PrincipalID string      `json:"principal_id,omitempty"`
	VulaID      string      `json:"vula_id,omitempty"`
	Server      string      `json:"server,omitempty"`
	DisplayName string      `json:"display_name,omitempty"`
	ACL         *ACLEntry   `json:"acl,omitempty"`        // co-cloud
	Capability  *Capability `json:"capability,omitempty"` // remote
	Link        string      `json:"link,omitempty"`       // remote
	Delivered   bool        `json:"delivered,omitempty"`  // remote: deliverer succeeded
}

// ShareByEmail resolves a recipient email and routes the share by locality.
//
//   - role must be editor or viewer.
//   - ownerAddr is THIS box's reachable base URL, embedded into a remote
//     capability so the recipient knows where to stream from (ignored co-cloud).
//   - ttl bounds a remote capability (clamped by IssueCapability); ignored
//     co-cloud.
//
// Only the node owner may share (enforced here AND re-checked by Share /
// IssueCapability). Fail closed: an unresolvable email yields ErrRecipientNotFound
// and grants nothing.
func (s *Service) ShareByEmail(ctx context.Context, actorID, nodeID, email string, role Role, ownerAddr string, ttl time.Duration) (*ShareByEmailResult, error) {
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

	// Authorize early (defense in depth; the downstream calls re-check).
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

	switch {
	case rec.IsLocal():
		// CO-CLOUD: per-node ACL grant, unchanged.
		acl, err := s.Share(actorID, nodeID, rec.PrincipalID, role)
		if err != nil {
			return nil, err
		}
		s.audit(actorID, "share.email.cocloud", nodeID, email+"="+string(role))
		return &ShareByEmailResult{
			Mode:        "co-cloud",
			PrincipalID: rec.PrincipalID,
			DisplayName: rec.DisplayName,
			ACL:         acl,
		}, nil

	case rec.IsRemote():
		// REMOTE: per-document capability bound to the recipient VulaID.
		cap, link, err := s.IssueCapability(actorID, nodeID, role, rec.VulaID, ownerAddr, ttl)
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
		// Deliver to the recipient's server intake (cell redeems for an
		// account-only user). Delivery failure does NOT fail the share — the link
		// is returned so the sharer can still hand it over.
		if s.capDeliverer != nil && rec.Server != "" {
			if derr := s.capDeliverer.DeliverCapability(ctx, rec.Server, CapabilityDelivery{
				RecipientVulaID: rec.VulaID,
				Link:            link,
				Capability:      cap,
			}); derr != nil {
				log.Printf("[files] capability delivery to %s failed (link returned to sharer): %v", rec.Server, derr)
			} else {
				res.Delivered = true
			}
		}
		s.audit(actorID, "share.email.remote", nodeID, email+"="+string(role)+" recipient="+rec.VulaID)
		return res, nil

	default:
		return nil, ErrRecipientNotFound
	}
}
