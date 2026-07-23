package appsplatform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Dual-backend store tests.
//
// The SQLite standalone store ALWAYS runs. The Postgres store runs only when
// VULOS_TEST_POSTGRES is set: its value is either a Postgres DSN, or a truthy
// flag ("1"/"true"/"yes") in which case the DSN is read from DATABASE_URL /
// VULOS_DATABASE_URL. CI without a database simply skips the Postgres subtests.

// testPostgresDSN returns the Postgres DSN to test against, or "" to skip.
func testPostgresDSN() string {
	v := strings.TrimSpace(os.Getenv("VULOS_TEST_POSTGRES"))
	switch strings.ToLower(v) {
	case "":
		return ""
	case "1", "true", "yes":
		return DatabaseURLFromEnv()
	default:
		return v
	}
}

type backend struct {
	name   string
	newReg func(t *testing.T) Registry
}

// storeBackends returns the registry backends to exercise. SQLite is always
// present; Postgres is appended only when VULOS_TEST_POSTGRES selects it.
func storeBackends(t *testing.T) []backend {
	t.Helper()
	backends := []backend{{
		name: "sqlite",
		newReg: func(t *testing.T) Registry {
			dsn := filepath.Join(t.TempDir(), "apps.db")
			r, err := NewStandaloneRegistry(dsn, WithSigningSecretEncryptor(testEncryptor(t)))
			if err != nil {
				t.Fatalf("sqlite open: %v", err)
			}
			t.Cleanup(func() { _ = r.Close() })
			return r
		},
	}}
	if dsn := testPostgresDSN(); dsn != "" {
		backends = append(backends, backend{
			name: "postgres",
			newReg: func(t *testing.T) Registry {
				r, err := NewPostgresRegistry(dsn, WithSigningSecretEncryptor(testEncryptor(t)))
				if err != nil {
					t.Fatalf("postgres open: %v", err)
				}
				// Isolate each test from leftover rows in the shared schema.
				if _, err := r.db.Exec(`TRUNCATE apps.apps`); err != nil {
					t.Fatalf("postgres truncate: %v", err)
				}
				t.Cleanup(func() { _ = r.Close() })
				return r
			},
		})
	}
	return backends
}

// TestStoreCRUDDualBackend runs the core registry contract against every
// configured backend.
func TestStoreCRUDDualBackend(t *testing.T) {
	for _, b := range storeBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			r := b.newReg(t)

			// Create.
			c, err := r.Create(CreateParams{
				Name:            "deployer",
				OwnerID:         "alice",
				Products:        []string{ProductOffice},
				Scopes:          []string{ScopeAppsWrite},
				SlashCommands:   []SlashCommand{{Name: "deploy"}},
				IncomingEnabled: true,
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if c.Token == "" || c.SigningSecret == "" {
				t.Fatal("missing one-time secrets")
			}

			// Get.
			got, err := r.Get(c.App.ID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.Name != "deployer" || !got.TargetsProduct(ProductOffice) || !got.HasScope(ScopeAppsWrite) {
				t.Fatalf("fields not persisted: %+v", got)
			}
			if !got.Incoming.Enabled {
				t.Fatal("incoming webhook should be enabled")
			}

			// Token-hash lookup (hash-only storage: plaintext never matches).
			byHash, err := r.GetByTokenHash(HashToken(c.Token))
			if err != nil || byHash.ID != c.App.ID {
				t.Fatalf("token lookup failed: %v", err)
			}
			if _, err := r.GetByTokenHash(HashToken("vat_nope")); err != ErrNotFound {
				t.Fatalf("unknown token should be ErrNotFound, got %v", err)
			}

			// Incoming-webhook lookup.
			byHook, err := r.GetByIncomingWebhookID(c.App.Incoming.ID)
			if err != nil || byHook.ID != c.App.ID {
				t.Fatalf("webhook lookup failed: %v", err)
			}

			// List owner-scoping.
			_, _ = r.Create(CreateParams{Name: "bobapp", OwnerID: "bob", Products: []string{ProductOffice}})
			mine, _ := r.List("alice", false)
			if len(mine) != 1 || mine[0].OwnerID != "alice" {
				t.Fatalf("owner scoping wrong: %+v", mine)
			}
			all, _ := r.List("alice", true)
			if len(all) != 2 {
				t.Fatalf("admin should see all, got %d", len(all))
			}

			// Update.
			prods := []string{ProductBoard, ProductFiles}
			upd, err := r.Update(c.App.ID, UpdateParams{Products: &prods})
			if err != nil {
				t.Fatalf("update: %v", err)
			}
			if upd.TargetsProduct(ProductOffice) || !upd.TargetsProduct(ProductBoard) {
				t.Fatalf("products not updated: %v", upd.Products)
			}

			// Slash command resolution (now targets board).
			if _, _, ok := r.ResolveSlashCommand(ProductBoard, "/deploy"); !ok {
				t.Fatal("should resolve deploy for board after update")
			}
			if _, _, ok := r.ResolveSlashCommand(ProductOffice, "deploy"); ok {
				t.Fatal("must not resolve for a product the app no longer targets")
			}
			if cmds := r.AllSlashCommands(ProductBoard); len(cmds) != 1 || cmds[0].Name != "deploy" {
				t.Fatalf("catalog wrong: %+v", cmds)
			}

			// Rotate token invalidates the old one.
			newTok, err := r.RotateToken(c.App.ID)
			if err != nil {
				t.Fatalf("rotate token: %v", err)
			}
			if _, err := r.GetByTokenHash(HashToken(c.Token)); err != ErrNotFound {
				t.Fatal("old token still valid after rotate")
			}
			if _, err := r.GetByTokenHash(HashToken(newTok)); err != nil {
				t.Fatal("new token invalid after rotate")
			}

			// Rotate secret.
			newSecret, err := r.RotateSecret(c.App.ID)
			if err != nil {
				t.Fatalf("rotate secret: %v", err)
			}
			after, _ := r.Get(c.App.ID)
			if after.SigningSecret != newSecret {
				t.Fatalf("secret not rotated: in-memory %q != %q", after.SigningSecret, newSecret)
			}

			// Delete (idempotency contract).
			if err := r.Delete(c.App.ID); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if err := r.Delete(c.App.ID); err != ErrNotFound {
				t.Fatalf("double delete should be ErrNotFound, got %v", err)
			}
			if _, err := r.Get(c.App.ID); err != ErrNotFound {
				t.Fatalf("get after delete should be ErrNotFound, got %v", err)
			}
		})
	}
}

// TestStoreSecurityDualBackend verifies the preserved security fixes on every
// backend: tokens are hash-only, signing secrets are encrypted at rest, and
// token TTL timing fields round-trip.
func TestStoreSecurityDualBackend(t *testing.T) {
	for _, b := range storeBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			r := b.newReg(t)
			c, err := r.Create(CreateParams{
				Name:     "secure",
				OwnerID:  "alice",
				Products: []string{ProductOffice},
				TokenTTL: 5 * time.Minute,
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}

			// vat_ tokens: only the hash is stored, plaintext is never recoverable.
			got, err := r.Get(c.App.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.TokenHash == "" || got.TokenHash == c.Token {
				t.Fatal("token must be stored as a hash, never plaintext")
			}
			if got.TokenHash != HashToken(c.Token) {
				t.Fatal("stored hash does not match HashToken(plaintext)")
			}

			// vas_ signing secret: in-memory plaintext round-trips and signs.
			if got.SigningSecret != c.SigningSecret {
				t.Fatalf("in-memory signing secret should be plaintext, got %q", got.SigningSecret)
			}
			ts, body := NowTimestamp(), []byte(`{"event":"x"}`)
			if !Verify(ts, body, got.SigningSecret, Sign(ts, body, got.SigningSecret)) {
				t.Fatal("signature round-trip failed with decrypted secret")
			}

			// Token TTL timing fields persisted.
			if got.TokenTTL != 5*time.Minute {
				t.Fatalf("TokenTTL not persisted: %v", got.TokenTTL)
			}
			if got.TokenIssuedAt.IsZero() || got.TokenExpiresAt.IsZero() {
				t.Fatal("token timing fields not persisted")
			}
		})
	}
}

// TestPostgresEncryptsSecretAtRest confirms that the Postgres backend stores the
// signing secret as ciphertext (enc: prefix), not cleartext. Postgres-only.
func TestPostgresEncryptsSecretAtRest(t *testing.T) {
	dsn := testPostgresDSN()
	if dsn == "" {
		t.Skip("VULOS_TEST_POSTGRES not set; skipping Postgres-only test")
	}
	enc := testEncryptor(t)
	r, err := NewPostgresRegistry(dsn, WithSigningSecretEncryptor(enc))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	if _, err := r.db.Exec(`TRUNCATE apps.apps`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	c, err := r.Create(CreateParams{Name: "enc", OwnerID: "a", Products: []string{ProductOffice}})
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := r.db.QueryRow(`SELECT signing_secret FROM apps.apps WHERE id = $1`, c.App.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == c.SigningSecret {
		t.Fatal("signing secret stored in plaintext — should be encrypted")
	}
	if !strings.HasPrefix(stored, encPrefix) {
		t.Fatalf("stored value should carry %q prefix, got %q", encPrefix, stored)
	}
}

// TestPostgresRefusesCleartextSecret confirms the fail-loud guard: persisting a
// non-empty signing secret with no encryptor configured is refused. Postgres-only.
func TestPostgresRefusesCleartextSecret(t *testing.T) {
	dsn := testPostgresDSN()
	if dsn == "" {
		t.Skip("VULOS_TEST_POSTGRES not set; skipping Postgres-only test")
	}
	// No encryptor and no VULOS_APPS_KEK → Create must refuse to persist.
	t.Setenv(AppsKEKEnv, "")
	r, err := NewPostgresRegistry(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	if _, err := r.db.Exec(`TRUNCATE apps.apps`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	_, err = r.Create(CreateParams{Name: "nokek", OwnerID: "a", Products: []string{ProductOffice}})
	if err == nil {
		t.Fatal("expected create to fail loudly without an encryptor")
	}
	if !strings.Contains(err.Error(), "cleartext") {
		t.Fatalf("error should mention cleartext refusal, got: %v", err)
	}
}

// TestOpenRegistrySelectsBackend verifies the env-driven seam selection. Without
// DATABASE_URL it returns the SQLite standalone store.
func TestOpenRegistrySelectsBackend(t *testing.T) {
	t.Setenv(DatabaseURLEnv, "")
	t.Setenv(DatabaseURLEnvAlt, "")
	dsn := filepath.Join(t.TempDir(), "apps.db")
	r, err := OpenRegistry(dsn, WithSigningSecretEncryptor(testEncryptor(t)))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		if c, ok := r.(*StandaloneRegistry); ok {
			_ = c.Close()
		}
	}()
	if _, ok := r.(*StandaloneRegistry); !ok {
		t.Fatalf("expected StandaloneRegistry without DATABASE_URL, got %T", r)
	}
}
