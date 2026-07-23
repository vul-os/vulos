// Package trydemo implements the "Try Vulos in your browser" shared-demo
// backend: queue management, per-IP throttling, Fly machine lifecycle, and the
// monthly $ cost ceiling (TRY_DEMO_MONTHLY_COGS_CAP_USD).
//
// Environment variables:
//
//	TRY_DEMO_FLY_APP          — the Fly app slug hosting the demo machine.
//	TRY_DEMO_FLY_MACHINE_ID   — the Fly machine id (the unit of control).
//	TRY_DEMO_FLY_REGION       — informational region label.
package trydemo

import (
	"os"
	"strconv"
)

// Config holds all tunable parameters for the demo subsystem.
// All fields have sane defaults; ConfigFromEnv reads from TRY_DEMO_* env vars.
type Config struct {
	MaxViewers              int     // 1 driver + N-1 spectators; default 8
	DriverSessionSecs       int     // hard cap on driver session; default 600
	DriverWarnSecs          int     // warning before forced handoff; default 120
	DriverIdleSecs          int     // idle-kill if no input from driver; default 60
	PerIPConcurrent         int     // max active sessions per IP; default 1
	PerIPDriverCooldownSecs int     // min seconds between driver claims from same IP; default 1800
	IdleStopSecs            int     // scale-to-zero: stop machine after N secs of no viewers; default 300
	MonthlyCostCapUSD       float64 // hard $ ceiling; above this, no new driver promotions; default 100
	Region                  string  // informational only; default "iad"

	// Compute app/machine identifiers (populated from env in
	// routes_trydemo.go, stored here for logging/error messages).
	FlyApp       string // Fly app slug
	FlyMachineID string // Fly machine id
}

// ConfigFromEnv reads the TRY_DEMO_* environment variables and returns a
// Config with sane defaults for any variable that is absent or unparseable.
func ConfigFromEnv() Config {
	cfg := Config{
		MaxViewers:              8,
		DriverSessionSecs:       600,
		DriverWarnSecs:          120,
		DriverIdleSecs:          60,
		PerIPConcurrent:         1,
		PerIPDriverCooldownSecs: 1800,
		IdleStopSecs:            300,
		MonthlyCostCapUSD:       100,
		Region:                  "iad",
	}

	if v := envInt("TRY_DEMO_MAX_VIEWERS"); v > 0 {
		cfg.MaxViewers = v
	}
	if v := envInt("TRY_DEMO_DRIVER_SESSION_SECS"); v > 0 {
		cfg.DriverSessionSecs = v
	}
	if v := envInt("TRY_DEMO_DRIVER_WARN_SECS"); v > 0 {
		cfg.DriverWarnSecs = v
	}
	if v := envInt("TRY_DEMO_DRIVER_IDLE_SECS"); v > 0 {
		cfg.DriverIdleSecs = v
	}
	if v := envInt("TRY_DEMO_PER_IP_CONCURRENT"); v > 0 {
		cfg.PerIPConcurrent = v
	}
	if v := envInt("TRY_DEMO_PER_IP_DRIVER_COOLDOWN_SECS"); v > 0 {
		cfg.PerIPDriverCooldownSecs = v
	}
	if v := envInt("TRY_DEMO_IDLE_STOP_SECS"); v > 0 {
		cfg.IdleStopSecs = v
	}
	if v := envFloat("TRY_DEMO_MONTHLY_COGS_CAP_USD"); v > 0 {
		cfg.MonthlyCostCapUSD = v
	}
	if v := os.Getenv("TRY_DEMO_FLY_REGION"); v != "" {
		cfg.Region = v
	}
	// Fly app slug.
	cfg.FlyApp = os.Getenv("TRY_DEMO_FLY_APP")
	if cfg.FlyApp == "" {
		cfg.FlyApp = "vulos-try"
	}
	// Fly machine id.
	cfg.FlyMachineID = os.Getenv("TRY_DEMO_FLY_MACHINE_ID")
	return cfg
}

func envInt(key string) int {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

func envFloat(key string) float64 {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0
	}
	return f
}
