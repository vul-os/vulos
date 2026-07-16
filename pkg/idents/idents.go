// Package idents provides shared identity validators for the vulos.cloud
// control plane.
//
// ULID representation: We use [16]byte (type ULID [16]byte) rather than a
// string alias so that callers get a compact, comparable value with no
// allocation after the parse, and because the raw bytes are what every store
// layer ultimately needs.
package idents

import (
	"errors"
	"regexp"
	"strings"
)

// ULID is the 16-byte decoded form of a 26-character Crockford base32 ULID.
type ULID [16]byte

// Crockford base32 alphabet (RFC 4648 base32 minus I, L, O, U).
// Characters I, L, O, U are explicitly excluded.
// The alphabet is case-insensitive; we normalise to upper before decoding.
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// crockfordVal maps an ASCII byte to its 5-bit value (0-31), or 0xFF if invalid.
var crockfordVal [256]byte

func init() {
	for i := range crockfordVal {
		crockfordVal[i] = 0xFF
	}
	for i, ch := range crockfordAlphabet {
		crockfordVal[ch] = byte(i)
		// lower-case equivalent
		if ch >= 'A' {
			crockfordVal[ch+32] = byte(i)
		}
	}
}

// ParseULID parses a 26-character Crockford base32 string into a ULID.
// The input is case-insensitive. Crockford's excluded characters (I, L, O, U)
// are rejected even though some encoders silently substitute them.
func ParseULID(s string) (ULID, error) {
	if len(s) != 26 {
		return ULID{}, errors.New("idents: ULID must be exactly 26 characters")
	}

	// Reject Crockford-excluded characters explicitly, both cases.
	if strings.ContainsAny(s, "ILOUilou") {
		return ULID{}, errors.New("idents: ULID contains excluded Crockford character (I/L/O/U)")
	}

	// Decode 26 x 5-bit symbols → 130 bits → 16 bytes (128 bits used; 2 leading bits of first symbol must be 0).
	var out ULID
	var buf [26]byte
	for i := 0; i < 26; i++ {
		v := crockfordVal[s[i]]
		if v == 0xFF {
			return ULID{}, errors.New("idents: ULID contains invalid character")
		}
		buf[i] = v
	}

	// The most-significant two bits of buf[0] must be zero for a 128-bit value.
	if buf[0] > 7 {
		return ULID{}, errors.New("idents: ULID value overflows 128 bits")
	}

	// Pack 26*5 = 130 bits into 16 bytes (big-endian, skip leading 2 zero bits).
	out[0] = (buf[0] << 5) | buf[1]
	out[1] = (buf[2] << 3) | (buf[3] >> 2)
	out[2] = (buf[3] << 6) | (buf[4] << 1) | (buf[5] >> 4)
	out[3] = (buf[5] << 4) | (buf[6] >> 1)
	out[4] = (buf[6] << 7) | (buf[7] << 2) | (buf[8] >> 3)
	out[5] = (buf[8] << 5) | buf[9]
	out[6] = (buf[10] << 3) | (buf[11] >> 2)
	out[7] = (buf[11] << 6) | (buf[12] << 1) | (buf[13] >> 4)
	out[8] = (buf[13] << 4) | (buf[14] >> 1)
	out[9] = (buf[14] << 7) | (buf[15] << 2) | (buf[16] >> 3)
	out[10] = (buf[16] << 5) | buf[17]
	out[11] = (buf[18] << 3) | (buf[19] >> 2)
	out[12] = (buf[19] << 6) | (buf[20] << 1) | (buf[21] >> 4)
	out[13] = (buf[21] << 4) | (buf[22] >> 1)
	out[14] = (buf[22] << 7) | (buf[23] << 2) | (buf[24] >> 3)
	out[15] = (buf[24] << 5) | buf[25]

	return out, nil
}

// nameRe matches a valid canonical name:
//   - single character: [a-z0-9]
//   - multi character: [a-z0-9][a-z0-9-]*[a-z0-9], no consecutive hyphens
//
// Length and double-hyphen checks are applied in ValidName to keep the regexp simple.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`)

// ValidName reports whether s is a valid canonical control-plane name.
// Rules:
//   - Contains only [a-z0-9-].
//   - Must begin and end with [a-z0-9].
//   - Length 1-63.
//   - No consecutive hyphens (--).
func ValidName(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	if strings.Contains(s, "--") {
		return false
	}
	return nameRe.MatchString(s)
}
