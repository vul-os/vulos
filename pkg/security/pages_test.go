package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSecurityDashboardRendersHTML verifies the page renders without error.
func TestSecurityDashboardRendersHTML(t *testing.T) {
	store := openTestStore(t)
	pages := NewPages(store)

	req := httptest.NewRequest(http.MethodGet, "/superadmin/security", nil)
	rec := httptest.NewRecorder()

	pages.SecurityDashboard(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Security dashboard") {
		t.Error("want 'Security dashboard' in rendered HTML")
	}
}

// TestSecurityDashboardShowsWAFSection verifies WAF section present in page.
func TestSecurityDashboardShowsWAFSection(t *testing.T) {
	store := openTestStore(t)
	pages := NewPages(store)

	req := httptest.NewRequest(http.MethodGet, "/superadmin/security", nil)
	rec := httptest.NewRecorder()

	pages.SecurityDashboard(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "WAF") {
		t.Error("want WAF section in security dashboard HTML")
	}
}

// TestSecurityDashboardShowsHoneypotSection verifies honeypot section present.
func TestSecurityDashboardShowsHoneypotSection(t *testing.T) {
	store := openTestStore(t)
	pages := NewPages(store)

	req := httptest.NewRequest(http.MethodGet, "/superadmin/security", nil)
	rec := httptest.NewRecorder()

	pages.SecurityDashboard(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "Honeypot") {
		t.Error("want Honeypot section in security dashboard HTML")
	}
}

// TestSecurityDashboardDismissATORedirects verifies dismiss redirects correctly.
func TestSecurityDashboardDismissATORedirects(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	// Pre-seed an ATO event.
	_ = store.RecordATOEvent(ctx, "user@vulos.org", "2fa_reset", "1.2.3.4")
	events, _ := store.PendingATOEvents(ctx, 1)
	if len(events) == 0 {
		t.Fatal("no ATO events to dismiss")
	}

	pages := NewPages(store)

	req := httptest.NewRequest(http.MethodPost,
		"/superadmin/security/ato/dismiss?id="+formatID(events[0].ID), nil)
	rec := httptest.NewRecorder()

	pages.DismissATO(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("want 303 redirect after dismiss, got %d", rec.Code)
	}
}

func formatID(id int64) string {
	if id < 0 {
		return "-1"
	}
	result := make([]byte, 0, 20)
	for id > 0 {
		result = append([]byte{byte('0' + id%10)}, result...)
		id /= 10
	}
	if len(result) == 0 {
		return "0"
	}
	return string(result)
}
