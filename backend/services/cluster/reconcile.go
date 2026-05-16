// Package cluster manages multi-node coordination for Vula OS.
// It reconciles the desired installed-apps state (from the shared cr-sqlite
// installed_apps table) against the actual state on the local filesystem,
// calling appnet.AppStore.InstallFromRegistry / Uninstall as needed.
//
// CLUSTER-07: App registry reconciler.
package cluster

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"vulos/backend/services/appnet"
)

// ---------------------------------------------------------------------------
// installed_apps table (CRR — synced via cr-sqlite across nodes)
// ---------------------------------------------------------------------------

// AppSyncStatus mirrors the status column in installed_apps.
type AppSyncStatus string

const (
	AppSyncActive  AppSyncStatus = "active"
	AppSyncRemoved AppSyncStatus = "removed"
	AppSyncPending AppSyncStatus = "pending"
)

// InstalledAppRecord is one row of the installed_apps CRDT table.
// In production this lives in a cr-sqlite database; here it is
// persisted as JSON so the reconciler can run without an external
// driver dependency.
type InstalledAppRecord struct {
	AppID       string        `json:"app_id"`
	Version     string        `json:"version"`
	InstalledBy string        `json:"installed_by"` // node that first installed it
	InstalledAt time.Time     `json:"installed_at"`
	Status      AppSyncStatus `json:"status"`
}

// ---------------------------------------------------------------------------
// local_app_status table (local only — NOT synced)
// ---------------------------------------------------------------------------

// LocalAppState mirrors the state column in local_app_status.
type LocalAppState string

const (
	LocalAppInstalling LocalAppState = "installing"
	LocalAppInstalled  LocalAppState = "installed"
	LocalAppFailed     LocalAppState = "failed"
	LocalAppRemoving   LocalAppState = "removing"
	LocalAppSkipped    LocalAppState = "skipped"
)

// LocalAppStatusRecord is one row of the local_app_status table.
// Tracks per-node install progress and retry metadata.
type LocalAppStatusRecord struct {
	AppID     string        `json:"app_id"`
	State     LocalAppState `json:"state"`
	Progress  string        `json:"progress,omitempty"` // human-readable progress: "downloading 45%", …
	Error     string        `json:"error,omitempty"`
	UpdatedAt time.Time     `json:"updated_at"`

	// Retry backoff fields (local only, not in roadmap schema but required by AC).
	RetryCount  int       `json:"retry_count,omitempty"`
	NextRetryAt time.Time `json:"next_retry_at,omitempty"`
}

// ---------------------------------------------------------------------------
// appReconcileStore — thin JSON-file persistence layer
// ---------------------------------------------------------------------------
// This stands in for the cr-sqlite / SQLite store until the full store pkg
// (CLUSTER-01) is merged.  The interface is kept narrow so it can be
// replaced with real DB calls transparently.

type appReconcileStore struct {
	mu          sync.RWMutex
	desiredPath string // installed_apps.json   (would be a shared DB table)
	localPath   string // local_app_status.json (node-local)
	desired     map[string]*InstalledAppRecord
	localStatus map[string]*LocalAppStatusRecord
}

func newAppReconcileStore(dataDir string) (*appReconcileStore, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	s := &appReconcileStore{
		desiredPath: filepath.Join(dataDir, "installed_apps.json"),
		localPath:   filepath.Join(dataDir, "local_app_status.json"),
		desired:     make(map[string]*InstalledAppRecord),
		localStatus: make(map[string]*LocalAppStatusRecord),
	}
	_ = s.loadDesired()
	_ = s.loadLocal()
	return s, nil
}

// --- installed_apps helpers -------------------------------------------------

func (s *appReconcileStore) loadDesired() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.desiredPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var rows []*InstalledAppRecord
	if err := json.Unmarshal(data, &rows); err != nil {
		return err
	}
	s.desired = make(map[string]*InstalledAppRecord, len(rows))
	for _, r := range rows {
		s.desired[r.AppID] = r
	}
	return nil
}

func (s *appReconcileStore) saveDesired() error {
	rows := make([]*InstalledAppRecord, 0, len(s.desired))
	for _, r := range s.desired {
		rows = append(rows, r)
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.desiredPath, data, 0644)
}

// UpsertDesiredApp inserts or updates a row in installed_apps.
func (s *appReconcileStore) UpsertDesiredApp(rec *InstalledAppRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.desired[rec.AppID] = rec
	return s.saveDesired()
}

// ListDesiredApps returns all rows in installed_apps.
func (s *appReconcileStore) ListDesiredApps() []*InstalledAppRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*InstalledAppRecord, 0, len(s.desired))
	for _, r := range s.desired {
		out = append(out, r)
	}
	return out
}

// --- local_app_status helpers -----------------------------------------------

func (s *appReconcileStore) loadLocal() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.localPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var rows []*LocalAppStatusRecord
	if err := json.Unmarshal(data, &rows); err != nil {
		return err
	}
	s.localStatus = make(map[string]*LocalAppStatusRecord, len(rows))
	for _, r := range rows {
		s.localStatus[r.AppID] = r
	}
	return nil
}

func (s *appReconcileStore) saveLocal() error {
	rows := make([]*LocalAppStatusRecord, 0, len(s.localStatus))
	for _, r := range s.localStatus {
		rows = append(rows, r)
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.localPath, data, 0644)
}

// SetLocalStatus upserts a row in local_app_status.
func (s *appReconcileStore) SetLocalStatus(rec *LocalAppStatusRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec.UpdatedAt = time.Now().UTC()
	s.localStatus[rec.AppID] = rec
	return s.saveLocal()
}

// GetLocalStatus returns the local status for an app, or nil if unknown.
func (s *appReconcileStore) GetLocalStatus(appID string) *LocalAppStatusRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.localStatus[appID]
}

// ListLocalStatuses returns all rows in local_app_status.
func (s *appReconcileStore) ListLocalStatuses() []*LocalAppStatusRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*LocalAppStatusRecord, 0, len(s.localStatus))
	for _, r := range s.localStatus {
		out = append(out, r)
	}
	return out
}

// ---------------------------------------------------------------------------
// Backoff schedule (AC: failures recorded + retried with backoff)
// ---------------------------------------------------------------------------

// reconcileBackoffSchedule returns successive retry delays.
// Matches the roadmap spec: 30 s, 1 m, 5 m, 15 m, then stop.
var reconcileBackoffSchedule = []time.Duration{
	30 * time.Second,
	1 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
}

// reconcileNextBackoff returns the next retry delay for a given attempt count (0-based).
// Returns 0 when the retry limit has been exhausted.
func reconcileNextBackoff(attempt int) time.Duration {
	if attempt < 0 || attempt >= len(reconcileBackoffSchedule) {
		return 0 // exhausted
	}
	return reconcileBackoffSchedule[attempt]
}

// ---------------------------------------------------------------------------
// AppInstaller — interface satisfied by *appnet.AppStore
// ---------------------------------------------------------------------------
// Defining the interface here keeps reconcile.go independent of the concrete
// AppStore type and makes testing straightforward with a mock.

// AppInstaller is the subset of appnet.AppStore used by the reconciler.
type AppInstaller interface {
	// Installed lists all locally installed apps.
	Installed() ([]*appnet.AppManifest, error)
	// InstallFromRegistry installs an app from the vetted registry.
	InstallFromRegistry(ctx context.Context, appID, version string) error
	// Uninstall removes a locally installed app.
	Uninstall(appID string) error
}

// ---------------------------------------------------------------------------
// Reconciler
// ---------------------------------------------------------------------------

// reconcileInterval is how often the background loop calls ReconcileApps.
// Uses a distinct identifier to avoid collisions with heartbeatInterval /
// staleThreshold / leaseHeartbeatInterval from other cluster files.
const reconcileInterval = 30 * time.Second

// Reconciler diffs the desired app state (installed_apps table) against the
// actual installed apps on disk and converges the two.
type Reconciler struct {
	store    *appReconcileStore
	appStore AppInstaller
	nodeID   string

	mu     sync.Mutex
	stopCh chan struct{}
}

// NewReconciler creates a Reconciler.
//
//	dataDir  — directory where installed_apps.json / local_app_status.json are stored
//	appStore — concrete *appnet.AppStore (or test mock)
//	nodeID   — unique identifier for this node (used in InstalledBy field)
func NewReconciler(dataDir string, appStore AppInstaller, nodeID string) (*Reconciler, error) {
	s, err := newAppReconcileStore(dataDir)
	if err != nil {
		return nil, err
	}
	return &Reconciler{
		store:    s,
		appStore: appStore,
		nodeID:   nodeID,
		stopCh:   make(chan struct{}),
	}, nil
}

// RegisterApp adds or updates an app in the desired state (installed_apps CRR).
// Call this when the local node installs an app so the record syncs to peers.
func (r *Reconciler) RegisterApp(appID, version string) error {
	return r.store.UpsertDesiredApp(&InstalledAppRecord{
		AppID:       appID,
		Version:     version,
		InstalledBy: r.nodeID,
		InstalledAt: time.Now().UTC(),
		Status:      AppSyncActive,
	})
}

// MarkAppRemoved sets status = 'removed' in installed_apps so peers reconcile.
func (r *Reconciler) MarkAppRemoved(appID string) error {
	rec := &InstalledAppRecord{
		AppID:  appID,
		Status: AppSyncRemoved,
	}
	// Preserve existing metadata if available.
	if existing := r.store.desired[appID]; existing != nil {
		rec.Version = existing.Version
		rec.InstalledBy = existing.InstalledBy
		rec.InstalledAt = existing.InstalledAt
	}
	return r.store.UpsertDesiredApp(rec)
}

// ListDesiredApps exposes the installed_apps table contents.
func (r *Reconciler) ListDesiredApps() []*InstalledAppRecord {
	return r.store.ListDesiredApps()
}

// ListLocalStatuses exposes the local_app_status table contents.
func (r *Reconciler) ListLocalStatuses() []*LocalAppStatusRecord {
	return r.store.ListLocalStatuses()
}

// ReconcileApps performs one reconcile pass:
//  1. Reads desired state from installed_apps (status = 'active').
//  2. Reads actual state via AppInstaller.Installed().
//  3. Installs missing apps; uninstalls removed apps.
//  4. Records progress and backoff in local_app_status.
//
// It is safe to call concurrently — each invocation operates under a mutex.
func (r *Reconciler) ReconcileApps(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 1. Desired state: active entries from installed_apps.
	desired := make(map[string]*InstalledAppRecord)
	for _, rec := range r.store.ListDesiredApps() {
		if rec.Status == AppSyncActive {
			desired[rec.AppID] = rec
		}
	}

	// 2. Actual state: apps currently on disk.
	actualList, err := r.appStore.Installed()
	if err != nil {
		log.Printf("[cluster/reconcile] failed to list installed apps: %v", err)
		return
	}
	actual := make(map[string]*appnet.AppManifest, len(actualList))
	for _, m := range actualList {
		actual[m.ID] = m
	}

	// 3. Diff — compute toInstall and toRemove sets.
	toInstall := make(map[string]*InstalledAppRecord)
	for id, rec := range desired {
		if _, ok := actual[id]; !ok {
			toInstall[id] = rec
		}
	}

	toRemove := make(map[string]*appnet.AppManifest)
	for id, m := range actual {
		if _, ok := desired[id]; !ok {
			toRemove[id] = m
		}
	}

	// 4a. Install missing apps.
	for id, rec := range toInstall {
		// Honour backoff: skip if a previous failure is still cooling down.
		if localRec := r.store.GetLocalStatus(id); localRec != nil {
			if localRec.State == LocalAppFailed && !localRec.NextRetryAt.IsZero() {
				if time.Now().Before(localRec.NextRetryAt) {
					log.Printf("[cluster/reconcile] %s: still in backoff until %s, skipping",
						id, localRec.NextRetryAt.Format(time.RFC3339))
					continue
				}
			}
			if localRec.State == LocalAppInstalling {
				log.Printf("[cluster/reconcile] %s: install already in progress, skipping", id)
				continue
			}
			// Bail out after all retries are exhausted.
			if localRec.State == LocalAppFailed && localRec.RetryCount >= len(reconcileBackoffSchedule) {
				log.Printf("[cluster/reconcile] %s: retry limit reached, skipping", id)
				continue
			}
		}

		r.installApp(ctx, rec)
	}

	// 4b. Uninstall removed apps.
	for id := range toRemove {
		r.uninstallApp(id)
	}
}

// installApp runs InstallFromRegistry for one app and updates local_app_status.
func (r *Reconciler) installApp(ctx context.Context, rec *InstalledAppRecord) {
	appID := rec.AppID

	// Determine retry count from existing status record.
	retryCount := 0
	if prev := r.store.GetLocalStatus(appID); prev != nil {
		retryCount = prev.RetryCount
	}

	// Mark as installing.
	_ = r.store.SetLocalStatus(&LocalAppStatusRecord{
		AppID:      appID,
		State:      LocalAppInstalling,
		Progress:   "queued",
		RetryCount: retryCount,
	})

	log.Printf("[cluster/reconcile] installing %s@%s (attempt %d)", appID, rec.Version, retryCount+1)

	if err := r.appStore.InstallFromRegistry(ctx, appID, rec.Version); err != nil {
		log.Printf("[cluster/reconcile] install failed %s: %v", appID, err)

		nextDelay := reconcileNextBackoff(retryCount)
		var nextRetry time.Time
		if nextDelay > 0 {
			nextRetry = time.Now().Add(nextDelay)
			log.Printf("[cluster/reconcile] %s will retry in %s", appID, nextDelay)
		} else {
			log.Printf("[cluster/reconcile] %s: no more retries, marking failed", appID)
		}

		_ = r.store.SetLocalStatus(&LocalAppStatusRecord{
			AppID:       appID,
			State:       LocalAppFailed,
			Error:       err.Error(),
			RetryCount:  retryCount + 1,
			NextRetryAt: nextRetry,
		})
		return
	}

	log.Printf("[cluster/reconcile] installed %s@%s", appID, rec.Version)
	_ = r.store.SetLocalStatus(&LocalAppStatusRecord{
		AppID:    appID,
		State:    LocalAppInstalled,
		Progress: "installed",
	})
}

// uninstallApp runs Uninstall for one app and updates local_app_status.
func (r *Reconciler) uninstallApp(appID string) {
	_ = r.store.SetLocalStatus(&LocalAppStatusRecord{
		AppID: appID,
		State: LocalAppRemoving,
	})

	log.Printf("[cluster/reconcile] uninstalling %s", appID)

	if err := r.appStore.Uninstall(appID); err != nil {
		log.Printf("[cluster/reconcile] uninstall failed %s: %v", appID, err)
		_ = r.store.SetLocalStatus(&LocalAppStatusRecord{
			AppID: appID,
			State: LocalAppFailed,
			Error: err.Error(),
		})
		return
	}

	log.Printf("[cluster/reconcile] uninstalled %s", appID)
	// Remove from local status now that it's gone.
	r.store.mu.Lock()
	delete(r.store.localStatus, appID)
	_ = r.store.saveLocal()
	r.store.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Background loop
// ---------------------------------------------------------------------------

// Start launches a background goroutine that calls ReconcileApps every
// reconcileInterval (30 s). It returns immediately. Call Stop to shut down.
func (r *Reconciler) Start(ctx context.Context) {
	go func() {
		// Run once immediately on start.
		r.ReconcileApps(ctx)

		ticker := time.NewTicker(reconcileInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				r.ReconcileApps(ctx)
			case <-r.stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Stop shuts down the background reconcile loop.
func (r *Reconciler) Stop() {
	select {
	case <-r.stopCh:
		// already closed
	default:
		close(r.stopCh)
	}
}
