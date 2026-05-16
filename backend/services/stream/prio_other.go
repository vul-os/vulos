//go:build !linux

package stream

// setRealtimePriority is a no-op on non-Linux platforms.
// Gaming mode still activates encoder/bitrate/audio profiles; only the
// OS-level process priority elevation is Linux-specific.
func setRealtimePriority(pid int) error {
	return nil
}
