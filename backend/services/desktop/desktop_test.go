package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ──────────────────────────────────────────────────────────────────────────────
// Fixtures
// ──────────────────────────────────────────────────────────────────────────────

const fixtureFirefox = `[Desktop Entry]
Version=1.0
Type=Application
Name=Firefox Web Browser
Comment=Browse the World Wide Web
Exec=firefox %u
Icon=firefox
Categories=Network;WebBrowser;
MimeType=text/html;text/xml;application/xhtml+xml;
Terminal=false
NoDisplay=false
`

const fixtureTerminal = `[Desktop Entry]
Type=Application
Name=Terminal
Exec=xterm -e bash %F
Icon=utilities-terminal
Categories=System;TerminalEmulator;
Terminal=true
`

const fixtureNoDisplay = `[Desktop Entry]
Type=Application
Name=Hidden App
Exec=hiddenapp
NoDisplay=true
`

const fixtureNonApplication = `[Desktop Entry]
Type=Link
Name=Some Link
Exec=doesnotmatter
`

const fixtureMultipleFieldCodes = `[Desktop Entry]
Type=Application
Name=Editor
Exec=gedit %F %u %i %c %k --new-window
Icon=gedit
Categories=TextEditor;Utility;
`

const fixtureIgnoresOtherSection = `[Desktop Entry]
Type=Application
Name=Real App
Exec=realapp
Icon=realapp

[Other Section]
Name=Should Not Override
Exec=badexec
`

// writeFixture writes content to a temp .desktop file and returns its path.
func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name+".desktop")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writeFixture: %v", err)
	}
	return path
}

// ──────────────────────────────────────────────────────────────────────────────
// parseDesktopFile — fixture-driven tests
// ──────────────────────────────────────────────────────────────────────────────

func TestParseDesktopFile_Firefox(t *testing.T) {
	path := writeFixture(t, "firefox", fixtureFirefox)
	e, err := parseDesktopFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if e.Name != "Firefox Web Browser" {
		t.Errorf("Name = %q, want %q", e.Name, "Firefox Web Browser")
	}
	if e.Comment != "Browse the World Wide Web" {
		t.Errorf("Comment = %q", e.Comment)
	}
	// Field code %u must be stripped.
	if strings.Contains(e.Exec, "%") {
		t.Errorf("Exec contains field code: %q", e.Exec)
	}
	if e.Exec != "firefox" {
		t.Errorf("Exec = %q, want %q", e.Exec, "firefox")
	}
	if e.Icon != "firefox" {
		t.Errorf("Icon = %q, want %q", e.Icon, "firefox")
	}
	if e.Terminal {
		t.Error("Terminal should be false")
	}
	if e.NoDisplay {
		t.Error("NoDisplay should be false")
	}
	if e.Type != "desktop" {
		t.Errorf("Type = %q, want %q", e.Type, "desktop")
	}

	// ID is derived from the file name without extension.
	if e.ID != "firefox" {
		t.Errorf("ID = %q, want %q", e.ID, "firefox")
	}

	// Categories
	wantCats := map[string]bool{"Network": true, "WebBrowser": true}
	for _, c := range e.Categories {
		delete(wantCats, c)
	}
	if len(wantCats) > 0 {
		t.Errorf("missing categories: %v", wantCats)
	}

	// MimeTypes
	found := false
	for _, m := range e.MimeTypes {
		if m == "text/html" {
			found = true
		}
	}
	if !found {
		t.Error("expected text/html in MimeTypes")
	}
}

func TestParseDesktopFile_Terminal(t *testing.T) {
	path := writeFixture(t, "xterm", fixtureTerminal)
	e, err := parseDesktopFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !e.Terminal {
		t.Error("Terminal should be true")
	}
	// Field code %F stripped; remaining args preserved.
	if strings.Contains(e.Exec, "%") {
		t.Errorf("Exec still has field code: %q", e.Exec)
	}
	if !strings.Contains(e.Exec, "xterm") {
		t.Errorf("Exec should contain binary name, got: %q", e.Exec)
	}
}

func TestParseDesktopFile_NoDisplay(t *testing.T) {
	path := writeFixture(t, "hidden", fixtureNoDisplay)
	e, err := parseDesktopFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !e.NoDisplay {
		t.Error("NoDisplay should be true")
	}
}

func TestParseDesktopFile_NonApplicationHidden(t *testing.T) {
	path := writeFixture(t, "link", fixtureNonApplication)
	e, err := parseDesktopFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Non-Application Type entries are hidden by the parser.
	if !e.NoDisplay {
		t.Error("Link type should be marked NoDisplay=true")
	}
}

func TestParseDesktopFile_MultipleFieldCodesStripped(t *testing.T) {
	path := writeFixture(t, "editor", fixtureMultipleFieldCodes)
	e, err := parseDesktopFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(e.Exec, "%") {
		t.Errorf("all field codes should be stripped; got: %q", e.Exec)
	}
	if !strings.Contains(e.Exec, "--new-window") {
		t.Errorf("non-field-code args should survive: %q", e.Exec)
	}
}

func TestParseDesktopFile_IgnoresOtherSections(t *testing.T) {
	path := writeFixture(t, "realapp", fixtureIgnoresOtherSection)
	e, err := parseDesktopFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Name != "Real App" {
		t.Errorf("Name = %q; [Other Section] should not override", e.Name)
	}
	if e.Exec != "realapp" {
		t.Errorf("Exec = %q; [Other Section] should not override", e.Exec)
	}
}

func TestParseDesktopFile_FileNotFound(t *testing.T) {
	_, err := parseDesktopFile("/nonexistent/path/app.desktop")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// stripFieldCodes — launch arg building without shell injection
// ──────────────────────────────────────────────────────────────────────────────

func TestStripFieldCodes(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"firefox %u", "firefox"},
		{"gedit %F %U", "gedit"},
		{"/usr/bin/app %f --flag", "/usr/bin/app --flag"},
		{"app %i %c %k --verbose", "app --verbose"},
		{"app", "app"},
		{"app --open %U --window", "app --open --window"},
		// Ensure longer-than-2-char tokens that start with % are not stripped.
		{"app --format=%s", "app --format=%s"},
	}

	for _, tc := range cases {
		got := stripFieldCodes(tc.input)
		if got != tc.want {
			t.Errorf("stripFieldCodes(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestStripFieldCodes_NoShellInjection verifies that shell metacharacters
// embedded in an Exec string survive the strip unchanged — the caller is
// responsible for safe exec, but we must not silently alter the string.
func TestStripFieldCodes_NoShellInjection(t *testing.T) {
	// A malicious Exec value: the shell metacharacters are NOT field codes and
	// must pass through unchanged (it is the launcher's job to exec() safely).
	input := "app ; rm -rf /"
	got := stripFieldCodes(input)
	if got != input {
		t.Errorf("stripFieldCodes should not alter non-field-code tokens; got %q", got)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Service.Scan — directory-level integration using temp fixtures
// ──────────────────────────────────────────────────────────────────────────────

func TestService_Scan_PicksUpDesktopFiles(t *testing.T) {
	dir := t.TempDir()

	// Write two valid entries and one that should be excluded (NoDisplay=true).
	for name, content := range map[string]string{
		"firefox":  fixtureFirefox,
		"terminal": fixtureTerminal,
		"hidden":   fixtureNoDisplay,
	} {
		if err := os.WriteFile(filepath.Join(dir, name+".desktop"), []byte(content), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	svc := &Service{dirs: []string{dir}}
	entries := svc.Scan()

	ids := map[string]bool{}
	for _, e := range entries {
		ids[e.ID] = true
	}

	if !ids["firefox"] {
		t.Error("expected firefox entry")
	}
	if !ids["terminal"] {
		t.Error("expected terminal entry")
	}
	if ids["hidden"] {
		t.Error("hidden (NoDisplay=true) must be excluded")
	}
}

func TestService_Scan_DeduplicatesByID(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	for _, dir := range []string{dir1, dir2} {
		if err := os.WriteFile(filepath.Join(dir, "firefox.desktop"), []byte(fixtureFirefox), 0644); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}

	svc := &Service{dirs: []string{dir1, dir2}}
	entries := svc.Scan()

	count := 0
	for _, e := range entries {
		if e.ID == "firefox" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 firefox entry, got %d", count)
	}
}

func TestService_Scan_EmptyDir(t *testing.T) {
	svc := &Service{dirs: []string{t.TempDir()}}
	entries := svc.Scan()
	if entries == nil {
		entries = []Entry{}
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for empty dir, got %d", len(entries))
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Category mapping (verifying categories round-trip through parse)
// ──────────────────────────────────────────────────────────────────────────────

func TestParseDesktopFile_CategoryMapping(t *testing.T) {
	cases := []struct {
		content  string
		wantCats []string
	}{
		{
			content:  "[Desktop Entry]\nType=Application\nName=A\nExec=a\nCategories=Network;WebBrowser;\n",
			wantCats: []string{"Network", "WebBrowser"},
		},
		{
			content:  "[Desktop Entry]\nType=Application\nName=B\nExec=b\nCategories=Office;\n",
			wantCats: []string{"Office"},
		},
		{
			content:  "[Desktop Entry]\nType=Application\nName=C\nExec=c\nCategories=\n",
			wantCats: []string{},
		},
	}

	for _, tc := range cases {
		path := writeFixture(t, "cattest", tc.content)
		e, err := parseDesktopFile(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := map[string]bool{}
		for _, c := range tc.wantCats {
			want[c] = true
		}
		for _, c := range e.Categories {
			delete(want, c)
		}
		if len(want) > 0 {
			t.Errorf("missing categories %v in %v", want, e.Categories)
		}
		if len(e.Categories) > len(tc.wantCats) {
			t.Errorf("extra categories: got %v, want %v", e.Categories, tc.wantCats)
		}
	}
}
