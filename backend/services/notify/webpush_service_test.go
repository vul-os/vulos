package notify

// webpush_service_test.go — tests for the notify.Service ↔ webpush package
// wiring (SetPush / maybeWebPush / pushBinding). Tests for the webpush
// transport/store/validation themselves (SSRF screening, VAPID resolve, store
// isolation/cap) live in backend/internal/webpush.

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"vulos/backend/internal/webpush"
)

func sampleSub(ep string) webpush.Subscription {
	return webpush.Subscription{Endpoint: ep, P256DH: "pkey", Auth: "asalt"}
}

func enabledCfg() webpush.Config {
	return webpush.Config{VAPIDPublic: "pub", VAPIDPrivate: "priv", VAPIDSubject: "mailto:a@b", TTL: 300}
}

// ---- send pump: fake sender, prune-on-gone, owner-targeting, DND -------------

type fakeSender struct {
	mu      sync.Mutex
	sent    []webpush.Subscription
	code    int
	err     error
	payload []byte
}

func (f *fakeSender) Send(sub webpush.Subscription, payload []byte, cfg webpush.Config) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sub)
	f.payload = payload
	return f.code, f.err
}

func (f *fakeSender) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) }

// waitFor polls until cond() or the deadline; the push pump runs async.
func waitFor(t *testing.T, cond func() bool) {
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

func TestSendNotification_PushesToOwnerOnly(t *testing.T) {
	svc := New()
	store := webpush.NewMemStore()
	_ = store.Save("alice", sampleSub("https://push.example/alice"))
	_ = store.Save("bob", sampleSub("https://push.example/bob"))
	fs := &fakeSender{code: http.StatusCreated}
	svc.setPushForTest(store, enabledCfg(), fs, nil)

	svc.SendNotification(Notification{Title: "hi", Body: "secret", UserID: "alice", Priority: PriorityHigh})

	waitFor(t, func() bool { return fs.count() == 1 })
	if fs.sent[0].OwnerID != "alice" {
		t.Fatalf("pushed to wrong owner: %q", fs.sent[0].OwnerID)
	}
	// Payload must carry the content but NOT any owner/recipient id.
	var pp webpush.Payload
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

func TestSendNotification_BoxLevelNotPushed(t *testing.T) {
	svc := New()
	store := webpush.NewMemStore()
	_ = store.Save("alice", sampleSub("https://push.example/alice"))
	fs := &fakeSender{code: http.StatusCreated}
	svc.setPushForTest(store, enabledCfg(), fs, nil)

	// No UserID → box-level → never web-pushed.
	svc.SendNotification(Notification{Title: "system", Priority: PriorityHigh})
	time.Sleep(50 * time.Millisecond)
	if fs.count() != 0 {
		t.Fatalf("box-level notification was pushed (%d sends)", fs.count())
	}
}

func TestSendNotification_SuppressedByDND(t *testing.T) {
	svc := New()
	store := webpush.NewMemStore()
	_ = store.Save("alice", sampleSub("https://push.example/alice"))
	fs := &fakeSender{code: http.StatusCreated}
	// Suppress everything.
	svc.setPushForTest(store, enabledCfg(), fs, func(string, Level, Priority) bool { return true })

	svc.SendNotification(Notification{Title: "hi", UserID: "alice", Priority: PriorityHigh})
	time.Sleep(50 * time.Millisecond)
	if fs.count() != 0 {
		t.Fatalf("suppressed notification was pushed")
	}
}

func TestPump_PrunesGoneSubscription(t *testing.T) {
	store := webpush.NewMemStore()
	_ = store.Save("alice", sampleSub("https://push.example/gone"))
	fs := &fakeSender{code: http.StatusGone}
	pb := &pushBinding{store: store, cfg: enabledCfg(), sender: fs}

	pb.pump("alice", webpush.Payload{Title: "x"})

	subs, _ := store.List("alice")
	if len(subs) != 0 {
		t.Fatalf("gone subscription not pruned: %+v", subs)
	}
}

func TestSetPush_DisabledConfigIsInert(t *testing.T) {
	svc := New()
	store := webpush.NewMemStore()
	_ = store.Save("alice", sampleSub("https://push.example/alice"))
	// cfg.Enabled() == false (no keys) → SetPush leaves push nil.
	svc.SetPush(store, webpush.Config{}, nil)
	if svc.pushEnabled() {
		t.Fatalf("push should be inert with no VAPID keys")
	}
	svc.SendNotification(Notification{Title: "hi", UserID: "alice", Priority: PriorityHigh})
	// Nothing to assert on the sender; the point is it does not panic and stays off.
}

func TestBodyString(t *testing.T) {
	if got := bodyString("hello"); got != "hello" {
		t.Fatalf("string body: %q", got)
	}
	if got := bodyString(nil); got != "" {
		t.Fatalf("nil body: %q", got)
	}
	if got := bodyString(map[string]int{"n": 1}); got != `{"n":1}` {
		t.Fatalf("struct body: %q", got)
	}
}
