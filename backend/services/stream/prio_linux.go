//go:build linux

package stream

import (
	"errors"
	"fmt"
	"log"
	"syscall"
	"unsafe"
)

// schedParam mirrors struct sched_param { int sched_priority; }.
// We need it for the sched_setscheduler(2) syscall.
type schedParam struct {
	schedPriority int32
}

const (
	schedFIFO = 1   // SCHED_FIFO scheduling policy
	niceGame  = -10 // nice value for gaming processes
)

// setRealtimePriority raises scheduling priority for pid in gaming mode.
//
// Strategy (in order):
//  1. sched_setscheduler(pid, SCHED_FIFO, priority=10) — requires CAP_SYS_NICE
//     or --cap-add SYS_NICE in Docker.
//  2. On EPERM: fall back to setpriority(PRIO_PROCESS, pid, -10).
//  3. If setpriority also fails with EPERM: log a warning and return nil
//     (soft-fail — container is unprivileged; gaming still works, just at
//     normal OS priority).
//
// All other errors are returned to the caller (applyGamingPriority) which
// logs them. Non-gaming code paths never call this function.
func setRealtimePriority(pid int) error {
	// Attempt SCHED_FIFO first.
	param := schedParam{schedPriority: 10}
	_, _, errno := syscall.RawSyscall(
		syscall.SYS_SCHED_SETSCHEDULER,
		uintptr(pid),
		uintptr(schedFIFO),
		uintptr(unsafe.Pointer(&param)),
	)
	if errno == 0 {
		log.Printf("[stream] gaming priority: pid=%d → SCHED_FIFO/10", pid)
		return nil
	}
	if !errors.Is(errno, syscall.EPERM) {
		return fmt.Errorf("sched_setscheduler: %w", errno)
	}

	// EPERM — no CAP_SYS_NICE. Fall back to nice -10 via setpriority(2).
	// The kernel reads the third argument as a signed int; we pass the
	// two's-complement bit pattern via a typed conversion through int.
	niceVal := int(niceGame)
	_, _, errno = syscall.RawSyscall(
		syscall.SYS_SETPRIORITY,
		0, // PRIO_PROCESS
		uintptr(pid),
		uintptr(niceVal),
	)
	if errno == 0 {
		log.Printf("[stream] gaming priority: pid=%d → nice %d (SCHED_FIFO unavailable: EPERM)", pid, niceGame)
		return nil
	}
	if errors.Is(errno, syscall.EPERM) {
		// Completely unprivileged — soft-fail with a warning.
		log.Printf("[stream] gaming priority: pid=%d — SYS_NICE unavailable (add --cap-add SYS_NICE for best performance)", pid)
		return nil
	}
	return fmt.Errorf("setpriority: %w", errno)
}
