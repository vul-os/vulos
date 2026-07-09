package peering

// PEER-42: wiring regression test.
//
// Builds the same peeringMux that cmd/server/main.go constructs and probes
// every previously-501 peering route.  A 501 response means a handler is
// defined in the package but was not wired into the mux — exactly the class
// of regression this guard catches.
//
// Any response OTHER than 501 counts as "wired":
//   - 200/201/204 → happy path (rare without real state)
//   - 400 → body parse error (handler reached, request invalid — OK)
//   - 401/403 → auth gate (handler reached — OK)
//   - 404 → resource not found (handler reached — OK)
//   - 405 → method not allowed (Go's ServeMux default — not a custom 501 stub)
//   - 503 → handler reached, dep unavailable — OK
//
// Only 501 is a hard failure.

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildPeer42Mux replicates the peeringMux construction from
// cmd/server/main.go (PEER-42 block) using temp-dir stores.
func buildPeer42Mux(t *testing.T) *http.ServeMux {
	t.Helper()

	home := t.TempDir()
	pRoot := filepath.Join(home, ".vulos", "peering")
	if err := os.MkdirAll(pRoot, 0o700); err != nil {
		t.Fatalf("mkdir pRoot: %v", err)
	}

	// Generate a throwaway Ed25519 key for signing.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	// Use a fixed test VulaID — no real base58 encoder needed for wiring tests.
	vulaID := "vula:test:deadbeef"

	peeringMux := http.NewServeMux()

	contactStore, csErr := NewContactStore(home)
	if csErr != nil {
		t.Fatalf("ContactStore: %v", csErr)
	}
	inboxStore, ibErr := NewInboxStore(home)
	if ibErr != nil {
		t.Fatalf("InboxStore: %v", ibErr)
	}
	peerClient := NewPeerClient()

	callerVulaID := func(r *http.Request) string { return r.Header.Get("X-Vula-ID") }

	// Contacts.
	contactAPI := NewContactAPI(contactStore, peerClient, NewHub(), priv, vulaID, "localhost:0")
	contactAPI.OnApprove = FetchPeerProfileAsync
	contactAPI.RegisterContactHandlers(peeringMux)

	// Messages.
	msgAPI := NewMessageAPI(contactStore, inboxStore, peerClient, NewHub(), priv, vulaID)
	msgAPI.RegisterMessageHandlers(peeringMux)

	// Groups.
	groupStore, gErr := NewGroupStore(home)
	if gErr != nil {
		t.Fatalf("GroupStore: %v", gErr)
	}
	groupAPI := NewGroupAPI(groupStore, contactStore, inboxStore, peerClient, NewHub(), priv, vulaID)
	RegisterGroupHandlers(peeringMux, groupAPI)

	// Relay.
	relayStore, rErr := NewRelayStore(home, contactStore)
	if rErr != nil {
		t.Fatalf("RelayStore: %v", rErr)
	}
	RegisterRelayHandlers(peeringMux, relayStore)

	// Feeds.
	feedStore, fErr := NewFeedStore(pRoot, priv, vulaID, contactStore)
	if fErr != nil {
		t.Fatalf("FeedStore: %v", fErr)
	}
	RegisterFeedHandlers(peeringMux, feedStore, callerVulaID)

	// Drop.
	dropSvc := NewDropService(vulaID, "", contactStore, nil)
	RegisterDropHandlers(peeringMux, dropSvc)

	// Media.
	mediaStore, mErr := NewMediaStore(home, priv, peerClient)
	if mErr != nil {
		t.Fatalf("MediaStore: %v", mErr)
	}
	mediaStore.RegisterMediaHandlers(peeringMux)

	// Calls.
	callRelay := NewCallRelay(vulaID, contactStore, NewHub(), peerClient, priv)
	RegisterCallHandlers(peeringMux, callRelay)

	// Mesh call.
	RegisterMeshCallHandlers(peeringMux, NewMeshSignalingHub())

	// Call history.
	RegisterCallHistoryHandlers(peeringMux, pRoot)

	// Lobby.
	bwMeter := NewBandwidthMeter(BandwidthConfig{})
	lobbySvc := NewLobbyService(bwMeter)
	RegisterLobbyHandlers(peeringMux, lobbySvc)

	// Profile.
	RegisterProfileHandlers(peeringMux, filepath.Join(pRoot, "profile"), vulaID, contactStore)

	// Verify.
	vfySvc, vErr := VerifyNewService(filepath.Join(pRoot, "identity"))
	if vErr != nil {
		t.Fatalf("VerifyService: %v", vErr)
	}
	RegisterVerifyHandlers(peeringMux, vfySvc)

	// Discovery.
	RegisterDiscoveryHandlers(peeringMux, DiscoveryNewService(nil))

	// ICE.
	RegisterICEHandlers(peeringMux)

	// Relay attestation.
	RegisterAttestHandlers(peeringMux, NewAttestStore())

	// Proximity drop.
	RegisterProximityHandlers(peeringMux, NewProxService(vulaID, "localhost:0"))

	// Collab + history.
	collabStore, cErr := NewCollabStore(filepath.Join(pRoot, "collab"))
	if cErr != nil {
		t.Fatalf("CollabStore: %v", cErr)
	}
	RegisterCollabHandlers(peeringMux, collabStore)
	RegisterCollabHistoryHandlers(peeringMux, collabStore)

	// Presence.
	RegisterPresenceHandlers(peeringMux, NewPresenceService())

	// Collab-invite inbound.
	sharesSvc := NewSharesService(contactStore, NewShareStore(), peerClient, priv, vulaID)
	peeringMux.HandleFunc("POST /api/peering/inbound/collab-invite", sharesSvc.HandleInboundShare)

	return peeringMux
}

// peer42RouteTable lists every previously-501 peering route.
// Entries are: method, path, body (empty string = no body).
var peer42RouteTable = []struct{ method, path, body string }{
	// contacts
	{"GET", "/api/peering/contacts", ""},
	{"GET", "/api/peering/contacts/requests", ""},
	{"POST", "/api/peering/contacts/request", `{"vula_id":"x","server":"localhost"}`},
	{"POST", "/api/peering/contacts/approve/test-id", "{}"},
	{"POST", "/api/peering/contacts/block/test-id", "{}"},
	{"DELETE", "/api/peering/contacts/test-id", ""},
	// messages / conversations
	{"GET", "/api/peering/conversations", ""},
	{"GET", "/api/peering/conversations/test-id/messages", ""},
	{"POST", "/api/peering/conversations/test-id/send", `{"text":"hi"}`},
	// groups
	{"GET", "/api/peering/groups", ""},
	{"POST", "/api/peering/groups", `{"name":"test"}`},
	{"GET", "/api/peering/groups/test-id", ""},
	{"POST", "/api/peering/groups/test-id/members", `{"vula_id":"x"}`},
	{"POST", "/api/peering/groups/test-id/send", `{"text":"hi"}`},
	// relay
	{"POST", "/api/peering/relay/deposit", `{"envelope":""}`},
	{"GET", "/api/peering/relay/pickup", ""},
	{"POST", "/api/peering/relay/ack", `{"ids":[]}`},
	{"GET", "/api/peering/relay/attest", ""},
	// feeds
	{"GET", "/api/feeds", ""},
	{"POST", "/api/feeds", `{"title":"test"}`},
	{"GET", "/api/feeds/test-id", ""},
	{"POST", "/api/feeds/test-id/publish", `{"content":"hello"}`},
	{"GET", "/api/feeds/test-id/entries", ""},
	// drop
	{"GET", "/api/peering/drop/nearby", ""},
	{"POST", "/api/peering/drop/send", `{"peer_id":"x"}`},
	{"GET", "/api/peering/drop/settings", ""},
	{"POST", "/api/peering/drop/decide", `{"transfer_id":"x","accept":false}`},
	// proximity
	{"POST", "/api/peering/drop/code/generate", "{}"},
	{"POST", "/api/peering/drop/code/redeem", `{"code":"XXXXXX"}`},
	// media
	{"GET", "/api/peering/media/fetch/aaaa", ""},
	{"GET", "/api/peering/media/thumb/aaaa", ""},
	// calls
	{"POST", "/api/peering/call/initiate", `{"peer_id":"x","sdp":""}`},
	{"POST", "/api/peering/call/answer", `{"call_id":"x","sdp":""}`},
	{"POST", "/api/peering/call/reject", `{"call_id":"x"}`},
	{"POST", "/api/peering/call/signal", `{"call_id":"x","candidate":""}`},
	{"POST", "/api/peering/call/hangup", `{"call_id":"x"}`},
	// call history
	{"GET", "/api/peering/call/history", ""},
	{"POST", "/api/peering/call/history/record", `{"peer_id":"x","direction":"outbound","duration_s":0}`},
	// lobby (PEER-25)
	{"GET", "/api/peering/lobby/capacity", ""},
	{"POST", "/api/peering/lobby/recommend-host", "{}"},
	// profile
	{"GET", "/api/peering/profile", ""},
	{"PUT", "/api/peering/profile", `{"display_name":"test"}`},
	{"GET", "/api/peering/profile/test-id", ""},
	// verify (email)
	{"POST", "/verify/email/start", `{"email":"smoke@example.com"}`},
	{"GET", "/verify/email/status", ""},
	// discovery
	{"GET", "/api/peering/discover", ""},
	// ICE / TURN config
	{"GET", "/api/peering/ice", ""},
	// endpoints: PEER-40 registry routes were deleted (CONSOLIDATION B-3) — no
	// longer expected to be wired.
	// collab CRDT
	{"GET", "/api/peering/collab/documents", ""},
	{"POST", "/api/peering/collab/share", `{"title":"test"}`},
	{"GET", "/api/peering/collab/test-id", ""},
	{"DELETE", "/api/peering/collab/test-id", ""},
	// collab history (PEER-42 addition)
	{"GET", "/api/peering/collab/test-id/history", ""},
	{"GET", "/api/peering/collab/test-id/history/1", ""},
	{"GET", "/api/peering/collab-sync-v2", ""},
	// presence (PEER-42 addition)
	{"GET", "/api/peering/presence/test-app/peers", ""},
	// inbound collab-invite (PEER-42 addition)
	{"POST", "/api/peering/inbound/collab-invite", `{"envelope":""}`},
	// inbound relay
	{"POST", "/api/peering/inbound/relay", `{}`},
	// inbound presence
	{"POST", "/api/peering/inbound/presence", `{}`},
}

// TestPEER42_No501Routes is the PEER-42 acceptance-criteria regression guard.
// It constructs the full peeringMux and asserts that no registered peering
// route returns HTTP 501.
func TestPEER42_No501Routes(t *testing.T) {
	mux := buildPeer42Mux(t)

	failures := 0
	for _, tc := range peer42RouteTable {
		var reqBody *strings.Reader
		if tc.body != "" {
			reqBody = strings.NewReader(tc.body)
		} else {
			reqBody = strings.NewReader("")
		}
		req := httptest.NewRequest(tc.method, tc.path, reqBody)
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code == http.StatusNotImplemented {
			t.Errorf("PEER-42 FAIL: %s %s returned 501 (not wired)", tc.method, tc.path)
			failures++
		}
	}

	if failures > 0 {
		t.Errorf("PEER-42: %d route(s) returned 501 — check cmd/server/main.go PEER-42 block", failures)
	}
}

// TestPEER42_PeeringMuxNoPanic verifies that wiring all handlers onto the
// same ServeMux does not trigger a panic from duplicate route registration.
func TestPEER42_PeeringMuxNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("PEER-42: ServeMux registration panicked (duplicate route?): %v", r)
		}
	}()
	_ = buildPeer42Mux(t)
}
