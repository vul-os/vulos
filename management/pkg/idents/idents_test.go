package idents_test

import (
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/idents"
)

// ---------------------------------------------------------------------------
// ParseULID
// ---------------------------------------------------------------------------

// validULIDStr is a known-good Crockford base32 ULID (26 chars, no I/L/O/U).
// Manually chosen: "01ARZ3NDEKTSV4RRFFQ69G5FAV"
const validULIDStr = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestParseULID_ValidRoundtrip(t *testing.T) {
	u, err := idents.ParseULID(validULIDStr)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	// Result must be a non-zero 16-byte value.
	var zero idents.ULID
	if u == zero {
		t.Fatal("parsed ULID is all-zero, expected non-zero bytes")
	}
}

func TestParseULID_CaseInsensitive(t *testing.T) {
	upper := validULIDStr
	lower := strings.ToLower(validULIDStr)

	u1, err := idents.ParseULID(upper)
	if err != nil {
		t.Fatalf("upper-case parse failed: %v", err)
	}
	u2, err := idents.ParseULID(lower)
	if err != nil {
		t.Fatalf("lower-case parse failed: %v", err)
	}
	if u1 != u2 {
		t.Fatalf("upper and lower case produced different results: %v vs %v", u1, u2)
	}
}

func TestParseULID_AllZeros(t *testing.T) {
	// 26 zeros is a valid ULID (all-zero value).
	u, err := idents.ParseULID("00000000000000000000000000")
	if err != nil {
		t.Fatalf("all-zeros ULID should be valid, got: %v", err)
	}
	var zero idents.ULID
	if u != zero {
		t.Fatal("all-zeros ULID should decode to zero value")
	}
}

func TestParseULID_MaxValid(t *testing.T) {
	// 7ZZZZZZZZZZZZZZZZZZZZZZZZZ is the maximum ULID.
	_, err := idents.ParseULID("7ZZZZZZZZZZZZZZZZZZZZZZZZZ")
	if err != nil {
		t.Fatalf("max ULID should be valid, got: %v", err)
	}
}

func TestParseULID_TooShort(t *testing.T) {
	_, err := idents.ParseULID("01ARZ3NDEKTSV4RRFFQ69G5FA") // 25 chars
	if err == nil {
		t.Fatal("expected error for 25-char input")
	}
}

func TestParseULID_TooLong(t *testing.T) {
	_, err := idents.ParseULID("01ARZ3NDEKTSV4RRFFQ69G5FAVX") // 27 chars
	if err == nil {
		t.Fatal("expected error for 27-char input")
	}
}

func TestParseULID_Empty(t *testing.T) {
	_, err := idents.ParseULID("")
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

// Crockford excludes I, L, O, U (both cases).
func TestParseULID_ExcludedChars(t *testing.T) {
	// Replace one character in a valid ULID with each excluded char.
	base := []byte(validULIDStr) // 26 chars
	excluded := []byte{'I', 'L', 'O', 'U', 'i', 'l', 'o', 'u'}
	for _, ch := range excluded {
		candidate := make([]byte, 26)
		copy(candidate, base)
		candidate[5] = ch // inject excluded char at position 5
		_, err := idents.ParseULID(string(candidate))
		if err == nil {
			t.Errorf("expected error for ULID containing excluded char %q, got none", ch)
		}
	}
}

func TestParseULID_InvalidChar(t *testing.T) {
	// '@' is not in Crockford alphabet.
	candidate := []byte(validULIDStr)
	candidate[3] = '@'
	_, err := idents.ParseULID(string(candidate))
	if err == nil {
		t.Fatal("expected error for ULID containing '@'")
	}
}

func TestParseULID_SpaceRejected(t *testing.T) {
	candidate := []byte(validULIDStr)
	candidate[0] = ' '
	_, err := idents.ParseULID(string(candidate))
	if err == nil {
		t.Fatal("expected error for ULID containing space")
	}
}

func TestParseULID_Overflow(t *testing.T) {
	// First character > '7' causes overflow (9 bits for 128-bit value).
	// '8' maps to 8 in Crockford, which is > 7 → overflow.
	overflow := "8" + validULIDStr[1:] // 26 chars, first char '8'
	_, err := idents.ParseULID(overflow)
	if err == nil {
		t.Fatal("expected overflow error when first symbol > 7")
	}
}

// ---------------------------------------------------------------------------
// ValidName
// ---------------------------------------------------------------------------

func TestValidName_GoldenValid(t *testing.T) {
	valid := []string{
		"a",
		"a1",
		"vulos",
		"my-app-1",
		"x" + strings.Repeat("a", 61) + "x", // exactly 63 chars
	}
	for _, s := range valid {
		if !idents.ValidName(s) {
			t.Errorf("ValidName(%q) = false, want true", s)
		}
	}
}

func TestValidName_GoldenInvalid(t *testing.T) {
	invalid := []string{
		"",                      // empty
		"-a",                    // leading hyphen
		"a-",                    // trailing hyphen
		"--",                    // double hyphen only
		"_x",                    // underscore
		"A1",                    // uppercase
		strings.Repeat("a", 64), // 64 chars, too long
		"vulos--cloud",          // double hyphen in middle
		"-",                     // hyphen only
		"a--b",                  // double hyphen
		"Hello",                 // uppercase H
		"my_app",                // underscore
		" a",                    // leading space
		"a ",                    // trailing space
	}
	for _, s := range invalid {
		if idents.ValidName(s) {
			t.Errorf("ValidName(%q) = true, want false", s)
		}
	}
}

func TestValidName_Boundary63(t *testing.T) {
	// 63 characters: valid.
	s63 := "a" + strings.Repeat("b", 61) + "c"
	if len(s63) != 63 {
		t.Fatalf("test setup error: expected 63, got %d", len(s63))
	}
	if !idents.ValidName(s63) {
		t.Errorf("ValidName(63-char name) = false, want true")
	}

	// 64 characters: invalid.
	s64 := s63 + "d"
	if idents.ValidName(s64) {
		t.Errorf("ValidName(64-char name) = true, want false")
	}
}

func TestValidName_SingleChar(t *testing.T) {
	// Single lowercase letter or digit: valid.
	for _, ch := range "abcdefghijklmnopqrstuvwxyz0123456789" {
		s := string(ch)
		if !idents.ValidName(s) {
			t.Errorf("ValidName(%q) = false, want true", s)
		}
	}
	// Single hyphen: invalid.
	if idents.ValidName("-") {
		t.Error("ValidName(\"-\") = true, want false")
	}
	// Single uppercase: invalid.
	if idents.ValidName("A") {
		t.Error("ValidName(\"A\") = true, want false")
	}
}
