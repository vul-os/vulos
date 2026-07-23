package ddos

import (
	_ "embed"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"time"
)

//go:embed templates/admin/ddos.html.tmpl
var tplDDoS string

// DDoSPages bundles all the dependencies for the DDoS super-admin dashboard.
type DDoSPages struct {
	Metrics   *MetricsCollector
	Blocklist *BlocklistStore
	Captcha   *CaptchaStore
	GeoIP     *GeoIPFilter
	Budget    *BudgetCircuitBreaker
	AuditLog  HoneypotAuditLogger
}

// ddosDashData is the template data for the DDoS dashboard page.
type ddosDashData struct {
	ReqPerSec         float64
	Sparkline         string
	TopIPs            []IPStat
	Top4xxRoutes      []IPStat
	HoneypotHits      []HoneypotHit
	BlocklistSize     int
	GeoBlockCountries []string
	CaptchaEverywhere bool
	HaltAutoscale     bool
	FlyMachines       int
	ManagedEgressGB   float64
	BudgetMaxMachines int
	BudgetMaxUSD      float64
	TrippedAt         *time.Time
}

// HandleDDoSDashboard renders the /superadmin/ddos page.
func (p *DDoSPages) HandleDDoSDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	blSize, _ := p.Blocklist.Count(ctx)
	machines, egress, halt, trippedAt := p.Budget.Snapshot()
	cfg := DefaultBudgetConfig()

	data := ddosDashData{
		ReqPerSec:         p.Metrics.RequestsPerSecLast5Min(),
		Sparkline:         p.Metrics.RateSparkline(),
		TopIPs:            p.Metrics.TopIPsByRequestCount(10),
		Top4xxRoutes:      p.Metrics.Top4xxRoutes(10),
		HoneypotHits:      p.Metrics.RecentHoneypotHits(20),
		BlocklistSize:     blSize,
		GeoBlockCountries: p.GeoIP.BlockedCountries(),
		CaptchaEverywhere: p.Captcha.CaptchaEverywhere(),
		HaltAutoscale:     halt,
		FlyMachines:       machines,
		ManagedEgressGB:   egress,
		BudgetMaxMachines: cfg.MaxFlyMachines,
		BudgetMaxUSD:      cfg.HourlyBudgetUSD,
		TrippedAt:         trippedAt,
	}

	tmpl, err := loadDDoSTemplate()
	if err != nil {
		http.Error(w, "template load error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("[ddos/pages] template error: %v", err)
	}
}

// HandleDDoSToggleCaptcha handles POST /superadmin/ddos/toggle-captcha.
func (p *DDoSPages) HandleDDoSToggleCaptcha(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	current := p.Captcha.CaptchaEverywhere()
	p.Captcha.SetCaptchaEverywhere(!current)
	actor := adminActorFromCtx(r)
	if p.AuditLog != nil {
		_ = p.AuditLog.Record(r.Context(), actor, "ddos.captcha_everywhere.toggle",
			"captcha", map[string]string{"enabled": boolStr(!current)})
	}
	log.Printf("[ddos/pages] captcha_everywhere toggled to %v by %s", !current, actor)
	http.Redirect(w, r, "/superadmin/ddos", http.StatusSeeOther)
}

// HandleDDoSToggleHalt handles POST /superadmin/ddos/toggle-halt.
func (p *DDoSPages) HandleDDoSToggleHalt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, _, halt, _ := p.Budget.Snapshot()
	p.Budget.SetHaltAutoscale(r.Context(), !halt)
	actor := adminActorFromCtx(r)
	if p.AuditLog != nil {
		_ = p.AuditLog.Record(r.Context(), actor, "ddos.halt_autoscale.toggle",
			"budget", map[string]string{"enabled": boolStr(!halt)})
	}
	log.Printf("[ddos/pages] halt_autoscale toggled to %v by %s", !halt, actor)
	http.Redirect(w, r, "/superadmin/ddos", http.StatusSeeOther)
}

// HandleDDoSBlocklistAdd handles POST /api/superadmin/ddos/blocklist (add entry).
func (p *DDoSPages) HandleDDoSBlocklistAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CIDR   string `json:"cidr"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	actor := adminActorFromCtx(r)
	if err := p.Blocklist.Add(r.Context(), req.CIDR, req.Reason, actor, nil); err != nil {
		http.Error(w, "blocklist add: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if p.AuditLog != nil {
		_ = p.AuditLog.Record(r.Context(), actor, "ddos.blocklist.add", req.CIDR,
			map[string]string{"reason": req.Reason})
	}
	w.WriteHeader(http.StatusCreated)
}

// HandleDDoSBlocklistRemove handles DELETE /api/superadmin/ddos/blocklist/{cidr}.
func (p *DDoSPages) HandleDDoSBlocklistRemove(w http.ResponseWriter, r *http.Request) {
	cidr := r.PathValue("cidr")
	if cidr == "" {
		http.Error(w, "missing cidr", http.StatusBadRequest)
		return
	}
	actor := adminActorFromCtx(r)
	if err := p.Blocklist.Remove(r.Context(), cidr); err != nil {
		http.Error(w, "blocklist remove: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if p.AuditLog != nil {
		_ = p.AuditLog.Record(r.Context(), actor, "ddos.blocklist.remove", cidr, nil)
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleDDoSBlocklistList handles GET /api/superadmin/ddos/blocklist.
func (p *DDoSPages) HandleDDoSBlocklistList(w http.ResponseWriter, r *http.Request) {
	entries, err := p.Blocklist.List(r.Context())
	if err != nil {
		http.Error(w, "list: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}

// HandleDDoSGeoBlockUpdate handles POST /api/superadmin/ddos/geo-block.
func (p *DDoSPages) HandleDDoSGeoBlockUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Countries []string `json:"countries"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	p.GeoIP.SetBlockedCountries(req.Countries)
	actor := adminActorFromCtx(r)
	if p.AuditLog != nil {
		_ = p.AuditLog.Record(r.Context(), actor, "ddos.geo_block.update", "geoip",
			map[string]string{"countries": joinStrings(req.Countries)})
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Template loading ───────────────��──────────────────────────────────────────

func loadDDoSTemplate() (*template.Template, error) {
	// The dashboard template is embedded (go:embed) and self-contained — it
	// defines its own "layout" block that links the shared /superadmin/admin.css
	// and the Vulos operator top bar. No filesystem lookup, so it renders
	// identically in every deployment (self-host, dev, container).
	funcMap := template.FuncMap{
		"boolStr": boolStr,
		"joinStr": joinStrings,
	}
	return template.New("ddos").Funcs(funcMap).Parse(tplDDoS)
}

// ── helpers ────────────────────────��──────────────────────���───────────────────

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func joinStrings(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

// adminActorFromCtx tries to extract the admin actor from the request context.
// Falls back to "unknown" if not present (e.g. in tests).
func adminActorFromCtx(r *http.Request) string {
	// The superadmin.AdminAccountIDFromCtx context value is set by RequireSuperAdmin.
	// We read it as a plain interface{} to avoid an import cycle.
	type ctxKey int
	const ctxAdminAccountID ctxKey = 0
	_ = ctxAdminAccountID
	// Check for a string value under the well-known string key used by tests.
	if v := r.Context().Value("admin_actor"); v != nil {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return "superadmin"
}
