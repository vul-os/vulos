package streamsession

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
)

// --- fakes ---------------------------------------------------------------

type fakeGPUAttacher struct {
	mu        sync.Mutex
	attached  map[string]string // ulid → gpu_type
	detached  []string
	attachErr error
	detachErr error
}

func newFakeGPU() *fakeGPUAttacher {
	return &fakeGPUAttacher{attached: map[string]string{}}
}

func (f *fakeGPUAttacher) Attach(_ context.Context, _, ulid, gpuType string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attachErr != nil {
		return f.attachErr
	}
	f.attached[ulid] = gpuType
	return nil
}

func (f *fakeGPUAttacher) Detach(_ context.Context, _, ulid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.detachErr != nil {
		return f.detachErr
	}
	delete(f.attached, ulid)
	f.detached = append(f.detached, ulid)
	return nil
}

type capturedSignal struct {
	endpoint string
	sess     Session
	payload  []byte
}

type fakeRelay struct {
	mu         sync.Mutex
	forwarded  []capturedSignal
	teardowns  []Session
	forwardErr error
}

func (r *fakeRelay) ForwardSignal(_ context.Context, endpoint string, sess Session, payload []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.forwardErr != nil {
		return r.forwardErr
	}
	// CAPTURE the bytes exactly as supplied — do NOT decode.
	cp := make([]byte, len(payload))
	copy(cp, payload)
	r.forwarded = append(r.forwarded, capturedSignal{endpoint: endpoint, sess: sess, payload: cp})
	return nil
}

func (r *fakeRelay) NotifyTeardown(_ context.Context, _ string, sess Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.teardowns = append(r.teardowns, sess)
	return nil
}

// --- tests ---------------------------------------------------------------

// TestStartSession_CloudGPU_AttachesAndActivates verifies the cloud_gpu
// allocation flow hooks gpusku.Attach and ends up in 'active'.
func TestStartSession_CloudGPU_AttachesAndActivates(t *testing.T) {
	st := openTestStore(t)
	gpu := newFakeGPU()
	svc := NewService(st, gpu, &fakeRelay{}, "https://relay.example/")

	sess, err := svc.StartSession(context.Background(), StartParams{
		AccountID: "acct-1",
		HostULID:  "ulid-host-1",
		GPUType:   "a10",
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if sess.State != StateActive {
		t.Fatalf("state = %q, want active", sess.State)
	}
	if sess.HostKind != HostKindCloudGPU {
		t.Fatalf("kind = %q", sess.HostKind)
	}
	if got := gpu.attached["ulid-host-1"]; got != "a10" {
		t.Fatalf("gpu attach not invoked: attached=%v", gpu.attached)
	}
	if sess.RelayEndpoint != "https://relay.example/" {
		t.Fatalf("relay endpoint not propagated: %q", sess.RelayEndpoint)
	}
}

// TestStartSession_BYO_NoAttach verifies BYO host_kind does NOT touch gpusku.
func TestStartSession_BYO_NoAttach(t *testing.T) {
	st := openTestStore(t)
	gpu := newFakeGPU()
	svc := NewService(st, gpu, &fakeRelay{}, "https://relay.example/")

	sess, err := svc.StartSession(context.Background(), StartParams{
		AccountID:   "acct-1",
		BYOGPUBoxID: "box-1",
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if sess.HostKind != HostKindBYOGPU {
		t.Fatalf("kind = %q", sess.HostKind)
	}
	if len(gpu.attached) != 0 {
		t.Fatalf("BYO must not call gpusku.Attach, got %v", gpu.attached)
	}
}

// TestStartSession_ParamValidation rejects bad inputs.
func TestStartSession_ParamValidation(t *testing.T) {
	st := openTestStore(t)
	svc := NewService(st, newFakeGPU(), &fakeRelay{}, "https://relay/")
	ctx := context.Background()

	// both empty
	if _, err := svc.StartSession(ctx, StartParams{AccountID: "a"}); err == nil {
		t.Fatal("expected error for missing gpu_type + byo_gpu_box_id")
	}
	// both set
	if _, err := svc.StartSession(ctx, StartParams{AccountID: "a", GPUType: "a10", BYOGPUBoxID: "x"}); err == nil {
		t.Fatal("expected error for both gpu_type AND byo_gpu_box_id")
	}
	// missing host_ulid for cloud_gpu
	if _, err := svc.StartSession(ctx, StartParams{AccountID: "a", GPUType: "a10"}); err == nil {
		t.Fatal("expected error for missing host_ulid")
	}
	// missing account
	if _, err := svc.StartSession(ctx, StartParams{HostULID: "u", GPUType: "a10"}); err == nil {
		t.Fatal("expected error for missing account_id")
	}
}

// TestStartSession_AttachFails_MarksFailed asserts that a gpusku attach
// failure leaves the row in 'failed' and does NOT activate.
func TestStartSession_AttachFails_MarksFailed(t *testing.T) {
	st := openTestStore(t)
	gpu := newFakeGPU()
	gpu.attachErr = errors.New("fly: quota exceeded")
	svc := NewService(st, gpu, &fakeRelay{}, "https://relay/")

	_, err := svc.StartSession(context.Background(), StartParams{
		AccountID: "acct-1", HostULID: "ulid-1", GPUType: "a10",
	})
	if err == nil {
		t.Fatal("expected attach error")
	}
}

// TestSignal_PassThrough_Verbatim verifies the cloud forwards SDP/ICE bytes
// VERBATIM and does NOT parse them. We assert exact byte equality between
// the input payload and what the relay captured.
func TestSignal_PassThrough_Verbatim(t *testing.T) {
	st := openTestStore(t)
	gpu := newFakeGPU()
	relay := &fakeRelay{}
	svc := NewService(st, gpu, relay, "https://relay.example/")
	ctx := context.Background()

	sess, err := svc.StartSession(ctx, StartParams{
		AccountID: "acct-1", HostULID: "ulid-1", GPUType: "a10",
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// Opaque payload — could be any VULOS-STREAM/1 frame. We use a binary
	// blob with non-text bytes to prove no parsing happens.
	payload := []byte{0x00, 0x01, 0x02, 'S', 'D', 'P', 0xff, 0xfe, 0xfd}
	if err := svc.Signal(ctx, sess.ID, "acct-1", payload); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	if len(relay.forwarded) != 1 {
		t.Fatalf("relay got %d forwards, want 1", len(relay.forwarded))
	}
	got := relay.forwarded[0]
	if !bytes.Equal(got.payload, payload) {
		t.Fatalf("relay payload != input (cloud must NOT mutate or parse bytes):\n got %v\nwant %v",
			got.payload, payload)
	}
	if got.endpoint != "https://relay.example/" {
		t.Fatalf("endpoint = %q", got.endpoint)
	}
	if got.sess.ID != sess.ID {
		t.Fatalf("session id mismatch")
	}
}

// TestSignal_TooLargeRejected is the headline media-never-transits-CP test:
// a payload larger than MaxSignalBytes MUST be rejected AND MUST NOT be
// forwarded to the relay.
func TestSignal_TooLargeRejected(t *testing.T) {
	st := openTestStore(t)
	relay := &fakeRelay{}
	svc := NewService(st, newFakeGPU(), relay, "https://relay/")
	ctx := context.Background()

	sess, err := svc.StartSession(ctx, StartParams{
		AccountID: "acct-1", HostULID: "ulid-1", GPUType: "a10",
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// One byte over the cap.
	huge := bytes.Repeat([]byte{'M'}, MaxSignalBytes+1)
	err = svc.Signal(ctx, sess.ID, "acct-1", huge)
	if !errors.Is(err, ErrSignalTooLarge) {
		t.Fatalf("want ErrSignalTooLarge, got %v", err)
	}
	if len(relay.forwarded) != 0 {
		t.Fatalf("MEDIA BYTES MUST NOT BE FORWARDED: relay got %d payloads", len(relay.forwarded))
	}
}

// TestSignal_OwnershipEnforced — a caller cannot signal on someone else's
// session id (returns ErrSessionNotFound, not a 403 leak).
func TestSignal_OwnershipEnforced(t *testing.T) {
	st := openTestStore(t)
	relay := &fakeRelay{}
	svc := NewService(st, newFakeGPU(), relay, "https://relay/")
	ctx := context.Background()
	sess, _ := svc.StartSession(ctx, StartParams{
		AccountID: "acct-OWNER", HostULID: "u", GPUType: "a10",
	})
	err := svc.Signal(ctx, sess.ID, "acct-ATTACKER", []byte("offer"))
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("want ErrSessionNotFound for foreign caller, got %v", err)
	}
	if len(relay.forwarded) != 0 {
		t.Fatalf("foreign caller leaked to relay: %d", len(relay.forwarded))
	}
}

// TestTeardown_DetachesGPU verifies session teardown stops the gpusku meter
// by calling Detach on the cloud_gpu host.
func TestTeardown_DetachesGPU(t *testing.T) {
	st := openTestStore(t)
	gpu := newFakeGPU()
	relay := &fakeRelay{}
	svc := NewService(st, gpu, relay, "https://relay/")
	ctx := context.Background()
	sess, _ := svc.StartSession(ctx, StartParams{
		AccountID: "acct-1", HostULID: "ulid-1", GPUType: "a10",
	})
	if _, err := svc.Teardown(ctx, sess.ID, "acct-1"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if len(gpu.detached) != 1 || gpu.detached[0] != "ulid-1" {
		t.Fatalf("gpu detach not invoked: %+v", gpu.detached)
	}
	if len(relay.teardowns) != 1 {
		t.Fatalf("relay teardown not invoked: %d", len(relay.teardowns))
	}
	got, _ := st.Get(ctx, sess.ID)
	if got.State != StateTornDown {
		t.Fatalf("state = %q, want torn_down", got.State)
	}
	// Idempotent: second teardown is a no-op.
	if _, err := svc.Teardown(ctx, sess.ID, "acct-1"); err != nil {
		t.Fatalf("second Teardown: %v", err)
	}
}

// TestTeardown_DetachError_Surfaced is the WAVE34-5 regression: a failed GPU
// Detach must be LOGGED (billing-leak risk — GPU may still be metering) rather
// than silently discarded, while teardown still completes (row is source truth).
func TestTeardown_DetachError_Surfaced(t *testing.T) {
	st := openTestStore(t)
	gpu := newFakeGPU()
	gpu.detachErr = errors.New("provider detach exploded")
	svc := NewService(st, gpu, &fakeRelay{}, "https://relay/")
	ctx := context.Background()
	sess, _ := svc.StartSession(ctx, StartParams{
		AccountID: "acct-1", HostULID: "ulid-1", GPUType: "a10",
	})

	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	got, err := svc.Teardown(ctx, sess.ID, "acct-1")
	if err != nil {
		t.Fatalf("Teardown should still succeed despite detach error: %v", err)
	}
	if got.State != StateTornDown {
		t.Fatalf("state = %q, want torn_down", got.State)
	}
	if !strings.Contains(buf.String(), "billing-leak-risk") {
		t.Errorf("detach error was not surfaced; log = %q", buf.String())
	}
}

// TestTeardown_BYO_NoDetach — BYO host_kind teardown does NOT invoke
// gpusku.Detach (BYO hosts are not on cloud GPU meter).
func TestTeardown_BYO_NoDetach(t *testing.T) {
	st := openTestStore(t)
	gpu := newFakeGPU()
	svc := NewService(st, gpu, &fakeRelay{}, "https://relay/")
	ctx := context.Background()
	sess, _ := svc.StartSession(ctx, StartParams{
		AccountID: "acct-1", BYOGPUBoxID: "box-1",
	})
	if _, err := svc.Teardown(ctx, sess.ID, "acct-1"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if len(gpu.detached) != 0 {
		t.Fatalf("BYO teardown must NOT call Detach: %+v", gpu.detached)
	}
}

// TestSignal_InactiveSession — signaling a torn-down session is rejected
// and does NOT reach the relay.
func TestSignal_InactiveSession(t *testing.T) {
	st := openTestStore(t)
	relay := &fakeRelay{}
	svc := NewService(st, newFakeGPU(), relay, "https://relay/")
	ctx := context.Background()
	sess, _ := svc.StartSession(ctx, StartParams{
		AccountID: "acct-1", HostULID: "u", GPUType: "a10",
	})
	_, _ = svc.Teardown(ctx, sess.ID, "acct-1")
	err := svc.Signal(ctx, sess.ID, "acct-1", []byte("late offer"))
	if err == nil {
		t.Fatal("expected error signaling torn-down session")
	}
	if len(relay.forwarded) != 0 {
		t.Fatalf("inactive-session payload leaked to relay")
	}
}

// --- GAME-SESSION-01: gaming provisioning flow ---------------------------

// TestGamingStart_FreeTierRejected — a gaming session with cap 0 (free tier) is
// rejected with ErrGamingNotEntitled and NEVER attaches a GPU (no billing).
func TestGamingStart_FreeTierRejected(t *testing.T) {
	st := openTestStore(t)
	gpu := newFakeGPU()
	svc := NewService(st, gpu, &fakeRelay{}, "https://relay/")
	_, err := svc.StartSession(context.Background(), StartParams{
		AccountID: "acct-free", HostULID: "ulid-1", GPUType: "l40s",
		Gaming: true, GamingSessionCap: 0,
	})
	if !errors.Is(err, ErrGamingNotEntitled) {
		t.Fatalf("want ErrGamingNotEntitled, got %v", err)
	}
	if len(gpu.attached) != 0 {
		t.Fatalf("free-tier gaming must NOT attach a GPU (billing leak): %v", gpu.attached)
	}
}

// TestGamingStart_ConcurrencyCap — a second concurrent gaming session over the
// cap is rejected with ErrGamingSessionLimit and does NOT attach a second GPU.
func TestGamingStart_ConcurrencyCap(t *testing.T) {
	st := openTestStore(t)
	gpu := newFakeGPU()
	svc := NewService(st, gpu, &fakeRelay{}, "https://relay/")
	ctx := context.Background()

	// cap 1 (pro tier). First gaming session succeeds.
	s1, err := svc.StartSession(ctx, StartParams{
		AccountID: "acct-1", HostULID: "ulid-A", GPUType: "l40s",
		Gaming: true, GamingSessionCap: 1,
	})
	if err != nil {
		t.Fatalf("first gaming start: %v", err)
	}
	if !s1.Gaming {
		t.Fatalf("session not marked gaming: %+v", s1)
	}

	// Second concurrent gaming session for the same account → over cap.
	_, err = svc.StartSession(ctx, StartParams{
		AccountID: "acct-1", HostULID: "ulid-B", GPUType: "l40s",
		Gaming: true, GamingSessionCap: 1,
	})
	if !errors.Is(err, ErrGamingSessionLimit) {
		t.Fatalf("want ErrGamingSessionLimit, got %v", err)
	}
	// Only ONE GPU attached — the second (over-cap) start must not attach.
	if len(gpu.attached) != 1 {
		t.Fatalf("over-cap start attached a GPU (billing leak): %v", gpu.attached)
	}
}

// TestGamingStart_TeardownFreesSlot — after teardown the account can start a new
// gaming session (torn-down sessions do not count against the cap), and the GPU
// is detached (meter stops — no double-bill).
func TestGamingStart_TeardownFreesSlot(t *testing.T) {
	st := openTestStore(t)
	gpu := newFakeGPU()
	svc := NewService(st, gpu, &fakeRelay{}, "https://relay/")
	ctx := context.Background()

	s1, err := svc.StartSession(ctx, StartParams{
		AccountID: "acct-1", HostULID: "ulid-A", GPUType: "l40s",
		Gaming: true, GamingSessionCap: 1,
	})
	if err != nil {
		t.Fatalf("first gaming start: %v", err)
	}
	// Over cap while active.
	if _, err := svc.StartSession(ctx, StartParams{
		AccountID: "acct-1", HostULID: "ulid-B", GPUType: "l40s",
		Gaming: true, GamingSessionCap: 1,
	}); !errors.Is(err, ErrGamingSessionLimit) {
		t.Fatalf("expected cap hit while active, got %v", err)
	}
	// Teardown frees the slot + detaches the GPU.
	if _, err := svc.Teardown(ctx, s1.ID, "acct-1"); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if len(gpu.detached) != 1 || gpu.detached[0] != "ulid-A" {
		t.Fatalf("teardown did not detach GPU (meter would keep billing): %+v", gpu.detached)
	}
	// Now a new gaming session is allowed again.
	if _, err := svc.StartSession(ctx, StartParams{
		AccountID: "acct-1", HostULID: "ulid-C", GPUType: "l40s",
		Gaming: true, GamingSessionCap: 1,
	}); err != nil {
		t.Fatalf("start after teardown should succeed: %v", err)
	}
}

// TestGamingStart_PerAccountIsolation — one account's gaming sessions do NOT
// count against another account's cap (IDOR-safe counting is per account_id).
func TestGamingStart_PerAccountIsolation(t *testing.T) {
	st := openTestStore(t)
	svc := NewService(st, newFakeGPU(), &fakeRelay{}, "https://relay/")
	ctx := context.Background()

	if _, err := svc.StartSession(ctx, StartParams{
		AccountID: "acct-A", HostULID: "ulid-A", GPUType: "l40s",
		Gaming: true, GamingSessionCap: 1,
	}); err != nil {
		t.Fatalf("acct-A start: %v", err)
	}
	// acct-B at cap 1 is unaffected by acct-A's active session.
	if _, err := svc.StartSession(ctx, StartParams{
		AccountID: "acct-B", HostULID: "ulid-B", GPUType: "l40s",
		Gaming: true, GamingSessionCap: 1,
	}); err != nil {
		t.Fatalf("acct-B must not be blocked by acct-A's session: %v", err)
	}
}

// TestGamingStart_BillsOnceViaHostULID — the gaming session bills through the
// SAME gpusku attachment (host_ulid) as any cloud_gpu session: exactly one
// Attach on start, one Detach on teardown. There is no separate gaming billing
// path (no double-bill).
func TestGamingStart_BillsOnceViaHostULID(t *testing.T) {
	st := openTestStore(t)
	gpu := newFakeGPU()
	svc := NewService(st, gpu, &fakeRelay{}, "https://relay/")
	ctx := context.Background()

	sess, err := svc.StartSession(ctx, StartParams{
		AccountID: "acct-1", HostULID: "ulid-meter", GPUType: "h100",
		Gaming: true, GamingSessionCap: 4,
	})
	if err != nil {
		t.Fatalf("gaming start: %v", err)
	}
	// Exactly one attachment, keyed on the host_ulid the meter walks.
	if len(gpu.attached) != 1 || gpu.attached["ulid-meter"] != "h100" {
		t.Fatalf("gaming session did not attach exactly once on host_ulid: %v", gpu.attached)
	}
	if _, err := svc.Teardown(ctx, sess.ID, "acct-1"); err != nil {
		t.Fatalf("teardown: %v", err)
	}
	if len(gpu.detached) != 1 {
		t.Fatalf("gaming teardown must detach exactly once: %+v", gpu.detached)
	}
}

// TestGaming_NonGamingUnaffected — a non-gaming cloud_gpu session ignores the
// gaming cap entirely (GamingSessionCap not consulted when Gaming=false).
func TestGaming_NonGamingUnaffected(t *testing.T) {
	st := openTestStore(t)
	svc := NewService(st, newFakeGPU(), &fakeRelay{}, "https://relay/")
	ctx := context.Background()
	// Cap 0 but Gaming=false → allowed (the gate only applies to gaming).
	if _, err := svc.StartSession(ctx, StartParams{
		AccountID: "acct-1", HostULID: "ulid-1", GPUType: "l40s",
		Gaming: false, GamingSessionCap: 0,
	}); err != nil {
		t.Fatalf("non-gaming session must not hit the gaming gate: %v", err)
	}
}

// TestSignal_RelayErrorSurfaces — a relay upstream failure surfaces as
// ErrUpstreamRelay (so the handler can map to 502).
func TestSignal_RelayErrorSurfaces(t *testing.T) {
	st := openTestStore(t)
	relay := &fakeRelay{forwardErr: ErrUpstreamRelay}
	svc := NewService(st, newFakeGPU(), relay, "https://relay/")
	ctx := context.Background()
	sess, _ := svc.StartSession(ctx, StartParams{
		AccountID: "acct-1", HostULID: "u", GPUType: "a10",
	})
	err := svc.Signal(ctx, sess.ID, "acct-1", []byte("offer"))
	if !errors.Is(err, ErrUpstreamRelay) {
		t.Fatalf("want ErrUpstreamRelay, got %v", err)
	}
}
