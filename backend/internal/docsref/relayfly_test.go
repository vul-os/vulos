package docsref

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"vulos/backend/services/reach/tunnel"
)

// RELAY-SELF-HOST.md's Fly.io recipe (Recipe B) is the one place in the docs
// corpus where VULOS_RELAY_* environment variables appear inside fenced
// ```toml and ```bash code blocks rather than as backticked inline code.
//
// TestDocumentedEnvVarsExist in envref_test.go only extracts names wrapped in
// single backticks (see its `documented` regexp), so a variable renamed or
// typo'd inside Recipe B's fly.toml or `fly secrets set` block would NOT be
// caught by that check — the general contract is blind to this exact
// document for this exact reason. This file closes that gap for the one
// document it applies to, rather than loosening the general regex (which
// would then also start matching fenced code in every OTHER doc, a much
// bigger change than the recipe warrants).
//
// It also pins the relay's health-check path, since Recipe B's `fly.toml`
// and its "Verify" step both hardcode it as a literal string, not a
// backticked reference — the same blind spot as the env vars.

const relaySelfHostDoc = "docs/RELAY-SELF-HOST.md"

// recipeBSection returns the text of "## Recipe B — Fly.io" up to (but not
// including) the next "## " heading, so matches from Recipe A or
// "Configuring your boxes" cannot leak into what this file checks.
func recipeBSection(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, relaySelfHostDoc))
	if err != nil {
		t.Fatalf("read %s: %v", relaySelfHostDoc, err)
	}
	src := string(b)
	start := strings.Index(src, "## Recipe B — Fly.io")
	if start < 0 {
		t.Fatalf("%s no longer has a %q heading; this check has lost its subject", relaySelfHostDoc, "## Recipe B — Fly.io")
	}
	rest := src[start+len("## Recipe B — Fly.io"):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	if len(rest) < 500 {
		t.Fatalf("Recipe B section of %s is only %d bytes; too short to be the recipe this check describes", relaySelfHostDoc, len(rest))
	}
	return rest
}

// The six VULOS_RELAY_* names the Fly recipe is documented (in the task that
// produced this file) to use: three in fly.toml's [env] block
// (VULOS_RELAY_ADDR, VULOS_RELAY_DOMAIN, VULOS_RELAY_RENDEZVOUS), two more
// alongside them (VULOS_RELAY_TRUST_PROXY_HEADERS, VULOS_RELAY_ADMIN_ADDR),
// and VULOS_RELAY_GRANTS in the `fly secrets set` step. Asserted as a floor,
// not a ceiling — the recipe naming additional vars is fine.
var relayFlyRecipeRequiredVars = []string{
	"VULOS_RELAY_ADDR",
	"VULOS_RELAY_DOMAIN",
	"VULOS_RELAY_RENDEZVOUS",
	"VULOS_RELAY_TRUST_PROXY_HEADERS",
	"VULOS_RELAY_ADMIN_ADDR",
	"VULOS_RELAY_GRANTS",
}

func TestFlyRecipeEnvVarsExistInCode(t *testing.T) {
	section := recipeBSection(t)
	inCode := envNamesInCode(t) // defined in envref_test.go, same package

	found := map[string]bool{}
	for _, m := range envNameRe.FindAllString(section, -1) {
		found[m] = true
	}
	if len(found) < 6 {
		t.Fatalf("only %d VULOS_* names were found in Recipe B; the section moved or the "+
			"extraction is broken, so a clean result here would prove nothing", len(found))
	}

	var missingFromCode []string
	for name := range found {
		if !inCode[name] {
			missingFromCode = append(missingFromCode, name)
		}
	}
	sort.Strings(missingFromCode)
	if len(missingFromCode) > 0 {
		t.Errorf("Recipe B (Fly.io) in %s names %d env var(s) nothing in the backend reads:\n  %s",
			relaySelfHostDoc, len(missingFromCode), strings.Join(missingFromCode, "\n  "))
	}

	var missingFromDoc []string
	for _, want := range relayFlyRecipeRequiredVars {
		if !found[want] {
			missingFromDoc = append(missingFromDoc, want)
		}
	}
	if len(missingFromDoc) > 0 {
		t.Errorf("Recipe B (Fly.io) in %s no longer mentions %d expected relay env var(s):\n  %s",
			relaySelfHostDoc, len(missingFromDoc), strings.Join(missingFromDoc, "\n  "))
	}
}

// The health path is a bare string literal in Recipe B's fly.toml health
// check and its curl verification step, not a backticked reference, so it
// sits in the same blind spot as the env vars above.
var healthPathRe = regexp.MustCompile(`/_vulos-reach/v1/health`)

func TestFlyRecipeHealthPathMatchesCode(t *testing.T) {
	section := recipeBSection(t)

	if !healthPathRe.MatchString(section) {
		t.Fatalf("Recipe B (Fly.io) in %s no longer mentions the relay health path "+
			"(%s) — the recipe's fly.toml health check and curl verification step "+
			"both depend on this exact path", relaySelfHostDoc, tunnel.HealthPath)
	}
	// tunnel.HealthPath is the actual constant the relay serves. Compare
	// against it directly rather than a second hardcoded literal here, so a
	// change to the constant is what breaks this test, not a copy that
	// drifted from it independently.
	if !strings.Contains(section, tunnel.HealthPath) {
		t.Errorf("Recipe B (Fly.io) in %s documents a health path that does not match "+
			"tunnel.HealthPath (%q) — the relay would 404 on exactly the request the "+
			"recipe tells a reader to run", relaySelfHostDoc, tunnel.HealthPath)
	}
}
