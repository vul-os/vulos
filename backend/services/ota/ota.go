// Package ota implements the box-side OTA (over-the-air OS update) client.
//
// # Model
//
// Vulos publishes each OS release to a public channel, but Vulos the org
// operates no hosted release channel, so there is NO default endpoint: an
// operator opts in by setting VULOS_UPDATE_URL to their own channel. When it is
// unset, OS update checks are disabled (see ResolveChannelURL / Check) — the
// box never polls a service that may not exist. When a channel IS configured,
// every box periodically POLLS it for a new manifest, VERIFIES it against the
// baked release-signing trust chain, and reports the result. The box owner
// decides whether to download-and-stage a verified update from the
// Settings → OS Update screen (POST /api/os/update/stage, see
// cmd/server/routes_ota.go) — this client never does that on its own.
//
// # This package is VERIFY-ONLY and OPT-IN
//
//   - It never signs anything. It has no access to any private key — only the
//     baked root trust-anchor PUBLIC key (services/signing.LoadAnchor) and the
//     release-key public key it authorises via a root-signed certificate
//     (services/signing.ReleaseKeyFromCert).
//   - It never auto-applies. Check (the background poll) only fetches and
//     verifies metadata — it never downloads the OS image and never touches
//     the A/B slots. Stage is the only method that writes anything, and it is
//     only ever called in response to an explicit owner action.
//   - It is fail-closed. Any verification failure (bad cert, bad signature,
//     epoch/rollback rejection, malformed manifest, unreachable channel) is
//     reported as an error and the previously-known-good status is preserved;
//     nothing is ever staged from unverified data.
//
// # Relationship to services/osdist
//
// services/osdist already implements a full OS-distribution pipeline
// (OSDIST-01..05): its own manifest schema, a bare-anchor-key signature model
// (no release-cert layer), an S3-style mirror Source, and a background loop
// that AUTO-DOWNLOADS AND AUTO-STAGES updates by default (osdist.Updater,
// wired in cmd/server/main.go, gated only by VULOS_OS_AUTOUPDATE). That
// auto-stage-by-default behaviour is exactly what the founder's opt-in model
// (see package doc above) rules out, so this package does not build on
// osdist.Updater/osdist.Source/osdist.ParseAndVerify.
//
// It DOES reuse osdist.SlotManager: the A/B slot + boot-state.json bookkeeping
// is real, hardware-backed, physical machine state (the same file cmd/init
// reads and writes at boot), and there must only ever be one such manager
// instance-shape talking to a given boot-state.json. Two independent
// SlotManager values pointed at the same cache directory are safe to use
// concurrently (all mutation goes through atomic write-then-rename), so
// callers may construct a second osdist.SlotManager at the same cache path
// used by the existing osdist wiring without any coordination.
//
// # Verification chain
//
// Unlike osdist's bare-anchor model, this client uses the full two-level PKI
// documented in services/signing/releasecert.go:
//
//	offline ROOT key  →  root-signed RELEASE-key certificate  →  release key
//	signs the day-to-day manifest + image artifacts.
//
// Concretely, Check fetches three files from the channel URL:
//
//	release-cert.json  — root-signed cert authorising the release pubkey
//	                      (verified via signing.ReleaseKeyFromCert against the
//	                      baked anchor from signing.LoadAnchor).
//	stable.json         — the manifest (see Manifest below).
//	stable.json.sig     — detached Ed25519 signature over the manifest's
//	                      canonical bytes (signing.Canonical), verified with
//	                      the release pubkey the cert just authorised.
//
// Both the release cert's and the manifest's min_epoch are checked against the
// box's persistent rollback floor (signing.EpochStore.AcceptEpoch), and the
// floor is RAISED to the ROOT-SIGNED cert's min_epoch once that cert verifies
// (signing.EpochStore.RaiseFromReleaseCert).
//
// That raise is what makes revocation real. This package used to be check-only
// — "never calls RaiseTo/BumpFloor" — and so was every other caller: nothing in
// the tree raised the floor, so a device sat at floor 0 for life, every
// min_epoch >= 0 passed, and issuing a cert with a higher -min-epoch revoked
// nothing at all. Verify-only still means what it says about the SYSTEM (no
// image is downloaded, no A/B slot is touched, staging stays explicit); it does
// not extend to declining to remember which epochs the root has retired.
package ota

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"vulos/backend/services/osdist"
	"vulos/backend/services/signing"
)

// ─── Channel configuration ───────────────────────────────────────────────────

// ChannelURLEnvKey is the runtime override for the update channel base URL.
const ChannelURLEnvKey = "VULOS_UPDATE_URL"

// DefaultChannelURL is intentionally EMPTY: Vulos the org operates no hosted
// release channel, so there is no central endpoint to point at by default. An
// operator opts in via VULOS_UPDATE_URL (or ClientConfig.ChannelURL). When it
// is unset, update checks are disabled and no network request is made. The
// channel location is SOFT configuration only regardless — trust comes
// entirely from the release-cert/anchor signature chain below, never from the
// URL, so pointing a configured channel at a mirror or accelerator is safe.
const DefaultChannelURL = ""

const (
	manifestName    = "stable.json"
	manifestSigName = "stable.json.sig"
	releaseCertName = "release-cert.json"
)

// ResolveChannelURL applies the resolution order: an explicit value (e.g. from
// ClientConfig.ChannelURL), then VULOS_UPDATE_URL, then DefaultChannelURL.
// The result never has a trailing slash, so relative paths can always be
// joined with a single "/".
func ResolveChannelURL(explicit string) string {
	if v := strings.TrimRight(strings.TrimSpace(explicit), "/"); v != "" {
		return v
	}
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv(ChannelURLEnvKey)), "/"); v != "" {
		return v
	}
	return DefaultChannelURL
}

// ─── Sentinel errors (all fail-closed) ───────────────────────────────────────

var (
	// ErrUpdatesDisabled means no update channel is configured (VULOS_UPDATE_URL
	// unset and no explicit ClientConfig.ChannelURL). Update checks are disabled
	// and no network request is made — the honest degraded state when Vulos the
	// org operates no hosted release channel and the operator has set none.
	ErrUpdatesDisabled = errors.New("ota: update checks disabled (no channel configured — set VULOS_UPDATE_URL)")

	// ErrAnchorUnavailable means the baked root trust-anchor public key could
	// not be loaded. Without it nothing can be authenticated, so every check
	// fails closed rather than trusting an unauthenticated manifest.
	ErrAnchorUnavailable = errors.New("ota: trust anchor unavailable")

	// ErrCertInvalid means release-cert.json failed to validate against the
	// root anchor (bad signature, expired, malformed).
	ErrCertInvalid = errors.New("ota: release certificate invalid")

	// ErrManifestMalformed means stable.json could not be parsed or is
	// missing required fields.
	ErrManifestMalformed = errors.New("ota: malformed manifest")

	// ErrManifestBadSignature means stable.json.sig did not verify against
	// the release key the cert authorised.
	ErrManifestBadSignature = errors.New("ota: manifest signature invalid")

	// ErrEpochRejected means the manifest's min_epoch is below this box's
	// persistent rollback floor — a downgrade/rollback attempt.
	ErrEpochRejected = errors.New("ota: manifest epoch rejected (rollback floor)")

	// ErrAlreadyUpToDate is returned by Stage when the running version
	// already matches the verified manifest's latest version.
	ErrAlreadyUpToDate = errors.New("ota: already up to date")

	// ErrNoVerifiedManifest means Stage was called but no manifest has ever
	// verified successfully — there is nothing authenticated to stage.
	ErrNoVerifiedManifest = errors.New("ota: no verified manifest available")

	// ErrNotImageInstall is returned by Stage on any machine without real A/B
	// slots (no boot-state.json — dev machines, containers, non-image
	// installs). Staging is refused BEFORE any file is written; this can
	// never brick or half-write a box.
	ErrNotImageInstall = errors.New("ota: not an image install — staging unavailable")

	// ErrStagingNotConfigured means the Client was built without a
	// SlotManager, so Stage has nothing to write into.
	ErrStagingNotConfigured = errors.New("ota: staging not configured (no slot manager)")

	// ErrHashMismatch means the downloaded image's SHA-256 does not match the
	// verified manifest's roothash field — a poisoned/corrupted download.
	ErrHashMismatch = errors.New("ota: image hash mismatch")

	// ErrImageBadSignature means the downloaded image's detached .sig did not
	// verify against the release key.
	ErrImageBadSignature = errors.New("ota: image signature invalid")
)

// ─── Manifest schema ──────────────────────────────────────────────────────────

// Manifest is the in-memory form of the channel's stable.json.
//
// The first seven fields mirror osdist.StableManifest's on-disk schema
// exactly (same JSON field names) so a single stable.json can serve both
// verification models during any transition period. The last three fields are
// new and additive/omitempty: a manifest that omits them canonicalizes to
// byte-identical output to before, so existing signatures produced against
// the plain 7-field schema keep verifying unchanged. Once the publish side
// starts setting is_security/severity/notes and re-signing, this client
// authenticates them the same way as every other field — they are part of
// the same canonical(Manifest) that the release key signs, not
// out-of-band/unauthenticated metadata.
type Manifest struct {
	Channel    string    `json:"channel"`
	Latest     string    `json:"latest"`
	MinEpoch   int64     `json:"min_epoch"`
	RootHash   string    `json:"roothash"`
	Size       int64     `json:"size"`
	ReleasedAt time.Time `json:"released_at"`
	Path       string    `json:"path"`

	// IsSecurity flags a release as a security fix. Drives the priority
	// notification in cmd/server/routes_ota.go.
	IsSecurity bool `json:"is_security,omitempty"`

	// Severity is an optional human/operator-facing label ("critical",
	// "high", "medium", "low"). Informational only; IsSecurity is what gates
	// the notification.
	Severity string `json:"severity,omitempty"`

	// Notes is the release-notes text shown in the update UI.
	Notes string `json:"notes,omitempty"`
}

// canonical returns the deterministic signed byte range for m (see
// signing.Canonical / FORMAT.md).
func (m *Manifest) canonical() ([]byte, error) {
	b, err := signing.Canonical(m)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManifestMalformed, err)
	}
	return b, nil
}

// parseManifest unmarshals and structurally validates a Manifest. Required
// fields absent ⇒ ErrManifestMalformed, fail-closed.
func parseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManifestMalformed, err)
	}
	if m.Channel == "" || m.Latest == "" || m.RootHash == "" || m.Path == "" {
		return nil, fmt.Errorf("%w: missing required fields (channel/latest/roothash/path)", ErrManifestMalformed)
	}
	return &m, nil
}

// ─── UpdateStatus ─────────────────────────────────────────────────────────────

// UpdateStatus is the result of a verified channel check. It is what GET
// /api/os/update/status (cmd/server/routes_ota.go) returns as JSON.
type UpdateStatus struct {
	// Available is true only when a verified manifest's Latest differs from
	// CurrentVersion. Never set on unverified/failed data.
	Available bool `json:"available"`

	// CurrentVersion is the version this box is currently running.
	CurrentVersion string `json:"current_version"`

	// LatestVersion is the verified manifest's version, present only when
	// Available.
	LatestVersion string `json:"latest_version,omitempty"`

	// IsSecurity / Severity / Notes mirror the verified manifest's fields.
	IsSecurity bool   `json:"is_security,omitempty"`
	Severity   string `json:"severity,omitempty"`
	Notes      string `json:"notes,omitempty"`

	// PublishedAt is the verified manifest's released_at.
	PublishedAt *time.Time `json:"published_at,omitempty"`

	// ChannelURL is the resolved channel base URL this box is polling.
	ChannelURL string `json:"channel_url"`

	// CheckedAt is when this status was last (successfully or not) refreshed.
	CheckedAt *time.Time `json:"checked_at,omitempty"`

	// LastError is the error from the most recent failed check. Empty when
	// the last check succeeded. Available/LatestVersion/etc. from the prior
	// successful check are preserved across a failed check — a transient
	// network blip does not erase the last known-good state.
	LastError string `json:"last_error,omitempty"`
}

// ─── Client ───────────────────────────────────────────────────────────────────

// ClientConfig configures a Client.
type ClientConfig struct {
	// ChannelURL overrides the channel base URL. Empty ⇒ ResolveChannelURL's
	// env/default fallback.
	ChannelURL string

	// AnchorPath is the baked root trust-anchor path. Empty ⇒
	// signing.DefaultAnchorPath.
	AnchorPath string

	// EpochStore enforces the persistent rollback floor. Required — a Client
	// with no epoch store cannot verify-only, since there would be no
	// rollback defence at all.
	EpochStore *signing.EpochStore

	// SlotManager is the real A/B slot manager (reuse the SAME cache
	// directory osdist already uses, e.g. datadir.Join("os-cache") — see
	// package doc). Optional: a nil SlotManager means Check/poll/notify still
	// work, but Stage always fails with ErrStagingNotConfigured.
	SlotManager *osdist.SlotManager

	// RunningVersion is this box's current OS version (e.g. from
	// VULOS_OS_VERSION), used for the Available comparison.
	RunningVersion string

	// HTTPClient is the client used for channel fetches. Nil ⇒ a client with
	// a 30s timeout.
	HTTPClient *http.Client
}

// DefaultPollInterval is the default background check cadence.
const DefaultPollInterval = 4 * time.Hour

// Client polls, verifies, and (only on explicit request) stages OS updates.
// Safe for concurrent use.
type Client struct {
	cfg        ClientConfig
	channelURL string
	anchorPath string
	http       *http.Client

	mu                 sync.RWMutex
	status             UpdateStatus
	verifiedManifest   *Manifest
	verifiedReleaseKey ed25519.PublicKey

	pollMu   sync.Mutex
	pollStop chan struct{}
}

// NewClient constructs a Client. EpochStore is required; every other field
// has a safe default.
func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.EpochStore == nil {
		return nil, errors.New("ota: NewClient: EpochStore must not be nil")
	}
	anchorPath := cfg.AnchorPath
	if anchorPath == "" {
		anchorPath = signing.DefaultAnchorPath
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	channelURL := ResolveChannelURL(cfg.ChannelURL)

	return &Client{
		cfg:        cfg,
		channelURL: channelURL,
		anchorPath: anchorPath,
		http:       httpClient,
		status: UpdateStatus{
			CurrentVersion: cfg.RunningVersion,
			ChannelURL:     channelURL,
		},
	}, nil
}

// ChannelURL returns the resolved channel base URL this Client polls.
func (c *Client) ChannelURL() string { return c.channelURL }

// Enabled reports whether an update channel is configured. When false, update
// checks are disabled: Check returns ErrUpdatesDisabled and never touches the
// network, and callers should not bother starting the background poll loop.
func (c *Client) Enabled() bool { return c.channelURL != "" }

// ─── fetch ────────────────────────────────────────────────────────────────────

func (c *Client) fetch(ctx context.Context, relPath string) ([]byte, error) {
	url := c.channelURL + "/" + strings.TrimLeft(relPath, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("ota: build request %q: %w", url, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ota: GET %q: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ota: GET %q: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ota: read %q: %w", url, err)
	}
	return data, nil
}

// ─── Check ────────────────────────────────────────────────────────────────────

// Check performs one verify-only channel poll: it fetches release-cert.json,
// stable.json, and stable.json.sig, verifies the full cert→release-key→
// manifest-signature chain against the baked anchor, enforces the epoch
// rollback floor, and returns the resulting UpdateStatus.
//
// Check NEVER downloads the OS image and NEVER touches the A/B slots — it is
// pure metadata verification. Only Stage does that, and only on explicit
// request.
//
// Fail-closed: on any verification failure, Check returns a non-nil error and
// an UpdateStatus that preserves the last known-good Available/LatestVersion
// (if any) with LastError set — it never reports an update as available based
// on unverified data.
func (c *Client) Check(ctx context.Context) (UpdateStatus, error) {
	if c.channelURL == "" {
		// No channel configured — updates are disabled. Degrade honestly: no
		// network call, status carries the disabled reason via LastError.
		return c.recordFailure(ErrUpdatesDisabled)
	}
	anchorPub, err := signing.LoadAnchor(c.anchorPath)
	if err != nil {
		return c.recordFailure(fmt.Errorf("%w: %v", ErrAnchorUnavailable, err))
	}

	certData, err := c.fetch(ctx, releaseCertName)
	if err != nil {
		return c.recordFailure(fmt.Errorf("ota: fetch release cert: %w", err))
	}
	cert, err := signing.ParseReleaseCert(certData)
	if err != nil {
		return c.recordFailure(fmt.Errorf("%w: parse: %v", ErrCertInvalid, err))
	}
	releasePub, err := signing.ReleaseKeyFromCert(anchorPub, cert)
	if err != nil {
		return c.recordFailure(fmt.Errorf("%w: %v", ErrCertInvalid, err))
	}

	// ── Revocation gate (SIGNING.md § Minimum Trusted Epoch) ─────────────────
	// The cert's min_epoch is the ROOT's statement of which release keys are
	// still current. A cert below this box's floor is a retired one being
	// replayed — refuse it before its release key is used for anything. This
	// is the check that makes bumping -min-epoch actually revoke.
	if err := c.cfg.EpochStore.AcceptEpoch(cert.MinEpoch); err != nil {
		return c.recordFailure(fmt.Errorf("%w: release cert: %v", ErrEpochRejected, err))
	}

	// ── ...and the raise that gives the gate above something to enforce ──────
	// Until this call existed, nothing anywhere raised the floor: it stayed at
	// 0 for the life of the device, every min_epoch >= 0 passed, and raising
	// -min-epoch revoked nothing. The floor rises here, and only here in this
	// package, because a root signature over this cert is what authorises it —
	// see signing.RaiseFromReleaseCert for why the release-key-signed manifest
	// below must NOT be allowed to move it.
	//
	// This runs BEFORE the manifest is verified, on purpose. The authority for
	// the raise is the root signature on the cert alone; making it contingent
	// on a later, separately-signed artifact would let a hostile channel hold
	// the floor down by serving a good cert with a broken manifest, and then
	// replay the retired cert afterwards. It also means the manifest below is
	// checked against the RAISED floor, enforcing cmd/sign's rule that a
	// manifest's min_epoch is at least its cert's.
	//
	// Fail-closed: if the new floor cannot be persisted, this check fails.
	// Reporting an update as verified while the rollback defence silently did
	// not record is the failure mode this whole mechanism exists to prevent.
	if err := c.cfg.EpochStore.RaiseFromReleaseCert(anchorPub, cert); err != nil {
		return c.recordFailure(fmt.Errorf("%w: epoch floor: %v", ErrCertInvalid, err))
	}

	manifestData, err := c.fetch(ctx, manifestName)
	if err != nil {
		return c.recordFailure(fmt.Errorf("ota: fetch manifest: %w", err))
	}
	sigData, err := c.fetch(ctx, manifestSigName)
	if err != nil {
		return c.recordFailure(fmt.Errorf("ota: fetch manifest signature: %w", err))
	}
	detachedSig, err := signing.ParseSig(sigData)
	if err != nil {
		return c.recordFailure(fmt.Errorf("%w: parse .sig: %v", ErrManifestMalformed, err))
	}

	manifest, err := parseManifest(manifestData)
	if err != nil {
		return c.recordFailure(err)
	}
	canonical, err := manifest.canonical()
	if err != nil {
		return c.recordFailure(err)
	}
	if !signing.Verify(releasePub, canonical, detachedSig.SigBytes) {
		return c.recordFailure(ErrManifestBadSignature)
	}
	if err := c.cfg.EpochStore.AcceptEpoch(manifest.MinEpoch); err != nil {
		return c.recordFailure(fmt.Errorf("%w: %v", ErrEpochRejected, err))
	}

	// Everything verified. Build the status the owner sees.
	now := time.Now().UTC()
	status := UpdateStatus{
		CurrentVersion: c.cfg.RunningVersion,
		ChannelURL:     c.channelURL,
		CheckedAt:      &now,
		Available:      manifest.Latest != c.cfg.RunningVersion,
	}
	if status.Available {
		status.LatestVersion = manifest.Latest
		status.IsSecurity = manifest.IsSecurity
		status.Severity = manifest.Severity
		status.Notes = manifest.Notes
		if !manifest.ReleasedAt.IsZero() {
			t := manifest.ReleasedAt
			status.PublishedAt = &t
		}
	}

	c.mu.Lock()
	c.status = status
	c.verifiedManifest = manifest
	c.verifiedReleaseKey = releasePub
	c.mu.Unlock()

	return status, nil
}

// recordFailure updates the cached status's CheckedAt/LastError while
// preserving whatever Available/LatestVersion/etc. the last successful check
// established, and returns that snapshot alongside err.
func (c *Client) recordFailure(err error) (UpdateStatus, error) {
	now := time.Now().UTC()
	c.mu.Lock()
	c.status.LastError = err.Error()
	c.status.CheckedAt = &now
	snapshot := c.status
	c.mu.Unlock()
	return snapshot, err
}

// Status returns the most recently cached UpdateStatus without performing a
// network request. Populated by Check (directly, or via the background
// poll loop started by StartPolling).
func (c *Client) Status() UpdateStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

// ─── StageResult ──────────────────────────────────────────────────────────────

// StageResult is returned by a successful Stage call.
type StageResult struct {
	Staged  bool   `json:"staged"`
	Slot    string `json:"slot,omitempty"`
	Version string `json:"version,omitempty"`
	Message string `json:"message"`
}

// ─── Stage ────────────────────────────────────────────────────────────────────

// Stage downloads and verifies the update image, then writes it into the
// INACTIVE A/B slot and marks it pending — via osdist.SlotManager, the same
// slot logic cmd/init uses at boot. It never touches the active slot and it
// never reboots or flips which slot is active; that remains a separate,
// manual step (the founder's model has the user choose to upgrade — staging
// makes the verified image ready, applying/rebooting is outside this call).
//
// Stage always re-verifies via Check immediately before writing anything —
// it never trusts a cache that might be hours old. It is called ONLY in
// response to an explicit owner action (POST /api/os/update/stage); nothing
// in this package calls it automatically.
//
// Hardware-gated: on any machine without real A/B slots (no boot-state.json —
// dev machines, containers, non-image installs) SlotManager.Load fails
// BEFORE any write is attempted, and Stage returns ErrNotImageInstall. This
// can never brick or half-write a box.
func (c *Client) Stage(ctx context.Context) (StageResult, error) {
	if c.cfg.SlotManager == nil {
		return StageResult{}, ErrStagingNotConfigured
	}

	status, err := c.Check(ctx)
	if err != nil {
		return StageResult{}, fmt.Errorf("ota: stage: verification failed: %w", err)
	}
	if !status.Available {
		return StageResult{}, ErrAlreadyUpToDate
	}

	c.mu.RLock()
	manifest := c.verifiedManifest
	releasePub := c.verifiedReleaseKey
	c.mu.RUnlock()
	if manifest == nil || releasePub == nil {
		return StageResult{}, ErrNoVerifiedManifest
	}

	// ── Hardware gate ──────────────────────────────────────────────────────
	// Load reads boot-state.json, which only exists on a real image install
	// (written by the boot pipeline at first real boot). Its absence fails
	// closed here, before any download or write — no partial state possible.
	bs, err := c.cfg.SlotManager.Load()
	if err != nil {
		return StageResult{}, fmt.Errorf("%w: %v", ErrNotImageInstall, err)
	}

	inactive, err := c.cfg.SlotManager.InactiveSlot(bs)
	if err != nil {
		return StageResult{}, fmt.Errorf("ota: determine inactive slot: %w", err)
	}

	// ── Download + verify the image ────────────────────────────────────────
	imageData, err := c.fetch(ctx, manifest.Path)
	if err != nil {
		return StageResult{}, fmt.Errorf("ota: fetch image: %w", err)
	}

	sum := sha256.Sum256(imageData)
	gotHash := hex.EncodeToString(sum[:])
	wantHash := strings.ToLower(strings.TrimSpace(manifest.RootHash))
	if !strings.EqualFold(gotHash, wantHash) {
		return StageResult{}, fmt.Errorf("%w: got %s want %s", ErrHashMismatch, gotHash, wantHash)
	}

	imgSigData, err := c.fetch(ctx, manifest.Path+".sig")
	if err != nil {
		return StageResult{}, fmt.Errorf("ota: fetch image signature: %w", err)
	}
	imgSig, err := signing.ParseSig(imgSigData)
	if err != nil {
		return StageResult{}, fmt.Errorf("ota: parse image signature: %w", err)
	}
	if !signing.Verify(releasePub, imageData, imgSig.SigBytes) {
		return StageResult{}, ErrImageBadSignature
	}

	// ── Stage atomically into the inactive slot only ───────────────────────
	if err := c.cfg.SlotManager.StageInto(bs, inactive, "os-core.squashfs", bytes.NewReader(imageData)); err != nil {
		return StageResult{}, fmt.Errorf("ota: stage into slot %s: %w", inactive, err)
	}
	if _, err := c.cfg.SlotManager.SetPending(bs, inactive); err != nil {
		return StageResult{}, fmt.Errorf("ota: set pending slot %s: %w", inactive, err)
	}

	return StageResult{
		Staged:  true,
		Slot:    string(inactive),
		Version: manifest.Latest,
		Message: fmt.Sprintf("staged %s to slot %s — reboot to activate", manifest.Latest, inactive),
	}, nil
}

// ─── Background poll loop ─────────────────────────────────────────────────────

// StartPolling begins the background verify-only poll loop (idempotent — a
// second call while already running is a no-op). The first check runs
// immediately; subsequent checks run every interval (DefaultPollInterval when
// <= 0). onCheck, when non-nil, is invoked with the resulting UpdateStatus
// after every check (success or failure) — cmd/server/routes_ota.go uses this
// to fire the security-priority notification on a new-version transition.
//
// The loop only ever calls Check, never Stage — polling can never
// auto-download or auto-stage anything.
func (c *Client) StartPolling(ctx context.Context, interval time.Duration, onCheck func(UpdateStatus)) {
	c.pollMu.Lock()
	if c.pollStop != nil {
		c.pollMu.Unlock()
		return
	}
	stop := make(chan struct{})
	c.pollStop = stop
	c.pollMu.Unlock()

	if interval <= 0 {
		interval = DefaultPollInterval
	}
	go c.pollLoop(ctx, interval, onCheck, stop)
}

// StopPolling ends the background poll loop started by StartPolling.
func (c *Client) StopPolling() {
	c.pollMu.Lock()
	if c.pollStop != nil {
		close(c.pollStop)
		c.pollStop = nil
	}
	c.pollMu.Unlock()
}

func (c *Client) pollLoop(ctx context.Context, interval time.Duration, onCheck func(UpdateStatus), stop chan struct{}) {
	runOnce := func() {
		status, _ := c.Check(ctx) // error is already folded into status.LastError
		if onCheck != nil {
			onCheck(status)
		}
	}

	runOnce()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}
