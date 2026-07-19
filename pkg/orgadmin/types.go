// Package orgadmin implements the backend API for the unified org-admin
// dashboard (OPS-ADMIN-DASHBOARD-01, Wave E). It backs the six-tab pane
// shipped in src/pages/admin/UnifiedOrgAdmin.jsx:
//
//	GET    /api/org/members
//	POST   /api/org/invite             { email, role, name? }
//	DELETE /api/org/members/{id}
//	PATCH  /api/org/members/{id}/role  { role }
//	PUT    /api/org/members/{id}/name  { name }
//	GET    /api/org/instances          { pooled[], dedicated[], byo[] }
//	GET    /api/org/usage              { active_users, gpu_hours{…}, … }
//	GET    /api/org/quotas             { tier, items[…] }
//	GET    /api/org/seats              { billable_seats, active_members, … } (SEAT-BILLING-01)
//	GET    /api/org/backup-mode        { mode, per_location[], … }
//	PUT    /api/org/backup-mode        { mode, per_location[] }
//	GET    /api/billing/summary        { subscription, wallet_zar, … }
//
// Design: the package stays decoupled from the concrete cross-package stores.
// It defines small reader/writer seams (MemberStore, UsageReader, QuotaReader,
// InstanceLister, BillingSummarizer, BackupModeStore) that main.go adapts onto
// the real subsystems (fleet, billing, gpusku, meetalloc, servingpool,
// multiloc, lancert, storagesel). routes.go takes a SessionGate seam mirroring
// customdomain — the tenant/account id ALWAYS comes from the session, never the
// request — so every route is tenant-scoped and cross-tenant access surfaces
// as 404.
//
// Persistence (this package owns): backup-mode + invite-token rows in a
// pure-Go modernc.org/sqlite database (CGO_ENABLED=0). The storage-mode
// (central-managed vs local-minio-sync, STORE-LOCAL-01) lives in the OS repo
// today, so the backup-mode store here is the cloud-side record of intent;
// see the TODO(wire-storagesel) in store.go.
package orgadmin

// ── Roles ────────────────────────────────────────────────────────────────
//
// The JSX surfaces roles in title-case ("Owner", "Admin", "Member") and the
// fleet store uses lower-case ("owner", "admin", "member"). The adapter layer
// is responsible for translating; this package transports whatever the
// MemberStore returns and accepts whatever the client sends, normalising
// case at the service boundary (see service.go normaliseRole).

// Member is one org seat as the Members tab renders it. Field names match the
// JSX reads: m.id, m.name, m.email, m.role, m.last_active.
type Member struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	Role       string `json:"role"`
	LastActive string `json:"last_active,omitempty"` // RFC3339, "" → JSX shows "—"
}

// MembersResponse is the GET /api/org/members body. The JSX accepts either a
// bare array or {members:[…]}; we return the object form for forward-compat.
//
// PAGINATE-01: the pagination metadata (limit/offset/next_cursor/count) is added
// alongside the roster. All four are omitempty so a response with no paging
// still serialises to the legacy {members:[…]} shape the JSX already reads.
type MembersResponse struct {
	Members    []Member `json:"members"`
	Limit      int      `json:"limit,omitempty"`
	Offset     int      `json:"offset,omitempty"`
	NextCursor string   `json:"next_cursor,omitempty"`
	Count      int      `json:"count,omitempty"`
}

// InviteRequest is the POST /api/org/invite body. Name is OPTIONAL — when
// supplied it is persisted on the invite and applied to the member's display
// name when the member is created; when empty the Members tab falls back to the
// member's email (auth is email+password only — no canonical full-name source).
type InviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
	Name  string `json:"name,omitempty"`
}

// MemberNameRequest is the PUT /api/org/members/{id}/name body. An empty name
// clears the display name (the Members tab then falls back to email).
type MemberNameRequest struct {
	Name string `json:"name"`
}

// InviteResponse acknowledges a created invite.
type InviteResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
	State string `json:"state"`
}

// RoleChangeRequest is the PATCH /api/org/members/{id}/role body.
type RoleChangeRequest struct {
	Role string `json:"role"`
}

// ── Instances ──────────────────────────────────────────────────────────────

// PooledInstance is one multi-tenant pool node. JSX reads: id|node_id, region,
// health, detail_url.
type PooledInstance struct {
	ID        string `json:"id"`
	Region    string `json:"region"`
	Health    string `json:"health"`
	DetailURL string `json:"detail_url,omitempty"`
}

// DedicatedInstance is a Team+ dedicated bundle node. JSX reads: id|node_id,
// region, spec|machine_spec, health, detail_url.
type DedicatedInstance struct {
	ID        string `json:"id"`
	Region    string `json:"region"`
	Spec      string `json:"spec,omitempty"`
	Health    string `json:"health"`
	DetailURL string `json:"detail_url,omitempty"`
}

// BYOInstance is a customer self-host node. JSX reads: id|node_id, label|
// hostname, lan_cert_status, connection_state, offline_since, detail_url.
type BYOInstance struct {
	ID              string `json:"id"`
	Label           string `json:"label,omitempty"`
	Hostname        string `json:"hostname,omitempty"`
	LANCertStatus   string `json:"lan_cert_status,omitempty"`
	ConnectionState string `json:"connection_state"` // "online" | "offline" | "unknown"
	OfflineSince    string `json:"offline_since,omitempty"`
	DetailURL       string `json:"detail_url,omitempty"`
}

// InstancesResponse is the GET /api/org/instances body.
type InstancesResponse struct {
	Pooled    []PooledInstance    `json:"pooled"`
	Dedicated []DedicatedInstance `json:"dedicated"`
	BYO       []BYOInstance       `json:"byo"`
}

// ── Usage ───────────────────────────────────────────────────────────────────

// GPUHours carries per-SKU GPU-hours. JSX reads keys L40S, A100_40GB, H100
// EXACTLY (note the Go json tags pin those names regardless of field casing).
type GPUHours struct {
	L40S    float64 `json:"L40S"`
	A100x40 float64 `json:"A100_40GB"`
	H100    float64 `json:"H100"`
}

// UsageResponse is the GET /api/org/usage body. JSX reads: active_users,
// active_users_cap, gpu_hours{…}, storage_gb.
//
// (meet_participant_minutes / meet_recording_minutes were removed 2026-07-19:
// Talk and Meet are no longer Vulos products — comms are third-party — and no
// adapter ever populated these fields in production; billing is Relay +
// backup storage only.)
type UsageResponse struct {
	ActiveUsers    int      `json:"active_users"`
	ActiveUsersCap *int     `json:"active_users_cap,omitempty"` // nil → JSX shows "no cap"
	GPUHours       GPUHours `json:"gpu_hours"`
	StorageGB      float64  `json:"storage_gb"`
}

// ── Quotas ───────────────────────────────────────────────────────────────────

// QuotaItem is one quota bar. JSX reads: key, label, used, cap, unit.
type QuotaItem struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Used  float64 `json:"used"`
	Cap   float64 `json:"cap"`
	Unit  string  `json:"unit,omitempty"`
}

// QuotasResponse is the GET /api/org/quotas body. JSX reads: tier, items[].
type QuotasResponse struct {
	Tier  string      `json:"tier"`
	Items []QuotaItem `json:"items"`
}

// ── Backup mode ───────────────────────────────────────────────────────────────

// Backup-mode values. central → the managed store (default); local → MinIO sync.
const (
	BackupModeCentral = "central"
	BackupModeLocal   = "local"
)

// PerLocationMode is one BYO-location backup override. JSX reads: id, label,
// hostname|region, mode.
type PerLocationMode struct {
	ID       string `json:"id"`
	Label    string `json:"label,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Region   string `json:"region,omitempty"`
	Mode     string `json:"mode"`
}

// BackupModeResponse is the GET /api/org/backup-mode body. JSX reads: mode,
// per_location[], last_sync, sync_health.
type BackupModeResponse struct {
	Mode        string            `json:"mode"`
	PerLocation []PerLocationMode `json:"per_location"`
	LastSync    string            `json:"last_sync,omitempty"`
	SyncHealth  string            `json:"sync_health,omitempty"`
}

// BackupModeRequest is the PUT /api/org/backup-mode body.
type BackupModeRequest struct {
	Mode        string            `json:"mode"`
	PerLocation []PerLocationMode `json:"per_location"`
}

// ── Billing summary ───────────────────────────────────────────────────────────

// SubscriptionSummary is the nested subscription object the JSX reads:
// summary.subscription.tier, .state, .current_period_end.
type SubscriptionSummary struct {
	Tier             string `json:"tier"`
	State            string `json:"state"`
	CurrentPeriodEnd string `json:"current_period_end,omitempty"` // RFC3339
	// DunningState surfaces the non-payment posture (DUNNING-01):
	// "current" | "grace" | "suspended". Empty when no dunning info is wired.
	// The dashboard renders a banner when this is "grace"/"suspended".
	DunningState string `json:"dunning_state,omitempty"`
	// GraceUntil is the RFC3339 instant entitlement is retained until while in
	// "grace"; empty otherwise.
	GraceUntil string `json:"grace_until,omitempty"`
}

// BillingSummaryResponse is the GET /api/billing/summary body. JSX reads:
// summary.subscription, .wallet_zar, .wallet_usd, .upcoming_invoice_zar,
// .card_last4, .rate.
type BillingSummaryResponse struct {
	Subscription       SubscriptionSummary `json:"subscription"`
	WalletZAR          float64             `json:"wallet_zar"`
	WalletUSD          float64             `json:"wallet_usd"`
	UpcomingInvoiceZAR float64             `json:"upcoming_invoice_zar"`
	CardLast4          string              `json:"card_last4,omitempty"`
	Rate               float64             `json:"rate"`
}
