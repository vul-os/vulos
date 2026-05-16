package appnet

import (
	"testing"
)

// net02TestManager builds an in-memory Manager pre-populated with namespaces.
// It bypasses the real Linux networking so tests run without root / iproute2.
func net02TestManager(namespaces map[string]*Namespace) *Manager {
	m := NewManager()
	m.namespaces = namespaces
	return m
}

// net02NS is a quick namespace builder for test fixtures.
func net02NS(appID, ownerID string) *Namespace {
	return &Namespace{
		Name:    "vulos_" + appID,
		AppID:   appID,
		OwnerID: ownerID,
		Active:  true,
	}
}

// --- net02ProfileKey ---

func TestNET02ProfileKey_DefaultProfile(t *testing.T) {
	key := net02ProfileKey("default", "calculator")
	if key != "calculator" {
		t.Errorf("expected bare appID for default profile, got %q", key)
	}
}

func TestNET02ProfileKey_EmptyProfile(t *testing.T) {
	key := net02ProfileKey("", "calculator")
	if key != "calculator" {
		t.Errorf("expected bare appID for empty profile, got %q", key)
	}
}

func TestNET02ProfileKey_NonDefaultProfile(t *testing.T) {
	key := net02ProfileKey("work", "calculator")
	if key != "work:calculator" {
		t.Errorf("expected 'work:calculator', got %q", key)
	}
}

// --- GetForProfile ---

func TestNET02GetForProfile_DefaultProfileFindsExistingNamespace(t *testing.T) {
	// Simulate a namespace created with the pre-NET-02 key format: userID-appID
	m := net02TestManager(map[string]*Namespace{
		"user1-calculator": net02NS("user1-calculator", "user1"),
	})

	ns, ok := m.GetForProfile("calculator", "user1", "default")
	if !ok {
		t.Fatal("expected to find namespace for default profile")
	}
	if ns.OwnerID != "user1" {
		t.Errorf("wrong owner: got %q", ns.OwnerID)
	}
}

func TestNET02GetForProfile_NonDefaultProfileFindsIsolatedNamespace(t *testing.T) {
	// "work" profile key is userID + "-" + "work:calculator"
	m := net02TestManager(map[string]*Namespace{
		"user1-work:calculator": net02NS("user1-work:calculator", "user1"),
	})

	ns, ok := m.GetForProfile("calculator", "user1", "work")
	if !ok {
		t.Fatal("expected to find namespace for 'work' profile")
	}
	_ = ns
}

func TestNET02GetForProfile_ProfilesAreIsolated(t *testing.T) {
	// Same appID, same user, two profiles — they must resolve independently.
	nsDefault := net02NS("user1-calculator", "user1")
	nsWork := net02NS("user1-work:calculator", "user1")

	m := net02TestManager(map[string]*Namespace{
		"user1-calculator":      nsDefault,
		"user1-work:calculator": nsWork,
	})

	gotDefault, okDefault := m.GetForProfile("calculator", "user1", "default")
	gotWork, okWork := m.GetForProfile("calculator", "user1", "work")

	if !okDefault {
		t.Fatal("default profile lookup failed")
	}
	if !okWork {
		t.Fatal("work profile lookup failed")
	}
	if gotDefault == gotWork {
		t.Error("default and work profile returned the same namespace — isolation broken")
	}
	if gotDefault.AppID != "user1-calculator" {
		t.Errorf("default: wrong AppID %q", gotDefault.AppID)
	}
	if gotWork.AppID != "user1-work:calculator" {
		t.Errorf("work: wrong AppID %q", gotWork.AppID)
	}
}

func TestNET02GetForProfile_MissingProfileReturnsNotFound(t *testing.T) {
	m := net02TestManager(map[string]*Namespace{
		"user1-calculator": net02NS("user1-calculator", "user1"),
	})

	_, ok := m.GetForProfile("calculator", "user1", "personal")
	if ok {
		t.Error("expected not-found for unknown profile, got found")
	}
}

// --- Backwards-compat: GetForUser delegates to GetForProfile("default") ---

func TestNET02GetForUser_BackwardsCompat(t *testing.T) {
	m := net02TestManager(map[string]*Namespace{
		"user1-calculator": net02NS("user1-calculator", "user1"),
	})

	// GetForUser must still work identically to before NET-02.
	ns, ok := m.GetForUser("calculator", "user1")
	if !ok {
		t.Fatal("GetForUser backwards-compat broken")
	}
	if ns.OwnerID != "user1" {
		t.Errorf("wrong owner: got %q", ns.OwnerID)
	}
}

// --- ParseSubdomain ---

func TestNET02ParseSubdomain_PlainAppID(t *testing.T) {
	app, profile, ok := ParseSubdomain("calculator.vulos.example.com", "vulos.example.com")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if app != "calculator" {
		t.Errorf("app: want %q got %q", "calculator", app)
	}
	if profile != "default" {
		t.Errorf("profile: want %q got %q", "default", profile)
	}
}

func TestNET02ParseSubdomain_ProfilePrefix(t *testing.T) {
	app, profile, ok := ParseSubdomain("work--calculator.vulos.example.com", "vulos.example.com")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if app != "calculator" {
		t.Errorf("app: want %q got %q", "calculator", app)
	}
	if profile != "work" {
		t.Errorf("profile: want %q got %q", "work", profile)
	}
}

func TestNET02ParseSubdomain_NoMatch(t *testing.T) {
	_, _, ok := ParseSubdomain("other.example.com", "vulos.example.com")
	if ok {
		t.Error("expected ok=false for non-matching domain")
	}
}

func TestNET02ParseSubdomain_StripPort(t *testing.T) {
	app, profile, ok := ParseSubdomain("calculator.vulos.example.com:8080", "vulos.example.com")
	if !ok {
		t.Fatal("expected ok=true with port")
	}
	if app != "calculator" {
		t.Errorf("app: want %q got %q", "calculator", app)
	}
	if profile != "default" {
		t.Errorf("profile: want %q got %q", "default", profile)
	}
}
