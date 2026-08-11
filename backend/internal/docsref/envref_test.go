package docsref

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Environment variables named in the docs must exist, and the ones the code
// reads should be findable in the docs.
//
// CONFIGURATION.md is a reference: someone sets what it lists and expects the
// box to behave. A variable that was renamed leaves them setting nothing, with
// no error — the box just keeps its default and they conclude the feature is
// broken.

var envNameRe = regexp.MustCompile(`VULOS_[A-Z0-9_]+`)

// envNamesInCode returns every VULOS_* string literal the backend contains,
// excluding tests. Literals rather than os.Getenv call sites, because several
// are read through constants — the same lesson the API check learned twice.
func envNamesInCode(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	found := map[string]bool{}
	root := filepath.Join(repoRoot, "backend")
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil // tests set fixtures; they are not the product's surface
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if v, uerr := strconv.Unquote(lit.Value); uerr == nil {
				for _, name := range envNameRe.FindAllString(v, -1) {
					found[name] = true
				}
			}
			return true
		})
		return nil
	})
	if len(found) < 80 {
		t.Fatalf("only %d VULOS_* names were read from the backend; the scan is broken and "+
			"a clean result would prove nothing", len(found))
	}
	return found
}

// documentedAsGone lists variables the docs name in order to say they are GONE.
// ARCHITECTURE.md records that isolated browsing and its flag were removed,
// which is the doc doing its job and would otherwise read as a dead reference.
var documentedAsGone = map[string]string{
	"VULOS_ENABLE_ISOLATED_BROWSER": "have been removed",
}

func TestDocumentedEnvVarsExist(t *testing.T) {
	inCode := envNamesInCode(t)

	// Anywhere outside docs/ counts: some variables are consumed by build.sh,
	// systemd units or CI rather than by Go, and a doc naming one of those is
	// not wrong.
	elsewhere := map[string]bool{}
	_ = filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		base := filepath.Base(path)
		if d.IsDir() && (base == ".git" || base == "node_modules" || base == "docs") {
			return filepath.SkipDir
		}
		if d.IsDir() || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil || len(b) > 4<<20 {
			return nil
		}
		for _, name := range envNameRe.FindAllString(string(b), -1) {
			elsewhere[name] = true
		}
		return nil
	})

	docs, err := filepath.Glob(filepath.Join(repoRoot, "docs/*.md"))
	if err != nil || len(docs) < 10 {
		t.Fatalf("found %d docs; the corpus moved", len(docs))
	}

	documented := map[string][]string{}
	for _, f := range docs {
		b, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatalf("read %s: %v", f, rerr)
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, m := range regexp.MustCompile("`(VULOS_[A-Z0-9_]+)`").FindAllStringSubmatch(line, -1) {
				documented[m[1]] = append(documented[m[1]], filepath.Base(f)+":"+itoa(i+1))
			}
		}
	}
	if len(documented) < 100 {
		t.Fatalf("only %d env vars were extracted from the docs; the extraction is broken",
			len(documented))
	}

	var dead []string
	for name, where := range documented {
		if _, gone := documentedAsGone[name]; gone {
			continue
		}
		if inCode[name] || elsewhere[name] {
			continue
		}
		dead = append(dead, name+" ("+where[0]+")")
	}
	sort.Strings(dead)
	if len(dead) > 0 {
		t.Errorf("the docs tell a reader to set %d variable(s) nothing reads:\n  %s",
			len(dead), strings.Join(dead, "\n  "))
	}
}

// The inverse: a variable documented as REMOVED must stay removed, and the doc
// must keep saying so. If the flag comes back while ARCHITECTURE.md still says
// it was removed, an operator is told a switch does not exist while it works.
func TestEnvVarsDocumentedAsGoneAreStillGone(t *testing.T) {
	inCode := envNamesInCode(t)
	b, err := os.ReadFile(filepath.Join(repoRoot, "docs/ARCHITECTURE.md"))
	if err != nil {
		t.Fatalf("ARCHITECTURE.md unreadable: %v", err)
	}
	src := string(b)

	for name, phrase := range documentedAsGone {
		if !strings.Contains(src, name) {
			t.Errorf("%s is listed here as documented-removed but ARCHITECTURE.md no longer "+
				"mentions it; the two have drifted", name)
			continue
		}
		if !strings.Contains(src, phrase) {
			t.Errorf("ARCHITECTURE.md no longer says %s was %q", name, phrase)
		}
		if inCode[name] {
			t.Errorf("%s is read by the backend again, but ARCHITECTURE.md still says it was "+
				"removed. An operator is being told a switch does not exist while it works.", name)
		}
	}
}
