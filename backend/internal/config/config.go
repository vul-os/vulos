package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	_, srcFile, _, _ = runtime.Caller(0)
	repoRoot         = filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(srcFile))))
)

type Config struct {
	Port        string
	AppURL      string // public URL of the OS app
	LandingPort string // separate port for landing page (empty = disabled)
	LandingURL  string // public URL of the landing page
}

func Load(env string) *Config {
	envFiles := map[string]string{
		"main":  filepath.Join(repoRoot, ".env.main"),
		"dev":   filepath.Join(repoRoot, ".env.dev"),
		"local": filepath.Join(repoRoot, ".env"),
	}

	vars := map[string]string{}

	envFile := envFiles[env]
	if env == "local" {
		localFile := filepath.Join(repoRoot, ".env.local")
		if _, err := os.Stat(localFile); err == nil {
			envFile = localFile
		}
	}

	if data, err := os.ReadFile(envFile); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
				continue
			}
			key, val, _ := strings.Cut(line, "=")
			vars[strings.TrimSpace(key)] = strings.TrimSpace(val)
		}
	}

	get := func(key, fallback string) string {
		if v, ok := vars[key]; ok {
			return v
		}
		if v := os.Getenv(key); v != "" {
			return v
		}
		return fallback
	}

	return &Config{
		Port:        get("PORT", "8080"),
		AppURL:      get("APP_URL", "http://localhost:8080"),
		LandingPort: get("LANDING_PORT", ""),
		LandingURL:  get("LANDING_URL", "http://localhost:3000"),
	}
}
