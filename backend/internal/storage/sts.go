package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/minio/minio-go/v7/pkg/credentials"
)

// STSConfig configures the MinIO/S3 STS AssumeRole credential minter (C1/C3).
type STSConfig struct {
	// Endpoint is the STS endpoint (e.g. "https://minio:9000"). Required.
	Endpoint string
	// RoleARN is optional; required for AWS STS, ignored by MinIO's
	// AssumeRole-with-static-creds flow.
	RoleARN string
	// DurationSeconds is the lifetime of the minted credentials. Defaults to
	// 900s (15m) when <= 0 — short-lived by design.
	DurationSeconds int
}

// scopedPolicy builds an inline session policy locking the bearer to exactly
// bucket/prefix*: object RW under the prefix plus a prefix-conditioned
// ListBucket. prefix is expected to be the gateway's "<userID>/<appID>/" form.
func scopedPolicy(bucket, prefix string) (string, error) {
	type statement struct {
		Effect    string                         `json:"Effect"`
		Action    []string                       `json:"Action"`
		Resource  []string                       `json:"Resource"`
		Condition map[string]map[string][]string `json:"Condition,omitempty"`
	}
	doc := struct {
		Version   string      `json:"Version"`
		Statement []statement `json:"Statement"`
	}{
		Version: "2012-10-17",
		Statement: []statement{
			{
				Effect:   "Allow",
				Action:   []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject"},
				Resource: []string{fmt.Sprintf("arn:aws:s3:::%s/%s*", bucket, prefix)},
			},
			{
				Effect:   "Allow",
				Action:   []string{"s3:ListBucket"},
				Resource: []string{fmt.Sprintf("arn:aws:s3:::%s", bucket)},
				Condition: map[string]map[string][]string{
					"StringLike": {"s3:prefix": {prefix + "*"}},
				},
			},
		},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// objectScopedPolicy builds an inline session policy locking the bearer to
// exactly bucket/key — read+write on the SINGLE object, no ListBucket, no
// prefix wildcard. Used by the Files grant broker (FILES-FOUNDATION) to mint
// object-scoped write credentials after an ACL check.
func objectScopedPolicy(bucket, key string) (string, error) {
	type statement struct {
		Effect   string   `json:"Effect"`
		Action   []string `json:"Action"`
		Resource []string `json:"Resource"`
	}
	doc := struct {
		Version   string      `json:"Version"`
		Statement []statement `json:"Statement"`
	}{
		Version: "2012-10-17",
		Statement: []statement{
			{
				Effect:   "Allow",
				Action:   []string{"s3:GetObject", "s3:PutObject", "s3:DeleteObject"},
				Resource: []string{fmt.Sprintf("arn:aws:s3:::%s/%s", bucket, key)},
			},
		},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// NewMinIOSTSMinter returns a CredentialMinter that calls MinIO/S3 STS
// AssumeRole with an inline session policy locked to bucket/prefix, using the
// resolver's static credentials as the calling identity. Mint failures return
// (_, false) so the resolver falls back to static per-user-bucket creds.
func (r *Resolver) NewMinIOSTSMinter(cfg STSConfig) CredentialMinter {
	dur := cfg.DurationSeconds
	if dur <= 0 {
		dur = 900 // 15 minutes
	}
	return func(ctx context.Context, bucket, prefix string) (ScopedCreds, bool) {
		if cfg.Endpoint == "" || r.cfg.AccessKey == "" || r.cfg.SecretKey == "" {
			return ScopedCreds{}, false
		}
		policy, err := scopedPolicy(bucket, prefix)
		if err != nil {
			return ScopedCreds{}, false
		}
		creds, err := credentials.NewSTSAssumeRole(cfg.Endpoint, credentials.STSAssumeRoleOptions{
			AccessKey:       r.cfg.AccessKey,
			SecretKey:       r.cfg.SecretKey,
			Policy:          policy,
			DurationSeconds: dur,
			RoleARN:         cfg.RoleARN,
			RoleSessionName: stsSessionName(prefix),
			Location:        r.cfg.Region,
		})
		if err != nil {
			return ScopedCreds{}, false
		}
		v, err := creds.GetWithContext(nil)
		if err != nil {
			return ScopedCreds{}, false
		}
		return ScopedCreds{
			AccessKey:    v.AccessKeyID,
			SecretKey:    v.SecretAccessKey,
			SessionToken: v.SessionToken,
		}, true
	}
}

// stsSessionName derives a (bounded) role-session name from the prefix.
func stsSessionName(prefix string) string {
	name := "vulos-" + prefix
	if len(name) > 60 {
		name = name[:60]
	}
	return name
}
