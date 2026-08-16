package docsref

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A bundled app must address its own API relative to the mount point it is
// actually served from — never with an absolute path.
//
// # The defect
//
// The gateway serves a bundled app from two different places, decided by one
// server-side flag (backend/services/gateway/gateway.go, Handler):
//
//	per-app origins ON   →  {app}--{profile}.{base}[:port]/…   app sits at the root
//	per-app origins OFF  →  {base}[:port]/app/{app}/…          app sits under a prefix
//
// In both shapes the app's own server only ever sees the path AFTER the mount:
// Handler sets appPath to r.URL.Path on an app origin, and to what remains once
// "/app/{id}" is stripped on the path prefix. So the app's server implements
// "/api/info" and the browser must ask for "/app/system-info/api/info" in the
// second shape.
//
// OFF is the DEFAULT — frontend/src/core/AppOrigins.ts starts at
// {enabled:false} and setOriginConfig additionally requires a non-empty
// base_domain — so the shape where an absolute path is wrong is the shape a box
// runs unless someone configures a base domain. An absolute "/api/info" from
// the frame leaves the app's mount entirely and lands on the SHELL's origin,
// where it is a 404 or, worse, some unrelated shell endpoint that answers.
//
// # Why a guard and not just the eleven fixes
//
// Eleven apps had it, forty-seven call sites. Fixing eleven files fixes eleven
// files; the twelfth app is written next week by someone who reasonably assumes
// an app served at a root can use root-absolute URLs, and every existing test
// stays green because a mocked backend answers whatever path it is asked. That
// is exactly how this shipped: the app looked broken while the backend was
// healthy, and nothing in the build could tell the difference.
//
// So the invariant is asserted over the whole directory instead: every absolute
// app-asset path in every bundled app goes through vulosApi.appUrl, or is named
// in boxAPIEscapes below as a call on the BOX's backend rather than the app's.
//
// # What this proves, and what it does not
//
// It proves no bundled app SOURCE carries an unresolved absolute path, and that
// the resolver every app carries is byte-identical to the one shared source.
// It does not prove the resolver is correct — that is the browser's job, and
// frontend/e2e/bundled-apps-api-base.e2e.ts loads the real helper into a real
// sandboxed frame at both mount shapes and asserts the URL that actually goes
// out on the wire.

const bundledAppsDir = "frontend/apps"

// sharedHelperRel is the single source of truth every app inlines.
const sharedHelperRel = bundledAppsDir + "/_shared/vulos-api.js"

// helperBlockRe matches the inlined copy in an app's index.html.
var helperBlockRe = regexp.MustCompile(`(?s)<script data-vulos-shared="vulos-api\.js">\n(.*?)</script>\n`)

// absPathRe finds an absolute path that names an app asset. The alternatives are
// the mount-relative roots the bundled servers actually serve: /api/ (every
// app), /media/ (gallery), /audio/ (music, voice-recorder), /stream/ (video).
var absPathRe = regexp.MustCompile(`/(?:api|media|audio|stream)/[A-Za-z0-9_./${}()+-]*`)

// resolvedRe counts call sites that went through the resolver.
var resolvedRe = regexp.MustCompile(`vulosApi\.appUrl\(`)

// lineCommentRe strips `// …` to end of line. The excluded lead characters are
// the ones that mean this `//` is inside a URL and not a comment: a colon ends a
// scheme (`https://`), and a closing brace ends a template substitution
// (`${proto}//${host}/api/…`, which is how text-editor builds its collab
// WebSocket URL). Eating that one would have hidden a real absolute path.
var lineCommentRe = regexp.MustCompile("(?m)([^:'\"`\\w}]|^)//[^\n]*")

// boxAPIEscapes lists, per app, the absolute-path prefixes that are deliberately
// NOT resolved against the app's mount, because they name routes on the BOX's
// backend and not on the app's own server.
//
// These are not exemptions, they are a filed defect. Each app's server.py was
// read: none of these paths appears in it, so nothing behind the app's mount can
// answer them. On the path prefix they reach the shell's origin from a frame
// that AppOrigins deliberately denies allow-same-origin, so they arrive with
// Origin: null and no credentials; on an app origin they reach the app's own
// server and 404. They are broken in both shapes and the fix is for the app's
// server to proxy them the way system-info and text-editor already proxy theirs
// (system-info/server.py `_fetch(VULOS_API + path)`), not a URL rewrite.
//
// The table is asserted EXACT below: a twelfth app cannot quietly join it, and
// an app that gains a new box-API call fails until the call is either proxied or
// consciously added here.
var boxAPIEscapes = map[string][]string{
	"notes":       {"/api/peering/profile"},
	"phone":       {"/api/telephony/"},
	"weather":     {"/api/proxy/"},
	"text-editor": {"/api/peering/collab/${encodeURIComponent(docId)}/sync"},
}

// Coverage floors. These exist because the cheapest way for a scanner to report
// success is to scan nothing: a broken glob, a regex that stopped matching after
// a refactor, or a directory that moved would all produce "no violations found".
const (
	minBundledApps   = 15 // 16 today; a drop means the walk lost the tree
	minResolvedSites = 61 // 61 today across 11 apps
	minHelperCopies  = 11 // the eleven apps with an own API
)

func bundledAppIndexes(t *testing.T) map[string]string {
	t.Helper()
	dirs, err := os.ReadDir(filepath.Join(repoRoot, bundledAppsDir))
	if err != nil {
		t.Fatalf("read %s: %v", bundledAppsDir, err)
	}
	out := map[string]string{}
	for _, d := range dirs {
		if !d.IsDir() || d.Name() == "_shared" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(repoRoot, bundledAppsDir, d.Name(), "index.html"))
		if err != nil {
			continue
		}
		out[d.Name()] = string(b)
	}
	if len(out) < minBundledApps {
		t.Fatalf("found %d bundled apps under %s, expected at least %d — the walk has lost "+
			"its subject and would report success while checking nothing",
			len(out), bundledAppsDir, minBundledApps)
	}
	return out
}

// stripHelper removes the inlined resolver, whose own prose names example paths.
func stripHelper(src string) string {
	return helperBlockRe.ReplaceAllString(src, "")
}

func TestBundledAppsResolveTheirOwnAPIAgainstTheirMount(t *testing.T) {
	apps := bundledAppIndexes(t)

	names := make([]string, 0, len(apps))
	for n := range apps {
		names = append(names, n)
	}
	sort.Strings(names)

	resolved := 0
	seenEscapes := map[string]map[string]bool{}

	for _, app := range names {
		body := lineCommentRe.ReplaceAllString(stripHelper(apps[app]), "$1")
		resolved += len(resolvedRe.FindAllString(body, -1))

		for _, loc := range absPathRe.FindAllStringIndex(body, -1) {
			path := body[loc[0]:loc[1]]
			// Already resolved? The resolver call sits immediately before the
			// path, with at most one opening quote between them.
			lo := loc[0] - 16
			if lo < 0 {
				lo = 0
			}
			if strings.Contains(body[lo:loc[0]], "appUrl(") {
				continue
			}
			escaped := false
			for _, pfx := range boxAPIEscapes[app] {
				if strings.HasPrefix(path, pfx) {
					if seenEscapes[app] == nil {
						seenEscapes[app] = map[string]bool{}
					}
					seenEscapes[app][pfx] = true
					escaped = true
					break
				}
			}
			if escaped {
				continue
			}
			line := 1 + strings.Count(body[:loc[0]], "\n")
			t.Errorf("%s/%s/index.html:~%d asks for the absolute path %q.\n"+
				"With per-app origins OFF (the default) the app is served at /app/%s/, so that "+
				"path leaves the app's mount and hits the SHELL's origin instead of the app's "+
				"own server. Wrap it: vulosApi.appUrl(%q). If it really names a route on the "+
				"BOX's backend rather than this app's, proxy it through %s/server.py the way "+
				"system-info does, or record it in boxAPIEscapes with a reason.",
				bundledAppsDir, app, line, path, app, path, app)
		}
	}

	if resolved < minResolvedSites {
		t.Errorf("only %d call sites go through vulosApi.appUrl, expected at least %d. "+
			"Either the resolver was removed from the apps or this scan stopped matching; "+
			"a zero here would otherwise read as a clean bill of health.",
			resolved, minResolvedSites)
	}

	// The escape table must be exactly the escapes actually taken: a stale entry
	// is a licence nobody is using, and a licence nobody is using is one a future
	// call site inherits for free.
	for app, pfxs := range boxAPIEscapes {
		for _, p := range pfxs {
			if !seenEscapes[app][p] {
				t.Errorf("boxAPIEscapes[%q] licenses %q but no such path is left in the app. "+
					"Drop the entry — the next absolute path added under that prefix would "+
					"otherwise pass unnoticed.", app, p)
			}
		}
	}
}

func TestEveryAppCarriesTheSharedResolverVerbatim(t *testing.T) {
	helper, err := os.ReadFile(filepath.Join(repoRoot, sharedHelperRel))
	if err != nil {
		t.Fatalf("read %s: %v", sharedHelperRel, err)
	}
	src := string(helper)
	// A gutted resolver would still be copied identically into every app and
	// this check would still pass, so the resolver's own substance is pinned.
	for _, must := range []string{"function appUrl", "function mountBase", `'/app/'`, "replace(/^\\/+/"} {
		if !strings.Contains(src, must) {
			t.Fatalf("%s no longer contains %q; the shared resolver is not the resolver these "+
				"apps are being checked against", sharedHelperRel, must)
		}
	}

	apps := bundledAppIndexes(t)
	copies := 0
	for app, body := range apps {
		usesHelper := resolvedRe.MatchString(stripHelper(body))
		m := helperBlockRe.FindStringSubmatch(body)
		if m == nil {
			if usesHelper {
				t.Errorf("%s calls vulosApi.appUrl but carries no inlined vulos-api.js block; "+
					"run `node %s/_shared/sync-shared-assets.mjs`", app, bundledAppsDir)
			}
			continue
		}
		copies++
		if m[1] != src {
			t.Errorf("%s/index.html's inlined vulos-api.js has drifted from %s "+
				"(%d bytes inlined vs %d bytes shared). The copies are mechanical: run "+
				"`node %s/_shared/sync-shared-assets.mjs`.",
				app, sharedHelperRel, len(m[1]), len(src), bundledAppsDir)
		}
	}
	if copies < minHelperCopies {
		t.Errorf("only %d apps carry the shared resolver, expected at least %d — this check "+
			"would pass trivially on an empty set", copies, minHelperCopies)
	}
}
