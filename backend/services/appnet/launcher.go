package appnet

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"vulos/backend/services/lease"
)

// appLogMaxBytes is the per-app log file rotation threshold (10 MiB).
const appLogMaxBytes = 10 * 1024 * 1024

// openAppLog opens (or creates) the per-app log file at
// ~/.vulos/logs/<appID>.log.  When the file already exceeds appLogMaxBytes it
// is atomically renamed to <appID>.log.old before a fresh file is created,
// so the old content is preserved and the new process gets a clean log.
//
// The returned *os.File must be closed by the caller when the app exits.
// On any error a nil is returned so callers can fall back to os.Stdout.
func openAppLog(appID string) *os.File {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	logsDir := filepath.Join(home, ".vulos", "logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return nil
	}

	logPath := filepath.Join(logsDir, appID+".log")

	// Rotate when the existing file is at or above the size threshold.
	if fi, err := os.Stat(logPath); err == nil && fi.Size() >= appLogMaxBytes {
		rotPath := logPath + ".old"
		// Remove any previous .old before rename so os.Rename does not fail on
		// systems that prohibit overwriting non-empty files.
		os.Remove(rotPath)
		if renameErr := os.Rename(logPath, rotPath); renameErr != nil {
			log.Printf("[appnet] log rotate warning for %s: %v", appID, renameErr)
			// Non-fatal: fall through; OpenFile below will truncate on O_TRUNC.
		}
	}

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[appnet] cannot open log file for %s (%v) — falling back to server stdout", appID, err)
		return nil
	}
	return f
}

// runLeaseTTL is the TTL for singleton run-leases (CONC-02).
const runLeaseTTL = 20 * time.Second

// runLeaseRenewEvery is how often the renew goroutine fires while an app runs.
// Must be well under runLeaseTTL to handle transient S3 latency.
const runLeaseRenewEvery = 5 * time.Second

// App represents a running application inside a namespace.
type App struct {
	ID          string
	Namespace   *Namespace
	Command     string
	Args        []string
	WorkDir     string
	Env         []string
	Process     *os.Process
	Running     bool
	Restarts    int
	MaxRestarts int // 0 = no auto-restart, 3 = default

	// runLease is non-nil for singleton apps; released when the app stops.
	runLease *lease.RunLease
	// stopRenew signals the renew loop to exit when the app stops normally.
	stopRenew chan struct{}
}

// Launcher manages app processes inside their namespaces.
type Launcher struct {
	mu      sync.Mutex
	manager *Manager
	apps    map[string]*App

	// leaseMgr, when non-nil, is used to gate singleton app launches.
	// If nil, run-lease gating is disabled (no S3 configured).
	leaseMgr *lease.Manager

	// holderID identifies this node/process in lease holder strings.
	holderID string
}

func NewLauncher(manager *Manager) *Launcher {
	return &Launcher{
		manager:  manager,
		apps:     make(map[string]*App),
		holderID: "node",
	}
}

// NewLauncherWithLease creates a Launcher with run-lease gating enabled.
// holderID should be a stable, unique node identifier (e.g. hostname or node UUID).
func NewLauncherWithLease(manager *Manager, leaseMgr *lease.Manager, holderID string) *Launcher {
	return &Launcher{
		manager:  manager,
		apps:     make(map[string]*App),
		leaseMgr: leaseMgr,
		holderID: holderID,
	}
}

// isSingleton reports whether the given concurrency value means singleton
// (active-passive).  Empty string defaults to singleton per manifest spec.
func isSingleton(concurrency string) bool {
	return concurrency == "" || concurrency == ConcurrencySingleton
}

// Launch starts an app inside its network namespace.
// The app can bind to any port it wants — the namespace isolates it.
// Traffic to host:hostPort is forwarded to ns:appPort via iptables.
// Profile defaults to "default"; use LaunchWithProfile to specify an explicit profile.
func (l *Launcher) Launch(ctx context.Context, appID, userID string, hostPort, appPort int, command string, args []string, workDir string, env []string) (*App, error) {
	return l.LaunchWithProfile(ctx, appID, userID, "default", hostPort, appPort, command, args, workDir, env)
}

// LaunchWithProfile starts an app inside its network namespace, scoped to the
// given profile.  When profile is "" or "default" the behaviour is identical
// to Launch (backwards-compatible key = userID-appID).  Any other profile
// value creates a separate, isolated namespace for the same appID
// (key = userID-profile:appID).
//
// For singleton apps (concurrency == "" or "singleton"), a run-lease
// (leases/run/<profile>/<appID>.json) is acquired before launch.  If another
// instance already holds the lease, ErrNotHolder is returned and no process is
// started.  The lease is renewed while the app runs and released on stop.
//
// Replicated and collaborative apps bypass the run-lease (active-active).
func (l *Launcher) LaunchWithProfile(ctx context.Context, appID, userID, profile string, hostPort, appPort int, command string, args []string, workDir string, env []string) (*App, error) {
	return l.launchWithConcurrency(ctx, appID, userID, profile, "", hostPort, appPort, command, args, workDir, env)
}

// LaunchManifest is like LaunchWithProfile but accepts the manifest so the
// launcher can read the concurrency field directly.
func (l *Launcher) LaunchManifest(ctx context.Context, m *AppManifest, userID, profile string, hostPort, appPort int) (*App, error) {
	return l.launchWithConcurrency(ctx, m.ID, userID, profile, m.Concurrency, hostPort, appPort, m.Command, nil, m.WorkDir, m.EnvSlice())
}

// launchWithConcurrency is the shared implementation used by LaunchWithProfile
// and LaunchManifest.  concurrency may be empty (defaults to singleton).
func (l *Launcher) launchWithConcurrency(ctx context.Context, appID, userID, profile, concurrency string, hostPort, appPort int, command string, args []string, workDir string, env []string) (*App, error) {
	// Scope app instance by user + profile — each (user, profile) pair gets
	// its own namespace.  net02ProfileKey keeps "default" backwards-compat.
	profileKey := net02ProfileKey(profile, appID)
	// Scope app instance by user — each user gets their own namespace
	instanceID := userID + "-" + profileKey

	l.mu.Lock()
	if existing, ok := l.apps[instanceID]; ok && existing.Running {
		l.mu.Unlock()
		return existing, nil
	}
	l.mu.Unlock()

	// ── Run-lease gate for singleton apps ─────────────────────────────────────
	//
	// Default (empty) concurrency == singleton.  Only gate when leaseMgr is
	// configured (non-nil); without S3 the system runs in single-node mode and
	// the local in-process dedup above is sufficient.
	var rl *lease.RunLease
	if isSingleton(concurrency) && l.leaseMgr != nil {
		// Use the profile as the lease profile key; fall back to "default".
		leaseProfile := profile
		if leaseProfile == "" {
			leaseProfile = "default"
		}
		var lerr error
		rl, lerr = lease.AcquireRunLease(ctx, l.leaseMgr, leaseProfile, appID, l.holderID, runLeaseTTL)
		if lerr != nil {
			// ErrNotHolder → another instance already holds the run-lease.
			// Return it directly so the caller can distinguish "already running
			// elsewhere" from a real error.
			return nil, fmt.Errorf("run-lease %s/%s: %w", leaseProfile, appID, lerr)
		}
		log.Printf("[appnet] run-lease acquired for %s/%s fence=%d", leaseProfile, appID, rl.Fence())
	}

	// Create namespace scoped to user
	ns, err := l.manager.Create(ctx, instanceID, userID, hostPort, appPort)
	if err != nil {
		if rl != nil {
			rl.Release(context.Background())
		}
		return nil, fmt.Errorf("create namespace: %w", err)
	}

	// Expand only the well-known numeric port tokens in the manifest command.
	// Do NOT perform generic ${VAR} substitution from the caller-supplied env
	// slice into the shell string: a caller could inject shell metacharacters
	// via an env value (e.g. KEY=x; rm -rf / → expanded into the sh -c arg).
	// Caller env entries are delivered to the process via cmd.Env instead.
	expandedCmd := strings.ReplaceAll(command, "${PORT}", fmt.Sprintf("%d", appPort))
	expandedCmd = strings.ReplaceAll(expandedCmd, "${CONSOLE_PORT}", fmt.Sprintf("%d", appPort))

	// Run inside namespace — use background context so the app outlives the HTTP request
	cmd := exec.Command("ip", "netns", "exec", ns.Name, "sh", "-c", expandedCmd)
	cmd.Dir = workDir
	// Minimal scrubbed environment — never inherit os.Environ() to avoid
	// leaking server secrets (API keys, tokens, etc.) into untrusted app processes.
	cmd.Env = append([]string{
		"PATH=/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin",
		"HOME=/root",
		"TMPDIR=/tmp",
	}, env...)
	cmd.Env = append(cmd.Env, fmt.Sprintf("PORT=%d", appPort))
	// Route stdout and stderr to a per-app log file under ~/.vulos/logs/<app-id>.log.
	// The file is rotated at appLogMaxBytes (10 MiB). Falls back to server stdout
	// when the log file cannot be opened (e.g. read-only fs, missing home dir).
	appLogFile := openAppLog(instanceID)
	if appLogFile != nil {
		cmd.Stdout = appLogFile
		cmd.Stderr = appLogFile
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true, // own process group so we can kill cleanly
	}

	log.Printf("[appnet] starting %s: ip netns exec %s sh -c %q", instanceID, ns.Name, expandedCmd)
	if err := cmd.Start(); err != nil {
		log.Printf("[appnet] start FAILED for %s: %v", instanceID, err)
		l.manager.Destroy(ctx, instanceID)
		if rl != nil {
			rl.Release(context.Background())
		}
		if appLogFile != nil {
			appLogFile.Close()
		}
		return nil, fmt.Errorf("start %s: %w", command, err)
	}
	log.Printf("[appnet] started %s pid=%d", instanceID, cmd.Process.Pid)

	stopRenew := make(chan struct{})

	app := &App{
		ID:          instanceID,
		Namespace:   ns,
		Command:     command,
		Args:        args,
		WorkDir:     workDir,
		Env:         env,
		Process:     cmd.Process,
		Running:     true,
		MaxRestarts: 0,
		runLease:    rl,
		stopRenew:   stopRenew,
	}

	l.mu.Lock()
	l.apps[instanceID] = app
	l.mu.Unlock()

	// ── Renew loop for singleton run-lease ────────────────────────────────────
	//
	// While the app runs, renew the lease every runLeaseRenewEvery.
	// If the lease is lost (e.g. this node stalled and another took over),
	// kill the local app to prevent stalled-then-resumed double-run.
	if rl != nil {
		rl.StartRenewLoop(context.Background(), runLeaseTTL, runLeaseRenewEvery, stopRenew, func(err error) {
			log.Printf("[appnet] run-lease lost for %s (%v) — killing local process to prevent double-run", instanceID, err)
			l.mu.Lock()
			a, ok := l.apps[instanceID]
			l.mu.Unlock()
			if ok && a.Process != nil {
				syscall.Kill(-a.Process.Pid, syscall.SIGTERM)
			}
		})
	}

	// Monitor process exit
	go func() {
		err := cmd.Wait()
		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}

		// Signal the renew loop to stop before we release the lease.
		close(stopRenew)

		l.mu.Lock()
		app.Running = false
		delete(l.apps, instanceID)
		l.mu.Unlock()

		// Release the run-lease so another instance can take over.
		if rl != nil {
			rl.Release(context.Background())
			log.Printf("[appnet] run-lease released for %s", instanceID)
		}

		// Close the per-app log file now that the process has exited.
		if appLogFile != nil {
			appLogFile.Close()
		}

		log.Printf("[appnet] app %s exited (code=%d, err=%v)", instanceID, exitCode, err)
	}()

	log.Printf("[appnet] launched %s (pid=%d) in %s — host:%d → %s:%d",
		appID, cmd.Process.Pid, ns.Name, hostPort, ns.NSIP, appPort)
	return app, nil
}

// Stop kills an app and tears down its namespace.
// Accepts either instanceID (userID-appID) or plain appID (stops all instances).
func (l *Launcher) Stop(ctx context.Context, appID string) error {
	l.mu.Lock()
	// Try exact match first
	app, ok := l.apps[appID]
	var key string
	if ok {
		key = appID
	} else {
		// Search by suffix (plain appID matches any user's instance)
		for k, a := range l.apps {
			if strings.HasSuffix(k, "-"+appID) {
				app = a
				key = k
				ok = true
				break
			}
		}
	}
	if !ok {
		l.mu.Unlock()
		return fmt.Errorf("app %s not found", appID)
	}
	delete(l.apps, key)
	l.mu.Unlock()

	if app.Process != nil && app.Running {
		// Kill the whole process group
		syscall.Kill(-app.Process.Pid, syscall.SIGTERM)

		// Give it 3 seconds then SIGKILL
		done := make(chan struct{})
		go func() {
			app.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			syscall.Kill(-app.Process.Pid, syscall.SIGKILL)
		}
	}

	return l.manager.Destroy(ctx, key)
}

// StopAll stops all running apps.
func (l *Launcher) StopAll(ctx context.Context) {
	l.mu.Lock()
	ids := make([]string, 0, len(l.apps))
	for id := range l.apps {
		ids = append(ids, id)
	}
	l.mu.Unlock()

	for _, id := range ids {
		l.Stop(ctx, id)
	}
}

// Status returns info about a running app.
func (l *Launcher) Status(appID string) (*App, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	app, ok := l.apps[appID]
	return app, ok
}

// ListRunning returns all running apps.
func (l *Launcher) ListRunning() []*App {
	l.mu.Lock()
	defer l.mu.Unlock()
	var result []*App
	for _, app := range l.apps {
		result = append(result, app)
	}
	return result
}
