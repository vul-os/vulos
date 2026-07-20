package appnet

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
	"vulos/backend/internal/datadir"
	"vulos/backend/services/env"
	"vulos/backend/services/packages"
	"vulos/backend/services/signing"
)

// Registry holds the catalog of vetted apps with versioned install recipes.
// The registry defines *what* can be installed and *how* — the AppStore handles
// the actual lifecycle (download, install, uninstall).
//
// Registry file format (registry.json):
//
//	{
//	  "apps": {
//	    "postgres": {
//	      "name": "PostgreSQL",
//	      "vetted": true,
//	      "description": "Relational database",
//	      "category": "database",
//	      "author": "PostgreSQL Global Development Group",
//	      "homepage": "https://postgresql.org",
//	      "versions": {
//	        "16": { "install": "apt-get install -y postgresql-16", ... },
//	        "15": { "install": "apt-get install -y postgresql-15", ... }
//	      }
//	    }
//	  }
//	}
type Registry struct {
	Apps map[string]*RegistryEntry `json:"apps"`
}

// RegistryEntry is a single app in the registry.
//
// # Registry signing trust model (REGISTRY-SIGN-01)
//
// Every entry is signed by the RELEASE key, which the offline ROOT key certifies.
// The root public key is the trust anchor baked into the image; the box chains
// through the root-signed release cert to get the key that entries must verify
// against.  See docs/KEY-CEREMONY.md and resolveRegistryTrust.
//
//	ROOT (offline)  →  /etc/vulos/trust-anchor.pub   (the anchor)
//	     │ signs
//	     ▼
//	RELEASE cert    →  /etc/vulos/release-cert.json
//	     │ signs
//	     ▼
//	each RegistryEntry.Signature
//
// The signature covers signing.Canonical of
// {"app_id": <id>, "entry": <entry-without-signature>} — the app ID is inside
// the signed bytes, so a signed entry cannot be relocated to another app slot
// (M4 re-key protection).  The ENTIRE entry is signed, including fields this
// struct does not model; see Extra.
//
// DEFAULT-CLOSED.  Every path that cannot prove authenticity refuses the install:
// no anchor, unreadable anchor, cert that does not chain, expired cert, missing
// signature, invalid signature.  There is no fall-open.
//
// VULOS_REGISTRY_INSECURE=1 skips verification, is loudly logged, and is REFUSED
// outright when VULOS_ENV=prod — which is also the default when VULOS_ENV is
// unset (services/env).  The repo's development keys are likewise refused in
// prod (signing.RefuseDevKeyInProd).
//
// The sha256 artifact integrity check (Checksum on VersionRecipe) is independent
// and always enforced for binary downloads.
//
// Downgrade protection: MinVersion (if non-empty) is enforced — requesting a
// version lower than MinVersion is refused.  Disabled entries are refused
// outright.
type RegistryEntry struct {
	Name        string                    `json:"name"`
	Vetted      bool                      `json:"vetted"` // true = reviewed and approved by Vulos team
	Type        string                    `json:"type"`   // "web" (serves HTTP), "desktop" (GUI app, streamed via WebRTC), or "service" (background daemon)
	Arch        []string                  `json:"arch"`   // supported architectures (e.g. ["amd64","arm64"]), empty = all
	Description string                    `json:"description"`
	Category    string                    `json:"category"`
	Author      string                    `json:"author"`
	Homepage    string                    `json:"homepage"`
	Icon        string                    `json:"icon"`     // unicode fallback icon
	IconURL     string                    `json:"icon_url"` // URL to download icon from
	Keywords    []string                  `json:"keywords"`
	License     string                    `json:"license"`
	MinVersion  string                    `json:"min_version,omitempty"` // downgrade floor — installing below this is refused
	Disabled    bool                      `json:"_disabled,omitempty"`   // true = app is administratively disabled; install is refused
	Versions    map[string]*VersionRecipe `json:"versions"`
	// Signature is a base64-encoded Ed25519 signature over signing.Canonical of
	// {"app_id": <id>, "entry": <entry-without-signature>}.
	// Required unless VULOS_REGISTRY_INSECURE=1 is set.
	Signature string `json:"signature,omitempty"`

	// Extra preserves registry fields this struct does not model (today:
	// "_note", "lane", "admin_only").  It is NOT optional bookkeeping — it is
	// load-bearing for the signature.  signablePayload signs the marshalled
	// entry, so any field dropped on unmarshal would be a field the publisher
	// signature does not cover, and an attacker could add or rewrite it in a
	// signed registry undetected.  Round-tripping the unmodelled keys verbatim
	// closes that gap and keeps SaveRegistry lossless.
	Extra map[string]json.RawMessage `json:"-"`
}

// knownEntryKeys / knownRecipeKeys are the JSON keys each struct models,
// derived by reflection so that adding a field can never leave it double-
// counted in Extra.
var (
	knownEntryKeys  = jsonKeySet(reflect.TypeOf(RegistryEntry{}))
	knownRecipeKeys = jsonKeySet(reflect.TypeOf(VersionRecipe{}))
)

// jsonKeySet returns the set of JSON object keys a struct type serialises to.
func jsonKeySet(t reflect.Type) map[string]struct{} {
	keys := make(map[string]struct{}, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		keys[name] = struct{}{}
	}
	return keys
}

// splitExtraJSON returns the top-level keys of data that known does not model.
func splitExtraJSON(data []byte, known map[string]struct{}) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	var extra map[string]json.RawMessage
	for k, v := range raw {
		if _, ok := known[k]; ok {
			continue
		}
		if extra == nil {
			extra = make(map[string]json.RawMessage, len(raw))
		}
		extra[k] = v
	}
	return extra, nil
}

// mergeExtraJSON re-serialises the modelled fields (base) with the unmodelled
// ones merged back in.  A modelled key always wins: an "extra" can never shadow
// a field the code actually reads.
func mergeExtraJSON(base []byte, extra map[string]json.RawMessage, known map[string]struct{}) ([]byte, error) {
	if len(extra) == 0 {
		return base, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(base, &obj); err != nil {
		return nil, err
	}
	for k, v := range extra {
		if _, modelled := known[k]; modelled {
			continue
		}
		obj[k] = v
	}
	return json.Marshal(obj)
}

// registryEntryAlias strips the custom JSON methods so the marshaller below can
// recurse into the plain struct without infinite recursion.
type registryEntryAlias RegistryEntry

// UnmarshalJSON decodes an entry, preserving any unmodelled fields in Extra.
func (e *RegistryEntry) UnmarshalJSON(data []byte) error {
	var base registryEntryAlias
	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}
	extra, err := splitExtraJSON(data, knownEntryKeys)
	if err != nil {
		return err
	}
	*e = RegistryEntry(base)
	e.Extra = extra
	return nil
}

// MarshalJSON re-encodes an entry, restoring any unmodelled fields from Extra
// so the bytes we sign (and save) are the bytes we loaded.
func (e RegistryEntry) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(registryEntryAlias(e))
	if err != nil {
		return nil, err
	}
	return mergeExtraJSON(base, e.Extra, knownEntryKeys)
}

// VersionRecipe defines how to install and run a specific version of an app.
type VersionRecipe struct {
	Install      string            `json:"install"`             // shell command to install (e.g., "apt-get install -y postgresql-16")
	FlatpakID    string            `json:"flatpak_id"`          // Flatpak app ID (e.g., "org.gimp.GIMP") — if set, install/run via Flatpak
	DownloadURL  string            `json:"download_url"`        // static install: URL to download a binary or tar.gz archive
	ArchiveStrip int               `json:"archive_strip"`       // static install: number of leading path components to strip when extracting (tar --strip-components)
	Command      string            `json:"command"`             // how to run it (e.g., "bin/postgres -D data/")
	Port         int               `json:"port"`                // default port the app listens on
	PostInstall  string            `json:"post_install"`        // one-time setup command (e.g., "bin/initdb -D data/")
	Deps         []string          `json:"deps"`                // additional OS package dependencies
	Env          map[string]string `json:"env"`                 // default environment variables
	Permissions  []string          `json:"permissions"`         // required permissions
	Checksum     string            `json:"checksum"`            // sha256 checksum of download (if applicable)
	Singleton    bool              `json:"singleton"`           // only one instance allowed
	AutoStart    bool              `json:"auto_start"`          // start on boot
	Disabled     bool              `json:"_disabled,omitempty"` // true = entry is administratively disabled; install is refused

	// Extra preserves recipe fields this struct does not model (today: "_note",
	// per-recipe "arch").  Same rationale as RegistryEntry.Extra: the publisher
	// signature covers the marshalled recipe, so anything dropped here would be
	// outside the signature.
	Extra map[string]json.RawMessage `json:"-"`
}

// versionRecipeAlias strips the custom JSON methods (see registryEntryAlias).
type versionRecipeAlias VersionRecipe

// UnmarshalJSON decodes a recipe, preserving any unmodelled fields in Extra.
func (v *VersionRecipe) UnmarshalJSON(data []byte) error {
	var base versionRecipeAlias
	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}
	extra, err := splitExtraJSON(data, knownRecipeKeys)
	if err != nil {
		return err
	}
	*v = VersionRecipe(base)
	v.Extra = extra
	return nil
}

// MarshalJSON re-encodes a recipe, restoring any unmodelled fields from Extra.
func (v VersionRecipe) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(versionRecipeAlias(v))
	if err != nil {
		return nil, err
	}
	return mergeExtraJSON(base, v.Extra, knownRecipeKeys)
}

// pipeToShellRe matches curl|bash and wget|bash patterns (pipe-to-shell install anti-pattern).
var pipeToShellRe = regexp.MustCompile(`(?i)\b(curl|wget)\b[^|]*\|\s*(ba)?sh\b`)

// shCPipeRe matches sh -c / bash -c invocations that contain a pipe (covers the sh -c "curl … | bash" shape).
var shCPipeRe = regexp.MustCompile(`(?i)^\s*(ba)?sh\s+-c\s+['"]?[^'"]*\|`)

// rejectPipeToShell returns an error if cmd contains a pipe-to-shell pattern.
// Pipe-to-shell recipes (curl|bash, wget|bash, sh -c "…|bash") allow arbitrary code
// execution from a remote URL with no integrity check and are unconditionally rejected.
func rejectPipeToShell(cmd string) error {
	if pipeToShellRe.MatchString(cmd) {
		return fmt.Errorf("recipe contains a pipe-to-shell pattern (curl|bash / wget|bash) which is a security risk: %q — use a pinned artifact download with a checksum instead", cmd)
	}
	if shCPipeRe.MatchString(cmd) {
		return fmt.Errorf("recipe contains a sh -c pipe pattern which is a security risk: %q — use a pinned artifact download with a checksum instead", cmd)
	}
	return nil
}

// binaryDownloadRe matches recipes that download a binary directly via curl/wget to a file
// (as opposed to piping to a shell or delegating to a package manager).
var binaryDownloadRe = regexp.MustCompile(`(?i)\b(curl|wget)\b`)

// requiresChecksum reports whether an install command downloads a binary directly and
// therefore requires a non-empty Checksum field for integrity verification.
// Package-manager installs (apt-get, pip, npm, gem) and Flatpak installs verify
// integrity through their own signing mechanisms and do not require the Checksum field.
func requiresChecksum(install string) bool {
	if install == "" {
		return false
	}
	// Package-manager keywords — these handle their own integrity.
	pkgMgrRe := regexp.MustCompile(`(?i)\b(apt-get|apt|pip[0-9]*|pip3|npm|yarn|gem|brew|dnf|yum|pacman)\b`)
	if pkgMgrRe.MatchString(install) && !binaryDownloadRe.MatchString(install) {
		return false
	}
	// Pure apt-get lines that also happen to pull a key via curl (e.g. Grafana/Syncthing)
	// only need package manager integrity, not a binary checksum.
	if pkgMgrRe.MatchString(install) {
		return false
	}
	// If the recipe downloads a binary directly (curl/wget writing to a file), checksum is required.
	return binaryDownloadRe.MatchString(install)
}

// validateRecipeSecurity checks a recipe for disallowed patterns and missing checksums.
// It must be called before executing any install or post-install commands.
func validateRecipeSecurity(recipe *VersionRecipe) error {
	if recipe.Disabled {
		return fmt.Errorf("this version entry is disabled and cannot be installed")
	}

	// Reject pipe-to-shell in install and post_install.
	if err := rejectPipeToShell(recipe.Install); err != nil {
		return err
	}
	if err := rejectPipeToShell(recipe.PostInstall); err != nil {
		return err
	}

	// Require checksum when the recipe downloads a binary directly.
	if requiresChecksum(recipe.Install) && strings.TrimSpace(recipe.Checksum) == "" {
		return fmt.Errorf("install recipe downloads a binary directly but has no checksum — " +
			"set a sha256 checksum in the registry entry before installing (SEC-H3)")
	}

	// SECAUDIT2 H1: the static-download path (recipe.DownloadURL set, Install
	// empty) fetches an archive/binary over the network and installs it. It
	// MUST have a sha256 checksum — requiresChecksum() only inspects
	// recipe.Install, so without this an unverified artifact would pass the
	// gate. registry.json is cluster-replicated trust data; never relax this.
	if strings.TrimSpace(recipe.DownloadURL) != "" && strings.TrimSpace(recipe.Checksum) == "" {
		return fmt.Errorf("static download recipe (download_url set) has no checksum — " +
			"set a sha256 checksum in the registry entry before installing (SECAUDIT2-H1)")
	}

	return nil
}

// LatestVersion returns the highest version string for an entry.
// Uses simple lexicographic sorting — versions should use sortable format (e.g., "16.3").
func (e *RegistryEntry) LatestVersion() string {
	if len(e.Versions) == 0 {
		return ""
	}
	versions := make([]string, 0, len(e.Versions))
	for v := range e.Versions {
		versions = append(versions, v)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(versions)))
	return versions[0]
}

// GetRecipe returns the recipe for a specific version, or nil.
func (e *RegistryEntry) GetRecipe(version string) *VersionRecipe {
	if version == "" || version == "latest" {
		version = e.LatestVersion()
	}
	return e.Versions[version]
}

// LoadRegistry reads a registry.json file.
func LoadRegistry(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Registry
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.Apps == nil {
		r.Apps = make(map[string]*RegistryEntry)
	}
	return &r, nil
}

// Environment variables that configure registry trust.
const (
	// envRegistryPubKey holds a trusted Ed25519 entry-verification public key
	// directly (base64-encoded raw 32-byte key), bypassing the release-cert
	// chain.  Intended for forks and private registries that sign with a single
	// key.  Consulted only when no pinned trust-anchor file is present.
	envRegistryPubKey = "VULOS_REGISTRY_PUBKEY"

	// envRegistryInsecure, when set to "1", skips signature verification
	// entirely.  DEV ONLY: refused outright when VULOS_ENV=prod (which is also
	// the default when VULOS_ENV is unset — see services/env).
	envRegistryInsecure = "VULOS_REGISTRY_INSECURE"

	// envTrustAnchor overrides the pinned trust-anchor file path.  Exists so a
	// container or test can point at a staged /etc/vulos equivalent.
	envTrustAnchor = "VULOS_TRUST_ANCHOR"

	// envReleaseCert overrides the root-signed release-cert file path.
	envReleaseCert = "VULOS_RELEASE_CERT"
)

// devKeyDirs are the repo-relative directories probed for the checked-in dev
// trust anchor + release cert when running from a source checkout, so that
// `make dev` and `go test` verify real signatures with no flags set.  This
// probe is skipped entirely in prod — see resolveRegistryTrust.
//
// It mirrors the webroot probe in cmd/server (./dist, ../dist, …): the same
// "walk up until the repo root shows up" idiom.
var devKeyDirs = []string{"keys", "../keys", "../../keys", "../../../keys", "../../../../keys"}

// parseEd25519PubKey decodes a base64-encoded raw Ed25519 public key (32 bytes).
func parseEd25519PubKey(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("not valid base64: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("decoded to %d bytes, expected %d (Ed25519 raw key)", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// isProdEnv resolves the active runtime environment via the shared env package.
// An unset VULOS_ENV means prod (env.Parse's default), so every escape hatch in
// this file is closed unless someone deliberately opts out of production.
func isProdEnv() (bool, error) {
	activeEnv, err := env.Parse("")
	if err != nil {
		// Unrecognised VULOS_ENV — treat as fatal rather than guessing.
		return true, fmt.Errorf("registry: %w", err)
	}
	return activeEnv.IsProd(), nil
}

// registryInsecureRequested reports whether VULOS_REGISTRY_INSECURE=1 is set,
// regardless of whether it is permitted.
func registryInsecureRequested() bool {
	return strings.TrimSpace(os.Getenv(envRegistryInsecure)) == "1"
}

// registryInsecureActive reports whether signature verification may be skipped.
//
// REGISTRY-SIGN-02: the insecure escape hatch is DEV-ONLY.  It is honoured only
// outside production; in prod it is refused (and resolveRegistryTrust turns the
// request into a hard, loud error before any install can start).
func registryInsecureActive() bool {
	if !registryInsecureRequested() {
		return false
	}
	prod, err := isProdEnv()
	if err != nil || prod {
		return false
	}
	return true
}

// findDevKeyFile returns the first existing devKeyDirs/<name>, or "".
func findDevKeyFile(name string) string {
	for _, dir := range devKeyDirs {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// registryTrust is the resolved answer to "which key must have signed each
// registry entry, and may verification be skipped at all?".
type registryTrust struct {
	// key is the Ed25519 public key every entry signature must verify against.
	// nil only when insecure is true.
	key ed25519.PublicKey

	// insecure is true when verification is deliberately skipped
	// (VULOS_REGISTRY_INSECURE=1, non-prod only).
	insecure bool

	// source describes where key came from, for logging.
	source string
}

// resolveRegistryTrust resolves the registry trust chain, fail-closed.
//
// Trust chain (REGISTRY-SIGN-01 / docs/KEY-CEREMONY.md):
//
//	offline ROOT key  →  trust anchor baked at /etc/vulos/trust-anchor.pub
//	     │ signs (offline, at ceremony time)
//	     ▼
//	RELEASE cert      →  /etc/vulos/release-cert.json
//	     │ signs (routinely)
//	     ▼
//	registry.json entry signatures
//
// Resolution order:
//
//  1. VULOS_REGISTRY_INSECURE=1 in prod → hard error.  The escape hatch is dev-only.
//  2. VULOS_REGISTRY_INSECURE=1 outside prod → verification skipped, loudly.
//  3. Trust anchor: $VULOS_TRUST_ANCHOR, else /etc/vulos/trust-anchor.pub.
//     Present-but-unreadable is a hard error — never fall through to a weaker source.
//  4. Else VULOS_REGISTRY_PUBKEY: a direct entry-verification key (no cert chain).
//  5. Else, non-prod only: the repo's checked-in dev anchor (keys/trust-anchor.pub).
//  6. A dev key resolved in prod → hard error (signing.RefuseDevKeyInProd).
//  7. With an anchor from a file, a release cert (if present) is validated against
//     it and its release key becomes the entry-verification key.  Without a cert,
//     the anchor verifies entries directly (single-key fork model).
//  8. No key at all → hard error.  Installs are refused.
func resolveRegistryTrust() (registryTrust, error) {
	prod, err := isProdEnv()
	if err != nil {
		return registryTrust{}, err
	}

	// 1. The insecure escape hatch is refused in production, loudly, before
	// anything else can happen.  Unset VULOS_ENV counts as prod.
	if prod && registryInsecureRequested() {
		log.Printf("[registry] REFUSED: %s=1 is set but VULOS_ENV=prod — "+
			"the insecure escape hatch is dev-only and is IGNORED here. Installs will be refused "+
			"until a real trust anchor is configured.", envRegistryInsecure)
		return registryTrust{}, fmt.Errorf(
			"registry: %s=1 is set in a production environment (VULOS_ENV=prod) — refusing to skip "+
				"publisher signature verification. Unset %s and install a trust anchor at %s "+
				"(see docs/KEY-CEREMONY.md)",
			envRegistryInsecure, envRegistryInsecure, signing.DefaultAnchorPath)
	}

	// 2. Insecure mode, outside production. Honoured here rather than only as a
	// fallback for "no anchor found", so the flag means what it says: with the
	// repo's dev anchor present, a developer testing an as-yet-unsigned entry
	// would otherwise find the flag silently doing nothing.
	//
	// This cannot weaken production: prod already returned an error above.
	if registryInsecureActive() {
		log.Printf("[registry] WARNING: *** SECURITY DISABLED *** %s=1 is set (VULOS_ENV is not prod). "+
			"Registry signature verification is SKIPPED. This MUST NOT be used in production.",
			envRegistryInsecure)
		return registryTrust{insecure: true, source: envRegistryInsecure + "=1"}, nil
	}

	anchor, anchorSrc, fromFile, err := loadRegistryAnchor(prod)
	if err != nil {
		return registryTrust{}, err
	}

	if anchor == nil {
		return registryTrust{}, fmt.Errorf(
			"registry: no trust anchor configured — install refused (REGISTRY-SIGN-01). "+
				"Bake a root public key at %s (see docs/KEY-CEREMONY.md), or set %s. "+
				"%s=1 skips verification but only outside production",
			signing.DefaultAnchorPath, envRegistryPubKey, envRegistryInsecure)
	}

	// The dev keys are derived from a published seed — anyone can forge with
	// them. They must never be trusted by a production box, whichever source
	// they arrived from.
	if err := signing.RefuseDevKeyInProd(anchor, prod); err != nil {
		return registryTrust{}, fmt.Errorf("registry: trust anchor from %s: %w", anchorSrc, err)
	}

	// A direct VULOS_REGISTRY_PUBKEY is the entry-verification key itself —
	// there is no cert to chain from.
	if !fromFile {
		return registryTrust{key: anchor, source: anchorSrc}, nil
	}

	cert, certSrc, found, err := loadRegistryReleaseCert(prod)
	if err != nil {
		return registryTrust{}, err
	}
	if !found {
		// Single-key model: the anchor signs entries directly. Legal, but it
		// means the root key has to come online to publish a registry — say so.
		log.Printf("[registry] trust anchor loaded from %s; no release cert present — "+
			"entries must be signed by the anchor key itself", anchorSrc)
		return registryTrust{key: anchor, source: anchorSrc}, nil
	}

	releaseKey, err := signing.ReleaseKeyFromCert(anchor, cert)
	if err != nil {
		return registryTrust{}, fmt.Errorf(
			"registry: release cert %s does not chain to the trust anchor %s: %w — install refused",
			certSrc, anchorSrc, err)
	}
	if err := signing.RefuseDevKeyInProd(releaseKey, prod); err != nil {
		return registryTrust{}, fmt.Errorf("registry: release key from %s: %w", certSrc, err)
	}

	log.Printf("[registry] trust anchor %s → release key %q (cert %s, expires %s)",
		anchorSrc, cert.KeyID, certSrc, cert.NotAfter)
	return registryTrust{key: releaseKey, source: fmt.Sprintf("release cert %s (key-id %s)", certSrc, cert.KeyID)}, nil
}

// loadRegistryAnchor resolves the root trust anchor.  fromFile reports whether
// it came from a pinned anchor file (and may therefore chain to a release cert)
// as opposed to a direct VULOS_REGISTRY_PUBKEY key.
//
// A key source that is present but unusable is a hard error: falling through to
// a weaker source on a malformed anchor is exactly the fail-open this whole
// mechanism exists to prevent.
func loadRegistryAnchor(prod bool) (key ed25519.PublicKey, source string, fromFile bool, err error) {
	// 1. Pinned anchor file (baked into the image at build time).
	pinned := strings.TrimSpace(os.Getenv(envTrustAnchor))
	path := pinned
	if path == "" {
		path = signing.DefaultAnchorPath
	}
	switch anchor, loadErr := signing.LoadAnchor(path); {
	case loadErr == nil:
		return anchor, path, true, nil
	case !errors.Is(loadErr, fs.ErrNotExist):
		return nil, "", false, fmt.Errorf(
			"registry: trust anchor %q is present but unusable: %w — fix or remove the file "+
				"before starting the service", path, loadErr)
	}

	// 2. Direct public key from the environment.
	if v := strings.TrimSpace(os.Getenv(envRegistryPubKey)); v != "" {
		anchor, parseErr := parseEd25519PubKey(v)
		if parseErr != nil {
			return nil, "", false, fmt.Errorf(
				"registry: %s is set but malformed: %w — correct the key or unset the variable",
				envRegistryPubKey, parseErr)
		}
		return anchor, envRegistryPubKey, false, nil
	}

	// 3. Dev fallback: the repo's checked-in anchor. Never consulted in prod,
	// and never when the operator named an explicit anchor path — an explicit
	// path is authoritative, so "not there" means "no anchor", not "look
	// somewhere weaker".
	if !prod && pinned == "" {
		if p := findDevKeyFile("trust-anchor.pub"); p != "" {
			anchor, loadErr := signing.LoadAnchor(p)
			if loadErr != nil {
				return nil, "", false, fmt.Errorf("registry: dev trust anchor %q is unusable: %w", p, loadErr)
			}
			return anchor, p, true, nil
		}
	}

	return nil, "", false, nil
}

// loadRegistryReleaseCert resolves the root-signed release certificate that
// authorises the key which signs registry entries.  A missing cert is not an
// error (the single-key model is legal); a malformed one is.
func loadRegistryReleaseCert(prod bool) (cert signing.ReleaseCert, source string, found bool, err error) {
	pinned := strings.TrimSpace(os.Getenv(envReleaseCert))
	path := pinned
	if path == "" {
		path = signing.DefaultReleaseCertPath
	}
	switch c, loadErr := signing.LoadReleaseCert(path); {
	case loadErr == nil:
		return c, path, true, nil
	case !errors.Is(loadErr, fs.ErrNotExist):
		return signing.ReleaseCert{}, "", false, fmt.Errorf(
			"registry: release cert %q is present but unusable: %w — fix or remove the file", path, loadErr)
	}

	if !prod && pinned == "" {
		if p := findDevKeyFile("release-cert.json"); p != "" {
			c, loadErr := signing.LoadReleaseCert(p)
			if loadErr != nil {
				return signing.ReleaseCert{}, "", false, fmt.Errorf("registry: dev release cert %q is unusable: %w", p, loadErr)
			}
			return c, p, true, nil
		}
	}

	return signing.ReleaseCert{}, "", false, nil
}

// TrustedKey resolves the Ed25519 public key that every registry entry
// signature must verify against.
//
// It returns (nil, nil) ONLY when verification is legitimately skipped
// (VULOS_REGISTRY_INSECURE=1 outside production).  Every other failure returns
// a non-nil error, and callers MUST refuse the install — there is no path that
// returns a nil key and a nil error in production.
func TrustedKey() (ed25519.PublicKey, error) {
	trust, err := resolveRegistryTrust()
	if err != nil {
		return nil, err
	}
	return trust.key, nil
}

// signablePayload returns the canonical bytes over which the Ed25519 signature
// is computed. It binds the appID to the entry so an entry cannot be re-keyed
// to a different app ID without invalidating the signature (C1/M4 fix).
// Uses signing.Canonical for deterministic, sort-stable JSON encoding.
func signablePayload(appID string, entry *RegistryEntry) ([]byte, error) {
	tmp := *entry // shallow copy — zero the Signature field
	tmp.Signature = ""
	payload := map[string]any{
		"app_id": appID,
		"entry":  &tmp,
	}
	return signing.Canonical(payload)
}

// SignEntry computes an Ed25519 signature over signing.Canonical of
// {"app_id": appID, "entry": <entry-without-signature>} and stores it in
// entry.Signature (base64-encoded).
//
// The appID parameter binds the signature to a specific app slot so a signed
// entry cannot be moved to a different app ID in the registry without the
// publisher re-signing it (M4 re-key protection).
func SignEntry(entry *RegistryEntry, appID string, privKey ed25519.PrivateKey) error {
	data, err := signablePayload(appID, entry)
	if err != nil {
		return fmt.Errorf("canonical-encode entry for signing: %w", err)
	}
	sig := ed25519.Sign(privKey, data)
	entry.Signature = base64.StdEncoding.EncodeToString(sig)
	return nil
}

// VerifyEntrySignature verifies the Ed25519 publisher signature on entry.
//
//   - pubKey nil + VULOS_REGISTRY_INSECURE=1: skip (returns nil, loudly logged).
//   - pubKey nil + INSECURE not permitted: fail closed (defence in depth —
//     resolveRegistryTrust already errors before reaching here).
//   - pubKey set + entry.Signature empty: error (fail-closed).
//   - pubKey set + signature invalid: error (fail-closed).
//   - pubKey set + signature over wrong appID: error (M4 re-key protection).
//
// Both the sha256 artifact Checksum and this signature are verified before
// any install proceeds — they are independent integrity/authenticity layers.
func VerifyEntrySignature(entry *RegistryEntry, appID string, pubKey ed25519.PublicKey) error {
	if pubKey == nil {
		// nil means either: (a) insecure mode is active and permitted (skip), or
		// (b) no trust anchor is configured (fail-closed). DEFAULT-CLOSED:
		// reject unless VULOS_REGISTRY_INSECURE=1 is set AND we are not in prod.
		if registryInsecureActive() {
			return nil // explicit insecure mode, non-prod — skip verification
		}
		return fmt.Errorf("registry: no trust anchor configured for entry %q and "+
			"%s is not usable here — install refused (C1/REGISTRY-SIGN-01). "+
			"configure a trust anchor (%s or %s) before installing apps",
			appID, envRegistryInsecure, envRegistryPubKey, signing.DefaultAnchorPath)
	}
	if entry.Signature == "" {
		return fmt.Errorf("registry entry %q has no publisher signature — "+
			"all entries must be signed by the trusted publisher (REGISTRY-SIGN-01)", appID)
	}
	sig, err := base64.StdEncoding.DecodeString(entry.Signature)
	if err != nil {
		return fmt.Errorf("registry entry %q signature is not valid base64: %w", appID, err)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("registry entry %q signature is %d bytes, expected %d (Ed25519)", appID, len(sig), ed25519.SignatureSize)
	}
	data, err := signablePayload(appID, entry)
	if err != nil {
		return fmt.Errorf("canonical-encode entry %q for verification: %w", appID, err)
	}
	if !ed25519.Verify(pubKey, data, sig) {
		return fmt.Errorf("registry entry %q signature verification failed — "+
			"entry may have been tampered with, re-keyed, or signed by a different key", appID)
	}
	return nil
}

// SaveRegistry writes a registry.json file.
//
// The output is lossless: RegistryEntry/VersionRecipe round-trip unmodelled
// fields through Extra, so re-signing the registry cannot silently drop a key
// that the previous signature covered.
func SaveRegistry(path string, r *Registry) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

// InstallFromRegistry installs an app from the registry into appsDir.
// It runs the install command, generates a validated app.json manifest,
// downloads the icon, and runs post_install if present.
func InstallFromRegistry(ctx context.Context, reg *Registry, appID, version, appsDir string) error {
	entry, ok := reg.Apps[appID]
	if !ok {
		return fmt.Errorf("app %q not found in registry", appID)
	}

	if version == "" || version == "latest" {
		version = entry.LatestVersion()
	}
	recipe, ok := entry.Versions[version]
	if !ok {
		return fmt.Errorf("version %q not found for app %q (available: %s)",
			version, appID, strings.Join(availableVersions(entry), ", "))
	}

	// REGISTRY-SIGN-01 (C1): verify the Ed25519 publisher signature before
	// touching the filesystem. DEFAULT-CLOSED — resolveRegistryTrust chains the
	// baked trust anchor to the root-signed release cert and errors on every
	// path that would otherwise fall open. The signature covers appID+entry via
	// signing.Canonical so entries cannot be re-keyed (M4).
	trustedKey, err := TrustedKey()
	if err != nil {
		return fmt.Errorf("registry entry %q: %w", appID, err)
	}
	if err := VerifyEntrySignature(entry, appID, trustedKey); err != nil {
		return fmt.Errorf("registry entry %q failed publisher signature check: %w", appID, err)
	}

	// Administratively disabled entries are refused outright. The flag lives on
	// the entry (not just the recipe) so a whole app can be pulled without
	// touching each version.
	if entry.Disabled {
		return fmt.Errorf("registry entry %q is administratively disabled (_disabled) — install refused", appID)
	}

	// Downgrade protection (M4): enforce per-app minimum version.
	if entry.MinVersion != "" && version < entry.MinVersion {
		return fmt.Errorf("registry: requested version %q for %q is below the minimum allowed version %q (downgrade protection)",
			version, appID, entry.MinVersion)
	}

	// Security gate: reject disabled entries, pipe-to-shell recipes, and
	// binary downloads with empty checksums before touching the filesystem.
	if err := validateRecipeSecurity(recipe); err != nil {
		return fmt.Errorf("security check failed for %s@%s: %w", appID, version, err)
	}

	appDir := filepath.Join(appsDir, appID)

	// Create strict directory structure
	for _, dir := range []string{"bin", "static", "data"} {
		os.MkdirAll(filepath.Join(appDir, dir), 0755)
	}

	// Flatpak install path
	if recipe.FlatpakID != "" {
		if err := FlatpakInstall(ctx, recipe.FlatpakID); err != nil {
			return fmt.Errorf("flatpak install %s: %w", recipe.FlatpakID, err)
		}
	} else if recipe.DownloadURL != "" {
		// Static (download) install path: download archive/binary, verify checksum, extract into app dir.
		if err := staticInstall(ctx, recipe, appDir); err != nil {
			return fmt.Errorf("static install %s: %w", appID, err)
		}
	} else {
		// Ensure apt cache is ready before installing
		if !packages.CacheReady() {
			log.Printf("[registry] apt cache empty, running apt-get update...")
			if err := exec.CommandContext(ctx, "apt-get", "update", "-qq").Run(); err != nil {
				return fmt.Errorf("apt-get update failed: %w (run 'Update Package Index' from Settings first)", err)
			}
		}

		// Run install command
		if recipe.Install != "" {
			log.Printf("[registry] installing %s@%s: %s", appID, version, recipe.Install)
			var stderrBuf bytes.Buffer
			cmd := exec.CommandContext(ctx, "sh", "-c", recipe.Install)
			cmd.Dir = appDir
			cmd.Stdout = os.Stdout
			cmd.Stderr = &stderrBuf
			home := os.Getenv("HOME")
			if home == "" {
				home = "/root"
			}
			cmd.Env = append(os.Environ(), fmt.Sprintf("APP_DIR=%s", appDir), "HOME="+home)
			if err := cmd.Run(); err != nil {
				errOutput := lastLines(stderrBuf.String(), 10)
				if errOutput != "" {
					return fmt.Errorf("install command failed: %w\n%s", err, errOutput)
				}
				return fmt.Errorf("install command failed: %w", err)
			}
		}
	}

	// Install additional deps
	if len(recipe.Deps) > 0 {
		packages.InstallDeps(ctx, recipe.Deps)
	}

	// Generate the app.json manifest
	appType := entry.Type
	if appType == "" {
		appType = "web"
	}
	appCommand := recipe.Command
	if recipe.FlatpakID != "" && appCommand == "" {
		appCommand = FlatpakRunCommand(recipe.FlatpakID)
	}
	manifest := &AppManifest{
		ID:          appID,
		Name:        entry.Name,
		Icon:        entry.Icon,
		IconPath:    "icon.svg",
		Description: entry.Description,
		Version:     version,
		Command:     appCommand,
		Port:        recipe.Port,
		Type:        appType,
		Category:    entry.Category,
		Keywords:    entry.Keywords,
		Env:         recipe.Env,
		Deps:        recipe.Deps,
		WorkDir:     appDir,
		AutoStart:   recipe.AutoStart,
		Singleton:   recipe.Singleton,
		Permissions: recipe.Permissions,
		Author:      entry.Author,
		License:     entry.License,
		Homepage:    entry.Homepage,
	}

	// Write manifest
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "app.json"), manifestData, 0644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	// Create a placeholder icon if none exists
	iconPath := filepath.Join(appDir, "icon.svg")
	if _, err := os.Stat(iconPath); os.IsNotExist(err) {
		placeholderIcon := generatePlaceholderIcon(entry.Name, entry.Icon)
		os.WriteFile(iconPath, []byte(placeholderIcon), 0644)
	}

	// Run post-install command
	if recipe.PostInstall != "" {
		log.Printf("[registry] post-install %s: %s", appID, recipe.PostInstall)
		var stderrBuf bytes.Buffer
		cmd := exec.CommandContext(ctx, "sh", "-c", recipe.PostInstall)
		cmd.Dir = appDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = &stderrBuf
		cmd.Env = append(os.Environ(),
			fmt.Sprintf("APP_DIR=%s", appDir),
			fmt.Sprintf("DATA_DIR=%s", filepath.Join(appDir, "data")),
		)
		if err := cmd.Run(); err != nil {
			errOutput := lastLines(stderrBuf.String(), 10)
			if errOutput != "" {
				log.Printf("[registry] post-install warning for %s: %v\n%s", appID, err, errOutput)
			} else {
				log.Printf("[registry] post-install warning for %s: %v", appID, err)
			}
		}
	}

	// Symlink data dir to user data directory
	userDataDir := datadir.Join("data", appID)
	appDataDir := filepath.Join(appDir, "data")
	if _, err := os.Stat(userDataDir); os.IsNotExist(err) {
		os.MkdirAll(filepath.Dir(userDataDir), 0755)
		// Only symlink if the data dir is empty (fresh install)
		entries, _ := os.ReadDir(appDataDir)
		if len(entries) == 0 {
			os.Remove(appDataDir)
			os.Symlink(userDataDir, appDataDir)
			os.MkdirAll(userDataDir, 0755)
		}
	}

	log.Printf("[registry] installed %s@%s → %s", appID, version, appDir)
	return nil
}

// RegistryList returns a flat list of all registry entries with their IDs,
// suitable for API responses.
type RegistryListEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`       // "web", "desktop", or "service"
	Arch        []string `json:"arch"`       // supported architectures, empty = all
	FlatpakID   string   `json:"flatpak_id"` // non-empty if installed via Flatpak
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Author      string   `json:"author"`
	Icon        string   `json:"icon"`
	Vetted      bool     `json:"vetted"`
	Versions    []string `json:"versions"`
	Latest      string   `json:"latest"`
	Installed   bool     `json:"installed"`
	Homepage    string   `json:"homepage"`
	License     string   `json:"license"`
	// Keywords surfaces RegistryEntry.Keywords to the App Hub list/search API
	// (GET /api/store/registry) — without this the registry's per-app keywords
	// data reaches neither the API response nor the frontend search box.
	Keywords []string `json:"keywords"`
}

// ListEntries returns a flat list of all registry apps, marking which are installed.
// For Flatpak apps, checks the live Flatpak state so removed apps disappear immediately.
func (r *Registry) ListEntries(appsDir string) []RegistryListEntry {
	flatpakApps := InstalledFlatpaks()
	var entries []RegistryListEntry
	for id, entry := range r.Apps {
		versions := availableVersions(entry)
		installed := false
		if _, err := os.Stat(filepath.Join(appsDir, id, "app.json")); err == nil {
			installed = true
		}
		// Sync: if this is a flatpak app, verify it's still actually installed
		if installed {
			if recipe := entry.GetRecipe(entry.LatestVersion()); recipe != nil && recipe.FlatpakID != "" {
				if !flatpakApps[recipe.FlatpakID] {
					// Flatpak was removed externally — clean up stale manifest
					installed = false
					os.RemoveAll(filepath.Join(appsDir, id))
				}
			}
		}
		appType := entry.Type
		if appType == "" {
			appType = "web" // default to web
		}
		// Get flatpak ID from latest recipe
		flatpakID := ""
		if recipe := entry.GetRecipe(entry.LatestVersion()); recipe != nil {
			flatpakID = recipe.FlatpakID
		}
		entries = append(entries, RegistryListEntry{
			ID:          id,
			Name:        entry.Name,
			Type:        appType,
			Arch:        entry.Arch,
			FlatpakID:   flatpakID,
			Description: entry.Description,
			Category:    entry.Category,
			Author:      entry.Author,
			Icon:        entry.Icon,
			Vetted:      entry.Vetted,
			Versions:    versions,
			Latest:      entry.LatestVersion(),
			Installed:   installed,
			Homepage:    entry.Homepage,
			License:     entry.License,
			Keywords:    entry.Keywords,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

// lastLines returns the last n non-empty lines from s, trimmed.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return ""
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func availableVersions(entry *RegistryEntry) []string {
	versions := make([]string, 0, len(entry.Versions))
	for v := range entry.Versions {
		versions = append(versions, v)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(versions)))
	return versions
}

// staticInstall downloads a binary or archive from recipe.DownloadURL, verifies its SHA-256
// checksum (when recipe.Checksum is set), and installs it into appDir.
//
//   - .tar.gz / .tgz archives: extracted with tar, stripping recipe.ArchiveStrip leading
//     path components. The caller is expected to set recipe.Command to a path relative to
//     appDir (e.g. "bin/server").
//   - Single binary: saved directly to bin/<basename>, made executable.
//
// The download is streamed so large files do not require buffering in memory.
func staticInstall(ctx context.Context, recipe *VersionRecipe, appDir string) error {
	url := recipe.DownloadURL
	log.Printf("[registry/static] downloading %s", url)

	client := &http.Client{Timeout: 10 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	// Buffer to a temp file so we can verify the checksum before extracting.
	tmpFile, err := os.CreateTemp(appDir, ".dl-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmpFile, h), resp.Body); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write download: %w", err)
	}
	tmpFile.Close()

	// Verify checksum when provided.
	if recipe.Checksum != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if got != strings.ToLower(recipe.Checksum) {
			return fmt.Errorf("checksum mismatch: expected %s, got %s", recipe.Checksum, got)
		}
		log.Printf("[registry/static] checksum OK (%s)", got[:12])
	}

	// Detect archive vs plain binary by URL extension.
	lowerURL := strings.ToLower(url)
	isArchive := strings.HasSuffix(lowerURL, ".tar.gz") ||
		strings.HasSuffix(lowerURL, ".tgz") ||
		strings.HasSuffix(lowerURL, ".tar.bz2") ||
		strings.HasSuffix(lowerURL, ".tar.xz")

	if isArchive {
		// SECAUDIT2 H1: pre-extraction traversal screen. `tar` does not block
		// `../` / absolute / symlink-escaping members. List members first and
		// refuse any that would escape appDir. Archive-format-agnostic so
		// strip-components and .bz2/.xz keep working; the artifact is already
		// checksum-verified above, this is defense-in-depth on its contents.
		listCmd := exec.CommandContext(ctx, "tar", "tf", tmpPath)
		listOut, lerr := listCmd.Output()
		if lerr != nil {
			return fmt.Errorf("list archive: %w", lerr)
		}
		for _, m := range strings.Split(string(listOut), "\n") {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			if strings.HasPrefix(m, "/") || strings.HasPrefix(m, "../") ||
				strings.Contains(m, "/../") || m == ".." {
				return fmt.Errorf("refusing archive: unsafe member %q (path traversal)", m)
			}
		}
		args := []string{"xf", tmpPath, "-C", appDir, "--no-same-owner"}
		if recipe.ArchiveStrip > 0 {
			args = append(args, fmt.Sprintf("--strip-components=%d", recipe.ArchiveStrip))
		}
		log.Printf("[registry/static] extracting archive into %s (strip=%d)", appDir, recipe.ArchiveStrip)
		cmd := exec.CommandContext(ctx, "tar", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("extract archive: %w\n%s", err, strings.TrimSpace(string(out)))
		}
	} else {
		// Plain binary: install to bin/<filename>.
		base := filepath.Base(strings.SplitN(url, "?", 2)[0])
		destPath := filepath.Join(appDir, "bin", base)
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return fmt.Errorf("create bin dir: %w", err)
		}
		if err := os.Rename(tmpPath, destPath); err != nil {
			// Rename may fail across mount points; fall back to copy.
			if err2 := copyFile(tmpPath, destPath); err2 != nil {
				return fmt.Errorf("install binary: %w", err2)
			}
		}
		if err := os.Chmod(destPath, 0755); err != nil {
			return fmt.Errorf("chmod binary: %w", err)
		}
		log.Printf("[registry/static] installed binary → %s", destPath)
	}

	return nil
}

// copyFile copies src to dst, creating dst if it does not exist.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// generatePlaceholderIcon creates a simple SVG icon with the app's unicode icon or first letter.
func generatePlaceholderIcon(name, icon string) string {
	display := icon
	if display == "" && name != "" {
		display = strings.ToUpper(name[:1])
	}
	if display == "" {
		display = "?"
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
  <rect width="64" height="64" rx="14" fill="#1a1a1a"/>
  <text x="32" y="40" text-anchor="middle" font-size="28" fill="#e5e5e5" font-family="system-ui">%s</text>
</svg>`, display)
}
