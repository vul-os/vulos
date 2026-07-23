// migrations_status.go — aggregate migration / schema-state view.
//
// The migrations status page polls each registered product's status endpoint
// (configured via a MigrationManifest) and renders the aggregate schema state
// across the suite. Each product endpoint is expected to return a JSON body of
// the form:
//
//	{
//	  "status":    "ok" | "pending" | "error" | "unknown",
//	  "pending":   3,          // optional: number of pending migrations
//	  "applied":   12,         // optional: number of applied migrations
//	  "latest":    "0012_…",   // optional: name of the latest applied migration
//	  "message":   "…"         // optional: human-readable status line
//	}
//
// Unreachable endpoints degrade gracefully to status "unreachable" — they do
// not cause the whole page to fail.
//
// The manifest is wired from cmd/server via SetMigrationManifest; it is driven
// by the VULOS_MIGRATE_MANIFEST_JSON env var (JSON array of Product+URL pairs),
// falling back to an empty list when unset (page renders with a notice).
package superadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Manifest types
// ─────────────────────────────────────────────────────────────────────────────

// MigrationEntry is one product in the migration manifest.
type MigrationEntry struct {
	// Product is the human-readable product name (e.g. "mail", "office", "cp").
	Product string `json:"product"`
	// StatusURL is the full URL of the product's migration-status endpoint.
	// The superadmin process must be able to reach it (internal network).
	StatusURL string `json:"status_url"`
}

// MigrationProductStatus is the result of polling one product.
type MigrationProductStatus struct {
	Product   string
	StatusURL string
	// Parsed fields from the status endpoint response.
	Status  string // "ok" | "pending" | "error" | "unreachable" | "unknown"
	Pending int
	Applied int
	Latest  string
	Message string
	// Error set when the poll itself failed (network / decode error).
	PollError string
}

// MigrationsStatusData is passed to the migrations_status template.
type MigrationsStatusData struct {
	Products     []MigrationProductStatus
	HasManifest  bool
	PollDuration string // human-readable time taken for all polls
}

// ─────────────────────────────────────────────────────────────────────────────
// Pages seam
// ─────────────────────────────────────────────────────────────────────────────

// SetMigrationManifest configures the product migration manifest. Each entry
// will be polled when the migrations status page is requested.
func (p *Pages) SetMigrationManifest(entries []MigrationEntry) {
	p.migrateManifest = entries
}

// LoadMigrationManifestFromEnv parses VULOS_MIGRATE_MANIFEST_JSON (a JSON
// array of {product, status_url} objects) and returns the entries. Returns nil
// (no error) when the env var is empty; logs a warning when it is malformed.
func LoadMigrationManifestFromEnv() []MigrationEntry {
	raw := strings.TrimSpace(os.Getenv("VULOS_MIGRATE_MANIFEST_JSON"))
	if raw == "" {
		return nil
	}
	var entries []MigrationEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		fmt.Printf("[superadmin] WARNING: VULOS_MIGRATE_MANIFEST_JSON parse error: %v\n", err)
		return nil
	}
	return entries
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler
// ─────────────────────────────────────────────────────────────────────────────

// MigrationsStatus renders GET /superadmin/migrations.
func (p *Pages) MigrationsStatus(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	d := MigrationsStatusData{
		HasManifest: len(p.migrateManifest) > 0,
	}

	if d.HasManifest {
		d.Products = pollMigrationManifest(r.Context(), p.migrateManifest)
	}

	d.PollDuration = fmt.Sprintf("%.0fms", float64(time.Since(start).Milliseconds()))

	auditFromRequest(r, p.al, p.actorEmail(r), "admin.migrations.view", "", nil)
	p.r.render(w, "Migrations Status", "migrations_status", d)
}

// ─────────────────────────────────────────────────────────────────────────────
// Polling
// ─────────────────────────────────────────────────────────────────────────────

// pollMigrationManifest polls each product's status endpoint concurrently
// (with a per-product timeout) and returns the results.
func pollMigrationManifest(ctx context.Context, manifest []MigrationEntry) []MigrationProductStatus {
	type result struct {
		idx    int
		status MigrationProductStatus
	}
	ch := make(chan result, len(manifest))

	// Per-product context with a hard 5-second timeout (network latency budget).
	for i, entry := range manifest {
		go func(idx int, e MigrationEntry) {
			pollCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			ch <- result{idx: idx, status: pollOne(pollCtx, e)}
		}(i, entry)
	}

	out := make([]MigrationProductStatus, len(manifest))
	for range manifest {
		r := <-ch
		out[r.idx] = r.status
	}
	return out
}

// pollOne polls a single product's status endpoint. Degrades gracefully on any
// error (network, non-200, decode failure).
func pollOne(ctx context.Context, entry MigrationEntry) MigrationProductStatus {
	s := MigrationProductStatus{
		Product:   entry.Product,
		StatusURL: entry.StatusURL,
		Status:    "unknown",
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, entry.StatusURL, nil)
	if err != nil {
		s.Status = "unreachable"
		s.PollError = "build request: " + err.Error()
		return s
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		s.Status = "unreachable"
		s.PollError = err.Error()
		return s
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		s.Status = "unreachable"
		s.PollError = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return s
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if err != nil {
		s.Status = "unreachable"
		s.PollError = "read body: " + err.Error()
		return s
	}

	// Parse the response. All fields are optional; unknown fields are ignored.
	var parsed struct {
		Status  string `json:"status"`
		Pending int    `json:"pending"`
		Applied int    `json:"applied"`
		Latest  string `json:"latest"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		// Non-JSON response — treat the raw body as the message.
		s.Status = "error"
		s.Message = strings.TrimSpace(string(body))
		if len(s.Message) > 200 {
			s.Message = s.Message[:200] + "…"
		}
		s.PollError = "non-JSON response (HTTP " + fmt.Sprint(resp.StatusCode) + ")"
		return s
	}

	s.Status = parsed.Status
	if s.Status == "" {
		s.Status = "unknown"
	}
	s.Pending = parsed.Pending
	s.Applied = parsed.Applied
	s.Latest = parsed.Latest
	s.Message = parsed.Message
	return s
}
