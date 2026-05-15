package notify

import (
	"encoding/json"
	"testing"
	"time"
)

// TestFillDefaults_NewNotification verifies that a bare Notification gets
// sensible defaults applied by fillDefaults.
func TestFillDefaults_NewNotification(t *testing.T) {
	n := Notification{
		Title:  "Hello",
		Source: "test",
	}
	fillDefaults(&n)

	if n.ID == "" {
		t.Error("ID should be set by fillDefaults")
	}
	if n.Priority != PriorityNormal {
		t.Errorf("default Priority = %q, want %q", n.Priority, PriorityNormal)
	}
	if n.Type != TypeAlert {
		t.Errorf("default Type = %q, want %q", n.Type, TypeAlert)
	}
	if n.Subtype != "system" {
		t.Errorf("default Subtype = %q, want %q", n.Subtype, "system")
	}
	if n.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set by fillDefaults")
	}
}

// TestFillDefaults_PreservesExisting ensures that values already set on
// a Notification are not overwritten by fillDefaults.
func TestFillDefaults_PreservesExisting(t *testing.T) {
	createdAt := time.Now().Add(-1 * time.Hour)
	n := Notification{
		ID:        "custom-id",
		Title:     "Ping",
		Type:      TypeCall,
		Subtype:   "incoming",
		Priority:  PriorityHigh,
		TTL:       60,
		CreatedAt: createdAt,
	}
	fillDefaults(&n)

	if n.ID != "custom-id" {
		t.Errorf("ID should not be overwritten, got %q", n.ID)
	}
	if n.Type != TypeCall {
		t.Errorf("Type should not be overwritten, got %q", n.Type)
	}
	if n.Subtype != "incoming" {
		t.Errorf("Subtype should not be overwritten, got %q", n.Subtype)
	}
	if n.Priority != PriorityHigh {
		t.Errorf("Priority should not be overwritten, got %q", n.Priority)
	}
	if n.TTL != 60 {
		t.Errorf("TTL should not be overwritten, got %d", n.TTL)
	}
	if !n.CreatedAt.Equal(createdAt) {
		t.Error("CreatedAt should not be overwritten")
	}
}

// TestClampPriority_CriticalNonCall verifies that critical priority is
// downgraded to high for non-call notification types.
func TestClampPriority_CriticalNonCall(t *testing.T) {
	types := []Type{TypePresence, TypeEvent, TypeAlert, TypeAction}
	for _, notifType := range types {
		got := clampPriority(notifType, PriorityCritical)
		if got != PriorityHigh {
			t.Errorf("clampPriority(%q, critical) = %q, want %q", notifType, got, PriorityHigh)
		}
	}
}

// TestClampPriority_CriticalCall verifies that critical priority is
// preserved for call notifications.
func TestClampPriority_CriticalCall(t *testing.T) {
	got := clampPriority(TypeCall, PriorityCritical)
	if got != PriorityCritical {
		t.Errorf("clampPriority(call, critical) = %q, want %q", got, PriorityCritical)
	}
}

// TestClampPriority_NonCriticalUnchanged verifies that non-critical priority
// values pass through unchanged for any type.
func TestClampPriority_NonCriticalUnchanged(t *testing.T) {
	priorities := []Priority{PriorityLow, PriorityNormal, PriorityHigh}
	types := []Type{TypePresence, TypeEvent, TypeCall, TypeAlert, TypeAction}
	for _, p := range priorities {
		for _, notifType := range types {
			got := clampPriority(notifType, p)
			if got != p {
				t.Errorf("clampPriority(%q, %q) = %q, want unchanged %q", notifType, p, got, p)
			}
		}
	}
}

// TestIsExpired_ZeroTTL verifies that TTL==0 means the notification never expires.
func TestIsExpired_ZeroTTL(t *testing.T) {
	n := &Notification{
		CreatedAt: time.Now().Add(-24 * time.Hour),
		TTL:       0,
	}
	if n.IsExpired() {
		t.Error("notification with TTL=0 should never expire")
	}
}

// TestIsExpired_Expired verifies that a notification past its TTL is expired.
func TestIsExpired_Expired(t *testing.T) {
	n := &Notification{
		CreatedAt: time.Now().Add(-2 * time.Minute),
		TTL:       60, // 1 minute
	}
	if !n.IsExpired() {
		t.Error("notification past TTL should be expired")
	}
}

// TestIsExpired_NotYetExpired verifies that a fresh notification is not expired.
func TestIsExpired_NotYetExpired(t *testing.T) {
	n := &Notification{
		CreatedAt: time.Now(),
		TTL:       3600, // 1 hour
	}
	if n.IsExpired() {
		t.Error("fresh notification should not be expired")
	}
}

// TestSend_DefaultFill verifies that Send (legacy wrapper) produces
// priority=normal for LevelInfo input and fills new fields.
func TestSend_DefaultFill(t *testing.T) {
	svc := New()
	got := svc.Send("Test title", "Test body", LevelInfo, "test")

	if got.Priority != PriorityNormal {
		t.Errorf("Send with LevelInfo: Priority = %q, want %q", got.Priority, PriorityNormal)
	}
	if got.Type != TypeAlert {
		t.Errorf("Send: Type = %q, want %q", got.Type, TypeAlert)
	}
	if got.Subtype != "system" {
		t.Errorf("Send: Subtype = %q, want %q", got.Subtype, "system")
	}
	if got.ID == "" {
		t.Error("Send: ID should be set")
	}
}

// TestSend_LevelWarning verifies that LevelWarning maps to priority=normal.
func TestSend_LevelWarning(t *testing.T) {
	svc := New()
	got := svc.Send("Warn", "body", LevelWarning, "test")
	if got.Priority != PriorityNormal {
		t.Errorf("LevelWarning -> Priority = %q, want normal", got.Priority)
	}
}

// TestSend_LevelUrgent verifies that LevelUrgent maps to priority=high.
func TestSend_LevelUrgent(t *testing.T) {
	svc := New()
	got := svc.Send("Urgent", "body", LevelUrgent, "test")
	if got.Priority != PriorityHigh {
		t.Errorf("LevelUrgent -> Priority = %q, want high", got.Priority)
	}
}

// TestSendNotification_CriticalDowngrade verifies end-to-end that a
// critical non-call notification stored via SendNotification is downgraded.
func TestSendNotification_CriticalDowngrade(t *testing.T) {
	svc := New()
	got := svc.SendNotification(Notification{
		Title:    "Alert",
		Type:     TypeAlert,
		Priority: PriorityCritical,
	})
	if got.Priority != PriorityHigh {
		t.Errorf("alert/critical -> Priority = %q, want high", got.Priority)
	}
}

// TestSendNotification_CriticalCallPreserved verifies that a critical call
// notification is NOT downgraded.
func TestSendNotification_CriticalCallPreserved(t *testing.T) {
	svc := New()
	got := svc.SendNotification(Notification{
		Title:    "Incoming call",
		Type:     TypeCall,
		Subtype:  "incoming",
		Priority: PriorityCritical,
	})
	if got.Priority != PriorityCritical {
		t.Errorf("call/critical -> Priority = %q, want critical", got.Priority)
	}
}

// TestNotification_JSONFields verifies that all new fields appear in the
// marshalled JSON.
func TestNotification_JSONFields(t *testing.T) {
	n := Notification{
		ID:        "test-id",
		Title:     "Test",
		Body:      map[string]string{"key": "value"},
		Type:      TypeEvent,
		Subtype:   "file_shared",
		Priority:  PriorityNormal,
		TTL:       300,
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	for _, field := range []string{"id", "type", "subtype", "priority", "ttl", "body", "created_at"} {
		if _, ok := out[field]; !ok {
			t.Errorf("JSON missing field %q", field)
		}
	}
}

// TestNotification_BodyAny verifies that Body accepts different value types.
func TestNotification_BodyAny(t *testing.T) {
	svc := New()

	// Body as string (legacy path via Send).
	n1 := svc.Send("title", "string body", LevelInfo, "src")
	if _, ok := n1.Body.(string); !ok {
		t.Errorf("string body: got type %T", n1.Body)
	}

	// Body as structured data via SendNotification.
	payload := map[string]any{"call_id": "abc", "media": "video"}
	n2 := svc.SendNotification(Notification{
		Title: "Call",
		Type:  TypeCall,
		Body:  payload,
	})
	if n2.Body == nil {
		t.Error("structured body should not be nil")
	}

	// Body as nil.
	n3 := svc.SendNotification(Notification{
		Title:   "Online",
		Type:    TypePresence,
		Subtype: "online",
		Body:    nil,
	})
	if n3.Body != nil {
		t.Errorf("nil body: got %v", n3.Body)
	}
}
