// Package cluster implements multi-node cluster coordination for Vula OS.
// Nodes announce themselves by writing metadata to S3 (via the CLUSTER-03
// encrypted client) and discover peers by reading those metadata objects.
//
// Usage:
//
//	cfg := cluster.LoadS3Config()
//	c, err := cluster.New(cfg, passphrase)
//	if err != nil {
//	    // S3 not configured or unreachable — cluster is disabled, no fatal.
//	    log.Println("cluster: disabled:", err)
//	    return
//	}
//	// Wire the heartbeat goroutine from your orchestrator when ready:
//	go c.Start(ctx)
package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"vulos/backend/services/lease"
)

const (
	// nodeMetaPrefix is the S3 key prefix under which each node writes its meta.
	// Full key: nodes/{node_id}/meta.json
	nodeMetaPrefix = "nodes/"

	// heartbeatInterval is how often Register() re-writes last_seen.
	heartbeatInterval = 30 * time.Second

	// staleThreshold is the duration after which a peer is considered stale.
	staleThreshold = 2 * time.Minute
)

// WellKnownLeaseScopes lists the always-present lease objects that must exist
// before any CAS operation.  Add new scopes here as features require them.
// EnsureLeases creates each of these in "free" state at cluster bootstrap.
var WellKnownLeaseScopes = []string{
	"cluster-init", // guards one-time cluster provisioning
}

// NodeMeta is the JSON structure written to S3 at nodes/{node_id}/meta.json.
// All fields are exported so they round-trip cleanly through encoding/json.
type NodeMeta struct {
	ID       string    `json:"id"`
	Mode     string    `json:"mode"` // "server" or "local"
	Hostname string    `json:"hostname"`
	LastSeen time.Time `json:"last_seen"`
	Storage  bool      `json:"storage"` // true when this node runs MinIO
}

// Node is the view of a cluster peer as returned by Peers().
type Node struct {
	NodeMeta
	Stale bool `json:"stale"` // true when LastSeen > staleThreshold ago
}

// Cluster manages node registration and peer discovery via S3.
// When S3 is not configured, the zero value is safe — all operations
// return empty/nil results without error (disabled mode).
type Cluster struct {
	client  *Client  // nil when disabled
	cfg     S3Config // stored so InitLeases can build a plain lease.Manager
	meta    NodeMeta // this node's identity
	mu      sync.RWMutex
	started bool
}

// New creates a Cluster wired to the encrypted S3 client.
// If cfg is not configured (missing credentials) it returns a disabled
// Cluster (non-nil, no error) so callers never crash on missing S3.
// A real connection error (bad credentials, unreachable endpoint) is
// returned so the operator sees it in logs.
func New(cfg S3Config, passphrase string) (*Cluster, error) {
	if !cfg.Configured() {
		return newDisabled(), nil
	}

	// Derive node identity from environment, falling back to hostname.
	nodeID := getenvOrDefault("VULOS_NODE_ID", hostname())
	mode := getenvOrDefault("VULOS_MODE", "local")
	_, hasStorage := os.LookupEnv("VULOS_STORAGE_ACCESS_KEY")

	meta := NodeMeta{
		ID:       nodeID,
		Mode:     mode,
		Hostname: hostname(),
		Storage:  hasStorage,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := NewClient(ctx, cfg, passphrase)
	if err != nil {
		return nil, fmt.Errorf("cluster: init: %w", err)
	}

	c := &Cluster{
		client: client,
		cfg:    cfg,
		meta:   meta,
	}

	// Bootstrap: create the well-known lease objects in "free" state.
	// Idempotent — safe to call on every startup; held leases are never clobbered.
	if err := c.InitLeases(ctx); err != nil {
		// Non-fatal: log and continue. The lease objects may already exist
		// (most re-runs), or the bucket may not have lease support yet.
		fmt.Fprintf(os.Stderr, "cluster: InitLeases (non-fatal): %v\n", err)
	}

	return c, nil
}

// InitLeases creates the well-known lease objects (WellKnownLeaseScopes) in
// "free" state inside the cluster bucket if they do not already exist.
// It is idempotent: calling it again when leases are already present (in any
// state) is a no-op — a held lease is never clobbered.
// If the cluster is disabled it returns nil immediately.
func (c *Cluster) InitLeases(ctx context.Context) error {
	if !c.Enabled() {
		return nil
	}

	leaseCfg := lease.S3Config{
		Endpoint:  c.cfg.Endpoint,
		Bucket:    c.cfg.Bucket,
		AccessKey: c.cfg.AccessKey,
		SecretKey: c.cfg.SecretKey,
		UseSSL:    c.cfg.UseSSL,
	}

	mgr, err := lease.New(leaseCfg)
	if err != nil {
		return fmt.Errorf("cluster: InitLeases: %w", err)
	}

	for _, scope := range WellKnownLeaseScopes {
		if err := mgr.EnsureLease(ctx, scope); err != nil {
			return fmt.Errorf("cluster: InitLeases %q: %w", scope, err)
		}
	}
	return nil
}

// newDisabled returns a Cluster whose client is nil — all operations are no-ops.
func newDisabled() *Cluster {
	return &Cluster{
		meta: NodeMeta{
			ID:       getenvOrDefault("VULOS_NODE_ID", hostname()),
			Mode:     getenvOrDefault("VULOS_MODE", "local"),
			Hostname: hostname(),
		},
	}
}

// Enabled reports whether the cluster is connected to S3.
func (c *Cluster) Enabled() bool {
	return c.client != nil
}

// S3Client returns the underlying encrypted S3 client.
// Returns nil when the cluster is disabled (not configured).
// Callers should check Enabled() before use.
func (c *Cluster) S3Client() *Client {
	return c.client
}

// Register writes this node's metadata to S3 at nodes/{node_id}/meta.json,
// refreshing LastSeen to now.  It is safe to call concurrently.
// If the cluster is disabled it returns nil immediately.
func (c *Cluster) Register(ctx context.Context) error {
	if !c.Enabled() {
		return nil
	}

	c.mu.Lock()
	c.meta.LastSeen = time.Now().UTC()
	meta := c.meta // copy under lock
	c.mu.Unlock()

	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("cluster: marshal meta: %w", err)
	}

	key := nodeMetaPrefix + meta.ID + "/meta.json"
	if err := c.client.PutEncrypted(ctx, key, data); err != nil {
		return fmt.Errorf("cluster: register: %w", err)
	}
	return nil
}

// Peers reads all node metadata from S3 and returns them as a slice of Node.
// Nodes whose LastSeen is older than staleThreshold are flagged with Stale=true.
// The slice always includes this node itself.
// If the cluster is disabled it returns an empty slice and no error.
func (c *Cluster) Peers(ctx context.Context) ([]Node, error) {
	if !c.Enabled() {
		return nil, nil
	}

	// List all objects under nodes/.
	// The minio client's ListObjects is used via the underlying mc field;
	// we reconstruct keys by listing the prefix ourselves.
	keys, err := c.listNodeKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("cluster: list peers: %w", err)
	}

	now := time.Now().UTC()
	var nodes []Node

	for _, key := range keys {
		data, err := c.client.GetEncrypted(ctx, key)
		if err != nil {
			// Skip unreadable keys (e.g. wrong encryption, partial write).
			continue
		}

		var meta NodeMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}

		stale := now.Sub(meta.LastSeen) > staleThreshold
		nodes = append(nodes, Node{NodeMeta: meta, Stale: stale})
	}

	return nodes, nil
}

// Health returns a snapshot of cluster health suitable for use in HTTP health
// endpoints or tunnel health checks.  It never blocks on S3; it reads a cached
// peer list or returns a minimal map when disabled.
func (c *Cluster) Health() map[string]any {
	if !c.Enabled() {
		return map[string]any{
			"enabled": false,
			"node_id": c.meta.ID,
			"mode":    c.meta.Mode,
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	peers, err := c.Peers(ctx)
	healthy := err == nil

	total := len(peers)
	stale := 0
	for _, p := range peers {
		if p.Stale {
			stale++
		}
	}

	h := map[string]any{
		"enabled":      true,
		"node_id":      c.meta.ID,
		"mode":         c.meta.Mode,
		"hostname":     c.meta.Hostname,
		"storage":      c.meta.Storage,
		"peers_total":  total,
		"peers_stale":  stale,
		"peers_online": total - stale,
		"healthy":      healthy,
	}
	if err != nil {
		h["error"] = err.Error()
	}
	return h
}

// Start runs the heartbeat loop — calling Register on every heartbeatInterval.
// It blocks until ctx is cancelled.  The orchestrator should call this in a
// goroutine; it is intentionally NOT called automatically so wiring is explicit.
//
//	go cluster.Start(ctx)
//
// If the cluster is disabled, Start returns immediately.
func (c *Cluster) Start(ctx context.Context) {
	if !c.Enabled() {
		return
	}

	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	c.started = true
	c.mu.Unlock()

	// First registration immediately.
	if err := c.Register(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "cluster: initial register: %v\n", err)
	}

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.Register(ctx); err != nil {
				fmt.Fprintf(os.Stderr, "cluster: heartbeat register: %v\n", err)
			}
		}
	}
}

// listNodeKeys returns the S3 keys of all node meta.json objects by listing
// the nodes/ prefix.  It uses the underlying minio client directly.
func (c *Cluster) listNodeKeys(ctx context.Context) ([]string, error) {
	// minio-go's ListObjects takes a ListObjectsOptions struct.
	// Recursive=true so we traverse sub-directories (nodes/{id}/meta.json).
	opts := minio.ListObjectsOptions{
		Prefix:    nodeMetaPrefix,
		Recursive: true,
	}
	objectCh := c.client.mc.ListObjects(ctx, c.client.bucket, opts)

	var keys []string
	for obj := range objectCh {
		if obj.Err != nil {
			return nil, obj.Err
		}
		if strings.HasSuffix(obj.Key, "/meta.json") {
			keys = append(keys, obj.Key)
		}
	}
	return keys, nil
}

// hostname returns the OS hostname, falling back to "unknown".
func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}
