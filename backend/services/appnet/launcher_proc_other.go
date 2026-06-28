//go:build !linux

package appnet

import "syscall"

// newAppSysProcAttr returns a minimal SysProcAttr for non-Linux platforms
// (macOS dev builds).  Mount namespaces (CLONE_NEWNS) are Linux-only; on
// macOS we use Setpgid only.  The security-critical Linux path is in
// launcher_proc_linux.go.
func newAppSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid: true,
	}
}
