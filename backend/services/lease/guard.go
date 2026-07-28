// Package lease — guard.go
//
// # Tigris strong-consistency requirement
//
// The bucket-lease primitive relies on If-Match CAS for correctness.  On Tigris,
// CAS is strongly consistent only on Single-region and Multi-region buckets.
// Global and Dual-region Tigris buckets use eventual consistency — CAS is NOT
// safe there, because two nodes may each believe they hold the lease
// simultaneously, violating mutual exclusion.
//
// See roadmap/COORDINATION.md § "Tigris note" and the Tigris documentation at
// https://www.tigrisdata.com/docs/buckets/regions/ for bucket-type definitions.
//
// Detection: the Endpoint field is inspected for known Tigris hostnames
// (*.tigris.dev, fly.storage.tigris.dev, t3.storage.tigris.dev).  When a
// Tigris endpoint is detected the BucketType field (if set) is checked against
// the unsafe list.  Non-Tigris backends (AWS S3, MinIO, …) are always treated as
// safe — their CAS semantics are strongly consistent by default.
//
// Modes:
//   - ConsistencyWarn (default, StrictMode=false): logs a prominent warning and
//     allows the Manager to be constructed.  Use during migration or when you are
//     certain your Tigris account overrides the bucket type at the control plane.
//   - ConsistencyStrict (StrictMode=true): returns ErrUnsafeBucket and refuses to
//     construct the Manager.  Recommended for production deployments.
//
// # Reachability of the strict path — read this before trusting the guard
//
// Both inputs to the strict path are caller-supplied fields on lease.S3Config
// (BucketType, StrictConsistency), and as of this commit NO production caller
// sets either one:
//
//	services/cluster.(*Cluster).InitLeases   — builds S3Config, sets neither
//	cmd/server/main.go (backupLeaseCfg)      — builds S3Config, sets neither
//	cmd/server/cmd_backup.go (leaseCfgFromS3) — builds S3Config, sets neither
//
// So in a running box CheckConsistency always takes the "BucketType unknown"
// branch: it logs and returns nil.  It has never refused anything outside its
// own tests.  Treat it as a warning, NOT as an enforcement point, until the
// three call sites above are given a way to populate the two fields (they are
// outside this package; see the note in the unknown-type warning below).
//
// This does not affect the default storage mode: since D-STORE-LOCAL-DEFAULT a
// box stores its bytes locally, no lease manager is constructed against a
// hosted bucket at all, and this guard is only reached by an operator who has
// explicitly opted into a hosted S3 backend.
package lease

import (
	"errors"
	"fmt"
	"log"
	"strings"
)

// ── Bucket type constants ─────────────────────────────────────────────────────

// TigrisBucketType enumerates the Tigris bucket replication/consistency classes.
// Only Single-region and Multi-region are safe for CAS-based leasing.
type TigrisBucketType string

const (
	// TigrisSingleRegion is a single-region Tigris bucket: strongly consistent.
	// Safe for CAS leasing.
	TigrisSingleRegion TigrisBucketType = "single-region"

	// TigrisMultiRegion is a multi-region Tigris bucket: strongly consistent.
	// Safe for CAS leasing.
	TigrisMultiRegion TigrisBucketType = "multi-region"

	// TigrisGlobal is a globally-replicated Tigris bucket: eventually consistent.
	// NOT safe for CAS leasing.
	TigrisGlobal TigrisBucketType = "global"

	// TigrisDualRegion is a dual-region Tigris bucket: eventually consistent.
	// NOT safe for CAS leasing.
	TigrisDualRegion TigrisBucketType = "dual-region"
)

// ── Sentinel error ────────────────────────────────────────────────────────────

// ErrUnsafeBucket is returned by CheckConsistency when StrictMode is true and
// the detected configuration is eventually consistent (unsafe for CAS leasing).
var ErrUnsafeBucket = errors.New("lease: bucket is not strongly consistent; CAS leasing is unsafe")

// ── ConsistencyConfig ─────────────────────────────────────────────────────────

// ConsistencyConfig carries the fields needed to assess bucket CAS safety.
// It is deliberately separate from S3Config so callers can set only what is
// relevant, and so the guard can be tested in isolation.
type ConsistencyConfig struct {
	// Endpoint is the S3-compatible endpoint URL or hostname (e.g.
	// "fly.storage.tigris.dev", "localhost:9000", "s3.amazonaws.com").
	// Used to detect whether the backend is Tigris.
	Endpoint string

	// BucketType, when non-empty, explicitly declares the Tigris replication
	// class.  Populated from the TIGRIS_BUCKET_TYPE environment variable or a
	// matching operator config.  Ignored for non-Tigris backends.
	BucketType TigrisBucketType

	// StrictMode controls the action taken when an unsafe configuration is
	// detected.  false (default) → warn loudly.  true → return ErrUnsafeBucket.
	StrictMode bool
}

// ── tigris hostname detection ─────────────────────────────────────────────────

// knownTigrisHosts are substrings present in every documented Tigris endpoint.
var knownTigrisHosts = []string{
	"tigris.dev",
	"fly.storage.tigris.dev",
	"t3.storage.tigris.dev",
}

// isTigrisEndpoint returns true when endpoint looks like a Tigris S3 hostname.
// It matches on the canonical domain fragment "tigris.dev" which is present in
// all Tigris-managed endpoints.
func isTigrisEndpoint(endpoint string) bool {
	lower := strings.ToLower(endpoint)
	for _, h := range knownTigrisHosts {
		if strings.Contains(lower, h) {
			return true
		}
	}
	return false
}

// unsafeBucketTypes is the set of Tigris bucket types that use eventual
// consistency.  Any bucket whose type is in this set is unsafe for CAS leasing.
var unsafeBucketTypes = map[TigrisBucketType]bool{
	TigrisGlobal:     true,
	TigrisDualRegion: true,
}

// ── CheckConsistency ──────────────────────────────────────────────────────────

// CheckConsistency inspects cfg and determines whether the bucket configuration
// is safe for If-Match CAS leasing.
//
// Rules:
//   - Non-Tigris backends are always safe.
//   - Tigris with BucketType == "" (unknown): warns that the type could not be
//     determined and advises the operator to set TIGRIS_BUCKET_TYPE.
//   - Tigris with a safe type (single-region, multi-region): safe.
//   - Tigris with an unsafe type (global, dual-region): unsafe.
//     StrictMode=false → warn loudly and return nil.
//     StrictMode=true  → return ErrUnsafeBucket.
//
// The function never panics.
func CheckConsistency(cfg ConsistencyConfig) error {
	if !isTigrisEndpoint(cfg.Endpoint) {
		// Non-Tigris backend (AWS S3, MinIO, etc.): CAS is strongly consistent.
		return nil
	}

	// Tigris endpoint detected.
	if cfg.BucketType == "" {
		// Type is unknown — we cannot prove safety.  Warn and allow, because the
		// operator may be using a Single/Multi-region bucket without having
		// configured BucketType.
		//
		// HONESTY NOTE: earlier text here told operators to "set
		// TIGRIS_BUCKET_TYPE".  Nothing in this repo reads that environment
		// variable — S3Config.BucketType is populated by the caller and no
		// production caller populates it — so following that advice changed
		// nothing.  The message below states what is actually true.
		log.Printf("[lease WARNING] Tigris endpoint detected (%s) but the bucket's consistency "+
			"class is unknown to this process, so CAS-lease safety CANNOT be verified here. "+
			"Global and Dual-region Tigris buckets are eventually consistent and unsafe for "+
			"leasing; use a Single-region or Multi-region bucket. "+
			"(Populating lease.S3Config.BucketType/StrictConsistency is not yet wired up at any "+
			"call site — see the reachability note in guard.go.)",
			cfg.Endpoint)
		return nil
	}

	if unsafeBucketTypes[cfg.BucketType] {
		msg := fmt.Sprintf(
			"lease: Tigris bucket type %q is eventually consistent — CAS leasing is unsafe. "+
				"Use a single-region or multi-region bucket. "+
				"See roadmap/COORDINATION.md for details.",
			cfg.BucketType,
		)
		if cfg.StrictMode {
			return fmt.Errorf("%w: %s", ErrUnsafeBucket, msg)
		}
		// Warn mode: loud log, but allow construction.
		log.Printf("[lease WARNING] %s", msg)
		return nil
	}

	// Known safe type (single-region or multi-region).
	return nil
}
