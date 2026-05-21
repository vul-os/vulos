package osdist

// source.go — Soft/runtime OS-bucket URL resolver with mirror failover (SEED-02).
//
// # Trust model
//
// The OS bucket is public-read; its URL is SOFT configuration — it can be a
// mirror, a cloud accelerator, or a failover endpoint.  The bucket URL does
// NOT enforce trust.  Trust is enforced exclusively by the Ed25519 signing key
// that is hard-baked into the seed at build time (/etc/vulos/trust-anchor.pub).
//
// Repointing the URL is therefore safe: a poisoned mirror or redirected DNS
// cannot serve a forged OS image because every fetched file is verified against
// the baked key (SIGN-02 / ParseAndVerify).  This is why mirroring and failover
// are free — URL location and cryptographic trust are independent.
//
// # Default URL
//
// The default bucket base URL is written into the seed at build time as the
// single-line file /etc/vulos/os-bucket-url (SEED-01/SEED-03 contract).
// Source reads that file when no runtime override is present.  The path can be
// overridden for tests via SourceOption or the defaultURLPath field.
//
// # Resolution order (Resolve)
//
//  1. Runtime override  — VULOS_OS_BUCKET_URL env var, or set via WithOverride.
//  2. Embedded default  — /etc/vulos/os-bucket-url (or the configured path).
//  3. Additional mirrors — supplied in order via WithMirrors.
//
// Duplicate or empty URLs are silently dropped.
//
// # Fetch
//
// Fetch(ctx, relPath) builds a full URL for each candidate (base + "/" +
// relPath), tries them in Resolve order, and returns the body of the first
// successful HTTP 200 response.  It advances to the next candidate on any
// network error or non-200 status code.  No credentials are sent — the bucket
// is public-read.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// DefaultEmbeddedURLPath is the on-device path where the build embeds the
// default OS bucket base URL (SEED-01/SEED-03 contract).
const DefaultEmbeddedURLPath = "/etc/vulos/os-bucket-url"

// envOverrideKey is the environment variable that provides a runtime URL
// override without requiring a config file change.
const envOverrideKey = "VULOS_OS_BUCKET_URL"

// ─── Source ──────────────────────────────────────────────────────────────────

// Source resolves the ordered list of candidate OS-bucket base URLs and
// performs failover HTTP fetches against them.
//
// Construct with NewSource; customise with the With* options.  The zero value
// is NOT usable — always use NewSource.
type Source struct {
	// embeddedURLPath is the filesystem path of the embedded default URL file.
	// Defaults to DefaultEmbeddedURLPath; overridable for tests.
	embeddedURLPath string

	// override is an explicit base URL that, when non-empty, takes the highest
	// priority in the resolution order.  Set via WithOverride or automatically
	// from VULOS_OS_BUCKET_URL at construction time.
	override string

	// mirrors is an ordered list of additional base URLs tried after the
	// embedded default.
	mirrors []string

	// client is the HTTP client used by Fetch.  Defaults to http.DefaultClient;
	// injectable for tests.
	client *http.Client
}

// SourceOption is a functional option for NewSource.
type SourceOption func(*Source)

// WithOverride sets an explicit runtime base URL that takes priority over the
// embedded default and all mirrors.  Passing an empty string is a no-op.
func WithOverride(url string) SourceOption {
	return func(s *Source) {
		if url != "" {
			s.override = url
		}
	}
}

// WithMirrors appends an ordered list of additional base URLs tried after the
// embedded default.  Empty strings in the list are ignored.
func WithMirrors(urls ...string) SourceOption {
	return func(s *Source) {
		for _, u := range urls {
			if u != "" {
				s.mirrors = append(s.mirrors, u)
			}
		}
	}
}

// WithEmbeddedURLPath overrides the filesystem path used to read the embedded
// default URL.  Intended for tests that cannot write to /etc/vulos/.
func WithEmbeddedURLPath(path string) SourceOption {
	return func(s *Source) {
		if path != "" {
			s.embeddedURLPath = path
		}
	}
}

// withHTTPClient replaces the HTTP client.  Unexported; used by tests via
// httptest servers.
func withHTTPClient(c *http.Client) SourceOption {
	return func(s *Source) {
		if c != nil {
			s.client = c
		}
	}
}

// NewSource constructs a Source with the given options.
//
// The VULOS_OS_BUCKET_URL environment variable is read at construction time
// and used as the override URL if set (and not already overridden by
// WithOverride).
func NewSource(opts ...SourceOption) *Source {
	s := &Source{
		embeddedURLPath: DefaultEmbeddedURLPath,
		client:          http.DefaultClient,
	}
	for _, o := range opts {
		o(s)
	}
	// Auto-populate override from env if not already set by an option.
	if s.override == "" {
		if v := os.Getenv(envOverrideKey); v != "" {
			s.override = v
		}
	}
	return s
}

// ─── Resolve ─────────────────────────────────────────────────────────────────

// Resolve returns the ordered list of candidate base URLs in priority order:
//
//  1. Runtime override  (highest priority)
//  2. Embedded default  (from /etc/vulos/os-bucket-url or the configured path)
//  3. Additional mirrors (in the order supplied to WithMirrors)
//
// The list contains only non-empty, deduplicated URLs (first occurrence wins).
// A missing or unreadable embedded-URL file is silently skipped; the caller
// (Fetch) will try whatever candidates remain.
func (s *Source) Resolve() []string {
	seen := make(map[string]bool)
	var out []string

	add := func(u string) {
		u = strings.TrimSpace(u)
		// Strip trailing slash for consistent URL joining in Fetch.
		u = strings.TrimRight(u, "/")
		if u == "" || seen[u] {
			return
		}
		seen[u] = true
		out = append(out, u)
	}

	// 1. Runtime override.
	add(s.override)

	// 2. Embedded default — read from disk; silently skip on any error.
	if raw, err := os.ReadFile(s.embeddedURLPath); err == nil {
		add(string(raw))
	}

	// 3. Additional mirrors.
	for _, m := range s.mirrors {
		add(m)
	}

	return out
}

// ─── Fetch ───────────────────────────────────────────────────────────────────

// ErrNoMirrors is returned by Fetch when Resolve returns an empty list — there
// are no base URLs to try.
var ErrNoMirrors = fmt.Errorf("osdist/source: no OS-bucket URLs configured")

// ErrAllMirrorsFailed is returned by Fetch when every candidate URL was tried
// and none produced a successful HTTP 200 response.
var ErrAllMirrorsFailed = fmt.Errorf("osdist/source: all OS-bucket mirrors failed")

// Fetch fetches relPath from the first candidate base URL that returns a
// successful HTTP 200 response.  It advances to the next candidate on any
// network error or non-200 status.
//
// relPath must be a bucket-relative path such as "os/stable.json" or
// "os/v08/os-core.squashfs".  The full request URL is:
//
//	<base>/<relPath>
//
// The returned ReadCloser must be closed by the caller.
//
// No credentials are attached to requests — the OS bucket is public-read.
// Trust is enforced by the caller verifying the fetched data against the
// baked signing key (ParseAndVerify / SIGN-02), not by the URL.
func (s *Source) Fetch(ctx context.Context, relPath string) (io.ReadCloser, error) {
	candidates := s.Resolve()
	if len(candidates) == 0 {
		return nil, ErrNoMirrors
	}

	relPath = strings.TrimLeft(relPath, "/")

	var lastErr error
	for _, base := range candidates {
		url := base + "/" + relPath

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			// Malformed URL; record and skip.
			lastErr = fmt.Errorf("osdist/source: build request for %q: %w", url, err)
			continue
		}

		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("osdist/source: GET %q: %w", url, err)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("osdist/source: GET %q: HTTP %d", url, resp.StatusCode)
			continue
		}

		// Success — return the open body; caller must close.
		return resp.Body, nil
	}

	return nil, fmt.Errorf("%w: last error: %v", ErrAllMirrorsFailed, lastErr)
}
