//go:build linux

package cgroups

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// applySlice creates the cgroup directory and writes the limit files.
// This is the Linux implementation.  It operates on the cgroupfs at path.
func applySlice(path string, l Limits) error {
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("cgroups: mkdir %s: %w", path, err)
	}

	// cpu.weight  (range 1–10000; spec maps 10%→100, 15%→150, 30%→300)
	if err := writeCgFile(path, "cpu.weight", fmt.Sprintf("%d\n", l.CPUWeight)); err != nil {
		return err
	}

	// memory.min  (system slice guarantee — apps may never eat into this)
	if l.MemMin > 0 {
		if err := writeCgFile(path, "memory.min", fmt.Sprintf("%d\n", l.MemMin)); err != nil {
			return err
		}
	}

	// memory.high (soft throttle; "max" means unlimited)
	highStr := "max\n"
	if l.MemHigh > 0 {
		highStr = fmt.Sprintf("%d\n", l.MemHigh)
	}
	if err := writeCgFile(path, "memory.high", highStr); err != nil {
		return err
	}

	// memory.max  (hard OOM kill; "max" means unlimited)
	maxStr := "max\n"
	if l.MemMax > 0 {
		maxStr = fmt.Sprintf("%d\n", l.MemMax)
	}
	if err := writeCgFile(path, "memory.max", maxStr); err != nil {
		return err
	}

	return nil
}

// readLiveStats reads memory.current and cpu.stat usage_usec from the cgroup
// directory.  Returns zeros on any error (e.g. cgroup not yet created).
func readLiveStats(path string) (memCurrent, cpuUsageUs int64) {
	// memory.current
	if data, err := os.ReadFile(path + "/memory.current"); err == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		memCurrent = v
	}

	// cpu.stat — look for "usage_usec <N>" line
	if f, err := os.Open(path + "/cpu.stat"); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "usage_usec ") {
				parts := strings.Fields(line)
				if len(parts) == 2 {
					v, _ := strconv.ParseInt(parts[1], 10, 64)
					cpuUsageUs = v
				}
				break
			}
		}
	}
	return
}

// writeCgFile writes content to a file within the cgroup directory.
func writeCgFile(dir, file, content string) error {
	p := dir + "/" + file
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		// Non-fatal: the cgroup controller may not be enabled on all kernels.
		// Log but don't fail the entire apply.
		return fmt.Errorf("cgroups: write %s: %w", p, err)
	}
	return nil
}
