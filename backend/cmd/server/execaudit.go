package main

import (
	"encoding/json"
	"log"
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
