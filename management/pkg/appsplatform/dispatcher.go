package appsplatform

import (
	"bytes"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Common event types. Products MAY define their own; these are the generic,
// Slack-lineage types the platform ships out of the box.
const (
	EventMessageCreated = "message.created"
	EventAppMention     = "app_mention"
	EventMemberJoined   = "member_joined"
	EventSlashCommand   = "slash_command"
)

// Event is the outbound event envelope. The Event field carries the per-type
// payload (a map so each type can carry its own shape).
type Event struct {
	Type      string         `json:"type"`
	AppID     string         `json:"app_id"`
	Product   string         `json:"product"`
	Event     map[string]any `json:"event"`
	EventTime int64          `json:"event_time"`
}

// Dispatcher signs and delivers outbound events to apps, both via their
// configured WebhookURL (fire-and-forget HTTP) and via in-memory SSE streams
// (socket-mode style). It also intercepts slash commands on a send path.
//
// It is product-agnostic: it enumerates apps via the Registry, filters them by
// product-targeting and event-subscription, and applies a caller-supplied
// visibility predicate. The product decides WHAT to emit and WHO can see it.
type Dispatcher struct {
	reg     Registry
	product string
	client  *http.Client

	mu     sync.Mutex
	nextID int
	subs   map[string]map[int]chan []byte // appID → subId → channel
}

// NewDispatcher builds a dispatcher for one product over a registry. All events
// it emits are stamped with product, and only apps targeting product receive
// them.
func NewDispatcher(reg Registry, product string) *Dispatcher {
	return &Dispatcher{
		reg:     reg,
		product: product,
		client:  newWebhookClient(5 * time.Second),
		subs:    make(map[string]map[int]chan []byte),
	}
}

// newWebhookClient builds the HTTP client used for outbound webhook delivery. Its
// transport dials through safeDialContext, which re-screens and pins the
// destination IP at dial time so a DNS rebind cannot redirect a validated
// webhook to an internal address (see safeDialContext).
//
// Proxy is explicitly nil: http.ProxyFromEnvironment would route outbound
// webhook requests through any *_PROXY env var, bypassing the SSRF IP guard
// (safeDialContext is not invoked for the proxy hop). Operators who need proxy
// support must configure it at the network layer (e.g. transparent proxy),
// never via the process-level env vars.
func newWebhookClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 nil, // see doc comment — env proxy bypasses SSRF guard
			DialContext:           safeDialContext(dialer),
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// Emit fans an event out to every app that (a) targets this product, (b)
// subscribes to eventType, and (c) passes the recipients predicate (a nil
// recipients delivers to all that pass a+b). Never blocks the caller: HTTP
// delivery is async and SSE sends are non-blocking. This is the single
// product-facing entry point — adapters receive it as an EmitFunc.
func (d *Dispatcher) Emit(eventType string, payload map[string]any, recipients func(*App) bool) {
	apps, err := d.reg.List("", true) // all apps
	if err != nil {
		return
	}
	for _, a := range apps {
		if !a.TargetsProduct(d.product) {
			continue
		}
		if !a.SubscribesTo(eventType) {
			continue
		}
		if recipients != nil && !recipients(a) {
			continue
		}
		d.deliver(a, Event{Type: eventType, Event: payload})
	}
}

// EmitFunc returns d.Emit as an EmitFunc, for handing to ProductAdapter.Act.
func (d *Dispatcher) EmitFunc() EmitFunc { return d.Emit }

// MaybeHandleSlash intercepts a message body that is a registered slash command
// for an app targeting this product. It returns true when the body was
// dispatched (and thus must NOT be stored as a normal message). Unknown commands
// (or non-slash bodies) return false so the caller stores them normally.
func (d *Dispatcher) MaybeHandleSlash(target, userID, body string) bool {
	name, args, ok := ParseSlash(body)
	if !ok {
		return false
	}
	app, cmd, found := d.reg.ResolveSlashCommand(d.product, name)
	if !found {
		return false
	}
	d.DispatchSlash(app, cmd, target, userID, args)
	return true
}

// DispatchSlash emits a slash_command event for the resolved command.
func (d *Dispatcher) DispatchSlash(app *App, cmd *SlashCommand, target, userID, args string) {
	d.deliver(app, Event{
		Type: EventSlashCommand,
		Event: map[string]any{
			"command": cmd.Name,
			"text":    args,
			"target":  target,
			"user_id": userID,
		},
	})
}

// ParseSlash splits a "/name args..." body. ok is false when body does not start
// with a slash or has no command token. name is returned without the slash.
func ParseSlash(body string) (name, args string, ok bool) {
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, "/") {
		return "", "", false
	}
	rest := strings.TrimPrefix(trimmed, "/")
	if rest == "" {
		return "", "", false
	}
	parts := strings.SplitN(rest, " ", 2)
	name = strings.ToLower(strings.TrimSpace(parts[0]))
	if name == "" {
		return "", "", false
	}
	if len(parts) == 2 {
		args = strings.TrimSpace(parts[1])
	}
	return name, args, true
}

// ---- delivery ----------------------------------------------------------------

// deliver fills in the envelope metadata and ships the event to the app's
// WebhookURL (if set) and any live SSE subscribers. Never blocks the caller.
func (d *Dispatcher) deliver(a *App, ev Event) {
	ev.AppID = a.ID
	ev.Product = d.product
	if ev.EventTime == 0 {
		ev.EventTime = time.Now().Unix()
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return
	}
	d.fanoutSSE(a.ID, body)
	if a.WebhookURL != "" {
		go d.post(a.WebhookURL, a.SigningSecret, body)
	}
}

// post signs and POSTs a single event body to url. Best-effort: failures are
// logged, never retried, and never surfaced to the originating request.
func (d *Dispatcher) post(url, secret string, body []byte) {
	if err := ValidateWebhookURL(url); err != nil {
		log.Printf("[apps] refusing event delivery: %v", err)
		return
	}
	ts := NowTimestamp()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		log.Printf("[apps] build event request to %s: %v", url, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SigHeaderTimestamp, ts)
	req.Header.Set(SigHeaderSignature, Sign(ts, body, secret))
	resp, err := d.client.Do(req)
	if err != nil {
		log.Printf("[apps] deliver event to %s failed: %v", url, err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("[apps] deliver event to %s: status %d", url, resp.StatusCode)
	}
}

// ---- SSE subscriber registry -------------------------------------------------

// Subscribe registers an SSE subscriber for appID. It returns a receive channel
// for serialized event JSON and an unsubscribe func that must be called on
// disconnect to free the slot.
func (d *Dispatcher) Subscribe(appID string) (<-chan []byte, func()) {
	ch := make(chan []byte, 16)
	d.mu.Lock()
	d.nextID++
	id := d.nextID
	if d.subs[appID] == nil {
		d.subs[appID] = make(map[int]chan []byte)
	}
	d.subs[appID][id] = ch
	d.mu.Unlock()

	return ch, func() {
		d.mu.Lock()
		if m := d.subs[appID]; m != nil {
			if c, ok := m[id]; ok {
				delete(m, id)
				close(c)
			}
			if len(m) == 0 {
				delete(d.subs, appID)
			}
		}
		d.mu.Unlock()
	}
}

// fanoutSSE pushes body to every live subscriber for appID. Slow/full
// subscribers are skipped (non-blocking send) so one stalled consumer never
// blocks delivery to others or to the originating request.
func (d *Dispatcher) fanoutSSE(appID string, body []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, ch := range d.subs[appID] {
		select {
		case ch <- body:
		default:
			// Subscriber is not keeping up; drop this event for them.
		}
	}
}
