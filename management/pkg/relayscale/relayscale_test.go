package relayscale

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── Policy ────────────────────────────────────────────────────────────────────

func TestPolicyPlan(t *testing.T) {
	p := Policy{ScaleUpAt: 0.75, ScaleDownAt: 0.25, MinPerRegion: 1, MaxPerRegion: 4, Step: 1}
	plans := p.Plan([]RegionLoad{
		{Region: "eu", Instances: 2, Saturation: 0.90}, // hot → up
		{Region: "af", Instances: 3, Saturation: 0.10}, // cold → down
		{Region: "us", Instances: 2, Saturation: 0.50}, // nominal → hold
		{Region: "ap", Instances: 0, Saturation: 0.00}, // cold start → floor
	})
	want := map[string]int{"eu": 3, "af": 2, "us": 2, "ap": 1}
	for _, pl := range plans {
		if want[pl.Region] != pl.Desired {
			t.Errorf("region %s desired=%d want %d (%s)", pl.Region, pl.Desired, want[pl.Region], pl.Reason)
		}
	}
	if plans[0].Region != "af" { // sorted
		t.Errorf("plans not sorted by region: %+v", plans)
	}
}

func TestPolicyClampsToBounds(t *testing.T) {
	p := Policy{MinPerRegion: 2, MaxPerRegion: 3, Step: 5}
	plans := p.Plan([]RegionLoad{
		{Region: "eu", Instances: 3, Saturation: 0.99}, // up but capped at max=3
		{Region: "af", Instances: 2, Saturation: 0.01}, // down but floored at min=2
	})
	got := map[string]int{}
	for _, pl := range plans {
		got[pl.Region] = pl.Desired
	}
	if got["eu"] != 3 {
		t.Errorf("eu desired=%d want 3 (max clamp)", got["eu"])
	}
	if got["af"] != 2 {
		t.Errorf("af desired=%d want 2 (min floor)", got["af"])
	}
}

func TestPolicyDemandHintFloors(t *testing.T) {
	p := Policy{MinPerRegion: 1, Step: 1}
	plans := p.Plan([]RegionLoad{{Region: "eu", Instances: 1, Saturation: 0.05, Demand: 3}})
	if plans[0].Desired != 3 {
		t.Fatalf("desired=%d want 3 (demand hint floors above scale-down)", plans[0].Desired)
	}
}

// ── Manual / External seams ───────────────────────────────────────────────────

func TestManualProvisionerNeverProvisions(t *testing.T) {
	m := NewManualProvisioner()
	if m.Enabled() {
		t.Fatal("manual Enabled()=true, want false")
	}
	if _, err := m.Provision(context.Background(), "eu", RelaySpec{}); !errors.Is(err, ErrManualMode) {
		t.Fatalf("Provision err=%v want ErrManualMode", err)
	}
	if err := m.Destroy(context.Background(), Instance{}); !errors.Is(err, ErrManualMode) {
		t.Fatalf("Destroy err=%v want ErrManualMode", err)
	}
	if l, _ := m.List(context.Background()); len(l) != 0 {
		t.Fatalf("List len=%d want 0", len(l))
	}
}

func TestExternalProvisionerSignalsExternalMode(t *testing.T) {
	e := NewExternalProvisioner()
	if e.Enabled() {
		t.Fatal("external Enabled()=true, want false")
	}
	if _, err := e.Provision(context.Background(), "eu", RelaySpec{}); !errors.Is(err, ErrExternalMode) {
		t.Fatalf("Provision err=%v want ErrExternalMode", err)
	}
}

// ── Actuator (with a fake enabled provisioner) ────────────────────────────────

// fakeProv is an in-memory RelayProvisioner for actuator tests.
type fakeProv struct {
	mu    sync.Mutex
	insts []Instance
	seq   int
	fail  bool
}

func (f *fakeProv) Name() string  { return "fake" }
func (f *fakeProv) Enabled() bool { return true }
func (f *fakeProv) Provision(_ context.Context, region string, _ RelaySpec) (Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return Instance{}, errors.New("boom")
	}
	f.seq++
	in := Instance{ID: region + "-" + itoa(f.seq), Region: region, Provider: "fake", Ready: true, CreatedAt: time.Now()}
	f.insts = append(f.insts, in)
	return in, nil
}
func (f *fakeProv) Destroy(_ context.Context, inst Instance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.insts[:0]
	for _, i := range f.insts {
		if i.ID != inst.ID {
			out = append(out, i)
		}
	}
	f.insts = out
	return nil
}
func (f *fakeProv) List(context.Context) ([]Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Instance(nil), f.insts...), nil
}

func itoa(n int) string { return string(rune('0' + n)) }

func TestActuatorReconcileProvisionsAndDestroys(t *testing.T) {
	fp := &fakeProv{}
	// seed 1 existing eu instance
	fp.insts = []Instance{{ID: "eu-seed", Region: "eu", Ready: true}}
	act := NewActuator(fp, Policy{MinPerRegion: 1, MaxPerRegion: 5, Step: 1, ScaleUpAt: 0.75, ScaleDownAt: 0.25}, nil, nil)

	// eu hot → provision one (1→2). af has 2 but cold → destroy one (2→1).
	fp.insts = append(fp.insts, Instance{ID: "af-a", Region: "af", Ready: true}, Instance{ID: "af-b", Region: "af", Ready: true})
	res, err := act.Reconcile(context.Background(), []RegionLoad{
		{Region: "eu", Instances: 1, Saturation: 0.9},
		{Region: "af", Instances: 2, Saturation: 0.1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Actuated {
		t.Fatal("Actuated=false, want true for enabled provisioner")
	}
	if len(res.Provisioned) != 1 || res.Provisioned[0].Region != "eu" {
		t.Fatalf("provisioned=%+v want 1 eu", res.Provisioned)
	}
	if len(res.Destroyed) != 1 || res.Destroyed[0].Region != "af" {
		t.Fatalf("destroyed=%+v want 1 af", res.Destroyed)
	}
}

func TestActuatorManualModeActuatesNothing(t *testing.T) {
	act := NewActuator(NewManualProvisioner(), Policy{}, nil, nil)
	res, err := act.Reconcile(context.Background(), []RegionLoad{{Region: "eu", Instances: 2, Saturation: 0.99}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Actuated {
		t.Fatal("manual mode Actuated=true, want false")
	}
	if len(res.Plans) != 1 || res.Plans[0].Desired <= res.Plans[0].Current {
		t.Fatalf("plans should still show desired scale-up: %+v", res.Plans)
	}
	if len(res.Provisioned) != 0 {
		t.Fatalf("manual mode provisioned %d, want 0", len(res.Provisioned))
	}
}

// ── Registry ──────────────────────────────────────────────────────────────────

func TestProvisionerFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	// default → manual
	p, err := provisionerFromEnv(env(nil))
	if err != nil || p.Name() != "manual" {
		t.Fatalf("default = %v, %v; want manual", p, err)
	}
	// external
	p, err = provisionerFromEnv(env(map[string]string{"RELAY_PROVISIONER": "external"}))
	if err != nil || p.Name() != "external" {
		t.Fatalf("external = %v, %v", p, err)
	}
	// unknown
	if _, err := provisionerFromEnv(env(map[string]string{"RELAY_PROVISIONER": "nope"})); !errors.Is(err, ErrUnknownProvisioner) {
		t.Fatalf("unknown err=%v want ErrUnknownProvisioner", err)
	}
	// kubernetes missing config → fail closed
	if _, err := provisionerFromEnv(env(map[string]string{"RELAY_PROVISIONER": "kubernetes"})); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("k8s missing cfg err=%v want ErrNotConfigured", err)
	}
	// proxmox missing config → fail closed
	if _, err := provisionerFromEnv(env(map[string]string{"RELAY_PROVISIONER": "proxmox"})); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("proxmox missing cfg err=%v want ErrNotConfigured", err)
	}
}

// ── Demand API ────────────────────────────────────────────────────────────────

func TestDemandAPIObserveAndDemand(t *testing.T) {
	store := NewDemandStore(time.Minute, nil)
	api := NewDemandAPI(store, Policy{ScaleUpAt: 0.75, MinPerRegion: 1, Step: 1}, "external", func() string { return "sekret" })
	mux := http.NewServeMux()
	api.Register(mux, "/api/relay/scale")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// POST /observe without secret → 401
	body := `{"regions":[{"region":"eu","instances":2,"saturation":0.9}]}`
	resp := do(t, srv.URL+"/api/relay/scale/observe", http.MethodPost, "", body)
	if resp != http.StatusUnauthorized {
		t.Fatalf("observe no-auth status=%d want 401", resp)
	}
	// with secret → 200
	if resp := do(t, srv.URL+"/api/relay/scale/observe", http.MethodPost, "sekret", body); resp != http.StatusOK {
		t.Fatalf("observe auth status=%d want 200", resp)
	}
	// GET /demand → shows eu desired=3 (2 + step), provisioner external, actuated false
	r, err := http.Get(srv.URL + "/api/relay/scale/demand")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var dr DemandResponse
	if err := json.NewDecoder(r.Body).Decode(&dr); err != nil {
		t.Fatal(err)
	}
	if dr.Provisioner != "external" || dr.Actuated {
		t.Fatalf("demand provisioner=%s actuated=%v want external/false", dr.Provisioner, dr.Actuated)
	}
	if len(dr.Regions) != 1 || dr.Regions[0].Desired != 3 {
		t.Fatalf("demand regions=%+v want eu desired 3", dr.Regions)
	}
}

func TestDemandStoreDropsStale(t *testing.T) {
	now := time.Now()
	clock := &now
	store := NewDemandStore(time.Minute, func() time.Time { return *clock })
	store.Observe(RegionLoad{Region: "eu", Instances: 1})
	if len(store.Snapshot()) != 1 {
		t.Fatal("fresh observation missing")
	}
	*clock = now.Add(2 * time.Minute)
	if len(store.Snapshot()) != 0 {
		t.Fatal("stale observation not dropped")
	}
}

func do(t *testing.T, url, method, secret, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		req.Header.Set("X-Relay-Auth", secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// ── Demand API: fail-closed + edge branches ─────────────────────────────────

// TestDemandAPI_ObserveFailsClosedWithoutSecret proves the observe endpoint
// refuses (503) when no shared secret is configured — it never accepts an
// anonymous load push that could steer the published desired state.
func TestDemandAPI_ObserveFailsClosedWithoutSecret(t *testing.T) {
	store := NewDemandStore(time.Minute, nil)
	// Empty secret ⇒ observe disabled.
	api := NewDemandAPI(store, Policy{}, "manual", func() string { return "" })
	mux := http.NewServeMux()
	api.Register(mux, "/api/relay/scale")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body := `{"regions":[{"region":"eu","instances":2,"saturation":0.9}]}`
	// Even WITH a bearer, an unconfigured secret is 503 (disabled), never 200.
	if code := do(t, srv.URL+"/api/relay/scale/observe", http.MethodPost, "anything", body); code != http.StatusServiceUnavailable {
		t.Fatalf("observe with no configured secret = %d, want 503 (fail-closed)", code)
	}
	// And nothing was recorded — the push was rejected before Observe.
	if n := len(store.Snapshot()); n != 0 {
		t.Fatalf("store recorded %d regions despite disabled observe; want 0", n)
	}
}

// TestDemandAPI_NilSecretDefaultsToDisabled proves NewDemandAPI treats a nil
// secret func as the empty (disabled) secret — fail-closed, not a nil-deref.
func TestDemandAPI_NilSecretDefaultsToDisabled(t *testing.T) {
	api := NewDemandAPI(NewDemandStore(0, nil), Policy{}, "manual", nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/observe", strings.NewReader(`{}`))
	api.ServeObserve(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("observe with nil secret = %d, want 503", rr.Code)
	}
}

// TestDemandAPI_ObserveBadJSON proves a malformed body is a clean 400, not a
// 500 or a panic — and does not mutate the store.
func TestDemandAPI_ObserveBadJSON(t *testing.T) {
	store := NewDemandStore(time.Minute, nil)
	api := NewDemandAPI(store, Policy{}, "manual", func() string { return "sekret" })
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/observe", strings.NewReader(`{not-json`))
	req.Header.Set("X-Relay-Auth", "sekret")
	api.ServeObserve(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("observe bad json = %d, want 400", rr.Code)
	}
	if n := len(store.Snapshot()); n != 0 {
		t.Fatalf("store mutated on bad json (%d regions); want 0", n)
	}
}

// TestDemandAPI_ActuatedReflectsProvisioner proves the Actuated flag is derived
// correctly: true only when a self-actuating provisioner (not manual/external)
// is named. This is what tells an external scaler whether IT must act.
func TestDemandAPI_ActuatedReflectsProvisioner(t *testing.T) {
	cases := map[string]bool{
		"manual":     false,
		"external":   false,
		"":           false,
		"kubernetes": true,
		"proxmox":    true,
	}
	for name, wantActuated := range cases {
		api := NewDemandAPI(NewDemandStore(0, nil), Policy{}, name, func() string { return "s" })
		if got := api.Demand().Actuated; got != wantActuated {
			t.Errorf("provisioner %q: Actuated=%v, want %v", name, got, wantActuated)
		}
	}
}

// ── Policy env parsing ──────────────────────────────────────────────────────

// Test_policyFromEnv proves RELAY_SCALE_* env is parsed into the Policy, and
// that unset/garbage values fall back to the zero-value defaults rather than
// erroring — a self-hoster who sets nothing gets a well-formed (default) policy.
func Test_policyFromEnv(t *testing.T) {
	// All set + valid.
	env := map[string]string{
		"RELAY_SCALE_UP_AT":          "0.8",
		"RELAY_SCALE_DOWN_AT":        "0.2",
		"RELAY_SCALE_MIN_PER_REGION": "2",
		"RELAY_SCALE_MAX_PER_REGION": "9",
		"RELAY_SCALE_STEP":           "3",
	}
	p := policyFromEnv(func(k string) string { return env[k] })
	if p.ScaleUpAt != 0.8 || p.ScaleDownAt != 0.2 || p.MinPerRegion != 2 || p.MaxPerRegion != 9 || p.Step != 3 {
		t.Fatalf("policyFromEnv parsed = %+v, want {0.8 0.2 2 9 3}", p)
	}

	// Unset + garbage ⇒ zero-value defaults (no error, no panic).
	bad := map[string]string{"RELAY_SCALE_UP_AT": "not-a-float", "RELAY_SCALE_STEP": "xyz"}
	p = policyFromEnv(func(k string) string { return bad[k] })
	if p.ScaleUpAt != 0 || p.MinPerRegion != 0 || p.Step != 0 {
		t.Fatalf("policyFromEnv on garbage = %+v, want zero-value policy", p)
	}
}
