//go:build linux

package main

import (
	"fmt"
	"os"
	"syscall"
)

// setSystemHostname installs name as the running system hostname.
//
// This is what makes a rename REAL rather than a file write that takes effect
// at the next reboot. avahi-daemon (started by cmd/init on the bare-metal
// image) and everything else that calls gethostname() follow the kernel's UTS
// value, not /etc/hostname, so without this call an owner who renamed the box
// to "study" kept being told the rename succeeded while the box went on
// answering to its old name — see routes_identity.go's header.
//
// It returns a descriptive error rather than swallowing one: the caller reports
// AppliedLive=false and tells the user a restart is needed. A rename that
// quietly did nothing and reported success is the failure mode being fixed.
//
// The name is expected to be pre-sanitised by lan.SanitizeHostname; the length
// check here is a last line of defence, since sethostname(2) itself performs no
// validation whatsoever and will accept arbitrary bytes.
func setSystemHostname(name string) error {
	if name == "" || len(name) > 63 {
		return fmt.Errorf("refusing to set an invalid hostname %q", name)
	}
	if err := syscall.Sethostname([]byte(name)); err != nil {
		if os.Getuid() != 0 {
			return fmt.Errorf("cannot set the system hostname without root: %w", err)
		}
		return fmt.Errorf("sethostname(%q): %w", name, err)
	}
	return nil
}
