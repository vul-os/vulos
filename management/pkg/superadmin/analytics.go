// analytics.go — server-rendered analytics dashboard for the super-admin portal.
//
// Data assembly:
//   - Signups by day (last 30 d) and total/new-30d counts — queried directly
//     from users (in auth.db, which the superadmin Store already holds).
//   - DAU / MAU — queried from the sessions table in the same DB.
//   - Per-product 7-day usage trend — assembled by an injectable
//     AnalyticsUsageProvider seam (wired in cmd/server) so this package stays
//     free of the billing-store dependency.
//
// Charts: inline SVG bar charts only — no external JS, consistent with the
// strict CSP (script-src 'self') applied by SecurityHeaders.
package superadmin

import (
	"context"
	"net/http"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// DayCount is a (date, count) pair used for trend charts.
type DayCount struct {
	Day   string // "2006-01-02"
	Count int64
}

// ChartBar is a pre-computed bar for an inline SVG chart.
// BarH is the height in SVG units (0..40); BarY is the top y-coordinate.
type ChartBar struct {
	Day   string
	Count int64
	BarH  int // height in SVG units (0..40)
	BarY  int // top y in SVG units (40 - BarH)
}

// ProductUsageTrend is one product's 7-day aggregate for the analytics dashboard.
type ProductUsageTrend struct {
	Product string
	Unit    string // "sends", "GiB", "min", "tokens", …
	Total7d int64
	PeakDay int64 // max daily count in the window (for SVG scale)
	Days    []DayCount
	// ChartBars are pre-computed for the inline SVG trend bars.
	ChartBars []ChartBar
}

// AnalyticsData is passed to the analytics template.
type AnalyticsData struct {
	// Signup counts from auth.db.
	TotalUsers  int
	NewUsers30d int
	DAU         int // sessions active in last 24 h
	MAU         int // sessions active in last 30 d

	// Signups by day, newest last (30-day window). Pre-computed for SVG sparkline.
	SignupsByDay []DayCount
	SignupPeak   int64      // max count in SignupsByDay (SVG scale)
	SignupBars   []ChartBar // pre-computed SVG bars

	// Per-product 7-day usage trends. Assembled by AnalyticsUsageProvider.
	ProductTrends []ProductUsageTrend
}

// toChartBars converts a DayCount slice to pre-computed SVG bars.
// SVG viewBox height is fixed at 40 units.
func toChartBars(days []DayCount, peak int64) []ChartBar {
	if peak <= 0 {
		peak = 1
	}
	bars := make([]ChartBar, len(days))
	for i, d := range days {
		h := int(d.Count * 40 / peak)
		if h < 1 && d.Count > 0 {
			h = 1
		}
		if h > 40 {
			h = 40
		}
		bars[i] = ChartBar{
			Day:   d.Day,
			Count: d.Count,
			BarH:  h,
			BarY:  40 - h,
		}
	}
	return bars
}

// AnalyticsUsageProvider assembles per-product 7-day usage trends at request
// time. Wired from cmd/server (which has access to the billing store).
// When nil, ProductTrends is empty and the template omits the section.
type AnalyticsUsageProvider func(ctx context.Context) []ProductUsageTrend

// ─────────────────────────────────────────────────────────────────────────────
// Pages seam
// ─────────────────────────────────────────────────────────────────────────────

// SetAnalyticsUsageProvider wires the per-product usage trend assembler.
func (p *Pages) SetAnalyticsUsageProvider(fn AnalyticsUsageProvider) {
	p.analyticsFn = fn
}

// ─────────────────────────────────────────────────────────────────────────────
// Handler
// ─────────────────────────────────────────────────────────────────────────────

// Analytics renders GET /superadmin/analytics.
func (p *Pages) Analytics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := time.Now().UTC()
	d := AnalyticsData{}

	// Total users.
	_ = p.s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&d.TotalUsers)

	// New users in last 30 days.
	since30 := now.AddDate(0, 0, -30).Format("2006-01-02")
	_ = p.s.db.QueryRowContext(ctx,
		p.s.db.Rebind(`SELECT COUNT(*) FROM users WHERE created_at >= ?`), since30,
	).Scan(&d.NewUsers30d)

	// DAU: distinct user_id with a session touched in last 24 h.
	since24h := now.Add(-24 * time.Hour).Format(time.RFC3339)
	_ = p.s.db.QueryRowContext(ctx,
		p.s.db.Rebind(`SELECT COUNT(DISTINCT user_id) FROM sessions
		 WHERE last_seen_at >= ? AND revoked = 0`), since24h,
	).Scan(&d.DAU)
	if d.DAU == 0 {
		// Fallback: count distinct user_id where updated_at / expires_at is recent.
		_ = p.s.db.QueryRowContext(ctx,
			p.s.db.Rebind(`SELECT COUNT(DISTINCT user_id) FROM sessions
			 WHERE expires_at >= ? AND revoked = 0`),
			now.Format(time.RFC3339),
		).Scan(&d.DAU)
	}

	// MAU: distinct user_id with an active session in last 30 days.
	since30RFC := now.AddDate(0, 0, -30).Format(time.RFC3339)
	_ = p.s.db.QueryRowContext(ctx,
		p.s.db.Rebind(`SELECT COUNT(DISTINCT user_id) FROM sessions
		 WHERE last_seen_at >= ? AND revoked = 0`), since30RFC,
	).Scan(&d.MAU)
	if d.MAU == 0 {
		_ = p.s.db.QueryRowContext(ctx,
			p.s.db.Rebind(`SELECT COUNT(DISTINCT user_id) FROM sessions
			 WHERE expires_at >= ? AND revoked = 0`), since30RFC,
		).Scan(&d.MAU)
	}

	// Signups by day (last 30 days). SQLite substr(created_at,1,10) extracts YYYY-MM-DD.
	signupRows, err := p.s.db.QueryContext(ctx,
		p.s.db.Rebind(`SELECT substr(created_at,1,10) AS day, COUNT(*) AS n
		 FROM users
		 WHERE created_at >= ?
		 GROUP BY day
		 ORDER BY day ASC`), since30)
	if err == nil {
		defer signupRows.Close()
		for signupRows.Next() {
			var dc DayCount
			if err2 := signupRows.Scan(&dc.Day, &dc.Count); err2 == nil {
				if dc.Count > d.SignupPeak {
					d.SignupPeak = dc.Count
				}
				d.SignupsByDay = append(d.SignupsByDay, dc)
			}
		}
	}
	// Fill in zero days for any gaps (so the SVG has a consistent 30-bar layout).
	d.SignupsByDay = fillDayGaps(d.SignupsByDay, since30, now.Format("2006-01-02"))
	d.SignupBars = toChartBars(d.SignupsByDay, d.SignupPeak)

	// Per-product trends (best-effort; omitted when no provider wired).
	if p.analyticsFn != nil {
		trends := p.analyticsFn(ctx)
		for i := range trends {
			trends[i].ChartBars = toChartBars(trends[i].Days, trends[i].PeakDay)
		}
		d.ProductTrends = trends
	}

	auditFromRequest(r, p.al, p.actorEmail(r), "admin.analytics.view", "", nil)
	p.r.render(w, "Analytics", "analytics", d)
}

// fillDayGaps ensures the day slice covers every calendar day from from to to
// (YYYY-MM-DD strings, inclusive), inserting zero-count entries for missing days.
func fillDayGaps(rows []DayCount, from, to string) []DayCount {
	// Build lookup map.
	lookup := make(map[string]int64, len(rows))
	for _, r := range rows {
		lookup[r.Day] = r.Count
	}

	start, err1 := time.Parse("2006-01-02", from)
	end, err2 := time.Parse("2006-01-02", to)
	if err1 != nil || err2 != nil {
		return rows
	}

	var out []DayCount
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		out = append(out, DayCount{Day: key, Count: lookup[key]})
	}
	return out
}
