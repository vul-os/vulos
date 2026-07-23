package main

import (
	"net/http"

	"vulos/backend/services/auth"
	"vulos/backend/services/notify"
	"vulos/backend/services/telephony"
)

// telephonyNotifier adapts notify.Service to telephony.Notifier. An inbound SMS
// fires a notification TARGETED at the box owner (UserID set) — which is what
// makes it web-push: box-level (untargeted) notifications are intentionally NOT
// web-pushed, so without a target the "reaches you even with the app closed" goal
// would silently fail. Targeting also scopes the OTP/alert to the owner, never
// another account on a multi-user box.
type telephonyNotifier struct {
	svc     *notify.Service
	ownerID func() string
}

func (n telephonyNotifier) Send(title, body, source string) {
	if n.svc == nil {
		return
	}
	owner := ""
	if n.ownerID != nil {
		owner = n.ownerID()
	}
	n.svc.SendNotification(notify.Notification{
		Title:  title,
		Body:   body,
		Level:  notify.LevelInfo,
		Source: source,
		Type:   notify.TypeAlert,
		UserID: owner, // targeted → web-pushed to the owner
	})
}

// registerTelephonyRoutes wires the box GSM service (TELE-01: SMS + calls via
// ModemManager's mmcli) onto mux and starts the inbound-SMS poll loop. Returns
// the service so the caller can StopPolling() at shutdown.
//
// Always safe to call: the service is hardware-gated. With no `mmcli` or no modem
// present, the endpoints return an "unavailable" status (the phone app renders a
// "connect a modem" state) and the poll loop simply idles.
func registerTelephonyRoutes(mux *http.ServeMux, notifySvc *notify.Service, authStore *auth.Store) *telephony.Service {
	// The box's single SIM is the OWNER's line. Both the notifier (targeting) and
	// the service (owner-gating of every /api/telephony route + the WS feed) resolve
	// the owner the same way. Resolved live so it tracks a later first-user setup.
	ownerID := func() string {
		if authStore == nil {
			return ""
		}
		return authStore.AdminUserID()
	}
	svc := telephony.New(telephonyNotifier{svc: notifySvc, ownerID: ownerID}, ownerID)
	telephony.RegisterHandlers(mux, svc)
	svc.StartPolling()
	return svc
}
