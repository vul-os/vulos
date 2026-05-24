// supervisor.go — process supervisor for the external streamer binary.
//
// The Supervisor is intentionally small: it does not parse the streamer's
// stdout, model session state, or know anything about WebRTC. Its job is:
//
//   - Start the binary with the requested args.
//   - Watch for exit; on non-zero exit, wait Backoff (exponentially up to
//     SupervisorMaxBackoff) and restart.
//   - On Stop, send SIGTERM, wait up to SupervisorTerminationGrace, then
//     SIGKILL.
//
// Tests substitute a tempdir shell script for the streamer binary; the
// supervisor cannot tell the difference.

package gpuhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	// SupervisorMaxBackoff is the upper bound on restart backoff.
	SupervisorMaxBackoff = 30 * time.Second
	// SupervisorTerminationGrace is how long we wait for the streamer to exit
	// after SIGTERM before escalating to SIGKILL.
	SupervisorTerminationGrace = 5 * time.Second
)

// Supervisor wraps one long-running external process and restarts it on
// failure. Safe for concurrent use.
type Supervisor struct {
	binary  string
	args    []string
	initBO  time.Duration

	mu       sync.Mutex
	cmd      *exec.Cmd
	cancel   context.CancelFunc
	doneCh   chan struct{} // closed when the watch goroutine exits
	stopReq  bool
	alive    atomic.Bool
	starts   atomic.Uint64
	restarts atomic.Uint64
	exits    atomic.Uint64
}

// NewSupervisor builds a Supervisor for binary with the given args and
// initial restart backoff.
func NewSupervisor(binary string, args []string, initialBackoff time.Duration) *Supervisor {
	if initialBackoff <= 0 {
		initialBackoff = 1 * time.Second
	}
	return &Supervisor{
		binary: binary,
		args:   args,
		initBO: initialBackoff,
	}
}

// Start launches the binary and begins the supervise loop. It returns once
// the first exec.Start has been attempted; the supervise goroutine then runs
// until Stop. ctx is the caller-supplied context; cancelling it does NOT
// stop the supervisor (use Stop for that), but a cancelled ctx means the
// process is started against a derived background context so it survives.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.cmd != nil {
		s.mu.Unlock()
		return errors.New("gpuhost/supervisor: already started")
	}
	s.stopReq = false
	s.doneCh = make(chan struct{})
	s.mu.Unlock()

	if err := s.spawnLocked(); err != nil {
		s.mu.Lock()
		s.doneCh = nil
		s.mu.Unlock()
		return err
	}

	go s.supervise()
	return nil
}

// spawnLocked starts one fresh exec.Cmd. The caller holds nothing; we acquire
// the mutex here. Returns the first-spawn error or nil; on nil the cmd is
// stored on s and s.alive is true.
func (s *Supervisor) spawnLocked() error {
	cmd := exec.Command(s.binary, s.args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Drain stdout/stderr so the child doesn't block on a full pipe.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("supervisor: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("supervisor: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("supervisor: exec %s: %w", s.binary, err)
	}

	go drain("streamer/stdout", stdout)
	go drain("streamer/stderr", stderr)

	s.mu.Lock()
	s.cmd = cmd
	s.alive.Store(true)
	s.starts.Add(1)
	s.mu.Unlock()
	return nil
}

// supervise waits for the current process to exit, then restarts with backoff
// until Stop is requested.
func (s *Supervisor) supervise() {
	defer func() {
		s.mu.Lock()
		ch := s.doneCh
		s.mu.Unlock()
		if ch != nil {
			// idempotent close — sync.Once-style by holding the lock during nil-swap.
			s.mu.Lock()
			if s.doneCh != nil {
				close(s.doneCh)
				s.doneCh = nil
			}
			s.mu.Unlock()
		}
	}()

	backoff := s.initBO
	for {
		s.mu.Lock()
		cmd := s.cmd
		s.mu.Unlock()
		if cmd == nil {
			return
		}

		err := cmd.Wait()
		s.alive.Store(false)
		s.exits.Add(1)

		s.mu.Lock()
		stopReq := s.stopReq
		s.cmd = nil
		s.mu.Unlock()

		if stopReq {
			if err != nil {
				log.Printf("[gpuhost/supervisor] streamer exited during shutdown: %v", err)
			}
			return
		}
		if err != nil {
			log.Printf("[gpuhost/supervisor] streamer exited: %v — restarting in %s", err, backoff)
		} else {
			log.Printf("[gpuhost/supervisor] streamer exited cleanly — restarting in %s", backoff)
		}

		// Sleep but be responsive to Stop during backoff.
		t := time.NewTimer(backoff)
		select {
		case <-t.C:
		}
		t.Stop()

		s.mu.Lock()
		stopReq = s.stopReq
		s.mu.Unlock()
		if stopReq {
			return
		}

		if err := s.spawnLocked(); err != nil {
			log.Printf("[gpuhost/supervisor] respawn failed: %v — backing off", err)
		} else {
			s.restarts.Add(1)
		}

		// Exponential backoff with cap.
		backoff *= 2
		if backoff > SupervisorMaxBackoff {
			backoff = SupervisorMaxBackoff
		}
	}
}

// Stop ends supervision: signals the current process and waits for the watch
// goroutine to exit. After Stop, the Supervisor may be started again.
func (s *Supervisor) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.cmd == nil && s.doneCh == nil {
		s.mu.Unlock()
		return nil
	}
	s.stopReq = true
	cmd := s.cmd
	doneCh := s.doneCh
	s.mu.Unlock()

	var firstErr error
	if cmd != nil && cmd.Process != nil {
		if err := signalProcess(cmd, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			firstErr = err
		}
	}

	// Wait up to TerminationGrace for the supervise loop to clear, then escalate.
	if doneCh != nil {
		grace := time.NewTimer(SupervisorTerminationGrace)
		select {
		case <-doneCh:
			grace.Stop()
		case <-grace.C:
			// SIGKILL the process and reap.
			if cmd != nil && cmd.Process != nil {
				_ = signalProcess(cmd, syscall.SIGKILL)
			}
			select {
			case <-doneCh:
			case <-ctx.Done():
				if firstErr == nil {
					firstErr = ctx.Err()
				}
			}
		}
	}
	return firstErr
}

// Alive reports whether the current process is running (true between a
// successful spawn and the corresponding Wait return).
func (s *Supervisor) Alive() bool { return s.alive.Load() }

// Starts returns the lifetime count of spawn attempts.
func (s *Supervisor) Starts() uint64 { return s.starts.Load() }

// Restarts returns the count of *successful* respawns (does not include the
// initial Start).
func (s *Supervisor) Restarts() uint64 { return s.restarts.Load() }

// Exits returns the count of process exits observed by the supervisor.
func (s *Supervisor) Exits() uint64 { return s.exits.Load() }

// signalProcess sends sig to the process group of cmd (so child processes the
// streamer may have spawned receive it too).
func signalProcess(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil {
		return syscall.Kill(-pgid, sig)
	}
	return cmd.Process.Signal(sig)
}

// drain reads r to EOF and discards. Without this the child's stdout/stderr
// would eventually block.
func drain(label string, r io.ReadCloser) {
	defer r.Close()
	_, _ = io.Copy(io.Discard, r)
}
