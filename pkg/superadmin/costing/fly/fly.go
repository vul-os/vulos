// Package fly hosts the superadmin infra-cost poller. It polls the Fly billing
// API hourly and persists cost snapshots via the cpdb dual-backend seam
// (SQLite for self-host, Postgres for cloud).
//
// The table name (fly_snapshots), all user-facing labels, and the upstream API
// all say "Fly".
//
// Environment variable:
//
//	FLY_API_TOKEN — org-scoped Bearer token.
//	               If empty, the poller runs as a no-op (single WARNING log).
package fly

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// flyBillingEndpoint is the Fly GraphQL billing API base. The hourly poller and
// its defensive response parser are exercised via httptest mocks (see
// fly_test.go), so the precise wire shape can be finalised against the live API
// without changing the public Store/Poller surface.
const flyBillingEndpoint = "https://api.fly.io/graphql"

// Snapshot is one row of hourly cost data.
type Snapshot struct {
	TakenAt   time.Time
	OrgID     string
	AppName   string
	Region    string
	AmountUSD float64
	Currency  string
}

// Store manages the Fly cost database (fly_snapshots table).
// Backed by either SQLite or Postgres via cpdb.
type Store struct {
	db *cpdb.DB
}

// Open applies migrations to db and returns a ready Store.
//
// db should be obtained from cpdb.Open("costing_fly") for production, or from
// cpdb.OpenSQLiteDSN(":memory:") for tests. Migrations are applied
// automatically and are idempotent (safe to call on every startup).
func Open(db *cpdb.DB) (*Store, error) {
	subFS, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("fly/costing: embed sub: %w", err)
	}
	if err := db.Migrate(subFS); err != nil {
		return nil, fmt.Errorf("fly/costing: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Insert persists a batch of snapshots.
func (s *Store) Insert(ctx context.Context, snaps []Snapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	stmt, err := tx.PrepareContext(ctx, s.db.Rebind(`
		INSERT INTO fly_snapshots (taken_at, org_id, app_name, region, amount_usd, currency)
		VALUES (?, ?, ?, ?, ?, ?)
	`))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, sn := range snaps {
		if _, err := stmt.ExecContext(ctx,
			sn.TakenAt.UTC().Format(time.RFC3339),
			sn.OrgID, sn.AppName, sn.Region, sn.AmountUSD, sn.Currency,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Snapshots returns rows in [from, to] inclusive.
func (s *Store) Snapshots(ctx context.Context, from, to time.Time) ([]Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, s.db.Rebind(`
		SELECT taken_at, org_id, app_name, region, amount_usd, currency
		FROM fly_snapshots
		WHERE taken_at >= ? AND taken_at <= ?
		ORDER BY taken_at ASC
	`), from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var snaps []Snapshot
	for rows.Next() {
		var sn Snapshot
		var takenStr string
		if err := rows.Scan(&takenStr, &sn.OrgID, &sn.AppName, &sn.Region, &sn.AmountUSD, &sn.Currency); err != nil {
			return nil, err
		}
		sn.TakenAt, _ = time.Parse(time.RFC3339, takenStr)
		snaps = append(snaps, sn)
	}
	return snaps, rows.Err()
}

// SpendByRegionSince sums what Fly actually billed us for one region since a
// point in time.
//
// This is the only figure in the whole cost model that is NOT derived from our own
// assumptions — it is the bill. Everything else (machine class rates, egress
// cents-per-GB, markup) is a model, and a model that never checks itself against
// the invoice is fiction. The regions console puts this column next to the modelled
// cost precisely so the two can be compared.
//
// It returns (0, nil) when the poller has recorded nothing for the region. The
// caller MUST distinguish that from a real zero and say "no data" — rendering
// $0.00 for an unpolled region reads as "this region is free", which is the most
// expensive thing a cost dashboard could imply.
func (s *Store) SpendByRegionSince(ctx context.Context, region string, since time.Time) (float64, error) {
	var total sql.NullFloat64
	err := s.db.QueryRowContext(ctx, s.db.Rebind(`
		SELECT COALESCE(SUM(amount_usd), 0)
		FROM fly_snapshots
		WHERE region = ? AND taken_at >= ?
	`), region, since.UTC().Format(time.RFC3339)).Scan(&total)
	if err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Float64, nil
}

// Sweep deletes rows older than 90 days.
func (s *Store) Sweep(ctx context.Context) error {
	cutoff := time.Now().UTC().AddDate(0, 0, -90).Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, s.db.Rebind(`DELETE FROM fly_snapshots WHERE taken_at < ?`), cutoff)
	return err
}

// ─── GraphQL response types (defensive: unknown fields ignored) ──────────────

// graphqlRequest is the JSON body for a GraphQL POST.
type graphqlRequest struct {
	Query string `json:"query"`
}

// orgBillingResponse is the partial GraphQL response we care about.
// We use json.RawMessage for nested objects so unknown schema changes don't crash.
type orgBillingResponse struct {
	Data struct {
		Organization *struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
			// CurrentBillingPeriod may not always exist.
			CurrentBillingPeriod *struct {
				TotalBilled *struct {
					Amount   float64 `json:"amount"`
					Currency string  `json:"currency"`
				} `json:"totalBilled"`
			} `json:"currentBillingPeriod"`
			// Apps is a connection of apps with spend data.
			Apps *struct {
				Nodes []struct {
					Name string `json:"name"`
					// per-app spend — may not be present in all API versions.
					CurrentSpend *struct {
						Amount   float64 `json:"amount"`
						Currency string  `json:"currency"`
					} `json:"currentSpend"`
					// per-region spend — may not be present.
					RegionSpend []struct {
						Region string  `json:"region"`
						Amount float64 `json:"amount"`
					} `json:"regionSpend"`
				} `json:"nodes"`
			} `json:"apps"`
		} `json:"organization"`
	} `json:"data"`
	// Errors are soft-ignored — we log them but don't crash.
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// billingQuery is the org-billing GraphQL query body sent to the Fly API. We
// parse the response defensively using pointer types so missing fields never
// cause failures.
const billingQuery = `
query OrgBilling($slug: String!) {
  organization(slug: $slug) {
    id
    slug
    currentBillingPeriod {
      totalBilled {
        amount
        currency
      }
    }
    apps {
      nodes {
        name
        currentSpend {
          amount
          currency
        }
        regionSpend {
          region
          amount
        }
      }
    }
  }
}
`

// Poller polls Fly billing hourly. Zero value is usable (no-op when token absent).
type Poller struct {
	Store      *Store
	HTTPClient *http.Client
	OrgSlug    string
}

// token returns the Fly API token from env (FLY_API_TOKEN).
func token() string {
	return os.Getenv("FLY_API_TOKEN")
}

// orgSlugFromEnv returns the Fly org slug from env (FLY_ORG_SLUG), used when
// Poller.OrgSlug is empty.
func orgSlugFromEnv() string {
	return os.Getenv("FLY_ORG_SLUG")
}

// Poll performs one billing poll. Returns nil without making any HTTP call when
// no API token is configured (no-op mode).
func (p *Poller) Poll(ctx context.Context) error {
	tok := token()
	if tok == "" {
		log.Println("[fly/costing] WARNING: FLY_API_TOKEN not set — skipping Fly billing poll (no-op)")
		return nil
	}

	orgSlug := p.OrgSlug
	if orgSlug == "" {
		orgSlug = orgSlugFromEnv()
	}
	if orgSlug == "" {
		log.Println("[fly/costing] WARNING: FLY_ORG_SLUG not set — skipping Fly billing poll (no-op)")
		return nil
	}

	body, _ := json.Marshal(map[string]any{
		"query":     billingQuery,
		"variables": map[string]string{"slug": orgSlug},
	})

	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, flyBillingEndpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("fly/costing: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fly/costing: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("fly/costing: unexpected status %d: %s", resp.StatusCode, b)
	}

	var parsed orgBillingResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("fly/costing: decode response: %w", err)
	}

	// Log API-level errors but don't hard-fail (schema may evolve).
	for _, e := range parsed.Errors {
		log.Printf("[fly/costing] API error: %s", e.Message)
	}

	now := time.Now().UTC()
	snaps := parseSnapshots(parsed, now)

	if len(snaps) == 0 {
		// Insert a zero-sum sentinel so we have a record of the poll.
		snaps = []Snapshot{{
			TakenAt: now, OrgID: orgSlug,
			AppName: "_org_total", Region: "", AmountUSD: 0, Currency: "USD",
		}}
	}

	return p.Store.Insert(ctx, snaps)
}

// parseSnapshots extracts Snapshot rows from the GraphQL response.
// All pointer dereferences guard against missing fields (defensive).
func parseSnapshots(r orgBillingResponse, now time.Time) []Snapshot {
	org := r.Data.Organization
	if org == nil {
		return nil
	}

	var snaps []Snapshot
	orgID := org.ID
	if orgID == "" {
		orgID = org.Slug
	}

	// Org-level total.
	if bp := org.CurrentBillingPeriod; bp != nil {
		if tb := bp.TotalBilled; tb != nil {
			currency := tb.Currency
			if currency == "" {
				currency = "USD"
			}
			snaps = append(snaps, Snapshot{
				TakenAt:   now,
				OrgID:     orgID,
				AppName:   "_org_total",
				Region:    "",
				AmountUSD: tb.Amount,
				Currency:  currency,
			})
		}
	}

	// Per-app and per-region spend.
	if apps := org.Apps; apps != nil {
		for _, app := range apps.Nodes {
			if spend := app.CurrentSpend; spend != nil {
				currency := spend.Currency
				if currency == "" {
					currency = "USD"
				}
				snaps = append(snaps, Snapshot{
					TakenAt:   now,
					OrgID:     orgID,
					AppName:   app.Name,
					Region:    "",
					AmountUSD: spend.Amount,
					Currency:  currency,
				})
			}
			for _, rs := range app.RegionSpend {
				snaps = append(snaps, Snapshot{
					TakenAt:   now,
					OrgID:     orgID,
					AppName:   app.Name,
					Region:    rs.Region,
					AmountUSD: rs.Amount,
					Currency:  "USD",
				})
			}
		}
	}

	return snaps
}

// LatestMachineCount returns the count of Fly instances implied by the most
// recent snapshot (approximated as the count of distinct app rows in the most
// recent polling batch).  Returns 0 when the store is empty or on error.
// Safe to call when the poller is not running (env vars unset) — returns 0.
func (s *Store) LatestMachineCount(ctx context.Context) (int, error) {
	if s == nil {
		return 0, nil
	}
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT app_name)
		FROM fly_snapshots
		WHERE taken_at = (SELECT MAX(taken_at) FROM fly_snapshots)
		  AND app_name != '_org_total'
	`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("fly/costing: LatestMachineCount: %w", err)
	}
	return count, nil
}

// RunHourly starts the hourly polling loop. Blocks until ctx is cancelled.
func (p *Poller) RunHourly(ctx context.Context) {
	tok := token()
	if tok == "" {
		log.Println("[fly/costing] WARNING: FLY_API_TOKEN not set — Fly billing poller disabled")
		<-ctx.Done()
		return
	}

	// Initial poll on start.
	if err := p.Poll(ctx); err != nil {
		log.Printf("[fly/costing] initial poll error: %v", err)
	}

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.Poll(ctx); err != nil {
				log.Printf("[fly/costing] poll error: %v", err)
			}
			if err := p.Store.Sweep(ctx); err != nil {
				log.Printf("[fly/costing] sweep error: %v", err)
			}
		}
	}
}
