// no-broker-dep:allow-file: tests exercise ProviderEphor/Set/ResetToEphor (internal package
// symbols) to prove Ephor is a real, selectable peer AND that
// ResetToEphor resets to vulos ('want vulos') -- the tests themselves
// assert the default is vulos, not ephor.

package relayconfig

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vulos/backend/services/network"
)

// resetState restores package globals to a clean slate between tests, mirroring
// gwurl's test reset pattern.
func resetState(t *testing.T) {
	t.Helper()
	mu.Lock()
	state = DefaultConfig()
	persistPath = ""
	mu.Unlock()
	turnStoreMu.Lock()
	turnStoreRef = nil
	turnStoreMu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		state = DefaultConfig()
		persistPath = ""
		mu.Unlock()
		turnStoreMu.Lock()
		turnStoreRef = nil
		turnStoreMu.Unlock()
	})
}

var bg = context.Background()

func TestDefaultConfig_IsVulosBuiltIn(t *testing.T) {
	resetState(t)
	if CurrentProvider() != ProviderVulos {
		t.Fatalf("CurrentProvider() = %q before Init, want vulos", CurrentProvider())
	}
}

func TestInit_MissingFile_DefaultsToBuiltIn(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init on empty dir: %v", err)
	}
	if CurrentProvider() != ProviderVulos {
		t.Fatalf("CurrentProvider() = %q, want vulos", CurrentProvider())
	}
}

func TestInit_CorruptFile_FailsSafeToBuiltIn(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "relayconfig.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Init(dir)
	if err == nil {
		t.Fatal("Init with corrupt file: want error")
	}
	if CurrentProvider() != ProviderVulos {
		t.Fatalf("after corrupt-file Init, CurrentProvider() = %q, want vulos (fail-safe)", CurrentProvider())
	}
	// The corrupt file must NOT be silently deleted — the owner should be
	// able to see/fix it.
	if _, statErr := os.Stat(filepath.Join(dir, "relayconfig.json")); statErr != nil {
		t.Fatalf("corrupt file was removed, want it left in place: %v", statErr)
	}
}

func TestInit_InvalidPersistedProvider_FailsSafeToBuiltIn(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	// Structurally valid JSON, but provider=turn with no ICE servers — invalid.
	bad := `{"provider":"turn","turn":{"ice_servers":[]}}`
	if err := os.WriteFile(filepath.Join(dir, "relayconfig.json"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Init(dir)
	if err == nil {
		t.Fatal("Init with invalid persisted config: want error")
	}
	if CurrentProvider() != ProviderVulos {
		t.Fatalf("after invalid-config Init, CurrentProvider() = %q, want vulos (fail-safe)", CurrentProvider())
	}
}

func TestSetAndReload_TURN(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	cfg := Config{
		Provider: ProviderTURN,
		TURN: TURNProviderConfig{ICEServers: []ICEServer{
			{URLs: []string{"turn:relay.example.org:3478?transport=udp"}, Username: "alice", Credential: "s3cr3t"},
		}},
	}
	// force=true: skip the health probe — this is a validation/persistence
	// test, not a network test, and relay.example.org is not dialable here.
	view, err := Set(cfg, true)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if view.Provider != ProviderTURN {
		t.Fatalf("view.Provider = %q, want turn", view.Provider)
	}
	if len(view.TURN.ICEServers) != 1 {
		t.Fatalf("view.TURN.ICEServers = %v, want 1 entry", view.TURN.ICEServers)
	}
	if view.TURN.ICEServers[0].HasCredential != true {
		t.Fatal("HasCredential = false, want true")
	}

	// Credential must NEVER appear in the public view's JSON-ish struct.
	// (PublicICEServer has no Credential field at all — this is a
	// compile-time guarantee, but assert on the value we DO expose too.)
	if view.TURN.ICEServers[0].Username != "alice" {
		t.Fatalf("Username = %q, want alice", view.TURN.ICEServers[0].Username)
	}

	// Survives reload.
	mu.Lock()
	state = DefaultConfig()
	mu.Unlock()
	if err := Init(dir); err != nil {
		t.Fatalf("re-Init: %v", err)
	}
	if CurrentProvider() != ProviderTURN {
		t.Fatalf("after reload, CurrentProvider() = %q, want turn", CurrentProvider())
	}

	// The store DOES persist the credential (it needs to, to hand it back to
	// WebRTC) — but Get()/the public view must never return it. Confirm the
	// redaction survives reload too.
	if got := Get(); got.TURN.ICEServers[0].HasCredential != true {
		t.Fatal("HasCredential lost across reload")
	}
}

func TestSet_InvalidConfig_RejectedAndCurrentUntouched(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Establish a known-good TURN config first.
	good := Config{
		Provider: ProviderTURN,
		TURN:     TURNProviderConfig{ICEServers: []ICEServer{{URLs: []string{"turn:good.example.org:3478"}}}},
	}
	if _, err := Set(good, true); err != nil {
		t.Fatalf("Set(good): %v", err)
	}

	// Now attempt an invalid change: libp2p with no peers.
	bad := Config{Provider: ProviderLibp2p}
	if _, err := Set(bad, true); err == nil {
		t.Fatal("Set(bad) succeeded, want rejection")
	}

	// Current provider must be UNCHANGED (still turn, still the good config).
	if CurrentProvider() != ProviderTURN {
		t.Fatalf("after rejected Set, CurrentProvider() = %q, want turn (unchanged)", CurrentProvider())
	}
	view := Get()
	if len(view.TURN.ICEServers) != 1 || view.TURN.ICEServers[0].URLs[0] != "turn:good.example.org:3478" {
		t.Fatalf("current config mutated by a rejected Set: %+v", view)
	}

	// And the file on disk must also be untouched (still the good config).
	if err := Init(dir); err != nil {
		t.Fatalf("re-Init after rejected Set: %v", err)
	}
	if CurrentProvider() != ProviderTURN {
		t.Fatalf("on-disk config corrupted by a rejected Set: provider = %q", CurrentProvider())
	}
}

func TestSet_ProbeFailure_RejectedWithoutForce(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Establish a known-good ephor baseline.
	cfg := Config{Provider: ProviderTURN, TURN: TURNProviderConfig{ICEServers: []ICEServer{
		{URLs: []string{"turn:127.0.0.1:1"}}, // port 1: connection refused, deterministic
	}}}
	_, err := Set(cfg, false)
	if err == nil {
		t.Fatal("Set with an unreachable TURN endpoint (force=false) succeeded, want rejection")
	}
	if CurrentProvider() != ProviderVulos {
		t.Fatalf("after rejected probe, CurrentProvider() = %q, want vulos (unchanged — never locked into a broken provider)", CurrentProvider())
	}
}

func TestSet_ProbeBypassedWithForce(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	cfg := Config{Provider: ProviderTURN, TURN: TURNProviderConfig{ICEServers: []ICEServer{
		{URLs: []string{"turn:127.0.0.1:1"}},
	}}}
	if _, err := Set(cfg, true); err != nil {
		t.Fatalf("Set with force=true should bypass the probe, got error: %v", err)
	}
	if CurrentProvider() != ProviderTURN {
		t.Fatalf("CurrentProvider() = %q, want turn (force bypassed the probe)", CurrentProvider())
	}
}

func TestSet_ProbeSucceeds_WhenEndpointIsReachable(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	cfg := Config{Provider: ProviderTURN, TURN: TURNProviderConfig{ICEServers: []ICEServer{
		{URLs: []string{"turn:127.0.0.1:" + port}},
	}}}
	if _, err := Set(cfg, false); err != nil {
		t.Fatalf("Set against a live listener (force=false) should pass the probe: %v", err)
	}
	if CurrentProvider() != ProviderTURN {
		t.Fatalf("CurrentProvider() = %q, want turn", CurrentProvider())
	}
}

func TestSet_EphorAndNone_NeverProbed(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// Neither of these has a dialable endpoint to probe; force=false must
	// still succeed immediately for both.
	if _, err := Set(Config{Provider: ProviderNone}, false); err != nil {
		t.Fatalf("Set(none, force=false): %v", err)
	}
	if _, err := Set(Config{Provider: ProviderEphor}, false); err != nil {
		t.Fatalf("Set(ephor, force=false): %v", err)
	}
}

func TestResetToDefault(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	turnCfg := Config{Provider: ProviderTURN, TURN: TURNProviderConfig{ICEServers: []ICEServer{{URLs: []string{"turn:x.example.org:3478"}}}}}
	if _, err := Set(turnCfg, true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	view, err := ResetToDefault()
	if err != nil {
		t.Fatalf("ResetToEphor: %v", err)
	}
	if view.Provider != ProviderVulos {
		t.Fatalf("ResetToEphor provider = %q, want vulos", view.Provider)
	}
	if CurrentProvider() != ProviderVulos {
		t.Fatalf("CurrentProvider() after reset = %q, want vulos", CurrentProvider())
	}
}

func TestValidate_Provider(t *testing.T) {
	resetState(t)
	if err := Validate(Config{Provider: "bogus"}); err == nil {
		t.Fatal("Validate accepted an unknown provider")
	}
	if err := Validate(Config{Provider: ProviderEphor}); err != nil {
		t.Fatalf("Validate rejected bare ephor: %v", err)
	}
	if err := Validate(Config{Provider: ProviderNone}); err != nil {
		t.Fatalf("Validate rejected bare none: %v", err)
	}
}

func TestValidate_TURN_ICEServers(t *testing.T) {
	resetState(t)
	cases := []struct {
		name    string
		servers []ICEServer
		wantErr bool
	}{
		{"valid stun", []ICEServer{{URLs: []string{"stun:stun.example.org:19302"}}}, false},
		{"valid turn with creds", []ICEServer{{URLs: []string{"turn:relay.example.org:3478?transport=tcp"}, Username: "u", Credential: "c"}}, false},
		{"valid turns no port", []ICEServer{{URLs: []string{"turns:relay.example.org"}}}, false},
		{"empty urls", []ICEServer{{URLs: nil}}, true},
		{"bad scheme", []ICEServer{{URLs: []string{"https://relay.example.org"}}}, true},
		{"no scheme", []ICEServer{{URLs: []string{"relay.example.org:3478"}}}, true},
		{"whitespace host", []ICEServer{{URLs: []string{"turn:evil host:3478"}}}, true},
		{"invalid port", []ICEServer{{URLs: []string{"turn:relay.example.org:notaport"}}}, true},
		{"empty url", []ICEServer{{URLs: []string{""}}}, true},
		{"control chars", []ICEServer{{URLs: []string{"turn:relay.example.org:3478\x00"}}}, true},
		{"embedded credentials rejected", []ICEServer{{URLs: []string{"turn:user:pass@relay.example.org:3478"}}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(Config{Provider: ProviderTURN, TURN: TURNProviderConfig{ICEServers: c.servers}})
			if c.wantErr && err == nil {
				t.Errorf("Validate(%s) = nil, want error", c.name)
			}
			if !c.wantErr && err != nil {
				t.Errorf("Validate(%s) = %v, want nil", c.name, err)
			}
		})
	}
}

func TestValidate_Libp2pRelayPeers(t *testing.T) {
	resetState(t)
	cases := []struct {
		name    string
		peers   []string
		wantErr bool
	}{
		{"valid p2p", []string{"/dns4/relay.example.org/tcp/4001/p2p/12D3KooWAbCdEfGhIjKlMnOpQrStUvWxYz"}, false},
		{"valid ipfs legacy", []string{"/ip4/1.2.3.4/tcp/4001/ipfs/QmSomePeerId"}, false},
		{"missing p2p component", []string{"/dns4/relay.example.org/tcp/4001"}, true},
		{"empty peer id after p2p", []string{"/dns4/relay.example.org/tcp/4001/p2p/"}, true},
		{"no leading slash", []string{"dns4/relay.example.org/tcp/4001/p2p/Qm"}, true},
		{"too short", []string{"/p2p"}, true},
		{"whitespace", []string{"/dns4/relay example.org/tcp/4001/p2p/Qm"}, true},
		{"empty string", []string{""}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(Config{Provider: ProviderLibp2p, Libp2p: Libp2pProviderConfig{RelayPeers: c.peers}})
			if c.wantErr && err == nil {
				t.Errorf("Validate(%s) = nil, want error", c.name)
			}
			if !c.wantErr && err != nil {
				t.Errorf("Validate(%s) = %v, want nil", c.name, err)
			}
		})
	}
}

func TestValidate_WireGuardEndpoint(t *testing.T) {
	resetState(t)
	cases := []struct {
		name     string
		endpoint string
		wantErr  bool
	}{
		{"valid host:port", "headscale.example.org:8080", false},
		{"valid https url", "https://headscale.example.org", false},
		{"valid http url (LAN)", "http://192.168.1.5:8080", false},
		{"empty", "", true},
		{"bad scheme", "ftp://headscale.example.org", true},
		{"no port bare host", "headscale.example.org", true},
		{"whitespace", "headscale example.org:8080", true},
		{"bad port", "headscale.example.org:notaport", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := Config{Provider: ProviderWireGuard, WireGuard: WireGuardProviderConfig{Endpoint: c.endpoint}}
			err := Validate(cfg)
			if c.wantErr && err == nil {
				t.Errorf("Validate(%s) = nil, want error", c.name)
			}
			if !c.wantErr && err != nil {
				t.Errorf("Validate(%s) = %v, want nil", c.name, err)
			}
		})
	}
}

// --- Facet-model tests: the critical trap the review flagged. ---

func TestICEServers_NeverGoesDarkForLibp2p(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	peer := "/dns4/relay.example.org/tcp/4001/p2p/12D3KooWAbCdEfGhIjKlMnOpQrStUvWxYz"
	if _, err := Set(Config{Provider: ProviderLibp2p, Libp2p: Libp2pProviderConfig{RelayPeers: []string{peer}}}, true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// libp2p claims facets B+C, NOT A — ICE must still resolve via ephor's
	// fallback, never an empty list.
	ice := ICEServers(bg, "user-1")
	if os.Getenv("VULOS_STUN_DISABLE_PUBLIC") == "" && len(ice) == 0 {
		t.Fatal("ICEServers() returned nothing while libp2p is the active provider — facet A silently went dark (the critical trap)")
	}
}

func TestICEServers_NeverGoesDarkForWireGuard(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := Set(Config{Provider: ProviderWireGuard, WireGuard: WireGuardProviderConfig{Endpoint: "hs.example.org:8080"}}, true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ice := ICEServers(bg, "user-1")
	if os.Getenv("VULOS_STUN_DISABLE_PUBLIC") == "" && len(ice) == 0 {
		t.Fatal("ICEServers() returned nothing while wireguard is the active provider — facet A silently went dark (the critical trap)")
	}
}

func TestICEServers_NoneStillServesICE(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := Set(Config{Provider: ProviderNone}, true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// "none" only opts OUT of the relay-tunnel ingress (facet B); ICE (facet
	// A) is untouched and still resolves via ephor.
	ice := ICEServers(bg, "user-1")
	if os.Getenv("VULOS_STUN_DISABLE_PUBLIC") == "" && len(ice) == 0 {
		t.Fatal("ICEServers() returned nothing while none is the active provider — facet A wrongly coupled to facet B")
	}
}

func TestICEServers_TURN_ReplacesEphorICE(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := Set(Config{Provider: ProviderTURN, TURN: TURNProviderConfig{ICEServers: []ICEServer{
		{URLs: []string{"turn:relay.example.org:3478"}, Username: "u", Credential: "c"},
	}}}, true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ice := ICEServers(bg, "user-1")
	if len(ice) != 1 || ice[0].URLs[0] != "turn:relay.example.org:3478" {
		t.Fatalf("ICEServers() = %+v, want exactly the configured BYO TURN server (turn provider claims facet A exclusively)", ice)
	}
}

func TestIngressInfo_FallsBackToBuiltInWhenUnclaimed(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// A live tunnel is up, as reported by the composition root.
	SetIngressReporter(func() IngressDescriptor {
		return IngressDescriptor{Mode: "relay-tunnel", Detail: "https://box1.relay.example.com"}
	})
	t.Cleanup(func() { SetIngressReporter(nil) })

	// turn only claims facet A — ingress must fall back to the built-in
	// provider, which reports the LIVE tunnel rather than going dark.
	if _, err := Set(Config{Provider: ProviderTURN, TURN: TURNProviderConfig{ICEServers: []ICEServer{
		{URLs: []string{"turn:relay.example.org:3478"}},
	}}}, true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ing := IngressInfo()
	if ing.Mode != "relay-tunnel" {
		t.Fatalf("IngressInfo().Mode = %q, want relay-tunnel (built-in fallback) when turn doesn't claim ingress", ing.Mode)
	}
	if ing.Detail != "https://box1.relay.example.com" {
		t.Errorf("IngressInfo().Detail = %q, want the live tunnel URL", ing.Detail)
	}
}

// TestIngressInfo_HonestWhenNoRelayConfigured is the anti-false-confidence
// rule. The previous implementation returned a hardcoded relay hostname
// whether or not anything was running there, so Settings could show a
// confident "relay-tunnel https://…" for a box that was in fact unreachable
// from the internet. Reporting the truth is more useful than reporting a URL
// nobody serves.
func TestIngressInfo_HonestWhenNoRelayConfigured(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	SetIngressReporter(nil)

	ing := IngressInfo()
	if ing.Mode != "none" {
		t.Errorf("IngressInfo().Mode = %q, want none when no relay is configured", ing.Mode)
	}
	if strings.Contains(ing.Detail, "https://") {
		t.Errorf("IngressInfo().Detail invented a URL: %q", ing.Detail)
	}
}

func TestIngressInfo_None(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := Set(Config{Provider: ProviderNone}, true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ing := IngressInfo()
	if ing.Mode != "direct-portforward" {
		t.Fatalf("IngressInfo().Mode = %q, want direct-portforward", ing.Mode)
	}
}

// TestIngress_EphorReportsLiveMultiRelayState is the dual-provider keystone:
// selecting Ephor as the provider must NOT regress ingress reporting to a
// single VULOS_RELAY_BASE_URL. Both the built-in Vulos provider and the Ephor
// provider report the SAME live tunnel state from the shared reporter, because
// the embedded agent holds one link per relay regardless of who operates it —
// which is exactly what lets an owner run a Vulos relay and an Ephor relay at
// the same time and see both.
func TestIngress_EphorReportsLiveMultiRelayState(t *testing.T) {
	resetState(t)
	// The composition root reports TWO live links: one Vulos relay, one Ephor
	// relay, held simultaneously by the one agent.
	SetIngressReporter(func() IngressDescriptor {
		return IngressDescriptor{
			Mode:   "relay-tunnel",
			Detail: "https://box1.vulos-relay.example.com, https://box1.ephor-relay.example.net",
		}
	})
	t.Cleanup(func() { SetIngressReporter(nil) })

	vulos := vulosProvider{}.Ingress()
	ephor := ephorProvider{}.Ingress()

	if vulos != ephor {
		t.Fatalf("vulos and ephor ingress disagree:\n vulos=%+v\n ephor=%+v", vulos, ephor)
	}
	if ephor.Mode != "relay-tunnel" {
		t.Fatalf("ephor Ingress().Mode = %q, want relay-tunnel from the live reporter", ephor.Mode)
	}
	if !strings.Contains(ephor.Detail, "ephor-relay.example.net") ||
		!strings.Contains(ephor.Detail, "vulos-relay.example.com") {
		t.Errorf("ephor Ingress().Detail = %q, want BOTH the Vulos and Ephor live links (coexistence)", ephor.Detail)
	}
}

// TestIngress_EphorHonestWhenNoTunnelUp is the anti-false-confidence rule for
// the Ephor provider — the same guarantee vulosProvider already had. With no
// live tunnel reported it must NOT invent a URL from a stale env var; it says
// "none".
func TestIngress_EphorHonestWhenNoTunnelUp(t *testing.T) {
	resetState(t)
	// A leftover legacy single-relay env var must not resurrect the old
	// single-URL behaviour now that both providers report from the live agent.
	t.Setenv("VULOS_RELAY_BASE_URL", "https://stale.example.com")
	SetIngressReporter(nil)

	ing := ephorProvider{}.Ingress()
	if ing.Mode != "none" {
		t.Errorf("ephor Ingress().Mode = %q, want none when no tunnel is up", ing.Mode)
	}
	if strings.Contains(ing.Detail, "stale.example.com") {
		t.Errorf("ephor Ingress().Detail leaked the stale legacy env URL: %q", ing.Detail)
	}
}

func TestResolvePeer_FallsBackWhenUnclaimed(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// ephor (default) does not implement live resolution in this package —
	// it defers to peering/resolve.go — so this must report not-ok, not a
	// fabricated URL.
	if _, ok := ResolvePeer(bg, "some-peer"); ok {
		t.Fatal("ResolvePeer() reported ok from the default ephor stub, want false")
	}
}

func TestEffective_None(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := Set(Config{Provider: ProviderNone}, true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	eff := Effective(bg, "user-1")
	if eff.Provider != ProviderNone {
		t.Fatalf("Provider = %q, want none", eff.Provider)
	}
	if eff.Ingress.Mode != "direct-portforward" {
		t.Fatalf("Ingress.Mode = %q, want direct-portforward", eff.Ingress.Mode)
	}
}

func TestEffective_TURN(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	cfg := Config{Provider: ProviderTURN, TURN: TURNProviderConfig{ICEServers: []ICEServer{
		{URLs: []string{"turn:relay.example.org:3478"}, Username: "u", Credential: "c"},
	}}}
	if _, err := Set(cfg, true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	eff := Effective(bg, "user-1")
	if eff.Provider != ProviderTURN {
		t.Fatalf("Provider = %q, want turn", eff.Provider)
	}
	if len(eff.ICEServers) != 1 || eff.ICEServers[0].Credential != "c" {
		t.Fatalf("Effective ICEServers = %+v, want the configured BYO server with credential intact", eff.ICEServers)
	}
}

func TestEffective_Libp2p(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	peer := "/dns4/relay.example.org/tcp/4001/p2p/12D3KooWAbCdEfGhIjKlMnOpQrStUvWxYz"
	if _, err := Set(Config{Provider: ProviderLibp2p, Libp2p: Libp2pProviderConfig{RelayPeers: []string{peer}}}, true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	eff := Effective(bg, "user-1")
	if eff.Provider != ProviderLibp2p {
		t.Fatalf("Provider = %q, want libp2p", eff.Provider)
	}
	if len(eff.Libp2pRelayPeers) != 1 || eff.Libp2pRelayPeers[0] != peer {
		t.Fatalf("Libp2pRelayPeers = %v, want [%s]", eff.Libp2pRelayPeers, peer)
	}
	// ICE must STILL be populated (ephor fallback) — see the "never goes
	// dark" tests above; just a light sanity check here too.
	if os.Getenv("VULOS_STUN_DISABLE_PUBLIC") == "" && len(eff.ICEServers) == 0 {
		t.Fatal("Effective().ICEServers empty for libp2p — facet A should fall back to ephor")
	}
}

func TestEffective_WireGuard(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := Set(Config{Provider: ProviderWireGuard, WireGuard: WireGuardProviderConfig{Endpoint: "hs.example.org:8080", Network: "mynet"}}, true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	eff := Effective(bg, "user-1")
	if eff.Provider != ProviderWireGuard {
		t.Fatalf("Provider = %q, want wireguard", eff.Provider)
	}
	if eff.WireGuard == nil || eff.WireGuard.Endpoint != "hs.example.org:8080" || eff.WireGuard.Network != "mynet" {
		t.Fatalf("WireGuard = %+v, want the configured endpoint/network", eff.WireGuard)
	}
}

func TestEffective_BuiltIn_Default(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	eff := Effective(bg, "user-1")
	if eff.Provider != ProviderVulos {
		t.Fatalf("Provider = %q, want vulos (default)", eff.Provider)
	}
	// No tunnel has been reported in this test, so the honest answer is none.
	if eff.Ingress.Mode != "none" {
		t.Fatalf("Ingress.Mode = %q, want none (no relay configured in this test)", eff.Ingress.Mode)
	}
	// Public STUN should be present unless VULOS_STUN_DISABLE_PUBLIC is set in
	// this test environment (it shouldn't be by default).
	if os.Getenv("VULOS_STUN_DISABLE_PUBLIC") == "" && len(eff.ICEServers) == 0 {
		t.Fatal("Effective() returned no ICE servers, want at least public STUN")
	}
}

func TestSetTURNStore_MakesAdminConfigAuthoritative(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	storeDir := t.TempDir()
	store, err := network.NewTURNStore(storeDir)
	if err != nil {
		t.Fatalf("NewTURNStore: %v", err)
	}
	if err := store.Set("relay.example.org", 3478, "vulos", "s3cr3t"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	SetTURNStore(store)
	t.Cleanup(func() { SetTURNStore(nil) })

	tc := effectiveTURNConfig()
	if !tc.Enabled || tc.Host != "relay.example.org" || tc.Port != 3478 {
		t.Fatalf("effectiveTURNConfig() = %+v, want the admin-configured store to be authoritative", tc)
	}

	// And it must actually reach ephor's ICE list (the split-brain fix).
	ice := builtinICEServers("user-1")
	found := false
	for _, s := range ice {
		for _, u := range s.URLs {
			if contains(u, "relay.example.org") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("builtinICEServers() = %+v, want an entry referencing the admin-configured TURN host", ice)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestGet_NeverExposesCredential(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	cfg := Config{Provider: ProviderTURN, TURN: TURNProviderConfig{ICEServers: []ICEServer{
		{URLs: []string{"turn:relay.example.org:3478"}, Username: "u", Credential: "top-secret"},
	}}}
	if _, err := Set(cfg, true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	view := Get()
	// PublicICEServer has no Credential field — this is enforced at compile
	// time by the type itself. Just confirm the redaction flag is right.
	if !view.TURN.ICEServers[0].HasCredential {
		t.Fatal("HasCredential = false, want true")
	}
}

func TestTestReachability_None(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := Set(Config{Provider: ProviderNone}, true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	res := TestReachability()
	if !res.Success {
		t.Fatalf("TestReachability() for none provider = %+v, want success (nothing to test)", res)
	}
}

func TestTestReachability_TURN_Unreachable(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	cfg := Config{Provider: ProviderTURN, TURN: TURNProviderConfig{ICEServers: []ICEServer{
		{URLs: []string{"turn:127.0.0.1:1"}}, // port 1 should refuse the connection
	}}}
	if _, err := Set(cfg, true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	res := TestReachability()
	if res.Success {
		t.Fatal("TestReachability() succeeded dialing a closed port, want failure")
	}
}

func TestTestReachability_Libp2p_NotSupported(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	peer := "/dns4/relay.example.org/tcp/4001/p2p/12D3KooWAbCdEfGhIjKlMnOpQrStUvWxYz"
	if _, err := Set(Config{Provider: ProviderLibp2p, Libp2p: Libp2pProviderConfig{RelayPeers: []string{peer}}}, true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	res := TestReachability()
	if res.Success {
		t.Fatal("TestReachability() for libp2p reported success, want an explicit not-supported failure")
	}
	if res.Detail == "" {
		t.Fatal("TestReachability() for libp2p returned no explanatory detail")
	}
}

func TestSet_NotInitialised(t *testing.T) {
	resetState(t)
	// Deliberately skip Init.
	_, err := Set(Config{Provider: ProviderNone}, true)
	if err == nil {
		t.Fatal("Set before Init succeeded, want error")
	}
}

func TestProviderValid(t *testing.T) {
	valid := []Provider{ProviderEphor, ProviderNone, ProviderTURN, ProviderLibp2p, ProviderWireGuard}
	for _, p := range valid {
		if !p.Valid() {
			t.Errorf("Provider(%q).Valid() = false, want true", p)
		}
	}
	if Provider("nonsense").Valid() {
		t.Error(`Provider("nonsense").Valid() = true, want false`)
	}
}
