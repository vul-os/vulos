package peering

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What these constructors take is the VULOS DATA ROOT (datadir.Root() —
// $VULOS_DATA_DIR or ~/.vulos), NOT os.UserHomeDir().
//
// Every doc comment in this package used to say otherwise: they promised
// filepath.Join(home, ".vulos", "peering", …) while the code joins only
// "peering", and New's own comment said "home should be the value of
// os.UserHomeDir()". Production has always passed datadir.Root(), so the code
// was right and eight comments were wrong.
//
// The reason this is worth a test rather than a corrected sentence is what
// happens to a caller who believes the comment. New() calls loadOrGenerate on
// <root>/peering/identity, which MINTS A NEW Ed25519 KEYPAIR when it finds
// none. The failure is not a misplaced file — it is a box with a different Vulos
// ID, the name every peer knows it by, generated silently and reported by
// nothing. The identical mistake in identity.Load minted a new instance ULID.
//
// So the contract is pinned: exactly one "peering" segment, appended to what
// the caller passed, with no ".vulos" of its own.

func TestNewTreatsItsArgumentAsTheDataRoot(t *testing.T) {
	dataRoot := t.TempDir()
	svc := New(dataRoot)

	want := filepath.Join(dataRoot, "peering")
	if got := svc.Root(); got != want {
		t.Errorf("Root() = %q, want %q — the storage root must be one \"peering\" "+
			"segment below the data root", got, want)
	}

	// Home() must give back what was passed, unchanged. main.go feeds Home()
	// into NewContactStore/NewInboxStore/…, each of which appends "peering"
	// itself — so if Home() returned the peering directory instead of the data
	// root, every store would land in <root>/peering/peering.
	if got := svc.Home(); got != dataRoot {
		t.Errorf("Home() = %q, want the data root %q. The store constructors append "+
			"\"peering\" to this value, so returning the peering directory here would "+
			"double the segment for all of them", got, dataRoot)
	}

	// No ".vulos" is invented on top of what the caller gave. That segment
	// belongs to datadir.Root()'s own resolution, and adding it again here is
	// exactly what the old comments described.
	if strings.Contains(strings.TrimPrefix(svc.Root(), dataRoot), ".vulos") {
		t.Errorf("Root() = %q adds a \".vulos\" segment below the data root; that "+
			"segment belongs to datadir.Root(), not to paths built on top of it", svc.Root())
	}

	if _, err := os.Stat(want); err != nil {
		t.Errorf("New did not create %s: %v", want, err)
	}
}

// The stores take the same data root and each append "peering" themselves. If
// one of them ever grew a ".vulos" segment — or dropped its "peering" one — the
// tree would split in two and the split would be silent: the new location is
// simply empty, and an empty contact list looks exactly like a box with no
// contacts yet.
func TestStoresAgreeOnWhereTheTreeLives(t *testing.T) {
	// Each store gets a FRESH data root and New() is deliberately not called.
	//
	// The first version of this test called New(dataRoot) first and then checked
	// that contacts.json existed — but New creates contacts.json itself, so the
	// assertion was satisfied before NewContactStore ran at all. Pointing the
	// store at "peering2" did not fail it. A test that cannot fail is the exact
	// defect this file exists to prevent, so each store now has to build its own
	// tree from nothing.
	t.Run("contacts", func(t *testing.T) {
		dataRoot := t.TempDir()
		if _, err := NewContactStore(dataRoot); err != nil {
			t.Fatalf("NewContactStore: %v", err)
		}
		want := filepath.Join(dataRoot, "peering", "contacts.json")
		if _, err := os.Stat(want); err != nil {
			t.Errorf("NewContactStore did not write %s: %v — it disagrees with New "+
				"about where the peering tree is", want, err)
		}
	})

	t.Run("inbox", func(t *testing.T) {
		dataRoot := t.TempDir()
		if _, err := NewInboxStore(dataRoot); err != nil {
			t.Fatalf("NewInboxStore: %v", err)
		}
		want := filepath.Join(dataRoot, "peering", "inbox")
		if _, err := os.Stat(want); err != nil {
			t.Errorf("NewInboxStore did not create %s: %v", want, err)
		}
	})

	t.Run("groups", func(t *testing.T) {
		dataRoot := t.TempDir()
		if _, err := NewGroupStore(dataRoot); err != nil {
			t.Fatalf("NewGroupStore: %v", err)
		}
		want := filepath.Join(dataRoot, "peering", "groups")
		if _, err := os.Stat(want); err != nil {
			t.Errorf("NewGroupStore did not create %s: %v", want, err)
		}
	})
}
