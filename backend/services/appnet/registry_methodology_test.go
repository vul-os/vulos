package appnet

// registry_methodology_test.go — the assertions behind roadmap/INSTALL-METHODOLOGY.md.
//
// Every test here exists because a claim in that document would otherwise be
// prose. "The raw-shell install path is gone" is a structural claim and gets a
// structural check; "an archive lands in extract_dir" is a behavioural claim and
// gets a real extraction. Each was mutation-tested: the mutation, the assertion
// it killed, and the failure text are recorded in the doc.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// 1. The raw-shell path is gone — structurally, not just behaviourally.
// ─────────────────────────────────────────────────────────────────────────────

// TestInstallFromRegistry_ExecsNoInstallShell reads the shipping source and
// asserts the install dispatch cannot run a recipe's shell string or a package
// manager.
//
// A behavioural test cannot prove this on its own. validateRecipeSecurity
// refuses `install` before the dispatch is reached, so a re-added `sh -c
// recipe.Install` branch would be dead code that no black-box test can see —
// right up until somebody relaxes the gate, at which point the shell is back
// and every existing test still passes. The claim being made is about the
// SHAPE of the function, so the check reads the shape.
func TestInstallFromRegistry_ExecsNoInstallShell(t *testing.T) {
	body := installFromRegistryBody(t)

	// `recipe.Install` may be READ (the manifest writer and the gate do), but
	// it must never reach an exec. Anchor on the exec, not the field.
	for _, banned := range []string{
		`recipe.Install`,
		`apt-get`,
		`packages.CacheReady`,
	} {
		if strings.Contains(body, banned) {
			t.Errorf("InstallFromRegistry mentions %q — the raw-shell / apt install path is supposed to be gone "+
				"(INSTALL-01, roadmap/INSTALL-METHODOLOGY.md)", banned)
		}
	}

	// Exactly one `sh -c` may remain in the whole function: post_install, which
	// survives narrowed (POSTINSTALL-02/03). Two would mean the install shell
	// came back next to it.
	if n := strings.Count(body, `"sh", "-c"`); n != 1 {
		t.Errorf("InstallFromRegistry runs %d shell commands, want exactly 1 (post_install only)", n)
	}
	if !strings.Contains(body, "recipe.PostInstall") {
		t.Errorf("the one remaining shell is not post_install — read the dispatch before trusting this test")
	}
}

// installFromRegistryBody returns the source text of InstallFromRegistry with
// comments stripped, so a mention inside a doc comment does not fail the test
// and, more importantly, does not let a real call hide behind one.
func installFromRegistryBody(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("registry.go")
	if err != nil {
		t.Fatalf("read registry.go: %v", err)
	}
	text := string(src)
	start := strings.Index(text, "func InstallFromRegistry(")
	if start < 0 {
		t.Fatal("InstallFromRegistry not found in registry.go — this guard is reading the wrong file")
	}
	rest := text[start:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatal("could not find the end of InstallFromRegistry")
	}
	body := rest[:end]

	var out []string
	for _, line := range strings.Split(body, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		out = append(out, line)
	}
	stripped := strings.Join(out, "\n")
	// Sanity: the guard must be looking at real code. If comment-stripping ever
	// eats the function, everything above passes vacuously.
	if !strings.Contains(stripped, "FlatpakInstall") || !strings.Contains(stripped, "staticInstall") {
		t.Fatalf("the extracted body does not contain the two install vehicles — the guard is not reading code:\n%s", stripped)
	}
	return stripped
}

// ─────────────────────────────────────────────────────────────────────────────
// 2. DOWNLOAD-01 — the single-URL form is refused.
// ─────────────────────────────────────────────────────────────────────────────

func TestDownloadURLFormRefused(t *testing.T) {
	r := &VersionRecipe{
		DownloadURL: "https://example.test/app-linux-amd64.tar.gz",
		Checksum:    strings.Repeat("a", 64),
		Command:     "bin/app",
	}
	err := validateRecipeSecurity(r)
	if err == nil {
		t.Fatal("DOWNLOAD-01 REGRESSION: a single-URL download recipe was ACCEPTED — " +
			"one URL cannot pin two architectures, so the entry is amd64-in-practice while claiming more")
	}
	if !strings.Contains(err.Error(), "DOWNLOAD-01") {
		t.Errorf("expected a DOWNLOAD-01 refusal, got: %v", err)
	}
}

// TestDownloadURLRulesStillReachable is the ordering assertion. DOWNLOAD-01 is
// deliberately the LAST download rule so three older guards keep firing for the
// inputs they were written for. Moving it earlier silently disables them, and
// every one of those older tests would still pass, because they only assert
// "an error happened".
func TestDownloadURLRulesStillReachable(t *testing.T) {
	cases := []struct {
		name   string
		recipe *VersionRecipe
		want   string
	}{
		{
			name:   "missing checksum still reports SECAUDIT2-H1, not DOWNLOAD-01",
			recipe: &VersionRecipe{DownloadURL: "https://example.test/a.tar.gz"},
			want:   "SECAUDIT2-H1",
		},
		{
			name: "both forms still reports the ambiguity, not DOWNLOAD-01",
			recipe: &VersionRecipe{
				DownloadURL: "https://example.test/a.tar.gz",
				Checksum:    strings.Repeat("a", 64),
				Artifacts: map[string]*Artifact{
					"amd64": {DownloadURL: "https://example.test/b", Checksum: strings.Repeat("b", 64)},
				},
			},
			want: "BOTH download_url and artifacts",
		},
		{
			name: "binary_name on an archive still reports the binary_name rule",
			recipe: &VersionRecipe{
				DownloadURL: "https://example.test/a.tar.gz",
				Checksum:    strings.Repeat("a", 64),
				BinaryName:  "a",
			},
			want: "does nothing for an archive",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRecipeSecurity(tc.recipe)
			if err == nil {
				t.Fatalf("expected a refusal mentioning %q, got acceptance", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("DOWNLOAD-01 has been ordered ahead of an older guard: expected %q, got %v", tc.want, err)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3. EXTRACT-01 — extract_dir is screened, and it is actually USED.
// ─────────────────────────────────────────────────────────────────────────────

func TestExtractDirRefusals(t *testing.T) {
	archive := map[string]*Artifact{
		"amd64": {DownloadURL: "https://example.test/a.tar.gz", Checksum: strings.Repeat("a", 64)},
	}
	binary := map[string]*Artifact{
		"amd64": {DownloadURL: "https://example.test/app", Checksum: strings.Repeat("a", 64)},
	}
	cases := []struct {
		name   string
		recipe *VersionRecipe
		want   string
	}{
		{"absolute", &VersionRecipe{Artifacts: archive, ExtractDir: "/etc"}, "relative path"},
		{"traversal", &VersionRecipe{Artifacts: archive, ExtractDir: "../../etc"}, "escapes"},
		{"traversal that cleans back out", &VersionRecipe{Artifacts: archive, ExtractDir: "static/../.."}, "escapes"},
		{"dot", &VersionRecipe{Artifacts: archive, ExtractDir: "."}, "escapes"},
		{"on a single binary", &VersionRecipe{Artifacts: binary, ExtractDir: "static"}, "not an archive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRecipeSecurity(tc.recipe)
			if err == nil {
				t.Fatalf("EXTRACT-01 REGRESSION: extract_dir %q was ACCEPTED", tc.recipe.ExtractDir)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected %q in the refusal, got: %v", tc.want, err)
			}
		})
	}

	// Control: a well-formed extract_dir on an archive must pass. Without this
	// the loop above would be satisfied by refusing extract_dir entirely.
	ok := &VersionRecipe{Artifacts: archive, ExtractDir: "static", Command: "x"}
	if err := validateRecipeSecurity(ok); err != nil {
		t.Fatalf("a valid extract_dir was refused: %v", err)
	}
}

// TestStaticInstall_ExtractDirIsHonoured is the behavioural half, and it is the
// one that matters. A validated field that the installer ignores is per-recipe
// `arch` all over again: it looks like protection and is not.
//
// It also asserts what the field is FOR: the app dir must be left alone, so a
// `--directory static/` server cannot reach data/ (a symlink to the owner's
// data directory) or app.json.
func TestStaticInstall_ExtractDirIsHonoured(t *testing.T) {
	tgz := makeTarGz(t, map[string]string{"top/index.html": "<h1>hi</h1>", "top/app.js": "1"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tgz)
	}))
	defer srv.Close()

	appDir := t.TempDir()
	recipe := &VersionRecipe{
		Artifacts: map[string]*Artifact{
			"amd64": {DownloadURL: srv.URL + "/site.tar.gz", Checksum: sha256Hex(tgz)},
			"arm64": {DownloadURL: srv.URL + "/site.tar.gz", Checksum: sha256Hex(tgz)},
		},
		ArchiveStrip: 1,
		ExtractDir:   "static",
	}
	if err := staticInstall(context.Background(), recipe, appDir); err != nil {
		t.Fatalf("staticInstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appDir, "static", "index.html")); err != nil {
		t.Fatalf("archive did not land in extract_dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appDir, "index.html")); !os.IsNotExist(err) {
		t.Fatalf("archive ALSO landed in the app dir root — extract_dir did not contain it (%v). "+
			"Serving that directory would publish data/ over HTTP.", err)
	}
}

// TestStaticInstall_ExtractDirRefusesEscapeAtInstallTime re-checks the value at
// the line that turns it into a path, not only at the gate. staticInstall is
// exported to the package and called directly by tests and by the verifier
// driver; a screen that lives only in validateRecipeSecurity is one call site
// away from being bypassed.
func TestStaticInstall_ExtractDirRefusesEscapeAtInstallTime(t *testing.T) {
	// The ORDER is asserted by counting requests, not by reading the error
	// text. An error message is a description of what happened; a request
	// counter is the thing itself. (Measured: an earlier version of this test
	// asserted on the message and a mutation that moved the screen after the
	// download survived it, because the containment check still produced an
	// EXTRACT-01 error — just one HTTP fetch too late.)
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte("payload"))
	}))
	defer srv.Close()

	appDir := t.TempDir()
	recipe := &VersionRecipe{
		Artifacts: map[string]*Artifact{
			"amd64": {DownloadURL: srv.URL + "/a.tar.gz", Checksum: sha256Hex([]byte("payload"))},
			"arm64": {DownloadURL: srv.URL + "/a.tar.gz", Checksum: sha256Hex([]byte("payload"))},
		},
		ExtractDir: "../escaped",
	}
	err := staticInstall(context.Background(), recipe, appDir)
	if err == nil {
		t.Fatal("staticInstall accepted an escaping extract_dir")
	}
	if !strings.Contains(err.Error(), "EXTRACT-01") && !strings.Contains(err.Error(), "escapes") {
		t.Errorf("expected an extract_dir refusal, got: %v", err)
	}
	if hits != 0 {
		t.Errorf("extract_dir was screened only AFTER %d download(s) — the box has already fetched "+
			"an attacker-chosen payload by the time it refuses", hits)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(appDir), "escaped")); !os.IsNotExist(statErr) {
		t.Errorf("extract_dir created a directory outside the app dir: %v", statErr)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. .war is a zip, and routing it anywhere else installs a broken app quietly.
// ─────────────────────────────────────────────────────────────────────────────

func TestWarIsExtractedAsAZip(t *testing.T) {
	war := makeZip(t, map[string]string{"index.html": "<html>drawio</html>", "js/app.js": "//"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(war)
	}))
	defer srv.Close()

	appDir := t.TempDir()
	recipe := &VersionRecipe{
		Artifacts: map[string]*Artifact{
			"amd64": {DownloadURL: srv.URL + "/draw.war", Checksum: sha256Hex(war)},
			"arm64": {DownloadURL: srv.URL + "/draw.war", Checksum: sha256Hex(war)},
		},
		ExtractDir: "static",
	}
	if err := staticInstall(context.Background(), recipe, appDir); err != nil {
		t.Fatalf("staticInstall on a .war: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appDir, "static", "index.html")); err != nil {
		t.Fatalf(".war was not extracted: %v", err)
	}
	// The failure this replaces: the file was copied whole into bin/ with mode
	// 0755 and the install reported success.
	if _, err := os.Stat(filepath.Join(appDir, "bin", "draw.war")); !os.IsNotExist(err) {
		t.Fatal(".war landed in bin/ as a single 'binary' — that is the silent broken install this closes")
	}
}

// TestWarCountsAsAnArchiveForBinaryName pins the other half: if isZipURL knows
// about .war but isArchiveURL does not, binary_name on a .war would be silently
// ignored rather than refused.
func TestWarCountsAsAnArchiveForBinaryName(t *testing.T) {
	r := &VersionRecipe{
		BinaryName: "draw",
		Artifacts:  map[string]*Artifact{"amd64": {DownloadURL: "https://example.test/draw.war", Checksum: strings.Repeat("a", 64)}},
	}
	if err := validateRecipeSecurity(r); err == nil {
		t.Fatal("binary_name on a .war was accepted — it would be silently ignored at install time")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 5. The registry data itself, as far as it can be checked without editing it.
// ─────────────────────────────────────────────────────────────────────────────

// TestStagedRegistryFragmentsUseOnlyTheTwoVehicles walks registry.d/, which is
// where every proposed entry is staged for the single writer of registry.json,
// and asserts each recipe passes the same gate the box applies.
//
// registry.json itself is NOT asserted here: it still holds the unmigrated
// entries, it has exactly one writer, and a test that went green only after
// somebody else's merge would be a test about the future.
func TestStagedRegistryFragmentsUseOnlyTheTwoVehicles(t *testing.T) {
	dir := ""
	for _, c := range []string{"../../../registry.d", "../../../../registry.d"} {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			dir = c
			break
		}
	}
	if dir == "" {
		t.Skip("registry.d not found relative to the test directory")
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read registry.d: %v", err)
	}
	checked := 0
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		frag, err := LoadRegistry(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Errorf("%s: does not parse as a registry fragment: %v", e.Name(), err)
			continue
		}
		for appID, entry := range frag.Apps {
			for ver, recipe := range entry.Versions {
				checked++
				if recipe.Disabled || entry.Disabled {
					continue
				}
				if err := validateRecipeSecurity(recipe); err != nil {
					t.Errorf("%s: %s@%s does not satisfy the install methodology: %v", e.Name(), appID, ver, err)
				}
			}
		}
	}
	// A staging directory that silently emptied would otherwise pass.
	if checked == 0 {
		t.Fatal("no staged recipes were checked — the fragment loader found nothing")
	}
	t.Logf("checked %d staged recipes in registry.d/", checked)
}

// TestOnlyTwoVehiclesAreDocumented keeps the doc honest about the code. If a
// third vehicle is ever added to the dispatch, this fails until the standard is
// updated — the reverse of the usual drift, where the code grows and the
// document does not.
func TestOnlyTwoVehiclesAreDocumented(t *testing.T) {
	body := installFromRegistryBody(t)
	installers := regexp.MustCompile(`(FlatpakInstall|staticInstall)\(`).FindAllString(body, -1)
	if len(installers) != 2 {
		t.Fatalf("InstallFromRegistry calls %d installers %v, want exactly 2 "+
			"(FlatpakInstall, staticInstall) — roadmap/INSTALL-METHODOLOGY.md documents two vehicles",
			len(installers), installers)
	}
}

// ── helpers ─────────────────────────────────────────────────────────────────

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// makeTarGz builds a gzipped tar in memory, so an extraction test does not
// depend on which tar the host machine ships (PER-ARCH-ARTIFACTS §3).
func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("tar header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}
