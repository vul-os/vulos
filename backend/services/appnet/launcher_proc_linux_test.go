//go:build linux

package appnet

import (
	"syscall"
	"testing"
)

// TestSysProcAttr_HasMountNamespace_Linux verifies CLONE_NEWNS is in the
// Cloneflags returned by newAppSysProcAttr on Linux.
// ISOLATION-PRIV-01: private mount namespace prevents host-FS mount leakage.
func TestSysProcAttr_HasMountNamespace_Linux(t *testing.T) {
	attr := newAppSysProcAttr()
	if attr.Cloneflags&syscall.CLONE_NEWNS == 0 {
		t.Fatal("ISOLATION-PRIV-01 REGRESSION: CLONE_NEWNS not in SysProcAttr.Cloneflags on Linux — mount namespace isolation missing")
	}
}
