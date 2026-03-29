// Package disks provides filesystem and disk usage information.
package disks

import (
	"context"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Mount represents a mounted filesystem.
type Mount struct {
	Device     string  `json:"device"`
	MountPoint string  `json:"mount_point"`
	FSType     string  `json:"fs_type"`
	TotalMB    int64   `json:"total_mb"`
	UsedMB     int64   `json:"used_mb"`
	FreeMB     int64   `json:"free_mb"`
	Percent    float64 `json:"percent"`
}

// DirUsage represents usage of a directory.
type DirUsage struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	SizeMB int64  `json:"size_mb"`
}

// Status is the full disk usage payload.
type Status struct {
	Mounts []Mount `json:"mounts"`
}

// GetStatus returns mounted filesystem usage.
func GetStatus() Status {
	s := Status{}

	out, err := exec.Command("mount").Output()
	if err != nil {
		// Fallback: just stat root
		s.Mounts = append(s.Mounts, statMount("/", "/", ""))
		return s
	}

	seen := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		// Format: device on /mount type fstype (options)
		parts := strings.Fields(line)
		if len(parts) < 5 || parts[1] != "on" || parts[3] != "type" {
			continue
		}
		device, mountPoint, fsType := parts[0], parts[2], parts[4]

		// Skip virtual/pseudo filesystems
		switch fsType {
		case "proc", "sysfs", "devpts", "tmpfs", "devtmpfs", "cgroup", "cgroup2",
			"securityfs", "debugfs", "tracefs", "fusectl", "configfs", "hugetlbfs",
			"mqueue", "pstore", "binfmt_misc", "autofs", "rpc_pipefs", "nfsd",
			"overlay", "nsfs", "efivarfs":
			continue
		}
		if strings.HasPrefix(device, "none") || strings.HasPrefix(device, "cgroup") {
			continue
		}
		if seen[mountPoint] {
			continue
		}
		seen[mountPoint] = true

		m := statMount(device, mountPoint, fsType)
		if m.TotalMB > 0 {
			s.Mounts = append(s.Mounts, m)
		}
	}

	if len(s.Mounts) == 0 {
		s.Mounts = append(s.Mounts, statMount("/", "/", ""))
	}

	return s
}

func statMount(device, mountPoint, fsType string) Mount {
	m := Mount{Device: device, MountPoint: mountPoint, FSType: fsType}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(mountPoint, &stat); err == nil {
		bsize := uint64(stat.Bsize)
		m.TotalMB = int64(stat.Blocks * bsize / (1024 * 1024))
		m.FreeMB = int64(stat.Bavail * bsize / (1024 * 1024))
		m.UsedMB = m.TotalMB - int64(stat.Bfree*bsize/(1024*1024))
		if m.TotalMB > 0 {
			m.Percent = float64(m.UsedMB) / float64(m.TotalMB) * 100
		}
	}
	return m
}

// DirBreakdown returns top-level directory sizes within a path.
func DirBreakdown(ctx context.Context, path string) []DirUsage {
	// Use du with max-depth=1 for a single-level breakdown
	out, err := exec.CommandContext(ctx, "du", "-m", "--max-depth=1", path).Output()
	if err != nil {
		// Try macOS/BSD variant
		out, err = exec.CommandContext(ctx, "du", "-m", "-d", "1", path).Output()
		if err != nil {
			return nil
		}
	}

	var dirs []DirUsage
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		sizeMB, _ := strconv.ParseInt(parts[0], 10, 64)
		dirPath := strings.Join(parts[1:], " ")

		// Skip the path itself (total)
		if filepath.Clean(dirPath) == filepath.Clean(path) {
			continue
		}

		dirs = append(dirs, DirUsage{
			Path:   dirPath,
			Name:   filepath.Base(dirPath),
			SizeMB: sizeMB,
		})
	}

	// Sort largest first
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].SizeMB > dirs[j].SizeMB })

	// Limit to top 20
	if len(dirs) > 20 {
		dirs = dirs[:20]
	}

	return dirs
}
