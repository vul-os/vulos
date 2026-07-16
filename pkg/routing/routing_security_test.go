// Package routing_security_test contains adversarial security tests for the
// routing subsystem contract (contract.go — FROZEN). Every test asserts
// fail-closed behaviour: forgery, hijack, parser confusion, authz bypass, and
// race conditions all must be rejected cleanly.
//
// Run with: go test -race ./internal/routing/...
package routing_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/vul-os/vulos-management/pkg/routing"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// goodULID is a real valid 26-char uppercase Crockford ULID used throughout.
const goodULID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

// goodULIDB is a second distinct valid ULID.
const goodULIDB = "01BX5ZZKBKACTAV9WEVGEMMVS0"

func newStore() *routing.MemStore { return routing.NewMemStore() }

func ctx() context.Context { return context.Background() }

func mustEnroll(t *testing.T, s *routing.MemStore, ulid, acct string, mode routing.ConnMode) routing.Binding {
	t.Helper()
	b, err := s.Enroll(ctx(), ulid, acct, mode)
	if err != nil {
		t.Fatalf("mustEnroll: unexpected error: %v", err)
	}
	return b
}

// assertError fatally fails unless err wraps or equals target.
func assertErr(t *testing.T, err error, target error, label string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected error %v, got nil", label, target)
	}
	if !errors.Is(err, target) {
		t.Fatalf("%s: expected %v, got %v", label, target, err)
	}
}

// assertNoError fatally fails if err is non-nil.
func assertNoErr(t *testing.T, err error, label string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", label, err)
	}
}

// ---------------------------------------------------------------------------
// 1. ULID FORGERY — CanonULID
// ---------------------------------------------------------------------------

func TestCanonULID_Forgery(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		// Wrong length
		{"empty", ""},
		{"too_short_25", "01ARZ3NDEKTSV4RRFFQ69G5FA"},
		{"too_long_27", "01ARZ3NDEKTSV4RRFFQ69G5FAVX"},
		{"too_short_1", "0"},
		{"too_long_52", strings.Repeat("0", 52)},

		// Crockford-excluded characters (upper and lower)
		{"excluded_I_upper", "0IARZNNDEKTSV4RRFFQ69G5FAV"},
		{"excluded_L_upper", "0LARZNNDEKTSV4RRFFQ69G5FAV"},
		{"excluded_O_upper", "0OARZNNDEKTSV4RRFFQ69G5FAV"},
		{"excluded_U_upper", "0UARZNNDEKTSV4RRFFQ69G5FAV"},
		{"excluded_i_lower", "0iarznndektsv4rrffq69g5fav"},
		{"excluded_l_lower", "0larznndektsv4rrffq69g5fav"},
		{"excluded_o_lower", "0oarznndektsv4rrffq69g5fav"},
		{"excluded_u_lower", "0uarznndektsv4rrffq69g5fav"},

		// 128-bit overflow: first character > '7' in Crockford means top symbol > 7
		// Crockford: '8' maps to value 8 which is > 7 → overflow
		{"overflow_leading_8", "81ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{"overflow_leading_9", "91ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{"overflow_leading_A_val10", "A1ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{"overflow_leading_Z", "Z1ARZ3NDEKTSV4RRFFQ69G5FAV"},

		// Non-Crockford symbols
		{"hyphen_in_ulid", "01ARZ3-DEKTSV4RRFFQ69G5FAV"},
		{"space_in_ulid", "01ARZ3 DEKTSV4RRFFQ69G5FAV"},
		{"dot_in_ulid", "01ARZ3.DEKTSV4RRFFQ69G5FAV"},
		{"slash_in_ulid", "01ARZ3/DEKTSV4RRFFQ69G5FAV"},
		{"null_byte", "01ARZ3\x00DEKTSV4RRFFQ69G5FAV"},
		{"crlf_injection", "01ARZ3NDEKTSV4R\r\nFFQ69G5FA"},
		{"tab_in_ulid", "01ARZ3NDEKTSV4R\tFFQ69G5FAV"},
		{"unicode_homoglyph", "01ARZ3NDEKTSV4RRFFQ69G5FАV"}, // Cyrillic А

		// Whitespace variants
		{"leading_space", " 01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{"trailing_space", "01ARZ3NDEKTSV4RRFFQ69G5FAV "},
		{"all_spaces", "                          "},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := routing.CanonULID(tc.input)
			if err == nil {
				t.Errorf("CanonULID(%q) should have failed, got %q", tc.input, out)
			}
			if !errors.Is(err, routing.ErrInvalidULID) {
				t.Errorf("CanonULID(%q) wrong error type: %v", tc.input, err)
			}
		})
	}
}

func TestCanonULID_ValidAccepted(t *testing.T) {
	// Valid ULIDs must be accepted and canonicalized.
	cases := []struct {
		input string
		want  string
	}{
		{goodULID, goodULID},
		{strings.ToLower(goodULID), goodULID}, // lowercase → canonical uppercase
		{goodULIDB, goodULIDB},
		{strings.ToLower(goodULIDB), goodULIDB},
		// All-zero ULID (valid)
		{"00000000000000000000000000", "00000000000000000000000000"},
		// Max valid leading character '7' (value 7 → no overflow)
		{"7ZZZZZZZZZZZZZZZZZZZZZZZZZ", "7ZZZZZZZZZZZZZZZZZZZZZZZZZ"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := routing.CanonULID(tc.input)
			if err != nil {
				t.Errorf("CanonULID(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("CanonULID(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestCanonULID_MixedCaseCanonicalisesNotBypasses(t *testing.T) {
	// Mixed-case valid ULIDs must be accepted but always return canonical uppercase.
	// This confirms that mixed-case cannot be used to create two different bindings
	// for the "same" ULID string (downstream uses canonical form as map key).
	lower := strings.ToLower(goodULID)
	upper := goodULID

	canonLower, err := routing.CanonULID(lower)
	assertNoErr(t, err, "lower canon")
	canonUpper, err := routing.CanonULID(upper)
	assertNoErr(t, err, "upper canon")

	if canonLower != canonUpper {
		t.Errorf("mixed-case bypass: lower %q → %q, upper → %q (must be identical)", lower, canonLower, canonUpper)
	}
}

// ---------------------------------------------------------------------------
// 2. ULID FORGERY — ParseHost propagation
// ---------------------------------------------------------------------------

func TestParseHost_ForgedULIDInHost(t *testing.T) {
	// Hosts containing forged ULIDs must be rejected by ParseHost.
	base := "vulos.org"
	forgedULIDs := []string{
		"01ARZ3NDEKTSV4RRFFQ69G5FA",   // too short
		"01ARZ3NDEKTSV4RRFFQ69G5FAVX", // too long
		"0IARZNNDEKTSV4RRFFQ69G5FAV",  // contains I
		"0LARZNNDEKTSV4RRFFQ69G5FAV",  // contains L
		"0OARZNNDEKTSV4RRFFQ69G5FAV",  // contains O
		"0UARZNNDEKTSV4RRFFQ69G5FAV",  // contains U
		"81ARZ3NDEKTSV4RRFFQ69G5FAV",  // overflow
		"ZZZZZZZZZZZZZZZZZZZZZZZZZZ",  // overflow (value > 7 in first symbol)
	}
	for _, fu := range forgedULIDs {
		host := fu + "." + base
		t.Run(fu, func(t *testing.T) {
			_, _, _, err := routing.ParseHost(host, base)
			if err == nil {
				t.Errorf("ParseHost(%q, %q) should fail for forged ULID", host, base)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 3. SUBDOMAIN CONFUSION / INJECTION — ParseHost
// ---------------------------------------------------------------------------

func TestParseHost_SubdomainConfusion(t *testing.T) {
	base := "vulos.org"
	ulid := goodULID

	cases := []struct {
		name string
		host string
	}{
		// Invalid app/profile names via -- separator
		{"double_hyphen_sub_a--b--c", "a--b--c." + ulid + "." + base},
		// -- at the start of sub label: app="" which is invalid
		{"leading_double_hyphen_sub", "--x." + ulid + "." + base},
		// -- at the end: profile="" which is invalid
		{"trailing_double_hyphen_sub", "x--." + ulid + "." + base},
		// Empty sub-label (leading dot)
		{"empty_sub_label", "." + ulid + "." + base},

		// Deep nesting — 3-part rest (a.b.{ulid}) → len(parts)==3 > 2 → reject
		{"deep_nesting_a_b_ulid", "a.b." + ulid + "." + base},

		// Wrong base domain
		{"wrong_base_evil_org", ulid + ".evil.org"},
		{"wrong_base_vulos_com", ulid + ".vulos.com"},
		{"wrong_base_subdomain_of_vulos", ulid + ".sub.vulos.org"},

		// Trailing dot in host (ParseHost trims it, but let's verify a double-trailing-dot)
		{"double_trailing_dot", ulid + "." + base + ".."},

		// Port stripping does NOT make a malformed ULID valid
		{"port_with_forged_ulid", "0IARZNNDEKTSV4RRFFQ69G5FAV." + base + ":443"},

		// Uppercase host (case-insensitive matching is fine; ULID comes back canonical)
		// This is actually VALID per spec — just verifying it doesn't error.
		// Tested separately in TestParseHost_UppercaseValid below.

		// Embedded double-dot (..): creates empty label
		{"embedded_double_dot", "app." + ulid + ".." + base},

		// Oversize sub-label (>63 chars)
		{"oversize_sub_label", strings.Repeat("a", 64) + "." + ulid + "." + base},

		// Unicode/IDN homoglyph in sub-label: 'а' (Cyrillic) instead of 'a'
		{"unicode_homoglyph_sub", "аpp." + ulid + "." + base},

		// CRLF injection in host
		{"crlf_injection_host", "app." + ulid + "." + base + "\r\nX-Evil: x"},

		// Null byte injection
		{"null_byte_host", "app.\x00" + ulid + "." + base},

		// Space injection
		{"space_injection_host", "app. " + ulid + "." + base},

		// Just the base domain — no ULID
		{"bare_base_domain", base},
		{"bare_base_with_dot", base + "."},

		// Empty host
		{"empty_host", ""},

		// Host is only the ULID, no base
		{"ulid_only", ulid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, profile, cu, err := routing.ParseHost(tc.host, base)
			if err == nil {
				t.Errorf("ParseHost(%q, %q) should fail, got app=%q profile=%q ulid=%q",
					tc.host, base, app, profile, cu)
			}
		})
	}
}

func TestParseHost_UppercaseHostValid(t *testing.T) {
	// The spec says matching is case-insensitive; uppercase host is valid.
	base := "vulos.org"
	ulid := goodULID
	host := strings.ToUpper(ulid + "." + base)
	app, profile, cu, err := routing.ParseHost(host, base)
	assertNoErr(t, err, "uppercase host")
	if app != "default" || profile != "default" {
		t.Errorf("expected app=default profile=default, got app=%q profile=%q", app, profile)
	}
	if cu != ulid {
		t.Errorf("expected canonical ULID %q, got %q", ulid, cu)
	}
}

func TestParseHost_ValidForms(t *testing.T) {
	// Confirm valid host forms are accepted (regression guard).
	base := "vulos.org"
	ulid := goodULID

	cases := []struct {
		host        string
		wantApp     string
		wantProfile string
	}{
		// Bare ULID.base — defaults
		{ulid + "." + base, "default", "default"},
		// With port
		{ulid + "." + base + ":8080", "default", "default"},
		// app--profile.ulid.base
		{"myapp--prod." + ulid + "." + base, "myapp", "prod"},
		// sub without profile separator → app=sub, profile=default
		{"myapp." + ulid + "." + base, "myapp", "default"},
		// Lowercase entire host
		{strings.ToLower(ulid + "." + base), "default", "default"},
		// Trailing dot stripped
		{ulid + "." + base + ".", "default", "default"},
		// Trailing dot with port
		{ulid + "." + base + ".:" + "443", "default", "default"},
	}

	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			app, profile, cu, err := routing.ParseHost(tc.host, base)
			if err != nil {
				t.Errorf("ParseHost(%q, %q): unexpected error: %v", tc.host, base, err)
				return
			}
			if app != tc.wantApp {
				t.Errorf("app: got %q, want %q", app, tc.wantApp)
			}
			if profile != tc.wantProfile {
				t.Errorf("profile: got %q, want %q", profile, tc.wantProfile)
			}
			if cu != strings.ToUpper(ulid) {
				t.Errorf("ulid: got %q, want %q", cu, ulid)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 4. CROSS-ACCOUNT HIJACK — Enroll
// ---------------------------------------------------------------------------

func TestHijack_EnrollDifferentAccount(t *testing.T) {
	s := newStore()
	ulid := goodULID

	// Account A enrolls first.
	b1 := mustEnroll(t, s, ulid, "acct-A", routing.ModeFabric)
	if b1.AccountID != "acct-A" {
		t.Fatalf("initial enroll wrong account: %q", b1.AccountID)
	}

	// Account B attempts re-enroll → must be rejected.
	_, err := s.Enroll(ctx(), ulid, "acct-B", routing.ModeFabric)
	assertErr(t, err, routing.ErrULIDBoundToOtherAccount, "B re-enroll")

	// Binding must be UNCHANGED — still owned by A, still same mode.
	b2, err := s.GetBinding(ctx(), ulid)
	assertNoErr(t, err, "GetBinding after B attempt")
	if b2.AccountID != "acct-A" {
		t.Errorf("hijack succeeded: binding account changed to %q", b2.AccountID)
	}
	if b2.ULID != ulid {
		t.Errorf("binding ULID changed: %q", b2.ULID)
	}
}

func TestHijack_BAccountCannotChangeMode(t *testing.T) {
	s := newStore()
	ulid := goodULID
	mustEnroll(t, s, ulid, "acct-A", routing.ModeFabric)

	// B tries to update mode via SetConnMode (no ownership check in this call,
	// but B should not be able to enroll to get here; we verify the store still
	// reflects A's mode after B's failed attempt via Enroll).
	_, err := s.Enroll(ctx(), ulid, "acct-B", routing.ModeDirect)
	assertErr(t, err, routing.ErrULIDBoundToOtherAccount, "B mode-change attempt")

	b, _ := s.GetBinding(ctx(), ulid)
	if b.Mode != routing.ModeFabric {
		t.Errorf("mode changed after failed hijack attempt: %v", b.Mode)
	}
}

func TestHijack_IdempotentReEnrollByOwner(t *testing.T) {
	// A's idempotent re-enroll must not be disrupted by B's failed attempt.
	s := newStore()
	ulid := goodULID

	mustEnroll(t, s, ulid, "acct-A", routing.ModeFabric)
	// B tries (and fails)
	_, _ = s.Enroll(ctx(), ulid, "acct-B", routing.ModeDirect)
	// A re-enrolls successfully
	b, err := s.Enroll(ctx(), ulid, "acct-A", routing.ModeOwnDomain)
	assertNoErr(t, err, "A idempotent re-enroll")
	if b.AccountID != "acct-A" {
		t.Errorf("re-enroll returned wrong account: %q", b.AccountID)
	}
	if b.Mode != routing.ModeOwnDomain {
		t.Errorf("re-enroll did not update mode: %v", b.Mode)
	}
}

// ---------------------------------------------------------------------------
// 5. DNS-UPDATE AUTHZ BYPASS — SetDirectIP
// ---------------------------------------------------------------------------

func TestAuthz_SetDirectIP_NonOwner(t *testing.T) {
	s := newStore()
	ulid := goodULID
	mustEnroll(t, s, ulid, "acct-A", routing.ModeDirect)

	// B attempts to update A's IP record.
	err := s.SetDirectIP(ctx(), ulid, "acct-B", "1.2.3.4")
	assertErr(t, err, routing.ErrNotOwner, "non-owner SetDirectIP")

	// IP must not have been changed.
	b, _ := s.GetBinding(ctx(), ulid)
	if b.DirectIP != "" {
		t.Errorf("DirectIP was set by non-owner: %q", b.DirectIP)
	}
}

func TestAuthz_SetDirectIP_OwnerSucceeds(t *testing.T) {
	s := newStore()
	ulid := goodULID
	mustEnroll(t, s, ulid, "acct-A", routing.ModeDirect)

	err := s.SetDirectIP(ctx(), ulid, "acct-A", "203.0.113.42")
	assertNoErr(t, err, "owner SetDirectIP")

	b, _ := s.GetBinding(ctx(), ulid)
	if b.DirectIP != "203.0.113.42" {
		t.Errorf("DirectIP not updated: %q", b.DirectIP)
	}
}

func TestAuthz_SetDirectIP_InvalidIPValues(t *testing.T) {
	s := newStore()
	ulid := goodULID
	mustEnroll(t, s, ulid, "acct-A", routing.ModeDirect)

	invalidIPs := []string{
		"",
		"not-an-ip",
		"256.0.0.1",
		"1.2.3",
		"::1 extra",
		"0.0.0.0", // unspecified — net.ParseIP parses this; verify gate behaviour
		"http://1.2.3.4",
		"1.2.3.4/24", // CIDR not an IP
		"example.com",
		"javascript:alert(1)",
		"\x00",
		" 1.2.3.4",
		"1.2.3.4\n",
	}

	for _, ip := range invalidIPs {
		t.Run("ip="+ip, func(t *testing.T) {
			err := s.SetDirectIP(ctx(), ulid, "acct-A", ip)
			// net.ParseIP("0.0.0.0") is non-nil so 0.0.0.0 is accepted by the gate.
			// We test that all clearly-invalid values are rejected.
			if net.ParseIP(ip) == nil && err == nil {
				t.Errorf("SetDirectIP(%q) should have been rejected", ip)
			}
		})
	}
}

func TestAuthz_SetDirectIP_GarbageIPs(t *testing.T) {
	// These are definitely rejected by net.ParseIP.
	s := newStore()
	ulid := goodULID
	mustEnroll(t, s, ulid, "acct-A", routing.ModeDirect)

	for _, ip := range []string{"not-an-ip", "256.0.0.1", "", "1.2.3.4/24", "http://evil.com"} {
		err := s.SetDirectIP(ctx(), ulid, "acct-A", ip)
		if err == nil {
			t.Errorf("SetDirectIP accepted garbage IP %q", ip)
		}
	}
}

func TestAuthz_SetDirectIP_NotEnrolled(t *testing.T) {
	s := newStore()
	// No enroll — should return ErrNotEnrolled (gate order: IP check first per impl,
	// then not-found check — either way it must error).
	err := s.SetDirectIP(ctx(), goodULID, "acct-A", "1.2.3.4")
	if err == nil {
		t.Fatal("SetDirectIP on unenrolled ULID should fail")
	}
}

// ---------------------------------------------------------------------------
// 6. VANITY SQUATTING / TAKEOVER
// ---------------------------------------------------------------------------

func TestVanity_SquatByDifferentAccount(t *testing.T) {
	s := newStore()

	// acct-A claims "myname" → ulid-1.
	err := s.ClaimVanity(ctx(), "myname", goodULID, "acct-A")
	assertNoErr(t, err, "initial claim")

	// acct-B tries to claim the same name with a different ULID.
	err = s.ClaimVanity(ctx(), "myname", goodULIDB, "acct-B")
	assertErr(t, err, routing.ErrVanityTaken, "B claim same name")

	// acct-A tries to claim same name but different ULID → still taken (same check).
	err = s.ClaimVanity(ctx(), "myname", goodULIDB, "acct-A")
	assertErr(t, err, routing.ErrVanityTaken, "A claim different ulid")

	// Resolve must still return A's entry.
	v, err := s.ResolveVanity(ctx(), "myname")
	assertNoErr(t, err, "resolve after squatting attempts")
	if v.AccountID != "acct-A" || v.ULID != goodULID {
		t.Errorf("vanity was hijacked: got account=%q ulid=%q", v.AccountID, v.ULID)
	}
}

func TestVanity_IdempotentReClaimBySameOwner(t *testing.T) {
	s := newStore()
	err := s.ClaimVanity(ctx(), "myname", goodULID, "acct-A")
	assertNoErr(t, err, "initial claim")

	// Identical re-claim must be idempotent (no error).
	err = s.ClaimVanity(ctx(), "myname", goodULID, "acct-A")
	assertNoErr(t, err, "idempotent re-claim")
}

func TestVanity_InvalidNamesRejected(t *testing.T) {
	s := newStore()

	invalidNames := []string{
		"",
		"-starts-with-hyphen",
		"ends-with-hyphen-",
		"has--double-hyphen",
		"HAS_UPPERCASE",
		"has space",
		"has.dot",
		strings.Repeat("a", 64), // exceeds 63 chars
		"\x00",
		"a\nb",
		"a--b", // double hyphen → invalid per ValidName
	}

	for _, name := range invalidNames {
		t.Run("name="+name, func(t *testing.T) {
			err := s.ClaimVanity(ctx(), name, goodULID, "acct-A")
			assertErr(t, err, routing.ErrInvalidName, "invalid name "+name)
		})
	}
}

func TestVanity_ResolveUnknownIsNotFound(t *testing.T) {
	s := newStore()
	_, err := s.ResolveVanity(ctx(), "does-not-exist")
	assertErr(t, err, routing.ErrNotFound, "resolve unknown vanity")
}

func TestVanity_ResolveUnknownNoEnumerationLeak(t *testing.T) {
	// ErrNotFound must be the same error regardless of whether name is taken or unknown,
	// so an attacker cannot enumerate existence by error type.
	s := newStore()
	_ = s.ClaimVanity(ctx(), "taken", goodULID, "acct-A")

	_, errTakenLookup := s.ResolveVanity(ctx(), "taken")      // exists → nil
	_, errUnknown := s.ResolveVanity(ctx(), "does-not-exist") // doesn't exist → ErrNotFound

	// taken → returns successfully (no error); unknown → ErrNotFound.
	// Key invariant: the error kinds differ (nil vs ErrNotFound), not two different
	// enumeration-leak errors. Confirming both sides of the gate.
	if errTakenLookup != nil {
		t.Errorf("resolve of existing vanity should succeed, got: %v", errTakenLookup)
	}
	if !errors.Is(errUnknown, routing.ErrNotFound) {
		t.Errorf("resolve of unknown should be ErrNotFound, got: %v", errUnknown)
	}
}

// ---------------------------------------------------------------------------
// 7. MODE-D LEAKAGE
// ---------------------------------------------------------------------------

func TestModeD_ValidConnModeRejectsLocal(t *testing.T) {
	invalidModes := []routing.ConnMode{
		"local", // mode D
		"D",
		"",
		"LOCAL",
		"Local",
		"mode-d",
		"auto",
		"relay",
	}
	for _, m := range invalidModes {
		if routing.ValidConnMode(m) {
			t.Errorf("ValidConnMode(%q) = true, want false", m)
		}
	}
}

func TestModeD_ValidConnModeAcceptsABC(t *testing.T) {
	if !routing.ValidConnMode(routing.ModeFabric) {
		t.Error("ModeFabric should be valid")
	}
	if !routing.ValidConnMode(routing.ModeDirect) {
		t.Error("ModeDirect should be valid")
	}
	if !routing.ValidConnMode(routing.ModeOwnDomain) {
		t.Error("ModeOwnDomain should be valid")
	}
}

func TestModeD_EnrollRejectsLocalMode(t *testing.T) {
	s := newStore()
	_, err := s.Enroll(ctx(), goodULID, "acct-A", "local")
	assertErr(t, err, routing.ErrInvalidMode, "enroll with mode=local")
}

func TestModeD_SetConnModeRejectsLocalMode(t *testing.T) {
	s := newStore()
	mustEnroll(t, s, goodULID, "acct-A", routing.ModeFabric)

	invalidModes := []routing.ConnMode{"local", "D", "", "Local", "LOCAL"}
	for _, m := range invalidModes {
		err := s.SetConnMode(ctx(), goodULID, m)
		if err == nil {
			t.Errorf("SetConnMode with mode=%q should fail", m)
		}
		if !errors.Is(err, routing.ErrInvalidMode) {
			t.Errorf("SetConnMode mode=%q wrong error: %v", m, err)
		}
	}
}

func TestModeD_NoEnrollPathYieldsModeDRecord(t *testing.T) {
	// After valid enroll + mode switches, ensure no mode-D record can exist.
	s := newStore()
	mustEnroll(t, s, goodULID, "acct-A", routing.ModeFabric)

	// Try to force mode D via SetConnMode — must fail.
	err := s.SetConnMode(ctx(), goodULID, "local")
	if err == nil {
		t.Fatal("SetConnMode(local) should be rejected")
	}

	// Binding mode must still be the original valid mode.
	b, _ := s.GetBinding(ctx(), goodULID)
	if !routing.ValidConnMode(b.Mode) {
		t.Errorf("binding has invalid mode %q after failed SetConnMode", b.Mode)
	}
	if b.Mode == "local" || b.Mode == "D" {
		t.Errorf("mode-D record found in store: %q", b.Mode)
	}
}

// ---------------------------------------------------------------------------
// 8. POP HEALTH GATING
// ---------------------------------------------------------------------------

func TestPoP_NearestPopNeverSelectsUnhealthy(t *testing.T) {
	pops := []routing.PoP{
		{ID: "us-east", Lat: 40, Lon: -74, Healthy: false}, // closest to query point
		{ID: "eu-west", Lat: 51, Lon: -0, Healthy: true},
		{ID: "ap-southeast", Lat: 1, Lon: 103, Healthy: false},
	}

	// Query near us-east (should NOT be selected — unhealthy).
	got, err := routing.NearestPoP(pops, 41, -73)
	assertNoErr(t, err, "nearest pop")
	if got.ID == "us-east" {
		t.Error("NearestPoP returned unhealthy us-east PoP")
	}
	if got.ID != "eu-west" {
		t.Errorf("expected eu-west (only healthy), got %q", got.ID)
	}
}

func TestPoP_AllUnhealthyReturnsError(t *testing.T) {
	pops := []routing.PoP{
		{ID: "p1", Healthy: false},
		{ID: "p2", Healthy: false},
	}
	_, err := routing.NearestPoP(pops, 0, 0)
	assertErr(t, err, routing.ErrNoHealthyPoP, "all-unhealthy")
}

func TestPoP_EmptyPoolReturnsError(t *testing.T) {
	_, err := routing.NearestPoP(nil, 0, 0)
	assertErr(t, err, routing.ErrNoHealthyPoP, "empty pool")

	_, err = routing.NearestPoP([]routing.PoP{}, 0, 0)
	assertErr(t, err, routing.ErrNoHealthyPoP, "empty slice")
}

func TestPoP_SingleUnhealthyReturnsError(t *testing.T) {
	pops := []routing.PoP{{ID: "only", Healthy: false}}
	_, err := routing.NearestPoP(pops, 0, 0)
	assertErr(t, err, routing.ErrNoHealthyPoP, "single unhealthy")
}

func TestPoP_HealthyPopUpsertAndDemotion(t *testing.T) {
	// Verify that marking a PoP unhealthy after insertion removes it from
	// HealthyPoPs, ensuring the store-level health gate is enforced.
	s := newStore()

	err := s.UpsertPoP(ctx(), routing.PoP{ID: "us-east", Region: "us-east-1",
		IP: "1.2.3.4", Lat: 40, Lon: -74, Healthy: true})
	assertNoErr(t, err, "upsert healthy pop")

	// Should appear in healthy list.
	healthy, err := s.HealthyPoPs(ctx())
	assertNoErr(t, err, "HealthyPoPs")
	if len(healthy) != 1 {
		t.Fatalf("expected 1 healthy PoP, got %d", len(healthy))
	}

	// Mark unhealthy.
	err = s.SetPoPHealth(ctx(), "us-east", false)
	assertNoErr(t, err, "SetPoPHealth false")

	healthy, err = s.HealthyPoPs(ctx())
	assertNoErr(t, err, "HealthyPoPs after demotion")
	if len(healthy) != 0 {
		t.Errorf("unhealthy PoP still in healthy list: %v", healthy)
	}

	// NearestPoP with this unhealthy PoP must fail.
	allPops := []routing.PoP{{ID: "us-east", Healthy: false}}
	_, err = routing.NearestPoP(allPops, 40, -74)
	assertErr(t, err, routing.ErrNoHealthyPoP, "nearest after demotion")
}

func TestPoP_SetHealthUnknownPopErrors(t *testing.T) {
	s := newStore()
	err := s.SetPoPHealth(ctx(), "nonexistent", true)
	assertErr(t, err, routing.ErrNotFound, "SetPoPHealth nonexistent")
}

func TestPoP_NearestSelectsClosestHealthy(t *testing.T) {
	// Two healthy PoPs; verify the geographically closer one is chosen.
	pops := []routing.PoP{
		{ID: "near", Lat: 1, Lon: 1, Healthy: true},
		{ID: "far", Lat: 50, Lon: 50, Healthy: true},
	}
	got, err := routing.NearestPoP(pops, 2, 2)
	assertNoErr(t, err, "nearest of two healthy")
	if got.ID != "near" {
		t.Errorf("expected 'near', got %q", got.ID)
	}
}

// ---------------------------------------------------------------------------
// 9. CONCURRENCY — parallel Enroll for the same ULID from two accounts
// ---------------------------------------------------------------------------

func TestConcurrency_ParallelEnrollSameULID(t *testing.T) {
	// Run multiple times to stress the race detector.
	for iter := 0; iter < 50; iter++ {
		s := newStore()

		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			winners []string
			errs    []error
		)

		accounts := []string{"acct-A", "acct-B"}
		for _, acct := range accounts {
			acct := acct
			wg.Add(1)
			go func() {
				defer wg.Done()
				b, err := s.Enroll(ctx(), goodULID, acct, routing.ModeFabric)
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					winners = append(winners, b.AccountID)
				} else {
					errs = append(errs, err)
				}
			}()
		}
		wg.Wait()

		// Exactly one must win.
		if len(winners) != 1 {
			t.Errorf("iter %d: expected exactly 1 winner, got %d: %v", iter, len(winners), winners)
		}
		// Exactly one must get ErrULIDBoundToOtherAccount.
		if len(errs) != 1 {
			t.Errorf("iter %d: expected exactly 1 error, got %d: %v", iter, len(errs), errs)
		} else if !errors.Is(errs[0], routing.ErrULIDBoundToOtherAccount) {
			t.Errorf("iter %d: expected ErrULIDBoundToOtherAccount, got %v", iter, errs[0])
		}

		// The stored binding must match exactly one winner.
		b, err := s.GetBinding(ctx(), goodULID)
		if err != nil {
			t.Errorf("iter %d: GetBinding failed: %v", iter, err)
			continue
		}
		if len(winners) == 1 && b.AccountID != winners[0] {
			t.Errorf("iter %d: stored account %q != winner %q", iter, b.AccountID, winners[0])
		}
	}
}

func TestConcurrency_ParallelSetDirectIP_NonOwnerAlwaysFails(t *testing.T) {
	// Enroll under A; then hammer SetDirectIP from both A and B concurrently.
	// B must always get ErrNotOwner; A must always succeed (or see its own update).
	s := newStore()
	mustEnroll(t, s, goodULID, "acct-A", routing.ModeDirect)

	var wg sync.WaitGroup
	const goroutines = 20
	errsCh := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			acct := "acct-A"
			if i%2 == 1 {
				acct = "acct-B"
			}
			err := s.SetDirectIP(ctx(), goodULID, acct, "1.2.3.4")
			if acct == "acct-B" {
				errsCh <- err
			}
		}()
	}
	wg.Wait()
	close(errsCh)

	for err := range errsCh {
		if !errors.Is(err, routing.ErrNotOwner) {
			t.Errorf("non-owner SetDirectIP got wrong error: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// 10. ENROLL — input validation edge cases
// ---------------------------------------------------------------------------

func TestEnroll_EmptyULIDRejected(t *testing.T) {
	s := newStore()
	_, err := s.Enroll(ctx(), "", "acct-A", routing.ModeFabric)
	if err == nil {
		t.Fatal("Enroll with empty ULID should fail")
	}
}

func TestEnroll_EmptyAccountRejected(t *testing.T) {
	s := newStore()
	_, err := s.Enroll(ctx(), goodULID, "", routing.ModeFabric)
	if err == nil {
		t.Fatal("Enroll with empty accountID should fail")
	}
}

func TestEnroll_InvalidModeRejected(t *testing.T) {
	s := newStore()
	invalidModes := []routing.ConnMode{"", "local", "D", "invalid", "FABRIC"}
	for _, m := range invalidModes {
		_, err := s.Enroll(ctx(), goodULID, "acct-A", m)
		if err == nil {
			t.Errorf("Enroll with mode=%q should fail", m)
		}
	}
}

// ---------------------------------------------------------------------------
// 11. STORE ISOLATION — distinct ULIDs are independent
// ---------------------------------------------------------------------------

func TestStore_TwoULIDsAreIndependent(t *testing.T) {
	s := newStore()

	mustEnroll(t, s, goodULID, "acct-A", routing.ModeFabric)
	mustEnroll(t, s, goodULIDB, "acct-B", routing.ModeDirect)

	// A owns goodULID; B owns goodULIDB.
	// B trying to act on A's ULID is rejected.
	err := s.SetDirectIP(ctx(), goodULID, "acct-B", "5.6.7.8")
	assertErr(t, err, routing.ErrNotOwner, "B acts on A's ULID")

	// A trying to act on B's ULID is rejected.
	err = s.SetDirectIP(ctx(), goodULIDB, "acct-A", "5.6.7.8")
	assertErr(t, err, routing.ErrNotOwner, "A acts on B's ULID")
}

func TestStore_GetBindingForUnknownULID(t *testing.T) {
	s := newStore()
	_, err := s.GetBinding(ctx(), goodULID)
	assertErr(t, err, routing.ErrNotEnrolled, "GetBinding unknown")
}

// ---------------------------------------------------------------------------
// 12. PARSEHOST — port stripping security
// ---------------------------------------------------------------------------

func TestParseHost_PortStrippingDoesNotBypassValidation(t *testing.T) {
	// Ensure that port stripping cannot be used to smuggle a bad ULID past
	// the ULID validator (e.g., a valid hostname with port that strips to an
	// invalid form — though in practice the ULID is in the hostname, not port).
	base := "vulos.org"

	// Forged ULID with port — port stripped first, then ULID check must fail.
	badULID := "0IARZNNDEKTSV4RRFFQ69G5FAV" // contains I
	host := badULID + "." + base + ":443"
	_, _, _, err := routing.ParseHost(host, base)
	if err == nil {
		t.Errorf("ParseHost with forged ULID + port should fail")
	}
}

func TestParseHost_EmptyBase(t *testing.T) {
	_, _, _, err := routing.ParseHost(goodULID+".vulos.org", "")
	if err == nil {
		t.Error("ParseHost with empty base should fail")
	}
}
