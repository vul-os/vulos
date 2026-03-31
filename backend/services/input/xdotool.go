package input

import (
	"os/exec"
)

// xdotoolExec runs xdotool with the given args on the specified display.
// This is the fallback path when uinput is not available (containers without /dev/uinput).
func xdotoolExec(display string, args []string) {
	cmd := exec.Command("xdotool", args...)
	cmd.Env = []string{"DISPLAY=" + display}
	cmd.Run()
}
