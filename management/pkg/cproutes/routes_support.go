// routes_support.go — SUPPORT-04 per-customer status page endpoint.
//
// Routes registered:
//
//	GET /api/support/status?ulid=   — per-tenant health surface (session)
//	                                  Returns latest telemetry sample + open alerts.
//	                                  Degrades gracefully: if no sample exists,
//	                                  returns zero-valued snapshot so the status
//	                                  page always renders.
//
// SUPPORT-01 dependency: this handler reads the telemetry.Store.
// If SUPPORT-01 is not yet merged, the store is MemStore (no persistent data)
// and the endpoint returns an empty-but-valid shape.
//
// Auth: caller must hold a valid session AND be the owner / org-admin of
// the requested ULID (same authz model as SECX).
package cproutes

import (
	"errors"
	"net/http"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/fleet"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/routing"
	"github.com/vul-os/vulos-management/pkg/telemetry"
)

// registerSupportRoutes wires support status endpoints into mux.
//
// Parameters:
//
//	tel       — telemetry store (MemStore or SQLStore; degrades if empty)
//	authStore — for session-based caller identification
//	fleetStore — for device ownership + org-admin checks
func registerSupportRoutes(
	mux *http.ServeMux,
	tel telemetry.Store,
	authStore *auth.Store,
	fleetStore fleet.Store,
) {
	h := &supportHandlers{tel: tel, auth: authStore, fleet: fleetStore}
	mux.HandleFunc("GET /api/support/status", h.getStatus)
	// The customer's OWN cell, resolved from their session — no ULID to know, and
	// a REDACTED view (never the raw provider app/instance/volume ids), with uptime
	// derived from the append-only fleet_heartbeats history.
	mux.HandleFunc("GET /api/status/mycell", h.getMyCell)
}

// supportHandlers holds dependencies for support HTTP handlers.
type supportHandlers struct {
	tel   telemetry.Store
	auth  *auth.Store
	fleet fleet.Store
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/support/status?ulid=
// ─────────────────────────────────────────────────────────────────────────────

// statusSignal is a single named metric as it appears in the JSON response.
type statusSignal struct {
	Signal   string `json:"signal"`
	Value    any    `json:"value"`
	Unit     string `json:"unit,omitempty"`
	Severity string `json:"severity"` // "ok" | "warn" | "critical"
	Detail   string `json:"detail,omitempty"`
}

// statusResponse is the full payload returned by GET /api/support/status.
type statusResponse struct {
	ULID      string         `json:"ulid"`
	SampledAt string         `json:"sampled_at"` // RFC3339; empty = no data yet
	Signals   []statusSignal `json:"signals"`
	Alerts    []alertSummary `json:"alerts"`   // open (unresolved) alerts
	HasData   bool           `json:"has_data"` // false when no sample exists yet
}

// alertSummary is the public shape of a telemetry.Alert.
type alertSummary struct {
	ID       int64  `json:"id"`
	Signal   string `json:"signal"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
	FiredAt  string `json:"fired_at"` // RFC3339
}

func (h *supportHandlers) getStatus(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return // RequireSession wrote 401
	}

	ulidParam := r.URL.Query().Get("ulid")
	if ulidParam == "" {
		httpx.Err(w, http.StatusBadRequest, "ulid is required")
		return
	}
	canon, err := routing.CanonULID(ulidParam)
	if err != nil {
		httpx.Err(w, http.StatusBadRequest, routing.ErrInvalidULID.Error())
		return
	}

	// Authz: caller must be owner or org admin of this ULID.
	if !h.isOwnerOrAdmin(w, r, u.ID, canon) {
		return
	}

	// Fetch latest telemetry sample. Degrade gracefully if none yet.
	sample, sampleErr := h.tel.LatestSample(r.Context(), canon)
	hasData := sampleErr == nil

	// Fetch open alerts regardless of whether a sample exists.
	alerts, _ := h.tel.OpenAlerts(r.Context(), canon)

	resp := statusResponse{
		ULID:    canon,
		HasData: hasData,
	}

	if hasData {
		resp.SampledAt = sample.SampledAt.UTC().Format("2006-01-02T15:04:05Z")
		resp.Signals = buildSignals(sample)
	} else if !errors.Is(sampleErr, telemetry.ErrSampleNotFound) {
		// Unexpected store error — report it but still return partial data.
		httpx.Err(w, http.StatusInternalServerError, sampleErr.Error())
		return
	}

	// Map open alerts to summary.
	for _, a := range alerts {
		resp.Alerts = append(resp.Alerts, alertSummary{
			ID:       a.ID,
			Signal:   a.Signal,
			Severity: a.Severity,
			Detail:   a.Detail,
			FiredAt:  a.FiredAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	if resp.Alerts == nil {
		resp.Alerts = []alertSummary{} // never null in JSON
	}

	httpx.JSON(w, resp)
}

// buildSignals converts a Sample into the status-page signal list.
// Each signal carries its raw value, unit, and the severity tier
// (ok / warn / critical) derived from telemetry.Threshold.
func buildSignals(s telemetry.Sample) []statusSignal {
	// Build a signal-name → severity map from threshold firings.
	sev := map[string]string{}
	det := map[string]string{}
	for _, f := range telemetry.Threshold(s) {
		sev[f.Signal] = f.Severity
		det[f.Signal] = f.Detail
	}
	sig := func(name string) (string, string) {
		sv, ok := sev[name]
		if !ok {
			sv = "ok"
		}
		return sv, det[name]
	}

	var out []statusSignal

	// ── Bucket sync ──────────────────────────────────────────────────────────
	sv, d := sig("sync_queue_depth")
	out = append(out, statusSignal{
		Signal:   "sync_queue_depth",
		Value:    s.SyncQueueDepth,
		Unit:     "items",
		Severity: sv,
		Detail:   d,
	})

	// ── Mail queue depth ─────────────────────────────────────────────────────
	sv, d = sig("mail_queue_depth")
	out = append(out, statusSignal{
		Signal:   "mail_queue_depth",
		Value:    s.MailQueueDepth,
		Unit:     "msgs",
		Severity: sv,
		Detail:   d,
	})

	// ── IP reputation ────────────────────────────────────────────────────────
	sv, d = sig("ip_reputation")
	out = append(out, statusSignal{
		Signal:   "ip_reputation",
		Value:    s.IPReputationScore,
		Unit:     "/100",
		Severity: sv,
		Detail:   d,
	})

	// ── Blocklist: Spamhaus ──────────────────────────────────────────────────
	blSpam := "ok"
	if s.BlocklistSpamhaus {
		blSpam = "critical"
	}
	out = append(out, statusSignal{
		Signal:   "blocklist_spamhaus",
		Value:    s.BlocklistSpamhaus,
		Severity: blSpam,
		Detail:   det["blocklist_spamhaus"],
	})

	// ── Blocklist: Barracuda ─────────────────────────────────────────────────
	blBarr := "ok"
	if s.BlocklistBarracuda {
		blBarr = "critical"
	}
	out = append(out, statusSignal{
		Signal:   "blocklist_barracuda",
		Value:    s.BlocklistBarracuda,
		Severity: blBarr,
		Detail:   det["blocklist_barracuda"],
	})

	// ── Blocklist: SORBS ─────────────────────────────────────────────────────
	blSorbs := "ok"
	if s.BlocklistSORBS {
		blSorbs = "critical"
	}
	out = append(out, statusSignal{
		Signal:   "blocklist_sorbs",
		Value:    s.BlocklistSORBS,
		Severity: blSorbs,
		Detail:   det["blocklist_sorbs"],
	})

	// ── DKIM expiry ──────────────────────────────────────────────────────────
	sv, d = sig("dkim_expiry")
	out = append(out, statusSignal{
		Signal:   "dkim_expiry_days",
		Value:    s.DKIMExpiryDays,
		Unit:     "days",
		Severity: sv,
		Detail:   d,
	})

	// ── DMARC pass rate ──────────────────────────────────────────────────────
	sv, d = sig("dmarc_pass_rate")
	out = append(out, statusSignal{
		Signal:   "dmarc_pass_rate_pct",
		Value:    s.DMARCPassRatePct,
		Unit:     "%",
		Severity: sv,
		Detail:   d,
	})

	// ── Instance uptime ──────────────────────────────────────────────────────
	out = append(out, statusSignal{
		Signal:   "uptime_sec",
		Value:    s.UptimeSec,
		Unit:     "sec",
		Severity: "ok",
	})

	// ── Last backup age ──────────────────────────────────────────────────────
	sv, d = sig("last_backup_age")
	out = append(out, statusSignal{
		Signal:   "last_backup_age_sec",
		Value:    s.LastBackupAgeSec,
		Unit:     "sec",
		Severity: sv,
		Detail:   d,
	})

	// ── Disk fill ────────────────────────────────────────────────────────────
	sv, d = sig("disk_fill")
	out = append(out, statusSignal{
		Signal:   "disk_used_pct",
		Value:    s.DiskUsedPct,
		Unit:     "%",
		Severity: sv,
		Detail:   d,
	})

	return out
}

// ─────────────────────────────────────────────────────────────────────────────
// Authz helper — reuses the same owner/org-admin logic as secx/fleet.
// ─────────────────────────────────────────────────────────────────────────────

func (h *supportHandlers) isOwnerOrAdmin(w http.ResponseWriter, r *http.Request, callerID, ulid string) bool {
	dev, err := h.fleet.GetDevice(r.Context(), ulid)
	if err != nil {
		httpx.Err(w, http.StatusForbidden, "not owner or device not found")
		return false
	}
	if dev.AccountID == callerID {
		return true
	}
	if dev.OrgID != "" {
		role, roleErr := h.fleet.MemberRole(r.Context(), dev.OrgID, callerID)
		if roleErr == nil && (role == "owner" || role == "admin") {
			return true
		}
	}
	httpx.Err(w, http.StatusForbidden, "not owner or org admin")
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/status/mycell — the caller's OWN cell, resolved from the session.
// ─────────────────────────────────────────────────────────────────────────────

// myCellResponse is the per-customer cell view. It is deliberately REDACTED: it
// carries the box health a customer needs (state, version, uptime, recent
// history) but NEVER the provider app/instance/volume ids that the raw
// managed.Instance / fleet.Device rows hold — those are operator-only.
type myCellResponse struct {
	HasCell       bool          `json:"has_cell"`
	ULID          string        `json:"ulid,omitempty"`
	Name          string        `json:"name,omitempty"`
	Region        string        `json:"region,omitempty"`
	Health        string        `json:"health,omitempty"` // ok | degraded | down | unknown
	Version       string        `json:"version,omitempty"`
	LastHeartbeat string        `json:"last_heartbeat,omitempty"`
	Uptime24h     *float64      `json:"uptime_24h"` // null when no heartbeat history yet
	Uptime7d      *float64      `json:"uptime_7d"`
	History       []cellHBPoint `json:"history"` // oldest-first, for a sparkline
}

// cellHBPoint is one heartbeat rendered for the customer: a timestamp + a coarse
// up/down verdict, nothing provider-identifying.
type cellHBPoint struct {
	At     string `json:"at"`
	Status string `json:"status"` // operational | degraded | down
}

func hbStatus(health string) string {
	switch health {
	case "ok", "healthy", "operational":
		return "operational"
	case "degraded", "warn":
		return "degraded"
	default:
		return "down"
	}
}

func (h *supportHandlers) getMyCell(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}

	// Resolve the caller's own device(s). No ULID is taken from the client, so a
	// customer can only ever see their own cell.
	devs, err := h.fleet.ListByAccount(r.Context(), u.ID, 1, 0)
	if err != nil {
		httpx.Err(w, http.StatusInternalServerError, "could not resolve your cell")
		return
	}
	if len(devs) == 0 {
		httpx.JSON(w, myCellResponse{HasCell: false, History: []cellHBPoint{}})
		return
	}
	d := devs[0]

	resp := myCellResponse{
		HasCell: true,
		ULID:    d.ULID,
		Name:    d.Name,
		Health:  hbStatus(d.Health),
		Version: d.Version,
		History: []cellHBPoint{},
	}
	if d.LastHeartbeat != nil {
		resp.LastHeartbeat = d.LastHeartbeat.UTC().Format("2006-01-02T15:04:05Z")
	}

	// Uptime + sparkline from the append-only heartbeat history (the only
	// timestamped per-box health the CP retains). Bounded read.
	hbs, err := h.fleet.RecentHeartbeats(r.Context(), d.ULID, 500)
	if err == nil && len(hbs) > 0 {
		now := timeNow()
		var up24, tot24, up7, tot7 int
		for _, hb := range hbs {
			st := hbStatus(hb.Health)
			resp.History = append(resp.History, cellHBPoint{
				At:     hb.ReceivedAt.UTC().Format("2006-01-02T15:04:05Z"),
				Status: st,
			})
			age := now.Sub(hb.ReceivedAt)
			isUp := st != "down"
			if age <= 24*time.Hour {
				tot24++
				if isUp {
					up24++
				}
			}
			if age <= 7*24*time.Hour {
				tot7++
				if isUp {
					up7++
				}
			}
		}
		if tot24 > 0 {
			v := float64(up24) / float64(tot24) * 100
			resp.Uptime24h = &v
		}
		if tot7 > 0 {
			v := float64(up7) / float64(tot7) * 100
			resp.Uptime7d = &v
		}
	}

	httpx.JSON(w, resp)
}
