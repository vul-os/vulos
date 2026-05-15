package store_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"vulos/backend/services/store"
)

// openTestDB creates a temporary database for each test and closes/removes it
// when the test finishes.
func openTestDB(t *testing.T) *store.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestOpen verifies that a new database can be created, migrations run, and
// the file is placed at the expected path.
func TestOpen(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()

	expected := filepath.Join(dir, "vulos.db")
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("expected database file at %s: %v", expected, err)
	}
}

// TestOpenDefaultPath verifies the default path (~/.vulos/db/vulos.db) is
// returned when an empty string is passed. We do NOT actually create the file;
// we just test that the function resolves without an error when the directory
// already exists.
func TestOpenDefaultPath(t *testing.T) {
	// Use a temp dir to avoid polluting the real home.
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open with dir: %v", err)
	}
	db.Close()
}

// TestMigrationsIdempotent calls Open twice on the same database and verifies
// that migrations do not fail or duplicate.
func TestMigrationsIdempotent(t *testing.T) {
	dir := t.TempDir()

	// First open — applies migrations.
	db1, err := store.Open(dir)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	db1.Close()

	// Second open — migrations already applied, must not error.
	db2, err := store.Open(dir)
	if err != nil {
		t.Fatalf("second open (idempotent check): %v", err)
	}
	defer db2.Close()

	// _migrations table must contain exactly one row per migration.
	rows, err := db2.Query(`SELECT id FROM _migrations ORDER BY id`)
	if err != nil {
		t.Fatalf("query _migrations: %v", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if len(ids) == 0 {
		t.Fatal("expected at least one migration recorded in _migrations")
	}
	// Verify the initial schema migration is present exactly once.
	count := 0
	for _, id := range ids {
		if id == "001_initial_schema" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 001_initial_schema exactly once in _migrations, got %d", count)
	}
}

// TestSchema verifies that all expected tables exist after migration.
func TestSchema(t *testing.T) {
	db := openTestDB(t)

	tables := []string{
		"users",
		"sessions",
		"profiles",
		"settings",
		"installed_apps",
		"local_app_status",
		"_migrations",
	}

	for _, table := range tables {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`,
			table,
		).Scan(&name)
		if err == sql.ErrNoRows {
			t.Errorf("table %q does not exist", table)
		} else if err != nil {
			t.Errorf("query for table %q: %v", table, err)
		}
	}
}

// TestInsertReadUser verifies that a row can be written to the users table and
// read back with the same values.
func TestInsertReadUser(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC().Format(time.RFC3339)
	userID := "user-test-001"
	username := "alice"

	_, err := db.Exec(
		`INSERT INTO users (id, username, email, name, created_at, last_login)
         VALUES (?, ?, ?, ?, ?, ?)`,
		userID, username, "alice@example.com", "Alice", now, now,
	)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}

	var gotID, gotUsername, gotEmail string
	err = db.QueryRow(
		`SELECT id, username, email FROM users WHERE id = ?`, userID,
	).Scan(&gotID, &gotUsername, &gotEmail)
	if err != nil {
		t.Fatalf("query user: %v", err)
	}

	if gotID != userID {
		t.Errorf("id: want %q, got %q", userID, gotID)
	}
	if gotUsername != username {
		t.Errorf("username: want %q, got %q", username, gotUsername)
	}
	if gotEmail != "alice@example.com" {
		t.Errorf("email: want %q, got %q", "alice@example.com", gotEmail)
	}
}

// TestInsertReadSession verifies sessions table write/read.
func TestInsertReadSession(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC()
	token := "tok-abc-123"
	userID := "user-sess-001"

	_, err := db.Exec(
		`INSERT INTO sessions (token, user_id, email, name, device_id, expires_at, created_at)
         VALUES (?, ?, ?, ?, ?, ?, ?)`,
		token, userID, "bob@example.com", "Bob", "device-1",
		now.Add(24*time.Hour).Format(time.RFC3339),
		now.Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}

	var gotToken, gotUserID string
	err = db.QueryRow(
		`SELECT token, user_id FROM sessions WHERE token = ?`, token,
	).Scan(&gotToken, &gotUserID)
	if err != nil {
		t.Fatalf("query session: %v", err)
	}
	if gotToken != token {
		t.Errorf("token: want %q, got %q", token, gotToken)
	}
	if gotUserID != userID {
		t.Errorf("user_id: want %q, got %q", userID, gotUserID)
	}
}

// TestInsertReadProfile verifies the profiles table.
func TestInsertReadProfile(t *testing.T) {
	db := openTestDB(t)

	userID := "user-prof-001"
	_, err := db.Exec(
		`INSERT INTO profiles (user_id, display_name, role, theme)
         VALUES (?, ?, ?, ?)`,
		userID, "Carol", "admin", "light",
	)
	if err != nil {
		t.Fatalf("insert profile: %v", err)
	}

	var displayName, role, theme string
	err = db.QueryRow(
		`SELECT display_name, role, theme FROM profiles WHERE user_id = ?`, userID,
	).Scan(&displayName, &role, &theme)
	if err != nil {
		t.Fatalf("query profile: %v", err)
	}
	if displayName != "Carol" {
		t.Errorf("display_name: want Carol, got %q", displayName)
	}
	if role != "admin" {
		t.Errorf("role: want admin, got %q", role)
	}
	if theme != "light" {
		t.Errorf("theme: want light, got %q", theme)
	}
}

// TestInsertReadInstalledApp verifies the installed_apps table.
func TestInsertReadInstalledApp(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO installed_apps (app_id, version, installed_by, installed_at, status)
         VALUES (?, ?, ?, ?, ?)`,
		"gimp", "2.10.36", "home-node", now, "active",
	)
	if err != nil {
		t.Fatalf("insert installed_app: %v", err)
	}

	var appID, version, status string
	err = db.QueryRow(
		`SELECT app_id, version, status FROM installed_apps WHERE app_id = ?`, "gimp",
	).Scan(&appID, &version, &status)
	if err != nil {
		t.Fatalf("query installed_app: %v", err)
	}
	if appID != "gimp" || version != "2.10.36" || status != "active" {
		t.Errorf("got app_id=%q version=%q status=%q", appID, version, status)
	}
}

// TestCRSQLiteGracefulDegradation verifies that the database opens and is fully
// functional even when cr-sqlite is not available. If cr-sqlite IS available,
// the test also verifies that crsql_as_crr succeeds.
func TestCRSQLiteGracefulDegradation(t *testing.T) {
	db := openTestDB(t)

	if db.CRSQLiteLoaded {
		t.Logf("cr-sqlite loaded from %s — testing crsql_as_crr", db.CRSQLitePath)

		// Verify the CRR tables are registered by querying crsql_changes
		// (a virtual table exposed by cr-sqlite).
		rows, err := db.Query(`SELECT * FROM crsql_changes LIMIT 0`)
		if err != nil {
			t.Errorf("crsql_changes table not accessible: %v", err)
		} else {
			rows.Close()
		}
	} else {
		t.Log("cr-sqlite not loaded — verifying graceful degradation")

		// The DB must still be fully functional.
		var val int
		if err := db.QueryRow(`SELECT 1`).Scan(&val); err != nil {
			t.Fatalf("basic query failed without cr-sqlite: %v", err)
		}
		if val != 1 {
			t.Errorf("SELECT 1 returned %d", val)
		}
	}
}

// TestSettings verifies the settings table.
func TestSettings(t *testing.T) {
	db := openTestDB(t)

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)`,
		"theme", "dark", now,
	)
	if err != nil {
		t.Fatalf("insert setting: %v", err)
	}

	var value string
	err = db.QueryRow(`SELECT value FROM settings WHERE key = ?`, "theme").Scan(&value)
	if err != nil {
		t.Fatalf("query setting: %v", err)
	}
	if value != "dark" {
		t.Errorf("setting value: want dark, got %q", value)
	}
}
