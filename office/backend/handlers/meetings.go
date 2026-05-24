// meetings.go — OFFICE-65: Scheduled meetings + meeting rooms.
//
// Routes (all under /api/meetings, protected by optional auth middleware):
//   POST   /api/meetings              — create a meeting room / schedule
//   GET    /api/meetings              — list all meetings
//   GET    /api/meetings/:id          — get a single meeting
//   DELETE /api/meetings/:id          — delete a meeting
//
// A join URL is also exposed publicly so external invitees can navigate
// directly to the room:
//   GET    /api/meetings/:id/join     — resolve meeting + return join metadata

package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"vulos-office/backend/models"
	"vulos-office/backend/storage"

	"github.com/gin-gonic/gin"
)

// MeetingHandler handles CRUD + join for scheduled meeting rooms.
type MeetingHandler struct {
	store storage.Storage
}

func NewMeetingHandler(store storage.Storage) *MeetingHandler {
	return &MeetingHandler{store: store}
}

// newID generates a short random hex id.
func newMeetingID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// POST /api/meetings
func (h *MeetingHandler) Create(c *gin.Context) {
	var req models.CreateMeetingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	id, err := newMeetingID()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "id generation failed"})
		return
	}

	// The session_id is the fabric session key: callers join via createCall({sessionId})
	// in rtc.js — we derive it deterministically from the meeting id so the join link is
	// stable across server restarts.
	sessionID := "meeting:" + id

	joinLink := fmt.Sprintf("/room/%s", sessionID)

	invitees := req.Invitees
	if invitees == nil {
		invitees = []string{}
	}

	m := &models.Meeting{
		ID:          id,
		Title:       strings.TrimSpace(req.Title),
		SessionID:   sessionID,
		HostVumail:  strings.TrimSpace(req.HostVumail),
		Invitees:    invitees,
		ScheduledAt: req.ScheduledAt,
		DurationMin: req.DurationMin,
		Status:      models.MeetingStatusScheduled,
		JoinLink:    joinLink,
	}

	if err := h.store.CreateMeeting(m); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, m)
}

// GET /api/meetings
func (h *MeetingHandler) List(c *gin.Context) {
	meetings, err := h.store.ListMeetings()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if meetings == nil {
		meetings = []*models.Meeting{}
	}
	c.JSON(http.StatusOK, meetings)
}

// GET /api/meetings/:id
func (h *MeetingHandler) Get(c *gin.Context) {
	id := c.Param("id")
	m, err := h.store.GetMeeting(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
		return
	}
	c.JSON(http.StatusOK, m)
}

// DELETE /api/meetings/:id
func (h *MeetingHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	if err := h.store.DeleteMeeting(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GET /api/meetings/:id/join
// Returns the meeting metadata plus the session_id to pass into createCall.
// This endpoint is intentionally not behind auth so external invitees can join
// via a bare link (the host can implement lobby/admit logic in the Room UI).
func (h *MeetingHandler) Join(c *gin.Context) {
	id := c.Param("id")
	m, err := h.store.GetMeeting(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "meeting not found"})
		return
	}

	// Transition status to active on first join if the meeting is still scheduled
	// and within 15 minutes of its scheduled time (or has no scheduled time).
	if m.Status == models.MeetingStatusScheduled {
		shouldActivate := m.ScheduledAt == nil
		if !shouldActivate && m.ScheduledAt != nil {
			diff := time.Until(*m.ScheduledAt)
			shouldActivate = diff <= 15*time.Minute
		}
		if shouldActivate {
			m.Status = models.MeetingStatusActive
			_ = h.store.UpdateMeeting(m)
		}
	}

	c.JSON(http.StatusOK, models.MeetingJoinResponse{
		Meeting:   m,
		SessionID: m.SessionID,
		JoinLink:  m.JoinLink,
	})
}
