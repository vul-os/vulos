// cloudflare.go — Cloudflare v4 REST API provider implementation.
//
// Uses plain net/http; no external SDK dependency.
// Secrets injected via env (see DNSPLANE_API.md).
package dnsplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

const cfBaseURL = "https://api.cloudflare.com/client/v4"

// cfRecord is the Cloudflare API v4 DNS record shape (subset used here).
type cfRecord struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
}

// cfResponse is the generic Cloudflare API envelope.
type cfResponse struct {
	Success bool            `json:"success"`
	Errors  []cfAPIError    `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cfAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e cfAPIError) Error() string {
	return fmt.Sprintf("cloudflare error %d: %s", e.Code, e.Message)
}

// cfDo sends an authenticated request to the Cloudflare API.
func (c *CloudflareProvider) cfDo(ctx context.Context, method, path string, body any) (json.RawMessage, int, error) {
	var br io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("dnsplane/cf: marshal: %w", err)
		}
		br = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, cfBaseURL+path, br)
	if err != nil {
		return nil, 0, fmt.Errorf("dnsplane/cf: new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("dnsplane/cf: http: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)

	// 401/403 always map to ErrProviderRefused (auth failures).
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, resp.StatusCode, ErrProviderRefused
	}

	var env cfResponse
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("dnsplane/cf: decode response: %w", err)
	}
	if !env.Success {
		if len(env.Errors) > 0 {
			msg := env.Errors[0].Error()
			// Treat provider-side refusals as ErrProviderRefused so callers
			// can match on the sentinel.
			return nil, resp.StatusCode, fmt.Errorf("%w: %s", ErrProviderRefused, msg)
		}
		return nil, resp.StatusCode, fmt.Errorf("%w: unknown CF error (status %d)", ErrProviderRefused, resp.StatusCode)
	}
	return env.Result, resp.StatusCode, nil
}

// CreateRecord implements Provider.
func (c *CloudflareProvider) CreateRecord(ctx context.Context, r Record) (string, error) {
	payload := cfRecord{
		Type:    strings.ToUpper(r.Type),
		Name:    r.FQDN,
		Content: r.Value,
		TTL:     r.TTL,
		Proxied: false,
	}
	raw, _, err := c.cfDo(ctx, http.MethodPost,
		"/zones/"+c.cfg.ZoneID+"/dns_records", payload)
	if err != nil {
		return "", err
	}
	var created cfRecord
	if err := json.Unmarshal(raw, &created); err != nil {
		return "", fmt.Errorf("dnsplane/cf: decode created record: %w", err)
	}
	return created.ID, nil
}

// UpdateRecord implements Provider.
func (c *CloudflareProvider) UpdateRecord(ctx context.Context, r Record) error {
	if r.ProviderID == "" {
		return fmt.Errorf("%w: provider_id required for update", ErrProviderRefused)
	}
	payload := cfRecord{
		Type:    strings.ToUpper(r.Type),
		Name:    r.FQDN,
		Content: r.Value,
		TTL:     r.TTL,
		Proxied: false,
	}
	_, _, err := c.cfDo(ctx, http.MethodPut,
		"/zones/"+c.cfg.ZoneID+"/dns_records/"+r.ProviderID, payload)
	return err
}

// DeleteRecord implements Provider.
func (c *CloudflareProvider) DeleteRecord(ctx context.Context, providerID string) error {
	if providerID == "" {
		return fmt.Errorf("%w: provider_id required for delete", ErrProviderRefused)
	}
	_, _, err := c.cfDo(ctx, http.MethodDelete,
		"/zones/"+c.cfg.ZoneID+"/dns_records/"+providerID, nil)
	return err
}

// cfPool is the Cloudflare Load Balancer pool body for create/replace.
// Requires a paid Cloudflare plan with the Load Balancing add-on and an
// account-level API token with "Load Balancers: Edit" permission.
// Set DNSPLANE_CF_ACCOUNT_ID to enable this path.
type cfPool struct {
	Name           string     `json:"name"`
	Origins        []cfOrigin `json:"origins"`
	MinimumOrigins int        `json:"minimum_origins"`
	Enabled        bool       `json:"enabled"`
	Description    string     `json:"description,omitempty"`
}

// cfOrigin is a single origin in a Cloudflare LB pool.
type cfOrigin struct {
	Name    string  `json:"name"`
	Address string  `json:"address"`
	Enabled bool    `json:"enabled"`
	Weight  float64 `json:"weight"`
}

// cfPoolListResult is the envelope for GET /accounts/{id}/load_balancers/pools.
type cfPoolListResult struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PoolUpsert implements Provider using the Cloudflare Load Balancer pool API
// when AccountID is set, or individual A records (v1 fallback) when it is not.
//
// LB pool path (AccountID non-empty):
//   - Requires a paid Cloudflare plan (Business/Enterprise or LB add-on) and an
//     account-level token with "Load Balancers: Edit" permission.
//   - GET /accounts/{account_id}/load_balancers/pools?name={name} to find if a
//     pool exists; then PUT (replace) or POST (create).
//   - Only healthy members (m.Healthy == true) are included as origins.
//
// A-record fallback (AccountID empty):
//   - Logs a warning and emits one A record per healthy member. This is the v1
//     behaviour; it works without a paid plan but does not provide LB semantics.
func (c *CloudflareProvider) PoolUpsert(ctx context.Context, name string, members []PoolMember) error {
	if name == "" {
		name = c.cfg.PoolName
	}

	// v1 fallback: no AccountID configured → individual A records.
	if c.cfg.AccountID == "" {
		log.Printf("[dnsplane/cf] PoolUpsert: no DNSPLANE_CF_ACCOUNT_ID — falling back to individual A records (set DNSPLANE_CF_ACCOUNT_ID for real LB pool semantics)")
		for _, m := range members {
			if !m.Healthy {
				continue
			}
			payload := cfRecord{
				Type:    "A",
				Name:    name,
				Content: m.IP,
				TTL:     60,
				Proxied: false,
			}
			if _, _, err := c.cfDo(ctx, http.MethodPost,
				"/zones/"+c.cfg.ZoneID+"/dns_records", payload); err != nil {
				return fmt.Errorf("dnsplane/cf: pool upsert member %s: %w", m.IP, err)
			}
		}
		return nil
	}

	// LB pool path: build the origins list from healthy members only.
	origins := make([]cfOrigin, 0, len(members))
	for i, m := range members {
		if !m.Healthy {
			continue
		}
		originName := m.PoPID
		if originName == "" {
			originName = fmt.Sprintf("pop-%d", i+1)
		}
		origins = append(origins, cfOrigin{
			Name:    originName,
			Address: m.IP,
			Enabled: true,
			Weight:  1,
		})
	}

	pool := cfPool{
		Name:           name,
		Origins:        origins,
		MinimumOrigins: 1,
		Enabled:        true,
		Description:    "Vulos PoP pool",
	}

	// Find existing pool by name.
	poolsPath := "/accounts/" + c.cfg.AccountID + "/load_balancers/pools"
	raw, _, err := c.cfDo(ctx, http.MethodGet, poolsPath+"?name="+name, nil)
	if err != nil {
		return fmt.Errorf("dnsplane/cf: list pools: %w", err)
	}
	var existing []cfPoolListResult
	if err := json.Unmarshal(raw, &existing); err != nil {
		return fmt.Errorf("dnsplane/cf: decode pool list: %w", err)
	}

	if len(existing) > 0 {
		// Pool found — replace with PUT.
		_, _, err = c.cfDo(ctx, http.MethodPut, poolsPath+"/"+existing[0].ID, pool)
		if err != nil {
			return fmt.Errorf("dnsplane/cf: put pool %s: %w", existing[0].ID, err)
		}
	} else {
		// Pool not found — create with POST.
		_, _, err = c.cfDo(ctx, http.MethodPost, poolsPath, pool)
		if err != nil {
			return fmt.Errorf("dnsplane/cf: create pool %q: %w", name, err)
		}
	}
	return nil
}
