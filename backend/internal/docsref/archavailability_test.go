package docsref

import (
	"regexp"
	"strings"
	"testing"
)

// ─── One wire shape, declared in three languages ─────────────────────────────
//
// ArchAvailability is what the box says about an app's architecture, and the
// App Hub renders every string on it VERBATIM — services/appnet/arch.go is
// emphatic that the browser composes no sentence of its own, so that there is
// one place the wording can be got wrong and one place a test can catch it.
//
// That argument holds for the WORDING. It never covered the SHAPE, which is
// declared three times:
//
//	services/appnet/arch.go                              the box, authoritative
//	frontend/e2e/apphub-fixture.ts                        FixtureAvailability
//	frontend/src/builtin/apphub/__tests__/AppHub.test.tsx FixtureAvailability
//
// and until this test, by nobody. Both fixtures are hand-transcribed. A field
// added to the Go struct, renamed, or given a different json tag leaves them
// still compiling, still passing, and now describing a payload the box does not
// send — so every App Hub spec would go on measuring a card the product cannot
// render, and would keep passing while it did.
//
// This is the same defect this suite keeps finding and the same remedy applied
// to internal/lan's app-interface list on 2026-08-17 (TestAppIfaceGlobsMatchGo):
// where one fact must be written down twice, pin the copies to each other in
// BOTH directions rather than trusting that they agree. Sharing a generated
// type instead would be better and is not free — these are Playwright fixtures
// with no build step that reaches into Go — so they are pinned.
//
// SCOPE, stated so nobody reads more into a green run than it earns: this
// compares the SET OF FIELD NAMES only. It does not check types, and it
// deliberately does not check the sentences — the Go tests own those
// (TestEvaluateArch_NoUnmeasuredClaimReachesTheUser sweeps every rung's copy),
// and the fixtures exist to measure layout and contrast, where what matters is
// that a badge is the right shape and length, not that a copy edit on the box
// is mirrored the same afternoon.

const (
	archGoFile      = "backend/services/appnet/arch.go"
	archFixtureFile = "frontend/e2e/apphub-fixture.ts"
	archUnitFile    = "frontend/src/builtin/apphub/__tests__/AppHub.test.tsx"

	archGoStruct  = "ArchAvailability"
	archTSIface   = "FixtureAvailability"
	archMinFields = 8
)

// goStructJSONTags returns the json tag names of every field in a Go struct.
func goStructJSONTags(t *testing.T, src, name string) []string {
	t.Helper()
	body := blockAfter(t, src, "type "+name+" struct {", archGoFile)

	re := regexp.MustCompile(`json:"([^",]+)`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		if tag := strings.TrimSpace(m[1]); tag != "" && tag != "-" {
			out = append(out, tag)
		}
	}
	return out
}

// tsInterfaceFields returns the property names of a TypeScript interface.
func tsInterfaceFields(t *testing.T, src, name, where string) []string {
	t.Helper()
	body := blockAfter(t, src, "interface "+name+" {", where)

	// A property line: an identifier, an optional `?`, then a colon. Comment
	// lines are skipped explicitly rather than by luck — a `//` line mentioning
	// a field would otherwise be counted as one.
	re := regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\??\s*:`)
	var out []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") ||
			strings.HasPrefix(trimmed, "/*") {
			continue
		}
		if m := re.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

// blockAfter returns the text between an opening line and its matching brace.
// It fails loudly rather than returning "" — a declaration that has been
// renamed or reshaped must not read as an empty, vacuously-equal set.
func blockAfter(t *testing.T, src, open, where string) string {
	t.Helper()
	i := strings.Index(src, open)
	if i < 0 {
		t.Fatalf("%s no longer contains %q — this check has lost its subject and "+
			"would otherwise compare two empty sets and pass", where, open)
	}
	rest := src[i+len(open):]
	// The declarations here contain no nested braces; `\n}` is the terminator.
	// If one ever gains a nested type, this finds the inner brace and the field
	// count below fails rather than silently truncating the comparison.
	j := strings.Index(rest, "\n}")
	if j < 0 {
		t.Fatalf("%s: %q is never closed by a line-initial `}`", where, open)
	}
	return rest[:j]
}

func setOf(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, it := range items {
		s[it] = true
	}
	return s
}

// TestArchAvailabilityShapeIsPinnedToGo pins both hand-written TypeScript
// fixtures to the Go struct the box actually serializes.
func TestArchAvailabilityShapeIsPinnedToGo(t *testing.T) {
	goTags := goStructJSONTags(t, readRepoFile(t, archGoFile), archGoStruct)

	// COVERAGE COUNT, first and unconditionally. Every comparison below is a
	// set difference, and set differences against an empty set are vacuously
	// satisfied — which is precisely how a guard in this repository ends up
	// printing PASS while examining nothing.
	if len(goTags) < archMinFields {
		t.Fatalf("parsed only %d json tags out of %s's %s (%v); the parse has drifted "+
			"and every assertion below would be vacuously true",
			len(goTags), archGoFile, archGoStruct, goTags)
	}

	for _, tsFile := range []string{archFixtureFile, archUnitFile} {
		fields := tsInterfaceFields(t, readRepoFile(t, tsFile), archTSIface, tsFile)

		if len(fields) != len(goTags) {
			t.Errorf("%s declares %d fields on %s and %s declares %d.\n"+
				"go:  %v\nts:  %v",
				tsFile, len(fields), archTSIface, archGoStruct, len(goTags), goTags, fields)
		}

		goSet, tsSet := setOf(goTags), setOf(fields)

		// Go → TS. A field the box sends that the fixture does not model: every
		// spec driven by that fixture measures a card rendered without it.
		for _, tag := range goTags {
			if !tsSet[tag] {
				t.Errorf("%s.%s sends %q and %s's %s does not declare it.\n"+
					"Every App Hub spec using that fixture renders a card the box would "+
					"never send, and passes while doing it.",
					archGoStruct, archGoFile, tag, tsFile, archTSIface)
			}
		}

		// TS → Go. The direction that gets left out. A fixture field the box
		// does not send is a spec asserting behaviour driven by a value that
		// only ever exists in the test — which is this suite's most expensive
		// recurring mistake, a test that asserts its own fabrication.
		for _, f := range fields {
			if !goSet[f] {
				t.Errorf("%s's %s declares %q and the box never sends it (%s.%s has no "+
					"such json tag).\nAny spec relying on it is asserting a value that "+
					"exists only in the fixture.",
					tsFile, archTSIface, f, archGoStruct, archGoFile)
			}
		}
	}
}
