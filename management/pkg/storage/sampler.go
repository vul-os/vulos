// sampler.go — background goroutine that samples bucket usage for every
// storage config row and persists the result via InsertUsage.
//
// STORE-05: RunUsageSampler is called from cmd/server/main.go (after
// wireStorage). It reads STORAGE_USAGE_SAMPLE_INTERVAL (default 24h) and
// loops, rate-limited to 1 provider call per second. Failures are logged and
// do not stop the loop.
package storage

import (
	"context"
	"log"
	"os"
	"time"
)

// samplerStore is the minimal store interface required by RunUsageSampler.
// Both MemStore and SQLStore satisfy it (ListConfigs is defined in this file
// for MemStore and in store.go for SQLStore).
type samplerStore interface {
	ListConfigs(ctx context.Context) ([]Config, error)
	InsertUsage(ctx context.Context, u UsageSample) error
}

// UsageSampleInterval returns the sampling interval from
// STORAGE_USAGE_SAMPLE_INTERVAL, defaulting to 24h. The override parameter
// is used in tests to inject a short interval without touching the environment.
func UsageSampleInterval(override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	raw := os.Getenv("STORAGE_USAGE_SAMPLE_INTERVAL")
	if raw == "" {
		return 24 * time.Hour
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Printf("[storage] WARNING: invalid STORAGE_USAGE_SAMPLE_INTERVAL %q — using default 24h", raw)
		return 24 * time.Hour
	}
	return d
}

// RunUsageSampler starts a background goroutine that, every interval, queries
// every storage config row and records a UsageSample via InsertUsage.
// Provider calls are rate-limited to 1/sec. Failures log and continue.
//
// interval is typically 0 (read from env / default 24h); pass a non-zero
// value in tests to override without touching the environment.
//
// The goroutine stops when ctx is cancelled.
func RunUsageSampler(ctx context.Context, svc *Service, interval time.Duration) {
	interval = UsageSampleInterval(interval)
	log.Printf("[storage] usage sampler started (interval=%s)", interval)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// Run immediately on first tick.
		runSample(ctx, svc)

		for {
			select {
			case <-ctx.Done():
				log.Println("[storage] usage sampler stopped")
				return
			case <-ticker.C:
				runSample(ctx, svc)
			}
		}
	}()
}

// runSample iterates all configs and records one UsageSample per config.
func runSample(ctx context.Context, svc *Service) {
	st, ok := svc.Store.(samplerStore)
	if !ok {
		log.Println("[storage] usage sampler: store does not implement ListConfigs — skipping")
		return
	}

	cfgs, err := st.ListConfigs(ctx)
	if err != nil {
		log.Printf("[storage] usage sampler: ListConfigs error: %v", err)
		return
	}

	limiter := time.NewTicker(time.Second)
	defer limiter.Stop()

	for _, cfg := range cfgs {
		// Rate-limit: wait for token before each provider call.
		select {
		case <-ctx.Done():
			return
		case <-limiter.C:
		}

		provider, err := svc.ProviderForAccount(ctx, cfg.AccountID)
		if err != nil {
			log.Printf("[storage] usage sampler: ProviderForAccount(%s): %v", cfg.AccountID, err)
			continue
		}

		bucket := cfg.Bucket
		if bucket == "" {
			if !cfg.BYO {
				// Managed bucket: use the canonical name for the first enrolled ULID.
				// When bucket is empty on a managed config we skip — it hasn't been
				// provisioned yet.
				log.Printf("[storage] usage sampler: account %s has empty bucket — skipping", cfg.AccountID)
				continue
			}
			log.Printf("[storage] usage sampler: account %s has empty BYO bucket — skipping", cfg.AccountID)
			continue
		}

		sizeBytes, objectCount, err := provider.BucketSize(ctx, bucket)
		if err != nil {
			log.Printf("[storage] usage sampler: BucketSize(%s/%s): %v", cfg.AccountID, bucket, err)
			continue
		}

		sample := UsageSample{
			AccountID:   cfg.AccountID,
			Bucket:      bucket,
			SizeBytes:   sizeBytes,
			ObjectCount: objectCount,
			SampledAt:   time.Now().UTC(),
		}
		if err := st.InsertUsage(ctx, sample); err != nil {
			log.Printf("[storage] usage sampler: InsertUsage(%s): %v", cfg.AccountID, err)
		}
	}
}
