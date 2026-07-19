package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/middleware"
)

// okHandler writes 200 OK with a plain text body.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
})

// --- RequestID ---

func TestRequestID_GeneratesID(t *testing.T) {
	t.Parallel()
	h := middleware.RequestID(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)
	id := rec.Header().Get("X-Request-ID")
	if id == "" {
		t.Fatal("expected X-Request-ID to be set on response")
	}
}

func TestRequestID_EchoesExistingID(t *testing.T) {
	t.Parallel()
	h := middleware.RequestID(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "test-id-123")
	h.ServeHTTP(rec, req)
	got := rec.Header().Get("X-Request-ID")
	if got != "test-id-123" {
		t.Fatalf("expected X-Request-ID=test-id-123, got %q", got)
	}
}

func TestRequestID_PropagatedToContext(t *testing.T) {
	t.Parallel()
	var gotID string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = middleware.RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := middleware.RequestID(inner)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "ctx-check")
	h.ServeHTTP(rec, req)
	if gotID != "ctx-check" {
		t.Fatalf("expected context ID ctx-check, got %q", gotID)
	}
}

// --- PanicRecovery ---

func TestPanicRecovery_Returns500(t *testing.T) {
	t.Parallel()
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic — should be recovered")
	})
	// Wrap PanicRecovery inside RequestID so the recovery logger has a req_id.
	h := middleware.RequestID(middleware.PanicRecovery(panicHandler))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	// Must NOT panic the test process.
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("response body not valid JSON: %v", err)
	}
	if body["error"] == "" {
		t.Fatal("expected non-empty error field in panic response body")
	}
}

func TestPanicRecovery_NormalRequestPassesThrough(t *testing.T) {
	t.Parallel()
	h := middleware.PanicRecovery(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// --- AccessLog ---

// AccessLog is difficult to assert the log output without hooking the logger.
// We verify it does NOT change the status code for normal requests.
func TestAccessLog_PassesThrough(t *testing.T) {
	t.Parallel()
	h := middleware.RequestID(middleware.AccessLog(okHandler))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestAccessLog_PreservesErrorStatus(t *testing.T) {
	t.Parallel()
	errHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	h := middleware.RequestID(middleware.AccessLog(errHandler))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// --- CORS ---

func TestCORS_SetsHeaders(t *testing.T) {
	t.Parallel()
	h := middleware.CORS(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("expected Access-Control-Allow-Origin=*, got %q", got)
	}
}

func TestCORS_OptionsReturns204(t *testing.T) {
	t.Parallel()
	h := middleware.CORS(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/test", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for OPTIONS preflight, got %d", rec.Code)
	}
}

func TestCORS_AllowsMethodsHeader(t *testing.T) {
	t.Parallel()
	h := middleware.CORS(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	h.ServeHTTP(rec, req)
	methods := rec.Header().Get("Access-Control-Allow-Methods")
	if methods == "" {
		t.Fatal("Access-Control-Allow-Methods header missing")
	}
}

// --- RateLimit ---

func TestRateLimit_AllowsUnderLimit(t *testing.T) {
	t.Parallel()
	cfg := middleware.RateLimitConfig{RequestsPerSecond: 100, Burst: 10}
	h := middleware.RateLimit(cfg)(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	req.RemoteAddr = "127.0.0.1:9999"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for first request, got %d", rec.Code)
	}
}

func TestRateLimit_Returns429WhenExceeded(t *testing.T) {
	t.Parallel()
	// Very tight config: 0.001 req/s, burst 1. After one request, the next must be rejected.
	cfg := middleware.RateLimitConfig{RequestsPerSecond: 0.001, Burst: 1}
	h := middleware.RateLimit(cfg)(okHandler)

	makeReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		return req
	}

	// First request: should succeed (token bucket starts full).
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, makeReq())
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 on first request, got %d", rec1.Code)
	}

	// Subsequent requests: bucket is empty, should get 429.
	got429 := false
	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, makeReq())
		if rec.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("expected 429 Too Many Requests after exhausting burst")
	}
}

func TestRateLimit_DifferentIPsAreIsolated(t *testing.T) {
	t.Parallel()
	cfg := middleware.RateLimitConfig{RequestsPerSecond: 0.001, Burst: 1}
	h := middleware.RateLimit(cfg)(okHandler)

	makeReq := func(ip string) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = ip + ":5000"
		return req
	}

	// Exhaust IP A.
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, makeReq("192.168.1.1"))
	}
	// IP B should still be at full burst.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, makeReq("192.168.1.2"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected IP B to be unaffected, got %d", rec.Code)
	}
}

func TestRateLimit_RefillsOverTime(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping time-sensitive test in short mode")
	}
	t.Parallel()
	// 10 req/s, burst 1: after 200ms we should have ~2 tokens (capped at 1),
	// so the second request should succeed.
	cfg := middleware.RateLimitConfig{RequestsPerSecond: 10, Burst: 2}
	h := middleware.RateLimit(cfg)(okHandler)

	makeReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "172.16.0.1:1000"
		return req
	}

	// Drain the bucket completely.
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, makeReq())
	}

	// Wait for refill.
	time.Sleep(250 * time.Millisecond)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, makeReq())
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after refill, got %d", rec.Code)
	}
}

// --- Chain ---

func TestChain_OrderIsPreserved(t *testing.T) {
	t.Parallel()
	var order []int
	mw := func(n int) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, n)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := middleware.Chain(okHandler, mw(1), mw(2), mw(3))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	h.ServeHTTP(rec, req)
	if len(order) != 3 || order[0] != 1 || order[1] != 2 || order[2] != 3 {
		t.Fatalf("expected order [1 2 3], got %v", order)
	}
}

// --- CSRFOriginCheck ---

// TestCSRFOriginCheck_DevEmptyOrigins verifies that in non-prod (dev/local),
// an empty origins list disables the check and passes requests through.
func TestCSRFOriginCheck_DevEmptyOrigins(t *testing.T) {
	t.Parallel()
	// Ensure we are NOT in prod.
	os.Setenv("VULOS_ENV", "local")
	h := middleware.CSRFOriginCheck(nil)(okHandler)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/test", nil)
	h.ServeHTTP(rec, req)
	// In dev with no origins the middleware is a pass-through.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (pass-through in dev with empty origins), got %d", rec.Code)
	}
}

// TestCSRFOriginCheck_ProdEmptyOriginsFatal verifies that in VULOS_ENV=prod,
// constructing CSRFOriginCheck with an empty origins list exits the process.
// We verify this by running the test binary as a subprocess.
func TestCSRFOriginCheck_ProdEmptyOriginsFatal(t *testing.T) {
	if os.Getenv("CSRF_FATAL_SUBPROC") == "1" {
		// Running as the subprocess: call the function under test.
		// log.Fatalf will call os.Exit(1), so we never return normally.
		middleware.CSRFOriginCheck(nil)
		return
	}

	// Parent: re-run this test as a subprocess with the sentinel env var set.
	cmd := exec.Command(os.Args[0], "-test.run=TestCSRFOriginCheck_ProdEmptyOriginsFatal", "-test.v")
	cmd.Env = append(os.Environ(), "VULOS_ENV=prod", "CSRF_FATAL_SUBPROC=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected subprocess to exit non-zero (log.Fatalf), but it succeeded; output:\n%s", out)
	}
	// The subprocess must have exited non-zero.
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() == 0 {
			t.Fatalf("subprocess exited 0, expected non-zero; output:\n%s", out)
		}
		// Non-zero exit — fatal was triggered as expected.
		return
	}
	// Some other error (e.g. binary not found) — surface it.
	t.Fatalf("subprocess error: %v; output:\n%s", err, out)
}

// TestCSRFOriginCheck_AllowsMatchingOrigin verifies that a POST with a
// matching Origin header passes.
func TestCSRFOriginCheck_AllowsMatchingOrigin(t *testing.T) {
	t.Parallel()
	os.Setenv("VULOS_ENV", "local")
	origins := []string{"https://app.vulos.org"}
	h := middleware.CSRFOriginCheck(origins)(okHandler)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/test", nil)
	req.Header.Set("Origin", "https://app.vulos.org")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for allowed origin, got %d", rec.Code)
	}
}

// TestCSRFOriginCheck_RejectsUnknownOrigin verifies that a POST with an
// unknown Origin is rejected with 403.
func TestCSRFOriginCheck_RejectsUnknownOrigin(t *testing.T) {
	t.Parallel()
	os.Setenv("VULOS_ENV", "local")
	origins := []string{"https://app.vulos.org"}
	h := middleware.CSRFOriginCheck(origins)(okHandler)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/test", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unknown origin, got %d", rec.Code)
	}
}

// TestCSRFOriginCheck_GetPassesWithoutOrigin verifies that GET requests are
// always allowed regardless of Origin/Referer.
func TestCSRFOriginCheck_GetPassesWithoutOrigin(t *testing.T) {
	t.Parallel()
	os.Setenv("VULOS_ENV", "local")
	origins := []string{"https://app.vulos.org"}
	h := middleware.CSRFOriginCheck(origins)(okHandler)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for GET without origin, got %d", rec.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// IsAllowedOrigin helper tests (SSO_COOKIE_WIRE)
// ─────────────────────────────────────────────────────────────────────────────

// TestIsAllowedOrigin_ExplicitAllow verifies an origin in the explicit allowlist
// is accepted regardless of parent domain.
func TestIsAllowedOrigin_ExplicitAllow(t *testing.T) {
	t.Parallel()
	os.Setenv("VULOS_ENV", "local")
	allowlist := []string{"https://app.vulos.org"}
	if !middleware.IsAllowedOrigin("https://app.vulos.org", allowlist, "") {
		t.Error("expected explicit allowlist origin to be accepted")
	}
}

// TestIsAllowedOrigin_SubdomainOfParent verifies that a subdomain of the parent
// domain is accepted when parentDomain is set (and scheme is https in non-prod).
func TestIsAllowedOrigin_SubdomainOfParent(t *testing.T) {
	os.Setenv("VULOS_ENV", "local") // non-prod: http allowed too
	allowlist := []string{"https://app.vulos.org"}
	if !middleware.IsAllowedOrigin("https://mail.vulos.org", allowlist, "vulos.org") {
		t.Error("expected subdomain of parent domain to be accepted")
	}
	if !middleware.IsAllowedOrigin("https://board.vulos.org", allowlist, "vulos.org") {
		t.Error("expected another subdomain of parent domain to be accepted")
	}
}

// TestIsAllowedOrigin_LeadingDotParentDomain pins that a leading-dot cookie
// domain (".vulos.org" — documented-valid in auth/session.go) works the same as
// the bare apex. Without normalisation it silently rejected every subdomain
// origin, CSRF-blocking the whole app on a legitimate config.
func TestIsAllowedOrigin_LeadingDotParentDomain(t *testing.T) {
	os.Setenv("VULOS_ENV", "local")
	allowlist := []string{"https://app.vulos.org"}
	if !middleware.IsAllowedOrigin("https://mail.vulos.org", allowlist, ".vulos.org") {
		t.Error("a subdomain must be accepted when parentDomain has a leading dot")
	}
	if !middleware.IsAllowedOrigin("https://vulos.org", allowlist, ".vulos.org") {
		t.Error("the apex must be accepted when parentDomain has a leading dot")
	}
	// The suffix-confusion guard must still hold with the leading-dot form.
	if middleware.IsAllowedOrigin("https://notvulos.org", allowlist, ".vulos.org") {
		t.Error("a mere suffix match must still be rejected with a leading-dot parentDomain")
	}
	if middleware.IsAllowedOrigin("https://vulos.org.evil.com", allowlist, ".vulos.org") {
		t.Error("a look-alike domain must still be rejected with a leading-dot parentDomain")
	}
}

// TestIsAllowedOrigin_NonSubdomainRejected verifies that an origin that is NOT
// a subdomain of parentDomain (and not in the allowlist) is rejected.
func TestIsAllowedOrigin_NonSubdomainRejected(t *testing.T) {
	t.Parallel()
	os.Setenv("VULOS_ENV", "local")
	allowlist := []string{"https://app.vulos.org"}
	if middleware.IsAllowedOrigin("https://evil.example.com", allowlist, "vulos.org") {
		t.Error("expected non-subdomain origin to be rejected")
	}
	// Also reject same TLD suffix that is not a proper subdomain.
	if middleware.IsAllowedOrigin("https://notvulos.org", allowlist, "vulos.org") {
		t.Error("expected origin that merely ends with the parent domain string to be rejected")
	}
}

// TestIsAllowedOrigin_WrongSchemeRejectedInProd verifies that an http:// origin
// is rejected when VULOS_ENV=prod even if it is a valid subdomain.
func TestIsAllowedOrigin_WrongSchemeRejectedInProd(t *testing.T) {
	orig := os.Getenv("VULOS_ENV")
	os.Setenv("VULOS_ENV", "prod")
	t.Cleanup(func() {
		if orig == "" {
			os.Setenv("VULOS_ENV", "local")
		} else {
			os.Setenv("VULOS_ENV", orig)
		}
	})
	allowlist := []string{"https://app.vulos.org"}
	if middleware.IsAllowedOrigin("http://mail.vulos.org", allowlist, "vulos.org") {
		t.Error("expected http:// origin to be rejected in prod")
	}
	// https should still pass.
	if !middleware.IsAllowedOrigin("https://mail.vulos.org", allowlist, "vulos.org") {
		t.Error("expected https:// subdomain to be accepted in prod")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// UNIFIED-SIGNIN: CSRF exemption for cookie-less RFC 8628 device endpoints
// ─────────────────────────────────────────────────────────────────────────────

// A Vulos box's enrollment client sends neither Origin nor Referer; the
// device_code IS the credential, so CSRF does not apply to /enroll/start and
// /enroll/poll. Everything else keeps the header requirement, and the
// exemption must not be reachable via path traversal.
func TestCSRFOriginCheck_EnrollDeviceEndpointsExempt(t *testing.T) {
	t.Parallel()
	os.Setenv("VULOS_ENV", "local")
	origins := []string{"https://app.vulos.org"}
	h := middleware.CSRFOriginCheck(origins)(okHandler)

	for _, p := range []string{"/enroll/start", "/enroll/poll"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, p, nil) // no Origin, no Referer
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200 (S2S exemption), got %d", p, rec.Code)
		}
	}

	// The owner-approval endpoint is cookie-authed and stays guarded.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/enroll/approve", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("/enroll/approve: expected 403 without Origin, got %d", rec.Code)
	}

	// Traversal cannot ride the exemption.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/enroll/start/../approve", nil)
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("traversal: expected 403, got %d", rec2.Code)
	}
}

// TestCSRFOriginCheck_BillingWebhookExempt pins a bug this middleware ACTUALLY
// had: Paystack is a server-to-server caller that sends neither Origin nor
// Referer, so the CSRF gate 403'd every webhook before billing.WebhookHandler
// ever ran — in any environment with CORSOrigins set (i.e. production). No
// charge.success would ever have granted a tier; no charge.failed would ever have
// opened dunning.
//
// CSRF cannot apply here: there is no ambient browser credential to ride on. The
// credential IS the HMAC-SHA512 signature over the raw body, which the handler
// verifies fail-closed. So the endpoint is exempt from the header requirement —
// and its neighbours are NOT, which is what this test holds down.
func TestCSRFOriginCheck_BillingWebhookExempt(t *testing.T) {
	t.Parallel()
	os.Setenv("VULOS_ENV", "local")
	h := middleware.CSRFOriginCheck([]string{"https://app.vulos.org"})(okHandler)

	// The webhook: no Origin, no Referer → must reach the handler.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/billing/webhook", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/billing/webhook: expected 200 (S2S exemption — Paystack sends no Origin/Referer), got %d", rec.Code)
	}

	// Sibling billing endpoints are session-cookie-authed and keep full CSRF cover.
	for _, p := range []string{"/api/billing/subscribe", "/api/billing/card", "/api/billing"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, p, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: expected 403 without Origin (cookie-authed, must stay guarded), got %d", p, rec.Code)
		}
	}

	// The exemption is not reachable by traversal.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/billing/webhook/../subscribe", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("traversal onto the webhook exemption: expected 403, got %d", rec.Code)
	}
}
