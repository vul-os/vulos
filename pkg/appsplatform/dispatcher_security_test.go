package appsplatform

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// These tests harden outbound event delivery: that every webhook POST is HMAC
// signed correctly end-to-end, that delivery to a private/metadata destination
// is refused at send time (SSRF), and that the in-memory SSE fan-out isolates
// subscribers and never blocks on a slow consumer.

// TestWebhookDeliveryIsSignedEndToEnd stands up a real receiver and asserts the
// platform's POST carries a fresh timestamp and a signature that verifies under
// the app's signing secret over the exact body received. Private destinations
// are permitted only so the loopback test server is reachable.
func TestWebhookDeliveryIsSignedEndToEnd(t *testing.T) {
	t.Setenv(AllowPrivateWebhooksEnv, "1") // allow the loopback httptest server

	type received struct {
		ts, sig string
		body    []byte
	}
	got := make(chan received, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- received{ts: r.Header.Get(SigHeaderTimestamp), sig: r.Header.Get(SigHeaderSignature), body: b}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	reg := NewMemoryRegistry()
	c, err := reg.Create(CreateParams{Name: "wh", Products: []string{ProductOffice}, WebhookURL: srv.URL})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	d := NewDispatcher(reg, ProductOffice)
	d.Emit(EventMessageCreated, map[string]any{"text": "hi"}, nil)

	select {
	case rec := <-got:
		if rec.ts == "" {
			t.Fatal("missing timestamp header")
		}
		if !strings.HasPrefix(rec.sig, "v0=") {
			t.Fatalf("signature missing v0= prefix: %q", rec.sig)
		}
		if !Verify(rec.ts, rec.body, c.SigningSecret, rec.sig) {
			t.Fatal("delivered signature does not verify under the app's secret")
		}
		// A wrong secret must NOT verify (proves the signature is secret-bound).
		if Verify(rec.ts, rec.body, "vas_wrong", rec.sig) {
			t.Fatal("signature verified under a wrong secret")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("event was never delivered")
	}
}

// TestWebhookDeliveryRefusesPrivateDestination injects an app whose webhook
// points at a loopback listener (bypassing Create's validation) and confirms
// that, with the SSRF guard ON, the platform never connects to it.
func TestWebhookDeliveryRefusesPrivateDestination(t *testing.T) {
	t.Setenv(AllowPrivateWebhooksEnv, "") // guard ON

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	hit := make(chan struct{}, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			hit <- struct{}{}
			conn.Close()
		}
	}()

	// Build an app with a private webhook directly in the registry (Create would
	// reject this URL — that is exactly the guard we are isolating here).
	reg := NewMemoryRegistry()
	reg.all["evil"] = &App{
		ID:            "evil",
		Products:      []string{ProductOffice},
		WebhookURL:    "http://" + ln.Addr().String() + "/hook",
		SigningSecret: "vas_x",
	}
	d := NewDispatcher(reg, ProductOffice)
	d.Emit(EventMessageCreated, map[string]any{"x": 1}, nil)

	select {
	case <-hit:
		t.Fatal("platform connected to a private webhook destination (SSRF)")
	case <-time.After(400 * time.Millisecond):
		// Good: the guard refused the delivery before dialing.
	}
}

// TestSSESubscriberIsolation confirms an SSE subscriber for one app never
// receives another app's events.
func TestSSESubscriberIsolation(t *testing.T) {
	reg := NewMemoryRegistry()
	a := newApp(t, reg, "o", []string{ProductOffice}, nil)
	b := newApp(t, reg, "o", []string{ProductOffice}, nil)
	d := NewDispatcher(reg, ProductOffice)

	chA, unA := d.Subscribe(a.App.ID)
	defer unA()
	chB, unB := d.Subscribe(b.App.ID)
	defer unB()

	// Deliver only to A via the recipients predicate.
	d.Emit(EventMessageCreated, map[string]any{"to": "a"}, func(app *App) bool { return app.ID == a.App.ID })

	select {
	case <-chA:
	case <-time.After(time.Second):
		t.Fatal("A did not receive its event")
	}
	select {
	case <-chB:
		t.Fatal("B received an event addressed only to A")
	default:
	}
}

// TestSSESlowSubscriberDropped confirms a subscriber whose buffer is full does
// not block delivery: excess events are dropped for it, never deadlocking Emit.
func TestSSESlowSubscriberDropped(t *testing.T) {
	reg := NewMemoryRegistry()
	a := newApp(t, reg, "o", []string{ProductOffice}, nil)
	d := NewDispatcher(reg, ProductOffice)

	_, un := d.Subscribe(a.App.ID) // never drained
	defer un()

	done := make(chan struct{})
	go func() {
		// Far more than the 16-deep channel buffer; must not block.
		for i := 0; i < 1000; i++ {
			d.Emit(EventMessageCreated, map[string]any{"n": i}, nil)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Emit blocked on a slow/full SSE subscriber")
	}
}

// TestUnsubscribeStopsDelivery confirms a freed subscriber slot stops receiving
// (no use-after-unsubscribe send onto a closed channel, which would panic).
func TestUnsubscribeStopsDelivery(t *testing.T) {
	reg := NewMemoryRegistry()
	a := newApp(t, reg, "o", []string{ProductOffice}, nil)
	d := NewDispatcher(reg, ProductOffice)

	ch, un := d.Subscribe(a.App.ID)
	un()
	// Channel is closed; a closed channel yields a zero value immediately.
	select {
	case _, open := <-ch:
		if open {
			t.Fatal("expected the unsubscribed channel to be closed")
		}
	default:
		t.Fatal("unsubscribed channel should be closed, not blocking")
	}
	// Emitting after unsubscribe must not panic (no send on the closed channel).
	d.Emit(EventMessageCreated, map[string]any{}, nil)
}
