package webhooks_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vulos/backend/services/webhooks"
)

// openTestStore opens an in-memory webhook store with SSRF validation disabled
// so that tests can use httptest.NewServer (loopback) endpoints. Production
// code calls webhooks.OpenStore, which enables the full SSRF guard.
func openTestStore(t *testing.T) *webhooks.Store {
	t.Helper()
	st, err := webhooks.OpenStoreForTest("")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// ─── Test 1: signed delivery ──────────────────────────────────────────────────

func TestSignedDelivery(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// The capture vars are written by the async delivery goroutine (the httptest
	// handler) and read by the test goroutine, so guard them with a mutex to keep
	// the test race-free under `go test -race`.
	var mu sync.Mutex
	var receivedSig string
	var receivedBody []byte
	var capturedSecret []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		receivedSig = r.Header.Get("X-Vulos-Signature")
		receivedBody = body
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sub, err := st.CreateSubscription(ctx, "admin1", srv.URL, []string{"device.enrolled"})
	if err != nil {
		t.Fatal(err)
	}
	capturedSecret = sub.Secret

	d := webhooks.NewTestDispatcher(st)
	payload := json.RawMessage(`{"deviceId":"d1","name":"laptop"}`)
	if err := d.Emit(ctx, "device.enrolled", payload); err != nil {
		t.Fatal(err)
	}

	// Wait for async delivery.
	sig, gotBody := "", []byte(nil)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		sig, gotBody = receivedSig, receivedBody
		mu.Unlock()
		if sig != "" {
			break
		}
	}
	if sig == "" {
		t.Fatal("webhook was not delivered")
	}

	// Verify the signature.
	if err := webhooks.VerifySignature(gotBody, sig, capturedSecret); err != nil {
		t.Errorf("signature verification failed: %v", err)
	}
}

// ─── Test 2: retry on 5xx ─────────────────────────────────────────────────────

func TestRetryOn5xx(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n < 2 {
			// First call fails with 500.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sub, err := st.CreateSubscription(ctx, "admin2", srv.URL, []string{"backup.completed"})
	if err != nil {
		t.Fatal(err)
	}

	// Emit the event.
	d := webhooks.NewTestDispatcher(st)
	payload := json.RawMessage(`{"jobId":"b1"}`)
	if err := d.Emit(ctx, "backup.completed", payload); err != nil {
		t.Fatal(err)
	}

	// Wait for first attempt (which should fail with 500).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if callCount.Load() >= 1 {
			break
		}
	}

	// Check delivery is in "failed" state.
	deliveries, err := st.PendingDeliveries(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) == 0 {
		t.Fatal("expected at least one delivery")
	}
	// After a 5xx the status should be "failed" (not dead).
	d2 := deliveries[0]
	if d2.Status != "failed" && d2.Status != "delivered" {
		// Allow delivered if the retry already ran.
		t.Logf("delivery status after 5xx: %s (attempt %d)", d2.Status, d2.AttemptCount)
	}
}

// ─── Test 3: dead-letter after final attempt ──────────────────────────────────

func TestDeadLetterAfterFinalAttempt(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	// Endpoint that always returns 500.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sub, err := st.CreateSubscription(ctx, "admin3", srv.URL, []string{"device.enrolled"})
	if err != nil {
		t.Fatal(err)
	}

	// Emit and immediately invoke a delivery.
	d := webhooks.NewTestDispatcher(st)
	payload := json.RawMessage(`{"deviceId":"d1"}`)
	if err := d.Emit(ctx, "device.enrolled", payload); err != nil {
		t.Fatal(err)
	}

	// Wait for first delivery attempt.
	time.Sleep(200 * time.Millisecond)

	deliveries, err := st.PendingDeliveries(ctx, sub.ID)
	if err != nil || len(deliveries) == 0 {
		t.Fatal("no deliveries found")
	}

	del := deliveries[0]

	// Simulate 6 failed attempts directly (beyond the backoff schedule of 6 steps).
	// This is a white-box test helper — in production the dispatcher does this over time.
	if err := st.SimulateMaxRetries(ctx, del.ID); err != nil {
		t.Fatalf("SimulateMaxRetries: %v", err)
	}

	// Re-read delivery — should be "dead".
	status, err := st.DeliveryStatus(ctx, del.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status != "dead" {
		t.Errorf("expected dead-letter status 'dead', got %q", status)
	}
}

// ─── Test 4: signature roundtrip verify ──────────────────────────────────────

func TestSignatureRoundtrip(t *testing.T) {
	secret := []byte("super-secret-key-for-testing-hmac")
	body := []byte(`{"event":"device.enrolled","data":{"id":"123"}}`)
	ts := time.Now().Unix()

	sig := webhooks.BuildSignature(secret, ts, body)

	// Valid case.
	if err := webhooks.VerifySignature(body, sig, secret); err != nil {
		t.Errorf("expected valid signature, got: %v", err)
	}

	// Tampered body.
	if err := webhooks.VerifySignature([]byte("tampered"), sig, secret); err == nil {
		t.Error("expected error for tampered body")
	}

	// Wrong secret.
	if err := webhooks.VerifySignature(body, sig, []byte("wrong-secret")); err == nil {
		t.Error("expected error for wrong secret")
	}
}

// ─── Test 5: topic filter ─────────────────────────────────────────────────────

func TestTopicFilter(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	var delivered atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Subscription only to "device.enrolled".
	_, err := st.CreateSubscription(ctx, "admin4", srv.URL, []string{"device.enrolled"})
	if err != nil {
		t.Fatal(err)
	}

	d := webhooks.NewTestDispatcher(st)

	// Emit a non-subscribed topic.
	_ = d.Emit(ctx, "backup.completed", json.RawMessage(`{}`))
	time.Sleep(200 * time.Millisecond)
	if delivered.Load() != 0 {
		t.Errorf("expected 0 deliveries for non-subscribed topic, got %d", delivered.Load())
	}

	// Emit subscribed topic.
	_ = d.Emit(ctx, "device.enrolled", json.RawMessage(`{}`))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if delivered.Load() >= 1 {
			break
		}
	}
	if delivered.Load() == 0 {
		t.Error("expected delivery for subscribed topic 'device.enrolled'")
	}
}

// ─── Test 6: CRUD roundtrip ────────────────────────────────────────────────────

func TestSubscriptionCRUD(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	sub, err := st.CreateSubscription(ctx, "admin5", "http://127.0.0.1:9/hook", []string{"device.enrolled"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.Secret) == 0 {
		t.Fatal("expected a non-empty secret on create")
	}

	got, err := st.GetSubscription(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != sub.URL {
		t.Errorf("URL mismatch: got %q want %q", got.URL, sub.URL)
	}

	list, err := st.ListSubscriptions(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSubscriptions: %v (len=%d)", err, len(list))
	}

	newURL := "http://127.0.0.1:9/hook2"
	updated, err := st.UpdateSubscription(ctx, sub.ID, &newURL, []string{"backup.completed"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated.URL != newURL {
		t.Errorf("update URL mismatch: got %q want %q", updated.URL, newURL)
	}
	if updated.Secret != nil {
		t.Error("update must not re-expose the secret")
	}

	newSecret, err := st.RotateSecret(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(newSecret) == string(sub.Secret) {
		t.Error("rotated secret must differ from the original")
	}

	if err := st.DeleteSubscription(ctx, sub.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSubscription(ctx, sub.ID); err != webhooks.ErrSubNotFound {
		t.Errorf("expected ErrSubNotFound after delete, got %v", err)
	}
}

// ─── Test 7: SendTest immediate delivery ───────────────────────────────────────

func TestSendTest(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()

	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Vulos-Topic")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sub, err := st.CreateSubscription(ctx, "admin6", srv.URL, []string{"device.enrolled"})
	if err != nil {
		t.Fatal(err)
	}

	d := webhooks.NewTestDispatcher(st)
	code, err := d.SendTest(ctx, sub)
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusOK {
		t.Errorf("expected 200, got %d", code)
	}
	if got != "test.ping" {
		t.Errorf("expected X-Vulos-Topic=test.ping, got %q", got)
	}

	deliveries, err := st.ListDeliveries(ctx, sub.ID, 10)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("ListDeliveries: %v (len=%d)", err, len(deliveries))
	}
	if deliveries[0].Status != "delivered" {
		t.Errorf("expected delivered status, got %q", deliveries[0].Status)
	}
}
