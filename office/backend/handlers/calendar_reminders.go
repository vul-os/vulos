// calendar_reminders.go — reminder worker and calendar/subscription handlers.
//
// The reminder worker is a goroutine that polls every minute and fires
// reminders that are due.  It is started by StartReminderWorker() which should
// be called once from main().
//
// Reminder lifecycle:
//  1. Event is created/updated with a Reminders list.
//  2. Worker computes fire time = event.Start − reminder.MinutesBefore.
//  3. When now ≥ fire time AND the reminder has not yet fired, it fires.
//  4. Fired reminders are tracked in firedReminders set (in-memory; replace
//     with DB in production).
//
// Subscriptions (external .ics feeds) are stored in calSubscriptions; a second
// goroutine fetches them daily.
package handlers

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ─── reminder worker ──────────────────────────────────────────────────────────

// firedKey uniquely identifies a fired reminder so it doesn't double-fire.
// Format: "<eventID>:<minutesBefore>:<channel>"
func firedKey(eventID string, r Reminder) string {
	return fmt.Sprintf("%s:%d:%s", eventID, r.MinutesBefore, r.Channel)
}

var firedMu sync.Mutex
var firedReminders = map[string]bool{}

// NotifyFunc is called by the worker when a reminder fires.  In production this
// would push to the email/push/in-app notification service.
type NotifyFunc func(event *CalEvent, reminder Reminder)

// defaultNotify logs the reminder (swap for real delivery).
func defaultNotify(event *CalEvent, reminder Reminder) {
	log.Printf("REMINDER [%s] event=%q starts=%s channel=%s",
		reminder.Channel, event.Title,
		event.Start.Format(time.RFC3339), reminder.Channel)
}

// StartReminderWorker launches the background goroutine.  It ticks every
// 60 seconds.  Pass a custom NotifyFunc for tests; pass nil to use the default.
func StartReminderWorker(notify NotifyFunc) {
	if notify == nil {
		notify = defaultNotify
	}
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			fireReminders(time.Now().UTC(), notify)
		}
	}()
}

// fireReminders is the core logic — exported so tests can call it directly
// without waiting for the ticker.
func fireReminders(now time.Time, notify NotifyFunc) {
	calStore.mu.RLock()
	events := make([]*CalEvent, 0, len(calStore.events))
	for _, e := range calStore.events {
		events = append(events, e)
	}
	calStore.mu.RUnlock()

	firedMu.Lock()
	defer firedMu.Unlock()

	for _, event := range events {
		for _, r := range event.Reminders {
			fireAt := event.Start.Add(-time.Duration(r.MinutesBefore) * time.Minute)
			key := firedKey(event.ID, r)
			if !firedReminders[key] && !now.Before(fireAt) {
				firedReminders[key] = true
				notify(event, r)
			}
		}
	}
}

// ResetFiredReminders is exported for test use — clears the fired set.
func ResetFiredReminders() {
	firedMu.Lock()
	firedReminders = map[string]bool{}
	firedMu.Unlock()
}

// FireRemindersAt is exported for test use — runs the reminder check at an
// arbitrary "now" with a custom notify function.
func FireRemindersAt(now time.Time, notify NotifyFunc) {
	fireReminders(now, notify)
}

// PutCalEvent is exported for test use — inserts an event into the store.
func PutCalEvent(e *CalEvent) {
	calStore.put(e)
}

// DeleteCalEvent is exported for test use — removes an event from the store.
func DeleteCalEvent(id string) {
	calStore.delete(id)
}

// ClearCalStore is exported for test use — removes all events.
func ClearCalStore() {
	calStore.mu.Lock()
	calStore.events = map[string]*CalEvent{}
	calStore.mu.Unlock()
}

// ─── calendar subscription handler ────────────────────────────────────────────

type calSubscription struct {
	ID    string    `json:"id"`
	URL   string    `json:"url"`
	Name  string    `json:"name"`
	Added time.Time `json:"added"`
}

var subsMu sync.RWMutex
var calSubscriptions = map[string]*calSubscription{}

// CalendarSubscribeHandler handles external .ics subscriptions.
type CalendarSubscribeHandler struct{}

func NewCalendarSubscribeHandler() *CalendarSubscribeHandler {
	return &CalendarSubscribeHandler{}
}

// Subscribe POST /api/calendar/subscribe  body: {"url":"https://...", "name":"Holidays"}
func (h *CalendarSubscribeHandler) Subscribe(c *gin.Context) {
	var req struct {
		URL  string `json:"url" binding:"required"`
		Name string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	sub := &calSubscription{
		ID:    uuid.NewString(),
		URL:   req.URL,
		Name:  req.Name,
		Added: time.Now().UTC(),
	}
	subsMu.Lock()
	calSubscriptions[sub.ID] = sub
	subsMu.Unlock()

	// Fetch immediately in the background.
	go fetchSubscription(sub)

	c.JSON(http.StatusCreated, sub)
}

// ListSubscriptions GET /api/calendar/subscriptions
func (h *CalendarSubscribeHandler) List(c *gin.Context) {
	subsMu.RLock()
	defer subsMu.RUnlock()
	out := make([]*calSubscription, 0, len(calSubscriptions))
	for _, s := range calSubscriptions {
		out = append(out, s)
	}
	c.JSON(http.StatusOK, out)
}

// StartSubscriptionRefresher refreshes all subscriptions once per day.
func StartSubscriptionRefresher() {
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			subsMu.RLock()
			subs := make([]*calSubscription, 0, len(calSubscriptions))
			for _, s := range calSubscriptions {
				subs = append(subs, s)
			}
			subsMu.RUnlock()
			for _, s := range subs {
				fetchSubscription(s)
			}
		}
	}()
}

func fetchSubscription(sub *calSubscription) {
	resp, err := http.Get(sub.URL) //nolint:gosec // user-supplied URL is intentional
	if err != nil {
		log.Printf("subscription fetch %q: %v", sub.URL, err)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("subscription read %q: %v", sub.URL, err)
		return
	}

	events := parseICSFeed(string(body), sub.ID)
	now := time.Now().UTC()
	for i := range events {
		events[i].CreatedAt = now
		events[i].UpdatedAt = now
		calStore.put(&events[i])
	}
	log.Printf("subscription %q: imported %d events", sub.Name, len(events))
}

// parseICSFeed is a minimal ICS parser for subscription feeds.
func parseICSFeed(ics, calID string) []CalEvent {
	var events []CalEvent
	blocks := strings.Split(ics, "BEGIN:VEVENT")
	for _, block := range blocks[1:] {
		uid := fieldValue(block, "UID")
		summary := fieldValue(block, "SUMMARY")
		dtstart := fieldValue(block, "DTSTART")
		dtend := fieldValue(block, "DTEND")
		if uid == "" {
			uid = uuid.NewString()
		}

		start := parseICSTimestamp(dtstart)
		end := parseICSTimestamp(dtend)
		if end.IsZero() {
			end = start.Add(time.Hour)
		}

		events = append(events, CalEvent{
			ID:         uid,
			CalendarID: calID,
			Title:      summary,
			Start:      start,
			End:        end,
		})
	}
	return events
}

func fieldValue(block, key string) string {
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, key+":") || strings.HasPrefix(line, key+";") {
			idx := strings.Index(line, ":")
			if idx >= 0 {
				return strings.TrimSpace(line[idx+1:])
			}
		}
	}
	return ""
}

func parseICSTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	// Try compact UTC: 20260601T100000Z
	t, err := time.Parse("20060102T150405Z", s)
	if err == nil {
		return t
	}
	// Try without Z
	t, err = time.Parse("20060102T150405", s)
	if err == nil {
		return t.UTC()
	}
	return time.Time{}
}
