package docsref

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every bundled app must carry the shared theme resolver and the shared design
// tokens, in that order, ahead of its own stylesheet — and the tokens must
// actually define both themes.
//
// # What was wrong
//
// Measured before this change: zero of the fifteen bundled apps carried a
// data-theme rule or a prefers-color-scheme query, and apps/_shared/
// vulos-tokens.css — whose own header claimed to mirror src/index.css, which
// themes properly — had zero theme rules and a single hardcoded dark palette
// two generations stale. Only four of the fifteen apps even linked it; the
// other eleven would have 404'd the link, which is presumably why they never
// got one.
//
// # The three things that can silently undo it
//
//  1. ORDER. The shared :root and an app's own :root have identical
//     specificity, so whichever comes last wins. Emitted after the app's style
//     the shared palette would repaint fifteen hand-designed apps; emitted
//     before it, it fills in only what the app does not define. The resolver
//     must also precede the tokens that read the attribute it stamps. Nothing
//     about that ordering is visible in a diff of the sync script.
//  2. DRIFT. Fifteen inlined copies is fifteen chances to hand-edit one.
//  3. A HALF PALETTE. A colour whose ONLY definition lives inside
//     [data-theme="light"] or a prefers-color-scheme block is undefined in the
//     other theme — the app renders that one property unstyled, which reads as
//     a rendering bug rather than a missing token. This is the same trap the
//     suite's composited-contrast work has hit before, and a human reading the
//     CSS will not spot it: both blocks look complete on their own.
//
// # What this proves, and what it does not
//
// It proves the structure: present, identical, ordered, and complete in both
// themes. It does not prove any app LOOKS right in light mode — most bundled
// apps still define their own hardcoded palette locally, which by design still
// wins, and converting those is per-app work. It also does not prove the shell
// ever sends the theme: appFrameSrc does not yet add __vulos_t, so today every
// app falls through to prefers-color-scheme. Both are stated in the report
// rather than asserted here, because a test that claimed either would be lying.

const (
	sharedThemeRel  = bundledAppsDir + "/_shared/vulos-theme.js"
	sharedTokensRel = bundledAppsDir + "/_shared/vulos-tokens.css"
)

var (
	themeBlockRe  = regexp.MustCompile(`(?s)<script data-vulos-shared="vulos-theme\.js">\n(.*?)</script>\n`)
	tokensBlockRe = regexp.MustCompile(`(?s)<style data-vulos-shared="vulos-tokens\.css">\n(.*?)</style>\n`)
	// The app's own stylesheet: a <style> with no data-vulos-shared attribute.
	ownStyleRe = regexp.MustCompile(`<style(?:>|\s+(?:[^>]*[^-]|)>)`)
	// A CSS custom property declaration.
	cssVarRe = regexp.MustCompile(`(?m)^\s*(--[a-z0-9-]+)\s*:`)
)

// osApps is every bundled app except site-template, which is the scaffold a
// user's own website is built from and deliberately carries no OS chrome.
func osApps(t *testing.T) map[string]string {
	t.Helper()
	all := bundledAppIndexes(t)
	delete(all, "site-template")
	if len(all) < minThemedApps {
		t.Fatalf("only %d OS apps found, expected at least %d", len(all), minThemedApps)
	}
	return all
}

const minThemedApps = 15

func TestEveryBundledAppCarriesTheThemeResolverAndTokensInOrder(t *testing.T) {
	theme, err := os.ReadFile(filepath.Join(repoRoot, sharedThemeRel))
	if err != nil {
		t.Fatalf("read %s: %v", sharedThemeRel, err)
	}
	tokens, err := os.ReadFile(filepath.Join(repoRoot, sharedTokensRel))
	if err != nil {
		t.Fatalf("read %s: %v", sharedTokensRel, err)
	}
	// Neither asset may be gutted: fifteen identical copies of nothing would
	// satisfy every byte-identity check below.
	if !strings.Contains(string(theme), "data-theme") || !strings.Contains(string(theme), "__vulos_t") {
		t.Fatalf("%s no longer reads __vulos_t or stamps data-theme; it is not the resolver "+
			"these apps are being checked against", sharedThemeRel)
	}
	if !strings.Contains(string(tokens), `[data-theme="light"]`) {
		t.Fatalf("%s has no light theme; the apps would be carrying a dark-only palette again",
			sharedTokensRel)
	}

	apps := osApps(t)
	names := make([]string, 0, len(apps))
	for n := range apps {
		names = append(names, n)
	}
	sort.Strings(names)

	themed, tokened, ordered := 0, 0, 0
	for _, app := range names {
		body := apps[app]

		tm := themeBlockRe.FindStringSubmatchIndex(body)
		km := tokensBlockRe.FindStringSubmatchIndex(body)
		if tm == nil {
			t.Errorf("%s/index.html carries no vulos-theme.js block, so nothing can ever tell it "+
				"the shell's theme; run `node %s/_shared/sync-shared-assets.mjs`", app, bundledAppsDir)
			continue
		}
		if km == nil {
			t.Errorf("%s/index.html carries no vulos-tokens.css block; run "+
				"`node %s/_shared/sync-shared-assets.mjs`", app, bundledAppsDir)
			continue
		}
		themed++
		tokened++

		if body[tm[2]:tm[3]] != string(theme) {
			t.Errorf("%s/index.html's inlined vulos-theme.js has drifted from %s (%d bytes vs %d); "+
				"the copies are mechanical, run `node %s/_shared/sync-shared-assets.mjs`",
				app, sharedThemeRel, tm[3]-tm[2], len(theme), bundledAppsDir)
		}
		if body[km[2]:km[3]] != string(tokens) {
			t.Errorf("%s/index.html's inlined vulos-tokens.css has drifted from %s (%d bytes vs %d); "+
				"the copies are mechanical, run `node %s/_shared/sync-shared-assets.mjs`",
				app, sharedTokensRel, km[3]-km[2], len(tokens), bundledAppsDir)
		}

		// ORDER: resolver, then tokens, then the app's own stylesheet. Both
		// :root blocks have the same specificity, so this is the only thing
		// deciding which palette an app ends up with.
		if tm[0] > km[0] {
			t.Errorf("%s/index.html puts the tokens before the theme resolver; the resolver stamps "+
				"the data-theme attribute the tokens select on, so it has to run first", app)
		}
		own := -1
		for _, loc := range ownStyleRe.FindAllStringIndex(body, -1) {
			if strings.Contains(body[loc[0]:loc[1]], "data-vulos-shared") {
				continue
			}
			own = loc[0]
			break
		}
		if own < 0 {
			// An app with no stylesheet of its own has nothing to be overridden.
			ordered++
			continue
		}
		if km[1] > own {
			t.Errorf("%s/index.html emits the shared tokens AFTER its own <style> at offset %d. "+
				"Both are :root at identical specificity, so the shared palette would override the "+
				"app's own — repainting a hand-designed app as a side effect of a sync.", app, own)
			continue
		}
		ordered++
	}

	if themed < minThemedApps || tokened < minThemedApps || ordered < minThemedApps {
		t.Errorf("coverage: %d apps with the resolver, %d with the tokens, %d correctly ordered; "+
			"expected at least %d of each. A zero in any column would otherwise read as a pass.",
			themed, tokened, ordered, minThemedApps)
	}

	// The mechanism this replaced 404'd in eleven of fifteen apps.
	for _, app := range names {
		if strings.Contains(apps[app], `href="vulos-tokens.css"`) {
			t.Errorf("%s/index.html still links vulos-tokens.css. Only four apps' server.py ever "+
				"served that path; in the rest it is a 404 and an unstyled paint.", app)
		}
	}
}

func TestSharedTokensDefineBothThemesWithNoHalfPalette(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, sharedTokensRel))
	if err != nil {
		t.Fatalf("read %s: %v", sharedTokensRel, err)
	}
	css := string(b)

	// The four layers src/index.css uses, in the same order.
	for _, layer := range []string{
		"\n:root {",
		`[data-theme="dark"] {`,
		`[data-theme="light"] {`,
		"@media (prefers-color-scheme: light) {",
		`:root:not([data-theme])`,
	} {
		if !strings.Contains(css, layer) {
			t.Fatalf("%s is missing the %q layer. Without all four an app is either dark-only, "+
				"or unable to honour an explicit choice, or flashes the wrong theme before the "+
				"attribute is stamped.", sharedTokensRel, layer)
		}
	}

	// The fallback must be scoped so an explicitly stamped attribute wins.
	fb := strings.Index(css, "@media (prefers-color-scheme: light) {")
	if fb < 0 || !strings.Contains(css[fb:], `:root:not([data-theme])`) {
		t.Fatalf("%s applies its prefers-color-scheme fallback to a bare :root. It would then "+
			"beat [data-theme=\"dark\"] on a system-light machine and an explicit Dark choice "+
			"would be ignored.", sharedTokensRel)
	}

	// NO HALF PALETTE: every custom property declared inside any conditional
	// block must also be declared in the unconditional :root, so no colour's
	// only definition can vanish with the theme.
	rootStart := strings.Index(css, "\n:root {")
	if rootStart < 0 {
		t.Fatal("no bare :root block")
	}
	rootEnd := strings.Index(css[rootStart:], "\n}")
	if rootEnd < 0 {
		t.Fatal("unterminated :root block")
	}
	base := map[string]bool{}
	for _, m := range cssVarRe.FindAllStringSubmatch(css[rootStart:rootStart+rootEnd], -1) {
		base[m[1]] = true
	}
	if len(base) < 30 {
		t.Fatalf("only %d custom properties parsed out of the bare :root block; the matcher is "+
			"broken and every conditional token would look 'already defined'", len(base))
	}

	checked := 0
	for _, blk := range []string{`[data-theme="dark"] {`, `[data-theme="light"] {`, `:root:not([data-theme]) {`} {
		i := strings.Index(css, blk)
		if i < 0 {
			t.Fatalf("block %q vanished between checks", blk)
		}
		rest := css[i:]
		end := strings.Index(rest, "\n  }")
		if end < 0 {
			end = strings.Index(rest, "\n}")
		}
		if end < 0 {
			t.Fatalf("unterminated block %q", blk)
		}
		vars := cssVarRe.FindAllStringSubmatch(rest[:end], -1)
		if len(vars) < 15 {
			t.Fatalf("block %q declares only %d custom properties; a theme that restates almost "+
				"nothing is not a theme", blk, len(vars))
		}
		for _, m := range vars {
			checked++
			if !base[m[1]] {
				t.Errorf("%s is defined in %s but NOT in the unconditional :root. In the other "+
					"theme it resolves to nothing and whatever uses it renders unstyled — which "+
					"reads as a rendering bug, not a missing token.", m[1], blk)
			}
		}
	}
	if checked < 60 {
		t.Errorf("only %d theme-block declarations examined; expected at least 60. This check "+
			"passes trivially on an empty scan.", checked)
	}
}
