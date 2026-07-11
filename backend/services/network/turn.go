package network

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// TURNConfig holds Coturn TURN server configuration.
type TURNConfig struct {
	Port    int    // default 3478
	Secret  string // shared secret for credential generation
	Realm   string // e.g., "vulos"
	Enabled bool
	// Host is the hostname/IP clients should dial to reach this TURN server.
	// Defaults to "localhost", which only works when the signaling client and
	// the TURN server are the same machine (local dev). Set TURN_HOST to the
	// box's real public hostname/IP for a usable self-hosted TURN deployment —
	// this is part of the sovereign-federation config profile (a fully
	// sovereign box needs no third-party STUN/TURN).
	Host string
}

// LoadTURNConfig reads TURN config from environment.
func LoadTURNConfig() TURNConfig {
	port := 3478
	if p := os.Getenv("TURN_PORT"); p != "" {
		fmt.Sscanf(p, "%d", &port)
	}
	return TURNConfig{
		Port:    port,
		Secret:  os.Getenv("TURN_SECRET"),
		Realm:   getenv("TURN_REALM", "vulos"),
		Enabled: os.Getenv("TURN_SECRET") != "",
		Host:    getenv("TURN_HOST", "localhost"),
	}
}

// TURNCredentials generates short-lived TURN credentials using HMAC.
// These are injected into web pages to force WebRTC through the TURN server.
type TURNCredentials struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username"`
	Credential string   `json:"credential"`
	TTL        int      `json:"ttl"`
}

// GenerateCredentials creates time-limited TURN credentials.
// Credential = base64(HMAC-SHA256(secret, username)) where username = "<expiry>:<userID>".
// Both ends (this server + the TURN server) must use the same algorithm.
// Coturn supports use-auth-secret with SHA-256 via the --sha256 flag; ensure the
// turnserver.conf includes "sha256" when consuming these credentials.
func (tc TURNConfig) GenerateCredentials(userID string) TURNCredentials {
	ttl := 24 * 3600 // 24 hours
	expiry := time.Now().Unix() + int64(ttl)
	username := fmt.Sprintf("%d:%s", expiry, userID)

	mac := hmac.New(sha256.New, []byte(tc.Secret))
	mac.Write([]byte(username))
	credential := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	host := tc.Host
	if host == "" {
		host = "localhost"
	}
	return TURNCredentials{
		URLs: []string{
			fmt.Sprintf("turn:%s:%d?transport=udp", host, tc.Port),
			fmt.Sprintf("turn:%s:%d?transport=tcp", host, tc.Port),
		},
		Username:   username,
		Credential: credential,
		TTL:        ttl,
	}
}

// WriteCoturnConfig writes a turnserver.conf for Coturn.
func (tc TURNConfig) WriteCoturnConfig(dataDir string) (string, error) {
	cfgPath := filepath.Join(dataDir, "turnserver.conf")
	cfg := fmt.Sprintf(`# Vulos — Coturn TURN server config
listening-port=%d
realm=%s
use-auth-secret
static-auth-secret=%s
# sha256 selects HMAC-SHA256 for time-limited credentials (matches GenerateCredentials).
sha256
no-cli
no-tls
fingerprint
lt-cred-mech
log-file=/var/log/turnserver.log
simple-log
`, tc.Port, tc.Realm, tc.Secret)

	if err := os.WriteFile(cfgPath, []byte(cfg), 0600); err != nil {
		return "", err
	}
	return cfgPath, nil
}

// StartCoturn launches the Coturn process.
func (tc TURNConfig) StartCoturn(ctx context.Context, dataDir string) (*exec.Cmd, error) {
	if !tc.Enabled {
		return nil, fmt.Errorf("TURN not configured (set TURN_SECRET)")
	}

	if _, err := exec.LookPath("turnserver"); err != nil {
		return nil, fmt.Errorf("turnserver not installed (install coturn with your package manager)")
	}

	cfgPath, err := tc.WriteCoturnConfig(dataDir)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "turnserver", "-c", cfgPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start coturn: %w", err)
	}

	log.Printf("[turn] coturn started on port %d", tc.Port)
	return cmd, nil
}
