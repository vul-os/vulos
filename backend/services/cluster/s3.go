// Package cluster provides the S3 cluster client for Vula OS multi-node sync.
// It wraps github.com/minio/minio-go/v7 with SSE-C encryption (server-side encryption
// with customer-provided keys) and derives the 256-bit AES key from a passphrase
// using Argon2id (time=3, mem=64 MiB, threads=4, keyLen=32).
package cluster

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/encrypt"
	"golang.org/x/crypto/argon2"
)

const (
	saltObjectKey = "cluster/encryption-salt"
	saltLen       = 32
)

// Argon2id parameters — memory-hard, resistant to GPU/ASIC attacks.
const (
	argonTime    uint32 = 3
	argonMem     uint32 = 64 * 1024 // 64 MiB
	argonThreads uint8  = 4
	argonKeyLen  uint32 = 32
)

// S3Config holds credentials read from VULOS_S3_* environment variables.
type S3Config struct {
	Endpoint  string // e.g. "localhost:9000" or "s3.amazonaws.com"
	Bucket    string
	AccessKey string
	SecretKey string
	UseSSL    bool
}

// LoadS3Config reads S3 credentials from the VULOS_S3_* environment variables.
// Returns an empty config (not an error) when variables are absent — callers
// should check Configured() before use.
func LoadS3Config() S3Config {
	return S3Config{
		Endpoint:  getenvOrDefault("VULOS_S3_ENDPOINT", "localhost:9000"),
		Bucket:    getenvOrDefault("VULOS_S3_BUCKET", "vulos-cluster"),
		AccessKey: os.Getenv("VULOS_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("VULOS_S3_SECRET_KEY"),
		UseSSL:    getenvOrDefault("VULOS_S3_USE_SSL", "false") == "true",
	}
}

// Configured returns true when the minimum credentials are present.
func (c S3Config) Configured() bool {
	return c.AccessKey != "" && c.SecretKey != "" && c.Bucket != ""
}

// Client is a thin wrapper around a minio.Client that transparently encrypts
// and decrypts objects using SSE-C with an Argon2id-derived key.
type Client struct {
	mc     *minio.Client
	bucket string
	encKey []byte // 32-byte AES key in memory
}

// NewClient creates a new cluster Client.
// It derives the encryption key from passphrase+salt; the salt is fetched from
// S3 (at cluster/encryption-salt) or created and uploaded if absent.
// ctx is used only during construction for the salt fetch/create round-trip.
func NewClient(ctx context.Context, cfg S3Config, passphrase string) (*Client, error) {
	if !cfg.Configured() {
		return nil, errors.New("cluster: S3 not configured (check VULOS_S3_* env vars)")
	}

	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("cluster: minio.New: %w", err)
	}

	salt, err := fetchOrCreateSalt(ctx, mc, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("cluster: salt: %w", err)
	}

	key := deriveKey(passphrase, salt)

	return &Client{
		mc:     mc,
		bucket: cfg.Bucket,
		encKey: key,
	}, nil
}

// NewClientWithKey creates a Client using a pre-derived 32-byte key.
// Useful in tests where the salt is managed externally.
func NewClientWithKey(cfg S3Config, key []byte) (*Client, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("cluster: encryption key must be 32 bytes, got %d", len(key))
	}
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("cluster: minio.New: %w", err)
	}
	keyCopy := make([]byte, 32)
	copy(keyCopy, key)
	return &Client{
		mc:     mc,
		bucket: cfg.Bucket,
		encKey: keyCopy,
	}, nil
}

// PutEncrypted uploads data to bucket/key encrypted with SSE-C.
func (c *Client) PutEncrypted(ctx context.Context, key string, data []byte) error {
	sse, err := encrypt.NewSSEC(c.encKey)
	if err != nil {
		return fmt.Errorf("cluster: SSE-C init: %w", err)
	}
	r := bytes.NewReader(data)
	_, err = c.mc.PutObject(ctx, c.bucket, key, r, int64(len(data)), minio.PutObjectOptions{
		ServerSideEncryption: sse,
	})
	if err != nil {
		return fmt.Errorf("cluster: put %q: %w", key, err)
	}
	return nil
}

// GetEncrypted downloads and decrypts an SSE-C encrypted object from bucket/key.
// Returns the plaintext bytes. Returns an error if the key is wrong or the
// object does not exist.
func (c *Client) GetEncrypted(ctx context.Context, key string) ([]byte, error) {
	sse, err := encrypt.NewSSEC(c.encKey)
	if err != nil {
		return nil, fmt.Errorf("cluster: SSE-C init: %w", err)
	}
	obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{
		ServerSideEncryption: sse,
	})
	if err != nil {
		return nil, fmt.Errorf("cluster: get %q: %w", key, err)
	}
	defer obj.Close()

	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, fmt.Errorf("cluster: read %q: %w", key, err)
	}
	return data, nil
}

// deriveKey returns a 32-byte AES key using Argon2id with the project parameters.
func deriveKey(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, argonTime, argonMem, argonThreads, argonKeyLen)
}

// fetchOrCreateSalt retrieves the cluster encryption salt from S3.
// If the salt object does not yet exist it generates a cryptographically random
// salt, uploads it unencrypted (the salt is public metadata by design), and
// returns it. Subsequent calls on other nodes will read back the same salt.
func fetchOrCreateSalt(ctx context.Context, mc *minio.Client, bucket string) ([]byte, error) {
	obj, err := mc.GetObject(ctx, bucket, saltObjectKey, minio.GetObjectOptions{})
	if err == nil {
		defer obj.Close()
		salt, err := io.ReadAll(obj)
		if err == nil && len(salt) == saltLen {
			return salt, nil
		}
		// Truncated or empty — fall through to create a fresh one.
	}

	// Check whether the error is "key not found" (NoSuchKey) or something else.
	if err != nil {
		errResp := minio.ToErrorResponse(err)
		if errResp.Code != "NoSuchKey" {
			return nil, fmt.Errorf("get salt: %w", err)
		}
	}

	// Generate a fresh salt and upload it.
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}

	r := bytes.NewReader(salt)
	_, err = mc.PutObject(ctx, bucket, saltObjectKey, r, int64(saltLen), minio.PutObjectOptions{
		ContentType: "application/octet-stream",
	})
	if err != nil {
		return nil, fmt.Errorf("upload salt: %w", err)
	}
	return salt, nil
}

func getenvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
