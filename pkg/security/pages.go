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

// securityLayout is the outer layout (mirrors superadmin's _layout.html.tmpl
// structure — we re-define it here so the security package is self-contained).
const securityLayout = `{{define "security_layout"}}<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}} — Vulos Admin</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:monospace;font-size:13px;background:#f5f5f5;color:#111}
nav{background:#222;color:#eee;padding:8px 16px;display:flex;gap:16px;align-items:center;flex-wrap:wrap}
nav a{color:#ccc;text-decoration:none}
nav a:hover{color:#fff;text-decoration:underline}
nav .brand{font-weight:bold;color:#fff;margin-right:16px}
main{padding:16px}
h1{font-size:16px;margin-bottom:12px}
h2{font-size:14px;margin:16px 0 8px;border-bottom:1px solid #ccc;padding-bottom:4px}
table{border-collapse:collapse;width:100%;background:#fff;margin-bottom:12px}
th,td{border:1px solid #ccc;padding:5px 8px;text-align:left}
th{background:#eee;font-weight:bold}
tr:nth-child(even){background:#fafafa}
.badge-ok{color:#080;font-weight:bold}
.badge-warn{color:#a60;font-weight:bold}
.badge-bad{color:#900;font-weight:bold}
.tile{display:inline-block;background:#fff;border:1px solid #ccc;padding:10px 16px;margin:4px;min-width:120px;vertical-align:top}
.tile .num{font-size:22px;font-weight:bold}
.tile .lbl{font-size:11px;color:#666}
button{font-family:monospace;font-size:12px;padding:3px 8px;cursor:pointer;background:#333;color:#fff;border:1px solid #555}
button:hover{background:#555}
.section{margin-bottom:24px}
</style>
</head>
<body>
<nav>
  <span class="brand">&#9881; Vulos Admin</span>
  <a href="/superadmin/">Dashboard</a>
  <a href="/superadmin/accounts">Accounts</a>
  <a href="/superadmin/reserved-handles">Reserved Handles</a>
  <a href="/superadmin/auditlog">Audit Log</a>
  <a href="/superadmin/maintenance">Maintenance</a>
  <a href="/superadmin/security" style="color:#ff9">Security</a>
  <a href="/superadmin/logout" style="margin-left:auto;color:#f88">Logout</a>
</nav>
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
