package notify

// unifiedpush_service_test.go — tests for the notify.Service ↔ unifiedpush
// package wiring (SetUnifiedPush / maybeUnifiedPush / upBinding). Tests for
// the unifiedpush transport/store/validation themselves (SSRF screening,
// store isolation/cap) live in backend/internal/unifiedpush. This mirrors
// webpush_service_test.go's coverage shape for the sibling transport.

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"vulos/backend/internal/unifiedpush"
	"vulos/backend/internal/webpush"
)

func sampleUPEndpoint(url string) unifiedpush.Endpoint {
	return unifiedpush.Endpoint{URL: url}
}

func enabledUPCfg() unifiedpush.Config {
	return unifiedpush.Config{Enabled: true}
}

// ---- send pump: fake sender, prune-on-gone, owner-targeting, DND -------------

type fakeUPSender struct {
	mu      sync.Mutex
	sent    []unifiedpush.Endpoint
	code    int
	err     error
	payload []byte
}

func (f *fakeUPSender) Send(ep unifiedpush.Endpoint, payload []byte, cfg unifiedpush.Config) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, ep)
	f.payload = payload
	return f.code, f.err
}

func (f *fakeUPSender) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) }

func waitForUP(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}

// TestSendNotification_UnifiedPushesToOwnerOnly is the "payload delivered to
// a registered endpoint" requirement: a notification targeted at alice must
// reach alice's registered UnifiedPush endpoint, carrying the content, and
// must NOT reach bob's endpoint.
func TestSendNotification_UnifiedPushesToOwnerOnly(t *testing.T) {
	svc := New()
	store := unifiedpush.NewMemStore()
	_ = store.Save("alice", sampleUPEndpoint("https://up.example/alice"))
	_ = store.Save("bob", sampleUPEndpoint("https://up.example/bob"))
	fs := &fakeUPSender{code: http.StatusCreated}
	svc.setUnifiedPushForTest(store, enabledUPCfg(), fs, nil)

	svc.SendNotification(Notification{Title: "hi", Body: "secret", UserID: "alice", Priority: PriorityHigh})

	waitForUP(t, func() bool { return fs.count() == 1 })
	if fs.sent[0].OwnerID != "alice" {
		t.Fatalf("pushed to wrong owner: %q", fs.sent[0].OwnerID)
	}
	if fs.sent[0].URL != "https://up.example/alice" {
		t.Fatalf("pushed to wrong endpoint: %q", fs.sent[0].URL)
	}
	var pp unifiedpush.Payload
	if err := json.Unmarshal(fs.payload, &pp); err != nil {
		t.Fatal(err)
	}
	if pp.Title != "hi" || pp.Body != "secret" {
		t.Fatalf("payload wrong: %+v", pp)
	}
	if strings.Contains(string(fs.payload), "alice") {
		t.Fatalf("payload leaked recipient id: %s", fs.payload)
	}
}

func TestSendNotification_UnifiedPushBoxLevelNotPushed(t *testing.T) {
	svc := New()
	store := unifiedpush.NewMemStore()
	_ = store.Save("alice", sampleUPEndpoint("https://up.example/alice"))
	fs := &fakeUPSender{code: http.StatusCreated}
	svc.setUnifiedPushForTest(store, enabledUPCfg(), fs, nil)

	// No UserID → box-level → never UnifiedPush'd, same rule as Web Push.
	svc.SendNotification(Notification{Title: "system", Priority: PriorityHigh})
	time.Sleep(50 * time.Millisecond)
	if fs.count() != 0 {
		t.Fatalf("box-level notification was pushed (%d sends)", fs.count())
	}
}

// TestSendNotification_UnifiedPushSuppressedByDND asserts UnifiedPush honours
// the SAME suppression predicate Web Push does — a registered endpoint must
// not become a way to bypass DND/prefs.
func TestSendNotification_UnifiedPushSuppressedByDND(t *testing.T) {
	svc := New()
	store := unifiedpush.NewMemStore()
	_ = store.Save("alice", sampleUPEndpoint("https://up.example/alice"))
	fs := &fakeUPSender{code: http.StatusCreated}
	// Suppress everything.
	svc.setUnifiedPushForTest(store, enabledUPCfg(), fs, func(string, Level, Priority) bool { return true })

	svc.SendNotification(Notification{Title: "hi", UserID: "alice", Priority: PriorityHigh})
	time.Sleep(50 * time.Millisecond)
	if fs.count() != 0 {
		t.Fatalf("suppressed notification was pushed")
	}
}

// TestUPPump_PrunesGoneEndpoint is the "failing endpoint is pruned after the
// same policy Web Push uses" requirement: a 410 Gone response prunes the
// endpoint, exactly as webpush's pushBinding.pump does for 404/410.
func TestUPPump_PrunesGoneEndpoint(t *testing.T) {
	store := unifiedpush.NewMemStore()
	_ = store.Save("alice", sampleUPEndpoint("https://up.example/gone"))
	fs := &fakeUPSender{code: http.StatusGone}
	pb := &upBinding{store: store, cfg: enabledUPCfg(), sender: fs}

	pb.pump("alice", unifiedpush.Payload{Title: "x"})

	eps, _ := store.List("alice")
	if len(eps) != 0 {
		t.Fatalf("gone endpoint not pruned: %+v", eps)
	}
}

// TestUPPump_KeepsEndpointOn404Only... covered by the 410 case above; 404 is
// asserted directly here too since both codes are documented as "gone".
func TestUPPump_PrunesOn404(t *testing.T) {
	store := unifiedpush.NewMemStore()
	_ = store.Save("alice", sampleUPEndpoint("https://up.example/gone404"))
	fs := &fakeUPSender{code: http.StatusNotFound}
	pb := &upBinding{store: store, cfg: enabledUPCfg(), sender: fs}

	pb.pump("alice", unifiedpush.Payload{Title: "x"})

	eps, _ := store.List("alice")
	if len(eps) != 0 {
		t.Fatalf("404 endpoint not pruned: %+v", eps)
	}
}

// TestUPPump_KeepsEndpointOnTransientError asserts a merely-failing (not
// gone) send does NOT prune the endpoint — only 404/410 do.
func TestUPPump_KeepsEndpointOnTransientError(t *testing.T) {
	store := unifiedpush.NewMemStore()
	_ = store.Save("alice", sampleUPEndpoint("https://up.example/flaky"))
	fs := &fakeUPSender{code: http.StatusServiceUnavailable}
	pb := &upBinding{store: store, cfg: enabledUPCfg(), sender: fs}

	pb.pump("alice", unifiedpush.Payload{Title: "x"})

	eps, _ := store.List("alice")
	if len(eps) != 1 {
		t.Fatalf("endpoint pruned on a transient (non-404/410) failure: %+v", eps)
	}
}

func TestSetUnifiedPush_DisabledConfigIsInert(t *testing.T) {
	svc := New()
	store := unifiedpush.NewMemStore()
	_ = store.Save("alice", sampleUPEndpoint("https://up.example/alice"))
	// cfg.Enabled == false → SetUnifiedPush leaves up nil.
	svc.SetUnifiedPush(store, unifiedpush.Config{}, nil)
	if svc.unifiedPushEnabled() {
		t.Fatalf("unifiedpush should be inert with Enabled=false")
	}
	svc.SendNotification(Notification{Title: "hi", UserID: "alice", Priority: PriorityHigh})
	// Nothing to assert on the sender; the point is it does not panic and stays off.
}

// TestSendNotification_BothTransportsFire confirms Web Push and UnifiedPush
// are genuinely ALONGSIDE each other — attaching both delivers to both, one
// notification, one owner, two independent transports.
func TestSendNotification_BothTransportsFire(t *testing.T) {
	svc := New()
	wpStore := webpush.NewMemStore()
	_ = wpStore.Save("alice", sampleSub("https://push.example/alice"))
	wpSender := &fakeSender{code: http.StatusCreated}
	svc.setPushForTest(wpStore, enabledCfg(), wpSender, nil)

	upStore := unifiedpush.NewMemStore()
	_ = upStore.Save("alice", sampleUPEndpoint("https://up.example/alice"))
	upSender := &fakeUPSender{code: http.StatusCreated}
	svc.setUnifiedPushForTest(upStore, enabledUPCfg(), upSender, nil)

	svc.SendNotification(Notification{Title: "hi", UserID: "alice", Priority: PriorityHigh})

	waitForUP(t, func() bool { return wpSender.count() == 1 && upSender.count() == 1 })
}
