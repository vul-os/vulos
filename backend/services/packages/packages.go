// Package packages manages Debian packages via apt-get/dpkg.
package packages

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// validPkgName is the allowlist for Debian package names and apt-cache search
// queries.  Debian policy: lowercase letters, digits, hyphens, dots and plus
// signs; first character must be alphanumeric.
// We also permit a trailing version suffix (=1.2.3) for install.
var validPkgName = regexp.MustCompile(`^[a-z0-9][a-z0-9.+\-]{0,127}(=[a-zA-Z0-9.+\-~:]+)?$`)

// validatePkgName returns an error when name cannot be passed safely to
// apt-get/apt-cache.  A leading '-' would be interpreted as a flag.
func validatePkgName(name string) error {
	if !validPkgName.MatchString(name) {
		return fmt.Errorf("invalid package name %q: must match ^[a-z0-9][a-z0-9.+\\-]{0,127}$", name)
	}
	return nil
}

// Package represents a Debian package.
type Package struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Size        string `json:"size"`
	Installed   bool   `json:"installed"`
	Repo        string `json:"repo"`
}

// Status is the overview payload.
type Status struct {
	InstalledCount int    `json:"installed_count"`
	AvailableCount int    `json:"available_count"`
	Repos          []Repo `json:"repos"`
}

// Repo represents a configured repository.
type Repo struct {
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
}

// GetStatus returns package manager overview.
func GetStatus(ctx context.Context) Status {
	s := Status{}

	// Count installed
	out, err := exec.CommandContext(ctx, "dpkg-query", "-W", "-f", "${Package}\n").Output()
	if err == nil {
		s.InstalledCount = len(strings.Split(strings.TrimSpace(string(out)), "\n"))
	}

	// Count available
	out, err = exec.CommandContext(ctx, "apt-cache", "pkgnames").Output()
	if err == nil {
		s.AvailableCount = len(strings.Split(strings.TrimSpace(string(out)), "\n"))
	}

	// Repos
	s.Repos = readRepos()
	return s
}

// ListInstalled returns all installed packages.
func ListInstalled(ctx context.Context) []Package {
	var pkgs []Package
	// Use binary:Summary for single-line description (avoids multiline Description breaking parsing)
	out, err := exec.CommandContext(ctx, "dpkg-query", "-W", "-f", "${Package}\t${Version}\t${binary:Summary}\n").Output()
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
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
		// Skip continuation lines from broken Description output
		if p.Name == "" || strings.HasPrefix(p.Name, " ") {
			continue
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

// Search finds packages matching a query.
func Search(ctx context.Context, query string) []Package {
	var pkgs []Package
	if err := validatePkgName(query); err != nil {
		return nil
	}
	out, err := exec.CommandContext(ctx, "apt-cache", "search", query).Output()
	if err != nil {
		return nil
	}

	installed := installedSet(ctx)

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
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

// Install installs a package.
func Install(ctx context.Context, name string) error {
	if err := validatePkgName(name); err != nil {
		return err
	}
	return exec.CommandContext(ctx, "apt-get", "install", "-y", "--no-install-recommends", name).Run()
}

// Remove removes a package.
func Remove(ctx context.Context, name string) error {
	if err := validatePkgName(name); err != nil {
		return err
	}
	return exec.CommandContext(ctx, "apt-get", "remove", "-y", name).Run()
}

// CacheReady returns true if the apt package cache exists (apt-get update has been run).
func CacheReady() bool {
	entries, err := os.ReadDir("/var/lib/apt/lists")
	if err != nil {
		return false
	}
	// Need more than just lock and partial — real package lists are present
	count := 0
	for _, e := range entries {
		if !e.IsDir() && strings.Contains(e.Name(), "Packages") {
			count++
		}
	}
	return count > 0
}

// Update refreshes the package index.
func Update(ctx context.Context) error {
	return exec.CommandContext(ctx, "apt-get", "update", "-qq").Run()
}

// Upgrade upgrades all packages.
func Upgrade(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "apt-get", "upgrade", "-y").CombinedOutput()
	return string(out), err
}

// GetInfo returns detailed info about a package.
func GetInfo(ctx context.Context, name string) map[string]string {
	info := make(map[string]string)
	if err := validatePkgName(name); err != nil {
		return info
	}
	out, err := exec.CommandContext(ctx, "apt-cache", "show", name).Output()
	if err != nil {
		return info
	}
	for _, line := range strings.Split(string(out), "\n") {
		if idx := strings.Index(line, ": "); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+2:])
			if key != "" && val != "" {
				info[key] = val
			}
		}
	}
	info["raw"] = string(out)
	return info
}

// InstallDeps IS DELETED, NOT DEPRECATED, and the deletion is the point.
//
// It was `apt-get install -y --no-install-recommends <deps…>` and it was the
// LAST package-manager call in the whole install path
// (roadmap/INSTALL-METHODOLOGY.md §4.6). Both of its call sites now go through
// VerifyDeps. Leaving it here unused would leave the one function whose only
// possible use is to re-open that path sitting next to the code that stopped
// using it — and a recipe field wired back to it is a one-line change nobody
// would read twice. `TestNoAptInstallManyRemains` is the guard that keeps it
// gone; the git history is where it lives now.

// VerifyDeps reports whether every package a recipe declares in `deps` is
// ALREADY INSTALLED on this box. It installs nothing, and it is the function
// that replaced InstallDeps.
//
// ── Why verifying is the only thing that can work here (DEPS-02) ─────────────
//
// The predecessor ran `apt-get install -y <deps…>`. Measured on 2026-08-17 in
// a debian:trixie-slim container arranged like the shipped image — apt lists
// cleared, exactly what build.sh does before packing, and nothing in the
// install path runs `apt-get update`:
//
//	apt-get install -y --no-install-recommends liburing2   → E: Unable to locate
//	                                                          package liburing2
//	                                                          exit 100
//	apt-get install -y --no-install-recommends git         → same, exit 100
//	apt-get install -y --no-install-recommends ca-certificates
//	  (NOT installed)                                      → no installation
//	                                                          candidate, exit 100
//	  (ALREADY installed)                                  → "already the newest
//	                                                          version", exit 0
//
// So on a real box the install call can only ever do one of two things: exit 0
// because the package was already there — in which case it installed nothing —
// or fail. There is no third case in which it usefully installs something,
// because there are no package lists to resolve from and fetching them would
// pull tens to hundreds of megabytes of Debian indices into a tmpfs sized at
// half of RAM (INSTALL-METHODOLOGY §2.2).
//
// Verifying states the same requirement without the pretence: `deps` names
// packages THE IMAGE MUST ALREADY CARRY. A box that does not carry one gets a
// loud refusal naming it, instead of a successful install of an app whose first
// exec dies with "error while loading shared libraries".
//
// ── Why a missing dpkg-query is an error and not a skip ──────────────────────
//
// `deps` are Debian package names. A box with no dpkg cannot answer the
// question at all, and answering "fine then" would be a guard that checks
// nothing — the dominant defect class in this repo. The refusal names the
// missing tool so it cannot be mistaken for a missing package.
func VerifyDeps(ctx context.Context, deps []string) error {
	if len(deps) == 0 {
		return nil
	}
	dpkg, err := exec.LookPath("dpkg-query")
	if err != nil {
		return fmt.Errorf("cannot verify the declared dependencies %v: this box has no dpkg-query, "+
			"so whether they are present is unknowable here (DEPS-02)", deps)
	}

	var missing []string
	for _, dep := range deps {
		name, wantVer, versioned := strings.Cut(dep, "=")
		if err := validatePkgName(name); err != nil {
			return fmt.Errorf("declared dependency %q: %w", dep, err)
		}
		out, err := exec.CommandContext(ctx, dpkg, "-W", "-f", "${Status}\t${Version}", name).Output()
		if err != nil {
			missing = append(missing, name+" (not installed)")
			continue
		}
		if why := depUnsatisfied(string(out), wantVer, versioned); why != "" {
			missing = append(missing, name+" ("+why+")")
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the box does not carry %d of %d declared dependencies: %s — "+
			"`deps` names packages the base image must already provide; add them to the image "+
			"(scripts/image-packages.txt and build.sh's package sets), not to the install path (DEPS-02)",
			len(missing), len(deps), strings.Join(missing, ", "))
	}
	return nil
}

// depUnsatisfied reads one `dpkg-query -W -f '${Status}\t${Version}'` line and
// returns "" when the package is genuinely installed, or a short reason when it
// is not. Split out from VerifyDeps for the reason archFromBinfmtEntry is split
// out of its directory walk: it can then be tested against real dpkg output on
// a machine that has no dpkg, which is every machine this suite runs on today.
func depUnsatisfied(line, wantVer string, versioned bool) string {
	status, gotVer, _ := strings.Cut(line, "\t")
	fields := strings.Fields(status)
	// dpkg's Status is three words: want, error-state, current-state. ONLY
	// "installed" means the files are on disk. "config-files" is a package
	// that was removed with its conffiles left behind, "half-installed" is an
	// interrupted unpack, and "not-installed" is a package dpkg merely knows
	// the name of — reading any of those as present is exactly how a
	// dependency check reports a shared library that is not there.
	if len(fields) != 3 || fields[1] != "ok" || fields[2] != "installed" {
		s := strings.TrimSpace(status)
		if s == "" {
			s = "no status"
		}
		return s
	}
	if versioned && strings.TrimSpace(gotVer) != wantVer {
		return "installed " + strings.TrimSpace(gotVer) + ", recipe wants " + wantVer
	}
	return ""
}

func installedSet(ctx context.Context) map[string]bool {
	m := make(map[string]bool)
	out, err := exec.CommandContext(ctx, "dpkg-query", "-W", "-f", "${Package}\n").Output()
	if err != nil {
		return m
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		m[strings.TrimSpace(line)] = true
	}
	return m
}

func readRepos() []Repo {
	var repos []Repo
	files := []string{"/etc/apt/sources.list"}
	entries, _ := os.ReadDir("/etc/apt/sources.list.d")
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".list") || strings.HasSuffix(e.Name(), ".sources") {
			files = append(files, "/etc/apt/sources.list.d/"+e.Name())
		}
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
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
	}
	return repos
}
