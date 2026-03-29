package pty

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

// ExecResult is the output of a one-shot command.
type ExecResult struct {
	Command  string `json:"command"`
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
	Duration string `json:"duration"`
}

// Exec runs a single command and returns its output.
// Used by the Portal for /commands without needing a full PTY session.
// Timeout: 10 seconds.
func Exec(ctx context.Context, command string) ExecResult {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	// Truncate to 10KB
	if len(output) > 10240 {
		output = output[:10240] + "\n... (truncated)"
	}

	return ExecResult{
		Command:  command,
		Output:   output,
		ExitCode: exitCode,
		Duration: time.Since(start).Truncate(time.Millisecond).String(),
	}
}
