package auth

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// What these pin, and why the first one is the whole point:
//
// Multi-instance sync replicates whole tables. profiles.data is the row that
// travels to a user's other boxes, so anything inside it is on every machine
// they own. AIAPIKey and PinHash used to be inside it, which is why the entire
// profiles domain had to be refused from replication — and settings are exactly
// what a user expects to follow them between their own machines.
//
// So the load-bearing assertion is not "the struct has the right fields" but
// "the BYTES WRITTEN to the replicated table do not contain the secret". A
// `json:"-"` tag would satisfy a struct-shaped test and fail this one.

func newTestStoreAt(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(dir, "auth.db"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// rawProfileRow returns exactly what is stored in the REPLICATED table.
func rawProfileRow(t *testing.T, s *Store, userID string) string {
	t.Helper()
	var raw string
	if err := s.db.QueryRow(`SELECT data FROM profiles WHERE user_id=?`, userID).Scan(&raw); err != nil {
		t.Fatalf("read profiles row: %v", err)
	}
	return raw
}

func TestProfileRow_DoesNotCarryCredentials(t *testing.T) {
	s := newTestStoreAt(t, t.TempDir())

	s.SetProfile(&Profile{
		UserID:   "u1",
		Theme:    "dark",
		Locale:   "en-ZA",
		AIAPIKey: "sk-live-SECRETVALUE",
		PinHash:  "$2a$10$PINHASHVALUE",
	})

	raw := rawProfileRow(t, s, "u1")
	if strings.Contains(raw, "SECRETVALUE") {
		t.Errorf("the replicated profile row contains the AI API key — it would be copied to every box:\n%s", raw)
	}
	if strings.Contains(raw, "PINHASHVALUE") {
		t.Errorf("the replicated profile row contains the PIN hash — a PIN set on one box would travel to the others:\n%s", raw)
	}
	// The config half must still be there, or the split has thrown out the
	// thing it exists to let replicate.
	if !strings.Contains(raw, "dark") || !strings.Contains(raw, "en-ZA") {
		t.Errorf("the replicated profile row lost its settings:\n%s", raw)
	}
}

func TestProfile_CredentialsSurviveAReload(t *testing.T) {
	dir := t.TempDir()
	s := newTestStoreAt(t, dir)
	s.SetProfile(&Profile{UserID: "u1", Theme: "dark", AIAPIKey: "sk-abc", PinHash: "hash-xyz"})

	// A fresh store over the same database: the split is only correct if the
	// two halves come back together.
	s2 := newTestStoreAt(t, dir)
	got, ok := s2.GetProfile("u1")
	if !ok {
		t.Fatal("profile missing after reload")
	}
	if got.AIAPIKey != "sk-abc" {
		t.Errorf("AIAPIKey = %q after reload, want sk-abc — the secret half was not stitched back", got.AIAPIKey)
	}
	if got.PinHash != "hash-xyz" {
		t.Errorf("PinHash = %q after reload, want hash-xyz — losing this locks a user out of their own machine", got.PinHash)
	}
	if got.Theme != "dark" {
		t.Errorf("Theme = %q after reload, want dark", got.Theme)
	}
}

// SetProfile takes a pointer into the store's map. Zeroing the credential
// fields in place — rather than on a copy — would blank a live profile's PIN
// the moment it was saved.
func TestSetProfile_DoesNotBlankTheCallersStruct(t *testing.T) {
	s := newTestStoreAt(t, t.TempDir())
	p := &Profile{UserID: "u1", AIAPIKey: "sk-abc", PinHash: "hash-xyz"}
	s.SetProfile(p)

	if p.AIAPIKey != "sk-abc" || p.PinHash != "hash-xyz" {
		t.Fatalf("SetProfile cleared the caller's credentials: AIAPIKey=%q PinHash=%q", p.AIAPIKey, p.PinHash)
	}
	if got, _ := s.GetProfile("u1"); got.PinHash != "hash-xyz" {
		t.Fatalf("in-memory profile lost its PIN hash: %q", got.PinHash)
	}
}

// THE MIGRATION. Rows written before the split hold the credentials inside
// profiles.data. Dropping them on upgrade is data loss, and dropping PinHash
// specifically can lock someone out of their own machine.
func TestMigration_MovesCredentialsOutOfAnOldRow(t *testing.T) {
	dir := t.TempDir()
	s := newTestStoreAt(t, dir)

	// Write a row in the OLD shape directly, bypassing the splitting path.
	old := Profile{UserID: "u1", Theme: "light", AIAPIKey: "sk-old", PinHash: "hash-old"}
	blob, err := json.Marshal(&old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO profiles(user_id, data) VALUES(?, ?)`, "u1", string(blob)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), "sk-old") {
		t.Fatal("fixture is wrong: the old-shape row must contain the secret, or this test proves nothing")
	}

	// Reopening runs load + migration.
	s2 := newTestStoreAt(t, dir)

	got, ok := s2.GetProfile("u1")
	if !ok {
		t.Fatal("profile disappeared during migration")
	}
	if got.AIAPIKey != "sk-old" {
		t.Errorf("AIAPIKey = %q after migration, want sk-old — the upgrade lost a secret", got.AIAPIKey)
	}
	if got.PinHash != "hash-old" {
		t.Errorf("PinHash = %q after migration, want hash-old — the upgrade could lock this user out", got.PinHash)
	}
	if raw := rawProfileRow(t, s2, "u1"); strings.Contains(raw, "sk-old") || strings.Contains(raw, "hash-old") {
		t.Errorf("the migration left credentials in the replicated row:\n%s", raw)
	}
}

// Settings is free-form, so a future feature could put a token in it. The
// replicated half must not carry a key that names a secret.
func TestSettings_SecretLookingKeysStayLocal(t *testing.T) {
	dir := t.TempDir()
	s := newTestStoreAt(t, dir)
	s.SetProfile(&Profile{
		UserID: "u1",
		Settings: map[string]string{
			"sidebar_width":    "240",
			"weather_api_key":  "wk-SECRET1",
			"session_token":    "st-SECRET2",
			"preferred_editor": "vim",
		},
	})

	raw := rawProfileRow(t, s, "u1")
	for _, secret := range []string{"wk-SECRET1", "st-SECRET2"} {
		if strings.Contains(raw, secret) {
			t.Errorf("a secret-named Settings key replicated:\n%s", raw)
		}
	}
	// ...and the ordinary preferences must still replicate, or the rule is
	// withholding the very thing it exists to carry.
	for _, kept := range []string{"sidebar_width", "preferred_editor"} {
		if !strings.Contains(raw, kept) {
			t.Errorf("ordinary setting %q was withheld from the replicated row:\n%s", kept, raw)
		}
	}

	s2 := newTestStoreAt(t, dir)
	got, _ := s2.GetProfile("u1")
	if got.Settings["weather_api_key"] != "wk-SECRET1" {
		t.Errorf("local-only setting lost on reload: %q", got.Settings["weather_api_key"])
	}
	if got.Settings["sidebar_width"] != "240" {
		t.Errorf("replicated setting lost on reload: %q", got.Settings["sidebar_width"])
	}
}

func TestSettingsKeyIsSecret(t *testing.T) {
	for _, k := range []string{"api_key", "API_KEY", "weather_apikey", "secret_thing", "auth_token", "user_password", "db_passwd", "aws_credential", "pin", "bearer_tok.key"} {
		if !settingsKeyIsSecret(k) {
			t.Errorf("settingsKeyIsSecret(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"sidebar_width", "theme", "preferred_editor", "monkey", "keyboard", "hotkey", "keymap"} {
		if settingsKeyIsSecret(k) {
			t.Errorf("settingsKeyIsSecret(%q) = true, want false — ordinary preferences must replicate", k)
		}
	}
}

// The profile's secret half and the user's local-only Preferences SHARE one
// profile_secrets row. Whichever is written second must not drop the other.
//
// This is not hypothetical: persistProfileSecrets originally marshalled a fresh
// struct and replaced the row, so adding user_prefs silently wiped a PIN hash —
// and losing a per-device credential is quiet, so nobody would notice until
// someone could not unlock their machine.
func TestSecretsRow_TheTwoHalvesDoNotClobberEachOther(t *testing.T) {
	// BOTH ORDERS. Whichever half is written SECOND is the one that can drop
	// the other, so testing one order proves only half the property — the first
	// version of this test wrote the profile first, when the row was still
	// empty, and a mutation that made the profile write replace the whole row
	// SURVIVED it.
	for _, order := range []string{"profile-then-user", "user-then-profile"} {
		t.Run(order, func(t *testing.T) {
			dir := t.TempDir()
			s := newTestStoreAt(t, dir)

			writeProfile := func() {
				s.SetProfile(&Profile{UserID: "u1", Theme: "dark", AIAPIKey: "sk-abc", PinHash: "hash-xyz"})
			}
			writeUser := func() {
				setUserForTest(s, &User{
					ID: "u1", Username: "sam", PasswordHash: "$2a$10$hash",
					Preferences: map[string]string{"weather_api_key": "wk-SECRET", "density": "cosy"},
				})
			}

			if order == "profile-then-user" {
				writeProfile()
				writeUser()
			} else {
				writeUser()
				writeProfile()
			}

			s2 := newTestStoreAt(t, dir)

			p, ok := s2.GetProfile("u1")
			if !ok {
				t.Fatal("profile disappeared")
			}
			if p.PinHash != "hash-xyz" {
				t.Errorf("PinHash = %q — the shared row was clobbered, and losing this locks someone out of their machine", p.PinHash)
			}
			if p.AIAPIKey != "sk-abc" {
				t.Errorf("AIAPIKey = %q — the shared row was clobbered", p.AIAPIKey)
			}

			u, ok := s2.GetUser("u1")
			if !ok {
				t.Fatal("user disappeared")
			}
			if u.Preferences["weather_api_key"] != "wk-SECRET" {
				t.Errorf("local-only preference lost: %q", u.Preferences["weather_api_key"])
			}
		})
	}
}

// The password hash DOES replicate — deliberately. The hash is what makes an
// account usable on another of your own boxes; without it the account exists on
// box B and cannot be logged into, so people make a second account by hand and
// the two drift. See user_secrets.go for the full reasoning and the residual.
func TestUserRow_CarriesThePasswordHashButNotSecretPreferences(t *testing.T) {
	s := newTestStoreAt(t, t.TempDir())
	setUserForTest(s, &User{
		ID: "u1", Username: "sam", PasswordHash: "$2a$10$REPLICATED",
		Preferences: map[string]string{"api_token": "tok-SECRET", "density": "cosy"},
	})

	var raw string
	if err := s.db.QueryRow(`SELECT data FROM users WHERE id=?`, "u1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "REPLICATED") {
		t.Errorf("the replicated user row has no password hash — the account cannot be used on another box:\n%s", raw)
	}
	if strings.Contains(raw, "tok-SECRET") {
		t.Errorf("a secret-named preference replicated:\n%s", raw)
	}
	if !strings.Contains(raw, "density") {
		t.Errorf("an ordinary preference was withheld:\n%s", raw)
	}
}

// setUserForTest mirrors what auth.go does on every user write: put it in the
// map, then persist. Using the real persist path is the point — a test helper
// that wrote the row itself would not exercise the splitting.
func setUserForTest(s *Store, u *User) {
	s.mu.Lock()
	s.users[u.ID] = u
	s.mu.Unlock()
	s.persistUser(u)
}
