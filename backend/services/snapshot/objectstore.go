// Package snapshot implements OS-level point-in-time restore points for a box.
//
// A snapshot is a capture of the box's bucket state (its data + config as it
// lives in object storage) stored INSIDE the box's own bucket under a reserved
// prefix. Snapshots are content-addressed and incremental: a snapshot re-walks
// every live object but only uploads blobs whose content is not already present,
// so unchanged objects cost no bytes. Restore rolls the box back to a chosen
// snapshot fail-closed — integrity is verified in full before anything is
// swapped, and the current state is itself snapshotted first so a bad restore is
// reversible.
//
// The package is provider-agnostic: it operates over the ObjectStore interface,
// which the box wires to whatever bucket it uses via internal/storage.Resolution
// (S3Store) — the same abstraction Files and cluster sync use.
package snapshot

import (
	"context"
	"errors"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"vulos/backend/internal/storage"
)

// ObjectInfo is the minimal object metadata the snapshotter needs.
type ObjectInfo struct {
	Key  string
	Size int64
	ETag string
}

// ObjectStore is the provider-agnostic object-store contract the snapshotter
// operates over. Implementations must be safe for concurrent use.
//
// List is recursive and returns every object whose key begins with prefix.
// Stat's second return reports whether the object exists (a missing object is
// not an error). Put must be able to accept size == -1 (unknown length) so
// callers may stream; the S3 adapter falls back to a streaming multipart put.
type ObjectStore interface {
	List(ctx context.Context, prefix string) ([]ObjectInfo, error)
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Delete(ctx context.Context, key string) error
	Stat(ctx context.Context, key string) (ObjectInfo, bool, error)
}

// ErrNotConfigured is returned when a snapshotter is asked to run against a box
// that has no object store configured.
var ErrNotConfigured = errors.New("snapshot: object store not configured")

// S3Store is the production ObjectStore backed by a MinIO/S3 bucket resolved
// from an internal/storage.Resolution. It is the same client shape the grant
// broker and cluster sync use, so it works over any S3-compatible provider.
type S3Store struct {
	mc     *minio.Client
	bucket string
}

// NewS3Store builds an S3Store from a resolved per-box storage binding. It
// returns ErrNotConfigured when the resolution points at no object store
// (local-FS fallback) — snapshots require a bucket.
func NewS3Store(res storage.Resolution) (*S3Store, error) {
	if !res.Configured() {
		return nil, ErrNotConfigured
	}
	host, useSSL := res.S3Host()
	mc, err := minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(res.AccessKey, res.SecretKey, res.SessionToken),
		Secure: useSSL,
		Region: res.Region,
	})
	if err != nil {
		return nil, err
	}
	return &S3Store{mc: mc, bucket: res.Bucket}, nil
}

// List returns every object under prefix (recursive).
func (s *S3Store) List(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	for obj := range s.mc.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		out = append(out, ObjectInfo{Key: obj.Key, Size: obj.Size, ETag: obj.ETag})
	}
	return out, nil
}

// Get opens key for reading. The caller must Close the returned reader.
func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := s.mc.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	// Force the request now so a missing object errors here rather than
	// mid-stream (GetObject is lazy).
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		return nil, err
	}
	return obj, nil
}

// Put writes r to key. size == -1 is accepted (streaming multipart).
func (s *S3Store) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := s.mc.PutObject(ctx, s.bucket, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}

// Delete removes key. A missing object is not an error.
func (s *S3Store) Delete(ctx context.Context, key string) error {
	if err := s.mc.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil
		}
		return err
	}
	return nil
}

// Stat reports object metadata and whether the object exists.
func (s *S3Store) Stat(ctx context.Context, key string) (ObjectInfo, bool, error) {
	st, err := s.mc.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return ObjectInfo{}, false, nil
		}
		return ObjectInfo{}, false, err
	}
	return ObjectInfo{Key: st.Key, Size: st.Size, ETag: st.ETag}, true, nil
}
