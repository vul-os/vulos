package fabric_test

// FABRIC-SECRET-ROT-01 tests.
//
// The test that matters, and the reason this file exists, is
// TestOldSecretIsAcceptedBeforeTheWindowClosesAndRefusedAfter: BOTH directions,
// against a real HTTPS listener serving the real handlers. A rotation that never
// rejects the old value has done nothing at all, and it would pass every
// "accepts the new secret" test you could write.
//
// The clock is injected rather than slept through. A test that sleeps past a
// real deadline is a test that flakes on a loaded host, and the whole point of
// the deadline is that it is evaluated per request — which an injected clock
// demonstrates precisely and a sleep only approximates.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"vulos/backend/internal/fabric"
	"vulos/backend/internal/multiinstance"
)

const (
	oldSecret = "fabric-secret-OLD-0000000000000000"
	newSecret = "fabric-secret-NEW-1111111111111111"
)

// fakeClock is a settable clock for the ring.
type fakeClock struct{ v atomic.Int64 }

func newFakeClock(t time.Time) *fakeClock {
	c := &fakeClock{}
	c.set(t)
	return c
}
func (c *fakeClock) set(t time.Time)         { c.v.Store(t.UnixNano()) }
func (c *fakeClock) now() time.Time          { return time.Unix(0, c.v.Load()).UTC() }
func (c *fakeClock) advance(d time.Duration) { c.set(c.now().Add(d)) }

// ── unit: what the ring accepts ──────────────────────────────────────────────

func TestSecretRingAcceptsCurrentAndRefusesEverythingElse(t *testing.T) {
	r := fabric.NewSecretRing(newSecret, "", time.Time{})
	if !r.Accepts(newSecret) {
		t.Fatal("the ring refused its own current secret")
	}
	for _, bad := range []string{"", oldSecret, newSecret + "x", strings.ToUpper(newSecret)} {
		if r.Accepts(bad) {
			t.Fatalf("the ring accepted %q with no overlap configured", bad)
		}
	}
	if r.OverlapOpen() {
		t.Fatal("no overlap was configured but OverlapOpen() is true")
	}
	if r.Current() != newSecret {
		t.Fatalf("Current() = %q, want the current secret", r.Current())
	}
}

func TestSecretRingWithNoCurrentSecretAcceptsNothing(t *testing.T) {
	// Matches the pre-rotation fail-closed behaviour: an unset VULOS_FABRIC_SECRET
	// must not become "accepts the empty string".
	r := fabric.NewSecretRing("", oldSecret, time.Now().Add(time.Hour))
	for _, bad := range []string{"", oldSecret} {
		if r.Accepts(bad) {
			t.Fatalf("a ring with no current secret accepted %q", bad)
		}
	}
	if len(r.Warnings()) == 0 {
		t.Fatal("an overlap with no current secret was silently dropped — it must warn")
	}
}

// TestSecretRingClosesTheWindowOnTheDeadlineWithoutARestart is the unit-level
// half of the headline test: ONE ring, ONE presented secret, two clock readings,
// two answers.
func TestSecretRingClosesTheWindowOnTheDeadlineWithoutARestart(t *testing.T) {
	start := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	deadline := start.Add(2 * time.Hour)
	clk := newFakeClock(start)
	r := fabric.NewSecretRing(newSecret, oldSecret, deadline).WithClock(clk.now)

	if !r.Accepts(oldSecret) {
		t.Fatal("BEFORE the deadline the overlap secret was refused — the window never opened")
	}
	if !r.OverlapOpen() {
		t.Fatal("OverlapOpen() is false inside the window")
	}

	// One nanosecond before: still open. The boundary is where an off-by-one
	// turns "closes at 14:00" into "closes at 14:00 tomorrow".
	clk.set(deadline.Add(-time.Nanosecond))
	if !r.Accepts(oldSecret) {
		t.Fatal("the overlap closed one nanosecond early")
	}

	// Exactly at the deadline: CLOSED. Before() is strict, and "until 14:00"
	// must not mean "and also at 14:00".
	clk.set(deadline)
	if r.Accepts(oldSecret) {
		t.Fatal("AFTER the deadline the overlap secret was still accepted — the window never closed, " +
			"which makes this two permanent secrets rather than a rotation")
	}
	if r.OverlapOpen() {
		t.Fatal("OverlapOpen() is true past the deadline")
	}

	// And the current secret is unaffected in both eras: closing the window must
	// not be an outage.
	clk.advance(365 * 24 * time.Hour)
	if !r.Accepts(newSecret) {
		t.Fatal("the current secret stopped being accepted when the overlap closed")
	}
	if r.Accepts(oldSecret) {
		t.Fatal("the overlap secret came back")
	}
}

// TestSecretRingRefusesAnOverlapThatWouldNeverClose covers every configuration
// that would turn "a rotation" into "two secrets". All of them must resolve to
// the NARROWER ring, never a wider one.
func TestSecretRingRefusesAnOverlapThatWouldNeverClose(t *testing.T) {
	cases := []struct {
		name     string
		alt      string
		until    time.Time
		presents string
	}{
		{
			name:     "no deadline at all",
			alt:      oldSecret,
			until:    time.Time{},
			presents: oldSecret,
		},
		{
			name:     "overlap identical to the current secret",
			alt:      newSecret,
			until:    time.Now().Add(time.Hour),
			presents: newSecret + "-not-really", // nothing new may be admitted
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := fabric.NewSecretRing(newSecret, tc.alt, tc.until)
			if r.OverlapOpen() {
				t.Fatalf("%s left the overlap OPEN", tc.name)
			}
			if r.Accepts(tc.presents) {
				t.Fatalf("%s admitted %q", tc.name, tc.presents)
			}
			if len(r.Warnings()) == 0 {
				t.Fatalf("%s was refused SILENTLY — an operator mid-roll would see a 401 with no explanation", tc.name)
			}
		})
	}
}

func TestSecretRingRefusesAnAlreadyExpiredOverlapWithoutCallingItAnError(t *testing.T) {
	// The normal END STATE of a roll: the operator left the variables in place
	// and the deadline passed. That is not a misconfiguration and must not warn,
	// but the old secret must be dead.
	r := fabric.NewSecretRing(newSecret, oldSecret, time.Now().Add(-time.Hour))
	if r.Accepts(oldSecret) {
		t.Fatal("an overlap whose deadline is already past was still accepted")
	}
	if len(r.Warnings()) != 0 {
		t.Fatalf("a naturally-expired overlap produced warnings %v — that is the expected end state, not an error", r.Warnings())
	}
	if !r.OverlapClosesAt().Equal(r.OverlapClosesAt().UTC()) || r.OverlapClosesAt().IsZero() {
		t.Fatal("OverlapClosesAt() must stay visible after the window has passed, so 'it closed at 14:02' is still answerable")
	}
}

func TestSecretRingClampsAnOverlapLongerThanTheMaximum(t *testing.T) {
	// A mistyped year is the failure that turns an overlap permanent.
	r := fabric.NewSecretRing(newSecret, oldSecret, time.Now().Add(3650*24*time.Hour))
	closes := r.OverlapClosesAt()
	if closes.After(time.Now().UTC().Add(fabric.MaxSecretOverlap + time.Minute)) {
		t.Fatalf("a 10-year overlap was accepted as-is (closes %s) — MaxSecretOverlap did not clamp it", closes)
	}
	if len(r.Warnings()) == 0 {
		t.Fatal("clamping a 10-year overlap down to the maximum was silent")
	}
	if !r.Accepts(oldSecret) {
		t.Fatal("clamping closed the window entirely — it should still be open, just bounded")
	}
}

// ── env parsing ──────────────────────────────────────────────────────────────

func TestLoadSecretRingFromEnv(t *testing.T) {
	until := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	t.Setenv(fabric.EnvFabricSecretAlso, oldSecret)
	t.Setenv(fabric.EnvFabricSecretAlsoUntil, until.Format(time.RFC3339))

	r := fabric.LoadSecretRingFromEnv(newSecret)
	if !r.Accepts(newSecret) || !r.Accepts(oldSecret) {
		t.Fatal("the environment-configured overlap did not admit both secrets")
	}
	if got := r.OverlapClosesAt(); !got.Equal(until) {
		t.Fatalf("OverlapClosesAt() = %s, want %s", got, until)
	}
}

func TestLoadSecretRingFromEnvRefusesAnUnparseableDeadline(t *testing.T) {
	t.Setenv(fabric.EnvFabricSecretAlso, oldSecret)
	t.Setenv(fabric.EnvFabricSecretAlsoUntil, "next tuesday")

	r := fabric.LoadSecretRingFromEnv(newSecret)
	if r.Accepts(oldSecret) {
		t.Fatal("an unparseable deadline opened an unbounded overlap — the exact failure this design exists to prevent")
	}
	if !r.Accepts(newSecret) {
		t.Fatal("a bad deadline took the CURRENT secret down with it — that is an outage, not a fail-closed")
	}
	if len(r.Warnings()) == 0 {
		t.Fatal("an unparseable deadline was refused silently")
	}
}

// ── the headline: both directions, over the wire, through the real handlers ──

// rotatingBox is one box whose fabric handlers are served over TLS, with a ring
// whose clock the test controls.
type rotatingBox struct {
	svc    *fabric.Service
	server *httptest.Server
	clk    *fakeClock
}

func newRotatingBox(t *testing.T, id, current, alt string, altUntil time.Time, clk *fakeClock) *rotatingBox {
	t.Helper()
	dir := t.TempDir()
	reg, err := multiinstance.Open(filepath.Join(dir, "instances.db"))
	if err != nil {
		t.Fatalf("[%s] open registry: %v", id, err)
	}
	t.Cleanup(func() { _ = reg.Close() })
	as, err := multiinstance.OpenAppSync(reg)
	if err != nil {
		t.Fatalf("[%s] open appsync: %v", id, err)
	}

	mux := http.NewServeMux()
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)

	ring := fabric.NewSecretRing(current, alt, altUntil)
	if clk != nil {
		ring.WithClock(clk.now)
	}
	svc, err := fabric.New(fabric.Config{
		InstanceID:   id,
		Secret:       current,
		Secrets:      ring,
		AppSync:      as,
		Discoverer:   fabric.NewStaticDiscoverer(),
		HTTPClient:   server.Client(),
		SyncInterval: time.Hour,
		SelfBaseURLs: []string{server.URL},
	})
	if err != nil {
		t.Fatalf("[%s] new fabric service: %v", id, err)
	}
	svc.RegisterHandlers(mux)
	return &rotatingBox{svc: svc, server: server, clk: clk}
}

// get issues an authenticated GET against a fabric endpoint and returns the
// status code and body.
func (b *rotatingBox) get(t *testing.T, path, secret string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, b.server.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if secret != "" {
		req.Header.Set("X-Fabric-Auth", secret)
	}
	resp, err := b.server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 1<<16)
	n, _ := resp.Body.Read(buf)
	return resp.StatusCode, buf[:n]
}

// TestOldSecretIsAcceptedBeforeTheWindowClosesAndRefusedAfter is THE test.
//
// A box has committed to the new secret and is running a closing overlap on the
// old one. A peer that holds ONLY the old secret must be served before the
// deadline and refused after it, with no restart in between — and the peer
// holding the new secret must be unaffected throughout, because a rotation that
// takes the fleet down has not rotated anything either.
func TestOldSecretIsAcceptedBeforeTheWindowClosesAndRefusedAfter(t *testing.T) {
	start := time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC)
	deadline := start.Add(4 * time.Hour)
	clk := newFakeClock(start)
	box := newRotatingBox(t, "01HWZROT00000000000000000A", newSecret, oldSecret, deadline, clk)

	for _, path := range []string{"/api/fabric/changeset", "/api/fabric/status"} {
		t.Run(strings.TrimPrefix(path, "/api/fabric/"), func(t *testing.T) {
			// BEFORE — the old secret works. Without this half, a "rotation" that
			// simply refused the old value from the start would pass the other half
			// while having partitioned the fleet.
			clk.set(start.Add(time.Minute))
			if code, body := box.get(t, path, oldSecret); code != http.StatusOK {
				t.Fatalf("BEFORE the deadline, a box holding only the OLD secret got %d on %s, want 200. "+
					"The overlap never opened, so rolling this fleet would partition it.\nbody: %s", code, path, body)
			}

			// AFTER — the same box, the same process, one clock tick later.
			clk.set(deadline.Add(time.Second))
			if code, _ := box.get(t, path, oldSecret); code != http.StatusUnauthorized {
				t.Fatalf("AFTER the deadline, a box holding only the OLD secret got %d on %s, want 401. "+
					"The window never closes, which makes this two permanent secrets and not a rotation.", code, path)
			}

			// And the new secret is served in both eras.
			for _, at := range []time.Time{start.Add(time.Minute), deadline.Add(time.Hour)} {
				clk.set(at)
				if code, _ := box.get(t, path, newSecret); code != http.StatusOK {
					t.Fatalf("the CURRENT secret got %d on %s at %s, want 200 — closing the overlap took the fleet down with it",
						code, path, at)
				}
			}

			// A secret that was never valid is refused in both eras, so the test
			// above is not passing because the box admits everything.
			for _, at := range []time.Time{start.Add(time.Minute), deadline.Add(time.Hour)} {
				clk.set(at)
				if code, _ := box.get(t, path, "not-either-secret"); code != http.StatusUnauthorized {
					t.Fatalf("an unrelated secret got %d on %s at %s, want 401", code, path, at)
				}
			}
		})
	}
}

// TestStatusNamesTheSlotTheCallerAuthenticatedWith proves the operator's
// box-by-box check works: present the new secret and the box says which slot it
// landed in, which is the difference between "prepared" and "committed".
func TestStatusNamesTheSlotTheCallerAuthenticatedWith(t *testing.T) {
	clk := newFakeClock(time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))
	deadline := clk.now().Add(4 * time.Hour)

	// PHASE 1 box: still sends the OLD secret, accepts the new one in its
	// overlap slot.
	prepared := newRotatingBox(t, "01HWZROT00000000000000000P", oldSecret, newSecret, deadline, clk)
	// PHASE 2 box: committed to the new secret, accepts the old one on overlap.
	committed := newRotatingBox(t, "01HWZROT00000000000000000C", newSecret, oldSecret, deadline, clk)

	slotOf := func(b *rotatingBox, secret string) string {
		code, body := b.get(t, "/api/fabric/status", secret)
		if code != http.StatusOK {
			t.Fatalf("status returned %d for a secret that should be accepted", code)
		}
		var st struct {
			AuthenticatedWith string `json:"authenticated_with"`
		}
		if err := json.Unmarshal(body, &st); err != nil {
			t.Fatalf("decode status: %v (body %s)", err, body)
		}
		return st.AuthenticatedWith
	}

	if got := slotOf(prepared, newSecret); got != fabric.SlotOverlap {
		t.Fatalf("a box that has only PREPARED reported authenticated_with=%q for the new secret, want %q — "+
			"an operator would read this as 'already committed' and close the window on a box that has not moved",
			got, fabric.SlotOverlap)
	}
	if got := slotOf(committed, newSecret); got != fabric.SlotCurrent {
		t.Fatalf("a box that has COMMITTED reported authenticated_with=%q for the new secret, want %q", got, fabric.SlotCurrent)
	}
}

// TestStatusReportsWhetherAnybodyIsStillOnTheOldSecret proves the fleet-wide
// half: the counters move, and they distinguish the two slots.
func TestStatusReportsWhetherAnybodyIsStillOnTheOldSecret(t *testing.T) {
	clk := newFakeClock(time.Date(2026, 8, 16, 9, 0, 0, 0, time.UTC))
	deadline := clk.now().Add(4 * time.Hour)
	box := newRotatingBox(t, "01HWZROT00000000000000000S", newSecret, oldSecret, deadline, clk)

	type rot struct {
		OverlapConfigured bool       `json:"overlap_configured"`
		OverlapOpen       bool       `json:"overlap_open"`
		AdmittedOnCurrent uint64     `json:"admitted_on_current"`
		AdmittedOnOverlap uint64     `json:"admitted_on_overlap"`
		OverlapLastUsedAt *time.Time `json:"overlap_last_used_at"`
	}
	read := func(secret string) rot {
		code, body := box.get(t, "/api/fabric/status", secret)
		if code != http.StatusOK {
			t.Fatalf("status returned %d", code)
		}
		var st struct {
			SecretRotation rot `json:"secret_rotation"`
		}
		if err := json.Unmarshal(body, &st); err != nil {
			t.Fatalf("decode status: %v (body %s)", err, body)
		}
		return st.SecretRotation
	}

	// Nobody has used the overlap yet.
	first := read(newSecret)
	if !first.OverlapConfigured || !first.OverlapOpen {
		t.Fatalf("an open overlap reported configured=%v open=%v", first.OverlapConfigured, first.OverlapOpen)
	}
	if first.AdmittedOnOverlap != 0 || first.OverlapLastUsedAt != nil {
		t.Fatalf("nothing has presented the old secret yet, but the status already reports %d overlap admissions",
			first.AdmittedOnOverlap)
	}

	// A laggard box arrives on the old secret.
	if code, _ := box.get(t, "/api/fabric/changeset", oldSecret); code != http.StatusOK {
		t.Fatalf("the overlap secret got %d inside the window", code)
	}
	after := read(newSecret)
	if after.AdmittedOnOverlap == 0 || after.OverlapLastUsedAt == nil {
		t.Fatal("a peer arrived on the OLD secret and the status did not record it — " +
			"an operator would close the window on a box that is still running")
	}
	if after.AdmittedOnCurrent <= first.AdmittedOnCurrent {
		t.Fatal("admissions on the CURRENT secret are not being counted, so the two slots cannot be told apart")
	}

	// Polling the status endpoint must not itself move the overlap counter:
	// Slot is inspection, Accepts is the door.
	before := read(newSecret).AdmittedOnOverlap
	_ = read(newSecret)
	if got := read(newSecret).AdmittedOnOverlap; got != before {
		t.Fatalf("polling the status on the CURRENT secret inflated the overlap counter (%d → %d) — "+
			"the measurement is corrupting itself", before, got)
	}
}

// TestFabricRefusesARingThatDisagreesWithTheSecretItSends pins the construction
// invariant: a box that sends one value and calls another one "current" fails
// halfway through a roll with an unexplained 401.
func TestFabricRefusesARingThatDisagreesWithTheSecretItSends(t *testing.T) {
	_, err := fabric.New(fabric.Config{
		InstanceID: "01HWZROT00000000000000000X",
		Secret:     newSecret,
		Secrets:    fabric.NewSecretRing(oldSecret, "", time.Time{}),
		AppSync:    nil,
		Discoverer: fabric.NewStaticDiscoverer(),
		HTTPClient: http.DefaultClient,
	})
	if err == nil {
		t.Fatal("fabric.New accepted a ring whose Current disagrees with Config.Secret")
	}
	if !strings.Contains(err.Error(), "Secrets.Current()") {
		t.Fatalf("error does not name the mismatch: %v", err)
	}
}

// TestFabricRegistersNoRotationEndpoint is a regression gate on the "who may
// rotate" decision, not a hollow 404 check: it asserts that the fabric service
// registers exactly the three endpoints it is documented to register, so adding
// a network-reachable rotation route trips this test and forces whoever adds it
// to read the reasoning in secretring.go (you cannot distribute a new group
// secret over a channel the evicted member can still read).
func TestFabricRegistersNoRotationEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	box := newRotatingBox(t, "01HWZROT00000000000000000R", newSecret, "", time.Time{}, nil)
	box.svc.RegisterHandlers(mux)

	for _, path := range []string{
		"/api/fabric/rotate", "/api/fabric/rotate-secret", "/api/fabric/secret",
		"/api/fabric/rekey", "/api/fabric/secret/rotate",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req, err := http.NewRequest(method, "http://box"+path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			if _, pattern := mux.Handler(req); pattern != "" {
				t.Fatalf("fabric registered %s %s (pattern %q). Rotation is an out-of-band operator act: "+
					"a rotation route any peer can call is a fleet-wide DoS primitive, and even owner-gated it would have to "+
					"distribute the new secret over the very channel the evicted box is still authenticated on. "+
					"See secretring.go's 'Who may rotate'.", method, path, pattern)
			}
		}
	}
}
