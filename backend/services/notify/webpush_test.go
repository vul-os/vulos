package notify

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- store isolation, cap, validation ---------------------------------------

func testStores(t *testing.T) map[string]PushStore {
	t.Helper()
	sq, err := NewSQLitePushStore(filepath.Join(t.TempDir(), "subs.sqlite"))
	if err != nil {
		t.Fatalf("sqlite store: %v", err)
	}
	t.Cleanup(func() { sq.Close() })
	return map[string]PushStore{"mem": NewMemPushStore(), "sqlite": sq}
}

func sampleSub(ep string) PushSubscription {
	return PushSubscription{Endpoint: ep, P256DH: "pkey", Auth: "asalt"}
}

func TestPushStore_PerOwnerIsolation(t *testing.T) {
	for name, st := range testStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := st.Save("alice", sampleSub("https://push.example/a")); err != nil {
				t.Fatal(err)
			}
			if err := st.Save("bob", sampleSub("https://push.example/b")); err != nil {
				t.Fatal(err)
			}
			a, _ := st.List("alice")
			if len(a) != 1 || a[0].Endpoint != "https://push.example/a" {
				t.Fatalf("alice sees wrong subs: %+v", a)
			}
			if a[0].OwnerID != "alice" {
				t.Fatalf("owner not stamped: %q", a[0].OwnerID)
			}
			// bob deleting alice's endpoint string must NOT touch alice's row.
			if err := st.Delete("bob", "https://push.example/a"); err != nil {
				t.Fatal(err)
			}
			if a2, _ := st.List("alice"); len(a2) != 1 {
				t.Fatalf("cross-owner delete leaked: alice now has %d", len(a2))
			}
		})
	}
}

func TestPushStore_CapEvictsOldest(t *testing.T) {
	for name, st := range testStores(t) {
		t.Run(name, func(t *testing.T) {
			base := time.Now().Add(-time.Hour)
			for i := 0; i < MaxSubsPerUser; i++ {
				s := sampleSub("https://push.example/" + string(rune('a'+i)))
				s.CreatedAt = base.Add(time.Duration(i) * time.Minute)
				if err := st.Save("u", s); err != nil {
					t.Fatal(err)
				}
			}
			// One more (newer) → the oldest ("a") is evicted, count stays capped.
			extra := sampleSub("https://push.example/NEW")
			extra.CreatedAt = time.Now()
			if err := st.Save("u", extra); err != nil {
				t.Fatal(err)
			}
			subs, _ := st.List("u")
			if len(subs) != MaxSubsPerUser {
				t.Fatalf("cap not enforced: %d subs", len(subs))
			}
			for _, s := range subs {
				if s.Endpoint == "https://push.example/a" {
					t.Fatalf("oldest not evicted")
				}
			}
		})
	}
}

func TestPushStore_UpsertSameEndpoint(t *testing.T) {
	for name, st := range testStores(t) {
		t.Run(name, func(t *testing.T) {
			s := sampleSub("https://push.example/x")
			if err := st.Save("u", s); err != nil {
				t.Fatal(err)
			}
			s.P256DH = "rotated"
			if err := st.Save("u", s); err != nil {
				t.Fatal(err)
			}
			subs, _ := st.List("u")
			if len(subs) != 1 || subs[0].P256DH != "rotated" {
				t.Fatalf("upsert failed: %+v", subs)
			}
		})
	}
}

func TestValidateSubscription(t *testing.T) {
	ok := PushSubscription{Endpoint: "https://push.example/x", P256DH: "p", Auth: "a"}
	if err := ValidateSubscription(ok); err != nil {
		t.Fatalf("valid rejected: %v", err)
	}
	cases := []PushSubscription{
		{Endpoint: "", P256DH: "p", Auth: "a"},
		{Endpoint: "https://x/y", P256DH: "", Auth: "a"},
		{Endpoint: "http://push.example/x", P256DH: "p", Auth: "a"}, // not https
		{Endpoint: "https://" + strings.Repeat("x", maxEndpointLen), P256DH: "p", Auth: "a"},
		{Endpoint: "https://x/y", P256DH: strings.Repeat("k", 300), Auth: "a"},
	}
	for i, c := range cases {
		if err := ValidateSubscription(c); err == nil {
			t.Fatalf("case %d: invalid sub accepted", i)
		}
	}
}

// ---- send pump: fake sender, prune-on-gone, owner-targeting, DND -------------

type fakeSender struct {
	mu      sync.Mutex
	sent    []PushSubscription
	code    int
	err     error
	payload []byte
}

func (f *fakeSender) send(sub PushSubscription, payload []byte, cfg PushConfig) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sub)
	f.payload = payload
	return f.code, f.err
}

func (f *fakeSender) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sent) }

func enabledCfg() PushConfig {
	return PushConfig{VAPIDPublic: "pub", VAPIDPrivate: "priv", VAPIDSubject: "mailto:a@b", TTL: 300}
}

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
	store := NewMemPushStore()
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
	var pp PushPayload
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
	store := NewMemPushStore()
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
	store := NewMemPushStore()
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
	store := NewMemPushStore()
	_ = store.Save("alice", sampleSub("https://push.example/gone"))
	fs := &fakeSender{code: http.StatusGone}
	pb := &pushBinding{store: store, cfg: enabledCfg(), sender: fs}

	pb.pump("alice", PushPayload{Title: "x"})

	subs, _ := store.List("alice")
	if len(subs) != 0 {
		t.Fatalf("gone subscription not pruned: %+v", subs)
	}
}

func TestSetPush_DisabledConfigIsInert(t *testing.T) {
	svc := New()
	store := NewMemPushStore()
	_ = store.Save("alice", sampleSub("https://push.example/alice"))
	// cfg.Enabled() == false (no keys) → SetPush leaves push nil.
	svc.SetPush(store, PushConfig{}, nil)
	if svc.pushEnabled() {
		t.Fatalf("push should be inert with no VAPID keys")
	}
	svc.SendNotification(Notification{Title: "hi", UserID: "alice", Priority: PriorityHigh})
	// Nothing to assert on the sender; the point is it does not panic and stays off.
}

// ---- VAPID resolve -----------------------------------------------------------

func TestResolvePushVAPID_GeneratesAndPersists(t *testing.T) {
	kf := filepath.Join(t.TempDir(), "vapid.json")
	c := PushConfig{VAPIDKeyFile: kf}
	if err := ResolvePushVAPID(&c); err != nil {
		t.Fatal(err)
	}
	if !c.Enabled() {
		t.Fatalf("keys not generated")
	}
	// File must be 0600 (private key is a secret).
	info, err := os.Stat(kf)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("key file perms = %v, want 0600", info.Mode().Perm())
	}
	// Second resolve loads the SAME pair (persistence).
	pub := c.VAPIDPublic
	c2 := PushConfig{VAPIDKeyFile: kf}
	if err := ResolvePushVAPID(&c2); err != nil {
		t.Fatal(err)
	}
	if c2.VAPIDPublic != pub {
		t.Fatalf("key not persisted: %q != %q", c2.VAPIDPublic, pub)
	}
}

func TestResolvePushVAPID_NoFileStaysOff(t *testing.T) {
	c := PushConfig{}
	if err := ResolvePushVAPID(&c); err != nil {
		t.Fatal(err)
	}
	if c.Enabled() {
		t.Fatalf("push should stay off with no keys and no key file")
	}
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
