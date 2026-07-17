package cproutes

// wire_superadmin_console_test.go — verifies the operator JSON admin handlers
// mirror the operator-page data faithfully, reading the REAL audit + security
// stores (not the gate — the gate is exercised by pkg/superadmin's own tests and
// the self-host boot). The handlers here are the "add JSON endpoints where only
// HTML handlers exist" half of the operator-admin→React conversion.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vul-os/vulos-management/pkg/auditlog"
	"github.com/vul-os/vulos-management/pkg/cpdb"
	"github.com/vul-os/vulos-management/pkg/security"
	"github.com/vul-os/vulos-management/pkg/superadmin"
)

func TestAdminDashboardJSON(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN("file:sad_dash?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("cpdb: %v", err)
	}
	saStore, err := superadmin.New(db)
	if err != nil {
		t.Fatalf("superadmin.New: %v", err)
	}
	al := newTestAuditLogger(t)
	_ = al.Record(context.Background(), "op@example.test", "admin.login", "session", map[string]string{"ip": "127.0.0.1"})

	rec := httptest.NewRecorder()
	handleAdminDashboard(saStore, al)(rec, httptest.NewRequest(http.MethodGet, "/api/superadmin/dashboard", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d, want 200", rec.Code)
	}
	var out struct {
		SuperAdminCount int                 `json:"superadmin_count"`
		AccountCount    int                 `json:"account_count"`
		RecentAudit     []auditlog.OrgEntry `json:"recent_audit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if len(out.RecentAudit) != 1 || out.RecentAudit[0].Action != "admin.login" {
		t.Fatalf("recent_audit = %+v, want the recorded admin.login row", out.RecentAudit)
	}
}

func TestAdminAuditJSONFilters(t *testing.T) {
	al := newTestAuditLogger(t)
	ctx := context.Background()
	_ = al.Record(ctx, "alice", "admin.login", "s", nil)
	_ = al.Record(ctx, "bob", "admin.denied.ip", "s", nil)
	_ = al.Record(ctx, "alice", "reserved.add", "handle:root", nil)

	// Unfiltered: newest-first, all three.
	rec := httptest.NewRecorder()
	handleAdminAudit(al)(rec, httptest.NewRequest(http.MethodGet, "/api/superadmin/audit", nil))
	var all struct {
		Rows  []auditlog.OrgEntry `json:"rows"`
		Count int                 `json:"count"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &all)
	if all.Count != 3 {
		t.Fatalf("unfiltered count = %d, want 3", all.Count)
	}
	if all.Rows[0].Actor != "alice" || all.Rows[0].Action != "reserved.add" {
		t.Fatalf("newest row = %+v, want alice/reserved.add", all.Rows[0])
	}

	// Actor filter.
	rec = httptest.NewRecorder()
	handleAdminAudit(al)(rec, httptest.NewRequest(http.MethodGet, "/api/superadmin/audit?actor=bob", nil))
	var filtered struct {
		Rows []auditlog.OrgEntry `json:"rows"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &filtered)
	if len(filtered.Rows) != 1 || filtered.Rows[0].Actor != "bob" {
		t.Fatalf("actor=bob rows = %+v, want one bob row", filtered.Rows)
	}
}

func TestAdminSecurityJSON(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN("file:sas_sec?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("cpdb: %v", err)
	}
	sec, err := security.Open(db)
	if err != nil {
		t.Fatalf("security.Open: %v", err)
	}
	if err := sec.RecordWAFEvent(context.Background(), "sqli", "' OR 1=1", "10.0.0.9", "/login"); err != nil {
		t.Fatalf("record waf: %v", err)
	}

	rec := httptest.NewRecorder()
	handleAdminSecurity(sec)(rec, httptest.NewRequest(http.MethodGet, "/api/superadmin/security", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("security status = %d, want 200", rec.Code)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, k := range []string{"waf", "bots", "stepups", "ato", "honeypot", "egress", "ct"} {
		if _, ok := out[k]; !ok {
			t.Fatalf("security payload missing section %q", k)
		}
	}
	var waf []map[string]any
	_ = json.Unmarshal(out["waf"], &waf)
	if len(waf) != 1 || waf[0]["RuleID"] != "sqli" {
		t.Fatalf("waf = %+v, want the recorded sqli event", waf)
	}

	// Nil store → all sections empty, still 200.
	rec = httptest.NewRecorder()
	handleAdminSecurity(nil)(rec, httptest.NewRequest(http.MethodGet, "/api/superadmin/security", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("nil-store security status = %d, want 200", rec.Code)
	}
}

// newTestAuditLogger builds an in-memory audit logger for the handler tests.
func newTestAuditLogger(t *testing.T) *auditlog.Logger {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN("file:saal_" + t.Name() + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("cpdb audit: %v", err)
	}
	al, err := auditlog.Open(db)
	if err != nil {
		t.Fatalf("auditlog.Open: %v", err)
	}
	t.Cleanup(func() { _ = al.Close() })
	return al
}
