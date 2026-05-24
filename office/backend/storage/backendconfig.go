// backendconfig.go — per-account S3 storage-backend config resolver for
// vulos-office (TASK: OFFICE-STORE-01).
//
// The office backend persists CRDT snapshots, op-logs, and file attachments to
// an S3-compatible object store. This file provides the resolver layer so the
// endpoint and credentials are sourced from the account's StorageBackend
// (configured via the Vulos cloud control-plane, or injected at instance
// startup for BYO) instead of being hardcoded to Tigris.
//
// Two backends are supported:
//
//	OfficeBEKindTigris — Vulos-managed Tigris (default). Endpoint and creds
//	  are read from TIGRIS_ENDPOINT / TIGRIS_REGION / TIGRIS_ACCESS_KEY_ID /
//	  TIGRIS_SECRET_ACCESS_KEY environment variables.
//
//	OfficeBEKindMinIO — Customer-provided MinIO / S3-compatible. Endpoint and
//	  creds are supplied explicitly in OfficeBackendConfig (BYO self-host flow).
//
// No CGO is used or required.
package storage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// OfficeBEKind identifies the storage backend family for office.
type OfficeBEKind string

const (
	// OfficeBEKindTigris uses Vulos-managed Tigris object storage (default).
	OfficeBEKindTigris OfficeBEKind = "tigris"

	// OfficeBEKindMinIO uses a customer-provided S3-compatible endpoint (BYO).
	OfficeBEKindMinIO OfficeBEKind = "minio"
)

// OfficeBackendConfig describes the S3 backend for a single office instance/account.
type OfficeBackendConfig struct {
	// Kind selects Tigris (managed default) or MinIO (BYO).
	Kind OfficeBEKind

	// Endpoint is the S3-compatible base URL.
	// For Tigris this may be empty (read from TIGRIS_ENDPOINT env var).
	// For MinIO this must start with "https://".
	Endpoint string

	// Region defaults to "auto" if empty.
	Region string

	// Bucket is the object storage bucket name. Required for MinIO.
	Bucket string

	// Prefix scopes all objects to a per-account/session key prefix.
	Prefix string

	// AccessKeyID and SecretAccessKey are S3 credentials.
	// For Tigris, empty values are filled from TIGRIS_* env vars.
	// For MinIO they must be provided explicitly.
	AccessKeyID     string
	SecretAccessKey string

	// HTTPClient is injected for tests. Nil uses http.DefaultClient.
	HTTPClient *http.Client
}

// Validate returns an error if the OfficeBackendConfig is malformed.
func (c OfficeBackendConfig) Validate() error {
	switch c.Kind {
	case OfficeBEKindTigris, OfficeBEKindMinIO:
	default:
		return fmt.Errorf("storage: officebackend: unknown kind %q (want tigris|minio)", c.Kind)
	}
	if c.Kind == OfficeBEKindMinIO {
		if !strings.HasPrefix(c.Endpoint, "https://") {
			return errors.New("storage: officebackend: minio endpoint must be https://…")
		}
		if c.Bucket == "" {
			return errors.New("storage: officebackend: bucket required for minio")
		}
	}
	return nil
}

// OfficeTigrisDefaults returns an OfficeBackendConfig pre-filled from the
// canonical Tigris environment variables.
func OfficeTigrisDefaults() OfficeBackendConfig {
	endpoint := os.Getenv("TIGRIS_ENDPOINT")
	if endpoint == "" {
		endpoint = "https://fly.storage.tigris.dev"
	}
	region := os.Getenv("TIGRIS_REGION")
	if region == "" {
		region = "auto"
	}
	return OfficeBackendConfig{
		Kind:            OfficeBEKindTigris,
		Endpoint:        endpoint,
		Region:          region,
		AccessKeyID:     os.Getenv("TIGRIS_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("TIGRIS_SECRET_ACCESS_KEY"),
	}
}

// OfficeS3Client is a minimal S3-compatible object client for the office
// storage backend. It supports Put, Get, and Delete via SigV4-signed requests.
// All operations are S3-compatible (works with Tigris and MinIO).
type OfficeS3Client struct {
	endpoint        string
	region          string
	bucket          string
	prefix          string
	accessKeyID     string
	secretAccessKey string
	httpClient      *http.Client
}

// NewOfficeS3Client constructs an S3 client from an OfficeBackendConfig.
// It validates the config, filling Tigris env-defaults for empty Tigris fields.
func NewOfficeS3Client(cfg OfficeBackendConfig) (*OfficeS3Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Fill Tigris env-defaults.
	if cfg.Kind == OfficeBEKindTigris {
		if cfg.Endpoint == "" {
			cfg.Endpoint = os.Getenv("TIGRIS_ENDPOINT")
		}
		if cfg.Endpoint == "" {
			cfg.Endpoint = "https://fly.storage.tigris.dev"
		}
		if cfg.Region == "" {
			cfg.Region = os.Getenv("TIGRIS_REGION")
		}
		if cfg.AccessKeyID == "" {
			cfg.AccessKeyID = os.Getenv("TIGRIS_ACCESS_KEY_ID")
		}
		if cfg.SecretAccessKey == "" {
			cfg.SecretAccessKey = os.Getenv("TIGRIS_SECRET_ACCESS_KEY")
		}
	}
	if cfg.Region == "" {
		cfg.Region = "auto"
	}

	c := cfg.HTTPClient
	if c == nil {
		c = http.DefaultClient
	}
	return &OfficeS3Client{
		endpoint:        strings.TrimSuffix(cfg.Endpoint, "/"),
		region:          cfg.Region,
		bucket:          cfg.Bucket,
		prefix:          strings.TrimSuffix(cfg.Prefix, "/"),
		accessKeyID:     cfg.AccessKeyID,
		secretAccessKey: cfg.SecretAccessKey,
		httpClient:      c,
	}, nil
}

func (c *OfficeS3Client) key(name string) string {
	if c.prefix == "" {
		return name
	}
	return c.prefix + "/" + name
}

func (c *OfficeS3Client) objURL(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = urlPathEscape(p)
	}
	return c.endpoint + "/" + urlPathEscape(c.bucket) + "/" + strings.Join(parts, "/")
}

// Put uploads body to the object store under name. Returns an error on failure.
func (c *OfficeS3Client) Put(name string, body []byte) error {
	req, err := c.signed(http.MethodPut, c.key(name), body)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("storage: s3 put %q: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("storage: s3 put %q: status %d", name, resp.StatusCode)
	}
	return nil
}

// Get downloads the object named by name. Caller must close the returned body.
func (c *OfficeS3Client) Get(name string) (io.ReadCloser, error) {
	req, err := c.signed(http.MethodGet, c.key(name), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("storage: s3 get %q: %w", name, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("storage: s3 get %q: not found", name)
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("storage: s3 get %q: status %d", name, resp.StatusCode)
	}
	return resp.Body, nil
}

// Delete removes the object named by name.
func (c *OfficeS3Client) Delete(name string) error {
	req, err := c.signed(http.MethodDelete, c.key(name), nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("storage: s3 delete %q: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil // idempotent
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("storage: s3 delete %q: status %d", name, resp.StatusCode)
	}
	return nil
}

// signed returns an http.Request signed with AWS SigV4.
func (c *OfficeS3Client) signed(method, key string, body []byte) (*http.Request, error) {
	u := c.objURL(key)
	req, err := http.NewRequest(method, u, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("storage: build request: %w", err)
	}
	c.signV4(req, body, time.Now().UTC())
	return req, nil
}

func (c *OfficeS3Client) signV4(req *http.Request, body []byte, t time.Time) {
	const service = "s3"
	amzDate := t.Format("20060102T150405Z")
	dateStamp := t.Format("20060102")
	payloadHash := sha256hex(body)

	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("Host", req.URL.Host)

	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	sort.Strings(signed)
	var canonHeaders strings.Builder
	for _, h := range signed {
		var v string
		switch h {
		case "host":
			v = req.URL.Host
		case "x-amz-content-sha256":
			v = payloadHash
		case "x-amz-date":
			v = amzDate
		}
		canonHeaders.WriteString(h + ":" + v + "\n")
	}
	signedHeaders := strings.Join(signed, ";")

	canonRequest := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		req.URL.RawQuery,
		canonHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, c.region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256hex([]byte(canonRequest)),
	}, "\n")

	kDate := hmacSHA256key([]byte("AWS4"+c.secretAccessKey), []byte(dateStamp))
	kRegion := hmacSHA256key(kDate, []byte(c.region))
	kService := hmacSHA256key(kRegion, []byte(service))
	kSigning := hmacSHA256key(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256key(kSigning, []byte(stringToSign)))

	auth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.accessKeyID, scope, signedHeaders, signature)
	req.Header.Set("Authorization", auth)
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256key(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// urlPathEscape escapes a single path segment (not the whole URL).
func urlPathEscape(s string) string {
	const safeChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	var b strings.Builder
	for _, c := range []byte(s) {
		if strings.IndexByte(safeChars, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
