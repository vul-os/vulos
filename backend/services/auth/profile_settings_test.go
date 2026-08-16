package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// What these pin.
//
// Profile.Settings is the per-user preference bag that rides inside the
// CRDT-replicated `profiles.data` blob. Before this change it had storage and
// replication and no way in — the same "converges perfectly, always empty"
// shape roadmap/SYNC-INVENTORY.md §1 records for app_registry. These tests are
// about the wire path that fixes that, and about the two ways it could be
// wrong: silently not replicating, and echoing a secret back.

// settingsHandler returns a handler plus a seeded profile id.
func settingsHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	store := &Store{
		users:    make(map[string]*User),
		sessions: make(map[string]*Session),
		profiles: make(map[string]*Profile),
		secret:   []byte("test-secret-prefs"),
	}
	store.profiles["u-prefs"] = DefaultProfile("u-prefs", "Prefs User")
	return NewHandler(store), "u-prefs"
}

// putProfile issues an authenticated PUT /api/profiles/{userId} with body and
// returns the recorder. It calls the handler directly (the Middleware is what
// sets X-User-ID in production; here it is set explicitly, which is the same
// contract every other handler test uses).
func putProfile(t *testing.T, h *Handler, userID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/profiles/"+userID, strings.NewReader(body))
	req.SetPathValue("userId", userID)
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.handleUpdateProfile(w, req)
	return w
}

func decodeProfile(t *testing.T, w *httptest.ResponseRecorder) Profile {
	t.Helper()
	var p Profile
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	return p
}

// The headline: a settings key written over the wire reaches the profile the
// CRDT bridge replicates. If PUT does not decode `settings`, this fails.
func TestUpdateProfile_WritesSettings(t *testing.T) {
	h, uid := settingsHandler(t)

	w := putProfile(t, h, uid, `{"settings":{"shell.accent":"#7c3aed"}}`)
	if w.Code != 200 {
		t.Fatalf("PUT settings: want 200, got %d — %s", w.Code, w.Body.String())
	}

	stored, ok := h.store.GetProfile(uid)
	if !ok {
		t.Fatal("profile vanished")
	}
	if stored.Settings["shell.accent"] != "#7c3aed" {
		t.Fatalf("settings did not reach the stored profile: %#v", stored.Settings)
	}
	if got := decodeProfile(t, w).Settings["shell.accent"]; got != "#7c3aed" {
		t.Fatalf("response did not echo the new setting: %q", got)
	}
}

// A second writer must not erase the first writer's keys. This is why the field
// is a patch and not a replacement: two independent panels write this map, and
// a snapshot round-trip would lose whichever write it did not observe.
func TestUpdateProfile_SettingsMergeRatherThanReplace(t *testing.T) {
	h, uid := settingsHandler(t)

	putProfile(t, h, uid, `{"settings":{"shell.theme":"dark"}}`)
	putProfile(t, h, uid, `{"settings":{"shell.accent":"#7c3aed"}}`)

	stored, _ := h.store.GetProfile(uid)
	if stored.Settings["shell.theme"] != "dark" {
		t.Fatalf("the second write dropped the first writer's key: %#v", stored.Settings)
	}
	if stored.Settings["shell.accent"] != "#7c3aed" {
		t.Fatalf("the second write did not land: %#v", stored.Settings)
	}
}

// An empty value is the delete verb. Without one there is no way to remove a
// key through a merge, and a "reset to default" would have to store a sentinel.
func TestUpdateProfile_EmptyValueDeletesTheKey(t *testing.T) {
	h, uid := settingsHandler(t)

	putProfile(t, h, uid, `{"settings":{"shell.accent":"#7c3aed"}}`)
	putProfile(t, h, uid, `{"settings":{"shell.accent":""}}`)

	stored, _ := h.store.GetProfile(uid)
	if _, present := stored.Settings["shell.accent"]; present {
		t.Fatalf("an empty value did not delete the key: %#v", stored.Settings)
	}
}

// The sharp one. persistProfile routes a secret-named key into profile_secrets,
// which is never replicated — so accepting one here would return 200 and
// produce a preference that does not sync. That is precisely the defect this
// wire path exists to end, so it must be a refusal and not a quiet success.
func TestUpdateProfile_RefusesSecretNamedSettingKeys(t *testing.T) {
	h, uid := settingsHandler(t)

	for _, key := range []string{"api_key", "shell.token", "my-password", "openai.secret", "device.pin"} {
		w := putProfile(t, h, uid, `{"settings":{"`+key+`":"v"}}`)
		if w.Code != 400 {
			t.Errorf("settings key %q: want 400, got %d — a secret-named key that is accepted here does not replicate", key, w.Code)
		}
		stored, _ := h.store.GetProfile(uid)
		if _, present := stored.Settings[key]; present {
			t.Errorf("settings key %q was refused with %d but stored anyway", key, w.Code)
		}
	}
}

// A refusal must not be a partial apply: the good key in the same patch must
// not land either, or a caller cannot tell what state it left behind.
func TestUpdateProfile_RefusedPatchAppliesNothing(t *testing.T) {
	h, uid := settingsHandler(t)

	w := putProfile(t, h, uid, `{"settings":{"shell.theme":"light","api_key":"sk-x"}}`)
	if w.Code != 400 {
		t.Fatalf("want 400, got %d", w.Code)
	}
	stored, _ := h.store.GetProfile(uid)
	if _, present := stored.Settings["shell.theme"]; present {
		t.Fatalf("the valid half of a refused patch was applied: %#v", stored.Settings)
	}
}

// Bounds. The map lives in one replicated blob, so an unbounded one is a way to
// make every box carry an arbitrary payload.
func TestUpdateProfile_SettingsBounds(t *testing.T) {
	h, uid := settingsHandler(t)

	long := strings.Repeat("v", MaxSettingValueLen+1)
	if w := putProfile(t, h, uid, `{"settings":{"shell.x":"`+long+`"}}`); w.Code != 400 {
		t.Errorf("oversized value: want 400, got %d", w.Code)
	}
	longKey := "shell." + strings.Repeat("k", MaxSettingKeyLen)
	if w := putProfile(t, h, uid, `{"settings":{"`+longKey+`":"v"}}`); w.Code != 400 {
		t.Errorf("oversized key: want 400, got %d", w.Code)
	}

	// Fill to the cap one key at a time, then prove the next one is refused.
	p, _ := h.store.GetProfile(uid)
	p.Settings = map[string]string{}
	for i := 0; i < MaxSettingKeys; i++ {
		p.Settings["shell.k"+string(rune('a'+i%26))+string(rune('a'+i/26))] = "v"
	}
	if len(p.Settings) != MaxSettingKeys {
		t.Fatalf("seed produced %d keys, wanted %d", len(p.Settings), MaxSettingKeys)
	}
	if w := putProfile(t, h, uid, `{"settings":{"shell.overflow":"v"}}`); w.Code != 400 {
		t.Errorf("exceeding the key cap: want 400, got %d", w.Code)
	}
}

// Omitting the field must leave the map alone — otherwise every existing caller
// of this endpoint (the Appearance panel sends `theme` only) would wipe it.
func TestUpdateProfile_OmittedSettingsLeavesMapIntact(t *testing.T) {
	h, uid := settingsHandler(t)

	putProfile(t, h, uid, `{"settings":{"shell.theme":"dark"}}`)
	putProfile(t, h, uid, `{"display_name":"Renamed"}`)

	stored, _ := h.store.GetProfile(uid)
	if stored.Settings["shell.theme"] != "dark" {
		t.Fatalf("a PUT that did not mention settings wiped them: %#v", stored.Settings)
	}
	if stored.DisplayName != "Renamed" {
		t.Fatalf("the named field did not apply: %q", stored.DisplayName)
	}
}

// The read side. loadProfileSecrets stitches the per-device half of the map
// back onto the in-memory profile, so a secret-named settings key IS present on
// the struct the handlers serialize. It must not be echoed.
func TestSanitizeProfile_MasksSecretNamedSettings(t *testing.T) {
	p := &Profile{
		UserID: "u1",
		Settings: map[string]string{
			"shell.theme":  "dark",
			"openai.token": "sk-live-SECRETVALUE",
		},
	}
	got := sanitizeProfile(p)

	if got.Settings["openai.token"] == "sk-live-SECRETVALUE" {
		t.Error("sanitizeProfile echoed a secret-named settings value")
	}
	if got.Settings["openai.token"] == "" {
		t.Error("the key should be masked, not dropped — the UI needs to know one is configured")
	}
	if got.Settings["shell.theme"] != "dark" {
		t.Errorf("a non-secret setting was masked: %q", got.Settings["shell.theme"])
	}
	// And the live profile must be untouched: `cp := *p` copies the map by
	// reference, so a sanitizer that mutated in place would destroy the stored
	// value for every later reader.
	if p.Settings["openai.token"] != "sk-live-SECRETVALUE" {
		t.Errorf("sanitizeProfile mutated the live stored profile: %q", p.Settings["openai.token"])
	}
}

// The end-to-end property the whole change is for: bytes written to the
// REPLICATED table must contain a shell preference and must not contain a
// secret. This reads the row the CRDT bridge captures, not the struct.
func TestSettingsReachTheReplicatedRow(t *testing.T) {
	s := newTestStoreAt(t, t.TempDir())
	s.SetProfile(&Profile{
		UserID: "u1",
		Theme:  "dark",
		Settings: map[string]string{
			"shell.accent": "#7c3aed",
			"openai.token": "sk-live-SECRETVALUE",
		},
	})

	raw := rawProfileRow(t, s, "u1")
	if !strings.Contains(raw, "#7c3aed") {
		t.Errorf("the shell preference is not in the replicated row — it will not follow the user:\n%s", raw)
	}
	if strings.Contains(raw, "SECRETVALUE") {
		t.Errorf("a secret-named setting reached the replicated row:\n%s", raw)
	}
}
