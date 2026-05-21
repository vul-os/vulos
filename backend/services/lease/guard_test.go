package lease

import (
	"errors"
	"testing"
)

// TestCheckConsistency_NonTigrisAlwaysSafe verifies that non-Tigris backends
// (AWS S3, MinIO, Wasabi, etc.) are always treated as safe regardless of what
// BucketType is set to.
func TestCheckConsistency_NonTigrisAlwaysSafe(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
	}{
		{"aws-s3", "s3.amazonaws.com"},
		{"aws-regional", "s3.us-east-1.amazonaws.com"},
		{"minio-local", "localhost:9000"},
		{"minio-docker", "minio:9000"},
		{"wasabi", "s3.wasabisys.com"},
		{"backblaze", "s3.us-west-002.backblazeb2.com"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Even if someone accidentally sets an "unsafe" BucketType, non-Tigris
			// endpoints must pass.
			for _, bt := range []TigrisBucketType{"", TigrisGlobal, TigrisDualRegion, TigrisSingleRegion} {
				err := CheckConsistency(ConsistencyConfig{
					Endpoint:   tc.endpoint,
					BucketType: bt,
					StrictMode: true, // strictest possible
				})
				if err != nil {
					t.Errorf("endpoint=%q bucketType=%q: want nil, got %v", tc.endpoint, bt, err)
				}
			}
		})
	}
}

// TestCheckConsistency_TigrisSafeTypes verifies that Single-region and
// Multi-region Tigris buckets are accepted (no error, no refusal).
func TestCheckConsistency_TigrisSafeTypes(t *testing.T) {
	tigrisEndpoints := []string{
		"fly.storage.tigris.dev",
		"t3.storage.tigris.dev",
		"bucket.tigris.dev",
		"BUCKET.TIGRIS.DEV", // case-insensitive
	}
	safeTypes := []TigrisBucketType{
		TigrisSingleRegion,
		TigrisMultiRegion,
	}

	for _, ep := range tigrisEndpoints {
		for _, bt := range safeTypes {
			t.Run(string(ep)+"/"+string(bt), func(t *testing.T) {
				err := CheckConsistency(ConsistencyConfig{
					Endpoint:   ep,
					BucketType: bt,
					StrictMode: true,
				})
				if err != nil {
					t.Errorf("endpoint=%q bucketType=%q: expected safe, got %v", ep, bt, err)
				}
			})
		}
	}
}

// TestCheckConsistency_TigrisUnsafeStrict verifies that Global and Dual-region
// Tigris buckets are refused (ErrUnsafeBucket) when StrictMode is true.
func TestCheckConsistency_TigrisUnsafeStrict(t *testing.T) {
	unsafeTypes := []TigrisBucketType{
		TigrisGlobal,
		TigrisDualRegion,
	}

	for _, bt := range unsafeTypes {
		t.Run(string(bt), func(t *testing.T) {
			err := CheckConsistency(ConsistencyConfig{
				Endpoint:   "fly.storage.tigris.dev",
				BucketType: bt,
				StrictMode: true,
			})
			if err == nil {
				t.Fatalf("bucket type %q with StrictMode=true: expected ErrUnsafeBucket, got nil", bt)
			}
			if !errors.Is(err, ErrUnsafeBucket) {
				t.Errorf("bucket type %q: want ErrUnsafeBucket in error chain, got %v", bt, err)
			}
		})
	}
}

// TestCheckConsistency_TigrisUnsafeWarn verifies that Global and Dual-region
// Tigris buckets log a warning but return nil when StrictMode is false.
func TestCheckConsistency_TigrisUnsafeWarn(t *testing.T) {
	unsafeTypes := []TigrisBucketType{
		TigrisGlobal,
		TigrisDualRegion,
	}

	for _, bt := range unsafeTypes {
		t.Run(string(bt), func(t *testing.T) {
			err := CheckConsistency(ConsistencyConfig{
				Endpoint:   "fly.storage.tigris.dev",
				BucketType: bt,
				StrictMode: false, // warn mode
			})
			// In warn mode we return nil (manager is allowed to be constructed).
			if err != nil {
				t.Errorf("bucket type %q with StrictMode=false: expected nil (warn only), got %v", bt, err)
			}
		})
	}
}

// TestCheckConsistency_TigrisUnknownType verifies that an unset BucketType on a
// Tigris endpoint produces a warning (nil return) rather than a hard refusal.
func TestCheckConsistency_TigrisUnknownType(t *testing.T) {
	err := CheckConsistency(ConsistencyConfig{
		Endpoint:   "fly.storage.tigris.dev",
		BucketType: "", // unknown
		StrictMode: true,
	})
	// Unknown type should warn but not block (we can't prove it's unsafe).
	if err != nil {
		t.Errorf("unknown bucket type: expected nil (warn only), got %v", err)
	}
}

// TestNew_RefusesUnsafeTigrisInStrictMode verifies that Manager.New returns
// ErrUnsafeBucket when StrictConsistency=true and the bucket type is unsafe.
// We pass a deliberately bogus endpoint (no real server) so the check fires
// before minio.New reaches the network.
func TestNew_RefusesUnsafeTigrisInStrictMode(t *testing.T) {
	cfg := S3Config{
		Endpoint:          "fly.storage.tigris.dev",
		Bucket:            "my-bucket",
		AccessKey:         "AKID",
		SecretKey:         "SECRET",
		UseSSL:            true,
		BucketType:        TigrisGlobal,
		StrictConsistency: true,
	}

	_, err := New(cfg)
	if err == nil {
		t.Fatal("New with unsafe Tigris config in strict mode: expected error, got nil")
	}
	if !errors.Is(err, ErrUnsafeBucket) {
		t.Errorf("New: want ErrUnsafeBucket in chain, got %v", err)
	}
}

// TestNew_WarnsButAllowsUnsafeTigrisInWarnMode verifies that Manager.New does
// NOT return ErrUnsafeBucket when StrictConsistency=false (the default).
// The minio.New constructor will succeed (it only validates credentials format,
// not connectivity), so we get a non-nil Manager back.
func TestNew_WarnsButAllowsUnsafeTigrisInWarnMode(t *testing.T) {
	cfg := S3Config{
		Endpoint:          "fly.storage.tigris.dev",
		Bucket:            "my-bucket",
		AccessKey:         "AKID",
		SecretKey:         "SECRET",
		UseSSL:            true,
		BucketType:        TigrisDualRegion,
		StrictConsistency: false, // warn mode — default
	}

	mgr, err := New(cfg)
	if errors.Is(err, ErrUnsafeBucket) {
		t.Fatalf("New in warn mode: must not return ErrUnsafeBucket, got %v", err)
	}
	// Any error other than ErrUnsafeBucket is fine here (e.g. minio connection
	// errors are irrelevant to this test).
	if err == nil && mgr == nil {
		t.Error("New returned nil Manager and nil error — unexpected")
	}
}

// TestIsTigrisEndpoint exercises the internal hostname detector.
func TestIsTigrisEndpoint(t *testing.T) {
	cases := []struct {
		endpoint string
		want     bool
	}{
		{"fly.storage.tigris.dev", true},
		{"t3.storage.tigris.dev", true},
		{"bucket.tigris.dev", true},
		{"BUCKET.TIGRIS.DEV", true}, // case insensitive
		{"s3.amazonaws.com", false},
		{"localhost:9000", false},
		{"minio:9000", false},
		{"s3.wasabisys.com", false},
		{"tigris-lookalike.example.com", false}, // must not false-positive
	}

	for _, tc := range cases {
		got := isTigrisEndpoint(tc.endpoint)
		if got != tc.want {
			t.Errorf("isTigrisEndpoint(%q) = %v, want %v", tc.endpoint, got, tc.want)
		}
	}
}
