// storehealth.go — shared store-open helpers used by the wire_* route
// registrars in this package. These are the composition-root primitives the
// operational stores open through: a readiness pinger type, the cpdb open
// helper, and the fail-closed in-memory fallback guard.
//
// (Ported from cmd/server/wire_stores.go, which stays cloud-side for the
// commercial store set; only the shared primitives the moved routes reference
// live here.)
package cproutes

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// StoreHealth pairs a human-readable name with a function that pings the
// underlying database via SELECT 1. /readyz calls each one. Exported (with
// exported fields) so a commercial composition root can collect the health
// pingers the operational Wire* funcs return and mount them on its own /readyz.
type StoreHealth struct {
	Name string
	Ping func(ctx context.Context) error
}

// openCPDB opens a store database through the cpdb seam. It sets VULOS_DB_DIR
// (once per process) so the SQLite fallback lands in dbDir, then opens the
// named schema/db. Postgres is selected automatically when DATABASE_URL /
// VULOS_DATABASE_URL is set; otherwise SQLite at <dbDir>/<name>.db.
func openCPDB(dbDir, name string) (*cpdb.DB, error) {
	if err := setDBDirIfUnset(dbDir); err != nil {
		return nil, err
	}
	return cpdb.Open(name)
}

// warnMemStoreFallback emits a standardised WARNING (data-loss-on-restart) log
// line whenever a store falls back to an in-memory implementation.
//
// FAIL-CLOSED: an ephemeral MemStore silently serving production traffic is data
// loss waiting to happen. The fallback is refused BY DEFAULT — a store-open
// failure is fatal — and is permitted ONLY when the operator has explicitly set
// VULOS_ALLOW_MEMSTORE_FALLBACK=1 (or =true/yes).
func warnMemStoreFallback(pkg string, err error) {
	msg := fmt.Sprintf("[%s] WARNING (data-loss-on-restart): SQL store unavailable (%v); using MemStore", pkg, err)
	if !memStoreFallbackAllowed() {
		log.Fatalf("%s — refusing to serve ephemeral in-memory storage; set VULOS_ALLOW_MEMSTORE_FALLBACK=1 to explicitly permit it (data will be lost on restart)", msg)
	}
	log.Print(msg)
}

// memStoreFallbackAllowed reports whether the operator has explicitly opted in to
// the data-losing in-memory store fallback. Default (unset) is false → fail closed.
func memStoreFallbackAllowed() bool {
	switch os.Getenv("VULOS_ALLOW_MEMSTORE_FALLBACK") {
	case "1", "true", "TRUE", "yes":
		return true
	default:
		return false
	}
}
