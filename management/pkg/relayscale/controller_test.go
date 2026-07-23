package relayscale

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// controller_test.go — table-driven tests for the periodic control loop: scale-up
// on high demand, scale-down-with-drain on low, advisory-only when the provider
// does not actuate, cooldown prevents flap, per-region independence, and a
// provider error HOLDS (no thrash).

// ctlFakeProv is a controllable RelayProvisioner for controller tests. Unlike the
// actuator's fakeProv it can fail List/Provision/Destroy independently, optionally
// implements Cordon, and records calls.
type ctlFakeProv struct {
	mu    sync.Mutex
	seq   int
	insts []Instance

	enabled  bool
	failList bool
	failProv bool
	failDest bool
	cordons  int
	provs    int
	dests    int
}

func (f *ctlFakeProv) Name() string  { return "fake" }
func (f *ctlFakeProv) Enabled() bool { return f.enabled }

func (f *ctlFakeProv) Provision(_ context.Context, region string, _ RelaySpec) (Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failProv {
		return Instance{}, errors.New("provision boom")
	}
	f.seq++
	in := Instance{ID: region + "-n" + itoa(f.seq), Region: region, Provider: "fake", Ready: true, CreatedAt: time.Unix(int64(f.seq), 0)}
	f.insts = append(f.insts, in)
	f.provs++
	return in, nil
}

func (f *ctlFakeProv) Destroy(_ context.Context, inst Instance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDest {
		return errors.New("destroy boom")
	}
	out := f.insts[:0]
	for _, i := range f.insts {
		if i.ID != inst.ID {
			out = append(out, i)
		}
	}
	f.insts = out
	f.dests++
	return nil
}

func (f *ctlFakeProv) List(context.Context) ([]Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failList {
		return nil, errors.New("list boom")
	}
	return append([]Instance(nil), f.insts...), nil
}

func (f *ctlFakeProv) counts() (prov, dest, cordon int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.provs, f.dests, f.cordons
}

func (f *ctlFakeProv) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.insts)
}

func (f *ctlFakeProv) seed(insts ...Instance) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.insts = append(f.insts, insts...)
}

// ctlCordonProv is a ctlFakeProv that also implements Cordoner.
type ctlCordonProv struct{ *ctlFakeProv }

func (f ctlCordonProv) Cordon(context.Context, Instance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cordons++
	return nil
}

// newTestController builds a controller with a fixed clock and tiny cooldowns/drain
// unless overridden.
func newTestController(prov RelayProvisioner, cfg ControllerConfig, store *DemandStore, clk *time.Time) *Controller {
	c := NewController(prov, Policy{MinPerRegion: 1, MaxPerRegion: 5, Step: 1, ScaleUpAt: 0.75, ScaleDownAt: 0.25}, nil, store, cfg, nil)
	c.now = func() time.Time { return *clk }
	return c
}

func TestController_ScaleUpOnHighDemand(t *testing.T) {
	now := time.Unix(1000, 0)
	fp := &ctlFakeProv{enabled: true}
	fp.seed(Instance{ID: "eu-seed", Region: "eu", Ready: true})
	store := NewDemandStore(time.Hour, func() time.Time { return now })
	store.Observe(RegionLoad{Region: "eu", Instances: 1, Saturation: 0.95})

	c := newTestController(fp, ControllerConfig{}, store, &now)
	c.Tick(context.Background())

	if p, _, _ := fp.counts(); p != 1 {
		t.Fatalf("provisions=%d want 1", p)
	}
	if fp.count() != 2 {
		t.Fatalf("fleet=%d want 2 (seed + provisioned)", fp.count())
	}
	st := regionStatus(c.Status(), "eu")
	if st.LastAction != actScaleUp || st.Desired != 2 {
		t.Fatalf("status=%+v want scale_up desired 2", st)
	}
}

func TestController_ScaleDownDrainsThenDestroys(t *testing.T) {
	now := time.Unix(1000, 0)
	fp := &ctlFakeProv{enabled: true}
	fp.seed(
		Instance{ID: "eu-a", Region: "eu", Ready: true, CreatedAt: time.Unix(1, 0)},
		Instance{ID: "eu-b", Region: "eu", Ready: true, CreatedAt: time.Unix(2, 0)},
	)
	store := NewDemandStore(time.Hour, func() time.Time { return now })
	store.Observe(RegionLoad{Region: "eu", Instances: 2, Saturation: 0.05}) // cold → shed one

	c := newTestController(fp, ControllerConfig{DrainTimeout: time.Minute, ScaleDownCooldown: 10 * time.Minute}, store, &now)

	// Tick 1: begins draining a victim, but destroys NOTHING yet.
	c.Tick(context.Background())
	if _, d, _ := fp.counts(); d != 0 {
		t.Fatalf("after drain-start destroys=%d want 0 (drain not elapsed)", d)
	}
	st := regionStatus(c.Status(), "eu")
	if st.LastAction != actDrain || st.Draining != 1 {
		t.Fatalf("status=%+v want drain / draining 1", st)
	}
	if fp.count() != 2 {
		t.Fatalf("fleet=%d want 2 (nothing destroyed during drain)", fp.count())
	}

	// Tick 2 BEFORE the window elapses: idempotent — still draining, no destroy,
	// no second victim (cooldown + already-draining exclusion).
	now = now.Add(30 * time.Second)
	c.Tick(context.Background())
	if _, d, _ := fp.counts(); d != 0 {
		t.Fatalf("mid-drain destroys=%d want 0", d)
	}
	if s := regionStatus(c.Status(), "eu"); s.Draining != 1 {
		t.Fatalf("mid-drain draining=%d want 1", s.Draining)
	}

	// Tick 3 AFTER the window: the drained node is destroyed.
	now = now.Add(time.Minute)
	c.Tick(context.Background())
	if _, d, _ := fp.counts(); d != 1 {
		t.Fatalf("post-drain destroys=%d want 1", d)
	}
	if fp.count() != 1 {
		t.Fatalf("fleet=%d want 1 after destroy", fp.count())
	}
	if s := regionStatus(c.Status(), "eu"); s.Draining != 0 || s.LastAction != actDestroy {
		t.Fatalf("status=%+v want destroy / draining 0", s)
	}
}

func TestController_CordonBeforeDrain(t *testing.T) {
	now := time.Unix(1000, 0)
	base := &ctlFakeProv{enabled: true}
	base.seed(
		Instance{ID: "eu-a", Region: "eu", Ready: true, CreatedAt: time.Unix(1, 0)},
		Instance{ID: "eu-b", Region: "eu", Ready: true, CreatedAt: time.Unix(2, 0)},
	)
	fp := ctlCordonProv{base}
	store := NewDemandStore(time.Hour, func() time.Time { return now })
	store.Observe(RegionLoad{Region: "eu", Instances: 2, Saturation: 0.01})

	c := newTestController(fp, ControllerConfig{DrainTimeout: time.Minute}, store, &now)
	c.Tick(context.Background())
	if _, _, cor := base.counts(); cor != 1 {
		t.Fatalf("cordons=%d want 1 (victim cordoned before drain)", cor)
	}
}

func TestController_AdvisoryWhenNotActuated(t *testing.T) {
	for _, prov := range []RelayProvisioner{NewManualProvisioner(), NewExternalProvisioner()} {
		now := time.Unix(1000, 0)
		store := NewDemandStore(time.Hour, func() time.Time { return now })
		store.Observe(RegionLoad{Region: "eu", Instances: 2, Saturation: 0.99}) // hot

		c := newTestController(prov, ControllerConfig{}, store, &now)
		c.Tick(context.Background())

		s := c.Status()
		if s.Actuated {
			t.Fatalf("provisioner %s: Actuated=true, want false", prov.Name())
		}
		st := regionStatus(s, "eu")
		if st.LastAction != actAdvisory {
			t.Fatalf("provisioner %s: last_action=%s want advisory", prov.Name(), st.LastAction)
		}
		// Desired still computed (2 hot + step = 3) — surfaced for an external scaler.
		if st.Desired != 3 {
			t.Fatalf("provisioner %s: desired=%d want 3 (advisory still computes)", prov.Name(), st.Desired)
		}
	}
}

func TestController_CooldownPreventsFlap(t *testing.T) {
	now := time.Unix(1000, 0)
	fp := &ctlFakeProv{enabled: true}
	fp.seed(Instance{ID: "eu-a", Region: "eu", Ready: true})
	store := NewDemandStore(time.Hour, func() time.Time { return now })
	store.Observe(RegionLoad{Region: "eu", Instances: 1, Saturation: 0.95})

	c := newTestController(fp, ControllerConfig{ScaleUpCooldown: 5 * time.Minute}, store, &now)

	// First tick: provisions one.
	c.Tick(context.Background())
	if p, _, _ := fp.counts(); p != 1 {
		t.Fatalf("first tick provisions=%d want 1", p)
	}
	// Second tick 1m later, still hot: WITHIN cooldown → no new provision.
	now = now.Add(time.Minute)
	store.Observe(RegionLoad{Region: "eu", Instances: 2, Saturation: 0.95})
	c.Tick(context.Background())
	if p, _, _ := fp.counts(); p != 1 {
		t.Fatalf("within-cooldown provisions=%d want 1 (no flap)", p)
	}
	if s := regionStatus(c.Status(), "eu"); s.LastAction != actCooldown {
		t.Fatalf("last_action=%s want cooldown", s.LastAction)
	}
	// Third tick AFTER cooldown, still hot: provisions again.
	now = now.Add(6 * time.Minute)
	store.Observe(RegionLoad{Region: "eu", Instances: 2, Saturation: 0.95})
	c.Tick(context.Background())
	if p, _, _ := fp.counts(); p != 2 {
		t.Fatalf("post-cooldown provisions=%d want 2", p)
	}
}

func TestController_PerRegionIndependence(t *testing.T) {
	now := time.Unix(1000, 0)
	fp := &ctlFakeProv{enabled: true}
	fp.seed(
		Instance{ID: "eu-a", Region: "eu", Ready: true},
		Instance{ID: "af-a", Region: "af", Ready: true, CreatedAt: time.Unix(1, 0)},
		Instance{ID: "af-b", Region: "af", Ready: true, CreatedAt: time.Unix(2, 0)},
	)
	store := NewDemandStore(time.Hour, func() time.Time { return now })
	store.Observe(RegionLoad{Region: "eu", Instances: 1, Saturation: 0.95}) // hot → up
	store.Observe(RegionLoad{Region: "af", Instances: 2, Saturation: 0.02}) // cold → drain
	store.Observe(RegionLoad{Region: "us", Instances: 2, Saturation: 0.50}) // nominal → hold
	fp.seed(Instance{ID: "us-a", Region: "us", Ready: true}, Instance{ID: "us-b", Region: "us", Ready: true})

	c := newTestController(fp, ControllerConfig{DrainTimeout: time.Minute}, store, &now)
	c.Tick(context.Background())

	if s := regionStatus(c.Status(), "eu"); s.LastAction != actScaleUp {
		t.Errorf("eu last_action=%s want scale_up", s.LastAction)
	}
	if s := regionStatus(c.Status(), "af"); s.LastAction != actDrain || s.Draining != 1 {
		t.Errorf("af status=%+v want drain / draining 1", s)
	}
	if s := regionStatus(c.Status(), "us"); s.LastAction != actHold {
		t.Errorf("us last_action=%s want hold", s.LastAction)
	}
	// eu got a node, af/us none provisioned.
	if p, _, _ := fp.counts(); p != 1 {
		t.Errorf("provisions=%d want 1 (eu only)", p)
	}
}

func TestController_ProviderErrorHolds(t *testing.T) {
	// List error: whole pass holds, nothing actuated, error surfaced.
	t.Run("list error holds", func(t *testing.T) {
		now := time.Unix(1000, 0)
		fp := &ctlFakeProv{enabled: true, failList: true}
		fp.seed(Instance{ID: "eu-a", Region: "eu", Ready: true})
		store := NewDemandStore(time.Hour, func() time.Time { return now })
		store.Observe(RegionLoad{Region: "eu", Instances: 1, Saturation: 0.95})

		c := newTestController(fp, ControllerConfig{}, store, &now)
		c.Tick(context.Background())
		if p, d, _ := fp.counts(); p != 0 || d != 0 {
			t.Fatalf("provisions=%d destroys=%d want 0/0 on list error", p, d)
		}
		if c.Status().LastError == "" {
			t.Fatal("LastError empty, want the list failure surfaced")
		}
	})

	// Provision error: region holds (no partial thrash), error surfaced.
	t.Run("provision error holds", func(t *testing.T) {
		now := time.Unix(1000, 0)
		fp := &ctlFakeProv{enabled: true, failProv: true}
		fp.seed(Instance{ID: "eu-a", Region: "eu", Ready: true})
		store := NewDemandStore(time.Hour, func() time.Time { return now })
		store.Observe(RegionLoad{Region: "eu", Instances: 1, Saturation: 0.95})

		c := newTestController(fp, ControllerConfig{}, store, &now)
		c.Tick(context.Background())
		if p, _, _ := fp.counts(); p != 0 {
			t.Fatalf("provisions=%d want 0 on provision error", p)
		}
		if c.Status().LastError == "" {
			t.Fatal("LastError empty, want the provision failure surfaced")
		}
		// Cooldown clock NOT advanced (no successful action) → a healthy provider
		// next tick can act immediately.
		if s := regionStatus(c.Status(), "eu"); s.LastActionAt != nil {
			t.Fatalf("lastActionAt set despite failed provision: %v", s.LastActionAt)
		}
	})

	// Destroy error during reap: node stays draining for a retry, not lost.
	t.Run("destroy error retries", func(t *testing.T) {
		now := time.Unix(1000, 0)
		fp := &ctlFakeProv{enabled: true}
		fp.seed(
			Instance{ID: "eu-a", Region: "eu", Ready: true, CreatedAt: time.Unix(1, 0)},
			Instance{ID: "eu-b", Region: "eu", Ready: true, CreatedAt: time.Unix(2, 0)},
		)
		store := NewDemandStore(time.Hour, func() time.Time { return now })
		store.Observe(RegionLoad{Region: "eu", Instances: 2, Saturation: 0.01})

		c := newTestController(fp, ControllerConfig{DrainTimeout: time.Minute, ScaleDownCooldown: time.Hour}, store, &now)
		c.Tick(context.Background()) // start drain
		fp.mu.Lock()
		fp.failDest = true
		fp.mu.Unlock()
		now = now.Add(2 * time.Minute)
		c.Tick(context.Background()) // reap attempts destroy, fails
		if _, d, _ := fp.counts(); d != 0 {
			t.Fatalf("destroys=%d want 0 (destroy failed)", d)
		}
		if s := regionStatus(c.Status(), "eu"); s.Draining != 1 {
			t.Fatalf("draining=%d want 1 (kept for retry)", s.Draining)
		}
		// Recover: next tick destroys successfully.
		fp.mu.Lock()
		fp.failDest = false
		fp.mu.Unlock()
		now = now.Add(time.Minute)
		c.Tick(context.Background())
		if _, d, _ := fp.counts(); d != 1 {
			t.Fatalf("destroys=%d want 1 after recovery", d)
		}
	})
}

func TestController_StatusEndpoint(t *testing.T) {
	now := time.Unix(1000, 0)
	fp := &ctlFakeProv{enabled: true}
	fp.seed(Instance{ID: "eu-a", Region: "eu", Ready: true})
	store := NewDemandStore(time.Hour, func() time.Time { return now })
	store.Observe(RegionLoad{Region: "eu", Instances: 1, Saturation: 0.95})

	c := newTestController(fp, ControllerConfig{}, store, &now)
	c.Tick(context.Background())

	mux := http.NewServeMux()
	c.Register(mux, "/api/relay/scale")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r, err := http.Get(srv.URL + "/api/relay/scale/controller")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var cs ControllerStatus
	if err := json.NewDecoder(r.Body).Decode(&cs); err != nil {
		t.Fatal(err)
	}
	if !cs.Actuated || cs.Provisioner != "fake" {
		t.Fatalf("status provisioner=%s actuated=%v want fake/true", cs.Provisioner, cs.Actuated)
	}
	if len(cs.Regions) != 1 || cs.Regions[0].Region != "eu" || cs.Regions[0].Desired != 2 {
		t.Fatalf("regions=%+v want eu desired 2", cs.Regions)
	}
}

func Test_controllerConfigFromEnv(t *testing.T) {
	env := map[string]string{
		"RELAY_SCALE_INTERVAL":      "10s",
		"RELAY_SCALE_UP_COOLDOWN":   "15s",
		"RELAY_SCALE_DOWN_COOLDOWN": "9m",
		"RELAY_SCALE_DRAIN_TIMEOUT": "90s",
	}
	cfg := controllerConfigFromEnv(func(k string) string { return env[k] })
	if cfg.Interval != 10*time.Second || cfg.ScaleUpCooldown != 15*time.Second ||
		cfg.ScaleDownCooldown != 9*time.Minute || cfg.DrainTimeout != 90*time.Second {
		t.Fatalf("parsed = %+v", cfg)
	}
	// Garbage/unset → zeros (defaults applied later by withDefaults).
	bad := controllerConfigFromEnv(func(string) string { return "not-a-dur" })
	if bad.Interval != 0 || bad.DrainTimeout != 0 {
		t.Fatalf("garbage env not zeroed: %+v", bad)
	}
	// withDefaults keeps ScaleDownCooldown >= DrainTimeout.
	c := ControllerConfig{ScaleDownCooldown: time.Second, DrainTimeout: time.Minute}
	c.withDefaults()
	if c.ScaleDownCooldown < c.DrainTimeout {
		t.Fatalf("scale-down cooldown %s < drain %s after defaults", c.ScaleDownCooldown, c.DrainTimeout)
	}
}

// regionStatus finds a region row in a ControllerStatus (empty if absent).
func regionStatus(s ControllerStatus, region string) RegionStatus {
	for _, r := range s.Regions {
		if r.Region == region {
			return r
		}
	}
	return RegionStatus{}
}
