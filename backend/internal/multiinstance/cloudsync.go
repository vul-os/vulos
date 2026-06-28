// MINST-02: Cloud-sync — pull instance list from Vulos account on login.
//
// CloudSync contacts the Vulos cloud control plane after the OS device
// authenticates and:
//
//  1. Fetches the full list of instances enrolled under the same account
//     (GET https://api.vulos.org/api/instances) and upserts every entry into
//     the local Registry.
//  2. Subscribes to wss://api.vulos.org/ws/instances for real-time presence
//     updates (instance online / offline) and applies them to the registry via
//     MarkSeen / Upsert.
//  3. Exposes GET /api/instances on the local OS HTTP server, returning the
//     merged registry list. When the cloud is unreachable the endpoint
//     gracefully degrades to the last-known registry state.
//
// Wiring: call RegisterSyncHandlers(mux, syncer) from a routes_*.go file
// in cmd/server — do NOT import from main.go.
//
// The cloud endpoint base URL defaults to DefaultCloudBaseURL but can be
// overridden via the VULOS_CLOUD_URL environment variable for dev / self-hosted
// deployments.
package multiinstance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultCloudBaseURL is the production Vulos cloud control-plane base URL.
const DefaultCloudBaseURL = "https://api.vulos.org"

// cloudBaseURL returns the effective cloud base URL for this box's home region.
// It delegates to PlaceFor so that CP calls are region-aware: in Phase-0
// (single cell) this is identical to returning DefaultCloudBaseURL, but when a
// second cell is added the resolver will automatically route boxes in that cell
// to the correct CP without any further code changes.
func cloudBaseURL() string {
	return PlaceFor(boxRegion())
}

// requireSecureCloudBase returns the effective cloud base URL only if it uses
// https. The device bearer token ("Authorization: VulosDevice <token>") is sent
// on every connect-back, so a plaintext http base would let a network attacker
// harvest it. Refuse plaintext unless the explicit insecure escape hatch
// (VULOS_CLOUD_ALLOW_INSECURE=1|true|yes) is set, mirroring the lancert puller
// (lan/lancert_puller.go).
func requireSecureCloudBase() (string, error) {
	base := cloudBaseURL()
	switch strings.ToLower(strings.TrimSpace(os.Getenv("VULOS_CLOUD_ALLOW_INSECURE"))) {
	case "1", "true", "yes":
		return base, nil
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("multiinstance: VULOS_CLOUD_URL %q: %w", base, err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("multiinstance: cloud base URL must be https (got %q); "+
			"set VULOS_CLOUD_ALLOW_INSECURE=1 only for local dev", u.Scheme)
	}
	return base, nil
}

// CloudInstance is the wire format returned by the cloud
// GET /api/instances endpoint.  It is exported so that tests and alternative
// transport implementations can construct values directly.
type CloudInstance struct {
	ULID             string `json:"ulid"`
	DisplayName      string `json:"display_name"`
	Kind             string `json:"kind"`
	EndpointURL      string `json:"endpoint_url"`
	Ed25519PublicKey string `json:"ed25519_public_key"`
	Role             string `json:"role"`
	Status           string `json:"status"`
	LastSeenAt       string `json:"last_seen_at"` // RFC3339Nano; may be empty
	// Region is the home cell of this instance (default "eu").
	// Phase-0: the CP always returns "eu"; the field is present so the OS can
	// persist and route without a second parse step when a second cell arrives.
	Region string `json:"region,omitempty"`
}

// PresenceEvent is the wire format for WebSocket presence update messages
// from wss://api.vulos.org/ws/instances.  It is exported so that tests and
// alternative transport implementations can construct and apply events directly.
type PresenceEvent struct {
	// Type is "online", "offline", or "upsert".
	Type     string         `json:"type"`
	Instance *CloudInstance `json:"instance,omitempty"`
	ULID     string         `json:"ulid,omitempty"`
}

// CloudSyncer pulls the instance list from the Vulos cloud control plane and
// subscribes to real-time presence updates.  It is safe for concurrent use.
type CloudSyncer struct {
	reg        *Registry
	httpClient *http.Client
	// deviceToken is the credential sent as "Authorization: VulosDevice <token>".
	// It is set once at construction time and is safe to read from multiple
	// goroutines without locking.
	deviceToken string

	mu          sync.RWMutex
	lastSyncErr error
}

// NewCloudSyncer creates a CloudSyncer backed by reg.  deviceToken is the OS
// device credential forwarded to the cloud control plane for authentication.
// Pass an empty string when no credential is available yet (Sync will return a
// clear error but will not panic).
func NewCloudSyncer(reg *Registry, deviceToken string) *CloudSyncer {
	return &CloudSyncer{
		reg:         reg,
		deviceToken: deviceToken,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// Sync performs a one-shot pull of all instances from the cloud control plane
// and upserts them into the local registry.  It is intended to be called once
// on login and again on reconnect.
//
// If the cloud is unreachable, Sync returns a wrapped error but does NOT clear
// the local registry — the OS continues to operate with the last-known state
// (graceful degradation).
func (cs *CloudSyncer) Sync(ctx context.Context) error {
	instances, err := cs.fetchInstances(ctx)
	if err != nil {
		cs.setSyncErr(err)
		return fmt.Errorf("cloudsync: fetch: %w", err)
	}

	for _, ci := range instances {
		inst := cloudInstanceToLocal(ci)
		if uErr := cs.reg.Upsert(inst); uErr != nil {
			log.Printf("[cloudsync] upsert %s: %v", ci.ULID, uErr)
		}
	}
	cs.setSyncErr(nil)
	return nil
}

// SubscribePresence keeps the local registry current with the cloud's view of
// the account's instances until ctx is cancelled.
//
// HONEST STATUS (audit P2-7): there is NO live WebSocket transport yet. The
// cloud /ws/instances endpoint is not implemented on either side, so this
// method runs a 30-second full-resync poll loop — it does not open a socket and
// does not stream PresenceEvents. The previous code routed through a
// connectAndReceive "WebSocket" wrapper that immediately fell back to the same
// poll, which produced misleading "presence feed disconnected; reconnecting"
// logs for a feed that never connected. That dead branch has been removed; the
// poll loop is now called directly and logged honestly. ApplyPresenceEvent
// remains exported for when the real streaming transport lands and for tests.
//
// Call this in a goroutine after a successful Sync:
//
//	go cs.SubscribePresence(ctx)
//
// It returns when ctx is cancelled (graceful shutdown).
func (cs *CloudSyncer) SubscribePresence(ctx context.Context) {
	log.Printf("[cloudsync] presence: no live feed implemented — using %s full-resync poll", presencePollInterval)
	cs.pollPresence(ctx)
}

// presencePollInterval is the cadence of the full-resync presence poll used in
// lieu of a live streaming feed.
const presencePollInterval = 30 * time.Second

// pollPresence re-syncs the full instance list every presencePollInterval so
// the registry stays current without a live WebSocket. It returns when ctx is
// cancelled. Resync errors are logged and the loop continues (graceful
// degradation — the OS keeps operating on last-known state).
func (cs *CloudSyncer) pollPresence(ctx context.Context) {
	ticker := time.NewTicker(presencePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := cs.Sync(ctx); err != nil {
				log.Printf("[cloudsync] presence poll resync: %v", err)
			}
		}
	}
}

// ApplyPresenceEvent applies a single decoded presence event to the registry.
// It is exported so that tests and alternative transport implementations can
// call it directly without going through the WebSocket layer.
func (cs *CloudSyncer) ApplyPresenceEvent(ev PresenceEvent) {
	switch ev.Type {
	case "online":
		ulid := ev.ULID
		if ev.Instance != nil && ev.Instance.ULID != "" {
			ulid = ev.Instance.ULID
		}
		if ulid == "" {
			return
		}
		if err := cs.reg.MarkSeen(ulid); err != nil {
			log.Printf("[cloudsync] MarkSeen %s: %v", ulid, err)
		}
	case "offline":
		ulid := ev.ULID
		if ev.Instance != nil && ev.Instance.ULID != "" {
			ulid = ev.Instance.ULID
		}
		if ulid == "" {
			return
		}
		// Mark as offline — we do this with a direct Upsert updating only
		// the status field.  A zero LastSeenAt leaves the existing value intact.
		cs.applyStatus(ulid, StatusOffline)
	case "upsert":
		if ev.Instance == nil {
			return
		}
		inst := cloudInstanceToLocal(*ev.Instance)
		if err := cs.reg.Upsert(inst); err != nil {
			log.Printf("[cloudsync] upsert from event: %v", err)
		}
	}
}

// applyStatus fetches the existing instance and re-upserts with the new status.
// If the instance is not in the registry it is a no-op.
func (cs *CloudSyncer) applyStatus(ulid string, status Status) {
	inst, ok := cs.reg.Get(ulid)
	if !ok {
		return
	}
	inst.Status = status
	if err := cs.reg.Upsert(inst); err != nil {
		log.Printf("[cloudsync] applyStatus %s: %v", ulid, err)
	}
}

// fetchInstances calls GET {cloudBase}/api/instances and returns the decoded slice.
func (cs *CloudSyncer) fetchInstances(ctx context.Context) ([]CloudInstance, error) {
	base, err := requireSecureCloudBase()
	if err != nil {
		return nil, err
	}
	url := base + "/api/instances"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if cs.deviceToken != "" {
		req.Header.Set("Authorization", "VulosDevice "+cs.deviceToken)
	}

	resp, err := cs.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GET %s: status %d: %s", url, resp.StatusCode, body)
	}

	var instances []CloudInstance
	if err := json.NewDecoder(resp.Body).Decode(&instances); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return instances, nil
}

// cloudInstanceToLocal converts a CloudInstance wire type to the local Instance
// type understood by the Registry.
func cloudInstanceToLocal(ci CloudInstance) Instance {
	inst := Instance{
		ULID:             ci.ULID,
		DisplayName:      ci.DisplayName,
		EndpointURL:      ci.EndpointURL,
		Ed25519PublicKey: ci.Ed25519PublicKey,
		Kind:             Kind(ci.Kind),
		Role:             Role(ci.Role),
		Status:           Status(ci.Status),
		Region:           ci.Region,
	}
	if ci.LastSeenAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, ci.LastSeenAt); err == nil {
			inst.LastSeenAt = t
		}
	}
	// Apply defaults for zero values so Upsert's guard doesn't reject them.
	if inst.Kind == "" {
		inst.Kind = KindDevice
	}
	if inst.Role == "" {
		inst.Role = RolePeer
	}
	if inst.Status == "" {
		inst.Status = StatusUnknown
	}
	if inst.Region == "" {
		inst.Region = "eu"
	}
	return inst
}

// setSyncErr records the last sync error under the write lock.
func (cs *CloudSyncer) setSyncErr(err error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.lastSyncErr = err
}

// LastSyncErr returns the error from the most recent Sync call, or nil if the
// last sync succeeded.
func (cs *CloudSyncer) LastSyncErr() error {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.lastSyncErr
}

// RegisterSyncHandlers wires GET /api/instances into mux.
//
// The endpoint returns the full merged list from the local registry.  When the
// cloud is unreachable (offline mode) it returns the last-known state with a
// 200 OK (graceful degradation).  Cloud sync errors are surfaced in the
// "cloud_sync_error" field of the response envelope.
//
// Usage (from a routes_*.go in cmd/server — never from main.go):
//
//	multiinstance.RegisterSyncHandlers(mux, syncer)
func RegisterSyncHandlers(mux *http.ServeMux, cs *CloudSyncer) {
	mux.HandleFunc("GET /api/instances", func(w http.ResponseWriter, r *http.Request) {
		list, err := cs.reg.List()
		if err != nil {
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []Instance{} // return [] not null
		}

		type response struct {
			Instances      []Instance `json:"instances"`
			CloudSyncError string     `json:"cloud_sync_error,omitempty"`
		}

		resp := response{Instances: list}
		if syncErr := cs.LastSyncErr(); syncErr != nil {
			resp.CloudSyncError = syncErr.Error()
		}

		w.Header().Set("Content-Type", "application/json")
		if encErr := json.NewEncoder(w).Encode(resp); encErr != nil {
			return
		}
	})
}
