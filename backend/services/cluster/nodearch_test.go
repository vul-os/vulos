package cluster

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"
)

// These tests deliberately exercise the REAL constructors, the REAL Health(),
// and the REAL NodeMeta struct tags.
//
// cluster_test.go's mockCluster re-implements register/peers rather than calling
// them, so a field added to NodeMeta and forgotten in Cluster.New would still
// pass every test in that file. That mock-vs-core divergence is this suite's
// documented dominant defect class, so nothing below goes through the mock.

// TestNodeMetaArchIsOnTheWire pins the JSON key. NodeMeta is serialised into S3
// at nodes/{id}/meta.json and read back by every other node — the tag IS the
// wire format. A rename or typo would not fail any compile or any struct-level
// assertion; peers would simply always see an empty arch, which decodes as
// "a node that predates the field" and is therefore silently benign-looking.
func TestNodeMetaArchIsOnTheWire(t *testing.T) {
	data, err := json.Marshal(NodeMeta{ID: "n1", Arch: "amd64"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"arch":"amd64"`) {
		t.Errorf("NodeMeta JSON missing `\"arch\":\"amd64\"`; got %s", data)
	}
}

// TestNodeMetaArchRoundTripsThroughTheSamePathPeersUses marshals and unmarshals
// exactly as Register (json.Marshal → PutEncrypted) and Peers (GetEncrypted →
// json.Unmarshal) do, so an arch written by one node is the arch another reads.
func TestNodeMetaArchRoundTripsThroughTheSamePathPeersUses(t *testing.T) {
	in := NodeMeta{
		ID:       "studio-box",
		Mode:     "server",
		Hostname: "studio-box",
		LastSeen: time.Now().UTC().Truncate(time.Second),
		Storage:  true,
		Arch:     "amd64",
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out NodeMeta
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Arch != in.Arch {
		t.Errorf("Arch round-trip = %q, want %q", out.Arch, in.Arch)
	}
}

// TestLegacyNodeMetaWithoutArchStillParses is the rollout-safety guarantee.
// A node running an older build writes a meta.json with no "arch" key. That
// object must still decode — an error here would make an upgraded node treat
// every not-yet-upgraded peer as unreadable and drop it from Peers() entirely
// (Peers skips objects that fail to unmarshal, silently), which would look like
// half the cluster vanishing.
func TestLegacyNodeMetaWithoutArchStillParses(t *testing.T) {
	legacy := `{"id":"old-node","mode":"local","hostname":"old","last_seen":"2026-08-01T00:00:00Z","storage":false}`
	var meta NodeMeta
	if err := json.Unmarshal([]byte(legacy), &meta); err != nil {
		t.Fatalf("legacy meta.json must still decode, got error: %v", err)
	}
	if meta.ID != "old-node" {
		t.Errorf("ID = %q, want old-node", meta.ID)
	}
	if meta.Arch != "" {
		t.Errorf("legacy node Arch = %q, want \"\" (unknown, not a guess)", meta.Arch)
	}
}

// TestNewDisabledAdvertisesArch covers the single-box install — the common case.
// newDisabled is the real constructor used whenever S3 is not configured.
func TestNewDisabledAdvertisesArch(t *testing.T) {
	c := newDisabled()
	if c.meta.Arch == "" {
		t.Fatal("newDisabled left Arch empty; a box always knows its own architecture")
	}
	if c.meta.Arch != runtime.GOARCH {
		t.Errorf("Arch = %q, want runtime.GOARCH %q", c.meta.Arch, runtime.GOARCH)
	}
}

// TestHealthReportsArchWhenDisabled exercises the real Health() disabled branch.
// Health is what an operator and the fleet UI read; an arch that exists on the
// struct but never reaches Health is invisible to everything that would use it.
func TestHealthReportsArchWhenDisabled(t *testing.T) {
	c := newDisabled()
	h := c.Health()
	got, ok := h["arch"]
	if !ok {
		t.Fatal(`Health() has no "arch" key`)
	}
	if got != runtime.GOARCH {
		t.Errorf(`Health()["arch"] = %v, want %q`, got, runtime.GOARCH)
	}
}

// TestNodeArchIsNotOverridableByEnvironment is a security property, not a
// nicety. Unlike appnet.BoxArch — whose VULOS_BOX_ARCH override never leaves the
// process that reads it — NodeMeta.Arch is REPLICATED to peers and surfaced to a
// user as a fact about a different machine ("Blender runs on studio-box, which
// is amd64"). If an environment variable could set it, any box could advertise
// an architecture it does not have and every peer would repeat the lie.
//
// The variables probed here are the plausible near-misses: the one appnet really
// uses, and the name someone would reach for when adding a test hook to this
// package.
func TestNodeArchIsNotOverridableByEnvironment(t *testing.T) {
	for _, key := range []string{"VULOS_BOX_ARCH", "VULOS_NODE_ARCH", "VULOS_ARCH"} {
		t.Setenv(key, "s390x")
	}
	if got := nodeArch(); got != runtime.GOARCH {
		t.Errorf("nodeArch() = %q with env overrides set, want %q — "+
			"a replicated capability claim must not be settable by configuration", got, runtime.GOARCH)
	}
	// And the constructor must not reintroduce it either.
	if c := newDisabled(); c.meta.Arch != runtime.GOARCH {
		t.Errorf("newDisabled().meta.Arch = %q with env overrides set, want %q",
			c.meta.Arch, runtime.GOARCH)
	}
}

// TestNodeArchIsDebianSpelledForShippedArches guards the coupling documented in
// nodearch.go: this package writes raw runtime.GOARCH, and consumers compare it
// against RegistryEntry.Arch, which is Debian-spelled. That is only safe while
// GOARCH and Debian agree for the architectures Vulos actually publishes. If a
// Vulos image is ever built for GOARCH "arm" (Debian "armhf") or "386" (Debian
// "i386"), raw comparison starts silently failing and this test is where that
// gets caught rather than in a user's App Hub.
func TestNodeArchIsDebianSpelledForShippedArches(t *testing.T) {
	debianSpelledSame := map[string]bool{"amd64": true, "arm64": true}
	got := nodeArch()
	if !debianSpelledSame[got] {
		t.Fatalf("this build's GOARCH is %q, which is NOT a spelling Debian shares. "+
			"NodeMeta.Arch is compared against RegistryEntry.Arch (Debian spelling) by "+
			"consumers; route both sides through appnet.NormalizeArch before shipping "+
			"a Vulos image for this architecture", got)
	}
}
