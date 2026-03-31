package tunnel

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
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
func (tc TURNConfig) GenerateCredentials(userID string) TURNCredentials {
	ttl := 24 * 3600 // 24 hours
	expiry := time.Now().Unix() + int64(ttl)
	username := fmt.Sprintf("%d:%s", expiry, userID)

	mac := hmac.New(sha1.New, []byte(tc.Secret))
	mac.Write([]byte(username))
	credential := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	return TURNCredentials{
		URLs: []string{
			fmt.Sprintf("turn:localhost:%d?transport=udp", tc.Port),
			fmt.Sprintf("turn:localhost:%d?transport=tcp", tc.Port),
		},
		Username:   username,
		Credential: credential,
		TTL:        ttl,
	}
}

// WriteCoturnConfig writes a turnserver.conf for Coturn.
func (tc TURNConfig) WriteCoturnConfig(dataDir string) (string, error) {
	cfgPath := filepath.Join(dataDir, "turnserver.conf")
	cfg := fmt.Sprintf(`# Vula OS — Coturn TURN server config
listening-port=%d
realm=%s
use-auth-secret
static-auth-secret=%s
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
