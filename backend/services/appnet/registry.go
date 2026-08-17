package appnet

import (
	"archive/zip"
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

// Artifact is one downloadable payload and the sha256 that must match it.
//
// The URL and its checksum live in the SAME object on purpose. The obvious
// alternative — letting `download_url` be either a string or an
// {arch: url} map while `checksum` stayed a scalar — can express a recipe that
// pins two different binaries to one hash. That is not a hypothetical mistake:
// it is the shape a curator reaches for first, and it would install an
// unverified artifact on every arch but one while looking pinned. A schema
// should not be able to say the wrong thing.
type Artifact struct {
	DownloadURL string `json:"download_url"`
	Checksum    string `json:"checksum"` // sha256, lower-case hex; MANDATORY (SECAUDIT2-H1)
}

// VersionRecipe defines how to install and run a specific version of an app.
type VersionRecipe struct {
	// Install USED TO BE a shell command run by the installer. It is now
	// REFUSED — see roadmap/INSTALL-METHODOLOGY.md. Two install vehicles remain
	// (Flatpak, and the Vulos-native per-arch `artifacts` map) and a shell
	// string is neither of them.
	//
	// The field is deliberately KEPT and deliberately REFUSED rather than
	// deleted. Deleting it would move `install` into the Extra passthrough map,
	// where it would round-trip through the publisher signature while being read
	// by nothing — which is exactly the per-recipe `arch` defect
	// (APP-RECIPE-STANDARD §1.1): a field that looks like it does something and
	// does not. A stale recipe must fail loudly, not quietly.
	//
	// What it cost while it worked: `code-server` shipped a FABRICATED sha256
	// piped into `|| true`, so `dpkg -i` ran whatever came back regardless. That
	// shape is only expressible in a shell string.
	Install   string `json:"install"`    // REFUSED (INSTALL-01) — see above
	FlatpakID string `json:"flatpak_id"` // Flatpak app ID (e.g., "org.gimp.GIMP") — if set, install/run via Flatpak
	// DownloadURL is the single-architecture download form, and it is now
	// REFUSED (DOWNLOAD-01) in favour of Artifacts. Same reasoning as Install:
	// kept so a stale recipe errors instead of being silently ignored.
	//
	// It is not a security hole — it carried a mandatory checksum — it is a
	// SHAPE hole: one URL cannot pin two architectures, so every entry using it
	// was implicitly amd64-only while claiming every arch the entry declared.
	// `artifacts` says the same thing with one entry per architecture and one
	// digest per artefact, and that is the only form the installer will run.
	DownloadURL string `json:"download_url"` // REFUSED (DOWNLOAD-01) — use Artifacts

	// Artifacts is the PER-ARCHITECTURE form of DownloadURL/Checksum, keyed by
	// Debian arch name ("amd64", "arm64") — the same spelling RegistryEntry.Arch
	// and arch.go already use, normalised through NormalizeArch on both sides so
	// a registry that says x86_64 still matches a box that says amd64.
	//
	// Why this exists: Vulos ships amd64 AND arm64 images, and a VersionRecipe
	// carried a single DownloadURL, so a download-based app had to pick one
	// architecture. Three first-party apps (diwan, wede, lilmail) publish
	// working binaries for BOTH and were nonetheless offered on one — telling an
	// arm64 owner the OS's own office suite was unavailable while the binary sat
	// in the release. It also forked the app set by architecture, which the
	// everything-syncs directive does not allow: an instance must be a near
	// clone of its siblings, and an app that cannot follow a user onto their
	// arm64 box breaks that.
	//
	// Set EITHER DownloadURL or Artifacts, never both — validateRecipeSecurity
	// refuses a recipe that sets both rather than picking a winner, because
	// which one wins is exactly the kind of thing a reader would guess wrong.
	// Artifacts is `omitempty`, so all 55 pre-existing entries serialise
	// byte-identically and their publisher signatures are untouched by this
	// schema change.
	//
	// There is deliberately NO fallback from Artifacts to DownloadURL when the
	// box's arch is missing from the map. Falling back would hand an arm64 box
	// an amd64 binary that passes its checksum and then fails to exec — a
	// successful install of an app that cannot run, which is the defect class
	// this whole change exists to remove. A missing arch is a loud refusal.
	Artifacts map[string]*Artifact `json:"artifacts,omitempty"`

	ArchiveStrip int `json:"archive_strip"` // static install: number of leading path components to strip when extracting (tar --strip-components)

	// ExtractDir is the subdirectory OF THE APP DIR an archive unpacks into.
	// Empty means the app dir itself, which is the pre-existing behaviour, so
	// every shipped entry is unaffected.
	//
	// It exists because removing the shell left one job with no declarative
	// expression. A static web app is served with
	// `python3 -m http.server ${PORT} --directory static/`, and every such
	// recipe used to get its bytes into static/ by running `unzip -d static/` or
	// `tar -C static/` in the install shell. Unpacking into the app dir instead
	// and serving `.` is NOT an equivalent substitute: the app dir also holds
	// data/, which is a symlink to the owner's data directory, so the one-line
	// "just serve the app dir" version publishes the app's own database over
	// HTTP. A field is cheaper than that mistake.
	//
	// It is a plain relative path inside the app dir — validateRecipeSecurity
	// refuses absolute paths and traversal, and staticInstall re-checks the
	// resolved destination, because this value becomes a filesystem path.
	// Setting it alongside a non-archive artefact is refused rather than
	// ignored, for the same reason binary_name is.
	ExtractDir string `json:"extract_dir,omitempty"`

	// BinaryName renames a single-binary download as it lands in bin/.
	//
	// Without it, a plain binary is installed to bin/<basename of the URL>, so
	// per-arch artifacts land under DIFFERENT names — bin/diwan-linux-amd64 on
	// one box, bin/diwan-linux-arm64 on another — while `command` is one string
	// shared by every architecture. No command could then name the binary on
	// both. Setting binary_name to "diwan" makes the installed path arch-
	// independent and lets `command` stay a single literal.
	//
	// The alternative was a ${ARCH} substitution inside `command`, expanded at
	// launch. That spreads architecture spelling into free-text recipe strings
	// and puts the fix in the launcher; this keeps it in the installer, where
	// the arch is already known and already enforced.
	//
	// It applies ONLY to the single-binary branch. Setting it alongside an
	// archive URL is refused by validateRecipeSecurity rather than ignored:
	// per-recipe `arch` was already dead data nobody read (APP-RECIPE-STANDARD
	// §1.1), and adding a second field that silently does nothing would repeat
	// exactly that.
	BinaryName string `json:"binary_name,omitempty"`

	Command     string            `json:"command"`             // how to run it (e.g., "bin/postgres -D data/")
	Port        int               `json:"port"`                // default port the app listens on
	PostInstall string            `json:"post_install"`        // one-time setup command (e.g., "bin/initdb -D data/")
	Deps        []string          `json:"deps"`                // additional OS package dependencies
	Env         map[string]string `json:"env"`                 // default environment variables
	Permissions []string          `json:"permissions"`         // required permissions
	Checksum    string            `json:"checksum"`            // sha256 checksum of download (if applicable)
	Singleton   bool              `json:"singleton"`           // only one instance allowed
	AutoStart   bool              `json:"auto_start"`          // start on boot
	Disabled    bool              `json:"_disabled,omitempty"` // true = entry is administratively disabled; install is refused

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

// tarExtensions are the formats handed to the `tar` binary. Zip is NOT among
// them and must never be added.
//
// GNU tar — what the shipped Debian image has — cannot read a zip at all. BSD
// tar (libarchive), which is what a developer Mac has, cannot only read one but
// will happily extract it. So routing .zip to the tar branch works on the
// machine the tests run on and fails on every box a user owns. That is the
// host-is-not-the-target trap, and it was caught here by a mutation that
// disabled the zip branch and survived: the fallthrough silently did the job on
// macOS. Zip is dispatched to extractZip, in-process, on every platform.
var tarExtensions = []string{".tar.gz", ".tgz", ".tar.bz2", ".tar.xz"}

// isTarURL reports whether a download URL names an archive for the tar branch.
// The query string is stripped first so a signed/CDN URL is still classified.
func isTarURL(url string) bool {
	lower := strings.ToLower(strings.SplitN(url, "?", 2)[0])
	for _, ext := range tarExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// isArchiveURL reports whether a URL names anything staticInstall unpacks
// rather than installing as a single binary. Used by validateRecipeSecurity to
// refuse a binary_name that would silently do nothing.
func isArchiveURL(url string) bool { return isTarURL(url) || isZipURL(url) }

// ArchAny is the artifacts key for a payload that is genuinely the same bytes on
// every architecture — a static web bundle, a WAR, a source archive.
//
// It exists because the obvious encoding is ambiguous in a dangerous way.
// Listing one URL and one digest under BOTH "amd64" and "arm64" is
// indistinguishable, from the data alone, from the real defect: a curator
// pinning the amd64 asset under both keys, so an arm64 box downloads an x86-64
// binary, passes its checksum, installs cleanly and cannot exec. That is not
// hypothetical — kerf v0.1.9 published three "per-platform" tarballs that were
// one identical file, and the entry looked plausible while being unusable.
// `scripts/verify-firstparty-artifacts.sh` refuses two architectures sharing one
// sha256 for exactly that reason, and it is right to.
//
// So the recipe says which it means, rather than a checker guessing. "any" is a
// CLAIM by the curator that the payload carries no machine code, and it is
// exclusive: combining it with a real architecture key is refused, because that
// would be a fallback, and a fallback is how an arm64 box ends up with an amd64
// binary in the first place.
const ArchAny = "any"

// HasArtifacts reports whether this recipe uses the per-architecture form.
func (v *VersionRecipe) HasArtifacts() bool { return len(v.Artifacts) > 0 }

// artifactAny returns the architecture-independent artefact, or nil.
func (v *VersionRecipe) artifactAny() *Artifact {
	for k, a := range v.Artifacts {
		if strings.ToLower(strings.TrimSpace(k)) == ArchAny {
			return a
		}
	}
	return nil
}

// ResolveArtifact returns the download URL and checksum this recipe installs on
// a box of architecture boxArch (Debian spelling; normalised here, so either
// spelling works on either side).
//
// Single-arch recipes (DownloadURL set) resolve to themselves regardless of
// boxArch — the entry-level `arch` gate in InstallFromRegistry is what keeps
// them off boxes they do not fit, and 55 shipped entries depend on that
// behaviour being unchanged.
//
// Per-arch recipes resolve strictly: no match is an error naming the box's arch
// and every arch the recipe does offer. It never falls back to DownloadURL.
func (v *VersionRecipe) ResolveArtifact(boxArch string) (url, checksum string, err error) {
	if !v.HasArtifacts() {
		return v.DownloadURL, v.Checksum, nil
	}
	// An architecture-independent payload resolves to itself on every box. This
	// is not a fallback: validateRecipeSecurity refuses "any" alongside a real
	// architecture key, so reaching here means the recipe pins exactly one
	// artefact and claims it carries no machine code.
	if a := v.artifactAny(); a != nil {
		return a.DownloadURL, a.Checksum, nil
	}
	want := NormalizeArch(boxArch)
	offered := make([]string, 0, len(v.Artifacts))
	for k, a := range v.Artifacts {
		n := NormalizeArch(k)
		offered = append(offered, n)
		if n == want && a != nil {
			return a.DownloadURL, a.Checksum, nil
		}
	}
	sort.Strings(offered)
	return "", "", fmt.Errorf("recipe publishes no artifact for this box's architecture %q (it offers: %s)",
		want, strings.Join(offered, ", "))
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

	// ── INSTALL-01: there is no shell install path ───────────────────────────
	//
	// Ordered AFTER the two checks above on purpose. Both of those still fire
	// first for the inputs they were written for (a pipe-to-shell string, a
	// curl-without-checksum string), so neither becomes a guard that can never
	// run — the failure this codebase keeps finding. This one catches what is
	// left: the benign-looking `apt-get install -y blender`.
	//
	// Why the whole shape goes rather than being tightened: an install shell is
	// unreviewable in general. `code-server`'s recipe verified a checksum and
	// then piped the result into `|| true`, so `dpkg -i` ran regardless — and
	// the checksum it "verified" was invented. No pattern list catches that;
	// only removing the ability to express it does.
	if s := strings.TrimSpace(recipe.Install); s != "" {
		return fmt.Errorf("recipe carries an `install` shell command, which this installer no longer runs "+
			"(INSTALL-01, roadmap/INSTALL-METHODOLOGY.md): %q — express the app as a Flatpak "+
			"(`flatpak_id`) or as per-architecture pinned artefacts (`artifacts`)", firstLine(s))
	}

	// ── POSTINSTALL-02: post_install configures, it does not install ─────────
	//
	// post_install survives because three verified first-party entries (diwan,
	// wede, lilmail) mint their config and their secrets there, and removing it
	// would break installs that are proved to work. It survives NARROWED: it may
	// only arrange bytes that are already on disk.
	//
	// Without this rule "we removed the install shell" would be false — the same
	// `wget … && tar …` line simply moves one field down. `uptime-kuma`'s
	// post_install is literally `npm install --production 2>/dev/null || true`.
	if err := rejectNetworkFetch(recipe.PostInstall); err != nil {
		return err
	}

	// ── POSTINSTALL-03: a step that cannot fail is not a step ────────────────
	//
	// POSTINSTALL-01 made a failed post_install fatal. `|| true` re-opens that
	// from inside the string: the command fails, sh exits 0, the installer
	// reports success and the app is unconfigured. This is the exact shape that
	// let code-server's forged checksum through, and the shape that made kerf's
	// install "succeed" with a placeholder page.
	if err := rejectSwallowedFailure(recipe.PostInstall); err != nil {
		return err
	}

	// ARTIFACTS-01: the per-architecture download form carries exactly the same
	// obligations as the single-URL one, and two more of its own.
	if recipe.HasArtifacts() {
		// Ambiguity is refused rather than resolved. If both forms were set,
		// whichever one the engine happened to prefer would be a coin-flip to
		// every reader of the registry, and a half-migrated entry would install
		// the stale artifact while looking migrated.
		if strings.TrimSpace(recipe.DownloadURL) != "" {
			return fmt.Errorf("recipe sets BOTH download_url and artifacts — " +
				"use exactly one: artifacts for a per-architecture app, download_url for a single-arch one (ARTIFACTS-01)")
		}
		if strings.TrimSpace(recipe.Checksum) != "" {
			return fmt.Errorf("recipe sets a top-level checksum alongside artifacts — " +
				"each artifact carries its own checksum; a shared one cannot be right for two different binaries (ARTIFACTS-01)")
		}
		// ARTIFACTS-02: "any" is exclusive. Combining it with a real
		// architecture would make it a FALLBACK, and a fallback is precisely how
		// an arm64 box ends up executing an amd64 binary that passed its own
		// checksum — the defect the strict resolver exists to remove.
		if recipe.artifactAny() != nil && len(recipe.Artifacts) > 1 {
			offered := make([]string, 0, len(recipe.Artifacts))
			for k := range recipe.Artifacts {
				offered = append(offered, k)
			}
			sort.Strings(offered)
			return fmt.Errorf("artifacts sets %q alongside per-architecture keys (%s) — "+
				"%q means one payload serves every architecture and is exclusive; two forms "+
				"would make one of them a silent fallback (ARTIFACTS-02)",
				ArchAny, strings.Join(offered, ", "), ArchAny)
		}
		seen := make(map[string]string, len(recipe.Artifacts))
		for arch, a := range recipe.Artifacts {
			if a == nil {
				return fmt.Errorf("artifacts[%q] is null — remove the key or give it a download_url and checksum (ARTIFACTS-01)", arch)
			}
			norm := NormalizeArch(arch)
			if norm == "" {
				return fmt.Errorf("artifacts has an empty architecture key (ARTIFACTS-01)")
			}
			// Two spellings of one arch ("amd64" and "x86_64") would make which
			// artifact wins depend on Go's randomised map iteration order.
			if prev, dup := seen[norm]; dup {
				return fmt.Errorf("artifacts names architecture %q twice (as %q and %q) — "+
					"one architecture, one artifact (ARTIFACTS-01)", norm, prev, arch)
			}
			seen[norm] = arch
			if strings.TrimSpace(a.DownloadURL) == "" {
				return fmt.Errorf("artifacts[%q] has no download_url (ARTIFACTS-01)", arch)
			}
			// Same rule as SECAUDIT2-H1, applied per artifact: an unchecksummed
			// artifact is an unverified binary no matter which key it hides under.
			if strings.TrimSpace(a.Checksum) == "" {
				return fmt.Errorf("artifacts[%q] has no checksum — "+
					"set a sha256 checksum before installing (SECAUDIT2-H1)", arch)
			}
		}
	}

	// A binary_name on an archive recipe would be silently ignored. Per-recipe
	// `arch` was already dead data read by nothing (APP-RECIPE-STANDARD §1.1);
	// refusing here keeps a second field from joining it.
	if strings.TrimSpace(recipe.BinaryName) != "" {
		if u := strings.TrimSpace(recipe.DownloadURL); u != "" && isArchiveURL(u) {
			return fmt.Errorf("recipe sets binary_name %q but download_url is an archive — "+
				"binary_name renames a single downloaded binary and does nothing for an archive; "+
				"use archive_strip and a command naming the extracted path (ARTIFACTS-01)", recipe.BinaryName)
		}
		for arch, a := range recipe.Artifacts {
			if a != nil && isArchiveURL(a.DownloadURL) {
				return fmt.Errorf("recipe sets binary_name %q but artifacts[%q] is an archive — "+
					"binary_name renames a single downloaded binary and does nothing for an archive (ARTIFACTS-01)", recipe.BinaryName, arch)
			}
		}
		// A name that is a path would escape bin/ (or land somewhere the command
		// does not expect). Only a plain filename is meaningful.
		if strings.ContainsAny(recipe.BinaryName, `/\`) || recipe.BinaryName == "." || recipe.BinaryName == ".." {
			return fmt.Errorf("binary_name %q must be a plain filename, not a path (ARTIFACTS-01)", recipe.BinaryName)
		}
	}

	// ── DOWNLOAD-01: one download form, and it is per-architecture ───────────
	//
	// Ordered LAST of the download rules, deliberately. Every earlier rule about
	// download_url — SECAUDIT2-H1's missing checksum, ARTIFACTS-01's both-forms
	// ambiguity, the binary_name-on-an-archive refusal — still fires for the
	// input it was written for. Putting this one first would have made three
	// shipped guards unreachable, which is the "guard that can never fire"
	// defect this codebase keeps finding, dressed up as a tightening.
	//
	// A single download_url cannot pin two architectures, so every recipe using
	// it was amd64-in-practice while its entry claimed every arch it declared.
	if strings.TrimSpace(recipe.DownloadURL) != "" {
		return fmt.Errorf("recipe sets `download_url`, which this installer no longer runs " +
			"(DOWNLOAD-01, roadmap/INSTALL-METHODOLOGY.md) — move it into `artifacts` keyed by " +
			"architecture, each with its own sha256, even when one artefact serves every architecture")
	}

	// EXTRACT-01: extract_dir becomes a filesystem path, so it is screened the
	// same way an archive member is — and it is refused on a recipe that unpacks
	// nothing, rather than being silently ignored (the binary_name rule again).
	if d := strings.TrimSpace(recipe.ExtractDir); d != "" {
		if err := validateExtractDir(d); err != nil {
			return err
		}
		for arch, a := range recipe.Artifacts {
			if a != nil && !isArchiveURL(a.DownloadURL) {
				return fmt.Errorf("recipe sets extract_dir %q but artifacts[%q] is not an archive — "+
					"extract_dir names where an archive unpacks and does nothing for a single binary; "+
					"use binary_name instead (EXTRACT-01)", d, arch)
			}
		}
	}

	// ── INSTALL-02: an unclassified recipe gets no install path ──────────────
	//
	// Structural default-closed, matching DeliveryUnknown in arch.go: a recipe
	// shape nobody classified must not silently acquire one. This is the second,
	// independent gate — InstallFromRegistry's dispatch has no fallthrough
	// branch either — so removing one of the two still leaves the catalogue
	// closed. `vaultwarden` ships today with neither field set and is listed as
	// installable.
	if recipe.FlatpakID == "" && !recipe.HasArtifacts() {
		return fmt.Errorf("recipe declares no install vehicle (INSTALL-02, roadmap/INSTALL-METHODOLOGY.md) — " +
			"set exactly one of `flatpak_id` (a Flathub app) or `artifacts` (per-architecture pinned payloads)")
	}

	// ── ARCH-02: no architecture in a string every architecture shares ───────
	//
	// LAST, and that is not an accident. It is the newest rule here, and every
	// rule above it must still answer for the input it was written for —
	// DOWNLOAD-01 in particular, since the entry that motivated this one
	// (conduit) trips both. Putting a new rule first is how three shipped guards
	// were made unreachable once already (§6.1); the test suite asserts WHICH
	// rule answers for a recipe that violates two.
	if err := rejectArchInSharedFields(recipe); err != nil {
		return err
	}

	return nil
}

// firstLine trims a recipe string to something safe to put in an error message.
// A refused install command can be several hundred characters of shell.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		return s[:117] + "..."
	}
	return s
}

// networkFetchRe matches the ways a shell string reaches the network for a
// payload. Package managers are in the list because "apt-get is gone" has to
// mean gone from every field, not gone from `install` and alive in
// `post_install`.
//
// Word-boundary anchored on both sides so `curlify-config` is not a match; the
// point is to refuse a FETCH, not any string containing four letters.
var networkFetchRe = regexp.MustCompile(`(?i)(^|[\s;&|(` + "`" + `])(curl|wget|git\s+clone|apt|apt-get|dpkg|pip|pip3|npm|yarn|gem|go\s+install|cargo\s+install|nix-env)([\s;&|)]|$)`)

// rejectNetworkFetch refuses a post_install that downloads anything.
//
// post_install exists to arrange bytes the installer has already fetched and
// checksummed — write a config file, mint a secret, create a state directory.
// The moment it can fetch, the pinned-artefact guarantee is decorative: the
// signed entry says sha256 X and the box runs whatever the network returned.
func rejectNetworkFetch(cmd string) error {
	if m := networkFetchRe.FindString(cmd); m != "" {
		return fmt.Errorf("post_install reaches the network or a package manager (%q) — "+
			"it may only arrange bytes the installer already fetched and verified "+
			"(POSTINSTALL-02, roadmap/INSTALL-METHODOLOGY.md): %q",
			strings.TrimSpace(m), firstLine(cmd))
	}
	return nil
}

// swallowedFailureRe matches the shell idioms that turn a failing command into
// a successful one: `|| true`, `|| :`, and `; true` as the final word.
var swallowedFailureRe = regexp.MustCompile(`\|\|\s*(true|:)\s*($|[;&|])`)

// rejectSwallowedFailure refuses a post_install that cannot report failure.
func rejectSwallowedFailure(cmd string) error {
	if swallowedFailureRe.MatchString(cmd) {
		return fmt.Errorf("post_install swallows its own failure with `|| true` — a step that cannot fail "+
			"cannot be relied on, and the installer would report success for an unconfigured app "+
			"(POSTINSTALL-03, roadmap/INSTALL-METHODOLOGY.md): %q", firstLine(cmd))
	}
	return nil
}

// validateExtractDir screens a recipe's extract_dir before it becomes a path.
//
// Refusals are absolute names, traversal, and anything that is not a plain
// relative path. `filepath.Clean` is applied first so "a/../../b" is caught on
// its resolved form rather than on its spelling — the same lesson extractZip's
// containment screen records.
func validateExtractDir(d string) error {
	if strings.HasPrefix(d, "/") || strings.HasPrefix(d, `\`) || filepath.IsAbs(d) {
		return fmt.Errorf("extract_dir %q must be a relative path inside the app directory (EXTRACT-01)", d)
	}
	clean := filepath.Clean(d)
	if clean == "." || clean == ".." || clean == "" ||
		clean == string(filepath.Separator) ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("extract_dir %q escapes the app directory (EXTRACT-01)", d)
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
//
// It REFUSES the unverified quarantine registry (see UnverifiedRegistryFile).
// That refusal is what lets `make verify-registry` and the REGISTRY-SIGN-02
// acceptance suite stay absolute — "every entry in the signed registry is signed
// by the trusted publisher", with no exception path inside the gate — while an
// entry that has not yet earned a signature still keeps its work and its
// caveats on disk.  Without this check, VULOS_REGISTRY=<quarantine> would load
// unsigned entries into the App Hub's catalogue; they would still fail closed at
// install time (VerifyEntrySignature), but they would be listed as installable,
// which is exactly the "looks trusted" state the split exists to prevent.
func LoadRegistry(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if quarantined, why := isQuarantineRegistry(path, data); quarantined {
		return nil, fmt.Errorf("%w: %s (%s) — its entries are NOT signed and NOT trusted; "+
			"the signed catalogue is registry.json. To read it deliberately, call "+
			"LoadUnverifiedRegistry (loudly logged, refused when VULOS_ENV=prod). To ship an "+
			"entry, promote it: real-hardware validation, then `make sign-registry` "+
			"(see the _promotion notes in %s and docs/KEY-CEREMONY.md)",
			ErrUnverifiedRegistry, path, why, UnverifiedRegistryFile)
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

// ─── The unverified quarantine registry ───────────────────────────────────────
//
// registry.json is the SIGNED set: every entry in it is signed by the release
// key the shipped anchor certifies, and `make verify-registry` fails on the
// first entry that is not.  There is deliberately no "quarantine" or "pending"
// concept inside that gate — an exception path in a security gate is a way for
// an unsigned entry to ship.
//
// An entry that is not ready to be signed — e.g. one whose own _note says
// UNVERIFIED, authored without the hardware needed to validate it — lives in
// registry-unverified.json instead.  That file:
//
//   - is NOT signed, and every entry in it must carry an EMPTY signature;
//   - is NOT loaded by any runtime path (LoadRegistry refuses it outright);
//   - is NOT copied into the image (Dockerfile and build.sh copy only registry.json);
//   - IS cross-checked by `make verify-registry`, which fails if an app ID
//     appears in both files, if an entry in it carries a signature, or if it
//     exists but is empty.
//
// Promotion (the only route into the signed set) is documented in that file's
// _promotion array and in docs/KEY-CEREMONY.md.

// UnverifiedRegistryFile is the conventional filename of the quarantine
// registry.  The name alone is enough for LoadRegistry to refuse it, so that
// stripping the in-file marker cannot launder it into the trusted path.
const UnverifiedRegistryFile = "registry-unverified.json"

// unverifiedMarkerKey is the top-level JSON key a quarantine registry sets to
// true.  Content-based detection means renaming the file cannot launder it
// either — both the name and the marker are refused.
const unverifiedMarkerKey = "_unverified"

// ErrUnverifiedRegistry is returned when a quarantine registry is handed to a
// path that only accepts the signed registry.  Callers can errors.Is it.
var ErrUnverifiedRegistry = errors.New("registry: refusing the UNVERIFIED quarantine registry")

// isQuarantineRegistry reports whether path/data is the unverified quarantine
// registry, and why.  Either signal is sufficient: the filename, or the
// "_unverified": true marker inside the file.
func isQuarantineRegistry(path string, data []byte) (bool, string) {
	byName := filepath.Base(path) == UnverifiedRegistryFile
	var probe map[string]json.RawMessage
	byMarker := false
	if err := json.Unmarshal(data, &probe); err == nil {
		if raw, ok := probe[unverifiedMarkerKey]; ok {
			var flag bool
			byMarker = json.Unmarshal(raw, &flag) == nil && flag
		}
	}
	switch {
	case byName && byMarker:
		return true, "filename is " + UnverifiedRegistryFile + ` and it declares "` + unverifiedMarkerKey + `": true`
	case byName:
		return true, "filename is " + UnverifiedRegistryFile
	case byMarker:
		return true, `it declares "` + unverifiedMarkerKey + `": true`
	}
	return false, ""
}

// LoadUnverifiedRegistry is the ONLY way to read the quarantine registry, and
// it is deliberately awkward: it must be called by name (nothing in the runtime
// does), it refuses anything that is not actually marked as quarantined, it
// refuses any entry that carries a signature — a quarantined entry that looks
// signed is the failure mode this whole split exists to prevent — it is refused
// outright under VULOS_ENV=prod (which is also the default when VULOS_ENV is
// unset), and it logs a banner naming the file and every entry it returns.
//
// Nothing it returns is trusted: the entries are unsigned, so any install
// attempt still fails closed in VerifyEntrySignature.
func LoadUnverifiedRegistry(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if quarantined, _ := isQuarantineRegistry(path, data); !quarantined {
		return nil, fmt.Errorf("registry: %s is not marked as an unverified quarantine registry "+
			"(no %q filename, no %q: true marker) — use LoadRegistry for the signed registry",
			path, UnverifiedRegistryFile, unverifiedMarkerKey)
	}

	prod, err := isProdEnv()
	if err != nil {
		return nil, err
	}
	if prod {
		return nil, fmt.Errorf("%w: %s cannot be read under VULOS_ENV=prod (unset counts as prod) — "+
			"unverified entries have no place on a production box", ErrUnverifiedRegistry, path)
	}

	var r Registry
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if r.Apps == nil {
		r.Apps = make(map[string]*RegistryEntry)
	}
	ids := make([]string, 0, len(r.Apps))
	for id, entry := range r.Apps {
		if entry.Signature != "" {
			return nil, fmt.Errorf("%w: %s entry %q carries a signature — a quarantined entry must be "+
				"unsigned. Either it belongs in registry.json (promote it: validate, then "+
				"`make sign-registry`), or the signature is bogus and must be removed",
				ErrUnverifiedRegistry, path, id)
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	log.Printf("[registry] *** UNVERIFIED QUARANTINE REGISTRY LOADED ON PURPOSE: %s ***", path)
	log.Printf("[registry] *** %d entry/entries, NONE signed, NONE trusted: %s", len(ids), strings.Join(ids, ", "))
	log.Printf("[registry] *** any install of these WILL be refused (REGISTRY-SIGN-01). " +
		"Promote via real-hardware validation + `make sign-registry` to make them real.")
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

// verifyDeps is the seam the dependency check goes through. It is a variable
// only so a test can supply a dependency set that IS satisfied — the negative
// direction needs no seam, because a package name no box carries is missing on
// every platform, while "this dep is present" is not portably expressible.
// Nothing outside the tests ever reassigns it, and the real function is
// exercised directly by services/packages' own tests.
var verifyDeps = packages.VerifyDeps

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

	// ARCH-01: refuse an install this box's architecture cannot satisfy.
	//
	// The listing marks these entries not-installable, but a listing is a
	// suggestion and this is the enforcement. Without it, an x86_64-only Flathub
	// app (Steam, Chrome, Spotify, Zoom, VS Code…) installed on an arm64 box
	// fails somewhere deep in `flatpak install` — or worse, half-succeeds,
	// leaving a manifest for an app that can never run. Refusing here names the
	// reason once, before anything touches the filesystem.
	//
	// Empty Arch still means "any", so the 46 entries that declare nothing are
	// unaffected.
	if !ArchSupported(entry.Arch, SupportedArches()) {
		return fmt.Errorf("registry entry %q cannot be installed on this box: %s",
			appID, ArchUnavailableReason(entry.Arch, SupportedArches()))
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

	// DEPS-01/DEPS-02: the declared dependencies must ALREADY be on the box,
	// and this is checked BEFORE anything is downloaded.
	//
	// It used to be `packages.InstallDeps(ctx, recipe.Deps)` with the error
	// DISCARDED, placed after the payload had landed. Two defects in one line:
	//
	//  1. A dependency that could not be installed produced a SUCCESSFUL
	//     install of an app that cannot exec — POSTINSTALL-01's defect one
	//     field down. Measured end to end on 2026-08-17: conduwuit 0.5.9's
	//     arm64 binary carries `liburing.so.2` in DT_NEEDED, and on a box
	//     without it every launch dies with
	//     "error while loading shared libraries: liburing.so.2".
	//  2. On a real box the install could not have worked anyway. build.sh
	//     clears the apt lists, nothing here runs `apt-get update`, and
	//     `apt-get install -y liburing2` in that state exits 100 with
	//     "Unable to locate package". See packages.VerifyDeps for the
	//     measurement.
	//
	// Running FIRST is deliberate, and it is the lesson EXTRACT-01 learned the
	// hard way (M10): a box that fetches 110 MB and only then notices it can
	// never run the result has already paid for the mistake. It also means
	// the refusal names the dependency rather than an unpacked half-app.
	if err := verifyDeps(ctx, recipe.Deps); err != nil {
		return fmt.Errorf("dependency check failed for %s@%s: %w", appID, version, err)
	}

	appDir := filepath.Join(appsDir, appID)

	// Create strict directory structure
	for _, dir := range []string{"bin", "static", "data"} {
		os.MkdirAll(filepath.Join(appDir, dir), 0755)
	}

	// ── The two install vehicles, and nothing else ───────────────────────────
	//
	// There is deliberately NO default branch that installs. A recipe shape
	// nobody classified must not silently acquire an install path — the same
	// rule DeliveryUnknown states in arch.go, applied where it decides
	// something. Before this, the fallthrough ran `sh -c recipe.Install`, so any
	// recipe that was not Flatpak and not a download became a shell script; that
	// is how `apt-get install -y blender`, `git clone … static/` and a forged
	// checksum piped into `|| true` were all the same code path.
	//
	// validateRecipeSecurity refuses those shapes earlier (INSTALL-01/02), so
	// this switch and that gate are two independent closures of the same hole.
	// Deleting either one leaves the catalogue closed, which is the point:
	// a single mutation cannot re-open a shell.
	switch {
	case recipe.FlatpakID != "":
		if err := FlatpakInstall(ctx, recipe.FlatpakID); err != nil {
			return fmt.Errorf("flatpak install %s: %w", recipe.FlatpakID, err)
		}
	case recipe.HasArtifacts():
		// Vulos-native: pinned per-architecture payload, own sha256, unpacked
		// into the app's own directory.
		if err := staticInstall(ctx, recipe, appDir); err != nil {
			return fmt.Errorf("static install %s: %w", appID, err)
		}
	default:
		return fmt.Errorf("registry entry %q@%s declares no install vehicle "+
			"(INSTALL-02, roadmap/INSTALL-METHODOLOGY.md): set `flatpak_id` or `artifacts`", appID, version)
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
		// POSTINSTALL-01: a failed post_install is FATAL, and the half-built app
		// directory is removed.
		//
		// This used to log a warning and carry on, which meant a successful
		// install of an UNCONFIGURED app. That is not a theoretical grade of
		// wrong: it shipped a broken app during this very change. lilmail's
		// post_install contained `\'` inside a single-quoted sh string, where a
		// backslash does not escape — it ends the quote. sh exited 2 with
		// "Syntax error: Unterminated quoted string", config.toml was never
		// written, and the installer nonetheless reported success, wrote the
		// manifest, and left an app whose every launch dies with
		// "Failed to load config". Only an HTTP probe caught it; install-path,
		// manifest-written and the binary all looked fine.
		//
		// For an app that writes its config here — conduit, gitea, navidrome,
		// diwan, wede, lilmail — post_install IS the install. Refusing loudly
		// gives the owner an error naming the app; the previous behaviour gave
		// them an app that starts and immediately dies.
		if err := cmd.Run(); err != nil {
			errOutput := lastLines(stderrBuf.String(), 10)
			// Roll back, so a retry starts clean rather than on top of whatever
			// the failed command managed to create.
			if rmErr := os.RemoveAll(appDir); rmErr != nil {
				log.Printf("[registry] post-install rollback for %s failed: %v", appID, rmErr)
			}
			if errOutput != "" {
				return fmt.Errorf("post-install for %s@%s failed: %w\n%s", appID, version, err, errOutput)
			}
			return fmt.Errorf("post-install for %s@%s failed: %w", appID, version, err)
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

	// STATE-01: hand the state directory to the uid the app actually runs as.
	// LAST, so it covers what post_install wrote and follows the data symlink
	// this step may just have created. See state_owner.go for the model: code
	// stays root-owned, state does not.
	if err := handOffStateDir(appDir); err != nil {
		return fmt.Errorf("install %s@%s: the app directory was built but its state directory "+
			"could not be handed to uid %d, which is the uid the launcher runs the app as — "+
			"the app would install and then fail on its first write (STATE-01): %w",
			appID, version, appUID, err)
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
	// Installable (ARCH-01) reports whether THIS BOX can install the app: its
	// declared Arch intersected with the box's supported arches. The comparison
	// is done here, server-side, and never handed to the client as two lists to
	// compare itself — the whole failure mode this closes is a comparison that
	// mixes naming schemes (Debian amd64/arm64 vs Flatpak x86_64/aarch64) and
	// silently matches nothing. See arch.go.
	Installable bool `json:"installable"`
	// InstallableReason explains a false Installable in one sentence a UI can
	// show verbatim ("requires amd64; this box is arm64"). Empty when installable.
	InstallableReason string `json:"installable_reason,omitempty"`
	// BoxArch is the architecture of the BOX, Debian spelling — the server
	// process's own runtime.GOARCH, never anything derived from the request.
	// Desktop apps are streamed FROM the box, so a user on an arm64 Mac driving
	// an amd64 box must see amd64.
	BoxArch string `json:"box_arch"`
	// Keywords surfaces RegistryEntry.Keywords to the App Hub list/search API
	// (GET /api/store/registry) — without this the registry's per-app keywords
	// data reaches neither the API response nor the frontend search box.
	Keywords []string `json:"keywords"`
}

// ListEntries returns a flat list of all registry apps, marking which are installed.
// For Flatpak apps, checks the live Flatpak state so removed apps disappear immediately.
func (r *Registry) ListEntries(appsDir string) []RegistryListEntry {
	flatpakApps := InstalledFlatpaks()
	// ARCH-01: resolved ONCE per listing, not per entry — SupportedArches may
	// shell out to flatpak, and the answer cannot differ between two entries of
	// the same response.
	supported := SupportedArches()
	boxArch := supported[0]
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

			Installable:       ArchSupported(entry.Arch, supported),
			InstallableReason: ArchUnavailableReason(entry.Arch, supported),
			BoxArch:           boxArch,
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
	// Per-architecture recipes resolve against THIS SERVER PROCESS's own
	// architecture — the same value ARCH-01 enforces the entry-level `arch`
	// against, via the same NormalizeArch table. Nothing here reads a request
	// header: desktop apps are streamed FROM the box, so the box's arch is the
	// only one that can matter.
	// extract_dir is screened FIRST, before anything is fetched. It is the one
	// recipe field that becomes a filesystem path outside the app dir if it is
	// wrong, and a box that has already downloaded the payload before noticing
	// has done the attacker's transfer for them. Order is the check.
	extractRoot := appDir
	if d := strings.TrimSpace(recipe.ExtractDir); d != "" {
		if err := validateExtractDir(d); err != nil {
			return err
		}
		extractRoot = filepath.Join(appDir, filepath.Clean(d))
		if !strings.HasPrefix(filepath.Clean(extractRoot)+string(os.PathSeparator),
			filepath.Clean(appDir)+string(os.PathSeparator)) {
			return fmt.Errorf("refusing extract_dir %q: it resolves outside the app directory (EXTRACT-01)", d)
		}
	}

	url, checksum, err := recipe.ResolveArtifact(BoxArch())
	if err != nil {
		return err
	}
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("static install: recipe has no download_url")
	}
	log.Printf("[registry/static] downloading %s", url)

	client := &http.Client{Timeout: 10 * time.Minute}
	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if rerr != nil {
		return fmt.Errorf("build request: %w", rerr)
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

	// Verify the checksum for the artifact we actually resolved. Note this is
	// `checksum` from ResolveArtifact, NOT recipe.Checksum: on a per-arch recipe
	// the top-level checksum is empty by construction (validateRecipeSecurity
	// refuses one), so reading it here would silently skip verification on every
	// per-arch install.
	if checksum != "" {
		got := hex.EncodeToString(h.Sum(nil))
		if got != strings.ToLower(checksum) {
			return fmt.Errorf("checksum mismatch: expected %s, got %s", checksum, got)
		}
		log.Printf("[registry/static] checksum OK (%s)", got[:12])
	}

	// Detect archive vs plain binary by URL extension. The three branches are
	// mutually exclusive by construction: a zip must reach extractZip and must
	// never fall through to `tar`, because whether that fallthrough "works"
	// depends on which tar the host has (see tarExtensions).
	if extractRoot != appDir {
		if err := os.MkdirAll(extractRoot, 0755); err != nil {
			return fmt.Errorf("create extract_dir %s: %w", extractRoot, err)
		}
	}

	if isZipURL(url) {
		return extractZip(tmpPath, extractRoot, recipe.ArchiveStrip)
	}

	if isTarURL(url) {
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
		args := []string{"xf", tmpPath, "-C", extractRoot, "--no-same-owner"}
		if recipe.ArchiveStrip > 0 {
			args = append(args, fmt.Sprintf("--strip-components=%d", recipe.ArchiveStrip))
		}
		log.Printf("[registry/static] extracting archive into %s (strip=%d)", extractRoot, recipe.ArchiveStrip)
		cmd := exec.CommandContext(ctx, "tar", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("extract archive: %w\n%s", err, strings.TrimSpace(string(out)))
		}
	} else {
		// Plain binary: install to bin/<filename>, or bin/<binary_name> when the
		// recipe pins one. The pinned name is what makes a per-arch recipe
		// runnable: the release assets are called diwan-linux-amd64 and
		// diwan-linux-arm64, but `command` is one string for both arches, so
		// without a stable name no command could refer to the installed file.
		base := filepath.Base(strings.SplitN(url, "?", 2)[0])
		if n := strings.TrimSpace(recipe.BinaryName); n != "" {
			// validateRecipeSecurity has already refused a name containing a
			// path separator; this is the belt to that braces, because the value
			// is about to become a filesystem path.
			if strings.ContainsAny(n, `/\`) || n == "." || n == ".." {
				return fmt.Errorf("refusing binary_name %q: must be a plain filename", n)
			}
			base = n
		}
		destPath := filepath.Join(appDir, "bin", base)
		// PAYLOAD-01: an archive that reaches this branch would be installed as
		// an executable and unpack nothing, while the install reported success.
		// The URL extension is what routed it here, and the URL extension is
		// exactly the thing that was wrong for drawio; the bytes are not.
		if err := refuseArchiveInstalledAsBinary(tmpPath, url, destPath); err != nil {
			return err
		}
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

// zipExtensions are the suffixes dispatched to extractZip.
//
// `.war` is here because a WAR **is** a ZIP — the Servlet specification defines
// it as one — and drawio's only published artefact is `draw.war`. This is not a
// convenience: with `.war` unrecognised, the file matched neither archive
// branch, fell through to the plain-binary branch, and was installed as
// `bin/draw.war` with mode 0755 while the installer REPORTED SUCCESS. That is
// the shipped behaviour of the `drawio` entry today, and it is the same silent
// shape the missing `.zip` support had before it (PER-ARCH-ARTIFACTS §3).
var zipExtensions = []string{".zip", ".war"}

// isZipURL reports whether a download URL names a zip archive.
func isZipURL(url string) bool {
	lower := strings.ToLower(strings.SplitN(url, "?", 2)[0])
	for _, ext := range zipExtensions {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// extractZip unpacks a .zip into appDir, stripping `strip` leading path
// components, exactly as the tar path does with --strip-components.
//
// Why this exists: staticInstall detected .tar.gz/.tgz/.tar.bz2/.tar.xz and
// nothing else, so a .zip fell through to the "plain binary" branch — it was
// copied to bin/<name>.zip and chmod 0755. The install REPORTED SUCCESS and the
// app could never start. lilmail publishes zips, and it is the first entry to
// need this; the gap was a silent one, which is the only reason it survived.
//
// It uses Go's archive/zip rather than shelling out to `unzip`, so the shipped
// image needs no new package (scripts/image-packages.txt is gated) and the
// traversal screen is applied member by member in-process rather than parsed
// out of another program's output.
func extractZip(zipPath, appDir string, strip int) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()

	// A zip member may name any path it likes, including ../.. and absolute
	// ones. Screen every member BEFORE creating anything, so a malicious archive
	// cannot half-extract.
	//
	// There are exactly TWO screens here and each one is load-bearing; neither
	// is decoration for the other:
	//
	//   1. Absolute names are refused outright. Containment alone would not
	//      refuse them — "/etc/cron.d/evil" trims to a relative path that lands
	//      harmlessly INSIDE appDir — so `tar`'s habit of silently stripping the
	//      leading slash would become ours. An archive that asks to write to /etc
	//      is not an archive we unpack quietly.
	//   2. Containment is what catches traversal, and it does so on the RESOLVED
	//      destination rather than on the spelling of the name. An earlier draft
	//      also string-matched "../" first, which made this check unreachable —
	//      a guard that can never fire, which is the failure mode this codebase
	//      keeps finding. The textual pre-filter is gone; the traversal tests now
	//      exercise this line.
	type planned struct {
		f    *zip.File
		dest string
	}
	var plan []planned
	root := filepath.Clean(appDir) + string(os.PathSeparator)
	for _, f := range zr.File {
		name := f.Name
		if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) || filepath.IsAbs(name) {
			return fmt.Errorf("refusing archive: unsafe member %q (absolute path)", name)
		}
		// Strip leading components the way tar --strip-components does: a member
		// with fewer components than `strip` is dropped, not flattened.
		parts := strings.Split(strings.Trim(name, "/"), "/")
		if strip > 0 {
			if len(parts) <= strip {
				continue
			}
			parts = parts[strip:]
		}
		rel := filepath.Join(parts...)
		if rel == "" || rel == "." {
			continue
		}
		dest := filepath.Join(appDir, rel)
		if !strings.HasPrefix(filepath.Clean(dest)+string(os.PathSeparator), root) {
			return fmt.Errorf("refusing archive: member %q escapes the app directory (path traversal)", name)
		}
		// Symlinks can point anywhere once written; a zip we unpack as root has
		// no business carrying them.
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing archive: member %q is a symlink", name)
		}
		plan = append(plan, planned{f: f, dest: dest})
	}

	for _, p := range plan {
		if p.f.FileInfo().IsDir() {
			if err := os.MkdirAll(p.dest, 0755); err != nil {
				return fmt.Errorf("create dir %s: %w", p.dest, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p.dest), 0755); err != nil {
			return fmt.Errorf("create dir for %s: %w", p.dest, err)
		}
		// Preserve the executable bit — a zip is how lilmail ships its binary,
		// and a 0644 binary is an install that cannot launch.
		mode := p.f.Mode().Perm()
		if mode == 0 {
			mode = 0644
		}
		if err := writeZipMember(p.f, p.dest, mode); err != nil {
			return err
		}
	}
	log.Printf("[registry/static] extracted zip into %s (strip=%d, %d members)", appDir, strip, len(plan))
	return nil
}

// writeZipMember copies one zip entry to dest with the given mode.
func writeZipMember(f *zip.File, dest string, mode os.FileMode) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open zip member %s: %w", f.Name, err)
	}
	defer rc.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return fmt.Errorf("write %s: %w", dest, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close %s: %w", dest, err)
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
