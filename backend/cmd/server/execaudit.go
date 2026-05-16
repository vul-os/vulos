package main

import (
	"log"
	"net/http"
	"time"
)

// execAuditLog records a privileged exec/launch/sandbox event.
// It logs the actor, route, and a short description of what was authorised.
// Call this AFTER the kill-switch and admin checks pass, BEFORE the action runs.
func execAuditLog(r *http.Request, route, description string) {
	userID := r.Header.Get("X-User-ID")
	ip := r.RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ip = xff
	}
	log.Printf("[exec-audit] %s route=%s actor=%q ip=%s %s",
		time.Now().UTC().Format(time.RFC3339), route, userID, ip, description)
}
