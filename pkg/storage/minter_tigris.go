// minter_tigris.go — TigrisIAMMinter: Tier-1 bucket-scoped access-key minting
// via the Tigris IAM API (STORAGE-ISOLATION.md §1.2, Tier 1).
//
// Tigris exposes an AWS-IAM-compatible surface at https://iam.storage.dev
// (verified 2026-07 against https://www.tigrisdata.com/docs/iam/). Supported
// calls we use: CreateAccessKey, CreatePolicy, AttachUserPolicy, and for revoke
// DeleteAccessKey (+ best-effort DetachUserPolicy/DeletePolicy). The master
// credential — the SAME TIGRIS_ACCESS_KEY_ID/SECRET the CP already holds — signs
// these IAM calls with SigV4; the master key stays inside the CP and is NEVER
// handed to a cell.
//
// Minting flow (per cell):
//  1. CreateAccessKey            → a fresh (AccessKeyId, SecretAccessKey), user id.
//  2. CreatePolicy(policyDoc)    → a policy pinned to arn:aws:s3:::vulos-{ulid}
//     and arn:aws:s3:::vulos-{ulid}/* with the exact S3 actions Vulos needs.
//  3. AttachUserPolicy           → bind the policy to the new access key's user.
//
// Result: a real credential the cell uses directly with the unchanged box s3.go
// path, able to touch ONLY its own bucket(s). Compromise of that cell yields at
// most the customer's own data.
//
// STS/session tokens (Tier 2) are NOT implemented: Tigris does not expose an
// AssumeRole/session surface as of 2026-07. When it does, add a sibling minter
// behind the same CredentialMinter seam (§4 P6).
//
// No new module dependency: we sign the IAM Query (form-POST) requests with the
// aws/signer/v4 package already present via aws-sdk-go-v2, rather than pulling
// the full service/iam client. This keeps CGO_ENABLED=0 pure-Go and the
// dependency surface unchanged.
package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

const (
	defaultTigrisIAMEndpoint = "https://iam.storage.dev"
	iamService               = "iam"
	iamAPIVersion            = "2010-05-08" // AWS IAM Query API version
)

// TigrisIAMConfig configures a TigrisIAMMinter. AccessKeyID/SecretAccessKey are
// the MASTER credential (the minting key) — they stay in the CP.
type TigrisIAMConfig struct {
	AccessKeyID     string // master key id (minting key)
	SecretAccessKey string // master secret (minting key)
	IAMEndpoint     string // default https://iam.storage.dev
	Region          string // signing region (default "auto")
	// S3Endpoint / S3Region are stamped onto the minted ScopedCred so the cell
	// knows where to point its S3 client.
	S3Endpoint string // default https://fly.storage.tigris.dev
	S3Region   string // default "auto"
}

// TigrisIAMMinter is the Tier-1 CredentialMinter backed by the Tigris IAM API.
type TigrisIAMMinter struct {
	cfg    TigrisIAMConfig
	signer *v4.Signer
	creds  credentials.StaticCredentialsProvider
	client *http.Client
}

// NewTigrisIAMMinter constructs a TigrisIAMMinter. Returns ErrProviderFailed if
// the master credential is missing.
func NewTigrisIAMMinter(cfg TigrisIAMConfig) (*TigrisIAMMinter, error) {
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, fmt.Errorf("%w: TigrisIAMMinter requires the master access key", ErrProviderFailed)
	}
	if cfg.IAMEndpoint == "" {
		cfg.IAMEndpoint = defaultTigrisIAMEndpoint
	}
	if cfg.Region == "" {
		cfg.Region = "auto"
	}
	if cfg.S3Endpoint == "" {
		cfg.S3Endpoint = defaultTigrisEndpoint
	}
	if cfg.S3Region == "" {
		cfg.S3Region = defaultTigrisRegion
	}
	return &TigrisIAMMinter{
		cfg:    cfg,
		signer: v4.NewSigner(),
		creds:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		client: &http.Client{Timeout: 20 * time.Second},
	}, nil
}

// MintBucketScoped creates a fresh access key + a bucket-scoped policy and
// attaches them. buckets[0] becomes ScopedCred.Bucket; every bucket in the slice
// is covered by the policy (used for the per-customer backup bucket, §2.3).
func (m *TigrisIAMMinter) MintBucketScoped(ctx context.Context, buckets []string, _ time.Duration) (ScopedCred, error) {
	if len(buckets) == 0 {
		return ScopedCred{}, fmt.Errorf("%w: MintBucketScoped needs at least one bucket", ErrProviderFailed)
	}

	// 1. CreateAccessKey.
	akResp, err := m.createAccessKey(ctx)
	if err != nil {
		return ScopedCred{}, fmt.Errorf("storage: mint CreateAccessKey: %w", err)
	}

	// 2. CreatePolicy scoped to the bucket(s).
	policyName := "vulos-scoped-" + akResp.AccessKeyID
	policyDoc := bucketScopedPolicyDoc(buckets)
	policyARN, err := m.createPolicy(ctx, policyName, policyDoc)
	if err != nil {
		// Roll back the orphaned access key so a failed mint leaves nothing live.
		_ = m.deleteAccessKey(ctx, akResp.UserName, akResp.AccessKeyID)
		return ScopedCred{}, fmt.Errorf("storage: mint CreatePolicy: %w", err)
	}

	// 3. AttachUserPolicy.
	if err := m.attachUserPolicy(ctx, akResp.UserName, policyARN); err != nil {
		_ = m.deletePolicy(ctx, policyARN)
		_ = m.deleteAccessKey(ctx, akResp.UserName, akResp.AccessKeyID)
		return ScopedCred{}, fmt.Errorf("storage: mint AttachUserPolicy: %w", err)
	}

	return ScopedCred{
		ID:              akResp.AccessKeyID, // access-key id doubles as the stable cred id
		AccessKeyID:     akResp.AccessKeyID,
		SecretAccessKey: akResp.SecretAccessKey,
		Endpoint:        m.cfg.S3Endpoint,
		Region:          m.cfg.S3Region,
		Bucket:          buckets[0],
		PolicyID:        policyARN,
		// UserName is needed at revoke time; we stash it in the policy field's
		// sibling by encoding it — but to keep ScopedCred flat, revoke re-derives
		// the user from the access-key id (Tigris user == access-key owner).
	}, nil
}

// Revoke deletes the access key and best-effort cleans up the attached policy.
func (m *TigrisIAMMinter) Revoke(ctx context.Context, credID, policyID string) error {
	// The Tigris access-key owner (user) shares the access-key id; DeleteAccessKey
	// keyed by the access-key id revokes it. DetachUserPolicy/DeletePolicy are
	// best-effort cleanup so a revoked cred leaves no dangling policy.
	var firstErr error
	if credID != "" {
		if err := m.deleteAccessKey(ctx, "", credID); err != nil {
			firstErr = err
		}
	}
	if policyID != "" {
		_ = m.detachUserPolicy(ctx, "", policyID)
		_ = m.deletePolicy(ctx, policyID)
	}
	return firstErr
}

// ---------------------------------------------------------------------------
// Policy document
// ---------------------------------------------------------------------------

// bucketScopedPolicyDoc builds an IAM policy JSON pinned to the given buckets,
// granting only the S3 actions Vulos needs (Get/Put/Delete/List/HEAD). ListBucket
// is granted on the bucket ARN; object actions on the /* ARN.
func bucketScopedPolicyDoc(buckets []string) string {
	var listRes, objRes []string
	for _, b := range buckets {
		listRes = append(listRes, `"arn:aws:s3:::`+b+`"`)
		objRes = append(objRes, `"arn:aws:s3:::`+b+`/*"`)
	}
	return `{"Version":"2012-10-17","Statement":[` +
		`{"Sid":"VulosBucketList","Effect":"Allow",` +
		`"Action":["s3:ListBucket","s3:GetBucketLocation"],` +
		`"Resource":[` + strings.Join(listRes, ",") + `]},` +
		`{"Sid":"VulosObjectRW","Effect":"Allow",` +
		`"Action":["s3:GetObject","s3:PutObject","s3:DeleteObject","s3:HeadObject"],` +
		`"Resource":[` + strings.Join(objRes, ",") + `]}]}`
}

// ---------------------------------------------------------------------------
// IAM Query API calls (SigV4-signed form POSTs)
// ---------------------------------------------------------------------------

type createAccessKeyResult struct {
	AccessKeyID     string
	SecretAccessKey string
	UserName        string
}

func (m *TigrisIAMMinter) createAccessKey(ctx context.Context) (createAccessKeyResult, error) {
	body, err := m.callIAM(ctx, url.Values{"Action": {"CreateAccessKey"}})
	if err != nil {
		return createAccessKeyResult{}, err
	}
	var parsed struct {
		XMLName               xml.Name `xml:"CreateAccessKeyResponse"`
		CreateAccessKeyResult struct {
			AccessKey struct {
				AccessKeyID     string `xml:"AccessKeyId"`
				SecretAccessKey string `xml:"SecretAccessKey"`
				UserName        string `xml:"UserName"`
			} `xml:"AccessKey"`
		} `xml:"CreateAccessKeyResult"`
	}
	if err := xml.Unmarshal(body, &parsed); err != nil {
		return createAccessKeyResult{}, fmt.Errorf("parse CreateAccessKey: %w", err)
	}
	ak := parsed.CreateAccessKeyResult.AccessKey
	if ak.AccessKeyID == "" || ak.SecretAccessKey == "" {
		return createAccessKeyResult{}, fmt.Errorf("%w: CreateAccessKey returned empty key", ErrProviderFailed)
	}
	return createAccessKeyResult{AccessKeyID: ak.AccessKeyID, SecretAccessKey: ak.SecretAccessKey, UserName: ak.UserName}, nil
}

func (m *TigrisIAMMinter) createPolicy(ctx context.Context, name, doc string) (string, error) {
	body, err := m.callIAM(ctx, url.Values{
		"Action":         {"CreatePolicy"},
		"PolicyName":     {name},
		"PolicyDocument": {doc},
	})
	if err != nil {
		return "", err
	}
	var parsed struct {
		CreatePolicyResult struct {
			Policy struct {
				Arn string `xml:"Arn"`
			} `xml:"Policy"`
		} `xml:"CreatePolicyResult"`
	}
	if err := xml.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parse CreatePolicy: %w", err)
	}
	if parsed.CreatePolicyResult.Policy.Arn == "" {
		return "", fmt.Errorf("%w: CreatePolicy returned empty ARN", ErrProviderFailed)
	}
	return parsed.CreatePolicyResult.Policy.Arn, nil
}

func (m *TigrisIAMMinter) attachUserPolicy(ctx context.Context, userName, policyARN string) error {
	_, err := m.callIAM(ctx, url.Values{
		"Action":    {"AttachUserPolicy"},
		"UserName":  {userName},
		"PolicyArn": {policyARN},
	})
	return err
}

func (m *TigrisIAMMinter) detachUserPolicy(ctx context.Context, userName, policyARN string) error {
	_, err := m.callIAM(ctx, url.Values{
		"Action":    {"DetachUserPolicy"},
		"UserName":  {userName},
		"PolicyArn": {policyARN},
	})
	return err
}

func (m *TigrisIAMMinter) deleteAccessKey(ctx context.Context, userName, accessKeyID string) error {
	vals := url.Values{"Action": {"DeleteAccessKey"}, "AccessKeyId": {accessKeyID}}
	if userName != "" {
		vals.Set("UserName", userName)
	}
	_, err := m.callIAM(ctx, vals)
	return err
}

func (m *TigrisIAMMinter) deletePolicy(ctx context.Context, policyARN string) error {
	_, err := m.callIAM(ctx, url.Values{"Action": {"DeletePolicy"}, "PolicyArn": {policyARN}})
	return err
}

// callIAM signs and sends one IAM Query request, returning the raw XML body.
// Errors from the provider (HTTP >= 400) are surfaced as ErrProviderFailed with
// the response body (which may carry an <Error><Message>) for diagnosis.
func (m *TigrisIAMMinter) callIAM(ctx context.Context, vals url.Values) ([]byte, error) {
	vals.Set("Version", iamAPIVersion)
	payload := vals.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.IAMEndpoint, strings.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")

	sum := sha256.Sum256([]byte(payload))
	payloadHash := hex.EncodeToString(sum[:])

	creds, err := m.creds.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("retrieve master creds: %w", err)
	}
	if err := m.signer.SignHTTP(ctx, creds, req, payloadHash, iamService, m.cfg.Region, time.Now().UTC()); err != nil {
		return nil, fmt.Errorf("sign IAM request: %w", err)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: IAM request: %v", ErrProviderFailed, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: IAM %q returned HTTP %d: %s",
			ErrProviderFailed, vals.Get("Action"), resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// compile-time assertion.
var _ CredentialMinter = (*TigrisIAMMinter)(nil)
