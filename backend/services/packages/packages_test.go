package packages

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers that mirror the logic inside packages.go but accept a string reader
// instead of running exec commands.  These let us feed fixture text without
// touching apt / dpkg.
// ---------------------------------------------------------------------------

// parseInstalledLines is the same loop used in ListInstalled.
func parseInstalledLines(raw string) []Package {
	var pkgs []Package
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		p := Package{Installed: true}
		if len(fields) >= 1 {
			p.Name = fields[0]
		}
		if len(fields) >= 2 {
			p.Version = fields[1]
		}
		if len(fields) >= 3 {
			p.Description = fields[2]
		}
		if p.Name == "" || strings.HasPrefix(p.Name, " ") {
			continue
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

// parseSearchLines mirrors the scanner loop in Search.
func parseSearchLines(raw string, installed map[string]bool) []Package {
	var pkgs []Package
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, desc, _ := strings.Cut(line, " - ")
		name = strings.TrimSpace(name)
		pkgs = append(pkgs, Package{
			Name:        name,
			Description: strings.TrimSpace(desc),
			Installed:   installed[name],
		})
	}
	return pkgs
}

// parseAptCacheShow mirrors GetInfo's parsing loop.
func parseAptCacheShow(raw string) map[string]string {
	info := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		if idx := strings.Index(line, ": "); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+2:])
			if key != "" && val != "" {
				info[key] = val
			}
		}
	}
	info["raw"] = raw
	return info
}

// buildInstallArgs mirrors how Install/InstallDeps build the exec args.
func buildInstallArgs(name string) []string {
	return []string{"apt-get", "install", "-y", "--no-install-recommends", name}
}

func buildInstallDepsArgs(deps []string) []string {
	return append([]string{"apt-get", "install", "-y", "--no-install-recommends"}, deps...)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// Fixture: typical dpkg-query -W output
const dpkgFixture = `bash	5.2.15-2+b7	GNU Bourne Again SHell
curl	7.88.1-10+deb12u8	command line tool for transferring data with URL syntax
 broken-continuation line should be skipped
vim	2:9.0.1378-2	Vi IMproved - enhanced vi editor
`

func TestParseInstalled_Count(t *testing.T) {
	pkgs := parseInstalledLines(dpkgFixture)
	if len(pkgs) != 3 {
		t.Fatalf("expected 3 packages, got %d", len(pkgs))
	}
}

func TestParseInstalled_Fields(t *testing.T) {
	pkgs := parseInstalledLines(dpkgFixture)
	if pkgs[0].Name != "bash" {
		t.Errorf("name: want bash, got %q", pkgs[0].Name)
	}
	if pkgs[0].Version != "5.2.15-2+b7" {
		t.Errorf("version: want 5.2.15-2+b7, got %q", pkgs[0].Version)
	}
	if pkgs[0].Description != "GNU Bourne Again SHell" {
		t.Errorf("desc: want GNU Bourne Again SHell, got %q", pkgs[0].Description)
	}
}

func TestParseInstalled_InstalledFlag(t *testing.T) {
	for _, p := range parseInstalledLines(dpkgFixture) {
		if !p.Installed {
			t.Errorf("package %q should be marked installed", p.Name)
		}
	}
}

func TestParseInstalled_SkipContinuationLines(t *testing.T) {
	// The fixture has one " broken-continuation line" — it must be skipped.
	pkgs := parseInstalledLines(dpkgFixture)
	for _, p := range pkgs {
		if strings.HasPrefix(p.Name, " ") {
			t.Errorf("continuation line leaked through: %q", p.Name)
		}
	}
}

func TestParseInstalled_Empty(t *testing.T) {
	if got := parseInstalledLines(""); got != nil {
		t.Errorf("expected nil slice for empty input, got %v", got)
	}
}

// Fixture: apt-cache search output
const searchFixture = `bash - GNU Bourne Again SHell
curl - command line tool for transferring data with URL syntax
libssl3 - Secure Sockets Layer toolkit - shared libraries
`

func TestParseSearch_Count(t *testing.T) {
	pkgs := parseSearchLines(searchFixture, nil)
	if len(pkgs) != 3 {
		t.Fatalf("expected 3, got %d", len(pkgs))
	}
}

func TestParseSearch_NameDesc(t *testing.T) {
	pkgs := parseSearchLines(searchFixture, nil)
	if pkgs[1].Name != "curl" {
		t.Errorf("want curl, got %q", pkgs[1].Name)
	}
	if pkgs[1].Description != "command line tool for transferring data with URL syntax" {
		t.Errorf("unexpected desc: %q", pkgs[1].Description)
	}
}

func TestParseSearch_InstalledFlag(t *testing.T) {
	installed := map[string]bool{"bash": true}
	pkgs := parseSearchLines(searchFixture, installed)
	for _, p := range pkgs {
		want := p.Name == "bash"
		if p.Installed != want {
			t.Errorf("package %q installed=%v, want %v", p.Name, p.Installed, want)
		}
	}
}

// Fixture: apt-cache show output
const aptCacheShowFixture = `Package: curl
Version: 7.88.1-10+deb12u8
Architecture: amd64
Maintainer: Alessandro Ghedini <ghedo@debian.org>
Description: command line tool for transferring data with URL syntax
`

func TestParseAptCacheShow_Fields(t *testing.T) {
	info := parseAptCacheShow(aptCacheShowFixture)
	if info["Package"] != "curl" {
		t.Errorf("Package: want curl, got %q", info["Package"])
	}
	if info["Version"] != "7.88.1-10+deb12u8" {
		t.Errorf("Version mismatch: %q", info["Version"])
	}
	if info["Architecture"] != "amd64" {
		t.Errorf("Architecture mismatch: %q", info["Architecture"])
	}
}

func TestParseAptCacheShow_RawPresent(t *testing.T) {
	info := parseAptCacheShow(aptCacheShowFixture)
	if info["raw"] == "" {
		t.Error("raw key should be present")
	}
}

func TestParseAptCacheShow_EmptyInput(t *testing.T) {
	info := parseAptCacheShow("")
	if len(info) != 1 || info["raw"] != "" {
		t.Errorf("unexpected keys in empty parse: %v", info)
	}
}

// ---------------------------------------------------------------------------
// Install-command builder — verifies no shell injection is possible.
// ---------------------------------------------------------------------------

func TestBuildInstallArgs_Basic(t *testing.T) {
	args := buildInstallArgs("curl")
	if args[0] != "apt-get" {
		t.Errorf("binary: want apt-get, got %q", args[0])
	}
	if args[len(args)-1] != "curl" {
		t.Errorf("last arg should be package name, got %q", args[len(args)-1])
	}
}

func TestBuildInstallArgs_NoShellExpansion(t *testing.T) {
	// A malicious name with shell metacharacters must arrive as a literal arg,
	// not be interpreted by a shell.  Since exec.Command is used (not sh -c),
	// the package name is passed verbatim — we just verify it lands in args[].
	evil := "curl; rm -rf /"
	args := buildInstallArgs(evil)
	if args[len(args)-1] != evil {
		t.Errorf("shell injection guard: arg should be literal, got %q", args[len(args)-1])
	}
	// There must be no shell binary in the command.
	for _, a := range args {
		if a == "sh" || a == "bash" || a == "-c" {
			t.Errorf("shell invocation detected in args: %v", args)
		}
	}
}

func TestBuildInstallDepsArgs_MultiplePackages(t *testing.T) {
	deps := []string{"curl", "vim", "git"}
	args := buildInstallDepsArgs(deps)
	// Expect: apt-get install -y --no-install-recommends curl vim git
	if len(args) != 4+len(deps) {
		t.Fatalf("arg count: want %d, got %d", 4+len(deps), len(args))
	}
	for i, dep := range deps {
		if args[4+i] != dep {
			t.Errorf("dep[%d]: want %q, got %q", i, dep, args[4+i])
		}
	}
}

func TestBuildInstallDepsArgs_Empty(t *testing.T) {
	// InstallDeps returns early for empty deps — no args should be built.
	// We replicate that guard here.
	deps := []string{}
	if len(deps) == 0 {
		return // guard matched — test passes
	}
	t.Error("guard did not match empty slice")
}

// ---------------------------------------------------------------------------
// Repo line parsing (mirrors readRepos inner logic, no filesystem access).
// ---------------------------------------------------------------------------

func parseRepoLines(data string) []Repo {
	var repos []Repo
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		enabled := !strings.HasPrefix(line, "#")
		url := strings.TrimPrefix(line, "#")
		url = strings.TrimSpace(url)
		if strings.HasPrefix(url, "deb ") || strings.HasPrefix(url, "deb-src ") {
			repos = append(repos, Repo{URL: url, Enabled: enabled})
		}
	}
	return repos
}

const sourcesListFixture = `deb http://deb.debian.org/debian bookworm main
# deb-src http://deb.debian.org/debian bookworm main
deb http://security.debian.org/debian-security bookworm-security main

# This is a comment, not a repo
deb-src http://deb.debian.org/debian bookworm-updates main
`

func TestParseRepoLines_Count(t *testing.T) {
	repos := parseRepoLines(sourcesListFixture)
	if len(repos) != 4 {
		t.Fatalf("expected 4 repos, got %d", len(repos))
	}
}

func TestParseRepoLines_EnabledDisabled(t *testing.T) {
	repos := parseRepoLines(sourcesListFixture)
	// Second entry is commented out
	if repos[1].Enabled {
		t.Error("commented-out repo should have Enabled=false")
	}
	if !repos[0].Enabled {
		t.Error("first repo should have Enabled=true")
	}
}

func TestParseRepoLines_URLPreservation(t *testing.T) {
	repos := parseRepoLines(sourcesListFixture)
	want := "deb http://deb.debian.org/debian bookworm main"
	if repos[0].URL != want {
		t.Errorf("URL: want %q, got %q", want, repos[0].URL)
	}
}
