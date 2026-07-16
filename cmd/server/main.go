// Command server runs the Vulos control plane, self-hosted, with the free
// no-op billing seam and bring-your-own-bucket storage. This is the binary a
// self-hoster runs on any host (VPS, box, laptop): it reads generic config from
// the environment, wires the free defaults from pkg/billingport and
// pkg/storageport, and starts the control plane via pkg/cpserver.
//
// A commercial distributor does NOT modify this binary — it ships its own thin
// main that builds the same generic cpserver.Config and injects real providers
// (a payment rail, a tier/quota resolver, a bucket provisioner) into
// cpserver.Deps. All operational logic lives in pkg/... regardless.
//
// Configuration (all optional; sensible self-host defaults):
//
//	CP_ADDR          listen address           (default ":8080")
//	VULOS_DOMAIN     deployment domain        (default "vulos.org")
//	DATABASE_URL     Postgres DSN             (empty => local SQLite)
//	CP_SQLITE_PATH   SQLite file path         (empty => in-memory; dev only)
//	CP_ENV           environment label        (default "dev")
//	CP_VERSION       build version            (default "dev")
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/vul-os/vulos-management/pkg/cproutes"
	"github.com/vul-os/vulos-management/pkg/cpserver"
)

func main() {
	cfg := cpserver.Config{
		Addr:        getenv("CP_ADDR", ":8080"),
		Domain:      getenv("VULOS_DOMAIN", ""),
		DatabaseDSN: strings.TrimSpace(os.Getenv("DATABASE_URL")),
		DBDir:       getenv("CP_DB_DIR", getenv("CP_SQLITE_PATH", "./cpdata")),
		Environment: getenv("CP_ENV", "dev"),
		Version:     getenv("CP_VERSION", "dev"),
	}

	// Self-host wiring: leave Deps' provider fields nil so cpserver installs the
	// free no-op billing rail, the unlimited self-host entitlement resolver, and
	// the bring-your-own-bucket storage provisioner. Nothing charges, nothing
	// phones home, nothing provisions managed buckets. cpserver.New mounts the
	// full operational route surface (cproutes.RegisterOperational) against these
	// seams; the only registrar we add here is the SPA fallback, which must be
	// LAST (least-specific) so every concrete API/health pattern wins over it.
	srv, err := cpserver.New(cfg, cpserver.Deps{
		Routes: []cpserver.RouteRegistrar{
			func(mux *http.ServeMux, _ *cpserver.Runtime) error {
				cproutes.RegisterSPAFallback(mux)
				return nil
			},
		},
	})
	if err != nil {
		log.Fatalf("control plane: %v", err)
	}
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := srv.Run(ctx); err != nil {
		log.Fatalf("control plane: %v", err)
	}
}

func getenv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
