package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Provider is a supported tunnel backend.
type Provider string

const (
	ProviderCloudflared Provider = "cloudflared"
	ProviderFRP         Provider = "frp"
)

// Route maps a subdomain to a local port.
type Route struct {
	Subdomain string `json:"subdomain"` // e.g., "calculator"
	LocalPort int    `json:"local_port"`
	Protocol  string `json:"protocol"` // "http" or "tcp"
}

// Status is the tunnel's current state.
type Status struct {
	Provider   Provider          `json:"provider"`
	Running    bool              `json:"running"`
	Domain     string            `json:"domain"`
	Routes     []Route           `json:"routes"`
	PublicURLs map[string]string `json:"public_urls"` // subdomain → full URL
	Error      string            `json:"error,omitempty"`
	StartedAt  time.Time         `json:"started_at,omitempty"`
}

// Config holds tunnel configuration.
type Config struct {
	Provider    Provider `json:"provider"`
	Domain      string   `json:"domain"`       // e.g., "mydevice.vulos.io"
	// Cloudflared
	CFToken     string   `json:"cf_token"`      // Cloudflare tunnel token
	CFTunnelID  string   `json:"cf_tunnel_id"`
	// FRP
	FRPServer   string   `json:"frp_server"`    // e.g., "relay.vulos.io:7000"
	FRPToken    string   `json:"frp_token"`
}

// LoadConfig reads tunnel config from env vars.
func LoadConfig() Config {
	return Config{
		Provider:   Provider(getenv("TUNNEL_PROVIDER", "")),
		Domain:     os.Getenv("TUNNEL_DOMAIN"),
		CFToken:    os.Getenv("CLOUDFLARED_TOKEN"),
		CFTunnelID: os.Getenv("CLOUDFLARED_TUNNEL_ID"),
		FRPServer:  os.Getenv("FRP_SERVER"),
		FRPToken:   os.Getenv("FRP_TOKEN"),
	}
}

// Service manages the tunnel lifecycle.
type Service struct {
	mu          sync.Mutex
	cfg         Config
	routes      []Route
	cmd         *exec.Cmd
	status      Status
	dataDir     string
	routesDirty bool
	ctx         context.Context
}

func New(cfg Config, dataDir string) *Service {
	os.MkdirAll(dataDir, 0755)
	return &Service{
		cfg:     cfg,
		dataDir: dataDir,
		status:  Status{Provider: cfg.Provider, Domain: cfg.Domain},
	}
}

// Configured returns true if a tunnel provider is set up.
func (s *Service) Configured() bool {
	switch s.cfg.Provider {
	case ProviderCloudflared:
		return s.cfg.CFToken != ""
	case ProviderFRP:
		return s.cfg.FRPServer != "" && s.cfg.FRPToken != ""
	}
	return false
}

// SetRoutes updates the route table. Call before Start or to hot-reload.
func (s *Service) SetRoutes(routes []Route) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes = routes
	s.rebuildPublicURLs()
}

// AddRoute adds a single route. Triggers auto-reload if tunnel is running.
func (s *Service) AddRoute(subdomain string, localPort int) {
	s.mu.Lock()
	for _, r := range s.routes {
		if r.Subdomain == subdomain {
			s.mu.Unlock()
			return
		}
	}
	s.routes = append(s.routes, Route{Subdomain: subdomain, LocalPort: localPort, Protocol: "http"})
	s.rebuildPublicURLs()
	running := s.status.Running
	s.mu.Unlock()

	if running {
		go s.Reload(s.ctx)
	}
}

// RemoveRoute removes a route. Triggers auto-reload if tunnel is running.
func (s *Service) RemoveRoute(subdomain string) {
	s.mu.Lock()
	filtered := s.routes[:0]
	for _, r := range s.routes {
		if r.Subdomain != subdomain {
			filtered = append(filtered, r)
		}
	}
	s.routes = filtered
	s.rebuildPublicURLs()
	running := s.status.Running
	s.mu.Unlock()

	if running {
		go s.Reload(s.ctx)
	}
}

func (s *Service) rebuildPublicURLs() {
	urls := make(map[string]string)
	for _, r := range s.routes {
		if s.cfg.Domain != "" {
			urls[r.Subdomain] = fmt.Sprintf("https://%s.%s", r.Subdomain, s.cfg.Domain)
		}
	}
	s.status.Routes = s.routes
	s.status.PublicURLs = urls
}

// Start launches the tunnel process.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.Configured() {
		return fmt.Errorf("tunnel not configured (set TUNNEL_PROVIDER + credentials)")
	}

	switch s.cfg.Provider {
	case ProviderCloudflared:
		return s.startCloudflared(ctx)
	case ProviderFRP:
		return s.startFRP(ctx)
	default:
		return fmt.Errorf("unknown provider: %s", s.cfg.Provider)
	}
}

// Stop kills the tunnel process.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
		s.cmd = nil
	}
	s.status.Running = false
	log.Printf("[tunnel] stopped")
}

// Status returns current tunnel state.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// --- Cloudflared ---

func (s *Service) startCloudflared(ctx context.Context) error {
	if _, err := exec.LookPath("cloudflared"); err != nil {
		return fmt.Errorf("cloudflared not installed (install with your package manager)")
	}

	// Write ingress config
	cfgPath := filepath.Join(s.dataDir, "cloudflared.yml")
	if err := s.writeCloudflaredConfig(cfgPath); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	args := []string{"tunnel", "--config", cfgPath, "run"}
	if s.cfg.CFTunnelID != "" {
		args = append(args, s.cfg.CFTunnelID)
	}

	cmd := exec.CommandContext(ctx, "cloudflared", args...)
	cmd.Env = append(os.Environ(), "TUNNEL_TOKEN="+s.cfg.CFToken)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start cloudflared: %w", err)
	}

	s.cmd = cmd
	s.status.Running = true
	s.status.StartedAt = time.Now()

	go func() {
		cmd.Wait()
		s.mu.Lock()
		s.status.Running = false
		ctx := s.ctx
		s.mu.Unlock()
		log.Printf("[tunnel/cloudflared] process exited — restarting in 5s")
		time.Sleep(5 * time.Second)
		if ctx != nil && ctx.Err() == nil {
			s.Start(ctx)
		}
	}()

	log.Printf("[tunnel/cloudflared] started with %d routes", len(s.routes))
	return nil
}

type cfConfig struct {
	Tunnel  string      `yaml:"tunnel" json:"tunnel"`
	Ingress []cfIngress `yaml:"ingress" json:"ingress"`
}

type cfIngress struct {
	Hostname string `yaml:"hostname,omitempty" json:"hostname,omitempty"`
	Service  string `yaml:"service" json:"service"`
}

func (s *Service) writeCloudflaredConfig(path string) error {
	cfg := cfConfig{Tunnel: s.cfg.CFTunnelID}

	// App routes: subdomain.domain → localhost:port
	for _, r := range s.routes {
		cfg.Ingress = append(cfg.Ingress, cfIngress{
			Hostname: fmt.Sprintf("%s.%s", r.Subdomain, s.cfg.Domain),
			Service:  fmt.Sprintf("http://localhost:%d", r.LocalPort),
		})
	}

	// Shell itself on the bare domain
	cfg.Ingress = append(cfg.Ingress, cfIngress{
		Hostname: s.cfg.Domain,
		Service:  "http://localhost:8080",
	})

	// Catch-all (required by cloudflared)
	cfg.Ingress = append(cfg.Ingress, cfIngress{
		Service: "http_status:404",
	})

	// Write as JSON (cloudflared accepts both YAML and JSON)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// --- FRP ---

func (s *Service) startFRP(ctx context.Context) error {
	if _, err := exec.LookPath("frpc"); err != nil {
		return fmt.Errorf("frpc not installed (install with your package manager)")
	}

	cfgPath := filepath.Join(s.dataDir, "frpc.toml")
	if err := s.writeFRPConfig(cfgPath); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	cmd := exec.CommandContext(ctx, "frpc", "-c", cfgPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start frpc: %w", err)
	}

	s.cmd = cmd
	s.status.Running = true
	s.status.StartedAt = time.Now()

	go func() {
		cmd.Wait()
		s.mu.Lock()
		s.status.Running = false
		ctx := s.ctx
		s.mu.Unlock()
		log.Printf("[tunnel/frp] process exited — restarting in 5s")
		time.Sleep(5 * time.Second)
		if ctx != nil && ctx.Err() == nil {
			s.Start(ctx)
		}
	}()

	log.Printf("[tunnel/frp] started with %d routes", len(s.routes))
	return nil
}

func (s *Service) writeFRPConfig(path string) error {
	parts := strings.SplitN(s.cfg.FRPServer, ":", 2)
	host := parts[0]
	port := "7000"
	if len(parts) > 1 {
		port = parts[1]
	}

	var b strings.Builder

	// Global section
	fmt.Fprintf(&b, "serverAddr = %q\n", host)
	fmt.Fprintf(&b, "serverPort = %s\n", port)
	fmt.Fprintf(&b, "auth.token = %q\n\n", s.cfg.FRPToken)

	// Shell on bare domain
	fmt.Fprintf(&b, "[[proxies]]\n")
	fmt.Fprintf(&b, "name = \"shell\"\n")
	fmt.Fprintf(&b, "type = \"http\"\n")
	fmt.Fprintf(&b, "localPort = 8080\n")
	fmt.Fprintf(&b, "customDomains = [%q]\n\n", s.cfg.Domain)

	// App routes
	for _, r := range s.routes {
		fmt.Fprintf(&b, "[[proxies]]\n")
		fmt.Fprintf(&b, "name = %q\n", r.Subdomain)
		fmt.Fprintf(&b, "type = \"http\"\n")
		fmt.Fprintf(&b, "localPort = %d\n", r.LocalPort)
		fmt.Fprintf(&b, "customDomains = [%q]\n\n", fmt.Sprintf("%s.%s", r.Subdomain, s.cfg.Domain))
	}

	return os.WriteFile(path, []byte(b.String()), 0644)
}

// --- Reload ---

// Reload regenerates config and restarts the tunnel with updated routes.
func (s *Service) Reload(ctx context.Context) error {
	s.Stop()
	return s.Start(ctx)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
