// no-broker-dep:allow-file: tests exercise ProviderEphor/Set/ResetToEphor (internal package
// symbols) to prove Pier is a real, selectable peer AND that
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
// selecting Pier as the provider must NOT regress ingress reporting to a
// single VULOS_RELAY_BASE_URL. Both the built-in Vulos provider and the Pier
// provider report the SAME live tunnel state from the shared reporter, because
// the embedded agent holds one link per relay regardless of who operates it —
// which is exactly what lets an owner run a Vulos relay and a Pier relay at
// the same time and see both.
func TestIngress_EphorReportsLiveMultiRelayState(t *testing.T) {
	resetState(t)
	// The composition root reports TWO live links: one Vulos relay, one Pier
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
		t.Errorf("ephor Ingress().Detail = %q, want BOTH the Vulos and Pier live links (coexistence)", ephor.Detail)
	}
}

// TestIngress_EphorHonestWhenNoTunnelUp is the anti-false-confidence rule for
// the Pier provider — the same guarantee vulosProvider already had. With no
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

// TestWireGuardProvider_SaysWhatItDoesNotDo is the anti-false-confidence rule
// applied to the mesh provider, and it is the sharper case: selecting
// "WireGuard mesh (Tailscale/Headscale/Nebula)" reads as "my boxes now find
// each other over my mesh", and it does not do that. It records a coordinator
// endpoint. Nothing more.
//
// Two independent reasons peer resolution is not merely unimplemented but
// UNREACHABLE, both asserted below so a future edit cannot quietly make the
// label true in one place and false in another:
//
//   - wireguardProvider does not claim FacetRendezvous, so the package-level
//     ResolvePeer dispatcher never reaches its method at all;
//   - the method returns not-ok regardless.
//
// See wireguardProvider.ResolvePeer for why implementing it was declined
// rather than deferred: the seam is handed a VulosID and a mesh knows machine
// names, and the box already has a working key-addressed mesh discovery path.
func TestWireGuardProvider_SaysWhatItDoesNotDo(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := Set(Config{
		Provider:  ProviderWireGuard,
		WireGuard: WireGuardProviderConfig{Endpoint: "headscale.example.org:8080", Network: "my-tailnet"},
	}, true); err != nil {
		t.Fatalf("Set: %v", err)
	}

	ing := IngressInfo()
	if ing.Mode != "wireguard-mesh" {
		t.Fatalf("IngressInfo().Mode = %q, want wireguard-mesh", ing.Mode)
	}
	if !strings.Contains(ing.Detail, "headscale.example.org:8080") {
		t.Errorf("IngressInfo().Detail lost the configured endpoint: %q", ing.Detail)
	}
	// The disclosure, and each half of it. A reader must be able to tell from
	// this string alone that neither ingress nor rendezvous is actuated.
	for _, want := range []string{"report-only", "does not re-route", "does not resolve peers"} {
		if !strings.Contains(ing.Detail, want) {
			t.Errorf("IngressInfo().Detail does not disclose %q — a control that appears to do "+
				"something is exactly the defect this asserts against. Got: %q", want, ing.Detail)
		}
	}
	// And it must point somewhere that says what DOES work.
	if !strings.Contains(ing.Detail, "REACH.md") {
		t.Errorf("IngressInfo().Detail says what does not work without saying what does: %q", ing.Detail)
	}

	// Reason 1: the facet is not claimed, so the dispatcher never gets here.
	if (wireguardProvider{}).Capabilities().Has(FacetRendezvous) {
		t.Error("wireguardProvider now claims FacetRendezvous. If that is deliberate, ResolvePeer " +
			"must actually resolve and this test must be rewritten — do not leave a claimed facet " +
			"answering not-ok.")
	}
	// Reason 2: and the method answers not-ok anyway.
	if url, ok := (wireguardProvider{}).ResolvePeer(bg, "vulos1abc"); ok {
		t.Errorf("wireguardProvider.ResolvePeer resolved %q; if peer resolution is now real, "+
			"claim FacetRendezvous and rewrite this test", url)
	}
	if url, ok := ResolvePeer(bg, "vulos1abc"); ok {
		t.Errorf("relayconfig.ResolvePeer resolved %q under the wireguard provider", url)
	}
}

// The same disclosure rule for libp2p, whose Ingress already carried a
// report-only note. Pinned so a refactor cannot drop it.
func TestLibp2pProvider_IngressDisclosesReportOnly(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := Set(Config{
		Provider: ProviderLibp2p,
		Libp2p:   Libp2pProviderConfig{RelayPeers: []string{"/dns4/relay.example.org/tcp/4001/p2p/12D3KooWtest"}},
	}, true); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ing := IngressInfo()
	if ing.Mode != "libp2p-circuit-relay" {
		t.Fatalf("IngressInfo().Mode = %q, want libp2p-circuit-relay", ing.Mode)
	}
	if url, ok := ResolvePeer(bg, "vulos1abc"); ok {
		t.Errorf("relayconfig.ResolvePeer resolved %q under the libp2p provider, which does not "+
			"implement dial-through-Circuit-Relay-v2 peer resolution", url)
	}
}

// ---------------------------------------------------------------------------
// The credential round trip: Get() never returns a credential, so Settings
// re-renders a saved TURN server with an EMPTY credential box. Saving that
// form used to send "no credential" into Set — which takes a whole Config and
// overwrites — so re-saving an unrelated field DESTROYED the stored TURN
// secret, and the box answered "Relay configuration saved." TURN is the
// fallback path when a direct connection fails, so the damage surfaced later
// and elsewhere, as calls that will not connect for some people.
//
// Both directions are pinned below. A test for preservation alone would let
// "always keep" ship, which is the same data bug pointing the other way: the
// owner could never remove a credential.
// ---------------------------------------------------------------------------

// storedCredential reads the real (unredacted) persisted credential — the
// value Get() deliberately will not show. Tests assert on the SECRET itself,
// not just on HasCredential: a mutation that swapped one non-empty credential
// for another would satisfy the flag and still have destroyed the config.
func storedCredential(t *testing.T, i int) string {
	t.Helper()
	cfg := currentConfig()
	if i >= len(cfg.TURN.ICEServers) {
		t.Fatalf("no stored ICE server #%d (have %d)", i, len(cfg.TURN.ICEServers))
	}
	return cfg.TURN.ICEServers[i].Credential
}

// turnCfgWith builds a one-server TURN config for the round-trip tests.
func turnCfgWith(urls []string, username, credential string, clear bool) Config {
	return Config{Provider: ProviderTURN, TURN: TURNProviderConfig{ICEServers: []ICEServer{
		{URLs: urls, Username: username, Credential: credential, ClearCredential: clear},
	}}}
}

var turnURLs = []string{"turn:relay.example.org:3478?transport=udp"}

// setUpStoredCredential establishes a saved TURN server with a credential and
// returns the temp dir it persists to.
func setUpStoredCredential(t *testing.T) string {
	t.Helper()
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := Set(turnCfgWith(turnURLs, "alice", "s3cr3t", false), true); err != nil {
		t.Fatalf("initial Set: %v", err)
	}
	if got := storedCredential(t, 0); got != "s3cr3t" {
		t.Fatalf("setup: stored credential = %q, want s3cr3t", got)
	}
	return dir
}

// DIRECTION 1 — an omitted credential must PRESERVE the stored one. This is
// the exact byte-for-byte shape Settings sends when the owner opens the panel
// and saves without touching the (necessarily empty) credential box.
func TestSet_OmittedCredential_PreservesStoredCredential(t *testing.T) {
	dir := setUpStoredCredential(t)

	// The re-save: same server, no credential, no clear flag.
	view, err := Set(turnCfgWith(turnURLs, "alice", "", false), true)
	if err != nil {
		t.Fatalf("re-Set: %v", err)
	}
	if !view.TURN.ICEServers[0].HasCredential {
		t.Error("HasCredential = false after a re-save that omitted the credential — " +
			"the box reported success while destroying the stored TURN secret")
	}
	if got := storedCredential(t, 0); got != "s3cr3t" {
		t.Fatalf("stored credential = %q after a re-save that omitted it, want s3cr3t preserved", got)
	}

	// And it must be preserved ON DISK, not just in memory — the in-memory
	// state is replaced wholesale on the next boot.
	mu.Lock()
	state = DefaultConfig()
	mu.Unlock()
	if err := Init(dir); err != nil {
		t.Fatalf("re-Init: %v", err)
	}
	if got := storedCredential(t, 0); got != "s3cr3t" {
		t.Fatalf("after reload, stored credential = %q, want s3cr3t", got)
	}
}

// DIRECTION 2 — an EXPLICIT clear must actually remove the credential. Without
// this, "always keep" would pass direction 1 forever and the owner would have
// no way to remove a credential at all.
func TestSet_ClearCredential_RemovesStoredCredential(t *testing.T) {
	dir := setUpStoredCredential(t)

	view, err := Set(turnCfgWith(turnURLs, "alice", "", true), true)
	if err != nil {
		t.Fatalf("clearing Set: %v", err)
	}
	if view.TURN.ICEServers[0].HasCredential {
		t.Error("HasCredential = true after an explicit clear_credential — the removal did not happen")
	}
	if got := storedCredential(t, 0); got != "" {
		t.Fatalf("stored credential = %q after an explicit clear, want it removed", got)
	}

	// Removed on disk too, and the clear flag itself must never be persisted:
	// it is a request verb, not state. A persisted clear_credential:true would
	// reload as a config that erases the next credential saved into it.
	blob, err := os.ReadFile(filepath.Join(dir, "relayconfig.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "s3cr3t") {
		t.Errorf("cleared credential is still in the persisted file: %s", blob)
	}
	if strings.Contains(string(blob), "clear_credential") {
		t.Errorf("clear_credential was persisted — it is a write-side verb, not state: %s", blob)
	}

	mu.Lock()
	state = DefaultConfig()
	mu.Unlock()
	if err := Init(dir); err != nil {
		t.Fatalf("re-Init: %v", err)
	}
	if got := storedCredential(t, 0); got != "" {
		t.Fatalf("after reload, stored credential = %q, want it stay removed", got)
	}
}

// A supplied credential still replaces the stored one — the merge must not
// have turned "keep" into "always keep".
func TestSet_SuppliedCredential_ReplacesStoredCredential(t *testing.T) {
	setUpStoredCredential(t)
	if _, err := Set(turnCfgWith(turnURLs, "alice", "rotated", false), true); err != nil {
		t.Fatalf("rotating Set: %v", err)
	}
	if got := storedCredential(t, 0); got != "rotated" {
		t.Fatalf("stored credential = %q, want the newly supplied 'rotated'", got)
	}
}

// A credential belongs to the host and account it was issued for. Editing the
// URL (or the username) makes it a DIFFERENT server, and the secret must not
// follow — carrying it would hand one operator's credential to another host on
// the next ICE handout.
func TestSet_CredentialNeverCarriesToADifferentServer(t *testing.T) {
	setUpStoredCredential(t)

	other := []string{"turn:someone-elses-relay.example.net:3478?transport=udp"}
	if _, err := Set(turnCfgWith(other, "alice", "", false), true); err != nil {
		t.Fatalf("Set with a different host: %v", err)
	}
	if got := storedCredential(t, 0); got != "" {
		t.Fatalf("credential %q was carried onto a DIFFERENT TURN host — a stored secret must "+
			"never follow an edited address", got)
	}

	// Same again for the username half of the identity.
	setUpStoredCredential(t)
	if _, err := Set(turnCfgWith(turnURLs, "bob", "", false), true); err != nil {
		t.Fatalf("Set with a different username: %v", err)
	}
	if got := storedCredential(t, 0); got != "" {
		t.Fatalf("credential %q was carried onto a different ACCOUNT on the same host", got)
	}
}

// Reordering a server's own URLs is not a different server: udp/tcp rows for
// one host are one entry however they are ordered, so the credential stays.
func TestSet_URLOrderDoesNotBreakCredentialPreservation(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	udp := "turn:relay.example.org:3478?transport=udp"
	tcp := "turn:relay.example.org:3478?transport=tcp"
	if _, err := Set(turnCfgWith([]string{udp, tcp}, "alice", "s3cr3t", false), true); err != nil {
		t.Fatalf("initial Set: %v", err)
	}
	if _, err := Set(turnCfgWith([]string{tcp, udp}, "alice", "", false), true); err != nil {
		t.Fatalf("re-Set with reordered URLs: %v", err)
	}
	if got := storedCredential(t, 0); got != "s3cr3t" {
		t.Fatalf("stored credential = %q after reordering the same server's URLs, want it preserved", got)
	}
}

// Position is NOT identity. Removing the first of two servers must not slide
// the second one's stored credential onto it: index-based matching is the
// obvious implementation of this merge and it is a credential-disclosure bug.
func TestSet_RemovingAServerDoesNotShiftCredentialsOntoAnother(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	first := []string{"turn:first.example.org:3478"}
	second := []string{"turn:second.example.org:3478"}
	seed := Config{Provider: ProviderTURN, TURN: TURNProviderConfig{ICEServers: []ICEServer{
		{URLs: first, Username: "u1", Credential: "first-secret"},
		{URLs: second, Username: "u2", Credential: "second-secret"},
	}}}
	if _, err := Set(seed, true); err != nil {
		t.Fatalf("seed Set: %v", err)
	}
	// Delete row 1 in Settings and save: one row left, at index 0, with an
	// empty credential box.
	if _, err := Set(turnCfgWith(second, "u2", "", false), true); err != nil {
		t.Fatalf("Set after removing the first server: %v", err)
	}
	if got := storedCredential(t, 0); got != "second-secret" {
		t.Fatalf("stored credential = %q, want second-secret — the surviving server must keep "+
			"ITS OWN credential, matched by identity and not by list position", got)
	}
}

// Two stored entries with the same identity are ambiguous. Declining to carry
// anything loses a credential the owner can re-enter; guessing could hand back
// the wrong secret, which they cannot detect.
func TestSet_AmbiguousDuplicateStoredServers_CarryNothing(t *testing.T) {
	resetState(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	dup := Config{Provider: ProviderTURN, TURN: TURNProviderConfig{ICEServers: []ICEServer{
		{URLs: turnURLs, Username: "alice", Credential: "one"},
		{URLs: turnURLs, Username: "alice", Credential: "two"},
	}}}
	if _, err := Set(dup, true); err != nil {
		t.Fatalf("seed Set: %v", err)
	}
	if _, err := Set(turnCfgWith(turnURLs, "alice", "", false), true); err != nil {
		t.Fatalf("re-Set: %v", err)
	}
	if got := storedCredential(t, 0); got != "" {
		t.Fatalf("stored credential = %q, want empty — an ambiguous match must not guess", got)
	}
}

// Both at once is a contradiction, and resolving it silently loses data
// whichever way it is read.
func TestSet_CredentialAndClearTogether_Rejected(t *testing.T) {
	setUpStoredCredential(t)
	_, err := Set(turnCfgWith(turnURLs, "alice", "new-one", true), true)
	if err == nil {
		t.Fatal("Set accepted both a credential and clear_credential")
	}
	if !strings.Contains(err.Error(), "clear_credential") {
		t.Errorf("error does not name the offending field: %v", err)
	}
	// Rejected means untouched, like every other invalid Set.
	if got := storedCredential(t, 0); got != "s3cr3t" {
		t.Fatalf("stored credential = %q after a REJECTED Set, want it untouched", got)
	}
}

// Reset means reset. The merge must not resurrect a credential into a config
// that deliberately carries no TURN servers at all.
func TestResetToDefault_DropsStoredCredential(t *testing.T) {
	setUpStoredCredential(t)
	if _, err := ResetToDefault(); err != nil {
		t.Fatalf("ResetToDefault: %v", err)
	}
	cfg := currentConfig()
	if len(cfg.TURN.ICEServers) != 0 {
		t.Fatalf("reset left %d ICE servers behind: %+v", len(cfg.TURN.ICEServers), cfg.TURN.ICEServers)
	}
}

// The TURN section (credential included) is preserved across a provider
// switch, exactly as this package's doc promises — a switch to libp2p and back
// must not be a way to lose the secret through the side door.
func TestSet_CredentialSurvivesAProviderSwitch(t *testing.T) {
	setUpStoredCredential(t)
	withLibp2p := Config{
		Provider: ProviderLibp2p,
		TURN:     TURNProviderConfig{ICEServers: []ICEServer{{URLs: turnURLs, Username: "alice"}}},
		Libp2p:   Libp2pProviderConfig{RelayPeers: []string{"/dns4/relay.example.org/tcp/4001/p2p/12D3KooWtest"}},
	}
	if _, err := Set(withLibp2p, true); err != nil {
		t.Fatalf("Set libp2p: %v", err)
	}
	if got := storedCredential(t, 0); got != "s3cr3t" {
		t.Fatalf("stored credential = %q after switching provider to libp2p, want it preserved", got)
	}
}
