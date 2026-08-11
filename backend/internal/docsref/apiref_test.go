package docsref

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Every API endpoint the documentation names must appear in the server's source.
//
// # What this proves, and what it does not
//
// It proves the path EXISTS in the backend as a string. It does not prove the
// path is routed: /api/apps appears in appsgate's gatedPrefixes list, so it
// resolves here even though APPS.md says plainly that none of those routes are
// built ("a design, not a built feature" — and the doc is right).
//
// Proving "routed" would mean resolving HandleFunc arguments through constants
// with go/types, because fleetid mounts its endpoints via RegisterHandlers using
// DefaultVouchPath rather than a literal. That is a bigger instrument than the
// defect warrants. What this catches is the common case: an endpoint renamed or
// deleted, where the path then appears nowhere at all.
//
// # Why
//
// A documented endpoint is an instruction: a reader copies it into curl, or
// builds against it. One that was renamed or removed fails at the worst possible
// moment — after someone has written code against it — and nothing in the build
// notices, because prose is not compiled.
//
// The corpus is clean today: 216 endpoints named across docs/, all resolving.
// This keeps it that way, and it is worth having because getting the extraction
// right took several passes. The naive version reported seven failures and all
// seven were notation:
//
//   - `GET /api/disks/breakdown?path=` — a query string, not part of the route
//   - `DELETE /api/developer/keys[/{id}]` — brackets meaning "with or without"
//   - `GET /api/...` — a placeholder in a README, not an endpoint
//   - three entries in CHANGELOG.md, which is HISTORY: it records what a release
//     changed, and an endpoint it named may since have been removed. Holding a
//     changelog to the current server would mean rewriting history to keep a
//     test green.

var (
	// A fenced or inline `METHOD /api/…` or bare `/api/…`.
	methodPathRe = regexp.MustCompile("`(GET|POST|PUT|DELETE|PATCH)\\s+(/api/[^`\\s]+)`")
	barePathRe   = regexp.MustCompile("`(/api/[a-zA-Z0-9/_{}.?=<>\\[\\]-]+)`")
	optionalRe   = regexp.MustCompile(`\[.*?\]`)
	bracketsRe   = regexp.MustCompile(`[\[\]]`)
	declRe       = regexp.MustCompile(`^(GET|POST|PUT|DELETE|PATCH) `)
	paramRe      = regexp.MustCompile(`^\{.*\}$`)
)

// routeDeclarations reads every /api route string the backend registers.
//
// Routes register as mux.HandleFunc("GET /api/thing", …) — the method is INSIDE
// the string — and as mux.HandleFunc("/api/pim/", …), a method-less SUBTREE that
// Go's ServeMux matches by prefix. Reading only the first shape calls the whole
// proxy surface fake.
func routeDeclarations(t *testing.T) []string {
	t.Helper()
	// PARSE the Go sources rather than grepping them.
	//
	// Two grep attempts were both wrong, in opposite directions, and the parser
	// is what makes the question answerable at all:
	//
	//   - Matching any quoted "/api/…" also matched paths inside COMMENTS. One
	//     comment in main.go names /api/meethost/status while explaining that the
	//     endpoint was retired, which made the retired-endpoint check report it as
	//     "served again".
	//   - Matching only `.HandleFunc("…"` fixed that and lost every route
	//     registered through a CONSTANT — fleetid mounts its vouch endpoints via
	//     RegisterHandlers using DefaultVouchPath, so three documented endpoints
	//     with security gates described in a table looked unserved.
	//
	// go/parser walks string literals, which are exactly the right set: constants
	// are literals, and comments are not.
	fset := token.NewFileSet()
	var decls []string
	root := filepath.Join(repoRoot, "backend")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Skip _test.go. Routes are not registered in tests, and scanning them
		// creates a feedback loop: this file's own documentedAsRemoved map holds
		// "/api/meethost/status" as a string literal, which made the check below
		// conclude the retired endpoint was served again — by reading itself.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0) // 0: comments not retained
		if perr != nil {
			return nil // an unparseable file is not this test's problem
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			if strings.HasPrefix(v, "/api/") || declRe.MatchString(v) && strings.Contains(v, "/api/") {
				decls = append(decls, v)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(decls) < 400 {
		t.Fatalf("only %d route declarations were read; the matcher would call every "+
			"documented endpoint fake", len(decls))
	}
	return decls
}

func serverDeclaresPath(decls []string, method, path string) bool {
	segs := strings.Split(path, "/")
	for _, m := range decls {
		// The bare "/api/" registration is the 404/405 FALLBACK. Counting it
		// makes every conceivable path exist and this check vacuous.
		if m == "/api/" {
			continue
		}
		dm, dp := "", m
		if declRe.MatchString(m) {
			parts := strings.SplitN(m, " ", 2)
			dm, dp = parts[0], parts[1]
		}
		if dm != "" && method != "*" && dm != method {
			continue
		}
		// A trailing slash is a subtree match, not an exact one.
		if strings.HasSuffix(dp, "/") && strings.HasPrefix(path, dp) {
			return true
		}
		ds := strings.Split(dp, "/")
		if len(ds) != len(segs) {
			continue
		}
		ok := true
		for i, d := range ds {
			if d != segs[i] && !paramRe.MatchString(d) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// pathVariants turns documentation notation into the route(s) it means.
func pathVariants(raw string) []string {
	p := strings.SplitN(raw, "?", 2)[0] // query strings are not part of the route
	// BEFORE trimming punctuation: TrimRight would turn "/api/..." into "/api"
	// and the placeholder would stop looking like one.
	if strings.Contains(p, "...") || strings.Contains(p, "\u2026") {
		return nil // a placeholder, not an endpoint
	}
	p = strings.TrimRight(p, ".,;:")
	if strings.Contains(p, "[") {
		// `keys[/{id}]` documents BOTH forms; either resolving is enough.
		return []string{optionalRe.ReplaceAllString(p, ""), bracketsRe.ReplaceAllString(p, "")}
	}
	return []string{p}
}

// documentedAsRemoved lists endpoints the docs name in order to say they are
// GONE. NETWORKING.md retires the Meet SFU host registry and names its endpoint
// while doing so, which is good documentation and would otherwise read as a
// broken reference. The inverse assertion below keeps this honest: if the route
// ever comes back, or the doc stops saying it is retired, the pair has drifted.
// designNotBuilt marks a document whose endpoints are deliberately unbuilt.
const designNotBuilt = "not a built feature"

var documentedAsRemoved = map[string]string{
	"/api/meethost/status": "retired",
}

func TestDocumentedAPIEndpointsExist(t *testing.T) {
	decls := routeDeclarations(t)

	// docs/ only. CHANGELOG.md is history — see the note at the top.
	files, err := filepath.Glob(filepath.Join(repoRoot, "docs/*.md"))
	if err != nil || len(files) < 10 {
		t.Fatalf("found %d docs; the corpus moved and this would check almost nothing", len(files))
	}

	checked := 0
	var missing []string
	var designOnly []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		// A document that says its endpoints are NOT BUILT is documenting a
		// design, and naming the routes is the point of it. APPS.md opens its
		// platform section with "a design, not a built feature" and "None of the
		// … routes below exist in this repository's backend". Holding that to the
		// server would punish the doc for being honest.
		//
		// Keyed on the disclaimer rather than the filename, so deleting the
		// disclaimer re-arms every endpoint in the file — which is what should
		// happen the day the feature ships.
		if strings.Contains(string(b), designNotBuilt) {
			designOnly = append(designOnly, filepath.Base(f))
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			type ep struct{ method, path string }
			var found []ep
			for _, m := range methodPathRe.FindAllStringSubmatch(line, -1) {
				found = append(found, ep{m[1], m[2]})
			}
			for _, m := range barePathRe.FindAllStringSubmatch(line, -1) {
				found = append(found, ep{"*", m[1]})
			}
			for _, e := range found {
				variants := pathVariants(e.path)
				if len(variants) == 0 {
					continue
				}
				if _, gone := documentedAsRemoved[variants[0]]; gone {
					continue
				}
				checked++
				ok := false
				for _, v := range variants {
					if serverDeclaresPath(decls, e.method, v) {
						ok = true
						break
					}
				}
				if !ok {
					missing = append(missing,
						filepath.Base(f)+":"+itoa(i+1)+" → "+e.method+" "+e.path)
				}
			}
		}
	}

	if len(designOnly) == 0 {
		t.Errorf("no document carries the %q disclaimer any more. If the Apps platform "+
			"shipped, delete this branch and let its endpoints be checked; if the wording "+
			"changed, update the constant.", designNotBuilt)
	}
	if checked < 100 {
		t.Fatalf("only %d endpoints were extracted from the docs; the extraction is broken, "+
			"so a clean result proves nothing", checked)
	}
	if len(missing) > 0 {
		t.Errorf("the docs name %d API endpoint(s) the server does not serve:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// The inverse: an endpoint documented as RETIRED must still be gone.
//
// If it comes back and the prose still calls it retired, a reader is told a
// capability does not exist while it answers requests — which is worse than a
// missing endpoint, because nothing fails to make anyone look.
func TestEndpointsDocumentedAsRemovedAreStillAbsent(t *testing.T) {
	decls := routeDeclarations(t)
	b, err := os.ReadFile(filepath.Join(repoRoot, "docs/NETWORKING.md"))
	if err != nil {
		t.Fatalf("NETWORKING.md is unreadable (%v); it is the document this pairs with", err)
	}
	src := string(b)

	for path, word := range documentedAsRemoved {
		if !strings.Contains(src, path) {
			t.Errorf("%s is listed here as documented-removed, but NETWORKING.md no longer "+
				"mentions it; the two have drifted apart", path)
			continue
		}
		if !strings.Contains(src, word) {
			t.Errorf("NETWORKING.md no longer describes %s as %q — if it came back, this "+
				"list and the prose both need updating deliberately", path, word)
		}
		if serverDeclaresPath(decls, "*", path) {
			t.Errorf("%s is served again, but the docs still say it is %s. A reader is being "+
				"told a capability does not exist while it answers requests.", path, word)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
