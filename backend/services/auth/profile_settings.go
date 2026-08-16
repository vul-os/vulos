package auth

// profile_settings.go — the merge rule for Profile.Settings, the free-form
// per-user preference bag.
//
// # Why this exists
//
// Profile.Settings has been in the struct, in the persisted row and inside the
// CRDT-replicated `profiles.data` blob since the split in 0002 — and until now
// it had no way in. handleUpdateProfile decoded eight named fields and not this
// one, so the only value it ever held was the empty map DefaultProfile creates.
// A replicated store nothing can write is the same defect shape as the
// app_registry table in roadmap/SYNC-INVENTORY.md §1: the engine converges it
// perfectly and it is always empty.
//
// The shell needs somewhere for user-owned preferences that follow the person
// between their own boxes (theme mode, accent, night shift). That store already
// exists; what was missing is this function and one decoded field.
//
// # Merge, not replace
//
// A PUT that carried the whole map would make every writer responsible for
// every other writer's keys: the Appearance panel saving an accent would drop a
// key the widget rail wrote a moment earlier, because it round-tripped a
// snapshot taken before that write. So a patch names only the keys it means,
// and an EMPTY VALUE DELETES — there is no other way to remove a key through a
// merge, and "" is not a meaningful value for any preference.
//
// # Secret-named keys are REFUSED, not accepted
//
// persistProfile routes any key settingsKeyIsSecret() matches into
// profile_secrets, which is deliberately never replicated. Accepting such a key
// here would therefore succeed, return 200, and quietly produce state that does
// not sync — the exact "the setting that syncs is not the setting that governs"
// failure this wire path was added to end. A 400 that says so is the honest
// answer; a caller with a genuine secret has profile_secrets' own endpoints.
//
// Note the asymmetry with the EXISTING half of the map: keys already stored are
// copied through untouched even when they are secret-named, because load
// stitches the local half back onto the in-memory profile and dropping them
// here would delete a user's stored secret as a side effect of setting a theme.

import "fmt"

const (
	// MaxSettingKeys bounds the whole map. Every key lands in the one
	// `profiles.data` blob that replicates as a single CRDT register, so the
	// bound is on the replicated payload rather than on any one write.
	MaxSettingKeys = 64
	// MaxSettingKeyLen bounds one key.
	MaxSettingKeyLen = 64
	// MaxSettingValueLen bounds one value. Preferences are enums, hex colours
	// and HH:MM strings; anything approaching this is a blob that wants a
	// different home (see the wallpaper note in roadmap/SYNC-INVENTORY.md §5).
	MaxSettingValueLen = 512
)

// mergeSettings applies patch onto existing and returns the result. It never
// mutates either argument.
//
// Rules, in order:
//   - a key with a non-empty value is set
//   - a key with an EMPTY value is deleted
//   - an oversized, empty or secret-named key in the PATCH is an error, and the
//     whole patch is refused rather than partially applied
//   - keys already in `existing` pass through unexamined
func mergeSettings(existing, patch map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(existing)+len(patch))
	for k, v := range existing {
		out[k] = v
	}
	for k, v := range patch {
		if k == "" {
			return nil, fmt.Errorf("settings: empty key")
		}
		if len(k) > MaxSettingKeyLen {
			return nil, fmt.Errorf("settings: key %q is longer than %d bytes", k, MaxSettingKeyLen)
		}
		if settingsKeyIsSecret(k) {
			return nil, fmt.Errorf("settings: key %q looks like a secret; secret-named settings are stored per-device and would not replicate", k)
		}
		if v == "" {
			delete(out, k)
			continue
		}
		if len(v) > MaxSettingValueLen {
			return nil, fmt.Errorf("settings: value for %q is longer than %d bytes", k, MaxSettingValueLen)
		}
		out[k] = v
	}
	if len(out) > MaxSettingKeys {
		return nil, fmt.Errorf("settings: %d keys exceeds the limit of %d", len(out), MaxSettingKeys)
	}
	return out, nil
}
