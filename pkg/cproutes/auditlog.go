// auditlog.go — opens the admin audit log and exposes it package-wide.
//
// cpAuditLog is set once during server startup. Every admin-action handler
// calls auditRecord(ctx, actor, action, target, meta) which is a nil-safe
// wrapper, so the log is optional (dev mode) and call sites need no error
// handling.
package cproutes

import (
	"context"
	"log"

	"github.com/vul-os/vulos-management/pkg/auditlog"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/orgadmin"
)

// cpAuditLog is the process-wide admin audit log.
// Set by WireAuditLog during server startup; nil in unit tests that don't call it.
var cpAuditLog *auditlog.Logger

// WireAuditLog opens (or creates) the admin audit log at <dbDir>/auditlog.db.
// Returns a closer that must be called on server shutdown.
func WireAuditLog(dbDir string) func() {
	// COORDINATOR: needs setDBDirIfUnset (composition-root helper, routes_developer_keys.go)
	if err := setDBDirIfUnset(dbDir); err != nil {
		log.Printf("[auditlog] WARNING: could not set DB dir (%v) — audit logging disabled", err)
		return func() {}
	}

	db, err := cpdb.Open("auditlog")
	if err != nil {
		log.Printf("[auditlog] WARNING: could not open cpdb (%v) — audit logging disabled", err)
		return func() {}
	}
	l, err := auditlog.Open(db)
	if err != nil {
		_ = db.Close()
		log.Printf("[auditlog] WARNING: could not open audit log (%v) — audit logging disabled", err)
		return func() {}
	}
	log.Printf("[auditlog] admin audit log opened (backend=%s)", db.Backend())
	cpAuditLog = l
	return func() {
		if err := l.Close(); err != nil {
			log.Printf("[auditlog] close: %v", err)
		}
	}
}

// auditRecord emits an admin audit entry. It is a nil-safe wrapper around
// cpAuditLog.Record: if the audit log is not configured the call is a no-op.
// actor is typically the user email; pass "system" for machine-initiated actions.
func auditRecord(ctx context.Context, actor, action, target string, meta map[string]string) {
	if cpAuditLog == nil {
		return
	}
	if err := cpAuditLog.Record(ctx, actor, action, target, meta); err != nil {
		log.Printf("[auditlog] record error (action=%s target=%s): %v", action, target, err)
	}
}

// AuditRecord is the exported entry point for the shared admin audit log, for
// route files that stay in the commercial module (routes_billing_*.go, etc).
// It delegates to the single process-wide cpAuditLog so every writer appends to
// ONE hash-chained, tamper-evident log — a distributor must NEVER open a second
// logger (that would fork the chain). Nil-safe: a no-op when audit is unconfigured.
func AuditRecord(ctx context.Context, actor, action, target string, meta map[string]string) {
	auditRecord(ctx, actor, action, target, meta)
}

// AuditRecordOrg is the exported org-scoped audit entry point (see auditRecordOrg).
// It delegates to the same shared cpAuditLog.
func AuditRecordOrg(ctx context.Context, tenantID, actor, action, target string, meta map[string]string) {
	auditRecordOrg(ctx, tenantID, actor, action, target, meta)
}

// AuditLogger returns the single process-wide admin audit logger (nil when audit
// logging is not configured). Exported so commercial route files can read the
// SAME logger instance rather than opening a second one and forking the chain.
func AuditLogger() *auditlog.Logger { return cpAuditLog }

// auditRecordOrg emits an ORG-SCOPED admin audit entry (ORGADMIN-AUDIT-01). The
// entry is bound to tenantID and is ONLY ever surfaced to that org's admins via
// GET /api/org/audit — never to another org and never in the platform log view.
// nil-safe + fail-open (an audit-write failure must never block the underlying
// mutation, which has already succeeded at the call site). tenantID must be
// non-empty; an empty tenant is dropped (a blank-tenant org record could leak).
func auditRecordOrg(ctx context.Context, tenantID, actor, action, target string, meta map[string]string) {
	if cpAuditLog == nil || tenantID == "" {
		return
	}
	if err := cpAuditLog.RecordOrg(ctx, tenantID, actor, action, target, meta); err != nil {
		log.Printf("[auditlog] org record error (tenant=%s action=%s): %v", tenantID, action, err)
	}
}

// OrgAuditReader adapts cpAuditLog.QueryOrg onto orgadmin.AuditReader. It reads
// the LIVE cpAuditLog var each call (rather than capturing it at wire time) so
// it works regardless of wiring order, and is nil-safe (returns an empty page
// when audit logging is disabled).
type OrgAuditReader struct{}

// QueryOrgAudit satisfies orgadmin.AuditReader. tenantID is the caller's own org
// (resolved server-side); QueryOrg hard-filters on it so no cross-org / platform
// row is ever returned.
func (OrgAuditReader) QueryOrgAudit(ctx context.Context, tenantID, action string, afterSeq int64, limit int) ([]orgadmin.AuditEntry, error) {
	if cpAuditLog == nil || tenantID == "" {
		return []orgadmin.AuditEntry{}, nil
	}
	rows, err := cpAuditLog.QueryOrg(ctx, tenantID, auditlog.OrgQueryOptions{
		Action:   action,
		AfterSeq: afterSeq,
		Limit:    limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]orgadmin.AuditEntry, 0, len(rows))
	for _, e := range rows {
		out = append(out, orgadmin.AuditEntry{
			Seq:      e.Seq,
			ID:       e.ID,
			TS:       e.TS,
			Actor:    e.Actor,
			Action:   e.Action,
			Target:   e.Target,
			Metadata: e.Metadata,
		})
	}
	return out, nil
}
