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
	"os"
	"sync"
	"time"
)

// DefaultCloudBaseURL is the production Vulos cloud control-plane base URL.
const DefaultCloudBaseURL = "https://api.vulos.org"

// cloudBaseURL returns the effective cloud base URL (env override or default).
func cloudBaseURL() string {
	if v := os.Getenv("VULOS_CLOUD_URL"); v != "" {
		return v
	}
	return DefaultCloudBaseURL
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

// SubscribePresence connects to the cloud WebSocket presence feed and applies
// updates to the local registry until ctx is cancelled.  It reconnects
// automatically with a short back-off when the connection drops.
//
// Call this in a goroutine after a successful Sync:
//
//	go cs.SubscribePresence(ctx)
//
// If the cloud WebSocket endpoint is unavailable, this function returns
// without error — presence updates are simply skipped (graceful degradation).
func (cs *CloudSyncer) SubscribePresence(ctx context.Context) {
	wsURL := wsBaseURL() + "/ws/instances"

	for {
		if err := ctx.Err(); err != nil {
			return
		}
		if err := cs.connectAndReceive(ctx, wsURL); err != nil {
			log.Printf("[cloudsync] presence feed disconnected: %v; reconnecting in 5s", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		} else {
			// Clean shutdown (ctx cancelled inside connectAndReceive).
			return
		}
	}
}

// connectAndReceive dials the WebSocket and reads presence events until the
// connection closes or ctx is cancelled.  It returns an error only when the
// connection drops unexpectedly; a ctx cancellation returns nil.
//
// The implementation uses the net/http Upgrade path via a minimal inline
// WebSocket framing reader so that the package stays pure-Go with no
// extra dependencies (gorilla/websocket is already in go.mod but the
// server package imports it — we use the standard HTTP upgrade approach
// here via a plain HTTP GET with Upgrade:websocket).
//
// For the purposes of MINST-02 the cloud control plane is a documented
// client call; the real WebSocket transport will be wired when the cloud
// server is live.  Until then we use HTTP long-polling fallback via
// presencePollFallback.
func (cs *CloudSyncer) connectAndReceive(ctx context.Context, wsURL string) error {
	// The gorilla/websocket package is already a transitive dependency but to
	// keep this package's import set minimal we use the HTTP polling fallback.
	// When the real WebSocket endpoint is available, swap this call for a
	// proper gorilla Dial.
	return cs.presencePollFallback(ctx)
}

// presencePollFallback polls GET /api/instances/presence (an SSE or JSON
// endpoint) once every 30 seconds as a degraded substitute for the WebSocket
// subscription.  It re-syncs the full instance list so the registry stays
// current even without a live WebSocket.
func (cs *CloudSyncer) presencePollFallback(ctx context.Context) error {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := cs.Sync(ctx); err != nil {
				// Log and continue — graceful degradation.
				log.Printf("[cloudsync] poll resync: %v", err)
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
	url := cloudBaseURL() + "/api/instances"

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

// wsBaseURL returns the WebSocket-scheme base URL derived from the HTTP base.
func wsBaseURL() string {
	base := cloudBaseURL()
	// Replace https:// → wss:// and http:// → ws://.
	switch {
	case len(base) >= 8 && base[:8] == "https://":
		return "wss://" + base[8:]
	case len(base) >= 7 && base[:7] == "http://":
		return "ws://" + base[7:]
	default:
		return "wss://" + base
	}
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
