// zz_screenshot_dump_test.go — offline HTML dumper for the security dashboard.
//
// Tooling harness for scripts/screenshots: it seeds an in-memory security store
// with fabricated events and renders the real /superadmin/security dashboard
// template to a standalone .html file for headless capture. Not a behavioural
// test — it runs only when SCREENSHOT_DUMP=1 and writes to SCREENSHOT_OUT.
package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScreenshotDumpSecurityDashboard(t *testing.T) {
	if os.Getenv("SCREENSHOT_DUMP") != "1" {
		t.Skip("set SCREENSHOT_DUMP=1 to dump the security dashboard (used by scripts/screenshots)")
	}
	outDir := os.Getenv("SCREENSHOT_OUT")
	if outDir == "" {
		t.Fatal("SCREENSHOT_OUT must be set")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}

	store := openTestStore(t)
	ctx := context.Background()

	// ── Seed a realistic mix of security events ──────────────────────────────
	_ = store.RecordWAFEvent(ctx, "sqli-001", "' OR 1=1 --", "203.0.113.9", "/api/auth/login")
	_ = store.RecordWAFEvent(ctx, "xss-014", "<script>", "198.51.100.22", "/api/profile")
	_ = store.RecordWAFEvent(ctx, "trav-003", "../../etc/passwd", "203.0.113.44", "/api/files/get")

	_ = store.RecordBotEvent(ctx, "45.155.205.8", 0.94, "datacenter ASN + no JS", "python-requests/2.31")
	_ = store.RecordBotEvent(ctx, "185.220.101.7", 0.81, "tor exit + rapid enumeration", "curl/8.4.0")

	_ = store.RecordStepUpEvent(ctx, "ada@vulos.org", "10.0.0.7", 0.72)
	_ = store.RecordStepUpEvent(ctx, "grace@vulos.org", "10.0.0.9", 0.55)

	_ = store.RecordATOEvent(ctx, "linus@acme.test", "impossible-travel login", "102.89.34.12")
	_ = store.RecordATOEvent(ctx, "margaret@acme.test", "credential-stuffing pattern", "45.155.205.8")

	_ = store.RecordHoneypotHit(ctx, "admin@vulos.org", "45.83.64.1")
	_ = store.RecordHoneypotHit(ctx, "root@vulos.org", "89.248.165.52")

	_ = store.RecordEgressAnomaly(ctx, "dennis@example.test", 4_200_000_000, 180_000_000, 6.4)

	_ = store.UpsertCTCert(ctx, "vulos.org", "Let's Encrypt R3", "2026-07-01", "2026-09-29", true)
	_ = store.UpsertCTCert(ctx, "vulos-status.org", "ZeroSSL", "2026-07-10", "2026-10-08", true)
	_ = store.UpsertCTCert(ctx, "vu1os.org", "Unknown CA", "2026-07-13", "2026-10-11", false)

	// Give the timestamps a moment of spread so ordering looks natural.
	time.Sleep(2 * time.Millisecond)

	pages := NewPages(store)
	rr := httptest.NewRecorder()
	pages.SecurityDashboard(rr, httptest.NewRequest("GET", "/superadmin/security", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("security dashboard status %d\n%s", rr.Code, rr.Body.String())
	}
	dst := filepath.Join(outDir, "security-dashboard.html")
	if err := os.WriteFile(dst, rr.Body.Bytes(), 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
	t.Logf("dumped security dashboard to %s", dst)
}
