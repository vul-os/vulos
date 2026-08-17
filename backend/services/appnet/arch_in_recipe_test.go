package appnet

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// ARCH-02 — the shipped conduit entry named `bin/conduwuit-linux-amd64` in
// `command`, a single string shared by every architecture, so the arm64 asset
// that had been in the same release all along could never be used. §5 of the
// methodology says nothing architecture-specific crosses the sync wire; this is
// that sentence with a code path behind it.
//
// The fixtures below are written out in full rather than derived from the
// registry, so that fixing the registry cannot make these tests vacuous.
// ─────────────────────────────────────────────────────────────────────────────

// archRecipe returns a recipe that is valid in every respect except the field
// under test: two real architectures, each with its own URL and digest.
func archRecipe() *VersionRecipe {
	return &VersionRecipe{
		Artifacts: map[string]*Artifact{
			"amd64": {
				DownloadURL: "https://example.invalid/releases/v1/tool-linux-amd64",
				Checksum:    "1111111111111111111111111111111111111111111111111111111111111111",
			},
			"arm64": {
				DownloadURL: "https://example.invalid/releases/v1/tool-linux-arm64",
				Checksum:    "2222222222222222222222222222222222222222222222222222222222222222",
			},
		},
		BinaryName: "tool",
		Command:    "bin/tool --config data/tool.toml",
		Port:       8080,
	}
}

// TestArchInCommandIsRefused is conduit's defect, by name.
func TestArchInCommandIsRefused(t *testing.T) {
	r := archRecipe()
	r.BinaryName = ""
	r.Command = "bin/conduwuit-linux-amd64 --config data/conduit.toml"

	err := validateRecipeSecurity(r)
	if err == nil {
		t.Fatal("a command naming bin/conduwuit-linux-amd64 was ACCEPTED — one command string " +
			"cannot name a per-architecture filename, so the entry works on exactly one " +
			"architecture while declaring two (ARCH-02)")
	}
	if !strings.Contains(err.Error(), "ARCH-02") {
		t.Errorf("refused, but by some other rule: %v", err)
	}
	if !strings.Contains(err.Error(), "command") {
		t.Errorf("the error does not name the offending field: %v", err)
	}
}

// TestArchInEachSharedFieldIsRefused covers the other fields a curator could
// move the same filename into. A rule that only watched `command` would be
// satisfied by writing the architecture into post_install instead.
func TestArchInEachSharedFieldIsRefused(t *testing.T) {
	cases := []struct {
		field string
		apply func(*VersionRecipe)
	}{
		{"post_install", func(r *VersionRecipe) {
			r.PostInstall = "mv bin/tool-linux-arm64 bin/tool"
		}},
		{"binary_name", func(r *VersionRecipe) { r.BinaryName = "tool-x86_64" }},
		{"env", func(r *VersionRecipe) { r.Env = map[string]string{"TOOL_BIN": "bin/tool-aarch64"} }},
	}
	for _, c := range cases {
		r := archRecipe()
		c.apply(r)
		err := validateRecipeSecurity(r)
		if err == nil {
			t.Errorf("%s: an architecture in this field was ACCEPTED (ARCH-02)", c.field)
			continue
		}
		if !strings.Contains(err.Error(), "ARCH-02") {
			t.Errorf("%s: refused by some other rule: %v", c.field, err)
		}
	}
}

// TestArchInArtifactURLsIsACCEPTED is the CONTROL, and it is the one that makes
// the rule usable. An architecture inside an artefact URL is the CORRECT place
// for one — the URL is keyed by the very architecture it names, and the
// resolver picks per box. A rule without this control would pass every refusal
// test above while refusing every correct per-arch recipe in the catalogue.
func TestArchInArtifactURLsIsACCEPTED(t *testing.T) {
	r := archRecipe() // its two URLs literally end in -amd64 and -arm64
	if err := validateRecipeSecurity(r); err != nil {
		t.Fatalf("a correct per-architecture recipe was refused: %v", err)
	}
}

// TestArchLookalikeWordsAreACCEPTED keeps the token match from becoming a
// substring hunt. "armhf" is an architecture; "disarm64bit" is not a word this
// rule has any business refusing, and a recipe refused for a reason a curator
// cannot act on is worse than no rule.
func TestArchLookalikeWordsAreACCEPTED(t *testing.T) {
	for _, cmd := range []string{
		"bin/tool --alarm 64 --data data/",
		"bin/tool --mode performance",
		"python3 -m http.server ${PORT} --directory static/",
	} {
		r := archRecipe()
		r.Command = cmd
		if err := validateRecipeSecurity(r); err != nil {
			t.Errorf("command %q was refused: %v", cmd, err)
		}
	}
}

// TestArchRuleDoesNotShadowTheOlderDownloadRules pins the ORDER, not just the
// outcome. conduit's shipped recipe trips DOWNLOAD-01 *and* ARCH-02; if the new
// rule answered first, DOWNLOAD-01 would become unreachable for exactly the
// entry it was written for, and every test of it would still pass because each
// only asserts "an error happened". That is §6.1's lesson, applied to the rule
// that was added after it was written.
func TestArchRuleDoesNotShadowTheOlderDownloadRules(t *testing.T) {
	r := &VersionRecipe{
		DownloadURL: "https://example.invalid/releases/v1/tool-linux-amd64",
		Checksum:    "3333333333333333333333333333333333333333333333333333333333333333",
		Command:     "bin/tool-linux-amd64 --config data/tool.toml",
		Port:        8080,
	}
	err := validateRecipeSecurity(r)
	if err == nil {
		t.Fatal("a download_url recipe was accepted")
	}
	if !strings.Contains(err.Error(), "DOWNLOAD-01") {
		t.Errorf("the older rule no longer answers for the input it was written for — "+
			"got %v", err)
	}
}
