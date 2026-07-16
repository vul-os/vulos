package region

import (
	"errors"
	"testing"
)

// The bug this table exists to prevent: the managed default region was the S3
// slug "eu-west-1", which was handed to the Fly Machines API — a provider with
// no such region. Every spelling must resolve to the same row, and every row
// must yield a slug in each provider's own vocabulary.
func TestLookup_AcceptsEverySpelling(t *testing.T) {
	for _, spelling := range []string{"eu-west", "ams", "eu-west-1", "  EU-West-1 "} {
		r, err := Lookup(spelling)
		if err != nil {
			t.Fatalf("Lookup(%q) = error %v, want the eu-west row", spelling, err)
		}
		if r.Key != "eu-west" || r.Fly != "ams" || r.S3 != "eu-west-1" {
			t.Fatalf("Lookup(%q) = %+v, want key=eu-west fly=ams s3=eu-west-1", spelling, r)
		}
	}
}

func TestLookup_UnknownFailsClosed(t *testing.T) {
	// "eu-west-9" and "mars" are not regions. Passing them THROUGH is the bug.
	for _, bad := range []string{"", "mars", "eu-west-9", "nrt"} {
		if _, err := Lookup(bad); !errors.Is(err, ErrUnknownRegion) {
			t.Fatalf("Lookup(%q) err = %v, want ErrUnknownRegion", bad, err)
		}
		if IsValid(bad) {
			t.Fatalf("IsValid(%q) = true, want false", bad)
		}
	}
}

func TestFlyAndS3_TranslateAcrossVocabularies(t *testing.T) {
	// The exact failing case: the S3 slug must translate to a REAL Fly code.
	fly, err := Fly("eu-west-1")
	if err != nil || fly == "eu-west-1" {
		t.Fatalf("Fly(eu-west-1) = %q, %v — want a Fly code, never the S3 slug back", fly, err)
	}
	if fly != "ams" {
		t.Fatalf("Fly(eu-west-1) = %q, want ams", fly)
	}
	s3, err := S3("jnb") // a Fly code in, an S3 slug out
	if err != nil || s3 != "af-south-1" {
		t.Fatalf("S3(jnb) = %q, %v, want af-south-1", s3, err)
	}
}

func TestSame_ComparesCanonically(t *testing.T) {
	// A residency policy in S3 slugs must match a request in Fly codes.
	if !Same("eu-west-1", "ams") {
		t.Fatal("Same(eu-west-1, ams) = false — a policy would reject the region it mandates")
	}
	if Same("eu-west", "us-east") {
		t.Fatal("Same(eu-west, us-east) = true")
	}
	// Unknowns are never the same as anything — not even themselves. An unknown
	// value must not satisfy a residency check by string equality.
	if Same("mars", "mars") {
		t.Fatal("Same(mars, mars) = true — an unknown region must never satisfy a residency check")
	}
}

func TestTable_EveryRowIsCompleteAndUnique(t *testing.T) {
	seen := map[string]string{} // spelling -> key that claimed it
	for _, r := range All() {
		if r.Key == "" || r.Fly == "" || r.S3 == "" || r.Family == "" || r.Label == "" {
			t.Fatalf("region row %+v has an empty field — every row must carry a slug per provider", r)
		}
		for _, spelling := range []string{r.Key, r.Fly, r.S3} {
			if prev, dup := seen[spelling]; dup {
				t.Fatalf("spelling %q claimed by both %q and %q — lookups would be ambiguous", spelling, prev, r.Key)
			}
			seen[spelling] = r.Key
		}
	}
}
