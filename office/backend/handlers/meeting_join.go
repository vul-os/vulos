// meeting_join.go — OFFICE-MEET: Join token issuance + lobby endpoints.
//
// Routes:
//   POST /api/meet/:roomId/token     — issue a signed join token (authenticated or anon)
//   GET  /api/meet/:roomId/lobby     — list waiting participants (organizer only)
//   POST /api/meet/:roomId/admit     — admit one participant by nonce (organizer only)
//   POST /api/meet/:roomId/admit-all — bulk admit all waiting (organizer only)
//   POST /api/meet/:roomId/deny      — deny one participant by nonce (organizer only)
//
// Security model:
//   - Join tokens: HMAC-SHA256, 1-hour TTL, nonce-based (single-use enforced client-side;
//     server validates exp + sig).
//   - Anonymous joins: placed in lobby when lobby_required=true; require organizer admit.
//   - All join events audit-logged.
//   - Rate-limited at /meet/* (GlobalLimiter in services/meeting).

package handlers

import (
	"net/http"
	"regexp"
	"strings"

	meetingsvc "vulos-office/backend/services/meeting"

	"github.com/gin-gonic/gin"
)

// roomIDRe validates that a room ID is exactly 22 URL-safe base64 characters.
// Case-sensitive matching is enforced to preserve the full 132-bit entropy of
// the room ID (lowercasing would reduce the alphabet and entropy).
// Format: ^[A-Za-z0-9_-]{22}$
var roomIDRe = regexp.MustCompile(`^[A-Za-z0-9_\-]{22}$`)

func validRoomID(roomID string) bool {
	return roomIDRe.MatchString(roomID)
}

// MeetJoinHandler issues join tokens and manages lobby state.
type MeetJoinHandler struct {
	scheduleHandler *MeetScheduleHandler
}

func NewMeetJoinHandler(sh *MeetScheduleHandler) *MeetJoinHandler {
	return &MeetJoinHandler{scheduleHandler: sh}
}

// POST /api/meet/:roomId/token
// Body: { display_name: string, email?: string }
// Returns: { token: string, lobby_required: bool, room_id: string }
func (h *MeetJoinHandler) IssueToken(c *gin.Context) {
	// Rate limit check
	if !meetingsvc.GlobalLimiter().Allow(c.ClientIP()) {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many requests"})
		return
	}

	roomID := c.Param("roomId")
	if roomID == "" || !validRoomID(roomID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room_id: must be 22 URL-safe base64 characters [A-Za-z0-9_-]"})
		return
	}

	var body struct {
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
	}
	_ = c.ShouldBindJSON(&body) // optional body fields
	body.DisplayName = strings.TrimSpace(body.DisplayName)

	accountID := c.GetString("userID")

	// Validate room exists
	unlock := h.scheduleHandler.lock()
	meeting, exists := h.scheduleHandler.meetings[roomID]
	lobbyRequired := false
	signinRequired := false
	if exists {
		lobbyRequired = meeting.LobbyRequired
		signinRequired = meeting.SigninRequired
	}
	unlock()

	// signin_required: anonymous join denied
	if signinRequired && accountID == "" {
		c.JSON(http.StatusForbidden, gin.H{
			"error":             "this meeting requires a signed-in account",
			"signin_required":   true,
		})
		return
	}

	token, err := meetingsvc.IssueJoinToken(roomID, accountID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token issuance failed"})
		return
	}

	// Audit: join attempt
	meetingsvc.GlobalAuditLog().Append(&meetingsvc.JoinAuditEvent{
		RoomID:    roomID,
		AccountID: accountID,
		IP:        c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
		Action:    "token-issued",
	})

	c.JSON(http.StatusOK, gin.H{
		"token":          token,
		"room_id":        roomID,
		"lobby_required": lobbyRequired,
	})
}

// POST /api/meet/:roomId/lobby/enter
// Called by a participant who has a valid token and is entering the lobby.
func (h *MeetJoinHandler) LobbyEnter(c *gin.Context) {
	roomID := c.Param("roomId")
	if !validRoomID(roomID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid room_id"})
		return
	}
	tokenStr := c.GetHeader("X-Meet-Token")
	if tokenStr == "" {
		// Also accept from body
		var body struct {
			Token       string `json:"token"`
			DisplayName string `json:"display_name"`
			Email       string `json:"email"`
		}
		if err := c.ShouldBindJSON(&body); err != nil || body.Token == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "token required"})
			return
		}
		tokenStr = body.Token
	}

	claims, err := meetingsvc.VerifyJoinToken(tokenStr)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	if claims.RoomID != roomID {
		c.JSON(http.StatusForbidden, gin.H{"error": "token not valid for this room"})
		return
	}
	if meetingsvc.Default().IsDenied(roomID, claims.Nonce) {
		c.JSON(http.StatusForbidden, gin.H{"error": "entry denied"})
		return
	}

	// Enforce per-room participant cap at the app layer.
	if meetingsvc.ParticipantCount(roomID) >= meetingsvc.MaxRoomPeers {
		c.Header("Retry-After", "30")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":       "room is at capacity",
			"max_peers":   meetingsvc.MaxRoomPeers,
			"retry_after": 30,
		})
		return
	}

	entry := &meetingsvc.WaitingEntry{
		Nonce:     claims.Nonce,
		AccountID: claims.AccountID,
		IP:        c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
	}
	meetingsvc.Default().Enter(roomID, entry)

	meetingsvc.GlobalAuditLog().Append(&meetingsvc.JoinAuditEvent{
		RoomID:    roomID,
		AccountID: claims.AccountID,
		IP:        c.ClientIP(),
		UserAgent: c.Request.UserAgent(),
		Action:    "waiting",
	})

	c.JSON(http.StatusOK, gin.H{"waiting": true, "nonce": claims.Nonce})
}

// GET /api/meet/:roomId/lobby  (organizer only)
func (h *MeetJoinHandler) LobbyList(c *gin.Context) {
	roomID := c.Param("roomId")
	callerID := c.GetString("userID")

	if !h.isOrganizer(roomID, callerID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "organizer only"})
		return
	}

	waiting := meetingsvc.Default().List(roomID)
	c.JSON(http.StatusOK, gin.H{"waiting": waiting})
}

// POST /api/meet/:roomId/admit  (organizer only)
// Body: { nonce: string }
func (h *MeetJoinHandler) Admit(c *gin.Context) {
	roomID := c.Param("roomId")
	callerID := c.GetString("userID")

	if !h.isOrganizer(roomID, callerID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "organizer only"})
		return
	}

	var body struct {
		Nonce string `json:"nonce" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	admitted := meetingsvc.Default().Admit(roomID, body.Nonce)
	if !admitted {
		c.JSON(http.StatusNotFound, gin.H{"error": "nonce not found in lobby"})
		return
	}

	meetingsvc.GlobalAuditLog().Append(&meetingsvc.JoinAuditEvent{
		RoomID:     roomID,
		IP:         c.ClientIP(),
		Action:     "admitted",
		AcceptedBy: callerID,
	})

	c.JSON(http.StatusOK, gin.H{"admitted": true})
}

// POST /api/meet/:roomId/admit-all  (organizer only)
func (h *MeetJoinHandler) AdmitAll(c *gin.Context) {
	roomID := c.Param("roomId")
	callerID := c.GetString("userID")

	if !h.isOrganizer(roomID, callerID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "organizer only"})
		return
	}

	admitted := meetingsvc.Default().AdmitAll(roomID)
	for _, e := range admitted {
		meetingsvc.GlobalAuditLog().Append(&meetingsvc.JoinAuditEvent{
			RoomID:     roomID,
			AccountID:  e.AccountID,
			IP:         e.IP,
			Action:     "admitted",
			AcceptedBy: callerID,
		})
	}
	c.JSON(http.StatusOK, gin.H{"admitted_count": len(admitted)})
}

// POST /api/meet/:roomId/deny  (organizer only)
// Body: { nonce: string }
func (h *MeetJoinHandler) Deny(c *gin.Context) {
	roomID := c.Param("roomId")
	callerID := c.GetString("userID")

	if !h.isOrganizer(roomID, callerID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "organizer only"})
		return
	}

	var body struct {
		Nonce string `json:"nonce" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	meetingsvc.Default().Deny(roomID, body.Nonce)

	meetingsvc.GlobalAuditLog().Append(&meetingsvc.JoinAuditEvent{
		RoomID:     roomID,
		IP:         c.ClientIP(),
		Action:     "denied",
		AcceptedBy: callerID,
	})
	c.JSON(http.StatusOK, gin.H{"denied": true})
}

// isOrganizer checks whether callerID is the organizer of the given room.
func (h *MeetJoinHandler) isOrganizer(roomID, callerID string) bool {
	if h.scheduleHandler == nil {
		return false
	}
	unlock := h.scheduleHandler.lock()
	m, exists := h.scheduleHandler.meetings[roomID]
	unlock()
	if !exists {
		return false
	}
	return m.OrganizerID == callerID
}
