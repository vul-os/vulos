package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// S3Config holds S3-compatible storage credentials.
type S3Config struct {
	Endpoint  string // e.g. s3.amazonaws.com, s3.wasabisys.com, minio:9000
	Bucket    string
	Region    string
	AccessKey string
	SecretKey string
	UseSSL    bool
}

// LoadS3Config reads S3 config from environment variables.
func LoadS3Config() *S3Config {
	return &S3Config{
		Endpoint:  getenv("S3_ENDPOINT", "s3.amazonaws.com"),
		Bucket:    getenv("S3_BUCKET", "vulos-vault"),
		Region:    getenv("S3_REGION", "us-east-1"),
		AccessKey: os.Getenv("S3_ACCESS_KEY"),
		SecretKey: os.Getenv("S3_SECRET_KEY"),
		UseSSL:    getenv("S3_USE_SSL", "true") == "true",
	}
}

// ResticEnv returns environment variables needed by Restic to access this S3 backend.
func (c *S3Config) ResticEnv() []string {
	scheme := "https"
	if !c.UseSSL {
		scheme = "http"
	}
	repo := fmt.Sprintf("s3:%s://%s/%s", scheme, c.Endpoint, c.Bucket)
	return []string{
		"RESTIC_REPOSITORY=" + repo,
		"AWS_ACCESS_KEY_ID=" + c.AccessKey,
		"AWS_SECRET_ACCESS_KEY=" + c.SecretKey,
		"AWS_DEFAULT_REGION=" + c.Region,
	}
}

// Configured returns true if credentials are present.
func (c *S3Config) Configured() bool {
	return c.AccessKey != "" && c.SecretKey != "" && c.Bucket != ""
}

// LocalStore provides local filesystem storage as fallback.
type LocalStore struct {
	Root string
}

func NewLocalStore(root string) *LocalStore {
	os.MkdirAll(root, 0755)
	return &LocalStore{Root: root}
}

func (s *LocalStore) Put(key string, r io.Reader) error {
	p := filepath.Join(s.Root, key)
	os.MkdirAll(filepath.Dir(p), 0755)
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

func (s *LocalStore) Get(key string) (io.ReadCloser, error) {
	return os.Open(filepath.Join(s.Root, key))
}

func (s *LocalStore) List(prefix string) ([]string, error) {
	var results []string
	root := filepath.Join(s.Root, prefix)
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(s.Root, path)
		results = append(results, rel)
		return nil
	})
	return results, nil
}

// HealthCheck verifies S3 connectivity with a simple HEAD request.
func (c *S3Config) HealthCheck(ctx context.Context) error {
	if !c.Configured() {
		return fmt.Errorf("s3 not configured")
	}
	scheme := "https"
	if !c.UseSSL {
		scheme = "http"
	}
	url := fmt.Sprintf("%s://%s/%s", scheme, c.Endpoint, c.Bucket)
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("s3 unreachable: %w", err)
	}
	resp.Body.Close()
	return nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
