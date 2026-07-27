package contacts

import "strings"

// normalize.go — the normalisation used both to DEDUPE fields within a contact
// and to build the MATCH KEYS the merge layer unions on. Getting these stable and
// forgiving is what lets "+27 82 123 4567", "0821234567" and "27821234567" all
// collapse to one person.

// normEmail lower-cases and trims an email for both display-dedup and matching.
// (Case-insensitive is safe: the domain is always case-insensitive and every real
// mail provider treats the local-part case-insensitively too.)
func normEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// phoneDigits keeps only the dialable digits of a phone string (drops +, spaces,
// dashes, parentheses). Used as the canonical display-dedup form.
func phoneDigits(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// phoneMatchKey is the key two phone numbers are considered "the same" on. Real
// address books mix national (0821234567), international (+27821234567) and
// bare-national (821234567) forms of one number, which share their trailing
// significant digits but differ in the country/trunk prefix. So we match on the
// LAST 9 digits — long enough to avoid collisions between distinct numbers, short
// enough to unify the national/international forms of one.
//
// Short strings (< matchTail digits: short codes, extensions) are matched on their
// full digit string rather than truncated, so "150" and "1150" stay distinct.
// An empty / digitless input yields "" (never a match key).
const matchTail = 9

func phoneMatchKey(s string) string {
	d := phoneDigits(s)
	if d == "" {
		return ""
	}
	if len(d) > matchTail {
		return d[len(d)-matchTail:]
	}
	return d
}

// normName collapses a display name to a comparison form: lower-cased, with runs
// of whitespace collapsed to single spaces and the ends trimmed.
func normName(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// nameTokens returns the whitespace-separated tokens of the normalised name.
func nameTokens(s string) []string {
	return strings.Fields(normName(s))
}

// isFullName reports whether a name is "strong" enough to merge two DIFFERENT
// records on the name ALONE (i.e. with no shared phone or email). We require at
// least two tokens (a first + last name): full-name collisions between two
// genuinely different people are far rarer than single-token collisions ("Mom",
// "Work", "Dad"), which must NOT be merged on the label alone. See merge.go.
func isFullName(s string) bool {
	return len(nameTokens(s)) >= 2
}

// nameKey is the name match key (only meaningful for full names).
func nameKey(s string) string {
	return normName(s)
}
