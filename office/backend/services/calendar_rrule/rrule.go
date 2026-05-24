// Package calendar_rrule provides RFC 5545 RRULE parsing and expansion using
// the pure-Go github.com/teambition/rrule-go library.
//
// Three top-level helpers are exposed:
//   - Parse  — validates an RRULE string and returns a typed *RRule value.
//   - Expand — returns all occurrence timestamps in a [from, to) window.
//   - NextOccurrence — returns the first occurrence strictly after a given time.
package calendar_rrule

import (
	"fmt"
	"time"

	rrulelib "github.com/teambition/rrule-go"
)

// RRule wraps the underlying rrule-go set for convenience.
type RRule struct {
	raw string
	set *rrulelib.Set
}

// String returns the original RRULE string.
func (r *RRule) String() string { return r.raw }

// Parse validates an RFC 5545 RRULE string (e.g. "RRULE:FREQ=WEEKLY;BYDAY=MO,WE")
// together with a dtstart anchor.  dtstart is needed by rrule-go to resolve
// relative patterns.
func Parse(dtstart time.Time, rruleStr string) (*RRule, error) {
	if rruleStr == "" {
		return nil, fmt.Errorf("calendar_rrule: empty RRULE string")
	}

	ropt, err := rrulelib.StrToROption(rruleStr)
	if err != nil {
		return nil, fmt.Errorf("calendar_rrule: %w", err)
	}
	ropt.Dtstart = dtstart

	rule, err := rrulelib.NewRRule(*ropt)
	if err != nil {
		return nil, fmt.Errorf("calendar_rrule: %w", err)
	}

	set := &rrulelib.Set{}
	set.RRule(rule)

	return &RRule{raw: rruleStr, set: set}, nil
}

// Expand returns all occurrences of the rule in the half-open interval [from, to).
// At most maxOccurrences results are returned (capped at 500 to guard against
// pathological RRULE strings like FREQ=SECONDLY).
func Expand(dtstart time.Time, rruleStr string, from, to time.Time) ([]time.Time, error) {
	const maxOccurrences = 500

	rule, err := Parse(dtstart, rruleStr)
	if err != nil {
		return nil, err
	}

	all := rule.set.Between(from, to, true)
	if len(all) > maxOccurrences {
		all = all[:maxOccurrences]
	}
	return all, nil
}

// NextOccurrence returns the first occurrence of the rule strictly after after.
// Returns time.Time{} (zero value) if the rule has no further occurrences.
func NextOccurrence(dtstart time.Time, rruleStr string, after time.Time) (time.Time, error) {
	rule, err := Parse(dtstart, rruleStr)
	if err != nil {
		return time.Time{}, err
	}

	next := rule.set.After(after, false)
	return next, nil
}
