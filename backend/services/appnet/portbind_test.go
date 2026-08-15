package appnet

// portbind_test.go — PORTBIND-01 gate.
//
// The defect: every bundled app declares `"port": 80`, and the launcher runs it
// as `ip netns exec <ns> setpriv --reuid=65534 ... sh -c "python3 server.py"`.
// Dropping to nobody clears the process's capabilities, and a FRESH network
// namespace resets net.ipv4.ip_unprivileged_port_start to the kernel default of
// 1024 no matter what the host is set to. bind(0.0.0.0, 80) therefore returns
// EACCES and the app dies milliseconds after launch.
//
// Measured in a privileged linux/arm64 container (scripts/prove-portbind.sh):
//
//	container /proc/sys/.../ip_unprivileged_port_start  = 0     (Docker sets this)
//	`ip netns add` fresh namespace                       = 1024  (kernel default)
//	bind 80 as nobody inside that fresh namespace        = PermissionError EACCES
//	same bind after lowering the floor to 80             = OK
//
// This test is the cheap always-on half: it pins that namespace setup actually
// EMITS the step, on every platform, without root. The container script is the
// half that proves the step changes what the kernel allows.

import (
	"strings"
	"testing"
)

// stepFor returns the first step whose description matches, and whether one
// was found.
func stepFor(steps []nsStep, desc string) (nsStep, bool) {
	for _, s := range steps {
		if s.desc == desc {
			return s, true
		}
	}
	return nsStep{}, false
}

// TestPrivilegedAppPort_LowersTheUnprivilegedFloor is the founder-visible half
// of the launch bug: even once something finally CALLS launch, an app that
// declares port 80 cannot bind it and exits instantly, so the gateway proxies
// to nothing.
func TestPrivilegedAppPort_LowersTheUnprivilegedFloor(t *testing.T) {
	ns := &Namespace{
		Name: "vulos_calculator", AppID: "calculator", OwnerID: "u1",
		VethHost: "vh_a", VethNS: "vn_a",
		HostIP: "10.200.7.1", NSIP: "10.200.7.2",
		HostPort: 7070, AppPort: 80,
	}
	steps := namespaceSteps(ns)
	s, ok := stepFor(steps, "unprivileged port floor")
	if !ok {
		var descs []string
		for _, st := range steps {
			descs = append(descs, st.desc)
		}
		t.Fatalf("namespace setup for an app on port 80 emits no step that lets a "+
			"non-root process bind it. The app WILL exit with EACCES the moment it "+
			"calls bind(), and the gateway will proxy to a dead namespace.\nsteps: %v",
			descs)
	}
	joined := strings.Join(s.args, " ")
	if !strings.Contains(joined, "ip netns exec vulos_calculator") {
		t.Fatalf("the floor is not being lowered INSIDE the app's own namespace "+
			"(%q) — applying it anywhere else changes the host's policy, which is a "+
			"far wider change than this app needs", joined)
	}
	if !strings.Contains(joined, "net.ipv4.ip_unprivileged_port_start=80") {
		t.Fatalf("floor set to something other than the app's own declared port: %q", joined)
	}
}

// TestUnprivilegedAppPort_LeavesTheFloorAlone. An app that already asks for a
// high port needs no relaxation, and taking one anyway would widen the range
// for no reason.
func TestUnprivilegedAppPort_LeavesTheFloorAlone(t *testing.T) {
	ns := &Namespace{
		Name: "vulos_grafana", AppID: "grafana", OwnerID: "u1",
		VethHost: "vh_b", VethNS: "vn_b",
		HostIP: "10.200.8.1", NSIP: "10.200.8.2",
		HostPort: 7071, AppPort: 3000,
	}
	if s, ok := stepFor(namespaceSteps(ns), "unprivileged port floor"); ok {
		t.Fatalf("an app on port 3000 needs no privileged-port relaxation, but the "+
			"namespace takes one anyway: %v", s.args)
	}
}

// TestPortFloorRunsBeforeTheAppStarts. Every step in namespaceSteps executes
// inside Manager.Create, which returns before Launcher ever forks the process —
// this pins that the floor step is part of that sequence and not something
// applied later, when the app would already have failed to bind.
func TestPortFloorRunsBeforeTheAppStarts(t *testing.T) {
	ns := &Namespace{
		Name: "vulos_notes", AppID: "notes", OwnerID: "u1",
		VethHost: "vh_c", VethNS: "vn_c",
		HostIP: "10.200.9.1", NSIP: "10.200.9.2",
		HostPort: 7072, AppPort: 80,
	}
	steps := namespaceSteps(ns)
	createIdx, floorIdx := -1, -1
	for i, s := range steps {
		if s.desc == "create netns" {
			createIdx = i
		}
		if s.desc == "unprivileged port floor" {
			floorIdx = i
		}
	}
	if createIdx < 0 || floorIdx < 0 {
		t.Fatalf("missing steps: create=%d floor=%d", createIdx, floorIdx)
	}
	if floorIdx < createIdx {
		t.Fatalf("the floor is lowered at step %d, before the namespace exists at "+
			"step %d — the sysctl would apply to the wrong namespace", floorIdx, createIdx)
	}
}

// TestFloorStepIsNotBestEffort. namespaceSteps feeds a loop in Manager.Create
// that hard-fails on the first error. A floor that silently failed would leave
// the app unable to bind with no explanation anywhere — the exact shape of the
// original bug. This asserts the step is in that hard-failing list and not in
// the best-effort block Create runs afterwards for ip6tables.
func TestFloorStepIsNotBestEffort(t *testing.T) {
	ns := &Namespace{
		Name: "vulos_clock", AppID: "clock", OwnerID: "u1",
		VethHost: "vh_d", VethNS: "vn_d",
		HostIP: "10.200.10.1", NSIP: "10.200.10.2",
		HostPort: 7073, AppPort: 80,
	}
	if _, ok := stepFor(namespaceSteps(ns), "unprivileged port floor"); !ok {
		t.Fatal("the floor step is not part of namespaceSteps, so it is not covered " +
			"by Create's hard-failing step loop")
	}
}
