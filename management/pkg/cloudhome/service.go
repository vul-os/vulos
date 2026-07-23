// service.go — the cloud-home Service ties together the three cloud-side
// contracts of account-only document sharing:
//
//	Contract 1: Provision      — lazily mint + custody a per-account VulaID.
//	Contract 2: Resolve        — directory: email -> {VulaID, Server, DisplayName}.
//	Contract 3: RedeemCapability — cell redeems a per-document peershare capability
//	            on the account's behalf using the cloud-home signer, then stages the
//	            bytes into the account's Drive (SaveReceivedToDrive bridge).
package cloudhome

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vul-os/vulos-management/pkg/contentseal"
)

// ErrNotContentBlind is returned by RedeemCapability when the bytes pulled from the
// sharer are NOT a content-blind VSEAL1 sealed envelope (or are not wrapped to the
// recipient). The cell refuses to stage them: it must only ever transport
// ciphertext it cannot read. This is the wave-3 fail-closed guarantee that closes
// the last cloud-read path.
var ErrNotContentBlind = errors.New("cloudhome: refusing to stage non-content-blind (unsealed) bytes")

// ─────────────────────────────────────────────────────────────────────────────
// Seams the Service depends on (wired in cmd/server). Keeping them as narrow
// interfaces keeps cloudhome free of import cycles with auth/keydir/files.
// ─────────────────────────────────────────────────────────────────────────────

// AccountLookup resolves a verified email to an account. Implemented over
// auth.Store in the cell. DisplayName falls back to the email when the account
// has no separate profile name.
type AccountLookup interface {
	// LookupByEmail returns (accountID, displayName) for email, or ErrNoAccount.
	LookupByEmail(ctx context.Context, email string) (accountID, displayName string, err error)
}

// ErrNoAccount is returned by AccountLookup when no account owns the email.
var ErrNoAccount = errors.New("cloudhome: no account for email")

// ErrNotDiscoverable is returned by Resolve when an account exists but has opted
// OUT of directory discovery (a private account). The directory handler maps it
// to the SAME uniform 404 as ErrNoAccount so the two cannot be distinguished by
// an enumeration prober (DIR-ENUM-01). The account-only opt-out flag itself is a
// forthcoming data-model field (cross-repo, see final report); this sentinel +
// the uniform mapping are in place so wiring it needs no route change.
var ErrNotDiscoverable = errors.New("cloudhome: account not directory-discoverable")

// ContentKeyLookup returns an account's PUBLISHED X25519 content-encryption public
// key (base64 std) — the value the account's client derived from its master key and
// published to its profile (vulos peering.ProfileData.ContentPubKey). It is PUBLIC
// key material only; the cell never sees the private half. Returns ("", nil) when
// the account has published no content key.
//
// It is OPTIONAL. When wired (CLOUDHOME_CONTENT_KEY_URL set — the standard managed
// config), RedeemCapability additionally requires the pulled ciphertext to be
// wrapped to this key, and FAILS CLOSED if the lookup errors/times out or the
// account has no published key (it must NOT fall open and accept an untargeted
// seal). When NOT wired the cell still enforces the unconditional content-blind gate
// (the pulled bytes MUST be a VSEAL1 sealed envelope the cell cannot open); this seam
// adds recipient-targeting integrity on top.
//
// Contract: ContentPubKey returns a non-nil error ONLY when the lookup could not be
// completed (unreachable / non-200) — that is the fail-closed signal. A completed
// lookup returns (key, nil), with key="" meaning the account has published none.
type ContentKeyLookup interface {
	ContentPubKey(ctx context.Context, accountID string) (string, error)
}

// BoxLookup reports whether an account runs its OWN box (and is therefore
// resolvable as a box peer rather than via its cloud-home identity). Implemented
// over keydir.Store in the cell. When ok is false the account is account-only.
type BoxLookup interface {
	// BoxIdentity returns the box VulaID + reachable server for an account that
	// publishes a discoverable box key, with ok=false when the account runs no box.
	BoxIdentity(ctx context.Context, accountID string) (vulaID, server string, ok bool, err error)
}

// DriveStager stages already-pulled document bytes into an account's Drive. This
// is the cell-side end of the vulos B→A bridge (Contract 3): the cell PULLs a
// capability's bytes from the sharer (PeerServeClient) and then hands them here
// to land in the account's Drive on the cell's own vulos Files service.
//
// RECONCILED with vulos backend/cmd/server/routes_files_peer.go
// `POST /api/files/peer/receive` → files.Service.SaveBytesToDrive: the cell
// posts X-User-ID=accountID plus a JSON body of EXACTLY the ReceivedDoc wire
// fields the receiver reads (parent_id, name, content_type, is_dir, bytes). vulos
// writes the node into that account's Drive.
type DriveStager interface {
	SaveReceivedToDrive(ctx context.Context, accountID string, doc ReceivedDoc) error
}

// ReceivedDoc is the staged payload handed to DriveStager after the cell has
// pulled the document bytes from the sharer. The json tags marked "vulos wire"
// are the exact shape the vulos /api/files/peer/receive handler decodes; the
// remaining fields are cell-side context (audit/source) not forwarded to vulos.
type ReceivedDoc struct {
	// Forwarded to vulos /api/files/peer/receive (shape MUST match exactly).
	// WAVE-7: for a sealed share the real Name/ContentType/IsDir are SEALED inside
	// the VSEAL1 envelope (a VMETA1 container), so these carry only an opaque
	// placeholder name, empty content-type, and IsDir=false — the cell never sees the
	// filename/type. The recipient's client recovers them on decrypt.
	ParentID    string `json:"parent_id"`    // "" => recipient's Drive root
	Name        string `json:"name"`         // capability.Name (opaque placeholder for sealed shares)
	ContentType string `json:"content_type"` // capability.ContentType (empty for sealed shares)
	IsDir       bool   `json:"is_dir"`       // capability.IsDir (always false for sealed shares)
	// Bytes is a WAVE-3 "VSEAL1" sealed-content envelope (contentseal), NOT
	// plaintext: the file content encrypted under a per-share key that is wrapped to
	// the recipient's published X25519 content key. The cell verified the framing
	// but holds no key to open it. vulos stores this ciphertext as the Drive node's
	// content; the recipient's client decrypts on download.
	Bytes []byte `json:"bytes"`

	// Cell-side context (NOT part of the vulos receiver contract):
	DocID        string `json:"doc_id,omitempty"`         // capability ID
	SourceVulaID string `json:"source_vula_id,omitempty"` // sharer box VulaID
	SourceServer string `json:"source_server,omitempty"`  // sharer serve base URL
	// Sealed marks the payload as a content-blind VSEAL1 envelope (always true in
	// wave 3). vulos ignores it; it documents the invariant on the wire.
	Sealed bool `json:"sealed,omitempty"`
}

// PeerServeClient PULLs a capability's document bytes from a sharer box by
// POSTing a signed PeerFetchRequest to <ownerAddr>/api/files/peer/serve and
// returning the streaming response body. This is the cell-side counterpart of
// vulos files.HTTPPeerTransport / ServeCapability: the cell signs the fetch proof
// with the recipient's cloud-home key (recipient binding) and streams the bytes
// the sharer serves. The concrete HTTP impl is wired in cmd/server; tests
// substitute an in-process fake standing in for the sharer.
type PeerServeClient interface {
	Serve(ctx context.Context, ownerAddr string, req PeerFetchRequest) (io.ReadCloser, error)
}

// PeerFetchRequest is the EXACT wire shape vulos files.PeerFetchRequest decodes
// at /api/files/peer/serve. Capability is forwarded opaquely (the cell does not
// verify it — the sharer re-verifies its own signature); RequesterID is the
// recipient's cloud-home VulaID; Proof is base64url(Ed25519 sig over
// fetchProofMessage(capID, ts)).
type PeerFetchRequest struct {
	Capability  json.RawMessage `json:"capability"`
	RequesterID string          `json:"requester_id"`
	Timestamp   int64           `json:"timestamp"`
	Proof       string          `json:"proof"`
}

// ─────────────────────────────────────────────────────────────────────────────
// DiscoveryResult — Contract 2 wire shape.
//
// CROSS-REPO ASSUMPTION (reconcile with the vulos agent): the vulos discovery
// client (discovery.go) expects exactly these three fields. JSON tags here are
// snake_case (vula_id/server/display_name); if vulos uses different tags, change
// ONLY these struct tags — the field set is the contract.
// ─────────────────────────────────────────────────────────────────────────────

// DiscoveryResult is what GET /api/verify/lookup?email= returns. These three
// fields are the success shape vulos's discovery client consumes.
//
// CONTENT-BLIND: the directory advertises ONLY PUBLIC key material — a public
// VulaID + server + display name + the recipient's PUBLISHED X25519 content
// public key. It does NOT advertise (and the cell does not hold) any content
// PRIVATE key or prekey-claim path — content forward secrecy is negotiated
// client-side, browser↔browser, so the cell never touches a content decryption key.
type DiscoveryResult struct {
	VulaID      string `json:"vula_id"`
	Server      string `json:"server"`
	DisplayName string `json:"display_name"`
	// ContentPubKey is the account's PUBLISHED X25519 content-encryption public key
	// (base64 std, 32 raw bytes) — see the vulos peering.ProfileData.ContentPubKey /
	// src/lib/contentSeal.js. A sharer wraps file content to this key so the cell,
	// which has no matching private key, stays content-blind. Empty means the
	// account has published none: a content-blind share to them must fail closed on
	// the sharer side (never plaintext through the cell). WAVE-3 wire contract,
	// reconciled with vulos peering.DiscoveryResult.ContentPubKey.
	ContentPubKey string `json:"content_pub_key,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Service
// ─────────────────────────────────────────────────────────────────────────────

// KeyAuditor records every access/use of a custodied cloud-home private key
// (sign / redeem / rotate). It is wired to the control-plane audit log in
// cmd/server; tests substitute a recorder. Implementations MUST be safe for
// concurrent use and SHOULD NOT block the signing path (best-effort, async-safe).
type KeyAuditor interface {
	// RecordKeyAccess logs that the private key for (accountID, vulaID) was used
	// for op ("sign", "redeem", "rotate"). detail carries op-specific context.
	RecordKeyAccess(ctx context.Context, op, accountID, vulaID string, detail map[string]string)
}

// Service provisions, resolves, signs for, and redeems shares on behalf of
// cloud-home accounts.
type Service struct {
	store       Store
	keyring     map[int][]byte // KEK version → 32-byte key (current + prior, for decrypt)
	kekVersion  int            // current version used for new writes / re-seals
	server      string         // this cell's server base URL (the Server in a cloud-home DiscoveryResult)
	account     AccountLookup
	box         BoxLookup        // optional; nil => every account resolves to cloud-home
	stager      DriveStager      // optional; nil => RedeemCapability fails closed at the staging step
	puller      PeerServeClient  // optional; nil => RedeemCapability fails closed at the pull step
	contentKeys ContentKeyLookup // optional; wired => additionally require the seal targets the recipient
	auditor     KeyAuditor       // optional; nil => no key-access audit (dev)

	now    func() time.Time
	provMu keyedMutex // serialise concurrent provision of the same account
	rotMu  sync.Mutex // serialise KEK rotation against itself
}

// Config carries the Service dependencies.
type Config struct {
	Store Store
	// KEK is the CURRENT 32-byte cloud-home KEK (new writes are sealed under it).
	KEK []byte
	// KEKVersion is the version label of KEK (default 1). Persisted with each row
	// so prior-version rows remain decryptable via PriorKEKs after a rotation.
	KEKVersion int
	// PriorKEKs maps prior version → 32-byte key, for decrypting rows not yet
	// re-sealed by RotateKEK. Optional. MUST NOT contain KEKVersion.
	PriorKEKs   map[int][]byte
	Server      string
	Account     AccountLookup
	Box         BoxLookup
	Stager      DriveStager
	Puller      PeerServeClient
	ContentKeys ContentKeyLookup
	Auditor     KeyAuditor
	Now         func() time.Time
}

// NewService validates cfg and returns a ready Service. It FAILS CLOSED: a nil
// store, a KEK that is not 32 bytes, or an empty server are rejected so the cell
// never custodies peering keys under a bad configuration.
func NewService(cfg Config) (*Service, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("cloudhome: nil store")
	}
	if len(cfg.KEK) != 32 {
		return nil, fmt.Errorf("cloudhome: KEK must be 32 bytes, got %d", len(cfg.KEK))
	}
	if strings.TrimSpace(cfg.Server) == "" {
		return nil, fmt.Errorf("cloudhome: server base URL required")
	}
	if cfg.Account == nil {
		return nil, fmt.Errorf("cloudhome: account lookup required")
	}
	version := cfg.KEKVersion
	if version < 1 {
		version = 1
	}
	keyring := map[int][]byte{version: cfg.KEK}
	for v, k := range cfg.PriorKEKs {
		if v == version {
			return nil, fmt.Errorf("cloudhome: PriorKEKs reuses current version %d", v)
		}
		if len(k) != 32 {
			return nil, fmt.Errorf("cloudhome: prior KEK version %d must be 32 bytes, got %d", v, len(k))
		}
		keyring[v] = k
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:       cfg.Store,
		keyring:     keyring,
		kekVersion:  version,
		server:      strings.TrimRight(cfg.Server, "/"),
		account:     cfg.Account,
		box:         cfg.Box,
		stager:      cfg.Stager,
		puller:      cfg.Puller,
		contentKeys: cfg.ContentKeys,
		auditor:     cfg.Auditor,
		now:         now,
		provMu:      newKeyedMutex(),
	}, nil
}

// NewServiceKeyring is a convenience constructor when the caller already holds a
// keyring (e.g. from LoadKeyring). version must exist in keyring.
func NewServiceKeyring(cfg Config, keyring map[int][]byte, version int) (*Service, error) {
	cur, ok := keyring[version]
	if !ok {
		return nil, fmt.Errorf("cloudhome: keyring missing current version %d", version)
	}
	prior := make(map[int][]byte, len(keyring))
	for v, k := range keyring {
		if v != version {
			prior[v] = k
		}
	}
	cfg.KEK = cur
	cfg.KEKVersion = version
	cfg.PriorKEKs = prior
	return NewService(cfg)
}

// ─────────────────────────────────────────────────────────────────────────────
// Contract 1 — lazy provisioning
// ─────────────────────────────────────────────────────────────────────────────

// Provision returns the account's cloud-home identity, minting one on first use.
// It is safe under concurrency: a per-account lock plus an ErrExists re-read
// guarantees at-most-one identity per account.
func (s *Service) Provision(ctx context.Context, accountID string) (Identity, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return Identity{}, ErrInvalidInput
	}
	// Fast path: already provisioned.
	if r, err := s.store.GetByAccount(ctx, accountID); err == nil {
		return r.identity(), nil
	} else if !errors.Is(err, ErrNotFound) {
		return Identity{}, err
	}

	unlock := s.provMu.lock(accountID)
	defer unlock()

	// Re-check under the lock (another goroutine may have just provisioned).
	if r, err := s.store.GetByAccount(ctx, accountID); err == nil {
		return r.identity(), nil
	} else if !errors.Is(err, ErrNotFound) {
		return Identity{}, err
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, fmt.Errorf("cloudhome: keygen: %w", err)
	}
	// Seal the 64-byte private key (seed||pub) under the CURRENT KEK; never store
	// plaintext. The version is persisted so a later rotation can re-seal it.
	enc, err := sealKEK(priv, s.keyring[s.kekVersion])
	if err != nil {
		return Identity{}, err
	}
	r := record{
		AccountID:    accountID,
		VulaID:       FormatVulaID(pub),
		PublicKeyB64: base64.StdEncoding.EncodeToString(pub),
		EncPrivKey:   enc,
		KEKVersion:   s.kekVersion,
		Server:       s.server,
		CreatedAt:    s.now().UTC(),
	}
	if err := s.store.Insert(ctx, r); err != nil {
		if errors.Is(err, ErrExists) {
			// Lost a race against another process; return the winner.
			if existing, gerr := s.store.GetByAccount(ctx, accountID); gerr == nil {
				return existing.identity(), nil
			}
		}
		return Identity{}, err
	}
	return r.identity(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Contract 2 — directory resolution
// ─────────────────────────────────────────────────────────────────────────────

// Resolve implements GET /api/verify/lookup?email=. A box-running user resolves
// to its box VulaID + server (as today); an account-only user resolves to its
// cloud-home VulaID + this cell's server, provisioning the cloud-home identity
// lazily if absent.
func (s *Service) Resolve(ctx context.Context, email string) (DiscoveryResult, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		return DiscoveryResult{}, ErrInvalidInput
	}
	accountID, displayName, err := s.account.LookupByEmail(ctx, email)
	if err != nil {
		return DiscoveryResult{}, err // ErrNoAccount bubbles up to a 404
	}
	if displayName == "" {
		displayName = email
	}

	// Box-running user → resolve as a box peer (no cloud-home involvement).
	if s.box != nil {
		if vulaID, server, ok, berr := s.box.BoxIdentity(ctx, accountID); berr == nil && ok && vulaID != "" {
			return DiscoveryResult{VulaID: vulaID, Server: server, DisplayName: displayName}, nil
		} else if berr != nil {
			return DiscoveryResult{}, berr
		}
	}

	// Account-only user → cloud-home identity (lazily provisioned).
	id, err := s.Provision(ctx, accountID)
	if err != nil {
		return DiscoveryResult{}, err
	}
	// Honor revocation: a revoked cloud-home identity is NOT a valid share target.
	// Fail closed with the uniform not-discoverable sentinel (the handler maps it to
	// the same 404 as a missing account); peers learn the revocation via Lifecycle.
	if revoked, rerr := s.IsRevoked(ctx, id.VulaID); rerr != nil {
		return DiscoveryResult{}, rerr
	} else if revoked {
		return DiscoveryResult{}, ErrNotDiscoverable
	}

	// Advertise the account's PUBLISHED content public key (if any) so a sharer can
	// wrap file content to it for a content-blind share. Best-effort: a missing key
	// simply leaves the field empty, and the sharer must then fail closed rather
	// than send plaintext through the cell.
	contentPub := ""
	if s.contentKeys != nil {
		if pub, cerr := s.contentKeys.ContentPubKey(ctx, accountID); cerr == nil {
			contentPub = pub
		}
	}
	return DiscoveryResult{
		VulaID:        id.VulaID,
		Server:        id.Server,
		DisplayName:   displayName,
		ContentPubKey: contentPub,
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Managed signer — the cell signs on the account's behalf with its cloud-home key
// ─────────────────────────────────────────────────────────────────────────────

// SignForVulaID signs msg with the cloud-home private key bound to vulaID.
// Returns ErrNotFound when the cell does not custody that VulaID.
func (s *Service) SignForVulaID(ctx context.Context, vulaID string, msg []byte) ([]byte, error) {
	r, err := s.store.GetByVulaID(ctx, vulaID)
	if err != nil {
		return nil, err
	}
	return s.signRecord(ctx, r, msg, "sign")
}

// SignForAccount signs msg with the account's cloud-home private key.
func (s *Service) SignForAccount(ctx context.Context, accountID string, msg []byte) ([]byte, error) {
	r, err := s.store.GetByAccount(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return s.signRecord(ctx, r, msg, "sign")
}

// signRecord decrypts r's private key (under the KEK version it was sealed with),
// signs msg, AUDITS the key access, then scrubs the plaintext key. op is the
// audit verb ("sign" for direct signing, "redeem" for capability redemption).
func (s *Service) signRecord(ctx context.Context, r record, msg []byte, op string) ([]byte, error) {
	ver := r.KEKVersion
	if ver < 1 {
		ver = 1 // legacy rows predate versioning
	}
	kek, ok := s.keyring[ver]
	if !ok {
		// Fail closed: a row sealed under a KEK version we no longer hold cannot be
		// decrypted (e.g. the prior key was dropped before this row was re-sealed).
		return nil, fmt.Errorf("cloudhome: no KEK for version %d (rotate keyring before dropping prior keys)", ver)
	}
	priv, err := openKEK(r.EncPrivKey, kek)
	if err != nil {
		return nil, err
	}
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("cloudhome: decrypted key is %d bytes, want %d", len(priv), ed25519.PrivateKeySize)
	}
	sig := ed25519.Sign(ed25519.PrivateKey(priv), msg)
	// Best-effort scrub of the decrypted key.
	for i := range priv {
		priv[i] = 0
	}
	// AUDIT every private-key use (sign/redeem) into the control-plane audit log.
	s.auditKey(ctx, op, r.AccountID, r.VulaID, map[string]string{"kek_version": strconv.Itoa(ver)})
	return sig, nil
}

// auditKey emits a key-access audit event (best-effort: a nil auditor is a no-op;
// auditing never fails the signing path).
func (s *Service) auditKey(ctx context.Context, op, accountID, vulaID string, detail map[string]string) {
	if s.auditor == nil {
		return
	}
	s.auditor.RecordKeyAccess(ctx, op, accountID, vulaID, detail)
}

// RotateKEK re-encrypts EVERY stored private key under newKEK at newVersion,
// without downtime: prior versions remain in the keyring so signing keeps working
// throughout, and each row flips to newVersion only after a successful re-seal.
// It is idempotent — rows already at newVersion are skipped — and serialised
// against concurrent rotations. On success the Service's current write-version is
// advanced to newVersion and newKEK is added to the keyring.
//
// After rotation completes for ALL rows, the operator may drop the prior KEK from
// CLOUDHOME_KEK_PRIOR. Returns the number of rows re-sealed.
func (s *Service) RotateKEK(ctx context.Context, newKEK []byte, newVersion int) (int, error) {
	if len(newKEK) != 32 {
		return 0, fmt.Errorf("cloudhome: new KEK must be 32 bytes, got %d", len(newKEK))
	}
	if newVersion < 1 {
		return 0, fmt.Errorf("cloudhome: new KEK version must be >= 1")
	}
	s.rotMu.Lock()
	defer s.rotMu.Unlock()

	// If the version already exists it MUST be the same key (else we'd corrupt the
	// keyring); a brand-new version is added.
	if existing, ok := s.keyring[newVersion]; ok {
		if subtle.ConstantTimeCompare(existing, newKEK) != 1 {
			return 0, fmt.Errorf("cloudhome: version %d already maps to a different KEK", newVersion)
		}
	} else {
		s.keyring[newVersion] = newKEK
	}

	all, err := s.store.ListAll(ctx)
	if err != nil {
		return 0, fmt.Errorf("cloudhome: rotate list: %w", err)
	}
	rotated := 0
	for _, r := range all {
		if r.KEKVersion == newVersion {
			continue // already re-sealed
		}
		oldVer := r.KEKVersion
		if oldVer < 1 {
			oldVer = 1
		}
		oldKEK, ok := s.keyring[oldVer]
		if !ok {
			return rotated, fmt.Errorf("cloudhome: rotate: no KEK for current version %d of account %s", oldVer, r.AccountID)
		}
		priv, derr := openKEK(r.EncPrivKey, oldKEK)
		if derr != nil {
			return rotated, fmt.Errorf("cloudhome: rotate decrypt account %s: %w", r.AccountID, derr)
		}
		enc, serr := sealKEK(priv, newKEK)
		for i := range priv {
			priv[i] = 0
		}
		if serr != nil {
			return rotated, fmt.Errorf("cloudhome: rotate reseal account %s: %w", r.AccountID, serr)
		}
		if uerr := s.store.UpdateEncKey(ctx, r.AccountID, enc, newVersion); uerr != nil {
			return rotated, fmt.Errorf("cloudhome: rotate persist account %s: %w", r.AccountID, uerr)
		}
		s.auditKey(ctx, "rotate", r.AccountID, r.VulaID, map[string]string{
			"from_version": strconv.Itoa(oldVer),
			"to_version":   strconv.Itoa(newVersion),
		})
		rotated++
	}

	// NOTE: the only key material re-sealed here is the IDENTITY/signing key. The
	// cell holds no content-decryption keys (no X3DH/message prekeys) to rotate —
	// see the CONTENT-BLIND INVARIANT in the package doc.

	// Advance the current write-version so subsequent provisions seal under newKEK.
	s.kekVersion = newVersion
	return rotated, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Contract 3 — cell-side capability redemption
// ─────────────────────────────────────────────────────────────────────────────

// InboundShare is what a remote (self-hosted) sharer delivers to the cell's share
// intake — EXACTLY the vulos files.CapabilityDelivery wire shape
// ({recipient, link, capability}). The cell redeems it on the bound account's
// behalf. Capability is the opaque, signed peershare.Capability JSON (vulos owns
// its format/verification); the cell only reads its `id` and `owner_addr` to PULL
// and forwards the whole blob to the sharer's serve endpoint unchanged.
type InboundShare struct {
	Recipient  string          `json:"recipient"` // recipient's cloud-home VulaID
	Link       string          `json:"link"`      // base64url(capability JSON)
	Capability json.RawMessage `json:"capability,omitempty"`
}

// peerCapability is the minimal subset of the opaque vulos Capability the cell
// must read to PULL the bytes. The signature is NOT checked here (the sharer
// re-verifies on serve); the cell only needs the capability id, the sharer's
// serve base URL, the recipient binding, and the staging metadata.
type peerCapability struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	IsDir       bool   `json:"is_dir"`
	ContentType string `json:"content_type"`
	OwnerVulaID string `json:"owner_vula_id"`
	OwnerAddr   string `json:"owner_addr"`
	Recipient   string `json:"recipient"`
}

// Receipt is returned to the remote sharer on successful redemption.
type Receipt struct {
	AccountID       string `json:"account_id"`
	RecipientVulaID string `json:"recipient_vula_id"`
	DocID           string `json:"doc_id"`
	StagedAt        string `json:"staged_at"`
}

// fetchProofMessage MUST byte-match vulos files.fetchProofMessage: it is the
// message the recipient signs to prove it initiated a specific capability fetch
// at a specific time. vulos ServeCapability verifies
// signer.Verify(RequesterID, fetchProofMessage(capID, ts), proof), so any drift
// here fails every redemption.
func fetchProofMessage(capID string, ts int64) []byte {
	return []byte(fmt.Sprintf("files-peer-fetch:%s:%d", capID, ts))
}

// decodeCapabilityLink parses a base64url(JSON) capability link into raw JSON.
// It mirrors vulos files.DecodeCapabilityLink's URL/fragment tolerance so a
// pasted link with a route prefix still decodes.
func decodeCapabilityLink(link string) (json.RawMessage, error) {
	link = strings.TrimSpace(link)
	if i := strings.LastIndexAny(link, "#=/"); i >= 0 && i < len(link)-1 {
		link = link[i+1:]
	}
	raw, err := base64.RawURLEncoding.DecodeString(link)
	if err != nil {
		return nil, fmt.Errorf("cloudhome: malformed capability link: %w", err)
	}
	return json.RawMessage(raw), nil
}

// RedeemCapability redeems an inbound per-document capability ON THE ACCOUNT'S
// BEHALF using vulos's PULL protocol. It FAILS CLOSED at every step:
//   - the recipient VulaID must be one this cell custodies (else ErrNotFound);
//   - the capability must carry an id and the sharer's serve base URL;
//   - a bound capability must name the recipient we hold;
//   - the cell signs fetchProofMessage(capID, ts) with the recipient's cloud-home
//     key and PULLs the bytes from <owner_addr>/api/files/peer/serve. Because the
//     signing key's VulaID == the capability's bound recipient, the sharer's
//     ServeCapability recipient-binding check passes only for the right account;
//   - the pulled bytes are written into the account's Drive via the DriveStager
//     bridge (no puller/stager configured => error; nothing is silently dropped).
func (s *Service) RedeemCapability(ctx context.Context, in InboundShare) (Receipt, error) {
	recipient := strings.TrimSpace(in.Recipient)
	if recipient == "" {
		return Receipt{}, ErrInvalidInput
	}

	// Recipient binding: the cell must custody this VulaID's cloud-home key.
	r, err := s.store.GetByVulaID(ctx, recipient)
	if err != nil {
		return Receipt{}, err // ErrNotFound => fail closed (we are not this account's home)
	}
	// Honor revocation: refuse to use a revoked cloud-home key on the account's
	// behalf (fail closed).
	if revoked, rerr := s.IsRevoked(ctx, recipient); rerr != nil {
		return Receipt{}, rerr
	} else if revoked {
		return Receipt{}, ErrRevoked
	}

	// Extract the opaque capability (object preferred; else decode the link).
	capRaw := in.Capability
	if len(capRaw) == 0 {
		decoded, derr := decodeCapabilityLink(in.Link)
		if derr != nil {
			return Receipt{}, fmt.Errorf("%w: %v", ErrInvalidInput, derr)
		}
		capRaw = decoded
	}
	var cap peerCapability
	if err := json.Unmarshal(capRaw, &cap); err != nil {
		return Receipt{}, fmt.Errorf("%w: malformed capability", ErrInvalidInput)
	}
	if cap.ID == "" || strings.TrimSpace(cap.OwnerAddr) == "" {
		return Receipt{}, fmt.Errorf("%w: capability missing id/owner_addr", ErrInvalidInput)
	}
	// Defense in depth: a bound capability must name the recipient we hold.
	if cap.Recipient != "" && cap.Recipient != recipient {
		return Receipt{}, ErrInvalidInput
	}

	if s.puller == nil {
		return Receipt{}, fmt.Errorf("cloudhome: no peer-serve client configured; cannot pull shared document")
	}
	if s.stager == nil {
		return Receipt{}, fmt.Errorf("cloudhome: no Drive stager configured; refusing to drop received document")
	}

	// Sign the fetch proof with the recipient's cloud-home key (recipient binding)
	// and PULL the bytes from the sharer's serve endpoint.
	ts := s.now().Unix()
	proof, err := s.signRecord(ctx, r, fetchProofMessage(cap.ID, ts), "redeem")
	if err != nil {
		return Receipt{}, err
	}
	freq := PeerFetchRequest{
		Capability:  capRaw,
		RequesterID: recipient,
		Timestamp:   ts,
		Proof:       base64.RawURLEncoding.EncodeToString(proof),
	}
	rc, err := s.puller.Serve(ctx, cap.OwnerAddr, freq)
	if err != nil {
		return Receipt{}, fmt.Errorf("cloudhome: peer fetch: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return Receipt{}, fmt.Errorf("cloudhome: read pulled bytes: %w", err)
	}

	// ── CONTENT-BLIND GATE (wave 3, the fix) ───────────────────────────────────
	// The cell must only ever handle ciphertext it cannot read. Require the pulled
	// bytes to be a well-formed VSEAL1 sealed envelope; the cell parses only the
	// PUBLIC framing (contentseal has no Open/private-key path) and can compute
	// nothing about the plaintext. Anything that is not sealed — e.g. a sharer that
	// tried to serve plaintext — is REFUSED here and never staged (fail closed).
	if _, perr := contentseal.Parse(data); perr != nil {
		return Receipt{}, fmt.Errorf("%w: %v", ErrNotContentBlind, perr)
	}
	// RECIPIENT-TARGETING GATE (wave 7, closes F2): when a content-key lookup is
	// wired, require the seal to be WRAPPED TO the recipient's published content key,
	// so the recipient — not just "someone" — is the intended target. This is an
	// INTEGRITY gate (the confidentiality guarantee above holds regardless); it FAILS
	// CLOSED — a lookup error/outage or a recipient with no published key REFUSES the
	// stage rather than falling open to accept an untargeted seal.
	if s.contentKeys != nil {
		pub, kerr := s.contentKeys.ContentPubKey(ctx, r.AccountID)
		if kerr != nil {
			return Receipt{}, fmt.Errorf("%w: recipient content-key lookup failed (fail closed): %v", ErrNotContentBlind, kerr)
		}
		if pub == "" {
			return Receipt{}, fmt.Errorf("%w: recipient has no published content key (fail closed)", ErrNotContentBlind)
		}
		if !contentseal.VerifyTargets(data, pub) {
			return Receipt{}, fmt.Errorf("%w: seal is not addressed to the recipient's content key", ErrNotContentBlind)
		}
	}

	if err := s.stager.SaveReceivedToDrive(ctx, r.AccountID, ReceivedDoc{
		Name:         cap.Name,
		ContentType:  cap.ContentType,
		IsDir:        cap.IsDir,
		Bytes:        data,
		DocID:        cap.ID,
		SourceVulaID: cap.OwnerVulaID,
		SourceServer: cap.OwnerAddr,
		Sealed:       true,
	}); err != nil {
		return Receipt{}, fmt.Errorf("cloudhome: stage to drive: %w", err)
	}

	return Receipt{
		AccountID:       r.AccountID,
		RecipientVulaID: r.VulaID,
		DocID:           cap.ID,
		StagedAt:        s.now().UTC().Format(time.RFC3339),
	}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// keyedMutex — per-key serialisation for provisioning
// ─────────────────────────────────────────────────────────────────────────────

type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func newKeyedMutex() keyedMutex { return keyedMutex{m: map[string]*sync.Mutex{}} }

func (k *keyedMutex) lock(key string) func() {
	k.mu.Lock()
	mu, ok := k.m[key]
	if !ok {
		mu = &sync.Mutex{}
		k.m[key] = mu
	}
	k.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}
