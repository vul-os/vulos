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
// # What Stage verifies, and what it used to
//
// Until this was corrected, Stage compared sha256(image) to manifest.RootHash
// and verified the image's detached .sig as a signature over the RAW IMAGE
// BYTES. Neither shape exists in this repository:
//
//	RootHash is a dm-verity root hash — `veritysetup format` over a SALTED
//	Merkle tree (build.sh VERITY-01, scripts/verity/gen-verity.sh) — and can
//	never equal the SHA-256 of the image it describes.
//
//	`cmd/sign sign-image` emits a RELEASE-key signature over
//	canonical(ImagePayload) = {path, roothash, size, min_epoch, released_at}.
//	No command anywhere signs raw image bytes with any key.
//
// So POST /api/os/update/stage — the only wired staging path in the product —
// was a WALL, not a hole: every staging attempt against a real signed release
// failed at the hash comparison, and would have failed at the signature check
// behind it. Nothing noticed because no test ever called Stage. The failure
// direction was safe; the feature simply did not exist.
//
// Stage now verifies what cmd/sign emits, in the order cmd/init and
// services/installer use at boot and at install:
//
//  1. Re-run Check (cert → release key → manifest), so nothing is staged from
//     a cache that may be hours old.
//  2. Reconstruct the ImagePayload STRAIGHT FROM THE MANIFEST BYTES the release
//     key signed — never re-encoded from the parsed manifest, because
//     round-tripping released_at through time.Time does not reproduce the
//     signer's bytes.
//  3. Verify manifest.Path + ".sig" as a RELEASE-key signature over
//     canonical(ImagePayload), and require the signed payload to NAME the
//     artifact being fetched (otherwise a valid signature over an older,
//     vulnerable image could be paired with a newer manifest).
//  4. BIND that signed description to the bytes that actually arrived: the
//     downloaded length must equal ImagePayload.Size, and `veritysetup verify`
//     must accept ImagePayload.RootHash for the downloaded image against the
//     hash tree published beside it.
//
// Step 4 is mandatory, not best-effort. An ImagePayload signature covers a
// NAME, a SIZE and a ROOT HASH; it does not cover the image. Verifying it
// alone proves someone with the release key once described an artifact — it
// says nothing about the bytes on the wire. The root hash is the only thing
// tying the two together, so a missing hash tree or an absent veritysetup
// returns ErrVerityUnavailable rather than degrading to the size check. A file
// of the right length is not authentication.
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
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	// cmd/verify is a library that happens to live under cmd/ ("Package verify
	// implements per-boot-stage signature verification"); cmd/init,
	// services/installer and services/osdist import it the same way. Importing
	// the ImagePayload definition rather than copying it is deliberate: an
	// independently maintained copy of the signing surface is how the two ends
	// came to disagree in the first place.
	"vulos/backend/cmd/verify"
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

	// ErrHashMismatch means the signed ImagePayload does not describe the bytes
	// that were downloaded — either the byte length differs from
	// ImagePayload.Size, or `veritysetup verify` rejects ImagePayload.RootHash
	// for the downloaded image, or the signed payload names a different
	// artifact than the one being fetched. The partial download is removed.
	ErrHashMismatch = errors.New("ota: signed image payload does not describe the downloaded image")

	// ErrImageBadSignature means the image's detached .sig did not verify as a
	// RELEASE-key signature over canonical(ImagePayload) — the shape
	// `cmd/sign sign-image` emits.
	ErrImageBadSignature = errors.New("ota: image signature invalid")

	// ErrVerityUnavailable means the downloaded image's dm-verity root hash
	// could not be checked at all — no hash tree published beside the image, or
	// no veritysetup on this box. Fail-closed: nothing is staged, because that
	// root hash is the only thing binding the release-key signature to the
	// bytes that arrived.
	ErrVerityUnavailable = errors.New("ota: cannot verify the image's dm-verity root hash — refusing to stage an unbound image")
)

// ─── Manifest schema ──────────────────────────────────────────────────────────

// Manifest is the in-memory form of the channel's stable.json.
//
// These ten fields ARE the signed surface, and they mirror
// osdist.StableManifest's on-disk schema exactly (same JSON field names), so a
// single stable.json serves both verification models.
//
// # is_security / severity / notes are signed now, and that is the whole point
//
// They used to exist here with `omitempty` while `cmd/sign sign-manifest` signed
// a SEVEN-field ManifestPayload with no flag for any of them. The moment a
// publisher set is_security, canonical(Manifest) would have grown a key the
// signature did not cover and NO manifest could ever have verified again. They
// were removed rather than ignored, because the alternative is worse than a
// mismatch: a field outside the signed surface is attacker-APPENDABLE to a
// legitimately signed stable.json without disturbing the signature, and `notes`
// is rendered to the box owner in Settings → OS Update.
//
// The consequence was that the security-update notification in
// cmd/server/routes_ota.go could never fire. Making it real meant extending the
// SIGNING surface, in the three places that must move together — cmd/sign's
// ManifestPayload plus its flags, osdist.StableManifest, and this struct — which
// is what has now happened. TestManifestSignedSurfaceMatchesSigner fails if any
// one of them moves alone.
//
// # Compatibility: additive `omitempty`, no format version
//
// signing.Canonical drops a zero value entirely, so a manifest that sets none of
// the three canonicalises to EXACTLY the seven-key bytes it did before. Every
// signature already issued keeps verifying, byte for byte — no versioned
// payload, no dual-verify path, no re-signing ceremony. A box meeting an
// old-shape manifest reads is_security=false: the honest report of a release
// that says nothing about severity.
//
// A `"format": 2` discriminator was considered and rejected. It would itself
// have to be inside the signed surface, which means every existing signature
// becomes invalid the day it is introduced, in exchange for nothing —
// `omitempty` already encodes "this publisher said nothing" unambiguously.
//
// The corollary that looks like a hole and is not: an attacker CAN append
// `"is_security": false` to a signed seven-key document and the signature still
// verifies, because it canonicalises away. Sound — the canonical form is what is
// authenticated, and every document sharing one has the same meaning. A value
// that CHANGES the meaning changes the canonical bytes and the signature fails,
// including flipping a signed is_security from true to false.
//
// Being signed is necessary but not sufficient: severity is a closed set and
// notes is bounded, printable and link-free (osdist.ValidateSecuritySurface),
// because a signature proves who wrote a string, not that it is safe to put in
// front of the box owner.
type Manifest struct {
	Channel    string    `json:"channel"`
	Latest     string    `json:"latest"`
	MinEpoch   int64     `json:"min_epoch"`
	RootHash   string    `json:"roothash"`
	Size       int64     `json:"size"`
	ReleasedAt time.Time `json:"released_at"`
	Path       string    `json:"path"`
	IsSecurity bool      `json:"is_security,omitempty"`
	Severity   string    `json:"severity,omitempty"`
	Notes      string    `json:"notes,omitempty"`
}

// manifestSigPayload is the EXACT surface `cmd/sign sign-manifest` signs
// (cmd/sign/pki.go:ManifestPayload). It is unmarshalled straight from the
// served stable.json bytes and never re-encoded from a parsed Manifest:
// released_at is a STRING here because round-tripping it through time.Time does
// not reproduce the signer's bytes (2026-05-20T09:00:00.000Z re-marshals
// without its milliseconds), and a signature is over bytes, not over meaning.
//
// It is declared here rather than imported because cmd/sign is package main.
// TestManifestSignedSurfaceMatchesSigner pins it against the field set.
//
// The three severity fields carry `omitempty` for the SAME reason the signer
// does: canonical() must reproduce the signer's bytes, and the signer omits
// them when they are unset. Without omitempty here, an old seven-key manifest
// would canonicalise to nine keys and every existing signature would fail.
type manifestSigPayload struct {
	Channel    string `json:"channel"`
	Latest     string `json:"latest"`
	MinEpoch   int64  `json:"min_epoch"`
	Path       string `json:"path"`
	ReleasedAt string `json:"released_at"`
	RootHash   string `json:"roothash"`
	Size       int64  `json:"size"`
	IsSecurity bool   `json:"is_security,omitempty"`
	Severity   string `json:"severity,omitempty"`
	Notes      string `json:"notes,omitempty"`
}

// signedManifestKeys is the complete set of stable.json keys covered by the
// release key's signature. Any other key in the document is unauthenticated —
// see the Manifest doc comment — so parseManifest refuses it.
var signedManifestKeys = map[string]struct{}{
	"channel": {}, "latest": {}, "min_epoch": {}, "path": {},
	"released_at": {}, "roothash": {}, "size": {},
	"is_security": {}, "severity": {}, "notes": {},
}

// canonicalSigned returns the deterministic signed byte range for the manifest
// document in data: canonical(ManifestPayload), reconstructed from the served
// bytes (see signing.Canonical / FORMAT.md).
func canonicalSigned(data []byte) ([]byte, error) {
	var p manifestSigPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManifestMalformed, err)
	}
	b, err := signing.Canonical(p)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManifestMalformed, err)
	}
	return b, nil
}

// parseManifest unmarshals and structurally validates a Manifest. Required
// fields absent, or any key present that the signature does not cover ⇒
// ErrManifestMalformed, fail-closed.
func parseManifest(data []byte) (*Manifest, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManifestMalformed, err)
	}
	for k := range raw {
		if _, ok := signedManifestKeys[k]; !ok {
			return nil, fmt.Errorf("%w: field %q is outside the signed manifest surface "+
				"(cmd/sign sign-manifest covers channel/latest/min_epoch/path/released_at/roothash/size"+
				"/is_security/severity/notes) — "+
				"refusing to read a field no signature covers", ErrManifestMalformed, k)
		}
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManifestMalformed, err)
	}
	if m.Channel == "" || m.Latest == "" || m.RootHash == "" || m.Path == "" {
		return nil, fmt.Errorf("%w: missing required fields (channel/latest/roothash/path)", ErrManifestMalformed)
	}
	// The severity surface is refused here, BEFORE the signature is checked, and
	// deliberately so: a valid signature over a 4 kB note carrying a link is
	// still a link in front of the box owner. This gate holds whoever signed it,
	// which is the point — the release key is the very thing that would be
	// compromised in the case it defends against. osdist owns the rule so the
	// signer and both verifiers cannot drift apart on it.
	if err := osdist.ValidateSecuritySurface(m.IsSecurity, m.Severity, m.Notes); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrManifestMalformed, err)
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

	// IsSecurity / Severity / Notes are the release-severity surface the
	// Settings → OS Update screen and the priority notification in
	// cmd/server/routes_ota.go consume.
	//
	// Every one of them is copied from a manifest field the release key SIGNED
	// and that osdist.ValidateSecuritySurface has already accepted — never from
	// a key the signature does not cover, which is what parseManifest's
	// signedManifestKeys check exists to prevent. Like LatestVersion they are
	// only set when an update is actually Available: a box already running the
	// latest version has nothing to be warned about.
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

	// ReleaseCertPath is the local, baked copy of the release certificate, used
	// only when the CHANNEL serves none at release-cert.json. Empty ⇒
	// signing.DefaultReleaseCertPath (/etc/vulos/release-cert.json), the same
	// file cmd/init reads, so a box and its updater trust one file rather than
	// two policies that can drift apart.
	//
	// Preferring the channel's copy is safe — a cert is inert until it
	// validates against the pinned anchor — and it is what lets a release key
	// be rotated without re-flashing every box.
	ReleaseCertPath string

	// VerityVerify proves that rootHash is the dm-verity root hash of the image
	// at imagePath, given the Merkle hash tree at hashtreePath. Nil ⇒
	// verityVerifyWithTool, which shells out to `veritysetup verify` — the same
	// primitive services/installer uses before a netboot install and the same
	// one the initramfs hook uses at boot. Injectable so the suite can exercise
	// the surrounding chain on hosts without cryptsetup (macOS has none);
	// production callers must leave it nil.
	VerityVerify func(ctx context.Context, imagePath, hashtreePath, rootHash string) error

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

	mu               sync.RWMutex
	status           UpdateStatus
	verifiedManifest *Manifest
	// verifiedManifestData is the RAW stable.json the release key signed. Stage
	// reconstructs the signed ImagePayload from these exact bytes rather than
	// re-encoding verifiedManifest — see the package doc, step 2.
	verifiedManifestData []byte
	verifiedReleaseKey   ed25519.PublicKey

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
	if cfg.ReleaseCertPath == "" {
		cfg.ReleaseCertPath = signing.DefaultReleaseCertPath
	}
	if cfg.VerityVerify == nil {
		cfg.VerityVerify = verityVerifyWithTool
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

// fetchStream opens one channel file for reading. The caller must Close the
// returned reader. Used for the OS image, which is hundreds of MiB and must
// never be held in memory.
func (c *Client) fetchStream(ctx context.Context, relPath string) (io.ReadCloser, error) {
	url := c.channelURL + "/" + strings.TrimLeft(relPath, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("ota: build request %q: %w", url, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ota: GET %q: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("ota: GET %q: HTTP %d", url, resp.StatusCode)
	}
	return resp.Body, nil
}

// fetch reads one whole channel file into memory. Every artifact this client
// consumes is small enough for that except the image itself, which
// fetchStream + Stage write straight to disk.
func (c *Client) fetch(ctx context.Context, relPath string) ([]byte, error) {
	rc, err := c.fetchStream(ctx, relPath)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("ota: read %q: %w", relPath, err)
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
		// Fall back to the baked seed copy — a channel that publishes no cert is
		// still verifiable by a box whose image shipped one. The cert is inert
		// until it validates against the pinned anchor below, so which copy it
		// came from decides nothing. With neither, there is no release key and
		// NOTHING on the channel can be verified: fail closed.
		local, lerr := os.ReadFile(c.cfg.ReleaseCertPath)
		if lerr != nil {
			return c.recordFailure(fmt.Errorf("%w: channel: %v; seed %s: %v",
				ErrCertInvalid, err, c.cfg.ReleaseCertPath, lerr))
		}
		certData = local
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
	// canonical(ManifestPayload) rebuilt from the SERVED BYTES, which is what
	// `cmd/sign sign-manifest` signed — not a re-encoding of the parsed struct.
	canonical, err := canonicalSigned(manifestData)
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
		// The release-severity surface, carried from the manifest the release key
		// signed and that parseManifest has already validated. This is what makes
		// the owner's priority notification (cmd/server/routes_ota.go) able to
		// fire at all; it could not before, because nothing signed these fields.
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
	c.verifiedManifestData = manifestData
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
	manifestData := c.verifiedManifestData
	releasePub := c.verifiedReleaseKey
	c.mu.RUnlock()
	if manifest == nil || releasePub == nil || len(manifestData) == 0 {
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
	// The image is written to a temp file inside the inactive slot's directory,
	// verified there, and only then handed to StageInto. Nothing reaches the
	// slot's real filename until every check below has passed, and the temp
	// file is removed on every failure path.
	if err := c.downloadAndVerify(ctx, manifest, manifestData, releasePub, bs, inactive); err != nil {
		return StageResult{}, err
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

// ─── download + verify + stage ────────────────────────────────────────────────

// downloadAndVerify downloads the image the manifest describes into the
// inactive slot's directory, verifies the release-key signature over the
// ImagePayload the manifest describes, BINDS that payload to the bytes that
// arrived (size + dm-verity root hash), and only then stages the file
// atomically under its real name.
//
// Any failure removes the partial download and stages nothing.
func (c *Client) downloadAndVerify(
	ctx context.Context,
	manifest *Manifest,
	manifestData []byte,
	releasePub ed25519.PublicKey,
	bs *osdist.BootState,
	slot osdist.Slot,
) error {
	// ── The ImagePayload the release key signed ────────────────────────────
	// Unmarshalled STRAIGHT FROM THE MANIFEST BYTES, not re-encoded from the
	// parsed Manifest: canonical() must reproduce the signer's bytes exactly.
	// osdist's equivalent then checks payload.Path against the path it is about
	// to fetch, because it derives that path from manifest.Latest and the two
	// can disagree. Here the fetch key IS manifest.Path — the same JSON key
	// payload.Path is read from — so such a check could never fail, and a guard
	// that cannot fail is worse than no guard. parseManifest has already
	// refused an empty roothash/path.
	var payload verify.ImagePayload
	if err := json.Unmarshal(manifestData, &payload); err != nil {
		return fmt.Errorf("%w: manifest does not describe an image payload: %v", ErrManifestMalformed, err)
	}

	slotDir, err := c.cfg.SlotManager.SlotDir(slot)
	if err != nil {
		return fmt.Errorf("ota: slot dir for %s: %w", slot, err)
	}

	// ── Download to a temp file beside the slot's final path ───────────────
	imageRC, err := c.fetchStream(ctx, manifest.Path)
	if err != nil {
		return fmt.Errorf("ota: fetch image: %w", err)
	}
	defer imageRC.Close()

	tmpFile, err := os.CreateTemp(slotDir, "os-core.squashfs.*.download")
	if err != nil {
		return fmt.Errorf("ota: create temp for image: %w", err)
	}
	tmpPath := tmpFile.Name()
	written, err := io.Copy(tmpFile, imageRC)
	if err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("ota: download image: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("ota: sync downloaded image: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("ota: close downloaded image: %w", err)
	}
	defer os.Remove(tmpPath)

	// ── The detached image signature ───────────────────────────────────────
	// A RELEASE-key signature over canonical(ImagePayload) — the shape
	// `cmd/sign sign-image` emits. It is NOT over the raw image bytes; no
	// command in this repository produces that.
	imgSigData, err := c.fetch(ctx, manifest.Path+".sig")
	if err != nil {
		return fmt.Errorf("ota: fetch image signature: %w", err)
	}
	imgSig, err := signing.ParseSig(imgSigData)
	if err != nil {
		return fmt.Errorf("ota: parse image signature: %w", err)
	}
	canonicalPayload, err := signing.Canonical(payload)
	if err != nil {
		return fmt.Errorf("ota: canonical image payload: %w", err)
	}
	if !signing.Verify(releasePub, canonicalPayload, imgSig.SigBytes) {
		return ErrImageBadSignature
	}

	// ── Bind the signed description to the downloaded bytes ────────────────
	// Size first: weak on its own but free, and it catches a truncated download
	// before the Merkle-tree walk would.
	if written != payload.Size {
		return fmt.Errorf("%w: signed size %d, downloaded %d bytes", ErrHashMismatch, payload.Size, written)
	}
	if err := c.bindByVerity(ctx, tmpPath, hashtreePathFor(manifest.Path), payload.RootHash, slotDir); err != nil {
		return err
	}

	// ── Stage atomically into the inactive slot only ───────────────────────
	// StageInto streams a reader into the slot directory using temp-then-rename,
	// so the verified temp file is reopened as the source. Streaming from the
	// file rather than a []byte keeps a ~600 MiB image off the heap.
	verified, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("ota: reopen verified image: %w", err)
	}
	stageErr := c.cfg.SlotManager.StageInto(bs, slot, "os-core.squashfs", verified)
	verified.Close()
	if stageErr != nil {
		return fmt.Errorf("ota: stage into slot %s: %w", slot, stageErr)
	}
	return nil
}

// hashtreePathFor returns the channel-relative path of the dm-verity Merkle
// hash tree published beside an image, matching the bucket layout
// osdist.VersionHashtreePath defines
// ("os/v08/os-core.squashfs" → "os/v08/os-core.hashtree").
func hashtreePathFor(imagePath string) string {
	return strings.TrimSuffix(imagePath, ".squashfs") + ".hashtree"
}

// bindByVerity is the step that makes the release-key signature mean something
// about the IMAGE rather than about a description of one.
//
// It fetches the Merkle hash tree published beside the image and asks
// veritysetup whether rootHash — the value the release key signed — is the root
// of that tree over these exact bytes.
//
// Fail-closed by construction: a missing hash tree or an absent veritysetup
// returns ErrVerityUnavailable rather than falling back to the size check that
// already ran. Size proves a file is the right length, not that it is the right
// file.
func (c *Client) bindByVerity(ctx context.Context, imagePath, hashtreeRelPath, rootHash, workDir string) error {
	hashtreeData, err := c.fetch(ctx, hashtreeRelPath)
	if err != nil {
		return fmt.Errorf("%w: no hash tree at %s: %v", ErrVerityUnavailable, hashtreeRelPath, err)
	}
	htFile, err := os.CreateTemp(workDir, "os-core.hashtree.*.download")
	if err != nil {
		return fmt.Errorf("ota: create temp for hash tree: %w", err)
	}
	htPath := htFile.Name()
	defer os.Remove(htPath)
	if _, err := htFile.Write(hashtreeData); err != nil {
		htFile.Close()
		return fmt.Errorf("ota: write hash tree: %w", err)
	}
	if err := htFile.Close(); err != nil {
		return fmt.Errorf("ota: close hash tree: %w", err)
	}

	// Whitespace matters: a garbled root hash handed to veritysetup is exactly
	// the failure mode that once panicked a kernel at boot (see build.sh
	// VERITY-01's note on reading the file, not stdout).
	return c.cfg.VerityVerify(ctx, imagePath, htPath, strings.ToLower(strings.TrimSpace(rootHash)))
}

// verityVerifyWithTool is the production VerityVerify: `veritysetup verify`
// re-reads every data block, rebuilds the Merkle tree, and asserts the stored
// root matches rootHash. This is the same primitive services/installer uses
// before a netboot install and the same one the initramfs hook uses at boot —
// not a stand-in for it.
func verityVerifyWithTool(ctx context.Context, imagePath, hashtreePath, rootHash string) error {
	bin, err := exec.LookPath("veritysetup")
	if err != nil {
		// build.sh VERITY-03 installs cryptsetup-bin into the rootfs and gates
		// on /sbin/veritysetup or /usr/sbin/veritysetup existing. Those are sbin
		// paths: if this process was started with a PATH that omits them, the
		// tool IS on the box and LookPath still fails — and after this change
		// that is the difference between an update that stages and one refused
		// with ErrVerityUnavailable. Check the two paths the build guarantees
		// before giving up.
		for _, p := range []string{"/sbin/veritysetup", "/usr/sbin/veritysetup"} {
			if info, serr := os.Stat(p); serr == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				bin, err = p, nil
				break
			}
		}
	}
	if err != nil {
		return fmt.Errorf("%w: veritysetup not available: %v", ErrVerityUnavailable, err)
	}
	out, err := exec.CommandContext(ctx, bin, "verify", imagePath, hashtreePath, rootHash).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: veritysetup verify rejected roothash %s: %v: %s",
			ErrHashMismatch, rootHash, err, strings.TrimSpace(string(out)))
	}
	return nil
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
