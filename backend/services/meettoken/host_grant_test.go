package meettoken

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// host_grant_test.go — MEET-HOST-01: the self-host token carries the host grant,
// on the wire, under the key everyone else actually reads.
//
// Three independent readers must agree on one byte sequence: LiveKit Server and
// vulos-meet's wrap.Validator unmarshal into livekit/protocol/auth.VideoGrant
// (json tag `roomAdmin`), and the browser base64url-decodes the payload itself
// and reads video.roomAdmin (web/src/lib/liveRoom.js tokenGrantsRoomAdmin) to
// decide whether to render any host control at all.
//
// This package hand-rolls the grant struct (to avoid pulling livekit's module
// into the OS backend), so a test that round-trips through our OWN claimGrants
// type proves nothing about that agreement — it would pass just as happily with
// the key misspelled. These tests therefore decode the RAW payload and assert
// the literal key, which is the only assertion that can fail if the wire shape
// drifts from what the SFU and the browser expect.

// rawVideoGrant base64url-decodes a JWT payload and returns its video grant as
// a free-form map — deliberately NOT typed through this package, so a wrong or
// renamed json tag surfaces as a missing key instead of being papered over.
func rawVideoGrant(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token is not a 3-part JWT: %d parts", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	var claims struct {
		Video map[string]any `json:"video"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if claims.Video == nil {
		t.Fatal("no video grant in the token payload")
	}
	return claims.Video
}

// TestMint_HostGrant_OnTheWire — a host token carries video.roomAdmin=true, spelled
// exactly as LiveKit and the browser read it.
func TestMint_HostGrant_OnTheWire(t *testing.T) {
	m, err := NewMinter(testKey, testSecret, 0)
	if err != nil {
		t.Fatalf("NewMinter: %v", err)
	}
	res, err := m.Mint(MintParams{TenantID: "user-abc", UserID: "user-abc", RoomName: "standup", Host: true})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	vg := rawVideoGrant(t, res.Token)
	admin, ok := vg["roomAdmin"]
	if !ok {
		t.Fatalf("no `roomAdmin` key in the video grant (keys: %v) — the SFU and the browser both look for exactly this key, so the host role would be silently dead", keysOf(vg))
	}
	if admin != true {
		t.Fatalf("video.roomAdmin=%v, want true", admin)
	}
	// Room-scoped only: a meeting token must never confer tenant-wide authority.
	if _, bad := vg["roomList"]; bad {
		t.Error("host token carries roomList — authority widened beyond its own room")
	}
	if _, bad := vg["roomCreate"]; bad {
		t.Error("host token carries roomCreate — authority widened beyond its own room")
	}
	if vg["room"] != "user-abc:standup" {
		t.Errorf("room=%v, want user-abc:standup", vg["room"])
	}
}

// TestMint_NoHostGrantByDefault — Host is opt-in. A caller that does not ask for
// it mints a plain participant token, and `omitempty` keeps the key off the wire
// entirely (which the browser reads as false).
func TestMint_NoHostGrantByDefault(t *testing.T) {
	m, _ := NewMinter(testKey, testSecret, 0)
	res, err := m.Mint(MintParams{TenantID: "user-abc", UserID: "user-abc", RoomName: "standup"})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	vg := rawVideoGrant(t, res.Token)
	if v, ok := vg["roomAdmin"]; ok && v == true {
		t.Fatal("a participant token carries roomAdmin=true — every joiner would be a host")
	}
}

// TestMint_HostGrantSurvivesExplicitMediaGrants — the host grant is orthogonal to
// the media grants; asking for a media-restricted token must not drop it (an
// observer-mode host is still the host).
func TestMint_HostGrantSurvivesExplicitMediaGrants(t *testing.T) {
	m, _ := NewMinter(testKey, testSecret, 0)
	res, err := m.Mint(MintParams{
		TenantID: "user-abc", UserID: "user-abc", RoomName: "standup",
		Host: true, CanPublish: false, CanSubscribe: true,
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	vg := rawVideoGrant(t, res.Token)
	if vg["roomAdmin"] != true {
		t.Fatal("host grant lost when media grants were set explicitly")
	}
	if vg["canPublish"] != false {
		t.Errorf("canPublish=%v, want false (explicit grant not honoured)", vg["canPublish"])
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
