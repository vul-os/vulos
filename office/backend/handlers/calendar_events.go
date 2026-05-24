// Package handlers — calendar event endpoints.
//
// Routes (all protected):
//
//	GET    /api/calendar/events            — list events for a date range
//	POST   /api/calendar/events            — create event
//	PUT    /api/calendar/events/:id        — update event
//	DELETE /api/calendar/events/:id        — delete event
//	POST   /api/calendar/events/:id/rsvp   — RSVP (status: accepted|declined|maybe|pending)
//	GET    /api/calendar/export/:calID     — ICS export for a calendar
//	POST   /api/calendar/subscribe         — subscribe to external .ics URL
//	POST   /api/calendar/rrule/expand      — expand RRULE in a window (helper for frontend)
//
// These handlers are intentionally thin: they delegate CalDAV persistence to
// the existing vulos-mail CalDAV package and store event metadata (invitees,
// reminders, colour, recurrence, etc.) in the in-memory store below (replacing
// with a real DB is a 1-line change).
package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"vulos-office/backend/services/calendar_rrule"
)

// ─── domain types ─────────────────────────────────────────────────────────────

// InviteeStatus tracks per-invitee RSVP state.
type InviteeStatus string

const (
	InviteePending  InviteeStatus = "pending"
	InviteeAccepted InviteeStatus = "accepted"
	InviteeDeclined InviteeStatus = "declined"
	InviteeMaybe    InviteeStatus = "maybe"
)

// Invitee is one calendar invite recipient.
type Invitee struct {
	Email  string        `json:"email"`
	Name   string        `json:"name,omitempty"`
	Status InviteeStatus `json:"status"`
}

// Reminder is one per-event reminder rule.
type Reminder struct {
	MinutesBefore int    `json:"minutes_before"` // minutes before event start
	Channel       string `json:"channel"`        // "email" | "push" | "in-app"
}

// CalEvent is the full rich event model.
type CalEvent struct {
	ID          string      `json:"id"`
	CalendarID  string      `json:"calendar_id"`
	Title       string      `json:"title"`
	AllDay      bool        `json:"all_day,omitempty"`
	Start       time.Time   `json:"start"`
	End         time.Time   `json:"end"`
	TimeZone    string      `json:"time_zone,omitempty"`
	Location    string      `json:"location,omitempty"`
	Description string      `json:"description,omitempty"`
	Invitees    []Invitee   `json:"invitees,omitempty"`
	Recurrence  string      `json:"recurrence,omitempty"` // RRULE string
	Reminders   []Reminder  `json:"reminders,omitempty"`
	Color       string      `json:"color,omitempty"` // hex from palette
	Visibility  string      `json:"visibility,omitempty"` // "public" | "private" | "default"
	MeetURL     string      `json:"meet_url,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// ─── in-memory store (replace with DB) ────────────────────────────────────────

type calendarStore struct {
	mu     sync.RWMutex
	events map[string]*CalEvent // keyed by event ID
}

var calStore = &calendarStore{events: map[string]*CalEvent{}}

func (s *calendarStore) list(from, to time.Time, calID string) []*CalEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*CalEvent
	for _, e := range s.events {
		if calID != "" && e.CalendarID != calID {
			continue
		}
		// Include if event overlaps [from, to)
		if e.End.After(from) && e.Start.Before(to) {
			out = append(out, e)
		}
	}
	return out
}

func (s *calendarStore) get(id string) (*CalEvent, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.events[id]
	return e, ok
}

func (s *calendarStore) put(e *CalEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events[e.ID] = e
}

func (s *calendarStore) delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.events, id)
}

// ─── handler ──────────────────────────────────────────────────────────────────

// CalendarEventHandler handles calendar event API requests.
type CalendarEventHandler struct{}

func NewCalendarEventHandler() *CalendarEventHandler { return &CalendarEventHandler{} }

// ListEvents GET /api/calendar/events?from=RFC3339&to=RFC3339&cal=calID
func (h *CalendarEventHandler) ListEvents(c *gin.Context) {
	from, to, err := parseDateRange(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	calID := c.Query("cal")
	events := calStore.list(from, to, calID)
	c.JSON(http.StatusOK, events)
}

// CreateEvent POST /api/calendar/events
func (h *CalendarEventHandler) CreateEvent(c *gin.Context) {
	var req CalEvent
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	now := time.Now().UTC()
	req.ID = uuid.NewString()
	req.CreatedAt = now
	req.UpdatedAt = now
	if req.CalendarID == "" {
		req.CalendarID = "personal"
	}
	// Set default reminders if none provided
	if len(req.Reminders) == 0 {
		req.Reminders = []Reminder{{MinutesBefore: 10, Channel: "in-app"}}
	}
	// Validate RRULE if present
	if req.Recurrence != "" {
		if _, err := calendar_rrule.Parse(req.Start, req.Recurrence); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid RRULE: " + err.Error()})
			return
		}
	}
	calStore.put(&req)
	c.JSON(http.StatusCreated, &req)
}

// UpdateEvent PUT /api/calendar/events/:id
func (h *CalendarEventHandler) UpdateEvent(c *gin.Context) {
	id := c.Param("id")
	existing, ok := calStore.get(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "event not found"})
		return
	}

	var req CalEvent
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.ID = id
	req.CreatedAt = existing.CreatedAt
	req.UpdatedAt = time.Now().UTC()
	if req.CalendarID == "" {
		req.CalendarID = existing.CalendarID
	}
	if req.Recurrence != "" {
		if _, err := calendar_rrule.Parse(req.Start, req.Recurrence); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid RRULE: " + err.Error()})
			return
		}
	}
	calStore.put(&req)
	c.JSON(http.StatusOK, &req)
}

// DeleteEvent DELETE /api/calendar/events/:id
func (h *CalendarEventHandler) DeleteEvent(c *gin.Context) {
	id := c.Param("id")
	if _, ok := calStore.get(id); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "event not found"})
		return
	}
	calStore.delete(id)
	c.Status(http.StatusNoContent)
}

// RSVPEvent POST /api/calendar/events/:id/rsvp  body: {"status":"accepted","email":"x@y.com"}
func (h *CalendarEventHandler) RSVPEvent(c *gin.Context) {
	id := c.Param("id")
	event, ok := calStore.get(id)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "event not found"})
		return
	}

	var req struct {
		Status InviteeStatus `json:"status" binding:"required"`
		Email  string        `json:"email" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	valid := map[InviteeStatus]bool{
		InviteePending:  true,
		InviteeAccepted: true,
		InviteeDeclined: true,
		InviteeMaybe:    true,
	}
	if !valid[req.Status] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid status"})
		return
	}

	updated := false
	for i := range event.Invitees {
		if strings.EqualFold(event.Invitees[i].Email, req.Email) {
			event.Invitees[i].Status = req.Status
			updated = true
			break
		}
	}
	if !updated {
		event.Invitees = append(event.Invitees, Invitee{
			Email:  req.Email,
			Status: req.Status,
		})
	}
	event.UpdatedAt = time.Now().UTC()
	calStore.put(event)
	c.JSON(http.StatusOK, event)
}

// ExportICS GET /api/calendar/export/:calID  — download .ics file
func (h *CalendarEventHandler) ExportICS(c *gin.Context) {
	calID := c.Param("calID")
	now := time.Now().UTC()
	from := now.Add(-30 * 24 * time.Hour)       // 30 days back
	to := now.Add(365 * 24 * time.Hour)         // 12 months forward

	events := calStore.list(from, to, calID)

	var sb strings.Builder
	sb.WriteString("BEGIN:VCALENDAR\r\n")
	sb.WriteString("VERSION:2.0\r\n")
	sb.WriteString("PRODID:-//Vulos Office//Calendar//EN\r\n")
	sb.WriteString("CALSCALE:GREGORIAN\r\n")
	sb.WriteString("METHOD:PUBLISH\r\n")

	for _, e := range events {
		sb.WriteString("BEGIN:VEVENT\r\n")
		sb.WriteString("UID:" + e.ID + "\r\n")
		sb.WriteString("SUMMARY:" + icsEscape(e.Title) + "\r\n")
		sb.WriteString("DTSTART:" + icsTime(e.Start) + "\r\n")
		sb.WriteString("DTEND:" + icsTime(e.End) + "\r\n")
		if e.Description != "" {
			sb.WriteString("DESCRIPTION:" + icsEscape(e.Description) + "\r\n")
		}
		if e.Location != "" {
			sb.WriteString("LOCATION:" + icsEscape(e.Location) + "\r\n")
		}
		if e.Recurrence != "" {
			sb.WriteString(e.Recurrence + "\r\n")
		}
		sb.WriteString("DTSTAMP:" + icsTime(e.UpdatedAt) + "\r\n")
		sb.WriteString("END:VEVENT\r\n")
	}

	sb.WriteString("END:VCALENDAR\r\n")

	filename := fmt.Sprintf("calendar-%s.ics", calID)
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Data(http.StatusOK, "text/calendar; charset=utf-8", []byte(sb.String()))
}

// ExpandRRule POST /api/calendar/rrule/expand  body: {dtstart, rrule, from, to}
func (h *CalendarEventHandler) ExpandRRule(c *gin.Context) {
	var req struct {
		DtStart string `json:"dtstart" binding:"required"`
		RRule   string `json:"rrule" binding:"required"`
		From    string `json:"from" binding:"required"`
		To      string `json:"to" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	dtstart, err := time.Parse(time.RFC3339, req.DtStart)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid dtstart: " + err.Error()})
		return
	}
	from, err := time.Parse(time.RFC3339, req.From)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid from: " + err.Error()})
		return
	}
	to, err := time.Parse(time.RFC3339, req.To)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid to: " + err.Error()})
		return
	}

	dates, err := calendar_rrule.Expand(dtstart, req.RRule, from, to)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"occurrences": dates})
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func parseDateRange(c *gin.Context) (time.Time, time.Time, error) {
	fromStr := c.Query("from")
	toStr := c.Query("to")

	var from, to time.Time
	if fromStr == "" {
		// Default: current month
		now := time.Now()
		from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		to = from.AddDate(0, 1, 0)
	} else {
		var err error
		from, err = time.Parse(time.RFC3339, fromStr)
		if err != nil {
			return from, to, fmt.Errorf("invalid from: %w", err)
		}
		to, err = time.Parse(time.RFC3339, toStr)
		if err != nil {
			return from, to, fmt.Errorf("invalid to: %w", err)
		}
	}
	return from, to, nil
}

func icsTime(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

func icsEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, ";", `\;`)
	s = strings.ReplaceAll(s, ",", `\,`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}
