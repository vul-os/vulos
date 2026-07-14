package snapshot

import (
	"context"
	"strings"

	"vulos/backend/internal/cpbilling"
)

// Meterer reports snapshot storage growth to a billing/metering seam. It is
// optional; a standalone box wires none and nothing is metered.
type Meterer interface {
	// MeterSnapshotBytes reports that bytes were added to snapshot storage for
	// accountID. Implementations must be non-blocking / best-effort.
	MeterSnapshotBytes(ctx context.Context, accountID string, bytes int64)
}

// CPMeter adapts the cp billing client to the Meterer interface. A nil or
// disabled client meters nothing.
type CPMeter struct{ Client *cpbilling.Client }

// MeterSnapshotBytes reports snapshot storage growth to cp as a storage-product
// usage event. It is fire-and-forget so the snapshot path never blocks on cp.
func (m CPMeter) MeterSnapshotBytes(_ context.Context, accountID string, bytes int64) {
	if m.Client == nil || bytes <= 0 {
		return
	}
	m.Client.MeterAsync(cpbilling.UsageEvent{
		Product:   cpbilling.ProductStorage,
		AccountID: accountID,
		Kind:      cpbilling.KindSnapshotBytes,
		Bytes:     bytes,
	})
}

// Usage is a breakdown of the bytes snapshot storage occupies in the bucket.
type Usage struct {
	BlobBytes     int64 `json:"blob_bytes"`
	BlobCount     int   `json:"blob_count"`
	ManifestBytes int64 `json:"manifest_bytes"`
	IndexBytes    int64 `json:"index_bytes"`
	IndexCount    int   `json:"index_count"`
	TotalBytes    int64 `json:"total_bytes"`
}

// StorageUsage measures the exact bytes held under the snapshot artifact prefix,
// broken down by kind. This is the figure reported to the owner and metered.
func (s *Snapshotter) StorageUsage(ctx context.Context) (*Usage, error) {
	objs, err := s.store.List(ctx, s.snapPfx)
	if err != nil {
		return nil, err
	}
	u := &Usage{}
	blobs := s.snapPfx + "blobs/"
	mans := s.snapPfx + "manifests/"
	idxs := s.snapPfx + "index/"
	for _, o := range objs {
		switch {
		case strings.HasPrefix(o.Key, blobs):
			u.BlobBytes += o.Size
			u.BlobCount++
		case strings.HasPrefix(o.Key, mans):
			u.ManifestBytes += o.Size
		case strings.HasPrefix(o.Key, idxs):
			u.IndexBytes += o.Size
			u.IndexCount++
		}
		u.TotalBytes += o.Size
	}
	return u, nil
}
