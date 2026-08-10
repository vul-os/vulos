package auth

// profile_secrets.go — keep per-device credentials out of the replicated
// profile blob.
//
// # Why this file exists
//
// Profile is one struct and stays one struct: every caller keeps using
// GetProfile/SetProfile and sees a whole profile. What changed is the STORAGE.
// The credential fields are written to a separate table (profile_secrets) and
// stitched back on load, so the row that replicates between a user's boxes
// carries their settings and not their secrets.
//
// The multi-instance sync engine replicates whole tables. While AIAPIKey and
// PinHash shared one JSON document with Theme and Locale, no column-level
// exclusion could reach inside it — so the whole profiles domain was refused
// from replication, and settings are the thing users most expect to follow them
// between their own machines. This is the split that unblocks that.
//
// A `json:"-"` tag would NOT have been enough: it hides a field from the
// encoder, but the goal here is that the bytes written to the replicated table
// never contain the secret at all. That is a storage question, not a tagging one.
//
// # The two fields, and why each is treated the way it is
//
// PinHash — the device-unlock PIN, stored as a hash. It is EXCLUDED from
// replication rather than shared, because a PIN is a property of a physical
// machine someone is standing at, not of an account. A user who sets a PIN on
// the box in their study has not thereby chosen to unlock the box in their
// office. Making that fleet-wide silently would be a surprise in the dangerous
// direction, and the reverse — re-entering a PIN per box — is a one-time cost
// the user can see and understand.
//
// AIAPIKey — a bearer secret for an external provider. Replicating it copies a
// credential that can be spent (billed, rate-limited, revoked) to every box, so
// one compromised machine leaks a key that works everywhere. It is excluded for
// the same reason no other bearer token in this system replicates.
//
// Settings (map[string]string) STAYS in the replicated half, deliberately, but
// see settingsKeyIsSecret below: it is free-form, so a future feature could put
// a secret in it. That is guarded rather than assumed.

import (
	"encoding/json"
	"log"
	"strings"
)

// profileSecrets is the per-device half of a Profile — everything that must not
// ride along when the profile row replicates to another box.
type profileSecrets struct {
	AIAPIKey string `json:"ai_api_key,omitempty"`
	PinHash  string `json:"pin_hash,omitempty"`
}

// splitProfile returns the replicable half and the per-device half of p.
//
// The returned config profile is a COPY with the credential fields zeroed — the
// caller's struct is never mutated, because callers hold pointers into the
// store's map and zeroing in place would blank a live profile's PIN.
func splitProfile(p *Profile) (config Profile, secrets profileSecrets) {
	config = *p
	secrets = profileSecrets{
		AIAPIKey: p.AIAPIKey,
		PinHash:  p.PinHash,
	}
	config.AIAPIKey = ""
	config.PinHash = ""
	return config, secrets
}

// settingsKeyIsSecret reports whether a free-form Settings key looks like it
// holds a credential.
//
// Settings is map[string]string and anything can be written into it. It is in
// the replicated half because that is what it is for — per-user preferences —
// but "we replicate whatever anyone puts here" is a standing invitation for a
// future feature to leak a token by accident, without anyone editing this file
// or noticing the consequence.
//
// So: keys that name a secret are held back, and the check is on the KEY rather
// than a guess at the value's shape, because a heuristic over values would both
// miss short tokens and withhold ordinary settings that happen to look random.
//
// Matching is on TOKENS, not substrings. A bare `strings.Contains(k, "key")`
// also matches "monkey", "keyboard" and "hotkey" — the first version of this
// did exactly that and TestSettingsKeyIsSecret caught it. Splitting on the
// separators settings keys actually use lets "api_key" and "apikey" match while
// "monkey" does not.
//
// The error weighting is deliberate and asymmetric: a false negative leaks a
// credential to every box the user owns, while a false positive merely means one
// preference does not follow them. So the strong markers below also match as
// substrings — "secret", "password", "passwd" and "credential" essentially never
// appear innocently in a settings key, and catching "mysecretpref" is worth more
// than replicating it.
func settingsKeyIsSecret(key string) bool {
	k := strings.ToLower(key)

	for _, strong := range []string{"secret", "password", "passwd", "credential"} {
		if strings.Contains(k, strong) {
			return true
		}
	}

	secretToken := map[string]bool{
		"key": true, "apikey": true, "privatekey": true, "seckey": true,
		"token": true, "auth": true, "bearer": true, "pass": true, "pin": true,
	}
	for _, tok := range strings.FieldsFunc(k, func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || r == ' ' || r == '/' || r == ':'
	}) {
		if secretToken[tok] {
			return true
		}
	}
	return false
}

// splitSettings divides a Settings map into the part that replicates and the
// part that stays on this box. Returns nils rather than empty maps when a half
// has nothing in it, so an untouched profile does not gain an empty object in
// its persisted JSON.
func splitSettings(in map[string]string) (replicable, local map[string]string) {
	for k, v := range in {
		if settingsKeyIsSecret(k) {
			if local == nil {
				local = map[string]string{}
			}
			local[k] = v
			continue
		}
		if replicable == nil {
			replicable = map[string]string{}
		}
		replicable[k] = v
	}
	return replicable, local
}

// persistProfileSecrets writes the per-device half. Best-effort in the same
// sense persistProfile is — it logs rather than failing the caller — but note
// the asymmetry that matters: a failure here means a PIN or API key was not
// SAVED, whereas a failure in persistProfile means a preference was not saved.
// The log line says which.
func (s *Store) persistProfileSecrets(userID string, sec profileSecrets, localSettings map[string]string) {
	if s.db == nil {
		return
	}
	rec := struct {
		profileSecrets
		Settings map[string]string `json:"settings,omitempty"`
	}{profileSecrets: sec, Settings: localSettings}

	// Nothing to keep: drop the row rather than storing an empty document, so
	// "has secrets" is answerable by the row's existence.
	if rec.AIAPIKey == "" && rec.PinHash == "" && len(rec.Settings) == 0 {
		if _, err := s.db.Exec(`DELETE FROM profile_secrets WHERE user_id=?`, userID); err != nil {
			log.Printf("[auth] sqlite: clear profile secrets %s: %v", userID, err)
		}
		return
	}

	data, err := json.Marshal(rec)
	if err != nil {
		log.Printf("[auth] sqlite: marshal profile secrets %s (PIN/API key NOT saved): %v", userID, err)
		return
	}
	if _, err := s.db.Exec(
		`INSERT INTO profile_secrets(user_id, data) VALUES(?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET data=excluded.data`,
		userID, string(data),
	); err != nil {
		log.Printf("[auth] sqlite: persist profile secrets %s (PIN/API key NOT saved): %v", userID, err)
	}
}

// loadProfileSecrets stitches the per-device half back onto the in-memory
// profiles after the replicated half has been loaded.
//
// MIGRATION. Profiles written before this split carry ai_api_key/pin_hash
// INSIDE profiles.data. Those rows are still read correctly, because Profile
// unmarshals the whole document either way — so the values survive the upgrade
// and are then re-written into the new shape by migrateProfileSecrets. Losing a
// PinHash here would lock a user out of their own machine, so the old location
// is read first and the row is only rewritten once the secret is safely stored.
func (s *Store) loadProfileSecrets() error {
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
			profileSecrets
			Settings map[string]string `json:"settings,omitempty"`
		}
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			log.Printf("[auth] sqlite: profile secrets %s are corrupt and were skipped: %v", userID, err)
			continue
		}
		p, ok := s.profiles[userID]
		if !ok {
			continue // secrets for a profile that no longer exists
		}
		p.AIAPIKey = rec.AIAPIKey
		p.PinHash = rec.PinHash
		for k, v := range rec.Settings {
			if p.Settings == nil {
				p.Settings = map[string]string{}
			}
			p.Settings[k] = v
		}
	}
	return rows.Err()
}

// migrateProfileSecrets moves credentials out of any profile row still holding
// them in the old single-blob shape, and rewrites that row without them.
//
// Runs after load, so the in-memory profiles already carry the values wherever
// they came from. Writing the secret half FIRST and the stripped config half
// second means an interruption between the two leaves the secret stored twice
// rather than not at all — the safe direction, since the load path tolerates a
// duplicate and cannot recover a deletion.
func (s *Store) migrateProfileSecrets() {
	if s.db == nil {
		return
	}
	rows, err := s.db.Query(`SELECT user_id, data FROM profiles`)
	if err != nil {
		log.Printf("[auth] sqlite: profile-secret migration scan: %v", err)
		return
	}
	type pending struct {
		userID string
		sec    profileSecrets
		local  map[string]string
	}
	var todo []pending
	for rows.Next() {
		var userID, raw string
		if err := rows.Scan(&userID, &raw); err != nil {
			rows.Close()
			log.Printf("[auth] sqlite: profile-secret migration read: %v", err)
			return
		}
		var old Profile
		if err := json.Unmarshal([]byte(raw), &old); err != nil {
			continue
		}
		_, localSettings := splitSettings(old.Settings)
		if old.AIAPIKey == "" && old.PinHash == "" && len(localSettings) == 0 {
			continue // already in the new shape, or never had a secret
		}
		todo = append(todo, pending{
			userID: userID,
			sec:    profileSecrets{AIAPIKey: old.AIAPIKey, PinHash: old.PinHash},
			local:  localSettings,
		})
	}
	rows.Close()

	for _, t := range todo {
		s.persistProfileSecrets(t.userID, t.sec, t.local)
		if p, ok := s.profiles[t.userID]; ok {
			s.persistProfile(p) // rewrites profiles.data through the splitting path
		}
		log.Printf("[auth] migrated profile %s: credentials moved out of the replicated profile row", t.userID)
	}
}
