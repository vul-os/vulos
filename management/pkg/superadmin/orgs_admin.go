// orgs_admin.go — org management pages for the super-admin portal.
//
// Data is assembled via injectable seams (OrgListProvider, OrgDetailProvider,
// OrgSuspendFunc, OrgRestoreFunc) wired from cmd/server, keeping this package
// free of the orgadmin store dependency. All state-changing actions go through
// the two-step confirm flow (consistent with account actions).
package superadmin

import (
	"context"
	"net/http"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// OrgAdminRow is one org entry in the list view.
type OrgAdminRow struct {
	ID          string
	Slug        string
	Name        string
	OwnerEmail  string
	MemberCount int
	SeatCount   int    // paid seats, if tracked
	Tier        string // billing tier of the owner account
	Suspended   bool   // whether the owner account is suspended
	CreatedAt   string
}

// OrgAdminDetail is the full drill-down for a single org.
type OrgAdminDetail struct {
	OrgAdminRow
	Members      []OrgMemberRow
	UsageSummary string // human-readable free-text (product provider assembles)
}

// OrgMemberRow is one member entry in the org detail view.
type OrgMemberRow struct {
	AccountID string
	Email     string
	Role      string
	JoinedAt  string
}

// OrgListResult is the response from OrgListProvider.
type OrgListResult struct {
	Orgs    []OrgAdminRow
	HasMore bool
}

// ─────────────────────────────────────────────────────────────────────────────
// Seams
// ─────────────────────────────────────────────────────────────────────────────

// OrgListProvider returns a paged org list, optionally filtered by q. Wired
// from cmd/server (orgadmin.SQLStore + billing store).
type OrgListProvider func(ctx context.Context, q string, limit, offset int) OrgListResult

// OrgDetailProvider returns the full org detail for orgID. Returns nil when
// not found.
type OrgDetailProvider func(ctx context.Context, orgID string) *OrgAdminDetail

// OrgSuspendFunc suspends the owner account of the given org. actorEmail is
// used for audit logging.
type OrgSuspendFunc func(ctx context.Context, orgID, actorEmail, reason string) error

// OrgRestoreFunc removes the suspension from the owner account of the given
// org. actorEmail is used for audit logging.
type OrgRestoreFunc func(ctx context.Context, orgID, actorEmail string) error

// SetOrgProviders wires the org management seams.
func (p *Pages) SetOrgProviders(
	list OrgListProvider,
	detail OrgDetailProvider,
	suspend OrgSuspendFunc,
	restore OrgRestoreFunc,
) {
	p.orgListFn = list
	p.orgDetailFn = detail
	p.orgSuspendFn = suspend
	p.orgRestoreFn = restore
}

// ─────────────────────────────────────────────────────────────────────────────
// Page data types
// ─────────────────────────────────────────────────────────────────────────────

type orgsListData struct {
	Q          string
	Orgs       []OrgAdminRow
	Limit      int
	Offset     int
	PrevOffset int
	NextOffset int
	HasMore    bool
	CSRFToken  string
	Flash      string
	FlashErr   string
}

type orgDetailData struct {
	Org       *OrgAdminDetail
	CSRFToken string
	Flash     string
	FlashErr  string
}

// ─────────────────────────────────────────────────────────────────────────────
// Handlers
// ─────────────────────────────────────────────────────────────────────────────

// OrgsList renders GET /superadmin/orgs.
func (p *Pages) OrgsList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit := queryInt(r, "limit", 50)
	offset := queryInt(r, "offset", 0)

	flash, flashErr := flashFromQuery(r)
	d := orgsListData{
		Q:         q,
		Limit:     limit,
		Offset:    offset,
		Flash:     flash,
		FlashErr:  flashErr,
		CSRFToken: p.csrf(w, r),
	}

	if p.orgListFn != nil {
		res := p.orgListFn(r.Context(), q, limit+1, offset)
		d.HasMore = res.HasMore
		if len(res.Orgs) > limit {
			res.Orgs = res.Orgs[:limit]
			d.HasMore = true
		}
		d.Orgs = res.Orgs
	}

	prev := offset - limit
	if prev < 0 {
		prev = 0
	}
	d.PrevOffset = prev
	d.NextOffset = offset + limit

	auditFromRequest(r, p.al, p.actorEmail(r), "admin.orgs.list", "", nil)
	p.r.render(w, "Orgs", "orgs_list", d)
}

// OrgDetail renders GET /superadmin/orgs/{id}.
func (p *Pages) OrgDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	flash, flashErr := flashFromQuery(r)

	if p.orgDetailFn == nil {
		p.r.render(w, "Org Detail", "org_detail", orgDetailData{
			FlashErr:  "Org data provider not wired.",
			CSRFToken: p.csrf(w, r),
		})
		return
	}

	org := p.orgDetailFn(r.Context(), id)
	if org == nil {
		http.Error(w, "org not found", http.StatusNotFound)
		return
	}

	auditFromRequest(r, p.al, p.actorEmail(r), "admin.orgs.detail", id, nil)
	p.r.render(w, "Org: "+org.Slug, "org_detail", orgDetailData{
		Org:       org,
		CSRFToken: p.csrf(w, r),
		Flash:     flash,
		FlashErr:  flashErr,
	})
}

// OrgActionConfirm renders the confirm page for org suspend/restore:
// GET /superadmin/orgs/{id}/confirm?action=suspend|restore
func (p *Pages) OrgActionConfirm(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	action := r.URL.Query().Get("action")
	back := "/superadmin/orgs/" + id

	if p.orgDetailFn == nil {
		http.Error(w, "org data provider not wired", http.StatusServiceUnavailable)
		return
	}
	org := p.orgDetailFn(r.Context(), id)
	if org == nil {
		http.Error(w, "org not found", http.StatusNotFound)
		return
	}

	d := confirmData{
		ActionURL:    back + "/action",
		CancelURL:    back,
		CSRFToken:    p.csrf(w, r),
		ConfirmLabel: "Confirm",
		Danger:       true,
		RequireTOTP:  stepUpTOTPRequired(),
		Hidden:       []confirmKV{{Name: "action", Value: action}},
	}
	switch action {
	case "suspend":
		d.Heading = "Suspend org"
		d.Lines = []string{
			"Org: " + org.Slug + " (" + org.Name + ")",
			"Owner: " + org.OwnerEmail,
			"This suspends the owner account, blocking access to all products.",
		}
		d.Warn = "All org members lose access immediately."
		d.ConfirmLabel = "Suspend org"
		d.Hidden = append(d.Hidden, confirmKV{Name: "reason", Value: "admin-initiated"})
	case "restore":
		d.Heading = "Restore org"
		d.Lines = []string{
			"Org: " + org.Slug + " (" + org.Name + ")",
			"Owner: " + org.OwnerEmail,
			"This restores the owner account, re-enabling access.",
		}
		d.Warn = "Ensure any abuse/payment issue is resolved before restoring."
		d.ConfirmLabel = "Restore org"
		d.Danger = false
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}

	auditFromRequest(r, p.al, p.actorEmail(r), "admin.orgs.confirm_view", id,
		map[string]string{"action": action})
	p.r.render(w, "Confirm — "+d.Heading, "confirm", d)
}

// OrgActionExecute performs a confirmed org action:
// POST /superadmin/orgs/{id}/action (CSRF-verified upstream).
func (p *Pages) OrgActionExecute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = r.ParseForm()
	action := r.PostFormValue("action")
	back := "/superadmin/orgs/" + id
	actor := p.actorEmail(r)

	if action != "restore" && !p.verifyStepUpTOTP(r, r.PostFormValue("totp")) {
		auditFromRequest(r, p.al, actor, "admin.orgs.stepup_failed", id,
			map[string]string{"action": action})
		redirectErr(w, r, back, "Step-up verification failed.")
		return
	}

	switch action {
	case "suspend":
		if p.orgSuspendFn == nil {
			redirectErr(w, r, back, "Org suspend not wired.")
			return
		}
		reason := r.PostFormValue("reason")
		if reason == "" {
			reason = "admin-initiated"
		}
		if err := p.orgSuspendFn(r.Context(), id, actor, reason); err != nil {
			auditFromRequest(r, p.al, actor, "admin.orgs.suspend.failed", id,
				map[string]string{"error": err.Error()})
			redirectErr(w, r, back, "Suspend failed.")
			return
		}
		auditFromRequest(r, p.al, actor, "admin.orgs.suspend", id,
			map[string]string{"reason": reason})
		redirectFlash(w, r, back, "Org suspended.")
	case "restore":
		if p.orgRestoreFn == nil {
			redirectErr(w, r, back, "Org restore not wired.")
			return
		}
		if err := p.orgRestoreFn(r.Context(), id, actor); err != nil {
			auditFromRequest(r, p.al, actor, "admin.orgs.restore.failed", id,
				map[string]string{"error": err.Error()})
			redirectErr(w, r, back, "Restore failed.")
			return
		}
		auditFromRequest(r, p.al, actor, "admin.orgs.restore", id, nil)
		redirectFlash(w, r, back, "Org restored.")
	default:
		redirectErr(w, r, back, "Unknown action.")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// queryInt parses an integer query parameter with a default fallback.
func queryInt(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	return n
}
