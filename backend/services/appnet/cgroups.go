package appnet

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
)

// ResourceLimits defines cgroup v2 resource constraints for an app.
type ResourceLimits struct {
	MemoryMax int64 // bytes, 0 = unlimited
	CPUQuota  int   // microseconds per 100ms period, 0 = unlimited (100000 = 1 core)
	PidsMax   int   // max number of processes, 0 = unlimited
}

// DefaultLimits returns sensible defaults for an app.
func DefaultLimits() ResourceLimits {
	return ResourceLimits{
		MemoryMax: 512 * 1024 * 1024, // 512MB
		CPUQuota:  100000,            // 1 core
		PidsMax:   256,
	}
}

// ApplyCgroup creates a cgroup v2 for an app and applies limits.
// The app's PID should be added to the cgroup after this.
func ApplyCgroup(appID string, pid int, limits ResourceLimits) error {
	cgroupPath := filepath.Join("/sys/fs/cgroup", "vulos", appID)
	if err := os.MkdirAll(cgroupPath, 0755); err != nil {
		return fmt.Errorf("create cgroup dir: %w", err)
	}

	// Memory limit
	if limits.MemoryMax > 0 {
		if err := writeFile(filepath.Join(cgroupPath, "memory.max"), strconv.FormatInt(limits.MemoryMax, 10)); err != nil {
			log.Printf("[cgroups] memory.max for %s: %v", appID, err)
		}
	}

	// CPU quota (microseconds per 100ms period)
	if limits.CPUQuota > 0 {
		val := fmt.Sprintf("%d 100000", limits.CPUQuota)
		if err := writeFile(filepath.Join(cgroupPath, "cpu.max"), val); err != nil {
			log.Printf("[cgroups] cpu.max for %s: %v", appID, err)
		}
	}

	// PIDs limit
	if limits.PidsMax > 0 {
		if err := writeFile(filepath.Join(cgroupPath, "pids.max"), strconv.Itoa(limits.PidsMax)); err != nil {
			log.Printf("[cgroups] pids.max for %s: %v", appID, err)
		}
	}

	// Add process to cgroup
	if pid > 0 {
		if err := writeFile(filepath.Join(cgroupPath, "cgroup.procs"), strconv.Itoa(pid)); err != nil {
			return fmt.Errorf("add pid %d to cgroup: %w", pid, err)
		}
	}

	log.Printf("[cgroups] %s: mem=%dMB cpu=%d%% pids=%d",
		appID, limits.MemoryMax/(1024*1024), limits.CPUQuota/1000, limits.PidsMax)
	return nil
}

// RemoveCgroup removes an app's cgroup.
func RemoveCgroup(appID string) {
	cgroupPath := filepath.Join("/sys/fs/cgroup", "vulos", appID)
	os.Remove(cgroupPath) // rmdir — only works if empty (processes exited)
}

// ReadCgroupStats reads current resource usage from cgroup.
func ReadCgroupStats(appID string) map[string]string {
	cgroupPath := filepath.Join("/sys/fs/cgroup", "vulos", appID)
	stats := make(map[string]string)

	if data, err := os.ReadFile(filepath.Join(cgroupPath, "memory.current")); err == nil {
		stats["memory_bytes"] = string(data)
	}
	if data, err := os.ReadFile(filepath.Join(cgroupPath, "cpu.stat")); err == nil {
		stats["cpu_stat"] = string(data)
	}
	if data, err := os.ReadFile(filepath.Join(cgroupPath, "pids.current")); err == nil {
		stats["pids"] = string(data)
	}
	return stats
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

func init() {
	// Ensure parent cgroup directory exists
	os.MkdirAll("/sys/fs/cgroup/vulos", 0755)
}
