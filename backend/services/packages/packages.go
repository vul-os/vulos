// Package packages manages Alpine Linux packages via apk.
package packages

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
)

// Package represents an Alpine package.
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

	// Count installed packages
	out, err := exec.CommandContext(ctx, "apk", "info").Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		s.InstalledCount = len(lines)
	}

	// Count available
	out, err = exec.CommandContext(ctx, "apk", "list", "--available").Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		s.AvailableCount = len(lines)
	}

	// Repos from /etc/apk/repositories
	out, _ = exec.CommandContext(ctx, "cat", "/etc/apk/repositories").Output()
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		enabled := !strings.HasPrefix(line, "#")
		url := strings.TrimPrefix(line, "#")
		s.Repos = append(s.Repos, Repo{URL: strings.TrimSpace(url), Enabled: enabled})
	}

	return s
}

// ListInstalled returns all installed packages.
func ListInstalled(ctx context.Context) []Package {
	var pkgs []Package
	out, err := exec.CommandContext(ctx, "apk", "info", "-v").Output()
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		// Format: "name-version - description" or just "name-version"
		name, version := splitNameVersion(line)
		pkgs = append(pkgs, Package{
			Name:      name,
			Version:   version,
			Installed: true,
		})
	}
	// Enrich with descriptions
	descOut, err := exec.CommandContext(ctx, "apk", "info", "-d").Output()
	if err == nil {
		descMap := parseDescriptions(string(descOut))
		for i := range pkgs {
			if desc, ok := descMap[pkgs[i].Name]; ok {
				pkgs[i].Description = desc
			}
		}
	}
	return pkgs
}

// Search finds packages matching a query.
func Search(ctx context.Context, query string) []Package {
	var pkgs []Package
	out, err := exec.CommandContext(ctx, "apk", "search", "-v", "-d", query).Output()
	if err != nil {
		return nil
	}

	// Get installed set for marking
	installed := installedSet(ctx)

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// "name-version - description"
		name, version := splitNameVersion(line)
		desc := ""
		if idx := strings.Index(line, " - "); idx >= 0 {
			desc = line[idx+3:]
		}
		pkgs = append(pkgs, Package{
			Name:        name,
			Version:     version,
			Description: desc,
			Installed:   installed[name],
		})
	}
	return pkgs
}

// Install installs a package.
func Install(ctx context.Context, name string) error {
	return exec.CommandContext(ctx, "apk", "add", "--no-cache", name).Run()
}

// Remove removes a package.
func Remove(ctx context.Context, name string) error {
	return exec.CommandContext(ctx, "apk", "del", name).Run()
}

// Update refreshes the package index.
func Update(ctx context.Context) error {
	return exec.CommandContext(ctx, "apk", "update").Run()
}

// Upgrade upgrades all packages.
func Upgrade(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "apk", "upgrade", "--no-cache").CombinedOutput()
	return string(out), err
}

// GetInfo returns detailed info about a package.
func GetInfo(ctx context.Context, name string) map[string]string {
	info := make(map[string]string)
	out, err := exec.CommandContext(ctx, "apk", "info", "-a", name).Output()
	if err != nil {
		return info
	}
	for _, line := range strings.Split(string(out), "\n") {
		if idx := strings.Index(line, ":"); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			if key != "" && val != "" {
				info[key] = val
			}
		}
	}
	info["raw"] = string(out)
	return info
}

func splitNameVersion(line string) (string, string) {
	// Remove description if present
	if idx := strings.Index(line, " - "); idx >= 0 {
		line = line[:idx]
	}
	line = strings.TrimSpace(line)
	// Split on last dash before a digit: "bash-5.2.21-r0" -> "bash", "5.2.21-r0"
	for i := len(line) - 1; i >= 0; i-- {
		if line[i] == '-' && i+1 < len(line) && line[i+1] >= '0' && line[i+1] <= '9' {
			return line[:i], line[i+1:]
		}
	}
	return line, ""
}

func installedSet(ctx context.Context) map[string]bool {
	m := make(map[string]bool)
	out, err := exec.CommandContext(ctx, "apk", "info").Output()
	if err != nil {
		return m
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		m[strings.TrimSpace(line)] = true
	}
	return m
}

func parseDescriptions(raw string) map[string]string {
	m := make(map[string]string)
	lines := strings.Split(raw, "\n")
	var currentPkg string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasSuffix(trimmed, " description:") {
			currentPkg = strings.TrimSuffix(trimmed, " description:")
		} else if currentPkg != "" {
			m[currentPkg] = trimmed
			currentPkg = ""
		}
	}
	return m
}
