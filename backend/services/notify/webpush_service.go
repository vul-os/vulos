package notify

// webpush_service.go — Service-side wiring of the cell-side Web Push send-path
// (PUSH-CELL-01).
//
// The actual VAPID/subscription/transport implementation is the generic,
// standalone backend/internal/webpush package (extracted so it can be
// imported without pulling in the notify service). This file is the glue: it
// attaches a webpush.Store + webpush.Config + an optional suppression
// predicate (DND/prefs) to the Service. Once attached, SendNotification ALSO
// fans a Web Push out to the notification's OWNER's subscriptions — but only
// when the notification is owner-targeted (Notification.UserID != "") and the
// suppression predicate does not veto it. Box-level (untargeted) notifications
// are NOT web-pushed: they have no single owner to send to and are the noisy
// system/DND-toggle class that belongs on the live in-app stream only.
//
// The push fan-out runs in a background goroutine so a slow push vendor never
// blocks the in-app WebSocket broadcast or the caller of SendNotification.

import (
	"encoding/json"
	"log"
	"net/http"

	"vulos/backend/internal/webpush"
)

// SuppressFunc reports whether a notification of the given priority (and legacy
// level) should be WITHHELD for owner. It lets the Service consult the real DND
// policy (which lives in the cmd/server layer) without importing it. A nil
// SuppressFunc means "never suppress".
type SuppressFunc func(owner string, level Level, priority Priority) bool

// pushBinding holds everything the push pump needs. It is swapped atomically
// under the Service lock via SetPush.
type pushBinding struct {
	store    webpush.Store
	cfg      webpush.Config
	sender   webpush.Sender
	suppress SuppressFunc
}

// SetPush attaches (or, with a nil store, detaches) the Web Push send-path.
// store may be nil to disable. cfg must be resolved (webpush.ResolveVAPID)
// already; if cfg.Enabled() is false the binding is inert (no key material, no
// sends). suppress may be nil. This is additive and never affects the in-app
// path.
func (s *Service) SetPush(store webpush.Store, cfg webpush.Config, suppress SuppressFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if store == nil || !cfg.Enabled() {
		s.push = nil
		return
	}
	s.push = &pushBinding{
		store:    store,
		cfg:      cfg,
		sender:   webpush.LiveSender{},
		suppress: suppress,
	}
}

// setPushForTest injects a custom sender (tests). Not for production use.
func (s *Service) setPushForTest(store webpush.Store, cfg webpush.Config, sender webpush.Sender, suppress SuppressFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.push = &pushBinding{store: store, cfg: cfg, sender: sender, suppress: suppress}
}

// pushEnabled reports whether a live push binding is attached.
func (s *Service) pushEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.push != nil
}

// maybeWebPush is invoked by SendNotification for every notification. It decides
// eligibility and, when eligible, fires the fan-out asynchronously. It reads the
// binding under the lock and then releases it — the send loop touches no Service
// state, so it never holds the lock across network I/O.
func (s *Service) maybeWebPush(n Notification) {
	// Only owner-targeted notifications are web-pushed (see file header).
	if n.UserID == "" {
		return
	}
	s.mu.RLock()
	pb := s.push
	s.mu.RUnlock()
	if pb == nil {
		return
	}
	// Respect DND / prefs: the same policy that governs the in-app surface.
	if pb.suppress != nil && pb.suppress(n.UserID, n.Level, n.Priority) {
		return
	}
	payload := webpush.Payload{
		Title:  n.Title,
		Body:   bodyString(n.Body),
		Tag:    string(n.Type),
		Source: n.Source,
		URL:    n.Action,
	}
	go pb.pump(n.UserID, payload)
}

// pump delivers payload to every one of ownerID's subscriptions. A subscription
// the push vendor reports as gone (HTTP 404/410) is pruned. Errors are logged
// WITHOUT any key material or payload content. This method touches no Service
// state, so it is safe to run detached.
func (pb *pushBinding) pump(ownerID string, payload webpush.Payload) {
	subs, err := pb.store.List(ownerID)
	if err != nil {
		log.Printf("[notify] push: list subs failed: %v", err)
		return
	}
	if len(subs) == 0 {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	for _, sub := range subs {
		code, err := pb.sender.Send(sub, raw, pb.cfg)
		if err != nil {
			// Never log the endpoint (it is a capability URL) or key material.
			log.Printf("[notify] push: send failed (status unknown): %v", err)
			continue
		}
		// RFC 8030 §8.3: 404/410 mean the subscription is permanently gone.
		if code == http.StatusGone || code == http.StatusNotFound {
			if delErr := pb.store.Delete(ownerID, sub.Endpoint); delErr != nil {
				log.Printf("[notify] push: prune gone sub failed: %v", delErr)
			}
		}
	}
}

// bodyString renders a Notification.Body (which is `any`) into the short text
// snippet the push payload carries. Strings pass through; anything else is
// JSON-encoded so the SW always receives a string.
func bodyString(body any) string {
	switch v := body.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return ""
	}
}
