// tigris.go — TigrisProvider: S3-compatible storage provider against Tigris.
//
// Risk callout (R-B from CLOUD_BUILDOUT_PLAN.md): This provider depends on
// Tigris S3 API contracts at https://fly.storage.tigris.dev. If Tigris
// changes its API surface, this file must be updated; MemProvider is
// unaffected.
//
// Credentials are loaded from env:
//   - TIGRIS_ACCESS_KEY_ID       (required)
//   - TIGRIS_SECRET_ACCESS_KEY   (required)
//   - TIGRIS_ENDPOINT            (default: https://fly.storage.tigris.dev)
//   - TIGRIS_REGION              (default: auto)
//   - TIGRIS_FORCE_PATH_STYLE=1  (default: off — see ForcePathStyle)
//
// Missing TIGRIS_ACCESS_KEY_ID or TIGRIS_SECRET_ACCESS_KEY → NewTigrisProvider
// returns ErrProviderFailed (caller should return 503).
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	defaultTigrisEndpoint = "https://fly.storage.tigris.dev"
	defaultTigrisRegion   = "auto"
)

// TigrisConfig holds credentials and endpoint configuration for Tigris.
type TigrisConfig struct {
	AccessKeyID     string
	SecretAccessKey string
	Endpoint        string // default: https://fly.storage.tigris.dev
	Region          string // default: auto
	// ForcePathStyle addresses buckets as <endpoint>/<bucket>/<key> instead of
	// <bucket>.<endpoint>/<key>.
	//
	// Tigris (and AWS) serve virtual-hosted style, which is the default and stays
	// the default — this flag is OFF unless TIGRIS_FORCE_PATH_STYLE=1. It exists
	// for S3-compatible endpoints that have no wildcard DNS, above all the local
	// MinIO in dev/docker-compose.local.yml: a virtual-hosted request there would
	// resolve <bucket>.tigris-local, which no DNS answers, so every object call —
	// and every presigned URL — would fail. With path style the local stack
	// exercises the REAL S3 client and REAL SigV4 presigning instead of falling
	// back to MemProvider's fake URLs.
	ForcePathStyle bool
}

// TigrisProvider is the production Provider backed by Tigris / S3-compatible
// object storage. Use NewTigrisProvider to construct.
type TigrisProvider struct {
	client   *s3.Client
	presign  *s3.PresignClient
	endpoint string
	region   string
}

// NewTigrisProvider constructs a TigrisProvider from the given config.
// Returns a wrapped ErrProviderFailed if credentials are missing.
func NewTigrisProvider(cfg TigrisConfig) (*TigrisProvider, error) {
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, fmt.Errorf("%w: TIGRIS_ACCESS_KEY_ID and TIGRIS_SECRET_ACCESS_KEY are required", ErrProviderFailed)
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = defaultTigrisEndpoint
	}
	region := cfg.Region
	if region == "" {
		region = defaultTigrisRegion
	}

	creds := credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")
	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithRegion(region),
		config.WithCredentialsProvider(creds),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: load aws config: %v", ErrProviderFailed, err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		// Tigris is path-style compatible; use virtual-hosted style by default
		// (Tigris supports both; virtual-hosted is the AWS SDK default). Path style
		// is opt-in for S3-compatible endpoints without wildcard DNS (MinIO) — see
		// TigrisConfig.ForcePathStyle.
		o.UsePathStyle = cfg.ForcePathStyle
	})

	return &TigrisProvider{
		client:   client,
		presign:  s3.NewPresignClient(client),
		endpoint: endpoint,
		region:   region,
	}, nil
}

// EnsureBucket creates the bucket if it does not exist (idempotent), in the
// provider's configured region (TIGRIS_REGION, default "auto").
// For Tigris, a CreateBucket with the existing bucket name is a no-op.
func (p *TigrisProvider) EnsureBucket(ctx context.Context, name string) error {
	return p.EnsureBucketInRegion(ctx, name, p.region)
}

// EnsureBucketInRegion implements RegionalBucketProvider: it pins the bucket's
// LocationConstraint to the CALLER's region rather than the provider-global one.
//
// Without this, a per-org residency region was persisted to storage_configs but
// never enforced: every bucket was created in the global TIGRIS_REGION (default
// "auto"), so the row promised "eu-west-1" while the data could sit anywhere.
// An empty region falls back to the provider default.
func (p *TigrisProvider) EnsureBucketInRegion(ctx context.Context, name, s3Region string) error {
	if s3Region == "" {
		s3Region = p.region
	}
	_, err := p.client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(name),
		CreateBucketConfiguration: &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(s3Region),
		},
	})
	if err != nil {
		// Bucket already exists errors are not failures.
		if isBucketAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("%w: EnsureBucket %q: %v", ErrProviderFailed, name, err)
	}
	return nil
}

// PresignGet returns a presigned GET URL for the given object. The URL is
// valid for the given ttl. The response includes X-Amz-Algorithm for AWS
// SigV4 presigned URL compliance.
func (p *TigrisProvider) PresignGet(ctx context.Context, bucket, key string, ttl time.Duration) (string, error) {
	req, err := p.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("%w: PresignGet %q/%q: %v", ErrProviderFailed, bucket, key, err)
	}
	return req.URL, nil
}

// PresignPut returns a presigned PUT URL for the given object.
func (p *TigrisProvider) PresignPut(ctx context.Context, bucket, key string, ttl time.Duration) (string, error) {
	req, err := p.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("%w: PresignPut %q/%q: %v", ErrProviderFailed, bucket, key, err)
	}
	return req.URL, nil
}

// ListBucket lists objects in the bucket with the given prefix, up to max
// results. max=0 means the S3 default (1000).
func (p *TigrisProvider) ListBucket(ctx context.Context, bucket, prefix string, max int) ([]ObjectInfo, error) {
	inp := &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
	}
	if prefix != "" {
		inp.Prefix = aws.String(prefix)
	}
	if max > 0 {
		inp.MaxKeys = aws.Int32(int32(max))
	}

	out, err := p.client.ListObjectsV2(ctx, inp)
	if err != nil {
		return nil, fmt.Errorf("%w: ListBucket %q: %v", ErrProviderFailed, bucket, err)
	}

	infos := make([]ObjectInfo, 0, len(out.Contents))
	for _, obj := range out.Contents {
		var lm time.Time
		if obj.LastModified != nil {
			lm = *obj.LastModified
		}
		var size int64
		if obj.Size != nil {
			size = *obj.Size
		}
		key := ""
		if obj.Key != nil {
			key = *obj.Key
		}
		infos = append(infos, ObjectInfo{Key: key, Size: size, LastModified: lm})
	}
	return infos, nil
}

// GetObject downloads an object. The caller must close the returned
// io.ReadCloser.
func (p *TigrisProvider) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	out, err := p.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: GetObject %q/%q: %v", ErrProviderFailed, bucket, key, err)
	}
	return out.Body, nil
}

// PutObject uploads an object.
func (p *TigrisProvider) PutObject(ctx context.Context, bucket, key string, r io.Reader, size int64) error {
	inp := &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   r,
	}
	if size >= 0 {
		inp.ContentLength = aws.Int64(size)
	}
	_, err := p.client.PutObject(ctx, inp)
	if err != nil {
		return fmt.Errorf("%w: PutObject %q/%q: %v", ErrProviderFailed, bucket, key, err)
	}
	return nil
}

// CopyObject copies one object to another key WITHIN the same bucket,
// server-side — the bytes never round-trip through the CP. It is not part of the
// frozen Provider interface (contract.go): callers that need it type-assert for
// it and fall back to Get→Put when the provider does not implement it (as
// MemProvider does not).
//
// A missing source is reported as ErrUnknownBucket — the same sentinel the
// package already uses for "that object is not there" (see MemProvider.GetObject)
// — so a caller can distinguish "nothing to copy" from a real provider failure.
//
// Single-part S3 copy caps at 5 GiB, which is also the ceiling of a single
// presigned PUT: nothing that reaches this bucket through a presigned upload can
// exceed it.
func (p *TigrisProvider) CopyObject(ctx context.Context, bucket, srcKey, dstKey string) error {
	_, err := p.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(bucket),
		Key:        aws.String(dstKey),
		CopySource: aws.String(url.PathEscape(bucket + "/" + srcKey)),
	})
	if err != nil {
		var noKey *s3types.NoSuchKey
		if errors.As(err, &noKey) || isNotFoundStatus(err) {
			return fmt.Errorf("%w: key %q not found in bucket %q", ErrUnknownBucket, srcKey, bucket)
		}
		return fmt.Errorf("%w: CopyObject %q/%q->%q: %v", ErrProviderFailed, bucket, srcKey, dstKey, err)
	}
	return nil
}

// isNotFoundStatus reports whether err is an S3 404. Tigris answers a copy from
// a missing source with a bare 404 rather than a typed NoSuchKey, so the typed
// check alone would miss it.
func isNotFoundStatus(err error) bool {
	var re *awshttp.ResponseError
	return errors.As(err, &re) && re.HTTPStatusCode() == http.StatusNotFound
}

// DeleteObject removes an object from the bucket.
func (p *TigrisProvider) DeleteObject(ctx context.Context, bucket, key string) error {
	_, err := p.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("%w: DeleteObject %q/%q: %v", ErrProviderFailed, bucket, key, err)
	}
	return nil
}

// BucketSize returns the total size in bytes and object count by iterating
// all objects. This is an O(n) operation and should only be called by the
// nightly metering job.
func (p *TigrisProvider) BucketSize(ctx context.Context, bucket string) (int64, int64, error) {
	var totalBytes, totalObjects int64
	paginator := s3.NewListObjectsV2Paginator(p.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return 0, 0, fmt.Errorf("%w: BucketSize %q: %v", ErrProviderFailed, bucket, err)
		}
		for _, obj := range page.Contents {
			if obj.Size != nil {
				totalBytes += *obj.Size
			}
			totalObjects++
		}
	}
	return totalBytes, totalObjects, nil
}

// compile-time assertion: TigrisProvider satisfies Provider.
var _ Provider = (*TigrisProvider)(nil)

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// isBucketAlreadyExists returns true when the S3 error indicates the bucket
// already exists (BucketAlreadyExists or BucketAlreadyOwnedByYou).
func isBucketAlreadyExists(err error) bool {
	var ae *s3types.BucketAlreadyExists
	if errors.As(err, &ae) {
		return true
	}
	var ao *s3types.BucketAlreadyOwnedByYou
	return errors.As(err, &ao)
}
