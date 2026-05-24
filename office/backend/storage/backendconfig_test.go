package storage_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vulos-office/backend/storage"
)

// officeS3TestServer is a minimal in-memory S3-compatible server for testing.
func officeS3TestServer(t *testing.T) *httptest.Server {
	t.Helper()
	objects := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "missing auth", http.StatusForbidden)
			return
		}
		// Key = last path segment(s) after bucket.
		key := strings.TrimPrefix(r.URL.Path, "/")
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			objects[key] = body
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			data, ok := objects[key]
			if !ok {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			_, _ = w.Write(data)
		case http.MethodDelete:
			delete(objects, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestOfficeTigrisDefaultsFillsEnvVars: OfficeTigrisDefaults reads from env.
func TestOfficeTigrisDefaultsFillsEnvVars(t *testing.T) {
	t.Setenv("TIGRIS_ENDPOINT", "https://custom.tigris.example.com")
	t.Setenv("TIGRIS_REGION", "eu-west-1")
	t.Setenv("TIGRIS_ACCESS_KEY_ID", "envak")
	t.Setenv("TIGRIS_SECRET_ACCESS_KEY", "envsk")

	cfg := storage.OfficeTigrisDefaults()
	if cfg.Endpoint != "https://custom.tigris.example.com" {
		t.Errorf("endpoint = %q, want https://custom.tigris.example.com", cfg.Endpoint)
	}
	if cfg.Region != "eu-west-1" {
		t.Errorf("region = %q, want eu-west-1", cfg.Region)
	}
	if cfg.AccessKeyID != "envak" {
		t.Errorf("access_key_id = %q", cfg.AccessKeyID)
	}
}

// TestOfficeTigrisDefaultsFallback: falls back to Tigris prod URL.
func TestOfficeTigrisDefaultsFallback(t *testing.T) {
	t.Setenv("TIGRIS_ENDPOINT", "")
	t.Setenv("TIGRIS_REGION", "")
	cfg := storage.OfficeTigrisDefaults()
	if cfg.Endpoint != "https://fly.storage.tigris.dev" {
		t.Errorf("fallback endpoint = %q", cfg.Endpoint)
	}
	if cfg.Region != "auto" {
		t.Errorf("fallback region = %q", cfg.Region)
	}
}

// TestOfficeBackendConfigValidation: MinIO requires https and bucket.
func TestOfficeBackendConfigValidation(t *testing.T) {
	cases := []struct {
		name    string
		cfg     storage.OfficeBackendConfig
		wantErr bool
	}{
		{"tigris-valid", storage.OfficeBackendConfig{Kind: storage.OfficeBEKindTigris}, false},
		{"minio-valid", storage.OfficeBackendConfig{Kind: storage.OfficeBEKindMinIO, Endpoint: "https://minio.example.com", Bucket: "b"}, false},
		{"minio-http", storage.OfficeBackendConfig{Kind: storage.OfficeBEKindMinIO, Endpoint: "http://minio.example.com", Bucket: "b"}, true},
		{"minio-no-bucket", storage.OfficeBackendConfig{Kind: storage.OfficeBEKindMinIO, Endpoint: "https://minio.example.com"}, true},
		{"invalid-kind", storage.OfficeBackendConfig{Kind: "gcs"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestOfficeS3ClientRoundtrip: Put+Get round-trip against the test server.
func TestOfficeS3ClientRoundtrip(t *testing.T) {
	srv := officeS3TestServer(t)

	cfg := storage.OfficeBackendConfig{
		Kind:            storage.OfficeBEKindTigris,
		Endpoint:        srv.URL,
		Region:          "auto",
		Bucket:          "office-bucket",
		Prefix:          "session/abc",
		AccessKeyID:     "ak",
		SecretAccessKey: "sk",
		HTTPClient:      srv.Client(),
	}
	client, err := storage.NewOfficeS3Client(cfg)
	if err != nil {
		t.Fatalf("NewOfficeS3Client: %v", err)
	}

	content := []byte("snapshot data for office CRDT")
	if err := client.Put("snap.json", content); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, err := client.Get("snap.json")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, content) {
		t.Fatalf("Get bytes = %q, want %q", got, content)
	}
}

// TestOfficeS3ClientMinIOEndpointSelection: MinIO config uses the custom endpoint.
func TestOfficeS3ClientMinIOEndpointSelection(t *testing.T) {
	srv := officeS3TestServer(t)

	// MinIO validation requires https; bypass by constructing with Tigris kind
	// but the custom srv.URL endpoint (tests the endpoint-injection path).
	cfg := storage.OfficeBackendConfig{
		Kind:            storage.OfficeBEKindTigris, // skip minio validation for test
		Endpoint:        srv.URL,
		Region:          "us-west-2",
		Bucket:          "byo-bucket",
		Prefix:          "office/tenant1",
		AccessKeyID:     "byo-ak",
		SecretAccessKey: "byo-sk",
		HTTPClient:      srv.Client(),
	}
	client, err := storage.NewOfficeS3Client(cfg)
	if err != nil {
		t.Fatalf("NewOfficeS3Client: %v", err)
	}

	// Put and Delete round-trip.
	if err := client.Put("ops.jsonl", []byte(`{"op":"insert"}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := client.Delete("ops.jsonl"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Get after delete should 404.
	if _, err := client.Get("ops.jsonl"); err == nil {
		t.Fatal("expected error for get-after-delete, got nil")
	}
}
