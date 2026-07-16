// Package region is the single source of truth for "where does this tenant run".
//
// The CP previously carried THREE incompatible region vocabularies and passed
// strings between them untranslated:
//
//	compute/Fly  — "jnb", "fra", "iad"        (Fly machine regions)
//	storage/S3   — "eu-west-1", "af-south-1"  (the managed store/S3 location constraints)
//	georoute     — "eu", "us", "af"           (coarse families)
//
// The default managed region was the S3 slug "eu-west-1", and it was handed
// straight to the Fly Machines API — which has no such region. Nothing validated
// it, because the provider's region mapper returned unknown strings unchanged.
//
// This package makes the vocabulary explicit and CLOSED: a region is one of the
// entries in the table below, an input string is canonicalized (accepting any of
// the three spellings), and anything unrecognised is an ERROR rather than a
// string that travels onward to fail at an infrastructure boundary — or, worse,
// to be recorded as a residency guarantee that was never enforced.
//
// Adding a region means adding one row here. That is deliberately the only way.
package region

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrUnknownRegion is returned for any region string outside the table.
var ErrUnknownRegion = errors.New("region: unknown region")

// Region is a canonical Vulos region: one row of the table, carrying the slug
// each downstream provider expects.
type Region struct {
	// Key is the canonical Vulos identifier, and the value that belongs in the
	// database. It is provider-neutral by construction.
	Key string
	// Fly is the Fly.io machine region code.
	Fly string
	// S3 is the the managed store/S3 LocationConstraint for buckets placed in this region.
	S3 string
	// Family is the coarse continental grouping (georoute's vocabulary).
	Family string
	// Label is the human-facing name.
	Label string
}

// table is the closed set of regions Vulos runs in. Every row must be a region
// that BOTH Fly and the managed store actually serve — that pairing is the whole point of
// the table, and it is what makes a residency promise enforceable.
var table = []Region{
	{Key: "eu-west", Fly: "ams", S3: "eu-west-1", Family: "eu", Label: "EU West (Amsterdam)"},
	{Key: "eu-central", Fly: "fra", S3: "eu-central-1", Family: "eu", Label: "EU Central (Frankfurt)"},
	{Key: "uk-south", Fly: "lhr", S3: "eu-west-2", Family: "eu", Label: "UK South (London)"},
	{Key: "af-south", Fly: "jnb", S3: "af-south-1", Family: "af", Label: "Africa South (Johannesburg)"},
	{Key: "us-east", Fly: "iad", S3: "us-east-1", Family: "us", Label: "US East (Virginia)"},
	{Key: "us-west", Fly: "sjc", S3: "us-west-1", Family: "us", Label: "US West (San Jose)"},
}

// index maps every accepted spelling — canonical key, Fly code, S3 slug — to its
// row, so a value written by any layer canonicalizes back to the same region.
var index = func() map[string]Region {
	m := make(map[string]Region, len(table)*3)
	for _, r := range table {
		m[r.Key] = r
		m[r.Fly] = r
		m[r.S3] = r
	}
	return m
}()

// All returns the region table, ordered by canonical key.
func All() []Region {
	out := append([]Region(nil), table...)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Lookup canonicalizes any accepted spelling of a region (canonical key, Fly
// code, or S3 slug; case- and space-insensitive) into its row.
//
// It FAILS CLOSED: an unrecognised string returns ErrUnknownRegion rather than
// being passed through. That pass-through is exactly how the S3 slug "eu-west-1"
// reached the Fly Machines API.
func Lookup(s string) (Region, error) {
	key := strings.ToLower(strings.TrimSpace(s))
	if key == "" {
		return Region{}, fmt.Errorf("%w: empty", ErrUnknownRegion)
	}
	r, ok := index[key]
	if !ok {
		return Region{}, fmt.Errorf("%w: %q", ErrUnknownRegion, s)
	}
	return r, nil
}

// IsValid reports whether s names a region in the table (in any spelling).
func IsValid(s string) bool {
	_, err := Lookup(s)
	return err == nil
}

// Canonical returns the canonical key for any accepted spelling.
func Canonical(s string) (string, error) {
	r, err := Lookup(s)
	if err != nil {
		return "", err
	}
	return r.Key, nil
}

// Fly returns the Fly machine region code for any accepted spelling.
func Fly(s string) (string, error) {
	r, err := Lookup(s)
	if err != nil {
		return "", err
	}
	return r.Fly, nil
}

// S3 returns the the managed store/S3 LocationConstraint for any accepted spelling.
func S3(s string) (string, error) {
	r, err := Lookup(s)
	if err != nil {
		return "", err
	}
	return r.S3, nil
}

// Same reports whether a and b name the same region, in ANY spelling — so a
// residency policy written as "eu-west-1" matches a request for "ams". Unknown
// regions are never "the same" as anything, including each other: an unknown
// value must not be able to satisfy a residency check by string equality.
func Same(a, b string) bool {
	ra, err := Lookup(a)
	if err != nil {
		return false
	}
	rb, err := Lookup(b)
	if err != nil {
		return false
	}
	return ra.Key == rb.Key
}
