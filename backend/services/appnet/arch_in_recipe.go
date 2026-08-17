package appnet

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ─── ARCH-02: no architecture inside a string every architecture shares ──────
//
// THE RULE THIS ENFORCES is §5 of roadmap/INSTALL-METHODOLOGY.md: a registry
// entry is FLEET-IDENTICAL. Every box in a fleet receives the same signed
// bytes and resolves its own artefact locally, so nothing architecture-specific
// may live anywhere except the `artifacts` map, whose keys ARE the architecture.
//
// WHAT IT COST BEFORE IT EXISTED. The shipped conduit entry ran
//
//	"command": "bin/conduwuit-linux-amd64 --config data/conduit.toml"
//
// One command string, one architecture-specific filename. The arm64 asset had
// been in the same upstream release the whole time and could never be used,
// because there is no second command string for an arm64 box to read. The entry
// then declared `arch: ["amd64"]` — which looks like a considered decision and
// was actually a filename leaking into a field nobody reads twice.
//
// `binary_name` is the answer for a single binary (it installs per-arch assets
// under ONE name), and `extract_dir` plus a stable path is the answer for an
// archive. Both keep the architecture inside the installer, where it is already
// known and already enforced, instead of in free text.
//
// WHY A REFUSAL AND NOT A LINT. A lint runs where someone remembers to run it.
// This is the same reasoning that made per-recipe `arch` a defect: it sat in the
// passthrough map for months, read by nothing, looking like protection.
//
// WHAT IS DELIBERATELY NOT CHECKED: the artefact URLs. `.../conduwuit-linux-amd64`
// under the "amd64" key is the CORRECT place for an architecture — it is keyed by
// the very thing it names, and the resolver picks it per box. A rule that also
// refused those would refuse every correct per-arch recipe in the catalogue,
// which is why the test suite carries that case as a control.

// archTokenRe matches a Debian, Flatpak/uname or Go spelling of a machine
// architecture appearing as a word. The three spellings are the ones §5's table
// lists; matching only "amd64" would miss "conduwuit-linux-x86_64" entirely.
var archTokenRe = regexp.MustCompile(`(?i)(^|[^a-z0-9])(amd64|arm64|x86[-_]?64|aarch64|i[36]86|armv[0-9][a-z0-9]*|armhf|ppc64le|ppc64|s390x|riscv64|mips64el)([^a-z0-9]|$)`)

// archSharedField is one recipe field that is shared by every architecture.
type archSharedField struct {
	name  string
	value string
}

// rejectArchInSharedFields refuses a recipe that names an architecture in a
// field every architecture reads.
func rejectArchInSharedFields(recipe *VersionRecipe) error {
	fields := []archSharedField{
		{"command", recipe.Command},
		{"post_install", recipe.PostInstall},
		{"binary_name", recipe.BinaryName},
		{"extract_dir", recipe.ExtractDir},
	}
	envKeys := make([]string, 0, len(recipe.Env))
	for k := range recipe.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys) // deterministic: which field is named must not depend on map order
	for _, k := range envKeys {
		fields = append(fields, archSharedField{"env[" + k + "]", recipe.Env[k]})
	}

	for _, f := range fields {
		m := archTokenRe.FindStringSubmatch(f.value)
		if m == nil {
			continue
		}
		return fmt.Errorf("recipe names the architecture %q in `%s`, which is ONE string shared by "+
			"every architecture this entry declares (ARCH-02, roadmap/INSTALL-METHODOLOGY.md §5): %q — "+
			"a registry entry is fleet-identical and each box resolves its own artefact locally, so an "+
			"arch-specific filename here makes the entry installable on exactly one architecture while "+
			"claiming them all. Use `binary_name` to give per-arch binaries one installed name, or "+
			"`extract_dir` and a stable path for an archive. The architecture belongs in the "+
			"`artifacts` KEY, and nowhere else",
			strings.TrimSpace(m[2]), f.name, firstLine(f.value))
	}
	return nil
}
