package routing

import (
	"errors"
	"testing"
)

// A valid 26-char Crockford base32 ULID with no excluded chars (I/L/O/U) and
// first char <= '7' to avoid 128-bit overflow.
const validULID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

// ─────────────────────────────────────────────────────────────────────────────
// CanonULID
// ─────────────────────────────────────────────────────────────────────────────

func TestCanonULID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   string
		wantErr bool
		wantOut string // only checked when !wantErr
	}{
		// Happy path
		{
			name:    "valid uppercase → same",
			input:   validULID,
			wantErr: false,
			wantOut: validULID,
		},
		{
			name:    "valid lowercase → uppercased",
			input:   "01arz3ndektsv4rrffq69g5fav",
			wantErr: false,
			wantOut: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		},
		{
			name:    "valid mixed case → uppercased",
			input:   "01Arz3NdEkTsV4RrFFQ69G5Fav",
			wantErr: false,
			wantOut: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		},

		// Length violations
		{
			name:    "empty string → error",
			input:   "",
			wantErr: true,
		},
		{
			name:    "25 chars (too short) → error",
			input:   "01ARZ3NDEKTSV4RRFFQ69G5FA",
			wantErr: true,
		},
		{
			name:    "27 chars (too long) → error",
			input:   "01ARZ3NDEKTSV4RRFFQ69G5FAVX",
			wantErr: true,
		},

		// Crockford-excluded characters I, L, O, U (upper and lower)
		{
			name:    "contains I → error",
			input:   "01ARZ3NDEKTSV4RRFFQ69G5FIA",
			wantErr: true,
		},
		{
			name:    "contains L → error",
			input:   "01ARZ3NDEKTSV4RRFFQ69G5FLA",
			wantErr: true,
		},
		{
			name:    "contains O → error",
			input:   "01ARZ3NDEKTSV4RRFFQ69G5FOA",
			wantErr: true,
		},
		{
			name:    "contains U → error",
			input:   "01ARZ3NDEKTSV4RRFFQ69G5FUA",
			wantErr: true,
		},
		{
			name:    "contains lowercase i → error",
			input:   "01arz3ndektsv4rrffq69g5fia",
			wantErr: true,
		},
		{
			name:    "contains lowercase l → error",
			input:   "01arz3ndektsv4rrffq69g5fla",
			wantErr: true,
		},
		{
			name:    "contains lowercase o → error",
			input:   "01arz3ndektsv4rrffq69g5foa",
			wantErr: true,
		},
		{
			name:    "contains lowercase u → error",
			input:   "01arz3ndektsv4rrffq69g5fua",
			wantErr: true,
		},

		// 128-bit overflow: first character must be <= '7' in Crockford
		// '8' maps to value 8 in Crockford (0-9 map to 0-9) which is > 7.
		{
			name:    "first char 8 → 128-bit overflow → error",
			input:   "81ARZ3NDEKTSV4RRFFQ69G5FAV",
			wantErr: true,
		},
		{
			name:    "first char 9 → 128-bit overflow → error",
			input:   "91ARZ3NDEKTSV4RRFFQ69G5FAV",
			wantErr: true,
		},
		// First char '7' (value 7) is the maximum allowed.
		{
			name:    "first char 7 → no overflow → ok",
			input:   "71ARZ3NDEKTSV4RRFFQ69G5FAV",
			wantErr: false,
			wantOut: "71ARZ3NDEKTSV4RRFFQ69G5FAV",
		},

		// Invalid characters
		{
			name:    "contains hyphen → error",
			input:   "01ARZ3NDEKTSV4RRFFQ69G5F-V",
			wantErr: true,
		},
		{
			name:    "contains space → error",
			input:   "01ARZ3NDEKTSV4RRFFQ69G5F V",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := CanonULID(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("CanonULID(%q) = %q, nil; want error", tc.input, got)
				}
				if !errors.Is(err, ErrInvalidULID) {
					t.Fatalf("CanonULID(%q) error = %v; want ErrInvalidULID", tc.input, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("CanonULID(%q) unexpected error: %v", tc.input, err)
			}
			if got != tc.wantOut {
				t.Fatalf("CanonULID(%q) = %q; want %q", tc.input, got, tc.wantOut)
			}
			if len(got) != 26 {
				t.Fatalf("CanonULID result length = %d; want 26", len(got))
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ParseHost — OSS-parity vectors
// ─────────────────────────────────────────────────────────────────────────────

func TestParseHost(t *testing.T) {
	t.Parallel()
	base := "vulos.org"
	canon := validULID // canonical uppercase result

	cases := []struct {
		name        string
		host        string
		base        string
		wantApp     string
		wantProfile string
		wantULID    string
		wantErr     error // nil means success; non-nil is matched with errors.Is
	}{
		// ── Happy: bare ULID (no sub-label) ──────────────────────────────────
		{
			name:        "bare ulid.vulos.org → app=default profile=default",
			host:        validULID + ".vulos.org",
			base:        base,
			wantApp:     "default",
			wantProfile: "default",
			wantULID:    canon,
		},
		{
			name:        "bare ulid lowercase → canonicalised",
			host:        "01arz3ndektsv4rrffq69g5fav.vulos.org",
			base:        base,
			wantApp:     "default",
			wantProfile: "default",
			wantULID:    canon,
		},

		// ── Happy: app--profile.ulid.base ────────────────────────────────────
		{
			name:        "app--profile.ulid.base → correct decomposition",
			host:        "myapp--mypro." + validULID + ".vulos.org",
			base:        base,
			wantApp:     "myapp",
			wantProfile: "mypro",
			wantULID:    canon,
		},
		{
			name:        "terminal--default.ulid.base",
			host:        "terminal--default." + validULID + ".vulos.org",
			base:        base,
			wantApp:     "terminal",
			wantProfile: "default",
			wantULID:    canon,
		},

		// ── Happy: app.ulid.base (no profile → profile=default) ──────────────
		{
			name:        "app.ulid.base → profile=default",
			host:        "browser." + validULID + ".vulos.org",
			base:        base,
			wantApp:     "browser",
			wantProfile: "default",
			wantULID:    canon,
		},

		// ── Happy: port stripped ─────────────────────────────────────────────
		{
			name:        "host:port → port stripped",
			host:        validULID + ".vulos.org:8443",
			base:        base,
			wantApp:     "default",
			wantProfile: "default",
			wantULID:    canon,
		},
		{
			name:        "app--profile.ulid.base:443 → port stripped",
			host:        "app--profile." + validULID + ".vulos.org:443",
			base:        base,
			wantApp:     "app",
			wantProfile: "profile",
			wantULID:    canon,
		},

		// ── Happy: uppercase host → lowercased then matched ──────────────────
		{
			name:        "uppercase ULID in host → canonical",
			host:        "APP--PROF." + validULID + ".VULOS.ORG",
			base:        base,
			wantApp:     "app",
			wantProfile: "prof",
			wantULID:    canon,
		},

		// ── Happy: trailing dot stripped ─────────────────────────────────────
		{
			name:        "trailing dot on host → stripped",
			host:        validULID + ".vulos.org.",
			base:        base,
			wantApp:     "default",
			wantProfile: "default",
			wantULID:    canon,
		},

		// ── Error: wrong base domain ─────────────────────────────────────────
		{
			name:    "wrong base → ErrNotFound",
			host:    validULID + ".example.com",
			base:    base,
			wantErr: ErrNotFound,
		},
		{
			name:    "totally unrelated host → ErrNotFound",
			host:    "www.google.com",
			base:    base,
			wantErr: ErrNotFound,
		},

		// ── Error: deep nesting (more than one sub-label before ULID) ─────────
		{
			name:    "a.b.ulid.vulos.org → deep nesting → ErrNotFound",
			host:    "a.b." + validULID + ".vulos.org",
			base:    base,
			wantErr: ErrNotFound,
		},
		{
			name:    "x.y.z.ulid.vulos.org → deep nesting → ErrNotFound",
			host:    "x.y.z." + validULID + ".vulos.org",
			base:    base,
			wantErr: ErrNotFound,
		},

		// ── Error: a--b--c in sub-label ──────────────────────────────────────
		// "a--b--c" has two "--" separators; after splitting on first "--" we get
		// app="a", profile="b--c"; "b--c" contains "--" so ValidName rejects it.
		{
			name:    "a--b--c.ulid.base → triple-dash invalid profile → ErrInvalidName",
			host:    "a--b--c." + validULID + ".vulos.org",
			base:    base,
			wantErr: ErrInvalidName,
		},

		// ── Error: invalid ULID in host ───────────────────────────────────────
		{
			name:    "invalid ulid in host → ErrInvalidULID",
			host:    "notaulid.vulos.org",
			base:    base,
			wantErr: ErrInvalidULID,
		},
		{
			name:    "ulid with excluded char O in host → ErrInvalidULID",
			host:    "01ARZ3NDEKTSV4RRFFQ69G5FOV.vulos.org",
			base:    base,
			wantErr: ErrInvalidULID,
		},

		// ── Error: empty app/profile (label starts with --) ──────────────────
		// sub-label is "--foo" → app="" which is empty, rejected as ErrInvalidName.
		// Note: index of "--" is 0, so app = sub[:0] = "".
		{
			name:    "--profile.ulid.base → empty app → ErrInvalidName",
			host:    "--profile." + validULID + ".vulos.org",
			base:    base,
			wantErr: ErrInvalidName,
		},
		// sub-label is "app--" → app="app", profile="" → ValidName("") = false.
		{
			name:    "app--.ulid.base → empty profile → ErrInvalidName",
			host:    "app--." + validULID + ".vulos.org",
			base:    base,
			wantErr: ErrInvalidName,
		},

		// ── Edge: base domain is bare ULID (no subdomain prefix) but stripped ─
		{
			name:    "just base domain → ErrNotFound",
			host:    "vulos.org",
			base:    base,
			wantErr: ErrNotFound,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, profile, ulid, err := ParseHost(tc.host, tc.base)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ParseHost(%q, %q) error = %v; want %v", tc.host, tc.base, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseHost(%q, %q) unexpected error: %v", tc.host, tc.base, err)
			}
			if app != tc.wantApp {
				t.Errorf("app = %q; want %q", app, tc.wantApp)
			}
			if profile != tc.wantProfile {
				t.Errorf("profile = %q; want %q", profile, tc.wantProfile)
			}
			if ulid != tc.wantULID {
				t.Errorf("ulid = %q; want %q", ulid, tc.wantULID)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// NearestPoP
// ─────────────────────────────────────────────────────────────────────────────

func TestNearestPoP(t *testing.T) {
	t.Parallel()

	t.Run("empty slice → ErrNoHealthyPoP", func(t *testing.T) {
		t.Parallel()
		_, err := NearestPoP(nil, 0, 0)
		if !errors.Is(err, ErrNoHealthyPoP) {
			t.Fatalf("NearestPoP(nil) = %v; want ErrNoHealthyPoP", err)
		}
	})

	t.Run("all unhealthy → ErrNoHealthyPoP", func(t *testing.T) {
		t.Parallel()
		pops := []PoP{
			{ID: "a", Healthy: false, Lat: 0, Lon: 0},
			{ID: "b", Healthy: false, Lat: 10, Lon: 10},
		}
		_, err := NearestPoP(pops, 0, 0)
		if !errors.Is(err, ErrNoHealthyPoP) {
			t.Fatalf("NearestPoP(all-unhealthy) = %v; want ErrNoHealthyPoP", err)
		}
	})

	t.Run("single healthy PoP → returned", func(t *testing.T) {
		t.Parallel()
		pops := []PoP{
			{ID: "only", Healthy: true, Lat: 1, Lon: 2},
		}
		p, err := NearestPoP(pops, 1, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.ID != "only" {
			t.Fatalf("got PoP %q; want %q", p.ID, "only")
		}
	})

	t.Run("nearest of two healthy chosen", func(t *testing.T) {
		t.Parallel()
		// Query point (0,0); "near" is at (1,1), "far" is at (50,50).
		pops := []PoP{
			{ID: "far", Healthy: true, Lat: 50, Lon: 50},
			{ID: "near", Healthy: true, Lat: 1, Lon: 1},
		}
		p, err := NearestPoP(pops, 0, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.ID != "near" {
			t.Fatalf("got PoP %q; want %q", p.ID, "near")
		}
	})

	t.Run("unhealthy excluded even if closer", func(t *testing.T) {
		t.Parallel()
		// "sick" is right at query point but unhealthy; "well" is farther.
		pops := []PoP{
			{ID: "sick", Healthy: false, Lat: 0, Lon: 0},
			{ID: "well", Healthy: true, Lat: 40, Lon: 40},
		}
		p, err := NearestPoP(pops, 0, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.ID != "well" {
			t.Fatalf("got PoP %q; want %q", p.ID, "well")
		}
	})

	t.Run("mix healthy/unhealthy — nearest healthy wins", func(t *testing.T) {
		t.Parallel()
		pops := []PoP{
			{ID: "sick-close", Healthy: false, Lat: 1, Lon: 1},
			{ID: "sick-med", Healthy: false, Lat: 5, Lon: 5},
			{ID: "healthy-med", Healthy: true, Lat: 5, Lon: 6},
			{ID: "healthy-far", Healthy: true, Lat: 30, Lon: 30},
		}
		p, err := NearestPoP(pops, 0, 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.ID != "healthy-med" {
			t.Fatalf("got PoP %q; want %q", p.ID, "healthy-med")
		}
	})

	t.Run("exact co-location with query point → returned", func(t *testing.T) {
		t.Parallel()
		pops := []PoP{
			{ID: "exact", Healthy: true, Lat: -33.9, Lon: 18.4},
			{ID: "other", Healthy: true, Lat: 51.5, Lon: -0.1},
		}
		p, err := NearestPoP(pops, -33.9, 18.4)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.ID != "exact" {
			t.Fatalf("got PoP %q; want %q", p.ID, "exact")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// ValidConnMode
// ─────────────────────────────────────────────────────────────────────────────

func TestValidConnMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode ConnMode
		want bool
	}{
		{ModeFabric, true},
		{ModeDirect, true},
		{ModeOwnDomain, true},
		{"local", false}, // mode D — must never be valid
		{"d", false},
		{"", false},
		{"fabric ", false}, // trailing space
		{"FABRIC", false},  // wrong case
		{"Fabric", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.mode), func(t *testing.T) {
			t.Parallel()
			if got := ValidConnMode(tc.mode); got != tc.want {
				t.Fatalf("ValidConnMode(%q) = %v; want %v", tc.mode, got, tc.want)
			}
		})
	}
}
