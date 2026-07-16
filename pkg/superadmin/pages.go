// pages.go — server-rendered HTML page handlers.
//
// Templates are embedded with go:embed; all pages are rendered via the
// base layout in templates/admin/_layout.html.tmpl.
// Plain html/template only — no JS framework, no Tailwind.
package superadmin

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/vul-os/vulos-management/pkg/auditlog"
	"github.com/vul-os/vulos-management/pkg/auth"

	_ "embed"
)

// ─────────────────────────────────────────────────────────────────────────────
// Template embed
// ─────────────────────────────────────────────────────────────────────────────

//go:embed templates/admin/_layout.html.tmpl
var tplLayout string

//go:embed templates/admin/dashboard.html.tmpl
var tplDashboard string

//go:embed templates/admin/login.html.tmpl
var tplLogin string

//go:embed templates/admin/accounts.html.tmpl
var tplAccounts string

//go:embed templates/admin/account_detail.html.tmpl
var tplAccountDetail string

//go:embed templates/admin/reserved_handles.html.tmpl
var tplReservedHandles string

//go:embed templates/admin/auditlog.html.tmpl
var tplAuditLog string

//go:embed templates/admin/maintenance.html.tmpl
var tplMaintenance string

//go:embed templates/admin/analytics.html.tmpl
var tplAnalytics string

//go:embed templates/admin/orgs_list.html.tmpl
var tplOrgsList string

//go:embed templates/admin/org_detail.html.tmpl
var tplOrgDetail string

//go:embed templates/admin/migrations_status.html.tmpl
var tplMigrationsStatus string

//go:embed templates/admin/confirm.html.tmpl
var tplConfirm string

//go:embed templates/admin/relay.html.tmpl
var tplRelay string

//go:embed templates/admin/incidents.html.tmpl
var tplIncidents string

// ─────────────────────────────────────────────────────────────────────────────
// Renderer
// ─────────────────────────────────────────────────────────────────────────────

// renderer holds the parsed template set.
type renderer struct {
	t *template.Template
}

// tplFuncs are the helper functions available to all admin templates. This is an
// operational console only — no money/pricing formatting lives here.
var tplFuncs = template.FuncMap{
	// humanInt formats an integer with thousands separators: 1234567 -> "1,234,567".
	"humanInt": func(n int) string { return groupThousands(int64(n)) },
	// humanInt64 is the same but for int64.
	"humanInt64": func(n int64) string { return groupThousands(n) },
	// migrateClass maps a migration status string to a CSS pill class.
	"migrateClass": func(status string) string {
		switch status {
		case "ok":
			return "pill ok"
		case "pending":
			return "pill warn"
		case "error", "unreachable":
			return "pill danger"
		default:
			return "pill"
		}
	},
	// add adds two integers (used for length-1 indexing in templates).
	"add": func(a, b int) int { return a + b },
	// mul7i multiplies an integer by 7 (SVG x-offset for 7-day trend bars).
	"mul7i": func(i int) int { return i * 7 },
}

// groupThousands formats an int64 with comma thousands separators, preserving a
// leading minus sign: -1234567 -> "-1,234,567".
func groupThousands(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	// Insert commas every three digits from the right.
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	out := b.String()
	if neg {
		return "-" + out
	}
	return out
}

func newRenderer() (*renderer, error) {
	t, err := template.New("layout").Funcs(tplFuncs).Parse(tplLayout)
	if err != nil {
		return nil, err
	}
	for _, src := range []string{
		tplDashboard, tplLogin, tplAccounts, tplAccountDetail,
		tplReservedHandles, tplAuditLog, tplMaintenance, tplConfirm,
		tplAnalytics, tplOrgsList, tplOrgDetail,
		tplMigrationsStatus, tplRelay, tplIncidents,
	} {
		if _, err := t.Parse(src); err != nil {
			return nil, err
		}
	}
	return &renderer{t: t}, nil
}

// navSection maps an inner-template name to the top-bar nav key it belongs to,
// so the layout can mark the active section. Empty means "no highlight" (login,
// confirm). Detail pages inherit their parent section.
func navSection(name string) string {
	switch name {
	case "dashboard":
		return "home"
	case "analytics":
		return "analytics"
	case "accounts", "account_detail":
		return "accounts"
	case "orgs_list", "org_detail":
		return "orgs"
	case "relay":
		return "relay"
	case "incidents":
		return "incidents"
	case "reserved_handles":
		return "handles"
	case "migrations_status":
		return "migrations"
	case "auditlog":
		return "audit"
	case "maintenance":
		return "maintenance"
	default:
		return ""
	}
}

// render executes the named inner template and wraps it in the layout.
func (r *renderer) render(w http.ResponseWriter, title, name string, data any) {
	// Execute inner template into a buffer first.
	var buf bytes.Buffer
	if err := r.t.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Wrap in layout.
	type layoutData struct {
		Title   string
		Section string // active top-bar nav key
		Body    template.HTML
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := r.t.ExecuteTemplate(w, "layout", layoutData{
		Title:   title,
		Section: navSection(name),
		Body:    template.HTML(buf.String()),
	}); err != nil {
		// Layout write failed — not much we can do.
		http.Error(w, "layout error: "+err.Error(), http.StatusInternalServerError)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Page handlers
// ─────────────────────────────────────────────────────────────────────────────

// Pages bundles all HTML page handlers.
//
// This is the OSS OPERATIONAL console only. Commercial surfaces (pricing,
// regions, billing reconciliation, fleet cost rollups) were removed from this
// module — a commercial distributor owns a billing/pricing/regions admin in its
// own module. Nothing here formats money or charges a card.
type Pages struct {
	r       *renderer
	s       *Store
	al      *auditlog.Logger
	auth    *auth.Store
	inbox   auth.InboxSender // for force-password-reset delivery; may be nil
	limiter *loginLimiter    // brute-force throttle for login

	// Analytics seam.
	analyticsFn AnalyticsUsageProvider

	// Orgs management seams.
	orgListFn    OrgListProvider
	orgDetailFn  OrgDetailProvider
	orgSuspendFn OrgSuspendFunc
	orgRestoreFn OrgRestoreFunc

	// Migrations status manifest.
	migrateManifest []MigrationEntry

	// Operator-console seams (relay / incidents). Each is assembled in cmd/server
	// where the backing store lives, keeping this package a pure presentation layer.
	// Nil seams render a "not wired" notice. (The managed-box FLEET seam was removed
	// with box-provisioning — Vulos runs no compute fleet.)
	relayFn       RelayProvider
	incidentAdmin IncidentAdmin
}

// NewPages constructs a Pages handler. If the template parse fails it panics
// (programming error; templates are embedded at compile time).
func NewPages(store *Store, al *auditlog.Logger, authStore *auth.Store) *Pages {
	r, err := newRenderer()
	if err != nil {
		panic("superadmin: template parse failed: " + err.Error())
	}
	return &Pages{r: r, s: store, al: al, auth: authStore, limiter: newLoginLimiter()}
}

// SetInboxSender wires the inbox delivery seam used by the server-rendered
// force-password-reset confirm flow. When unset, force-password-reset returns
// an error rather than silently no-op'ing.
func (p *Pages) SetInboxSender(s auth.InboxSender) { p.inbox = s }

// requestSecure reports whether the request arrived over TLS, so the Secure
// cookie flag can be set correctly behind the Fly.io edge.
func requestSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return strings.EqualFold(r.URL.Scheme, "https")
}

// csrf issues (or reuses) the CSRF token for this request and sets the cookie.
func (p *Pages) csrf(w http.ResponseWriter, r *http.Request) string {
	return issueCSRFToken(w, r, requestSecure(r))
}

// actorEmail resolves the operator's email from the admin account id in context
// (for audit logging). Falls back to the raw id when the lookup fails.
func (p *Pages) actorEmail(r *http.Request) string {
	id := AdminAccountIDFromCtx(r.Context())
	if id == "" {
		return "unknown"
	}
	var email string
	if err := p.s.db.QueryRowContext(r.Context(),
		p.s.db.Rebind(`SELECT email FROM users WHERE id = ?`), id).Scan(&email); err == nil && email != "" {
		return email
	}
	return id
}

// ─── Dashboard ───────────────────────────────────────────────────────────────

// dashboardData is the operator cockpit: purely operational signals (fleet
// health, account/super-admin counts, live incidents/maintenance and the
// hash-chained audit feed). No revenue, cost or pricing lives here.
type dashboardData struct {
	SuperAdminCount int
	AccountCount    int

	// Status seam (nil when no status store is wired).
	HasStatus       bool
	OpenIncidents   int
	MaintenanceSoon int
	// TopIncident is the most recent open incident, surfaced on the health banner.
	TopIncident *IncidentView

	AuditRows []auditRow
}

// AllClear reports whether the fleet has no open incidents (drives the health
// banner). A self-host CP with no status store wired is treated as nominal.
func (d dashboardData) AllClear() bool { return d.OpenIncidents == 0 }

type auditRow struct {
	Seq    int64
	Ts     string
	Actor  string
	Action string
	Target string
}

func (p *Pages) Dashboard(w http.ResponseWriter, r *http.Request) {
	d := dashboardData{}

	_ = p.s.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM superadmins WHERE revoked_at IS NULL`,
	).Scan(&d.SuperAdminCount)

	_ = p.s.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM users`,
	).Scan(&d.AccountCount)

	// Fleet health from the status seam (best-effort; nil-safe).
	if p.incidentAdmin != nil {
		if incs, maint, err := p.incidentAdmin.List(r.Context()); err == nil {
			d.HasStatus = true
			for i := range incs {
				if !incs[i].Resolved() {
					if d.TopIncident == nil {
						d.TopIncident = &incs[i]
					}
					d.OpenIncidents++
				}
			}
			d.MaintenanceSoon = len(maint)
		}
	}

	// Recent audit rows (best-effort).
	rows, err := p.s.db.QueryContext(r.Context(),
		`SELECT seq, ts, actor, action, target
		 FROM auditlog_entries ORDER BY seq DESC LIMIT 12`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var row auditRow
			if err := rows.Scan(&row.Seq, &row.Ts, &row.Actor, &row.Action, &row.Target); err == nil {
				d.AuditRows = append(d.AuditRows, row)
			}
		}
	}

	p.r.render(w, "Dashboard", "dashboard", d)
}

// ─── Login ───────────────────────────────────────────────────────────────────

type loginData struct {
	Stage     string
	Error     string
	CSRFToken string
	// WebAuthnOptions is the marshalled CredentialAssertion, delivered to the
	// external login.js via a data-* attribute (NOT an inline <script>) so the
	// strict CSP (script-src 'self') holds.
	WebAuthnOptions string
}

func (p *Pages) LoginGet(w http.ResponseWriter, r *http.Request) {
	p.r.render(w, "Login", "login", loginData{Stage: "main", CSRFToken: p.csrf(w, r)})
}

// genericLoginErr is the single message shown for every credential/privilege
// failure so the page never reveals whether an account exists, is a super-admin,
// or has TOTP configured.
const genericLoginErr = "Invalid credentials."

// loginFail renders the generic login error (stage reset to main) and re-issues
// a CSRF token.
func (p *Pages) loginFail(w http.ResponseWriter, r *http.Request, msg string) {
	if msg == "" {
		msg = genericLoginErr
	}
	p.r.render(w, "Login", "login", loginData{Stage: "main", Error: msg, CSRFToken: p.csrf(w, r)})
}

// LoginPost handles the main+TOTP step (stage=main).
// On success it redirects to the WebAuthn step.
func (p *Pages) LoginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		p.loginFail(w, r, genericLoginErr)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	totpCode := strings.TrimSpace(r.FormValue("totp"))
	ip := remoteIP(r)
	ua := r.UserAgent()

	// 0. Brute-force throttle: reject if this IP or this target account is
	// currently locked out, before doing any credential work (so a locked
	// attacker gets no timing/oracle signal).
	ipKey := "ip:" + ip
	acctKey := "acct:" + strings.ToLower(email)
	if locked, _ := p.limiter.anyLocked(ipKey, acctKey); locked {
		auditFromRequest(r, p.al, "unknown", "admin.login.throttled", email,
			map[string]string{"reason": "locked out"})
		w.WriteHeader(http.StatusTooManyRequests)
		p.r.render(w, "Login", "login", loginData{
			Stage:     "main",
			Error:     "Too many attempts. Try again later.",
			CSRFToken: p.csrf(w, r),
		})
		return
	}

	// fail is the shared failure path: record the failed attempt against both
	// keys, audit a lockout if one was triggered, and render the generic error.
	fail := func() {
		if p.limiter.recordFailures(ipKey, acctKey) {
			auditFromRequest(r, p.al, "unknown", "admin.login.locked", email,
				map[string]string{"ip": ip})
		}
		p.loginFail(w, r, genericLoginErr)
	}

	// 1. Verify main credentials.
	result, err := p.auth.Login(r.Context(), email, password, ip, ua)
	if err != nil || result == nil {
		fail()
		return
	}

	// 2. TOTP verification (required for admin login). When TOTPRequired=true,
	// result.Token is a partial session; verify the code then upgrade it.
	accountID := result.User.ID
	if result.TOTPRequired {
		if totpCode == "" {
			fail()
			return
		}
		encSecret, enabled, totpErr := p.auth.LoadTOTPSecret(r.Context(), accountID)
		if totpErr != nil || !enabled || encSecret == nil {
			fail()
			return
		}
		kek, kekErr := auth.LoadTOTPKEK()
		if kekErr != nil {
			// Server misconfiguration, not a credential failure — do not count it.
			p.loginFail(w, r, "Server configuration error.")
			return
		}
		plain, decErr := auth.DecryptTOTPSecret(encSecret, kek)
		if decErr != nil {
			p.loginFail(w, r, "Server configuration error.")
			return
		}
		if !auth.VerifyTOTPAndRecord(plain, totpCode, accountID) {
			fail()
			return
		}
		if _, upgradeErr := p.auth.UpgradePartialSession(r.Context(), result.Token, ip, ua); upgradeErr != nil {
			p.loginFail(w, r, "Server configuration error.")
			return
		}
	}

	// 3. Must be a superadmin (generic failure — do not reveal status).
	isSA, _ := p.s.IsSuperAdmin(r.Context(), accountID)
	if !isSA {
		fail()
		return
	}

	// Credentials + TOTP + super-admin status all verified: clear throttle.
	p.limiter.reset(ipKey, acctKey)

	// 4. Begin WebAuthn challenge.
	assertion, _, err := p.s.BeginAdminWebAuthnLogin(r.Context(), accountID)
	if err != nil {
		// No registered key / WebAuthn not configured — generic message.
		p.loginFail(w, r, "Hardware key step unavailable. Contact another operator.")
		return
	}

	optJSON, _ := json.Marshal(assertion)

	// Store accountID in a short-lived cookie for the finish step.
	http.SetCookie(w, &http.Cookie{
		Name:     "vulos_admin_pending",
		Value:    accountID,
		Path:     "/superadmin",
		MaxAge:   300,
		HttpOnly: true,
		Secure:   requestSecure(r),
		SameSite: http.SameSiteStrictMode,
	})

	auditFromRequest(r, p.al, p.actorEmailFor(r, accountID), "admin.login.webauthn_challenge", accountID, nil)

	p.r.render(w, "Login — WebAuthn", "login", loginData{
		Stage:           "webauthn",
		WebAuthnOptions: string(optJSON),
		CSRFToken:       p.csrf(w, r),
	})
}

// actorEmailFor resolves an email for a specific account id (for audit logging).
func (p *Pages) actorEmailFor(r *http.Request, accountID string) string {
	var email string
	if err := p.s.db.QueryRowContext(r.Context(),
		p.s.db.Rebind(`SELECT email FROM users WHERE id = ?`), accountID).Scan(&email); err == nil && email != "" {
		return email
	}
	return accountID
}

// LoginWebAuthnFinish handles the WebAuthn assertion POST.
func (p *Pages) LoginWebAuthnFinish(w http.ResponseWriter, r *http.Request) {
	pending, err := r.Cookie("vulos_admin_pending")
	if err != nil || pending.Value == "" {
		p.r.render(w, "Login", "login", loginData{Stage: "main", Error: "Session expired — start over"})
		return
	}
	accountID := pending.Value

	// Cross-validate the pending accountID against the authenticated main session.
	// This prevents an attacker who can forge/inject the vulos_admin_pending cookie
	// from substituting an arbitrary accountID (including a superadmin's) for the
	// WebAuthn finish step.  The main session must be valid and must belong to the
	// same account as the pending cookie.
	mainUser := p.auth.RequireSession(r.Context(), w, r)
	if mainUser == nil {
		// RequireSession already wrote the error response.
		return
	}
	if mainUser.ID != accountID {
		// Cookie/session mismatch — somebody is tampering.
		// Clear both cookies and restart.
		http.SetCookie(w, &http.Cookie{
			Name: "vulos_admin_pending", Value: "", Path: "/superadmin", MaxAge: -1,
		})
		p.r.render(w, "Login", "login", loginData{Stage: "main", Error: "Session mismatch — start over"})
		return
	}

	token, err := p.s.FinishAdminWebAuthnLogin(r.Context(), accountID, r, remoteIP(r), r.UserAgent())
	if err != nil {
		p.loginFail(w, r, "Hardware key verification failed.")
		return
	}

	SetAdminSessionCookie(w, token, requestSecure(r))

	// Clear pending cookie.
	http.SetCookie(w, &http.Cookie{
		Name: "vulos_admin_pending", Value: "", Path: "/superadmin", MaxAge: -1,
	})

	auditFromRequest(r, p.al, p.actorEmailFor(r, accountID), "admin.login.success", accountID, nil)

	http.Redirect(w, r, "/superadmin/", http.StatusSeeOther)
}

// Logout clears the admin session (server-side + cookie) and redirects to login.
func (p *Pages) Logout(w http.ResponseWriter, r *http.Request) {
	token := AdminSessionFromRequest(r)
	if token != "" {
		_ = p.s.DeleteAdminSession(r.Context(), token)
	}
	ClearAdminSessionCookie(w)
	auditFromRequest(r, p.al, p.actorEmail(r), "admin.logout", "", nil)
	http.Redirect(w, r, "/superadmin/login", http.StatusSeeOther)
}

// flashFromQuery extracts ?flash= / ?err= query params for server-rendered
// success/error banners (replaces the previous client-side flash JS).
func flashFromQuery(r *http.Request) (flash, flashErr string) {
	return r.URL.Query().Get("flash"), r.URL.Query().Get("err")
}

// ─── Accounts ────────────────────────────────────────────────────────────────

type accountsData struct {
	Q          string
	Accounts   []AccountRow
	Limit      int
	Offset     int
	PrevOffset int
	NextOffset int
	HasMore    bool
	CSRFToken  string
	Flash      string
	FlashErr   string
}

func (p *Pages) AccountsList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit == 0 {
		limit = 50
	}
	accounts, _ := p.s.SearchAccounts(r.Context(), q, limit+1, offset)

	hasMore := len(accounts) > limit
	if hasMore {
		accounts = accounts[:limit]
	}
	prevOffset := offset - limit
	if prevOffset < 0 {
		prevOffset = 0
	}

	flash, flashErr := flashFromQuery(r)
	p.r.render(w, "Accounts", "accounts", accountsData{
		Q:          q,
		Accounts:   accounts,
		Limit:      limit,
		Offset:     offset,
		PrevOffset: prevOffset,
		NextOffset: offset + limit,
		HasMore:    hasMore,
		CSRFToken:  p.csrf(w, r),
		Flash:      flash,
		FlashErr:   flashErr,
	})
}

type detailData struct {
	Account   *AccountDetail
	CSRFToken string
	Flash     string
	FlashErr  string
}

func (p *Pages) AccountDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	detail, err := p.s.GetAccountDetail(r.Context(), id)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	flash, flashErr := flashFromQuery(r)
	p.r.render(w, "Account "+detail.Email, "account_detail", detailData{
		Account:   detail,
		CSRFToken: p.csrf(w, r),
		Flash:     flash,
		FlashErr:  flashErr,
	})
}

// ─── Reserved Handles ────────────────────────────────────────────────────────

type rhData struct {
	Handles   []ReservedHandle
	CSRFToken string
	Flash     string
	FlashErr  string
}

func (p *Pages) ReservedHandles(w http.ResponseWriter, r *http.Request) {
	handles, _ := p.s.ListReservedHandles()
	flash, flashErr := flashFromQuery(r)
	p.r.render(w, "Reserved Handles", "reserved_handles", rhData{
		Handles:   handles,
		CSRFToken: p.csrf(w, r),
		Flash:     flash,
		FlashErr:  flashErr,
	})
}

// ─── Audit Log ───────────────────────────────────────────────────────────────

type auditLogData struct {
	Actor  string
	Action string
	From   string
	To     string
	Rows   []auditLogRow
}

type auditLogRow struct {
	Seq          int64
	Ts           string
	Actor        string
	Action       string
	Target       string
	MetadataJSON string
}

func (p *Pages) AuditLog(w http.ResponseWriter, r *http.Request) {
	actor := r.URL.Query().Get("actor")
	action := r.URL.Query().Get("action")
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")

	d := auditLogData{Actor: actor, Action: action, From: from, To: to}

	rows, err := p.s.db.QueryContext(r.Context(),
		p.s.db.Rebind(`SELECT seq, ts, actor, action, target, metadata_json
		 FROM auditlog_entries
		 WHERE (? = '' OR actor LIKE ?)
		   AND (? = '' OR action LIKE ?)
		   AND (? = '' OR ts >= ?)
		   AND (? = '' OR ts <= ?)
		 ORDER BY seq DESC LIMIT 200`),
		actor, "%"+actor+"%",
		action, "%"+action+"%",
		from, from,
		to, to,
	)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var row auditLogRow
			if scanErr := rows.Scan(&row.Seq, &row.Ts, &row.Actor, &row.Action, &row.Target, &row.MetadataJSON); scanErr == nil {
				d.Rows = append(d.Rows, row)
			}
		}
	}

	p.r.render(w, "Audit Log", "auditlog", d)
}

// ─── Maintenance ─────────────────────────────────────────────────────────────

type maintenanceData struct {
	Flash             string
	Error             string
	AuditVerifyResult string
	RotationResult    string
	SubProcessorLog   string
	CSRFToken         string
}

func (p *Pages) Maintenance(w http.ResponseWriter, r *http.Request) {
	p.r.render(w, "Maintenance", "maintenance", maintenanceData{CSRFToken: p.csrf(w, r)})
}

func (p *Pages) VerifyAuditLog(w http.ResponseWriter, r *http.Request) {
	var result string
	if err := p.al.Verify(r.Context(), 0, 1<<62); err != nil {
		result = "CHAIN BROKEN: " + err.Error()
	} else {
		result = "OK — chain intact"
	}
	p.r.render(w, "Maintenance", "maintenance", maintenanceData{AuditVerifyResult: result, CSRFToken: p.csrf(w, r)})
}

func (p *Pages) RotationCheck(w http.ResponseWriter, r *http.Request) {
	// The control plane uses an env-backed SecretsProvider (secrets loaded from
	// environment variables / a vault-compatible KMS at startup).  The
	// secrets.Manager rotation DB is not wired into this process, so per-secret
	// rotatedAt timestamps are not available here.
	//
	// What we CAN report honestly:
	//   1. The most recent secret.rotated audit-log entries (if any have been
	//      recorded by a RotationWorker in the past).
	//   2. A clear explanation of what would need to be configured to get live
	//      rotation-age data.

	var sb strings.Builder

	rows, err := p.s.db.QueryContext(r.Context(),
		`SELECT ts, actor, action, target
		 FROM auditlog_entries
		 WHERE action LIKE 'secret%' OR action LIKE '%rotat%'
		 ORDER BY seq DESC LIMIT 20`)
	if err == nil {
		defer rows.Close()
		count := 0
		for rows.Next() {
			var ts, actor, action, target string
			if scanErr := rows.Scan(&ts, &actor, &action, &target); scanErr == nil {
				sb.WriteString(ts + "  " + actor + "  " + action + "  " + target + "\n")
				count++
			}
		}
		if count == 0 {
			sb.WriteString("No secret rotation events found in the audit log.\n\n")
		} else {
			sb.WriteString("\n")
		}
	} else {
		sb.WriteString("Audit log query error: " + err.Error() + "\n\n")
	}

	sb.WriteString("Note: live rotation-age data (last rotated, next due) requires the\n")
	sb.WriteString("secrets.Manager DB to be wired into this process (secrets.Open +\n")
	sb.WriteString("RotationWorker). Currently the CP uses an env-backed SecretsProvider;\n")
	sb.WriteString("rotation is performed out-of-band by redeploying with a new secret value\n")
	sb.WriteString("or by running the secrets CLI against secrets.db directly.\n")
	sb.WriteString("To enable in-process rotation status: wire secrets.Manager into Pages\n")
	sb.WriteString("and call Manager.StatusAll() here.")

	p.r.render(w, "Maintenance", "maintenance", maintenanceData{
		RotationResult: sb.String(),
		CSRFToken:      p.csrf(w, r),
	})
}

func (p *Pages) SubprocessorChangelog(w http.ResponseWriter, r *http.Request) {
	// Best-effort query for subprocessor changes.
	var sb strings.Builder
	rows, err := p.s.db.QueryContext(r.Context(),
		`SELECT ts, actor, action, target, metadata_json
		 FROM auditlog_entries WHERE action LIKE 'subprocessor%'
		 ORDER BY seq DESC LIMIT 50`)
	if err != nil {
		sb.WriteString("(no subprocessor events found)")
	} else {
		defer rows.Close()
		for rows.Next() {
			var ts, actor, action, target, meta string
			if err := rows.Scan(&ts, &actor, &action, &target, &meta); err == nil {
				sb.WriteString(ts + "\t" + actor + "\t" + action + "\t" + target + "\n")
			}
		}
		if sb.Len() == 0 {
			sb.WriteString("(no subprocessor events found)")
		}
	}
	p.r.render(w, "Maintenance", "maintenance", maintenanceData{SubProcessorLog: sb.String(), CSRFToken: p.csrf(w, r)})
}

// ─── DB helper ───────────────────────────────────────────────────────────────

// GetAdminStats returns a quick summary (superadmin count, account count).
func (s *Store) GetAdminStats(db *sql.DB) (saCount, acctCount int) {
	_ = db.QueryRow(`SELECT COUNT(*) FROM superadmins WHERE revoked_at IS NULL`).Scan(&saCount)
	_ = db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&acctCount)
	return
}
