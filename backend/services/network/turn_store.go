package network

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// net10TURNRecord is the on-disk representation. Secret is stored but never returned via API.
type net10TURNRecord struct {
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Realm        string `json:"realm"`
	SharedSecret string `json:"shared_secret,omitempty"` // write-only: stored on disk, never sent to client
	Configured   bool   `json:"configured"`
}

// TURNStoreConfig is the public (API-safe) view returned by GET /api/turn/config.
type TURNStoreConfig struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Realm      string `json:"realm"`
	Configured bool   `json:"configured"`
}

// TURNStore persists user-provided TURN server settings to disk.
// The shared secret is write-only: it is persisted but never returned via the API.
type TURNStore struct {
	mu   sync.RWMutex
	path string
	rec  net10TURNRecord
}

// NewTURNStore loads (or initialises) the TURN config from dbDir/turn.json.
func NewTURNStore(dbDir string) (*TURNStore, error) {
	path := filepath.Join(dbDir, "turn.json")
	s := &TURNStore{path: path}
	if raw, err := os.ReadFile(path); err == nil {
		if err2 := json.Unmarshal(raw, &s.rec); err2 != nil {
			return nil, err2
		}
	}
	return s, nil
}

// Get returns the public view of the stored config (secret omitted).
func (s *TURNStore) Get() TURNStoreConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return TURNStoreConfig{
		Host:       s.rec.Host,
		Port:       s.rec.Port,
		Realm:      s.rec.Realm,
		Configured: s.rec.Configured,
	}
}

// Set persists the full config, including the shared secret.
// Pass an empty secret to leave the existing secret unchanged.
func (s *TURNStore) Set(host string, port int, realm, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rec.Host = host
	s.rec.Port = port
	s.rec.Realm = realm
	if secret != "" {
		s.rec.SharedSecret = secret
	}
	s.rec.Configured = host != ""
	return s.flush()
}

// Secret returns the stored shared secret (used internally for credential generation).
func (s *TURNStore) Secret() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rec.SharedSecret
}

func (s *TURNStore) flush() error {
	data, err := json.MarshalIndent(s.rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// TURNTestResult is returned by the test endpoint.
type TURNTestResult struct {
	Success bool          `json:"success"`
	Latency time.Duration `json:"latency_ms"`
	Error   string        `json:"error,omitempty"`
}

// TestReachability performs a quick TCP dial to host:port and reports latency.
func (s *TURNStore) TestReachability() TURNTestResult {
	s.mu.RLock()
	host := s.rec.Host
	port := s.rec.Port
	s.mu.RUnlock()

	if host == "" {
		return TURNTestResult{Success: false, Error: "no TURN host configured"}
	}
	if port == 0 {
		port = 3478
	}

	addr := net.JoinHostPort(host, itoa(port))
	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	latency := time.Since(start)

	if err != nil {
		return TURNTestResult{
			Success: false,
			Latency: latency / time.Millisecond,
			Error:   err.Error(),
		}
	}
	conn.Close()
	return TURNTestResult{
		Success: true,
		Latency: latency / time.Millisecond,
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := make([]byte, 0, 10)
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
