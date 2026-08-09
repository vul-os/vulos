package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// config_precedence_test.go — the process environment must beat the .env file.
//
// repoRoot is derived from runtime.Caller(0): the path of config.go ON THE
// MACHINE THAT COMPILED THE BINARY. The tracked .env living there is therefore
// read by that binary wherever it later runs, and it used to OUTRANK the real
// environment — so `PORT=9000 vulos-server` silently kept binding 8080 because
// a file the operator had never seen said PORT=8080, with no diagnostic.
//
// This is not hypothetical: it is exactly what blocked the real-server e2e
// harness, which could not move the server off port 8080 no matter what it
// exported. Pinned here so the precedence cannot quietly flip back.

func TestEnvironmentBeatsDotEnvFile(t *testing.T) {
	if _, err := os.Stat(filepath.Join(repoRoot, ".env")); err != nil {
		t.Skipf("no .env at %s — there is no file here to be overridden by", repoRoot)
	}

	t.Setenv("PORT", "39917")

	if cfg := Load("local"); cfg.Port != "39917" {
		t.Errorf("Port = %q, want 39917 — the .env file overrode an explicit environment variable", cfg.Port)
	}
}

// The file must still supply keys the environment does NOT set, or a
// developer's checkout stops working.
func TestDotEnvFileStillSuppliesUnsetKeys(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repoRoot, ".env"))
	if err != nil {
		t.Skipf("no .env to read: %v", err)
	}
	var pinned string
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && strings.TrimSpace(k) == "PORT" {
			pinned = strings.TrimSpace(v)
		}
	}
	if pinned == "" {
		t.Skip(".env does not set PORT; nothing to assert here")
	}

	// An empty value must count as "unset" for this purpose — get() treats
	// os.Getenv=="" as absent, and the file has to fill the gap.
	t.Setenv("PORT", "")

	if cfg := Load("local"); cfg.Port != pinned {
		t.Errorf("Port = %q with PORT unset, want %q from .env — the file is no longer consulted at all", cfg.Port, pinned)
	}
}
