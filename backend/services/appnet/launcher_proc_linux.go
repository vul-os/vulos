//go:build linux

package appnet

import "syscall"

// newAppSysProcAttr returns the SysProcAttr used for every process-app exec.
// On Linux it adds CLONE_NEWNS to give the app a private mount namespace:
// mounts created inside the app's process tree do not propagate to the host,
// and host-side bind-mounts added after launch are invisible to the app.
// Setpgid puts the process in its own process group for clean SIGTERM-on-stop.
//
// ISOLATION-PRIV-01: mount namespace + privilege drop (setpriv in the command)
// + network namespace (namespace.go) form the three-layer isolation model.
func newAppSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid:    true,
		Cloneflags: syscall.CLONE_NEWNS, // private mount namespace
	}
}
