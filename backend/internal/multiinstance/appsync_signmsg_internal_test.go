package multiinstance

// Byte-level guard on the changeset signing message (SYNC-APPS-01).
//
// This is an INTERNAL test on purpose. The round-trip test in the external
// package proves that this version can verify what this version signs, which is
// exactly the assurance a mixed-version fleet does not have: both sides there
// run the same function, so a change that rewrote the message for everyone would
// pass it unchanged. The only way to check compatibility with a version that is
// not in the process is to pin the bytes.

import (
	"strings"
	"testing"
	"time"
)

// TestSigningMessageIsByteIdenticalForPreDesireChangesets pins the exact message
// for a changeset a box predating the fleet desired set could have produced.
//
// A fleet is upgraded one box at a time. If the message changed for these rows,
// the first upgraded box would fail to verify every other box's signed uninstall
// observations — they would silently stop counting toward quorum, and removals
// would stop converging with nothing in any log naming the cause.
func TestSigningMessageIsByteIdenticalForPreDesireChangesets(t *testing.T) {
	ts := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	entries := []AppRegistryEntry{
		// An install: excluded from the message (not quorum-gated).
		{InstanceULID: "01HWZMINST000000000000001A", AppID: "notes", Installed: true, UpdatedAt: ts},
		// Two uninstalls: the signed observations.
		{InstanceULID: "01HWZMINST000000000000001A", AppID: "browser", Installed: false, UpdatedAt: ts},
		{InstanceULID: "01HWZMINST000000000000001B", AppID: "steam", Installed: false, UpdatedAt: ts},
	}

	stamp := ts.UTC().Format(time.RFC3339Nano)
	want := strings.Join([]string{
		"vulos:appsync:uninstall-observation:v1",
		"01HWZMORIGIN0000000000000A",
		"01HWZMINST000000000000001A", "browser", stamp,
		"01HWZMINST000000000000001B", "steam", stamp,
	}, "\x00")

	got := string(changesetSigningMessage("01HWZMORIGIN0000000000000A", entries))
	if got != want {
		t.Errorf("the signing message for legacy-shaped entries CHANGED — every signature a box predating "+
			"the fleet desired set produced now fails to verify.\n got: %q\nwant: %q", got, want)
	}
}

// TestSigningMessageCoversDesireRowsOfBothPolarities is the other half: the new
// section must actually be there, and must distinguish "wanted" from "removed".
//
// If desired=true rows were left out (the natural mirror of "installs are not
// signed"), a peer could inject an unauthenticated fleet-wide INSTALL — a
// strictly worse primitive than the remote uninstall the quorum was built to
// stop. If the polarity were left out, a signature over "install steam" would
// also authenticate "remove steam".
func TestSigningMessageCoversDesireRowsOfBothPolarities(t *testing.T) {
	ts := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	const origin = "01HWZMORIGIN0000000000000A"

	want := string(changesetSigningMessage(origin, []AppRegistryEntry{
		{InstanceULID: DesiredSetULID, AppID: "steam", Installed: true, UpdatedAt: ts},
	}))
	removed := string(changesetSigningMessage(origin, []AppRegistryEntry{
		{InstanceULID: DesiredSetULID, AppID: "steam", Installed: false, UpdatedAt: ts},
	}))
	none := string(changesetSigningMessage(origin, nil))

	if want == none {
		t.Error("a changeset carrying a desired=true row signs the same bytes as an EMPTY one — " +
			"the signature does not cover fleet installs, which makes desired=true an unauthenticated remote-install primitive")
	}
	if want == removed {
		t.Error("desired=true and desired=false sign the same bytes — a signature authorising an install also authorises the removal")
	}
	if !strings.Contains(want, "vulos:appsync:fleet-desire:v1") {
		t.Errorf("the desire section tag is missing from the message: %q", want)
	}
}

// TestSigningMessageIsIndependentOfSliceOrder covers the property both sections
// need to be usable at all: the emitter and the receiver iterate the same rows
// in whatever order the transport produced them.
func TestSigningMessageIsIndependentOfSliceOrder(t *testing.T) {
	ts := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	const origin = "01HWZMORIGIN0000000000000A"
	a := AppRegistryEntry{InstanceULID: DesiredSetULID, AppID: "steam", Installed: true, UpdatedAt: ts}
	b := AppRegistryEntry{InstanceULID: DesiredSetULID, AppID: "notes", Installed: false, UpdatedAt: ts}
	c := AppRegistryEntry{InstanceULID: "01HWZMINST000000000000001A", AppID: "browser", Installed: false, UpdatedAt: ts}

	fwd := string(changesetSigningMessage(origin, []AppRegistryEntry{a, b, c}))
	rev := string(changesetSigningMessage(origin, []AppRegistryEntry{c, b, a}))
	if fwd != rev {
		t.Errorf("the signing message depends on slice order, so a receiver that iterates differently cannot verify:\n%q\n%q", fwd, rev)
	}
}
