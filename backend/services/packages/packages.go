// Package packages manages Debian packages via apt-get/dpkg.
package packages

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strings"
)

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
	return exec.CommandContext(ctx, "apt-get", "install", "-y", "--no-install-recommends", name).Run()
}

// Remove removes a package.
func Remove(ctx context.Context, name string) error {
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

// InstallDeps installs multiple packages at once.
func InstallDeps(ctx context.Context, deps []string) error {
	if len(deps) == 0 {
		return nil
	}
	args := append([]string{"install", "-y", "--no-install-recommends"}, deps...)
	return exec.CommandContext(ctx, "apt-get", args...).Run()
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
