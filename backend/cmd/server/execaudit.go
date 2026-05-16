package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// execAuditEntry is the structured record written for every /api/exec call.
type execAuditEntry struct {
	TS      time.Time `json:"ts"`
	UserID  string    `json:"user_id"`
	Command string    `json:"command"`
	Exit    int       `json:"exit"`
}

// logExecAudit writes a single structured audit entry to the standard logger.
// Output: {"ts":"...","user_id":"...","command":"...","exit":0}
func logExecAudit(userID, command string, exit int) {
	entry := execAuditEntry{
		TS:      time.Now().UTC(),
		UserID:  userID,
		Command: command,
		Exit:    exit,
	}
	b, err := json.Marshal(entry)
	if err != nil {
		log.Printf("[exec-audit] marshal error: %v", err)
		return
	}
	log.Printf("[exec-audit] %s", b)
}

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
