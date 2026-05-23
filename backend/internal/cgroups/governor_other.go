//go:build !linux

package cgroups

// applySlice is a no-op on non-Linux platforms.
// cgroup v2 is a Linux-only kernel feature; developer machines on darwin or
// windows will compile and run the package normally but no cgroup files are
// written.
func applySlice(_ string, _ Limits) error {
	return nil
}

// readLiveStats returns zeros on non-Linux platforms because cgroupfs is not
// available.
func readLiveStats(_ string) (int64, int64) {
	return 0, 0
}
