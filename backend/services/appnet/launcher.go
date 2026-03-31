package appnet

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

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
}

// Launcher manages app processes inside their namespaces.
type Launcher struct {
	mu      sync.Mutex
	manager *Manager
	apps    map[string]*App
}

func NewLauncher(manager *Manager) *Launcher {
	return &Launcher{
		manager: manager,
		apps:    make(map[string]*App),
	}
}

// Launch starts an app inside its network namespace.
// The app can bind to any port it wants — the namespace isolates it.
// Traffic to host:hostPort is forwarded to ns:appPort via iptables.
func (l *Launcher) Launch(ctx context.Context, appID, userID string, hostPort, appPort int, command string, args []string, workDir string, env []string) (*App, error) {
	// Scope app instance by user — each user gets their own namespace
	instanceID := userID + "-" + appID

	l.mu.Lock()
	if existing, ok := l.apps[instanceID]; ok && existing.Running {
		l.mu.Unlock()
		return existing, nil
	}
	l.mu.Unlock()

	// Create namespace scoped to user
	ns, err := l.manager.Create(ctx, instanceID, userID, hostPort, appPort)
	if err != nil {
		return nil, fmt.Errorf("create namespace: %w", err)
	}

	// Expand ${PORT} and ${CONSOLE_PORT} in command
	expandedCmd := strings.ReplaceAll(command, "${PORT}", fmt.Sprintf("%d", appPort))

	// Expand env vars in command (e.g. ${CONSOLE_PORT})
	for _, e := range env {
		if parts := strings.SplitN(e, "=", 2); len(parts) == 2 {
			expandedCmd = strings.ReplaceAll(expandedCmd, "${"+parts[0]+"}", parts[1])
		}
	}

	// Run inside namespace — use background context so the app outlives the HTTP request
	cmd := exec.Command("ip", "netns", "exec", ns.Name, "sh", "-c", expandedCmd)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env, fmt.Sprintf("PORT=%d", appPort))
	cmd.Stdout = os.Stdout // TODO: capture to log file per app
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true, // own process group so we can kill cleanly
	}

	log.Printf("[appnet] starting %s: ip netns exec %s sh -c %q", instanceID, ns.Name, expandedCmd)
	if err := cmd.Start(); err != nil {
		log.Printf("[appnet] start FAILED for %s: %v", instanceID, err)
		l.manager.Destroy(ctx, instanceID)
		return nil, fmt.Errorf("start %s: %w", command, err)
	}
	log.Printf("[appnet] started %s pid=%d", instanceID, cmd.Process.Pid)

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
	}

	l.mu.Lock()
	l.apps[instanceID] = app
	l.mu.Unlock()

	// Monitor process exit
	go func() {
		err := cmd.Wait()
		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		l.mu.Lock()
		app.Running = false
		delete(l.apps, instanceID)
		l.mu.Unlock()
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
