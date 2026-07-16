package ddos

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

func TestDDoSDashboard_RendersWithoutError(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb open: %v", err)
	}
	bl, err := OpenBlocklistStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("open blocklist: %v", err)
	}
	defer bl.Close()

	mc := NewMetricsCollector()
	limiter := NewIPRateLimiter(DefaultPolicies)
	captcha := NewCaptchaStore(limiter)
	geoFilter, _ := OpenGeoIPFilter()
	budget := NewBudgetCircuitBreaker(DefaultBudgetConfig(), BudgetReaders{}, bl, nil)

	pages := &DDoSPages{
		Metrics:   mc,
		Blocklist: bl,
		Captcha:   captcha,
		GeoIP:     geoFilter,
		Budget:    budget,
	}

	r := httptest.NewRequest("GET", "/superadmin/ddos", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	pages.HandleDDoSDashboard(w, r)

	body := w.Body.String()
	// The dashboard template is now embedded and self-contained (defines its own
	// "layout"), so it renders deterministically in every environment.
	if w.Code != 200 {
		t.Fatalf("unexpected status %d: %s", w.Code, body)
	}
	if !strings.Contains(body, "DDoS defence") {
		t.Errorf("want 'DDoS defence' heading in rendered dashboard")
	}
}
