package notify

// unifiedpush_service.go — Service-side wiring of the cell-side UnifiedPush
// send-path (UP-CELL-01). This is the ALONGSIDE sibling of
// webpush_service.go: same choke point (SendNotification), same
// owner-targeting rule, same SuppressFunc/DND hook — a UnifiedPush endpoint
// is a second TRANSPORT, never a second policy path. See webpush_service.go's
// header for the shared reasoning; it is not repeated here.
//
// The actual endpoint-registration/SSRF-screening/transport implementation is
// the generic, standalone backend/internal/unifiedpush package. This file is
// the glue: it attaches a unifiedpush.Store + unifiedpush.Config + the SAME
// SuppressFunc type webpush_service.go defines to the Service. Once attached,
// SendNotification ALSO fans a UnifiedPush POST out to the notification's
// OWNER's registered endpoint(s) — but only when the notification is
// owner-targeted (Notification.UserID != "") and the suppression predicate
// does not veto it. Box-level (untargeted) notifications are never
// UnifiedPush'd, exactly like Web Push.

import (
	"encoding/json"
	"log"
	"net/http"

	"vulos/backend/internal/unifiedpush"
)

// upBinding holds everything the UnifiedPush send pump needs. It is swapped
// atomically under the Service lock via SetUnifiedPush, independently of
// pushBinding — a box may have Web Push, UnifiedPush, both, or neither
// attached at once.
type upBinding struct {
	store    unifiedpush.Store
	cfg      unifiedpush.Config
	sender   unifiedpush.Sender
	suppress SuppressFunc
}

// SetUnifiedPush attaches (or, with a nil store, detaches) the UnifiedPush
// send-path. store may be nil to disable. cfg must be resolved
// (unifiedpush.LoadConfig); if cfg.Enabled is false the binding is inert (no
// sends). suppress may be nil. This is additive and never affects the in-app
// path or the separate Web Push path.
func (s *Service) SetUnifiedPush(store unifiedpush.Store, cfg unifiedpush.Config, suppress SuppressFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if store == nil || !cfg.Enabled {
		s.up = nil
		return
	}
	s.up = &upBinding{
		store:    store,
		cfg:      cfg,
		sender:   unifiedpush.LiveSender{},
		suppress: suppress,
	}
}

// setUnifiedPushForTest injects a custom sender (tests). Not for production use.
func (s *Service) setUnifiedPushForTest(store unifiedpush.Store, cfg unifiedpush.Config, sender unifiedpush.Sender, suppress SuppressFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.up = &upBinding{store: store, cfg: cfg, sender: sender, suppress: suppress}
}

// unifiedPushEnabled reports whether a live UnifiedPush binding is attached.
func (s *Service) unifiedPushEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.up != nil
}

// maybeUnifiedPush is invoked by SendNotification for every notification,
// right alongside maybeWebPush. It decides eligibility with the EXACT SAME
// rules (owner-targeting, then the suppress predicate) and, when eligible,
// fires the fan-out asynchronously. It reads the binding under the lock and
// then releases it — the send loop touches no Service state, so it never
// holds the lock across network I/O.
func (s *Service) maybeUnifiedPush(n Notification) {
	// Only owner-targeted notifications are pushed (same rule as Web Push —
	// a box-level notification has no single owner to send to).
	if n.UserID == "" {
		return
	}
	s.mu.RLock()
	pb := s.up
	s.mu.RUnlock()
	if pb == nil {
		return
	}
	// Respect DND / prefs: the SAME policy that governs the in-app surface
	// and Web Push — a UnifiedPush endpoint is not a way to bypass it.
	if pb.suppress != nil && pb.suppress(n.UserID, n.Level, n.Priority) {
		return
	}
	payload := unifiedpush.Payload{
		Title:  n.Title,
		Body:   bodyString(n.Body), // shared helper, defined in webpush_service.go
		Tag:    string(n.Type),
		Source: n.Source,
		URL:    n.Action,
	}
	go pb.pump(n.UserID, payload)
}

// pump delivers payload to every one of ownerID's registered UnifiedPush
// endpoints. An endpoint the distributor reports as gone (HTTP 404/410 — the
// same convention Web Push subscriptions are pruned on) is pruned. Errors are
// logged WITHOUT the endpoint URL (a capability URL) or payload content. This
// method touches no Service state, so it is safe to run detached.
func (pb *upBinding) pump(ownerID string, payload unifiedpush.Payload) {
	eps, err := pb.store.List(ownerID)
	if err != nil {
		log.Printf("[notify] unifiedpush: list endpoints failed: %v", err)
		return
	}
	if len(eps) == 0 {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	for _, ep := range eps {
		code, err := pb.sender.Send(ep, raw, pb.cfg)
		if err != nil {
			// Never log the endpoint URL (it is a capability URL) or payload content.
			log.Printf("[notify] unifiedpush: send failed (status unknown): %v", err)
			continue
		}
		if code == http.StatusGone || code == http.StatusNotFound {
			if delErr := pb.store.Delete(ownerID, ep.URL); delErr != nil {
				log.Printf("[notify] unifiedpush: prune gone endpoint failed: %v", delErr)
			}
		}
	}
}
