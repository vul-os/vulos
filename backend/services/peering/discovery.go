// Package peering implements Vula OS peer-to-peer communication services.
// This file implements email/display-name discovery against the vulos.org
// directory APIs so that users can locate peers by email address or name.
package peering

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// discoveryDefaultBaseURL is the default vulos.org API base URL used for
// discovery lookups. Override with the VULOS_VERIFY_URL environment variable
// (same variable as used by VerifyService so both share one config knob).
const discoveryDefaultBaseURL = "https://vulos.org"

// discoveryHTTPTimeout caps individual outbound lookup requests.
const discoveryHTTPTimeout = 10 * time.Second

// DiscoveryResult represents a single match returned by the vulos.org
// discovery endpoints. Both fields are always populated when a match is
// found; an empty slice or nil pointer signals "no matches" / "not found".
type DiscoveryResult struct {
	// VulaID is the cryptographic identity string ("vula:ed25519:<base58-pubkey>").
	VulaID string `json:"vula_id"`
	// Server is the reachable address of the peer's Vula instance,
	// e.g. "alice.vulos.org" or "192.0.2.1:8080".
	Server string `json:"server"`
	// DisplayName is the user-chosen name registered with the directory.
	// May be empty if the user has not set one.
	DisplayName string `json:"display_name,omitempty"`
}

// DiscoveryService proxies lookup requests to the vulos.org verify/directory
// APIs. It is intentionally stateless — no caching, no local storage — so
// that results always reflect the upstream source of truth.
//
// Construct via DiscoveryNewService; do not embed or copy after first use.
type DiscoveryService struct {
	baseURL string
	client  *http.Client
}

// DiscoveryNewService returns a DiscoveryService ready for use.
//
// The API base URL is read from VULOS_VERIFY_URL (trimmed of trailing slashes);
// if the variable is unset the production vulos.org endpoint is used.
// An injectable *http.Client may be passed for tests; pass nil for the default.
func DiscoveryNewService(client *http.Client) *DiscoveryService {
	baseURL := strings.TrimRight(os.Getenv("VULOS_VERIFY_URL"), "/")
	if baseURL == "" {
		baseURL = discoveryDefaultBaseURL
	}
	if client == nil {
		client = &http.Client{Timeout: discoveryHTTPTimeout}
	}
	return &DiscoveryService{baseURL: baseURL, client: client}
}

// DiscoveryLookupByEmail queries GET /api/verify/lookup?email=<addr> on
// vulos.org and returns the single result for users who have opted into
// discovery. Returns nil, nil when the email is not found or the user has
// not opted in — callers must treat a nil result as "not found", not as an
// error.
func (ds *DiscoveryService) DiscoveryLookupByEmail(ctx context.Context, email string) (*DiscoveryResult, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, fmt.Errorf("discovery: email must not be empty")
	}

	endpoint := ds.baseURL + "/api/verify/lookup?email=" + url.QueryEscape(email)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("discovery: build request: %w", err)
	}

	resp, err := ds.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discovery: lookup request: %w", err)
	}
	defer resp.Body.Close()

	// 404 → not found / user opted out — graceful empty result.
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("discovery: lookup: server returned %d: %s", resp.StatusCode, string(raw))
	}

	var result DiscoveryResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("discovery: decode lookup response: %w", err)
	}
	if result.VulaID == "" {
		// Server returned 200 with no usable data — treat as not found.
		return nil, nil
	}
	return &result, nil
}

// DiscoverySearchByName queries GET /api/verify/search?name=<query> on the
// vulos.org directory and returns zero or more matching entries. An empty
// slice is a valid result indicating no matches — callers must not treat it
// as an error.
func (ds *DiscoveryService) DiscoverySearchByName(ctx context.Context, name string) ([]DiscoveryResult, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("discovery: name must not be empty")
	}

	endpoint := ds.baseURL + "/api/verify/search?name=" + url.QueryEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("discovery: build request: %w", err)
	}

	resp, err := ds.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discovery: search request: %w", err)
	}
	defer resp.Body.Close()

	// 404 means zero matches.
	if resp.StatusCode == http.StatusNotFound {
		return []DiscoveryResult{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("discovery: search: server returned %d: %s", resp.StatusCode, string(raw))
	}

	var results []DiscoveryResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("discovery: decode search response: %w", err)
	}
	if results == nil {
		results = []DiscoveryResult{}
	}
	return results, nil
}

// RegisterDiscoveryHandlers wires the discovery endpoints into mux:
//
//	GET /api/peering/discover?email=<addr>   — lookup by verified email
//	GET /api/peering/discover?name=<query>   — search by display name
//
// Exactly one of the query parameters must be provided per request. Both
// handlers proxy to vulos.org and return graceful empty responses when there
// are no matches, never an error status for "not found".
func RegisterDiscoveryHandlers(mux *http.ServeMux, svc *DiscoveryService) {
	mux.HandleFunc("GET /api/peering/discover", svc.handleDiscover)
}

// handleDiscover dispatches to the correct upstream lookup based on which
// query parameter was supplied.
//
//   - ?email=addr  → DiscoveryLookupByEmail → single result or empty object
//   - ?name=query  → DiscoverySearchByName  → array (possibly empty)
func (ds *DiscoveryService) handleDiscover(w http.ResponseWriter, r *http.Request) {
	email := strings.TrimSpace(r.URL.Query().Get("email"))
	name := strings.TrimSpace(r.URL.Query().Get("name"))

	switch {
	case email != "" && name != "":
		discoveryWriteErr(w, http.StatusBadRequest, "provide either email or name, not both")

	case email != "":
		result, err := ds.DiscoveryLookupByEmail(r.Context(), email)
		if err != nil {
			discoveryWriteErr(w, http.StatusBadGateway, err.Error())
			return
		}
		if result == nil {
			// Graceful empty: opted out or not registered.
			discoveryWriteJSON(w, http.StatusOK, map[string]any{"found": false})
			return
		}
		discoveryWriteJSON(w, http.StatusOK, map[string]any{
			"found":  true,
			"result": result,
		})

	case name != "":
		results, err := ds.DiscoverySearchByName(r.Context(), name)
		if err != nil {
			discoveryWriteErr(w, http.StatusBadGateway, err.Error())
			return
		}
		discoveryWriteJSON(w, http.StatusOK, map[string]any{
			"results": results,
			"count":   len(results),
		})

	default:
		discoveryWriteErr(w, http.StatusBadRequest, "email or name query parameter is required")
	}
}

// discoveryWriteJSON encodes v as JSON with the given status code.
// Prefixed to avoid collision with any writeJSON defined elsewhere in the package.
func discoveryWriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// discoveryWriteErr writes a JSON error response.
func discoveryWriteErr(w http.ResponseWriter, status int, msg string) {
	discoveryWriteJSON(w, status, map[string]string{"error": msg})
}
