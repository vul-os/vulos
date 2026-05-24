package multiinstance_test

// MINST-06 tests: notification fan-out, dedup, P2/P3 batching.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vulos/backend/internal/multiinstance"
)

// jsonReader wraps a byte slice as an io.Reader for httptest requests.
func jsonReader(b []byte) io.Reader {
	return bytes.NewReader(b)
}

// ---------------------------------------------------------------------------
// Notify: immediate (P0/P1) fan-out
// ---------------------------------------------------------------------------

func TestNotify_P0FanOutImmediately(t *testing.T) {
	reg := openTempRegistry(t)

	// Add one online instance that the notification should be sent to.
	ulid := "01HWZNOTIF0000000000000001"
	if err := reg.Upsert(multiinstance.Instance{
		ULID:        ulid,
		Kind:        multiinstance.KindCloud,
		Status:      multiinstance.StatusOnline,
		EndpointURL: "", // EndpointURL empty — relay base URL used
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Stub relay that counts incoming POSTs.
	var mu sync.Mutex
	var received int
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()
	t.Setenv("VULOS_RELAY_URL", relay.URL)

	// Instance needs a non-empty endpoint to be included in fan-out.
	// Re-upsert with endpoint set.
	if err := reg.Upsert(multiinstance.Instance{
		ULID:        ulid,
		Kind:        multiinstance.KindCloud,
		Status:      multiinstance.StatusOnline,
		EndpointURL: "https://instance.vulos.net",
	}); err != nil {
		t.Fatalf("Upsert with endpoint: %v", err)
	}

	nf := multiinstance.NewNotifyFanout(reg)
	n := multiinstance.Notification{
		ID:       "notif-p0-001",
		Title:    "Test",
		Priority: 0,
	}
	if err := nf.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	mu.Lock()
	count := received
	mu.Unlock()
	if count < 1 {
		t.Errorf("expected relay to receive at least 1 POST, got %d", count)
	}
}

func TestNotify_P2BufferedNotImmediatelySent(t *testing.T) {
	reg := openTempRegistry(t)
	if err := reg.Upsert(multiinstance.Instance{
		ULID:        "01HWZNOTIF0000000000000002",
		Kind:        multiinstance.KindCloud,
		Status:      multiinstance.StatusOnline,
		EndpointURL: "https://instance.vulos.net",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	var mu sync.Mutex
	var received int
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()
	t.Setenv("VULOS_RELAY_URL", relay.URL)

	nf := multiinstance.NewNotifyFanout(reg)
	n := multiinstance.Notification{
		ID:       "notif-p2-001",
		Title:    "Low priority",
		Priority: 2,
	}
	if err := nf.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify P2: %v", err)
	}

	// P2 notification should NOT have been sent yet (buffered).
	mu.Lock()
	count := received
	mu.Unlock()
	if count != 0 {
		t.Errorf("P2 notification was sent immediately; expected buffered (count=%d)", count)
	}
}

// ---------------------------------------------------------------------------
// Dedup via seen_notifications
// ---------------------------------------------------------------------------

func TestReceiveNotification_DedupsAlreadySeen(t *testing.T) {
	reg := openTempRegistry(t)
	nf := multiinstance.NewNotifyFanout(reg)

	n := multiinstance.Notification{
		ID:    "notif-dedup-001",
		Title: "Hello",
	}

	// First receive — should be delivered.
	delivered, ok := nf.ReceiveNotification(n)
	if !ok {
		t.Fatal("ReceiveNotification: expected first delivery to return true")
	}
	if delivered.ID != n.ID {
		t.Errorf("delivered.ID: got %q want %q", delivered.ID, n.ID)
	}

	// Second receive of same ID — should be dropped.
	_, ok2 := nf.ReceiveNotification(n)
	if ok2 {
		t.Fatal("ReceiveNotification: expected duplicate to return false")
	}
}

func TestReceiveNotification_EmptyIDDropped(t *testing.T) {
	reg := openTempRegistry(t)
	nf := multiinstance.NewNotifyFanout(reg)

	_, ok := nf.ReceiveNotification(multiinstance.Notification{ID: ""})
	if ok {
		t.Fatal("ReceiveNotification with empty ID should return false")
	}
}

// ---------------------------------------------------------------------------
// Notify dedup: markSeen prevents re-sending
// ---------------------------------------------------------------------------

func TestNotify_P0AlreadySeenNotSentAgain(t *testing.T) {
	reg := openTempRegistry(t)
	if err := reg.Upsert(multiinstance.Instance{
		ULID:        "01HWZNOTIF0000000000000010",
		Kind:        multiinstance.KindCloud,
		Status:      multiinstance.StatusOnline,
		EndpointURL: "https://instance.vulos.net",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	var mu sync.Mutex
	var received int
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()
	t.Setenv("VULOS_RELAY_URL", relay.URL)

	nf := multiinstance.NewNotifyFanout(reg)
	n := multiinstance.Notification{
		ID:       "notif-dedup-send-001",
		Title:    "Test",
		Priority: 0,
	}

	// First send.
	if err := nf.Notify(context.Background(), n); err != nil {
		t.Fatalf("first Notify: %v", err)
	}
	mu.Lock()
	firstCount := received
	mu.Unlock()

	// Second send with same ID — should be a no-op (already seen).
	if err := nf.Notify(context.Background(), n); err != nil {
		t.Fatalf("second Notify: %v", err)
	}
	mu.Lock()
	secondCount := received
	mu.Unlock()

	// Relay received count should not have increased on the second send.
	if secondCount > firstCount {
		t.Errorf("relay received %d calls; expected no increase on duplicate notify", secondCount)
	}
}

// ---------------------------------------------------------------------------
// P2/P3 batching: flushBatch deduplicates
// ---------------------------------------------------------------------------

func TestRunBatcher_FlushesAndDeduplicates(t *testing.T) {
	reg := openTempRegistry(t)
	if err := reg.Upsert(multiinstance.Instance{
		ULID:        "01HWZNOTIF0000000000000020",
		Kind:        multiinstance.KindCloud,
		Status:      multiinstance.StatusOnline,
		EndpointURL: "https://instance.vulos.net",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	var mu sync.Mutex
	ids := make(map[string]int)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env struct {
			Notification multiinstance.Notification `json:"notification"`
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &env); err == nil {
			mu.Lock()
			ids[env.Notification.ID]++
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()
	t.Setenv("VULOS_RELAY_URL", relay.URL)

	nf := multiinstance.NewNotifyFanout(reg)

	// Queue two notifications: one duplicate.
	_ = nf.Notify(context.Background(), multiinstance.Notification{ID: "batch-001", Priority: 2})
	_ = nf.Notify(context.Background(), multiinstance.Notification{ID: "batch-002", Priority: 3})
	_ = nf.Notify(context.Background(), multiinstance.Notification{ID: "batch-001", Priority: 2}) // duplicate

	// Flush the batch directly (avoids waiting for the 30-second tick in tests).
	nf.FlushBatch(context.Background())

	mu.Lock()
	defer mu.Unlock()

	// batch-001 should appear at most once (dedup in batch).
	if ids["batch-001"] > 1 {
		t.Errorf("batch-001 sent %d times; want ≤1 (dedup)", ids["batch-001"])
	}
	// batch-002 should appear exactly once.
	if ids["batch-002"] != 1 {
		t.Errorf("batch-002 sent %d times; want 1", ids["batch-002"])
	}
}

// ---------------------------------------------------------------------------
// Offline instance skipped
// ---------------------------------------------------------------------------

func TestNotify_OfflineInstanceSkipped(t *testing.T) {
	reg := openTempRegistry(t)
	if err := reg.Upsert(multiinstance.Instance{
		ULID:        "01HWZNOTIF0000000000000030",
		Kind:        multiinstance.KindCloud,
		Status:      multiinstance.StatusOffline, // offline → skip
		EndpointURL: "https://instance.vulos.net",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	var mu sync.Mutex
	var received int
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()
	t.Setenv("VULOS_RELAY_URL", relay.URL)

	nf := multiinstance.NewNotifyFanout(reg)
	if err := nf.Notify(context.Background(), multiinstance.Notification{
		ID:       "notif-offline-001",
		Priority: 0,
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	mu.Lock()
	count := received
	mu.Unlock()
	if count != 0 {
		t.Errorf("expected 0 relay calls for offline instance, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Bounded-concurrency fan-out (audit P1-6)
// ---------------------------------------------------------------------------

// TestFanOut_ConcurrentNotSerial proves the fan-out is concurrent, not a serial
// blocking loop: with many online instances each backed by a relay that sleeps,
// the total wall-clock time is far below N×delay. A serial loop would take
// roughly N×delay; a bounded pool finishes in about ceil(N/concurrency)×delay.
func TestFanOut_ConcurrentNotSerial(t *testing.T) {
	reg := openTempRegistry(t)

	const numInstances = 24
	for i := 0; i < numInstances; i++ {
		if err := reg.Upsert(multiinstance.Instance{
			ULID:        fmt.Sprintf("01HWZFANOUT%015d", i),
			Kind:        multiinstance.KindCloud,
			Status:      multiinstance.StatusOnline,
			EndpointURL: "https://instance.vulos.net",
		}); err != nil {
			t.Fatalf("Upsert %d: %v", i, err)
		}
	}

	const perCallDelay = 100 * time.Millisecond
	var inFlight, maxInFlight int32
	var mu sync.Mutex
	var received int
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if cur <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, cur) {
				break
			}
		}
		time.Sleep(perCallDelay)
		atomic.AddInt32(&inFlight, -1)
		mu.Lock()
		received++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()
	t.Setenv("VULOS_RELAY_URL", relay.URL)

	nf := multiinstance.NewNotifyFanout(reg)

	start := time.Now()
	if err := nf.Notify(context.Background(), multiinstance.Notification{
		ID:       "fanout-concurrency-001",
		Priority: 0, // immediate fan-out
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	elapsed := time.Since(start)

	mu.Lock()
	gotReceived := received
	mu.Unlock()
	if gotReceived != numInstances {
		t.Errorf("relay received %d POSTs; want %d", gotReceived, numInstances)
	}

	// A fully serial loop would take >= numInstances * perCallDelay (~2.4s).
	// With bounded concurrency it must finish much faster.
	serialBound := time.Duration(numInstances) * perCallDelay
	if elapsed >= serialBound {
		t.Errorf("fan-out took %s (>= serial bound %s) — not concurrent", elapsed, serialBound)
	}

	// And it must actually have run multiple POSTs in parallel.
	if peak := atomic.LoadInt32(&maxInFlight); peak < 2 {
		t.Errorf("peak in-flight = %d; expected concurrent dispatch (>1)", peak)
	}
}

// TestFanOut_ContextCancelStopsScheduling proves a cancelled context aborts
// further scheduling promptly instead of marching through every peer.
func TestFanOut_ContextCancelStopsScheduling(t *testing.T) {
	reg := openTempRegistry(t)
	for i := 0; i < 8; i++ {
		if err := reg.Upsert(multiinstance.Instance{
			ULID:        fmt.Sprintf("01HWZFANCAN%015d", i),
			Kind:        multiinstance.KindCloud,
			Status:      multiinstance.StatusOnline,
			EndpointURL: "https://instance.vulos.net",
		}); err != nil {
			t.Fatalf("Upsert %d: %v", i, err)
		}
	}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer relay.Close()
	t.Setenv("VULOS_RELAY_URL", relay.URL)

	nf := multiinstance.NewNotifyFanout(reg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	// Should return promptly without panicking; error is best-effort.
	done := make(chan struct{})
	go func() {
		_ = nf.Notify(ctx, multiinstance.Notification{ID: "fanout-cancel-001", Priority: 0})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("fan-out did not return promptly after context cancellation")
	}
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

func TestRegisterNotifyHandlers_FanoutEndpoint(t *testing.T) {
	reg := openTempRegistry(t)

	// No online instances — relay won't be called; test just the HTTP layer.
	nf := multiinstance.NewNotifyFanout(reg)
	mux := http.NewServeMux()
	multiinstance.RegisterNotifyHandlers(mux, nf)

	body, _ := json.Marshal(multiinstance.Notification{
		ID:       "http-fanout-001",
		Title:    "Hello",
		Priority: 0,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/notify/fanout", jsonReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body: %s", w.Code, w.Body.String())
	}
}

func TestRegisterNotifyHandlers_FanoutMissingIDReturns422(t *testing.T) {
	reg := openTempRegistry(t)
	nf := multiinstance.NewNotifyFanout(reg)
	mux := http.NewServeMux()
	multiinstance.RegisterNotifyHandlers(mux, nf)

	body, _ := json.Marshal(map[string]string{"title": "no id"})
	req := httptest.NewRequest(http.MethodPost, "/api/notify/fanout", jsonReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status: got %d want 422", w.Code)
	}
}

func TestRegisterNotifyHandlers_ReceiveEndpointDeduplicates(t *testing.T) {
	reg := openTempRegistry(t)
	nf := multiinstance.NewNotifyFanout(reg)
	mux := http.NewServeMux()
	multiinstance.RegisterNotifyHandlers(mux, nf)

	notif := multiinstance.Notification{
		ID:    "http-receive-001",
		Title: "Inbound",
	}
	body, _ := json.Marshal(notif)

	// First receive — delivered.
	req1 := httptest.NewRequest(http.MethodPost, "/api/notify/receive", jsonReader(body))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	mux.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Fatalf("first receive status: got %d want 200", w1.Code)
	}
	var resp1 map[string]interface{}
	if err := json.NewDecoder(w1.Body).Decode(&resp1); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if resp1["status"] != "delivered" {
		t.Errorf("first receive status: got %v want delivered", resp1["status"])
	}

	// Second receive — duplicate.
	req2 := httptest.NewRequest(http.MethodPost, "/api/notify/receive", jsonReader(body))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("second receive status: got %d want 200", w2.Code)
	}
	var resp2 map[string]interface{}
	if err := json.NewDecoder(w2.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if resp2["status"] != "duplicate" {
		t.Errorf("second receive status: got %v want duplicate", resp2["status"])
	}
}

func TestRegisterNotifyHandlers_ReceiveInvalidBodyReturns400(t *testing.T) {
	reg := openTempRegistry(t)
	nf := multiinstance.NewNotifyFanout(reg)
	mux := http.NewServeMux()
	multiinstance.RegisterNotifyHandlers(mux, nf)

	req := httptest.NewRequest(http.MethodPost, "/api/notify/receive", bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", w.Code)
	}
}
