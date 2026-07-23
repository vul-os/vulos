package abuse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ─── test helpers ────────────────────────────────────────────────────────────

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	st, err := OpenStore(db)
	if err != nil {
		db.Close()
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// recordingAudit records every audit call so tests can assert on them.
type recordingAudit struct {
	mu      sync.Mutex
	records []recordedCall
}

type recordedCall struct {
	Actor, Action, Target string
	Meta                  map[string]string
}

func (r *recordingAudit) Record(_ context.Context, actor, action, target string, meta map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, recordedCall{actor, action, target, meta})
	return nil
}

func (r *recordingAudit) count(action string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.records {
		if c.Action == action {
			n++
		}
	}
	return n
}

// ─── detector: per-account + per-IP thresholds ───────────────────────────────

func TestDetector_PerIPSignupBurstTrips(t *testing.T) {
	st := newTestStore(t)
	cfg := Config{Window: time.Minute, SignupBurstPerIP: 5}
	d := NewDetector(cfg, st, nil, nil, nil, nil)

	ctx := context.Background()
	var lastWarn, lastStrong *Signal
	for i := 0; i < 5; i++ {
		s := d.ObserveSignup(ctx, "", "1.1.1.1")
		if s != nil && s.IsWarn(d.cfg) {
			lastWarn = s
		}
		if s != nil && s.IsStrong(d.cfg) {
			lastStrong = s
		}
	}
	if lastWarn == nil {
		t.Fatalf("expected a warn signal before strong threshold")
	}
	if lastStrong == nil {
		t.Fatalf("expected a strong signal at SignupBurstPerIP=5")
	}
	if lastStrong.Kind != KindSignup {
		t.Errorf("kind = %q want signup", lastStrong.Kind)
	}
	if lastStrong.IP != "1.1.1.1" {
		t.Errorf("ip = %q", lastStrong.IP)
	}

	// Different IP must not trip immediately.
	if s := d.ObserveSignup(ctx, "", "9.9.9.9"); s != nil && s.IsStrong(d.cfg) {
		t.Errorf("fresh IP should not be strong on first event, got %+v", s)
	}
}

func TestDetector_PerAccountSendBurstTrips(t *testing.T) {
	st := newTestStore(t)
	cfg := Config{Window: time.Minute, SendBurstPerAccount: 3, SendBurstPerIP: 1000}
	d := NewDetector(cfg, st, nil, nil, nil, nil)

	ctx := context.Background()
	var strong *Signal
	for i := 0; i < 3; i++ {
		s := d.ObserveSend(ctx, "acct-a", "10.0.0.1", 1)
		if s != nil && s.IsStrong(d.cfg) {
			strong = s
		}
	}
	if strong == nil {
		t.Fatalf("expected strong on 3rd send")
	}
	if strong.AccountID != "acct-a" {
		t.Errorf("account_id = %q", strong.AccountID)
	}

	// Other account isolated.
	if s := d.ObserveSend(ctx, "acct-b", "10.0.0.1", 1); s != nil && s.IsStrong(d.cfg) {
		t.Errorf("acct-b should not be strong yet, got %+v", s)
	}
}

func TestDetector_FanoutEmitsOnSingleSend(t *testing.T) {
	st := newTestStore(t)
	cfg := Config{
		Window:                 time.Minute,
		SendBurstPerAccount:    100000,
		SendBurstPerIP:         100000,
		FanoutRecipientsStrong: 50,
		FanoutRecipientsWarn:   10,
	}
	d := NewDetector(cfg, st, nil, nil, nil, nil)

	if s := d.ObserveSend(context.Background(), "acct-fan", "1.2.3.4", 75); s == nil || !s.IsStrong(d.cfg) || s.Kind != KindFanout {
		t.Fatalf("expected strong fanout signal, got %+v", s)
	}
	if s := d.ObserveSend(context.Background(), "acct-fan2", "1.2.3.5", 25); s == nil || s.Kind != KindFanout || s.IsStrong(d.cfg) {
		t.Fatalf("expected warn fanout signal, got %+v", s)
	}
}

func TestDetector_LoginBurstPerIP(t *testing.T) {
	st := newTestStore(t)
	cfg := Config{Window: time.Minute, LoginBurstPerIP: 4}
	d := NewDetector(cfg, st, nil, nil, nil, nil)

	var strong *Signal
	for i := 0; i < 4; i++ {
		s := d.ObserveLogin(context.Background(), "", "8.8.8.8")
		if s != nil && s.IsStrong(d.cfg) {
			strong = s
		}
	}
	if strong == nil {
		t.Fatalf("expected strong login burst signal")
	}
}

// ─── auto-suspend on strong signal ───────────────────────────────────────────

func TestDetector_AutoSuspendOnStrong(t *testing.T) {
	st := newTestStore(t)
	au := &recordingAudit{}
	cfg := Config{Window: time.Minute, SendBurstPerAccount: 2, SendBurstPerIP: 1000}
	d := NewDetector(cfg, st, nil, nil, au, nil)

	ctx := context.Background()
	_ = d.ObserveSend(ctx, "spammer", "5.5.5.5", 1)
	sig := d.ObserveSend(ctx, "spammer", "5.5.5.5", 1)
	if sig == nil || !sig.IsStrong(d.cfg) {
		t.Fatalf("expected strong signal, got %+v", sig)
	}

	susp, err := st.IsSuspended(ctx, "spammer")
	if err != nil || !susp {
		t.Fatalf("expected suspended, got susp=%v err=%v", susp, err)
	}
	if au.count("abuse.auto_suspend") != 1 {
		t.Errorf("expected 1 auto-suspend audit entry, got %d", au.count("abuse.auto_suspend"))
	}

	// Repeated strong signals must not re-suspend (idempotent).
	_ = d.ObserveSend(ctx, "spammer", "5.5.5.5", 1)
	if au.count("abuse.auto_suspend") != 1 {
		t.Errorf("expected suspend to be idempotent, got %d audit entries", au.count("abuse.auto_suspend"))
	}
}

func TestStore_ReinstateRequiresHuman(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	changed, err := st.Suspend(ctx, "acct-x", "test", 1.0, true)
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if !changed {
		t.Errorf("expected changed=true on first suspend")
	}
	if changed, _ := st.Suspend(ctx, "acct-x", "again", 1.0, true); changed {
		t.Errorf("expected changed=false on idempotent re-suspend")
	}
	if err := st.Reinstate(ctx, "acct-x", ""); err == nil {
		t.Errorf("expected error on reinstate without actor")
	}
	if err := st.Reinstate(ctx, "acct-x", "alice@vulos.org"); err != nil {
		t.Fatalf("reinstate: %v", err)
	}
	susp, _ := st.IsSuspended(ctx, "acct-x")
	if susp {
		t.Errorf("expected not suspended after reinstate")
	}
	rec, err := st.GetSuspension(ctx, "acct-x")
	if err != nil {
		t.Fatalf("get suspension: %v", err)
	}
	if !strings.HasPrefix(rec.ReinstatedBy, "human:") {
		t.Errorf("ReinstatedBy = %q, want human:* prefix", rec.ReinstatedBy)
	}
	if errors.Is(st.Reinstate(ctx, "acct-x", "alice"), nil) {
		t.Errorf("second reinstate should fail (no active suspension)")
	}
}

func TestDetector_ReinstateByHumanAuditTrail(t *testing.T) {
	st := newTestStore(t)
	au := &recordingAudit{}
	d := NewDetector(Config{}, st, nil, nil, au, nil)

	ctx := context.Background()
	if _, err := st.Suspend(ctx, "acct-r", "auto", 1.0, true); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if err := d.ReinstateByHuman(ctx, "acct-r", "ts-lead"); err != nil {
		t.Fatalf("reinstate: %v", err)
	}
	if au.count("abuse.reinstate") != 1 {
		t.Errorf("expected reinstate audit entry, got %d", au.count("abuse.reinstate"))
	}
}

// ─── report intake + list + status ───────────────────────────────────────────

func TestStore_FileAndListReports(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	r1, err := st.FileReport(ctx, "alice@vulos.org", "acct-evil", CategoryPhishing, "fake login page", map[string]string{"url": "https://bad.example"})
	if err != nil {
		t.Fatalf("file report: %v", err)
	}
	if r1.ID == "" || r1.Status != StatusOpen {
		t.Errorf("bad report: %+v", r1)
	}

	_, err = st.FileReport(ctx, "bob@vulos.org", "acct-evil", CategorySpam, "", nil)
	if err != nil {
		t.Fatalf("file report 2: %v", err)
	}

	open, err := st.ListReports(ctx, StatusOpen, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("expected 2 open reports, got %d", len(open))
	}

	if err := st.SetReportStatus(ctx, r1.ID, StatusInvestigating, "ts-lead"); err != nil {
		t.Fatalf("set status: %v", err)
	}
	stillOpen, _ := st.ListReports(ctx, StatusOpen, 0)
	if len(stillOpen) != 1 {
		t.Errorf("expected 1 open after status change, got %d", len(stillOpen))
	}

	if err := st.SetReportStatus(ctx, "deadbeef", StatusActioned, "ts-lead"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	if err := st.SetReportStatus(ctx, r1.ID, "bogus", "ts-lead"); err == nil {
		t.Errorf("expected error on bad status")
	}
}

func TestStore_FileReportValidatesCategory(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.FileReport(context.Background(), "alice", "acct-x", "not-a-category", "", nil); err == nil {
		t.Errorf("expected error on invalid category")
	}
}

// ─── CSAM hook: pluggable + default no-op ────────────────────────────────────

type alwaysMatcher struct{ calls atomic.Int32 }

func (m *alwaysMatcher) Match(_ context.Context, _ []byte) (bool, error) {
	m.calls.Add(1)
	return true, nil
}

func TestCSAM_DefaultStubMatchesNothing(t *testing.T) {
	st := newTestStore(t)
	d := NewDetector(Config{}, st, nil, nil, nil, nil) // nil matcher → NoopCSAMMatcher
	sig, err := d.ObserveCSAM(context.Background(), "acct-z", "1.2.3.4", []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("ObserveCSAM err: %v", err)
	}
	if sig != nil {
		t.Errorf("default NoopCSAMMatcher must never match; got %+v", sig)
	}
	susp, _ := st.IsSuspended(context.Background(), "acct-z")
	if susp {
		t.Errorf("default matcher must not cause suspend")
	}
}

func TestCSAM_PluggedInMatcherSuspendsAndFilesReport(t *testing.T) {
	st := newTestStore(t)
	au := &recordingAudit{}
	matcher := &alwaysMatcher{}
	d := NewDetector(Config{}, st, nil, matcher, au, nil)

	ctx := context.Background()
	sig, err := d.ObserveCSAM(ctx, "acct-csam", "1.1.1.1", []byte("hash"))
	if err != nil {
		t.Fatalf("ObserveCSAM: %v", err)
	}
	if sig == nil || sig.Kind != KindCSAM || !sig.IsStrong(d.cfg) {
		t.Fatalf("expected strong CSAM signal, got %+v", sig)
	}
	if matcher.calls.Load() != 1 {
		t.Errorf("matcher should have been called exactly once, got %d", matcher.calls.Load())
	}
	susp, _ := st.IsSuspended(ctx, "acct-csam")
	if !susp {
		t.Errorf("CSAM match must auto-suspend")
	}
	reps, _ := st.ListReports(ctx, "", 0)
	hasCSAM := false
	for _, r := range reps {
		if r.Category == CategoryCSAM && r.SubjectID == "acct-csam" {
			hasCSAM = true
		}
	}
	if !hasCSAM {
		t.Errorf("CSAM match must queue an abuse report for human/legal review")
	}
	if au.count("abuse.auto_suspend") != 1 {
		t.Errorf("expected auto-suspend audit entry")
	}
}

// TestCSAM_FileReportFailure_SurfacedAndAudited is the WAVE34-5 regression: a
// CSAM report is a mandatory-reporting obligation, so a failed FileReport must
// NOT be swallowed — ObserveCSAM must return the error AND write a durable
// audit-log fallback so the obligation is still recorded.
func TestCSAM_FileReportFailure_SurfacedAndAudited(t *testing.T) {
	st := newTestStore(t)
	au := &recordingAudit{}
	d := NewDetector(Config{}, st, nil, &alwaysMatcher{}, au, nil)

	// Force FileReport to fail by removing its target table.
	if _, err := st.db.Exec(`DROP TABLE abuse_reports`); err != nil {
		t.Fatalf("drop abuse_reports: %v", err)
	}

	sig, err := d.ObserveCSAM(context.Background(), "acct-csam", "1.1.1.1", []byte("hash"))
	if err == nil {
		t.Fatal("ObserveCSAM must return an error when the mandatory CSAM report fails")
	}
	if !strings.Contains(err.Error(), "mandatory-reporting gap") {
		t.Errorf("error should flag the mandatory-reporting gap, got %v", err)
	}
	// The suspend still happened and the signal is still returned.
	if sig == nil || sig.Kind != KindCSAM {
		t.Fatalf("expected the strong CSAM signal alongside the error, got %+v", sig)
	}
	// Durable fallback: the failure is recorded in the tamper-evident audit log.
	if au.count("abuse.csam_report_failed") != 1 {
		t.Errorf("expected a csam_report_failed audit fallback entry, audits=%+v", au.records)
	}
}

// ─── ContentClassifier: pluggable + default no-op ────────────────────────────

type scoringClassifier struct{ score float64 }

func (s scoringClassifier) Classify(context.Context, []byte) (float64, error) { return s.score, nil }

func TestContentClassifier_DefaultNoop(t *testing.T) {
	st := newTestStore(t)
	d := NewDetector(Config{}, st, nil, nil, nil, nil) // nil → NoopClassifier
	sig, err := d.ObserveContent(context.Background(), "acct", "1.1.1.1", []byte("hello"))
	if err != nil || sig != nil {
		t.Errorf("default classifier must not flag; sig=%+v err=%v", sig, err)
	}
}

func TestContentClassifier_Pluggable(t *testing.T) {
	st := newTestStore(t)
	d := NewDetector(Config{}, st, scoringClassifier{score: 1.1}, nil, nil, nil)
	sig, err := d.ObserveContent(context.Background(), "acct-c", "1.1.1.1", []byte("phishbody"))
	if err != nil {
		t.Fatalf("ObserveContent: %v", err)
	}
	if sig == nil || !sig.IsStrong(d.cfg) {
		t.Fatalf("expected strong content signal, got %+v", sig)
	}
	susp, _ := st.IsSuspended(context.Background(), "acct-c")
	if !susp {
		t.Errorf("strong content signal must suspend")
	}
}

// ─── HTTP routes ─────────────────────────────────────────────────────────────

type fakeAuth struct {
	user    string
	isAdmin bool
}

func (f fakeAuth) Authenticate(w http.ResponseWriter, r *http.Request) (string, bool) {
	if f.user == "" {
		http.Error(w, `{"error":"not authenticated"}`, http.StatusUnauthorized)
		return "", false
	}
	return f.user, true
}

func (f fakeAuth) IsAdmin(_ context.Context, _ string) bool { return f.isAdmin }

func TestRoutes_ReportIntakeAndAdminList(t *testing.T) {
	st := newTestStore(t)
	d := NewDetector(Config{}, st, nil, nil, nil, nil)

	// Non-admin user can file a report.
	user := fakeAuth{user: "alice@vulos.org", isAdmin: false}
	mux := http.NewServeMux()
	NewHandler(d, st, user, "").Register(mux)

	body := bytes.NewBufferString(`{"subject_id":"acct-phish","category":"phishing","description":"fake login","evidence":{"url":"https://bad"}}`)
	req := httptest.NewRequest("POST", "/api/abuse/report", body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("report: code=%d body=%s", w.Code, w.Body.String())
	}
	var rep Report
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if rep.ID == "" || rep.Status != StatusOpen {
		t.Fatalf("bad report response: %+v", rep)
	}

	// Non-admin must NOT be able to list.
	req = httptest.NewRequest("GET", "/api/abuse/reports?status=open", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("non-admin list: code=%d want 403", w.Code)
	}

	// Admin can drain.
	adminMux := http.NewServeMux()
	NewHandler(d, st, fakeAuth{user: "ts@vulos.org", isAdmin: true}, "").Register(adminMux)
	req = httptest.NewRequest("GET", "/api/abuse/reports?status=open", nil)
	w = httptest.NewRecorder()
	adminMux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("admin list: code=%d body=%s", w.Code, w.Body.String())
	}
	var listResp struct {
		Reports []Report `json:"reports"`
		Count   int      `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if listResp.Count != 1 || listResp.Reports[0].ID != rep.ID {
		t.Fatalf("unexpected list: %+v", listResp)
	}

	// Admin status update.
	req = httptest.NewRequest("POST", "/api/abuse/reports/"+rep.ID+"/status",
		bytes.NewBufferString(`{"status":"actioned"}`))
	w = httptest.NewRecorder()
	adminMux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status update: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestRoutes_AdminReinstate(t *testing.T) {
	st := newTestStore(t)
	au := &recordingAudit{}
	d := NewDetector(Config{}, st, nil, nil, au, nil)
	ctx := context.Background()
	if _, err := st.Suspend(ctx, "acct-r", "auto", 1.0, true); err != nil {
		t.Fatalf("seed suspend: %v", err)
	}

	mux := http.NewServeMux()
	NewHandler(d, st, fakeAuth{user: "ts@vulos.org", isAdmin: true}, "").Register(mux)

	req := httptest.NewRequest("POST", "/api/abuse/suspensions/acct-r/reinstate", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("reinstate: code=%d body=%s", w.Code, w.Body.String())
	}
	susp, _ := st.IsSuspended(ctx, "acct-r")
	if susp {
		t.Errorf("expected not suspended after admin reinstate")
	}
}

// TestRoutes_ErrorScrubbing — admin/intake error bodies must NEVER contain
// the raw err.Error() text (which could carry SQL / PRAGMA / schema detail
// to a compromised admin session). The generic message must appear instead.
// Covers FIX-ADMIN-ERR-LEAK-01.
func TestRoutes_ErrorScrubbing(t *testing.T) {
	st := newTestStore(t)
	d := NewDetector(Config{}, st, nil, nil, nil, nil)

	user := fakeAuth{user: "alice@vulos.org", isAdmin: true}
	mux := http.NewServeMux()
	NewHandler(d, st, user, "").Register(mux)

	// 1. Malformed JSON to /report → invalid_json with generic message.
	req := httptest.NewRequest("POST", "/api/abuse/report",
		bytes.NewBufferString("not-valid-json"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON: code=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"message":"internal error"`) {
		t.Errorf("expected generic message body, got %s", body)
	}
	// The raw json decoder error tends to contain "invalid character" — must
	// NOT have leaked through.
	if strings.Contains(body, "invalid character") {
		t.Errorf("raw decoder err leaked into body: %s", body)
	}

	// 2. Bad category to /report → store-side validation error returned with
	//    the generic message (the leak risk is the underlying validation
	//    string carrying internal field names).
	req = httptest.NewRequest("POST", "/api/abuse/report",
		bytes.NewBufferString(`{"subject_id":"acct-x","category":"not-a-real-category"}`))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad category: code=%d body=%s", w.Code, w.Body.String())
	}
	body = w.Body.String()
	if !strings.Contains(body, `"message":"internal error"`) {
		t.Errorf("expected generic message body, got %s", body)
	}
	// The store returns a helpful string mentioning the category name; assert
	// it is NOT echoed to the wire.
	if strings.Contains(body, "not-a-real-category") {
		t.Errorf("raw store err leaked into body: %s", body)
	}

	// 3. Malformed JSON to /status → same scrub.
	req = httptest.NewRequest("POST", "/api/abuse/reports/abc/status",
		bytes.NewBufferString("garbage"))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status malformed JSON: code=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"message":"internal error"`) {
		t.Errorf("expected generic message, got %s", w.Body.String())
	}
}

func TestRoutes_SharedSecretAllowsInternalReport(t *testing.T) {
	st := newTestStore(t)
	d := NewDetector(Config{}, st, nil, nil, nil, nil)
	mux := http.NewServeMux()
	NewHandler(d, st, nil, "supers3cret").Register(mux)

	body := bytes.NewBufferString(`{"subject_id":"acct-bot","category":"spam"}`)
	req := httptest.NewRequest("POST", "/api/abuse/report", body)
	req.Header.Set("X-CP-Auth", "supers3cret")
	req.Header.Set("X-CP-Actor", "device:abc")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("shared-secret report: code=%d body=%s", w.Code, w.Body.String())
	}

	// Wrong secret rejected.
	body = bytes.NewBufferString(`{"subject_id":"acct-bot","category":"spam"}`)
	req = httptest.NewRequest("POST", "/api/abuse/report", body)
	req.Header.Set("X-CP-Auth", "wrong")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong shared secret: code=%d want 401", w.Code)
	}
}
