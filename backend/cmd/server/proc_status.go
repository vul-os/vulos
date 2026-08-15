package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"vulos/backend/internal/proctl"
	"vulos/backend/services/appnet"
	"vulos/backend/services/gateway"
	"vulos/backend/services/stream"
	"vulos/backend/services/telemetry"
)

// collectAppStatus answers "what is running, and is it responding?" for every
// app the box runs — and it is the one place the FOUR CATEGORIES are kept
// apart. The temptation is a single `frozen` flag per row; this file exists to
// refuse it, because the mechanism available differs per category and three of
// four would then be a guess presented as a measurement:
//
//	process-backed apps  — HTTP liveness probe through the app's namespace.
//	                       Real: a server that has stopped draining its
//	                       accept loop is the same condition macOS reports.
//	streamed X11 apps    — UNKNOWN. X's own mechanism is _NET_WM_PING and
//	                       Vulos has no X client to send one.
//	built-in surfaces    — NOT APPLICABLE. React views in the shell's tab;
//	                       no process, no port, nothing to probe.
//	system processes     — the /proc state letter, reported as a NOTE and
//	                       never as a verdict. Handled in the process list,
//	                       not here.
func collectAppStatus(launcher *appnet.Launcher, gw *gateway.Gateway, pool *stream.Pool) []telemetry.AppStatus {
	out := []telemetry.AppStatus{}

	// Process-backed apps. Probed concurrently: the probe budget is 3s and a
	// box with a dozen apps, one of which is genuinely hung, would otherwise
	// make the app switcher wait for the sum rather than the maximum.
	running := launcher.ListRunning()
	probes := make([]proctl.Responsiveness, len(running))
	done := make(chan int, len(running))
	for i, a := range running {
		go func(i int, a *appnet.App) {
			if gw != nil && a.Running {
				healthy, code := gw.HealthCheck(a.ID)
				if healthy || code > 0 {
					// Any complete response is evidence of life, a 5xx
					// included: the app accepted the connection, routed the
					// request and generated a reply. HealthCheck returns
					// code 0 only when nothing came back at all.
					probes[i] = proctl.Responsiveness{
						Status: proctl.StatusResponding,
						Method: proctl.MethodHTTPProbe,
						Detail: "HTTP " + strconv.Itoa(code),
					}
				} else {
					probes[i] = proctl.Responsiveness{
						Status: proctl.StatusNotResponding,
						Method: proctl.MethodHTTPProbe,
						Detail: "no reply within the probe budget",
					}
				}
			} else {
				probes[i] = proctl.Responsiveness{
					Status: proctl.StatusUnknown,
					Method: proctl.MethodNone,
					Detail: "app is not running",
				}
			}
			done <- i
		}(i, a)
	}
	for range running {
		<-done
	}

	for i, a := range running {
		pid := 0
		if a.Process != nil {
			pid = a.Process.Pid
		}
		out = append(out, telemetry.AppStatus{
			AppID:      a.ID,
			Name:       a.ID,
			Kind:       telemetry.KindProcess,
			Running:    a.Running,
			PID:        pid,
			MemRSS:     rssOf(pid),
			Responding: probes[i],
			Closable:   true,
		})
	}

	// Streamed X11 desktop apps.
	if pool != nil {
		for _, s := range pool.List() {
			out = append(out, telemetry.AppStatus{
				AppID:   s.ID,
				Name:    s.Name,
				Kind:    telemetry.KindStream,
				Running: s.Running,
				// No pid: Session's *exec.Cmd fields are unexported, and
				// reaching for them would hand the client a pid it must not
				// signal directly — the session owns the teardown.
				Responding: proctl.StreamUnknown(),
				Closable:   true,
			})
		}
	}

	return out
}

// rssOf reads a process's resident set size, or 0 when it cannot.
//
// Field 24 of /proc/<pid>/stat is RSS in PAGES, not bytes. Reporting the raw
// number would understate memory by a factor of the page size — 4096 on every
// machine this runs on — and a memory column that is wrong by 4096x looks
// merely small rather than broken.
func rssOf(pid int) uint64 {
	if pid <= 0 {
		return 0
	}
	data, err := os.ReadFile(filepath.Join(proctl.DefaultRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0
	}
	lastParen := strings.LastIndex(string(data), ")")
	if lastParen < 0 || lastParen+2 >= len(data) {
		return 0
	}
	rest := strings.Fields(string(data)[lastParen+2:])
	if len(rest) < 22 {
		return 0
	}
	pages, _ := strconv.ParseUint(rest[21], 10, 64)
	return pages * uint64(os.Getpagesize())
}
