package input

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
)

// xdotoolExec runs xdotool with the given args on the specified display.
// This is the fallback path when uinput is not available (containers without /dev/uinput).
func xdotoolExec(display string, args []string) {
	cmd := exec.Command("xdotool", args...)
	cmd.Env = []string{"DISPLAY=" + display}
	cmd.Run()
}

// xdotoolPipe is a persistent xdotool process that reads commands from stdin.
// Eliminates fork-per-event overhead and prevents modifier race conditions.
// xdotool supports reading commands from stdin when invoked with "-" as the script arg.
type xdotoolPipe struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	display string
	dead    bool
}

// newXdotoolPipe starts a persistent xdotool process for the given display.
func newXdotoolPipe(display string) *xdotoolPipe {
	p := &xdotoolPipe{display: display}
	p.start()
	return p
}

func (p *xdotoolPipe) start() {
	cmd := exec.Command("xdotool", "-")
	cmd.Env = []string{"DISPLAY=" + p.display}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Printf("[input] xdotool pipe stdin failed: %v", err)
		p.dead = true
		return
	}
	// Drain stderr to prevent blocking
	stderr, _ := cmd.StderrPipe()
	if stderr != nil {
		go func() {
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				// discard
			}
		}()
	}
	if err := cmd.Start(); err != nil {
		log.Printf("[input] xdotool pipe start failed: %v", err)
		p.dead = true
		return
	}
	p.cmd = cmd
	p.stdin = stdin
	p.dead = false
	log.Printf("[input] xdotool pipe started (pid=%d) on %s", cmd.Process.Pid, p.display)

	// Monitor for unexpected exit
	go func() {
		cmd.Wait()
		p.mu.Lock()
		p.dead = true
		p.mu.Unlock()
	}()
}

// send writes a command line to the xdotool stdin pipe.
// Falls back to fork-exec if the pipe is dead.
func (p *xdotoolPipe) send(args ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.dead {
		// Try restart once
		p.start()
		if p.dead {
			// Final fallback: fork-exec
			xdotoolExec(p.display, args)
			return
		}
	}

	// Build command line
	line := ""
	for i, a := range args {
		if i > 0 {
			line += " "
		}
		line += a
	}
	line += "\n"

	if _, err := fmt.Fprint(p.stdin, line); err != nil {
		p.dead = true
		// Fallback for this call
		xdotoolExec(p.display, args)
	}
}

// close kills the persistent process.
func (p *xdotoolPipe) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stdin != nil {
		p.stdin.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
}
