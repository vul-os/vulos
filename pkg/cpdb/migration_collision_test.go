// migration_collision_test.go — WAVE34-6 tree-wide guard: no subsystem may ship
// BOTH a bare NNNN_desc.sql and a dialect variant (NNNN_desc.sqlite.sql /
// .postgres.sql) of the same stem. A bare file applies on BOTH backends, so a
// colliding dialect file would be double-applied on the matching backend.
//
// This walks the actual source tree (internal/*/migrations) rather than the
// embedded FSes so it needs no import of every subsystem (avoiding a heavy /
// cyclic dependency), and it catches the hazard at the source of truth.
package cpdb_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoBareDialectMigrationCollisions(t *testing.T) {
	// This test file lives in .../backend/cp/internal/cpdb; the migration dirs are
	// under .../backend/cp/internal/<sub>/migrations.
	internalRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("abs internal root: %v", err)
	}

	subs, err := os.ReadDir(internalRoot)
	if err != nil {
		t.Fatalf("read internal dir: %v", err)
	}

	checked := 0
	for _, sub := range subs {
		if !sub.IsDir() {
			continue
		}
		migDir := filepath.Join(internalRoot, sub.Name(), "migrations")
		entries, err := os.ReadDir(migDir)
		if err != nil {
			continue // no migrations dir for this subsystem
		}
		checked++

		bare := map[string]bool{}
		dialect := map[string]string{}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".sql") {
				continue
			}
			switch {
			case strings.HasSuffix(name, ".sqlite.sql"):
				dialect[strings.TrimSuffix(name, ".sqlite.sql")] = name
			case strings.HasSuffix(name, ".postgres.sql"):
				dialect[strings.TrimSuffix(name, ".postgres.sql")] = name
			default:
				bare[strings.TrimSuffix(name, ".sql")] = true
			}
		}
		for stem := range bare {
			if d, ok := dialect[stem]; ok {
				t.Errorf("subsystem %q migration collision: bare %q coexists with dialect %q (double-apply hazard) — delete the bare stub, keep only the dialect-split pair",
					sub.Name(), stem+".sql", d)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no migration directories were checked — path resolution likely wrong")
	}
}
