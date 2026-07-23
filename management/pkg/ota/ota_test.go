package ota

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

const (
	testULID1 = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	testULID2 = "01BX5ZZKBKACTAV9WEVGEMMVRY"
)

func baseRelease() Release {
	return Release{
		Version:      "1.2.0",
		Channel:      "stable",
		ArtifactURL:  "https://cdn.vulos.cloud/ota/v1.2.0/artifact.tar.gz",
		Sha256:       "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		MinFrom:      "1.0.0",
		Security:     false,
		RolloutPct:   100,
		SignatureB64: "AAAA", // placeholder; test signer will be used for sig tests
		DeferMaxSec:  604800,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// CohortHash
// ─────────────────────────────────────────────────────────────────────────────

func TestCohortHash_Range(t *testing.T) {
	t.Parallel()
	for i := 0; i < 1000; i++ {
		ulid := testULID1
		v := "1.0." + itoa(i)
		h := CohortHash(ulid, v)
		if h > 99 {
			t.Fatalf("CohortHash(%q, %q) = %d; want 0-99", ulid, v, h)
		}
	}
}

func TestCohortHash_Deterministic(t *testing.T) {
	t.Parallel()
	h1 := CohortHash(testULID1, "1.2.0")
	h2 := CohortHash(testULID1, "1.2.0")
	if h1 != h2 {
		t.Fatalf("CohortHash should be deterministic: %d != %d", h1, h2)
	}
}

func TestCohortHash_Distribution(t *testing.T) {
	t.Parallel()
	// Generate 10000 diverse ULID-like strings, check 30% rollout gives [2800, 3200] hits.
	// We use a simple LCG to get varied inputs without importing crypto/rand.
	const total = 10000
	const rollout = 30
	var inCohort int
	state := uint64(0xdeadbeefcafe1234)
	for i := 0; i < total; i++ {
		ulid := lcgULID(&state)
		if int(CohortHash(ulid, "1.0.0")) < rollout {
			inCohort++
		}
	}
	const lo, hi = 2800, 3200
	if inCohort < lo || inCohort > hi {
		t.Fatalf("CohortHash distribution at 30%% rollout: %d/%d in cohort; want [%d, %d]",
			inCohort, total, lo, hi)
	}
}

// lcgULID generates a pseudo-random 26-char Crockford string using a linear
// congruential generator — deterministic but well-distributed.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func lcgULID(state *uint64) string {
	b := make([]byte, 26)
	for i := range b {
		// LCG: multiplier and addend from Knuth vol 2.
		*state = *state*6364136223846793005 + 1442695040888963407
		b[i] = crockford[(*state>>33)%32]
	}
	// Ensure valid Crockford (first char must give value 0-7 for 128-bit fit).
	// Force first char to be in '0'-'7' range.
	b[0] = crockford[(*state>>33)%8]
	return string(b)
}

// syntheticULID returns a deterministic valid-looking 26-char string for i.
// Used only in FeedCohortInOut to search for ULIDs with specific cohort values.
func syntheticULID(i int) string {
	state := uint64(i*1000000007 + 42)
	return lcgULID(&state)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := 20
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

// ─────────────────────────────────────────────────────────────────────────────
// ValidChannel
// ─────────────────────────────────────────────────────────────────────────────

func TestValidChannel(t *testing.T) {
	t.Parallel()
	for _, ch := range []string{"stable", "beta", "pinned"} {
		if !ValidChannel(ch) {
			t.Errorf("ValidChannel(%q) = false; want true", ch)
		}
	}
	for _, ch := range []string{"", "nightly", "STABLE", "Stable"} {
		if ValidChannel(ch) {
			t.Errorf("ValidChannel(%q) = true; want false", ch)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// MemSigner
// ─────────────────────────────────────────────────────────────────────────────

func TestMemSigner_RoundTrip(t *testing.T) {
	t.Parallel()
	s := NewMemSigner()
	data := []byte(`{"version":"1.0.0","artifact_url":"https://example.com/v1.tar.gz","sha256":"abc","security":false}`)
	sig, err := s.Sign(data)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if sig == "" {
		t.Fatal("Sign returned empty signature")
	}
	if err := s.Verify(data, sig); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestMemSigner_TamperedData(t *testing.T) {
	t.Parallel()
	s := NewMemSigner()
	data := []byte(`{"version":"1.0.0","sha256":"abc"}`)
	sig, err := s.Sign(data)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	// Tamper one byte.
	tampered := make([]byte, len(data))
	copy(tampered, data)
	tampered[5] = 'X'
	if err := s.Verify(tampered, sig); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("Verify(tampered) = %v; want ErrInvalidSignature", err)
	}
}

func TestMemSigner_TamperedSignature(t *testing.T) {
	t.Parallel()
	s := NewMemSigner()
	data := []byte(`{"version":"1.0.0"}`)
	sig, _ := s.Sign(data)
	// Tamper the last character.
	bsig := []byte(sig)
	bsig[len(bsig)-1] ^= 0x01
	badSig := string(bsig)
	if err := s.Verify(data, badSig); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("Verify(bad sig) = %v; want ErrInvalidSignature", err)
	}
}

func TestMemSigner_CrossKeyFailure(t *testing.T) {
	t.Parallel()
	s1 := NewMemSigner()
	s2 := NewMemSigner()
	data := []byte(`{"version":"1.2.0"}`)
	sig, _ := s1.Sign(data)
	// sig from s1; verify with s2 must fail.
	if err := s2.Verify(data, sig); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("Verify(cross-key) = %v; want ErrInvalidSignature", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// semver comparison
// ─────────────────────────────────────────────────────────────────────────────

func TestSemverGT(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.2.1", "1.2.0", true},
		{"1.3.0", "1.2.0", true},
		{"2.0.0", "1.9.9", true},
		{"1.2.0", "1.2.0", false},
		{"1.1.0", "1.2.0", false},
		{"1.0.0", "2.0.0", false},
	}
	for _, c := range cases {
		if got := semverGT(c.a, c.b); got != c.want {
			t.Errorf("semverGT(%q, %q) = %v; want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestSemverGTE(t *testing.T) {
	t.Parallel()
	if !semverGTE("1.2.0", "1.2.0") {
		t.Error("semverGTE equal case failed")
	}
	if !semverGTE("1.3.0", "1.2.0") {
		t.Error("semverGTE greater case failed")
	}
	if semverGTE("1.1.0", "1.2.0") {
		t.Error("semverGTE lesser case should be false")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// MemStore basic operations (tested individually; conformance suite is thorough)
// ─────────────────────────────────────────────────────────────────────────────

func TestMemStore_InsertAndFeed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	defer st.Close()

	r := baseRelease()
	id, err := st.InsertRelease(ctx, r)
	if err != nil {
		t.Fatalf("InsertRelease: %v", err)
	}
	if id <= 0 {
		t.Errorf("InsertRelease returned id %d; want > 0", id)
	}

	rel, err := st.FeedFor(ctx, testULID1, "stable", "1.0.0")
	if err != nil {
		t.Fatalf("FeedFor: %v", err)
	}
	if rel.Version != "1.2.0" {
		t.Errorf("FeedFor version = %q; want 1.2.0", rel.Version)
	}
}

func TestMemStore_DuplicateRelease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	defer st.Close()

	r := baseRelease()
	if _, err := st.InsertRelease(ctx, r); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	_, err := st.InsertRelease(ctx, r)
	if !errors.Is(err, ErrDuplicateRelease) {
		t.Fatalf("second insert = %v; want ErrDuplicateRelease", err)
	}
}

func TestMemStore_HaltSuppressesFeed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	defer st.Close()

	r := baseRelease()
	id, _ := st.InsertRelease(ctx, r)
	if err := st.HaltRelease(ctx, id); err != nil {
		t.Fatalf("HaltRelease: %v", err)
	}
	_, err := st.FeedFor(ctx, testULID1, "stable", "1.0.0")
	if !errors.Is(err, ErrNoUpgrade) {
		t.Fatalf("FeedFor(halted release) = %v; want ErrNoUpgrade", err)
	}
}

func TestMemStore_HaltUnknown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	defer st.Close()

	if err := st.HaltRelease(ctx, 9999); !errors.Is(err, ErrReleaseNotFound) {
		t.Fatalf("HaltRelease(unknown) = %v; want ErrReleaseNotFound", err)
	}
}

func TestMemStore_FeedNoUpgradeWhenCurrent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	defer st.Close()

	r := baseRelease() // version = 1.2.0
	if _, err := st.InsertRelease(ctx, r); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Device already at 1.2.0 — no upgrade.
	_, err := st.FeedFor(ctx, testULID1, "stable", "1.2.0")
	if !errors.Is(err, ErrNoUpgrade) {
		t.Fatalf("FeedFor(current) = %v; want ErrNoUpgrade", err)
	}
}

func TestMemStore_FeedMinFromConstraint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	defer st.Close()

	r := baseRelease()
	r.MinFrom = "1.1.0" // device must be >= 1.1.0 to receive
	if _, err := st.InsertRelease(ctx, r); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Device at 1.0.0 < min_from 1.1.0 — no upgrade.
	_, err := st.FeedFor(ctx, testULID1, "stable", "1.0.0")
	if !errors.Is(err, ErrNoUpgrade) {
		t.Fatalf("FeedFor(below min_from) = %v; want ErrNoUpgrade", err)
	}

	// Device at 1.1.0 >= min_from — should get upgrade.
	rel, err := st.FeedFor(ctx, testULID1, "stable", "1.1.0")
	if err != nil {
		t.Fatalf("FeedFor(at min_from): %v", err)
	}
	if rel.Version != "1.2.0" {
		t.Errorf("version = %q; want 1.2.0", rel.Version)
	}
}

func TestMemStore_PinSuppressesNonSecurity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	defer st.Close()

	// Insert v1.3.0 on stable.
	r := baseRelease()
	r.Version = "1.3.0"
	if _, err := st.InsertRelease(ctx, r); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Pin device to v1.2.0.
	if err := st.SetPolicy(ctx, DevicePolicy{
		ULID:       testULID1,
		Channel:    "stable",
		PinVersion: "1.2.0",
	}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	// Feed should be suppressed (pin != release version).
	_, err := st.FeedFor(ctx, testULID1, "stable", "1.0.0")
	if !errors.Is(err, ErrNoUpgrade) {
		t.Fatalf("FeedFor(pinned, non-security) = %v; want ErrNoUpgrade", err)
	}
}

func TestMemStore_SecurityOverridesPin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	defer st.Close()

	r := baseRelease()
	r.Version = "1.3.0"
	r.Security = true
	if _, err := st.InsertRelease(ctx, r); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Pin device to v1.2.0.
	if err := st.SetPolicy(ctx, DevicePolicy{
		ULID:       testULID1,
		Channel:    "stable",
		PinVersion: "1.2.0",
	}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	// Security release must override the pin.
	rel, err := st.FeedFor(ctx, testULID1, "stable", "1.0.0")
	if err != nil {
		t.Fatalf("FeedFor(security overrides pin): %v", err)
	}
	if rel.Version != "1.3.0" {
		t.Errorf("version = %q; want 1.3.0", rel.Version)
	}
}

func TestMemStore_DeferSuppressesNonSecurity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	defer st.Close()

	r := baseRelease()
	r.Version = "1.3.0"
	if _, err := st.InsertRelease(ctx, r); err != nil {
		t.Fatalf("insert: %v", err)
	}

	future := time.Now().Add(24 * time.Hour)
	if err := st.SetPolicy(ctx, DevicePolicy{
		ULID:       testULID1,
		Channel:    "stable",
		DeferUntil: &future,
	}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	// Non-security release suppressed while in defer window.
	_, err := st.FeedFor(ctx, testULID1, "stable", "1.0.0")
	if !errors.Is(err, ErrNoUpgrade) {
		t.Fatalf("FeedFor(deferred, non-security) = %v; want ErrNoUpgrade", err)
	}
}

func TestMemStore_SecurityOverridesDefer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	defer st.Close()

	r := baseRelease()
	r.Version = "1.3.0"
	r.Security = true
	if _, err := st.InsertRelease(ctx, r); err != nil {
		t.Fatalf("insert: %v", err)
	}

	future := time.Now().Add(24 * time.Hour)
	if err := st.SetPolicy(ctx, DevicePolicy{
		ULID:       testULID1,
		Channel:    "stable",
		DeferUntil: &future,
	}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	// Security release ignores defer.
	rel, err := st.FeedFor(ctx, testULID1, "stable", "1.0.0")
	if err != nil {
		t.Fatalf("FeedFor(security overrides defer): %v", err)
	}
	if rel.Version != "1.3.0" {
		t.Errorf("version = %q; want 1.3.0", rel.Version)
	}
}

func TestMemStore_OptOutFeaturesSuppressesNonSecurity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	defer st.Close()

	r := baseRelease()
	r.Version = "1.3.0"
	if _, err := st.InsertRelease(ctx, r); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := st.SetPolicy(ctx, DevicePolicy{
		ULID:           testULID1,
		Channel:        "stable",
		OptOutFeatures: true,
	}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	_, err := st.FeedFor(ctx, testULID1, "stable", "1.0.0")
	if !errors.Is(err, ErrNoUpgrade) {
		t.Fatalf("FeedFor(opt-out, non-security) = %v; want ErrNoUpgrade", err)
	}
}

func TestMemStore_OptOutFeaturesAllowsSecurity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	defer st.Close()

	r := baseRelease()
	r.Version = "1.3.0"
	r.Security = true
	if _, err := st.InsertRelease(ctx, r); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := st.SetPolicy(ctx, DevicePolicy{
		ULID:           testULID1,
		Channel:        "stable",
		OptOutFeatures: true,
	}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}

	rel, err := st.FeedFor(ctx, testULID1, "stable", "1.0.0")
	if err != nil {
		t.Fatalf("FeedFor(opt-out, security): %v", err)
	}
	if rel.Version != "1.3.0" {
		t.Errorf("version = %q; want 1.3.0", rel.Version)
	}
}

func TestMemStore_GetPolicy_UnknownULID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	defer st.Close()

	_, err := st.GetPolicy(ctx, testULID1)
	if !errors.Is(err, ErrUnknownULID) {
		t.Fatalf("GetPolicy(unknown) = %v; want ErrUnknownULID", err)
	}
}

func TestMemStore_InsertReport(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	defer st.Close()

	if err := st.InsertReport(ctx, testULID1, "1.2.0", "applied"); err != nil {
		t.Fatalf("InsertReport: %v", err)
	}
}

func TestMemStore_FeedNoPolicyUsesDefaults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := NewMemStore()
	defer st.Close()

	// Insert a stable release. ULID has no policy — default channel = stable.
	r := baseRelease()
	if _, err := st.InsertRelease(ctx, r); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// No policy set — FeedFor should succeed using default stable channel.
	rel, err := st.FeedFor(ctx, testULID1, "stable", "1.0.0")
	if err != nil {
		t.Fatalf("FeedFor(no policy): %v", err)
	}
	if rel.Version != "1.2.0" {
		t.Errorf("version = %q; want 1.2.0", rel.Version)
	}
}

func TestMemStore_FeedCohortInOut(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Find a ULID that is IN the 30% cohort for "1.4.0".
	var inCohort, outCohort string
	for i := 0; i < 1000; i++ {
		u := syntheticULID(i)
		if int(CohortHash(u, "1.4.0")) < 30 {
			inCohort = u
			break
		}
	}
	for i := 0; i < 1000; i++ {
		u := syntheticULID(i + 500)
		if int(CohortHash(u, "1.4.0")) >= 30 {
			outCohort = u
			break
		}
	}
	if inCohort == "" || outCohort == "" {
		t.Skip("could not find suitable test ULIDs")
	}

	st := NewMemStore()
	defer st.Close()
	r := baseRelease()
	r.Version = "1.4.0"
	r.RolloutPct = 30
	if _, err := st.InsertRelease(ctx, r); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// In-cohort: should receive the release.
	if _, err := st.FeedFor(ctx, inCohort, "stable", "1.0.0"); err != nil {
		t.Fatalf("FeedFor(in-cohort) = %v; want release", err)
	}

	// Out-of-cohort: should NOT receive the release.
	_, err := st.FeedFor(ctx, outCohort, "stable", "1.0.0")
	if !errors.Is(err, ErrNoUpgrade) {
		t.Fatalf("FeedFor(out-of-cohort) = %v; want ErrNoUpgrade", err)
	}
}
