package docsref

// Every image a document shows a reader must exist.
//
// The sibling test in this package checks that SOURCE paths cited in prose
// resolve. Images were never checked, and the gap surfaced the moment someone
// wanted to act on it: a screenshot audit found ~29 MB of committed PNGs that
// nothing references, and could not safely delete any of them, because nothing
// would have caught it if a delete took a live embed with it.
//
// A broken image is also a defect that survives review especially well. Markdown
// renders it as alt text or a small broken icon rather than failing, so on
// GitHub a README with a dead screenshot looks like a README with a slightly
// empty section — which is exactly what a reader takes as evidence the project
// is thin, rather than as a bug someone should fix.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Markdown `![alt](path)` and inline `<img src="path">`. Both appear in this
// repository's docs, so checking only one would leave half the references
// unguarded while reporting a healthy count.
var (
	mdImageRe   = regexp.MustCompile(`!\[[^\]]*\]\(([^)\s]+)`)
	htmlImageRe = regexp.MustCompile(`<img[^>]+src="([^"]+)"`)
)

// imageRefs returns every image path a markdown file points at, with the line
// it appears on.
func imageRefs(body string) map[string]int {
	out := map[string]int{}
	for i, line := range strings.Split(body, "\n") {
		for _, re := range []*regexp.Regexp{mdImageRe, htmlImageRe} {
			for _, m := range re.FindAllStringSubmatch(line, -1) {
				ref := strings.TrimSpace(m[1])
				// Remote images are somebody else's uptime, not this repo's
				// correctness, and a data: URI carries its own bytes.
				if ref == "" || strings.HasPrefix(ref, "http://") ||
					strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "data:") {
					continue
				}
				// Strip a #fragment or a "title" suffix; neither is part of the path.
				if i := strings.IndexAny(ref, "#\""); i >= 0 {
					ref = strings.TrimSpace(ref[:i])
				}
				if ref != "" {
					out[ref] = i + 1
				}
			}
		}
	}
	return out
}

func TestDocImagesResolve(t *testing.T) {
	checked := 0
	var broken []string

	for _, f := range markdownFiles(t) {
		if skipDocs[filepath.Base(f)] {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		docDir := filepath.Dir(f)
		rel := strings.TrimPrefix(filepath.ToSlash(f), filepath.ToSlash(repoRoot)+"/")

		for ref, line := range imageRefs(string(body)) {
			checked++
			// A markdown image path is relative to the document, unless it is
			// rooted — in which case it is relative to the repository, which is
			// how GitHub resolves it.
			target := filepath.Join(docDir, ref)
			if strings.HasPrefix(ref, "/") {
				target = filepath.Join(repoRoot, ref)
			}
			if _, err := os.Stat(target); err != nil {
				broken = append(broken, rel+":"+itoa(line)+" -> "+ref)
			}
		}
	}

	// Vacuity guard, counted rather than assumed. This repository embeds dozens
	// of screenshots; a regex that stopped matching would report a clean run
	// having examined nothing, which is the failure this package exists to
	// prevent in the docs it checks.
	if checked < 20 {
		t.Fatalf("only %d image references found; the scan is broken and this "+
			"check would pass without examining the docs", checked)
	}

	if len(broken) > 0 {
		t.Errorf("%d image reference(s) point at a file that does not exist:\n  %s\n\n"+
			"Markdown renders a dead image as alt text rather than failing, so this "+
			"does not look like a bug to a reader — it looks like an empty section.",
			len(broken), strings.Join(broken, "\n  "))
	}
}
