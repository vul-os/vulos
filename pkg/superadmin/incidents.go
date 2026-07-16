// incidents.go — the superadmin SYSTEM HEALTH + INCIDENTS console (item 6).
//
// The public status page already READS incidents + maintenance from the status
// store (GET /api/status/incidents). Until now those could only be authored via
// the JSON API. This page gives operators an HTML surface over the same store:
// open an incident, post updates to it, resolve it, and schedule maintenance —
// every mutation CSRF-protected and audit-logged like the rest of this console.
//
// The status-store dependency stays in cmd/server behind the IncidentAdmin seam
// so this package imports no status package.
package superadmin

import (
	"context"
	"net/http"
	"strings"
)

// IncidentView is one incident as the console renders it (RFC3339 timestamp
// strings, ready for display).
type IncidentView struct {
	ID         string
	Title      string
	Body       string
	Severity   string
	Components []string
	StartedAt  string
	ResolvedAt string // "" when still open
	Updates    []IncidentUpdateView
}

// Resolved reports whether the incident is closed.
func (i IncidentView) Resolved() bool { return i.ResolvedAt != "" }

// IncidentUpdateView is one timeline update on an incident.
type IncidentUpdateView struct {
	Status   string
	Body     string
	PostedAt string
}

// MaintenanceView is one scheduled maintenance window.
type MaintenanceView struct {
	ID         string
	Title      string
	Body       string
	Components []string
	StartsAt   string
	EndsAt     string
}

// IncidentAdmin is the seam over the status store. cmd/server implements it; this
// package stays free of the status dependency. Every method is fail-closed: an
// error surfaces as an operator-facing flash, never a silent success.
type IncidentAdmin interface {
	List(ctx context.Context) (incidents []IncidentView, maintenance []MaintenanceView, err error)
	Create(ctx context.Context, title, body, severity string, components []string) error
	Update(ctx context.Context, incidentID, status, body string) error
	Resolve(ctx context.Context, incidentID string) error
	ScheduleMaintenance(ctx context.Context, title, body string, components []string, startsAt, endsAt string) error
}

// SetIncidentAdmin wires the incidents console. When unset, the page renders a
// notice and the POST handlers report "not wired".
func (p *Pages) SetIncidentAdmin(a IncidentAdmin) { p.incidentAdmin = a }

type incidentsData struct {
	Available   bool
	Incidents   []IncidentView
	Maintenance []MaintenanceView
	CSRFToken   string
	Flash       string
	FlashErr    string
}

// Incidents renders the incident list, the maintenance schedule and the author
// forms.
func (p *Pages) Incidents(w http.ResponseWriter, r *http.Request) {
	d := incidentsData{Available: p.incidentAdmin != nil, CSRFToken: p.csrf(w, r)}
	if p.incidentAdmin != nil {
		incs, maint, err := p.incidentAdmin.List(r.Context())
		if err == nil {
			d.Incidents = incs
			d.Maintenance = maint
		} else {
			d.FlashErr = "Could not load incidents."
		}
	}
	flash, flashErr := flashFromQuery(r)
	if flash != "" {
		d.Flash = flash
	}
	if flashErr != "" {
		d.FlashErr = flashErr
	}
	p.r.render(w, "Incidents", "incidents", d)
}

// splitComponents parses the comma/space-separated components field into a slice.
func splitComponents(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

const incidentsBack = "/superadmin/incidents"

// IncidentCreate opens a new incident (CSRF-verified upstream, audit-logged).
func (p *Pages) IncidentCreate(w http.ResponseWriter, r *http.Request) {
	if p.incidentAdmin == nil {
		redirectErr(w, r, incidentsBack, "incidents are not wired")
		return
	}
	_ = r.ParseForm()
	title := strings.TrimSpace(r.PostFormValue("title"))
	body := strings.TrimSpace(r.PostFormValue("body"))
	severity := strings.TrimSpace(r.PostFormValue("severity"))
	components := splitComponents(r.PostFormValue("components"))
	if title == "" {
		redirectErr(w, r, incidentsBack, "title is required")
		return
	}
	actor := p.actorEmail(r)
	if err := p.incidentAdmin.Create(r.Context(), title, body, severity, components); err != nil {
		redirectErr(w, r, incidentsBack, "could not create incident")
		return
	}
	if p.al != nil {
		_ = p.al.Record(r.Context(), actor, "incident.create", title, map[string]string{"severity": severity})
	}
	redirectFlash(w, r, incidentsBack, "incident opened")
}

// IncidentUpdate posts an update to an incident (CSRF-verified upstream).
func (p *Pages) IncidentUpdate(w http.ResponseWriter, r *http.Request) {
	if p.incidentAdmin == nil {
		redirectErr(w, r, incidentsBack, "incidents are not wired")
		return
	}
	_ = r.ParseForm()
	id := strings.TrimSpace(r.PostFormValue("incident_id"))
	status := strings.TrimSpace(r.PostFormValue("status"))
	body := strings.TrimSpace(r.PostFormValue("body"))
	if id == "" || body == "" {
		redirectErr(w, r, incidentsBack, "incident and update text are required")
		return
	}
	actor := p.actorEmail(r)
	if err := p.incidentAdmin.Update(r.Context(), id, status, body); err != nil {
		redirectErr(w, r, incidentsBack, "could not add update")
		return
	}
	if p.al != nil {
		_ = p.al.Record(r.Context(), actor, "incident.update", id, map[string]string{"status": status})
	}
	redirectFlash(w, r, incidentsBack, "update posted")
}

// IncidentResolve closes an incident (CSRF-verified upstream).
func (p *Pages) IncidentResolve(w http.ResponseWriter, r *http.Request) {
	if p.incidentAdmin == nil {
		redirectErr(w, r, incidentsBack, "incidents are not wired")
		return
	}
	_ = r.ParseForm()
	id := strings.TrimSpace(r.PostFormValue("incident_id"))
	if id == "" {
		redirectErr(w, r, incidentsBack, "incident is required")
		return
	}
	actor := p.actorEmail(r)
	if err := p.incidentAdmin.Resolve(r.Context(), id); err != nil {
		redirectErr(w, r, incidentsBack, "could not resolve incident")
		return
	}
	if p.al != nil {
		_ = p.al.Record(r.Context(), actor, "incident.resolve", id, nil)
	}
	redirectFlash(w, r, incidentsBack, "incident resolved")
}

// MaintenanceSchedule schedules a maintenance window (CSRF-verified upstream).
func (p *Pages) MaintenanceSchedule(w http.ResponseWriter, r *http.Request) {
	if p.incidentAdmin == nil {
		redirectErr(w, r, incidentsBack, "incidents are not wired")
		return
	}
	_ = r.ParseForm()
	title := strings.TrimSpace(r.PostFormValue("title"))
	body := strings.TrimSpace(r.PostFormValue("body"))
	components := splitComponents(r.PostFormValue("components"))
	startsAt := strings.TrimSpace(r.PostFormValue("starts_at"))
	endsAt := strings.TrimSpace(r.PostFormValue("ends_at"))
	if title == "" || startsAt == "" || endsAt == "" {
		redirectErr(w, r, incidentsBack, "title, starts_at and ends_at (RFC3339) are required")
		return
	}
	actor := p.actorEmail(r)
	if err := p.incidentAdmin.ScheduleMaintenance(r.Context(), title, body, components, startsAt, endsAt); err != nil {
		redirectErr(w, r, incidentsBack, "could not schedule maintenance (check the RFC3339 timestamps)")
		return
	}
	if p.al != nil {
		_ = p.al.Record(r.Context(), actor, "maintenance.schedule", title, nil)
	}
	redirectFlash(w, r, incidentsBack, "maintenance scheduled")
}
