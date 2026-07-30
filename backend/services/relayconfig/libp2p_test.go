// no-broker-dep:allow-file: test comments/names use ProviderEphor (an internal package constant,
// not an import of anything external) to prove libp2p stays inert when a
// DIFFERENT provider is selected -- a negative test, not a dependency.

package relayconfig

// libp2p_test.go — hermetic unit tests for the optional embedded libp2p
// Circuit Relay v2 client host (libp2p_env.go/libp2p_limits.go/
// libp2p_host.go/libp2p_manager.go). None of these tests perform real
// dialing or DNS resolution: the "disabled" tests assert the manager never
// even reaches buildResourceManager/buildConnManager/libp2p.New, and the
// "limits" tests exercise buildResourceManager/buildConnManager/
// parseRelayPeers directly and in isolation — never via a constructed,
// running host — so no socket or background goroutine (autorelay's dial
// loop, in particular) is ever created here.

import (
	"testing"

	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
)

// testRelayPeer uses a REAL, well-formed libp2p peer ID (not a placeholder
// string) so parseRelayPeers — which round-trips through go-multiaddr/
// go-libp2p's actual multihash validation, unlike validate.go's deliberately
// simplified structural check — accepts it. relay.example.org itself is
// never dialed by these hermetic tests (see file doc).
const testRelayPeer = "/dns4/relay.example.org/tcp/4001/p2p/12D3KooWQCVLiM5Jng9CXua8J5DpQx4UZiZC4Gfh7ksnD3N7qqsx"

// --- Gate #2: the env opt-in (libp2p_env.go) ---

func TestLibp2pHostEnabledByEnv(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"anything-else", false},
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"  1  ", true}, // whitespace-trimmed
	}
	for _, c := range cases {
		t.Run("val="+c.val, func(t *testing.T) {
			t.Setenv(envLibp2pHostEnable, c.val)
			if got := libp2pHostEnabledByEnv(); got != c.want {
				t.Errorf("libp2pHostEnabledByEnv() with %s=%q = %v, want %v", envLibp2pHostEnable, c.val, got, c.want)
			}
		})
	}
}

// --- OFF BY DEFAULT: the manager must never construct a host unless BOTH
// gates are satisfied. These assert the whitebox invariant directly (the
// package-level host handle stays nil), which is only possible because the
// `!want` branch in ensureLibp2pManager returns before ever calling
// buildResourceManager/buildConnManager/newLibp2pReachabilityHost/libp2p.New. ---

func TestEnsureLibp2pManager_FreshBoxDefaults_NoHostEverStarts(t *testing.T) {
	resetLibp2pManagerForTest()
	t.Cleanup(resetLibp2pManagerForTest)
	// Do NOT set VULOS_LIBP2P_HOST_ENABLE at all (simulates a fresh box's
	// process environment) and use the package's actual zero-value default
	// config (provider=ephor, no libp2p section) — exactly what a box that
	// never touched Settings looks like.
	st := ensureLibp2pManager(DefaultConfig())
	if st.Running {
		t.Fatal("ensureLibp2pManager on a fresh/default config reported Running=true, want false")
	}
	if st.HostEnvEnabled {
		t.Fatal("HostEnvEnabled = true without the env var set, want false")
	}
	lp2pMgrMu.Lock()
	host := lp2pMgrHost
	lp2pMgrMu.Unlock()
	if host != nil {
		t.Fatal("a real libp2p host was constructed for the default/fresh config — off-by-default is broken")
	}
}

func TestEnsureLibp2pManager_ProviderSelectedButEnvUnset_StaysReportOnly(t *testing.T) {
	resetLibp2pManagerForTest()
	t.Cleanup(resetLibp2pManagerForTest)
	// Explicitly unset (simulates: operator selected "libp2p" in Settings,
	// i.e. gate #1 satisfied, but never flipped the box's own env var).
	t.Setenv(envLibp2pHostEnable, "")

	cfg := Config{Provider: ProviderLibp2p, Libp2p: Libp2pProviderConfig{RelayPeers: []string{testRelayPeer}}}
	st := ensureLibp2pManager(cfg)
	if st.Running {
		t.Fatal("host reported Running with the env gate unset — gate #2 was not enforced")
	}
	if st.NumPeers != 1 {
		t.Fatalf("NumPeers = %d, want 1 (config peers still reported even report-only)", st.NumPeers)
	}
	lp2pMgrMu.Lock()
	host := lp2pMgrHost
	lp2pMgrMu.Unlock()
	if host != nil {
		t.Fatal("a real libp2p host was constructed with the env gate unset — off-by-default is broken")
	}
}

func TestEnsureLibp2pManager_EnvEnabledButProviderNotSelected_NoHost(t *testing.T) {
	resetLibp2pManagerForTest()
	t.Cleanup(resetLibp2pManagerForTest)
	// The inverse: env var IS set, but the box's selected provider is still
	// ephor (gate #1 not satisfied) — must still be a no-op.
	t.Setenv(envLibp2pHostEnable, "1")
	st := ensureLibp2pManager(Config{Provider: ProviderEphor})
	if st.Running {
		t.Fatal("host reported Running while provider=ephor — gate #1 was not enforced")
	}
	lp2pMgrMu.Lock()
	host := lp2pMgrHost
	lp2pMgrMu.Unlock()
	if host != nil {
		t.Fatal("a real libp2p host was constructed while provider=ephor")
	}
}

func TestEnsureLibp2pManager_NoRelayPeersConfigured_NoHost(t *testing.T) {
	resetLibp2pManagerForTest()
	t.Cleanup(resetLibp2pManagerForTest)
	// Both gates nominally "on" (provider=libp2p, env set) but the relay
	// peer list is empty — nothing to build a host for, must stay a no-op.
	t.Setenv(envLibp2pHostEnable, "1")
	st := ensureLibp2pManager(Config{Provider: ProviderLibp2p})
	if st.Running {
		t.Fatal("host reported Running with zero configured relay peers")
	}
}

// --- Resource-manager / connection-manager limits are actually applied.
// These construct the REAL rcmgr.ResourceManager / connmgr.BasicConnMgr
// go-libp2p will use, but never a full host — no transport, no listener, no
// dialing, no goroutine that could ever touch the network. ---

func TestBuildResourceManager_LimitsApplied(t *testing.T) {
	concrete := libp2pResourceLimits().Build(rcmgr.DefaultLimits.AutoScale())
	partial := concrete.ToPartialLimitConfig()

	checks := []struct {
		name string
		got  rcmgr.LimitVal
		want int
	}{
		{"System.Conns", partial.System.Conns, libp2pMaxConns},
		{"System.ConnsInbound", partial.System.ConnsInbound, libp2pMaxConns},
		{"System.ConnsOutbound", partial.System.ConnsOutbound, libp2pMaxConns},
		{"System.Streams", partial.System.Streams, libp2pMaxStreams},
		{"System.StreamsInbound", partial.System.StreamsInbound, libp2pMaxStreams},
		{"System.StreamsOutbound", partial.System.StreamsOutbound, libp2pMaxStreams},
		{"System.FD", partial.System.FD, libp2pMaxFD},
		{"Transient.Conns", partial.Transient.Conns, libp2pMaxConns},
		{"Transient.Streams", partial.Transient.Streams, libp2pMaxStreams},
	}
	for _, c := range checks {
		if int(c.got) != c.want {
			t.Errorf("%s = %d, want %d (conservative fixed cap, never go-libp2p's unbounded default)", c.name, int(c.got), c.want)
		}
	}
	if int64(partial.System.Memory) != int64(libp2pMaxMemoryBytes) {
		t.Errorf("System.Memory = %d, want %d", int64(partial.System.Memory), int64(libp2pMaxMemoryBytes))
	}
	if int64(partial.Transient.Memory) != int64(libp2pMaxMemoryBytes) {
		t.Errorf("Transient.Memory = %d, want %d", int64(partial.Transient.Memory), int64(libp2pMaxMemoryBytes))
	}

	// buildResourceManager() itself must construct successfully from these
	// limits (this allocates the manager but starts no network I/O).
	rm, err := buildResourceManager()
	if err != nil {
		t.Fatalf("buildResourceManager: %v", err)
	}
	defer rm.Close()
}

func TestBuildConnManager_WatermarksApplied(t *testing.T) {
	cm, err := buildConnManager()
	if err != nil {
		t.Fatalf("buildConnManager: %v", err)
	}
	defer cm.Close()

	info := cm.GetInfo()
	if info.LowWater != libp2pConnsLowWater {
		t.Errorf("LowWater = %d, want %d", info.LowWater, libp2pConnsLowWater)
	}
	if info.HighWater != libp2pConnsHighWater {
		t.Errorf("HighWater = %d, want %d", info.HighWater, libp2pConnsHighWater)
	}
	if libp2pConnsLowWater >= libp2pConnsHighWater {
		t.Fatalf("sanity: low water (%d) must be < high water (%d)", libp2pConnsLowWater, libp2pConnsHighWater)
	}
	if libp2pConnsHighWater >= libp2pMaxConns {
		t.Fatalf("sanity: connmgr high water (%d) must stay below the resource manager's hard ceiling (%d), so trimming (not a rejected dial) is the normal path", libp2pConnsHighWater, libp2pMaxConns)
	}
}

// --- Multiaddr parsing (pure, no network) ---

func TestParseRelayPeers(t *testing.T) {
	valid := []string{testRelayPeer, "/ip4/1.2.3.4/tcp/4001/ipfs/12D3KooWBZPLacPvR1TGA3QSc2stf2omNCgYDXwo7QwFFRnEfSMQ"}
	infos, err := parseRelayPeers(valid)
	if err != nil {
		t.Fatalf("parseRelayPeers(valid): %v", err)
	}
	if len(infos) != len(valid) {
		t.Fatalf("parseRelayPeers(valid) returned %d infos, want %d", len(infos), len(valid))
	}

	badCases := [][]string{
		nil,
		{},
		{"not-a-multiaddr"},
		{"/dns4/relay.example.org/tcp/4001"}, // no /p2p component
	}
	for _, bad := range badCases {
		if _, err := parseRelayPeers(bad); err == nil {
			t.Errorf("parseRelayPeers(%v) = nil error, want an error", bad)
		}
	}
}

// --- Provider seam wiring: Ingress()/ResolvePeer() must reconcile without
// ever constructing a real host while the env gate is unset (the path
// exercised by every existing relayconfig_test.go libp2p test too). ---

func TestLibp2pProvider_Ingress_ReportOnlyWithoutEnvGate(t *testing.T) {
	resetLibp2pManagerForTest()
	t.Cleanup(resetLibp2pManagerForTest)
	t.Setenv(envLibp2pHostEnable, "")

	p := libp2pProvider{cfg: Libp2pProviderConfig{RelayPeers: []string{testRelayPeer}}}
	ing := p.Ingress()
	if ing.Mode != "libp2p-circuit-relay" {
		t.Fatalf("Ingress().Mode = %q, want libp2p-circuit-relay", ing.Mode)
	}
	lp2pMgrMu.Lock()
	host := lp2pMgrHost
	lp2pMgrMu.Unlock()
	if host != nil {
		t.Fatal("libp2pProvider.Ingress() started a real host without the env gate set")
	}

	if _, ok := p.ResolvePeer(bg, "some-peer"); ok {
		t.Fatal("libp2pProvider.ResolvePeer() reported ok — resolution is not implemented, must stay false")
	}
}
