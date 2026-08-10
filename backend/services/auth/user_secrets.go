package auth

import (
	"encoding/json"
	"log"
)

// user_secrets.go — what of a user account replicates between a person's own
// boxes, and what does not.
//
// # The password hash SYNCS, deliberately
//
// This is the consequential call in this file, so the reasoning is here rather
// than in a commit message.
//
// The hash IS the account. Under close-to-identical instances, a user expects
// to log into any of their own boxes with the credentials they set once. If
// PasswordHash does not replicate, the account technically exists on box B and
// cannot be used there — so the person creates a second account by hand, picks
// a different password, and the two drift. That is a worse outcome than
// replication, not a safer one: it multiplies weak passwords instead of copies
// of one strong hash.
//
// bcrypt at DefaultCost is designed to be STORED. Its threat model is an
// attacker who has the hash and must crack it offline, and the cost factor is
// the defence — a defence that does not weaken with the number of copies. An
// attacker who compromises a box to read the users table already owns that box,
// including its sessions and key material.
//
// The residual, stated plainly: more copies means more machines from which a
// hash can be stolen, and in a fleet with one internet-facing box that box
// becomes the weakest link for every account. That is real. It is accepted
// because the alternative — accounts that do not work across a person's own
// machines — defeats the purpose of the fleet, and because the mitigation is
// the one that always applied: a strong password and bcrypt's cost factor.
//
// # What does NOT sync
//
// Preferences is a free-form map[string]string, exactly like Profile.Settings,
// so a future feature could put a token in it without anyone editing this file.
// Secret-named keys are held back by the same rule (settingsKeyIsSecret), which
// matches on tokens rather than substrings — a bare Contains(k, "key") also
// matches "monkey" and "keyboard".

// splitUser returns the replicable half and the per-device half of u.
//
// The returned user is a COPY: callers hold pointers into the store's map, and
// zeroing in place would blank a live account's preferences.
func splitUser(u *User) (replicable User, localPrefs map[string]string) {
	replicable = *u
	prefs, local := splitSettings(u.Preferences)
	replicable.Preferences = prefs
	return replicable, local
}

// persistUserSecrets stores the per-device half of a user's Preferences.
//
// Reuses the profile_secrets table rather than adding a second one: both hold
// "the part of a per-user record that must not leave this box", the key is the
// same user id, and one excluded table is easier to keep excluded than two.
// The two halves live under distinct JSON keys so neither can overwrite the
// other.
func (s *Store) persistUserSecrets(userID string, localPrefs map[string]string) {
	if s.db == nil {
		return
	}
	// Read-modify-write: a user's local prefs and a profile's local settings
	// share the row, so writing one must not drop the other.
	var raw string
	_ = s.db.QueryRow(`SELECT data FROM profile_secrets WHERE user_id=?`, userID).Scan(&raw)

	rec := map[string]any{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &rec)
	}
	if len(localPrefs) == 0 {
		delete(rec, "user_prefs")
	} else {
		rec["user_prefs"] = localPrefs
	}
	if len(rec) == 0 {
		if _, err := s.db.Exec(`DELETE FROM profile_secrets WHERE user_id=?`, userID); err != nil {
			log.Printf("[auth] sqlite: clear user secrets %s: %v", userID, err)
		}
		return
	}
	data, err := json.Marshal(rec)
	if err != nil {
		log.Printf("[auth] sqlite: marshal user secrets %s: %v", userID, err)
		return
	}
	if _, err := s.db.Exec(
		`INSERT INTO profile_secrets(user_id, data) VALUES(?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET data=excluded.data`,
		userID, string(data),
	); err != nil {
		log.Printf("[auth] sqlite: persist user secrets %s: %v", userID, err)
	}
}

// loadUserSecrets stitches local-only Preferences back onto the in-memory users.
func (s *Store) loadUserSecrets() error {
	if s.db == nil {
		return nil
	}
	rows, err := s.db.Query(`SELECT user_id, data FROM profile_secrets`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var userID, raw string
		if err := rows.Scan(&userID, &raw); err != nil {
			return err
		}
		var rec struct {
			UserPrefs map[string]string `json:"user_prefs,omitempty"`
		}
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			continue
		}
		u, ok := s.users[userID]
		if !ok || len(rec.UserPrefs) == 0 {
			continue
		}
		if u.Preferences == nil {
			u.Preferences = map[string]string{}
		}
		for k, v := range rec.UserPrefs {
			u.Preferences[k] = v
		}
	}
	return rows.Err()
}
