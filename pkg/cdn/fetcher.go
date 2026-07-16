package cdn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// RangeFetcher fetches and stores the published CDN egress IP ranges for a
// provider. Call RefreshAll periodically (e.g. every 4 h) to keep the cache
// current.
type RangeFetcher struct {
	Store      Store
	HTTPClient *http.Client
}

// NewRangeFetcher returns a RangeFetcher backed by st.
func NewRangeFetcher(st Store) *RangeFetcher {
	return &RangeFetcher{
		Store:      st,
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// RefreshAll fetches IP ranges for all known providers and stores them.
// Individual provider failures are logged but do not abort the refresh.
func (f *RangeFetcher) RefreshAll(ctx context.Context) {
	for _, p := range []Provider{ProviderCloudflare, ProviderFastly, ProviderBunny} {
		cidrs, err := f.fetch(ctx, p)
		if err != nil {
			log.Printf("[cdn] fetch ip ranges %s: %v", p, err)
			continue
		}
		if err := f.Store.SetIPRanges(ctx, p, cidrs); err != nil {
			log.Printf("[cdn] store ip ranges %s: %v", p, err)
			continue
		}
		log.Printf("[cdn] refreshed %d ip ranges for %s", len(cidrs), p)
	}
}

// RunRefresher starts a background goroutine that calls RefreshAll at the
// given interval. It runs until ctx is cancelled.
func (f *RangeFetcher) RunRefresher(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 4 * time.Hour
	}
	// Seed on startup.
	f.RefreshAll(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.RefreshAll(ctx)
		}
	}
}

// fetch retrieves the IP ranges for a single provider.
func (f *RangeFetcher) fetch(ctx context.Context, p Provider) ([]string, error) {
	switch p {
	case ProviderCloudflare:
		return f.fetchCloudflare(ctx)
	case ProviderFastly:
		return f.fetchFastly(ctx)
	case ProviderBunny:
		return f.fetchBunny(ctx)
	default:
		return nil, fmt.Errorf("cdn: unknown provider: %s", p)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Cloudflare  https://api.cloudflare.com/client/v4/ips
// ─────────────────────────────────────────────────────────────────────────────

type cfIPsResponse struct {
	Result struct {
		IPv4CIDRs []string `json:"ipv4_cidrs"`
		IPv6CIDRs []string `json:"ipv6_cidrs"`
	} `json:"result"`
}

func (f *RangeFetcher) fetchCloudflare(ctx context.Context) ([]string, error) {
	body, err := f.get(ctx, "https://api.cloudflare.com/client/v4/ips")
	if err != nil {
		return nil, err
	}
	var resp cfIPsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("cdn: cloudflare parse: %w", err)
	}
	cidrs := append(resp.Result.IPv4CIDRs, resp.Result.IPv6CIDRs...)
	if bad := ValidateCIDRs(cidrs); len(bad) > 0 {
		log.Printf("[cdn] cloudflare: %d invalid CIDRs ignored: %v", len(bad), bad)
		cidrs = filterValid(cidrs)
	}
	return cidrs, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Fastly  https://api.fastly.com/public-ip-list
// ─────────────────────────────────────────────────────────────────────────────

type fastlyIPsResponse struct {
	Addresses     []string `json:"addresses"`
	IPv6Addresses []string `json:"ipv6_addresses"`
}

func (f *RangeFetcher) fetchFastly(ctx context.Context) ([]string, error) {
	body, err := f.get(ctx, "https://api.fastly.com/public-ip-list")
	if err != nil {
		return nil, err
	}
	var resp fastlyIPsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("cdn: fastly parse: %w", err)
	}
	cidrs := append(resp.Addresses, resp.IPv6Addresses...)
	if bad := ValidateCIDRs(cidrs); len(bad) > 0 {
		log.Printf("[cdn] fastly: %d invalid CIDRs ignored: %v", len(bad), bad)
		cidrs = filterValid(cidrs)
	}
	return cidrs, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Bunny  https://bunnycdn.com/api/system/edgeserverlist/cidr
// ─────────────────────────────────────────────────────────────────────────────

func (f *RangeFetcher) fetchBunny(ctx context.Context) ([]string, error) {
	body, err := f.get(ctx, "https://bunnycdn.com/api/system/edgeserverlist/cidr")
	if err != nil {
		return nil, err
	}
	// Bunny returns a JSON array of CIDR strings.
	var cidrs []string
	if err := json.Unmarshal(body, &cidrs); err != nil {
		return nil, fmt.Errorf("cdn: bunny parse: %w", err)
	}
	if bad := ValidateCIDRs(cidrs); len(bad) > 0 {
		log.Printf("[cdn] bunny: %d invalid CIDRs ignored: %v", len(bad), bad)
		cidrs = filterValid(cidrs)
	}
	return cidrs, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// HTTP helper
// ─────────────────────────────────────────────────────────────────────────────

func (f *RangeFetcher) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("cdn: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := f.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cdn: http get %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cdn: http get %s: status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20)) // 2 MiB max
	if err != nil {
		return nil, fmt.Errorf("cdn: read body %s: %w", url, err)
	}
	return body, nil
}

// filterValid returns only the CIDRs that pass net.ParseCIDR.
func filterValid(cidrs []string) []string {
	out := make([]string, 0, len(cidrs))
	for _, c := range cidrs {
		if bad := ValidateCIDRs([]string{c}); len(bad) == 0 {
			out = append(out, c)
		}
	}
	return out
}
