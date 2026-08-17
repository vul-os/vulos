//go:build !linux

package main

import (
	"fmt"
	"runtime"
)

// setSystemHostname is a no-op that reports why on non-Linux hosts.
//
// The OS ships as a Linux image; a developer running the server on macOS must
// not have the box quietly change their laptop's hostname. Returning an error
// (rather than nil) is deliberate: the caller reports AppliedLive=false and the
// UI tells the user the rename needs a restart, which is exactly true here.
func setSystemHostname(name string) error {
	return fmt.Errorf("setting the system hostname is not supported on %s", runtime.GOOS)
}
