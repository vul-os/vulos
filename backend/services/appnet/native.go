package appnet

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// NativeLaunchSpec describes a native Wayland/X11 process to spawn.
type NativeLaunchSpec struct {
	// Binary is an absolute path or a name resolvable via exec.LookPath.
	Binary string
	// Args are passed directly — never parsed from a single shell string.
	Args []string
	// WaylandDisplay is the WAYLAND_DISPLAY value (e.g. "wayland-1").
	WaylandDisplay string
	// XDGRuntimeDir is the XDG_RUNTIME_DIR value.
	XDGRuntimeDir string
}

// LaunchNative spawns a native Wayland/X11 binary with a minimal scrubbed
// environment (no os.Environ passthrough).  It reaps the process in a
// background goroutine and logs the exit status.
// Returns the PID of the spawned process.
func LaunchNative(spec NativeLaunchSpec) (pid int, err error) {
	// Validate binary — must not contain ".." and must be absolute or LookPath-able.
	if strings.Contains(spec.Binary, "..") {
		return 0, fmt.Errorf("binary path must not contain '..'")
	}
	resolvedBin := spec.Binary
	if !filepath.IsAbs(spec.Binary) {
		resolvedBin, err = exec.LookPath(spec.Binary)
		if err != nil {
			return 0, fmt.Errorf("binary %q not found: %w", spec.Binary, err)
		}
	} else {
		if _, statErr := os.Stat(spec.Binary); statErr != nil {
			return 0, fmt.Errorf("binary %q not found: %w", spec.Binary, statErr)
		}
	}

	// Build a minimal, scrubbed environment — never inherit os.Environ().
	env := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	if home := os.Getenv("HOME"); home != "" {
		env = append(env, "HOME="+home)
	}
	if tmp := os.Getenv("TMPDIR"); tmp != "" {
		env = append(env, "TMPDIR="+tmp)
	}
	if spec.WaylandDisplay != "" {
		env = append(env, "WAYLAND_DISPLAY="+spec.WaylandDisplay)
	}
	if spec.XDGRuntimeDir != "" {
		env = append(env, "XDG_RUNTIME_DIR="+spec.XDGRuntimeDir)
	}

	cmd := exec.Command(resolvedBin, spec.Args...)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Own process group. This path used to set no SysProcAttr at all, so a
	// native app inherited the SERVER's process group, and that mattered more
	// here than anywhere else: LaunchNative returns a pid to the caller and
	// never stops the process itself, so proctl — the Activity Monitor "End
	// process" path — is the ONLY way to stop a native app. proctl.Protect
	// refuses any pid sharing the server's group with the denial "self_group",
	// so every app launched this way was unkillable from the UI, and the one
	// control the product offers for it could not work.
	//
	// The reverse hazard is the same one EXEC-PGID-01 describes: a
	// group-directed signal aimed at the app reached the server.
	//
	// Setpgid only, deliberately NOT newAppSysProcAttr(): that adds
	// CLONE_NEWNS, which needs CAP_SYS_ADMIN, and would turn a working launch
	// into a failing one on any box where the server is not root. Process apps
	// take that namespace because they are already root-gated by `ip netns`;
	// this path is not, and hardening it must not be what stops it running.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start %q: %w", spec.Binary, err)
	}

	pid = cmd.Process.Pid
	log.Printf("[native] launched %q pid=%d", spec.Binary, pid)

	// Reap the process and log exit.
	go func() {
		waitErr := cmd.Wait()
		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		log.Printf("[native] pid=%d (%s) exited code=%d err=%v", pid, spec.Binary, exitCode, waitErr)
	}()

	return pid, nil
}
