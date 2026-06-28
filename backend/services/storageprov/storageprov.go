// Package storageprov provisions MinIO object storage for Vulos OS.
// It exposes POST /api/setup/storage and persists state to ~/.vulos/db/storage.json.
package storageprov

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// storageprovRequest is the body accepted by POST /api/setup/storage.
type storageprovRequest struct {
	Enable     bool   `json:"enable"`
	SizeGB     int    `json:"size_gb"`
	Password   string `json:"password"`
	Passphrase string `json:"passphrase"`
}

// storageprovState is persisted to storage.json (passphrase is never stored).
type storageprovState struct {
	Enabled    bool   `json:"enabled"`
	AccessKey  string `json:"access_key"`
	SecretKey  string `json:"secret_key"`
	SSECKey    string `json:"ssec_key"`
	BucketName string `json:"bucket_name"`
	// Region is the storage cell for this bucket (default "eu").
	// Phase-0 (single cell): BucketPlaceFor always returns "eu".
	// Additive field — existing storage.json files without this field
	// are read back with an empty Region (treated as "eu" by callers).
	Region        string    `json:"region,omitempty"`
	ProvisionedAt time.Time `json:"provisioned_at"`
}

// storageprovResponse is returned to the caller.
type storageprovResponse struct {
	Status       string `json:"status"`
	AccessKey    string `json:"access_key,omitempty"`
	SecretKey    string `json:"secret_key,omitempty"`
	BucketName   string `json:"bucket_name,omitempty"`
	MinIORunning bool   `json:"minio_running,omitempty"`
	Degraded     bool   `json:"degraded,omitempty"`
	DegradedMsg  string `json:"degraded_msg,omitempty"`
}

const storageprovBucket = "vulos-cluster"

var storageprovMu sync.Mutex

// BucketPlaceFor returns the storage region slug for the given region.
// Phase-0 (single cell): all regions resolve to "eu".
// To add a second storage cell later: extend this function and set VULOS_REGION
// on boxes in that cell — no other callers need to change.
func BucketPlaceFor(region string) string {
	// Phase-0: single cell. All regions map to "eu".
	return "eu"
}

// storageprovRegion returns the box's home region from the VULOS_REGION env
// variable, defaulting to "eu".
func storageprovRegion() string {
	if v := os.Getenv("VULOS_REGION"); v != "" {
		return v
	}
	return "eu"
}

// RegisterHandlers registers the storage provisioning endpoint on mux.
// home is the user home directory (e.g. os.UserHomeDir()).
func RegisterHandlers(mux *http.ServeMux, home string) {
	mux.HandleFunc("POST /api/setup/storage", func(w http.ResponseWriter, r *http.Request) {
		storageprovHandle(w, r, home)
	})
}

func storageprovHandle(w http.ResponseWriter, r *http.Request, home string) {
	var req storageprovRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		storageprovWriteErr(w, 400, "invalid request body")
		return
	}

	// enable:false → no-op 200
	if !req.Enable {
		storageprovWriteJSON(w, storageprovResponse{Status: "disabled"})
		return
	}

	storageprovMu.Lock()
	defer storageprovMu.Unlock()

	// Generate access/secret keys
	accessKey, err := storageprovRandBase64(15) // ~20 chars URL-safe
	if err != nil {
		storageprovWriteErr(w, 500, "failed to generate access key")
		return
	}
	secretKey, err := storageprovRandBase64(30) // ~40 chars
	if err != nil {
		storageprovWriteErr(w, 500, "failed to generate secret key")
		return
	}
	// SSE-C key: 32 random bytes, base64-encoded (256-bit AES key)
	ssecKeyBytes := make([]byte, 32)
	if _, err := rand.Read(ssecKeyBytes); err != nil {
		storageprovWriteErr(w, 500, "failed to generate SSE-C key")
		return
	}
	ssecKey := base64.StdEncoding.EncodeToString(ssecKeyBytes)

	// Attempt to install/start MinIO
	minioRunning, degraded, degradedMsg := storageprovEnsureMinIO(accessKey, secretKey, req.SizeGB)

	// Persist state (passphrase is intentionally excluded)
	dbDir := filepath.Join(home, ".vulos", "db")
	if err := os.MkdirAll(dbDir, 0700); err != nil {
		log.Printf("[storageprov] failed to create db dir: %v", err)
	}
	state := storageprovState{
		Enabled:       true,
		AccessKey:     accessKey,
		SecretKey:     secretKey,
		SSECKey:       ssecKey,
		BucketName:    storageprovBucket,
		Region:        BucketPlaceFor(storageprovRegion()),
		ProvisionedAt: time.Now().UTC(),
	}
	stateData, _ := json.MarshalIndent(state, "", "  ")
	stateFile := filepath.Join(dbDir, "storage.json")
	if err := os.WriteFile(stateFile, stateData, 0600); err != nil {
		log.Printf("[storageprov] failed to write storage.json: %v", err)
	}

	resp := storageprovResponse{
		Status:       "ok",
		AccessKey:    accessKey,
		SecretKey:    secretKey,
		BucketName:   storageprovBucket,
		MinIORunning: minioRunning,
		Degraded:     degraded,
		DegradedMsg:  degradedMsg,
	}
	storageprovWriteJSON(w, resp)
}

// storageprovEnsureMinIO attempts to find and start the minio binary, then create the bucket.
// Returns (running, degraded, message). Never panics.
func storageprovEnsureMinIO(accessKey, secretKey string, sizeGB int) (running, degraded bool, msg string) {
	// Check for minio binary
	minioPath, err := exec.LookPath("minio")
	if err != nil {
		return false, true, "minio binary not found in PATH; install MinIO to enable object storage"
	}

	// Check if minio is already running
	if storageprovMinIORunning() {
		_ = storageprovCreateBucket(accessKey, secretKey)
		return true, false, ""
	}

	// Start MinIO server in background
	dataDir := "/var/lib/vulos/minio-data"
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return false, true, fmt.Sprintf("failed to create minio data dir: %v", err)
	}

	cmd := exec.Command(minioPath, "server", dataDir, "--address", "127.0.0.1:9000")
	cmd.Env = append(os.Environ(),
		"MINIO_ROOT_USER="+accessKey,
		"MINIO_ROOT_PASSWORD="+secretKey,
	)
	if err := cmd.Start(); err != nil {
		return false, true, fmt.Sprintf("failed to start minio: %v", err)
	}

	// Give MinIO a moment to start up
	time.Sleep(2 * time.Second)

	// Create bucket (best effort)
	bucketErr := storageprovCreateBucket(accessKey, secretKey)
	if bucketErr != nil {
		log.Printf("[storageprov] bucket create: %v", bucketErr)
	}

	return true, false, ""
}

// storageprovMinIORunning checks if minio is already listening on :9000.
func storageprovMinIORunning() bool {
	out, err := exec.Command("sh", "-c", "curl -sf http://127.0.0.1:9000/minio/health/live").Output()
	_ = out
	return err == nil
}

// storageprovCreateBucket creates the vulos-cluster bucket using mc (MinIO Client) if available,
// falling back to a raw HTTP call.
func storageprovCreateBucket(accessKey, secretKey string) error {
	// Try mc (MinIO Client)
	mcPath, err := exec.LookPath("mc")
	if err == nil {
		// Configure alias and create bucket
		exec.Command(mcPath, "alias", "set", "vulos",
			"http://127.0.0.1:9000", accessKey, secretKey).Run() // nolint
		out, err := exec.Command(mcPath, "mb", "--ignore-existing",
			"vulos/"+storageprovBucket).CombinedOutput()
		if err != nil {
			return fmt.Errorf("mc mb: %s: %w", out, err)
		}
		return nil
	}

	// Fallback: raw S3 PUT bucket request
	req, err := http.NewRequest("PUT",
		"http://127.0.0.1:9000/"+storageprovBucket, nil)
	if err != nil {
		return err
	}
	// Minimal auth — enough for unsigned local calls in dev mode
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+accessKey)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("bucket PUT: %w", err)
	}
	resp.Body.Close()
	return nil
}

// storageprovRandBase64 returns n random bytes as a URL-safe base64 string.
func storageprovRandBase64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

func storageprovWriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func storageprovWriteErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
