// pages.go — super-admin security dashboard at /superadmin/security.
//
// Gated by RequireSuperAdmin middleware (wired in wire_security.go).
// Uses the same layout template as the existing superadmin portal.
package security

import (
	"bytes"
	_ "embed"
	"html/template"
	"net/http"
)

//go:embed templates/security.html.tmpl
var tplSecurityPage string

// securityLayout is the outer layout. It reuses the SAME Vulos "instrument-panel"
// stylesheet and sticky top bar the rest of the operator console ships
// (/superadmin/admin.css), so the security dashboard is visually part of the
// console rather than a bolt-on panel. Served same-origin under the same strict
// CSP (style-src 'self'); no inline styles, no remote fonts.
const securityLayout = `{{define "security_layout"}}<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="dark">
<title>{{.Title}} — Vulos Operator Console</title>
<link rel="stylesheet" href="/superadmin/admin.css">
</head>
<body>
<header class="topbar">
  <a class="tb-brand" href="/superadmin/">
    <span class="tb-mark">V</span>
    <span class="tb-word">Vulos</span>
    <span class="tb-tag">Operator</span>
  </a>
  <nav class="tb-nav" aria-label="Primary">
    <span class="tb-group">
      <a href="/superadmin/">Overview</a>
      <a href="/superadmin/analytics">Analytics</a>
    </span>
    <span class="tb-group">
      <a href="/superadmin/accounts">Accounts</a>
      <a href="/superadmin/orgs">Orgs</a>
    </span>
    <span class="tb-group">
      <a href="/superadmin/security" class="active">Security</a>
      <a href="/superadmin/auditlog">Audit Log</a>
      <a href="/superadmin/maintenance">Maintenance</a>
    </span>
  </nav>
  <div class="tb-right">
    <span class="tb-op"><span class="dot"></span>operator</span>
    <a class="tb-logout" href="/superadmin/logout">Log out</a>
  </div>
</header>
<main>
{{.Body}}
</main>
</body>
</html>
{{end}}`

// securityPageRenderer is the parsed template set for the security page.
type securityPageRenderer struct {
	t *template.Template
}

func newSecurityPageRenderer() (*securityPageRenderer, error) {
	t, err := template.New("security_layout").Parse(securityLayout)
	if err != nil {
		return nil, err
	}
	if _, err := t.Parse(tplSecurityPage); err != nil {
		return nil, err
	}
	return &securityPageRenderer{t: t}, nil
}

func (r *securityPageRenderer) render(w http.ResponseWriter, title string, data any) {
	var buf bytes.Buffer
	if err := r.t.ExecuteTemplate(&buf, "security_page", data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	type layoutData struct {
		Title string
		Body  template.HTML
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := r.t.ExecuteTemplate(w, "security_layout", layoutData{
		Title: title,
		Body:  template.HTML(buf.String()),
	}); err != nil {
		http.Error(w, "layout error: "+err.Error(), http.StatusInternalServerError)
	}
}

// ─── Handler ─────────────────────────────────────────────────────────────────

// SecurityPageData is the view model for the security dashboard page.
type SecurityPageData struct {
	WAFEvents       []WAFEvent
	BotEvents       []BotEvent
	StepUpEvents    []StepUpEvent
	ATOEvents       []ATOEvent
	CTCerts         []CTCert
	EgressAnomalies []EgressAnomaly
	HoneypotHits    []HoneypotHit
}

// Pages holds the security dashboard handlers.
type Pages struct {
	r     *securityPageRenderer
	store *Store
}

// NewPages constructs a Pages handler. Panics if template parse fails.
func NewPages(store *Store) *Pages {
	r, err := newSecurityPageRenderer()
	if err != nil {
		panic("security: template parse failed: " + err.Error())
	}
	return &Pages{r: r, store: store}
}

// SecurityDashboard renders the /superadmin/security page.
func (p *Pages) SecurityDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := SecurityPageData{}

	if p.store != nil {
		d.WAFEvents, _ = p.store.RecentWAFEvents(ctx, 50)
		d.BotEvents, _ = p.store.RecentBotEvents(ctx, 50)
		d.StepUpEvents, _ = p.store.RecentStepUpEvents(ctx, 50)
		d.ATOEvents, _ = p.store.PendingATOEvents(ctx, 50)
		d.CTCerts, _ = p.store.RecentCTCerts(ctx, 50)
		d.EgressAnomalies, _ = p.store.RecentEgressAnomalies(ctx, 50)
		d.HoneypotHits, _ = p.store.RecentHoneypotHits(ctx, 50)
	}

	p.r.render(w, "Security", d)
}

// DismissATO handles POST /superadmin/security/ato/dismiss?id=<n>.
func (p *Pages) DismissATO(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var id int64
	if _, err := parseIntQuery(r, "id", &id); err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if p.store != nil {
		_ = p.store.DismissATOEvent(r.Context(), id)
	}
	http.Redirect(w, r, "/superadmin/security", http.StatusSeeOther)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func parseIntQuery(r *http.Request, key string, out *int64) (bool, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return false, nil
	}
	n, err := parseInt64(v)
	if err != nil {
		return false, err
	}
	*out = n
	return true, nil
}

func parseInt64(s string) (int64, error) {
	var n int64
	_, err := scanInt64(s, &n)
	return n, err
}

func scanInt64(s string, out *int64) (int, error) {
	if len(s) == 0 {
		return 0, errEmptyInt
	}
	var n int64
	neg := false
	i := 0
	if s[0] == '-' {
		neg = true
		i++
	}
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return i, errBadInt
		}
		n = n*10 + int64(s[i]-'0')
	}
	if neg {
		n = -n
	}
	*out = n
	return i, nil
}

var (
	errEmptyInt = errStatic("empty int")
	errBadInt   = errStatic("bad int")
)

type errStatic string

func (e errStatic) Error() string { return string(e) }
