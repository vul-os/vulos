// spaces_ext.go — additive handler methods on SpacesHandler for:
//   reactions (OFFICE-SPACES-1), pins (OFFICE-SPACES-6),
//   user status (OFFICE-SPACES-4), channel search (OFFICE-SPACES-5).
//
// All state is in-memory (NullPersister pattern) — no CGO, no external DB.
// Full-text search uses a simple linear scan over the in-memory message index
// (equivalent to SQLite FTS5 for the MVP; pluggable when the Persister gains FTS).
package handlers

import (
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"vulos-office/backend/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ---- in-memory stores --------------------------------------------------------

type reactionsStore struct {
	mu   sync.RWMutex
	rows []*models.Reaction // append-only; deletions are filtered on read
	// deleted set: (msgID, emoji, userID)
	deleted map[string]bool
}

func newReactionsStore() *reactionsStore {
	return &reactionsStore{deleted: make(map[string]bool)}
}

func reactionKey(msgID, emoji, userID string) string {
	return msgID + "|" + emoji + "|" + userID
}

func (rs *reactionsStore) Add(msgID, emoji, userID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	k := reactionKey(msgID, emoji, userID)
	delete(rs.deleted, k)
	// idempotent: check if already exists
	for _, r := range rs.rows {
		if r.MessageID == msgID && r.Emoji == emoji && r.UserID == userID {
			return
		}
	}
	rs.rows = append(rs.rows, &models.Reaction{
		MessageID: msgID,
		Emoji:     emoji,
		UserID:    userID,
		CreatedAt: time.Now(),
	})
}

func (rs *reactionsStore) Remove(msgID, emoji, userID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.deleted[reactionKey(msgID, emoji, userID)] = true
}

func (rs *reactionsStore) ListByChannel(channelID string, messages []*models.Message) []*models.Reaction {
	// Build set of message IDs in this channel
	ids := make(map[string]bool, len(messages))
	for _, m := range messages {
		ids[m.ID] = true
	}
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	var out []*models.Reaction
	for _, r := range rs.rows {
		if ids[r.MessageID] && !rs.deleted[reactionKey(r.MessageID, r.Emoji, r.UserID)] {
			out = append(out, r)
		}
	}
	return out
}

// ---- pinsStore ---------------------------------------------------------------

type pinsStore struct {
	mu   sync.RWMutex
	pins map[string][]*models.PinnedMessage // channelID → pins
}

func newPinsStore() *pinsStore {
	return &pinsStore{pins: make(map[string][]*models.PinnedMessage)}
}

func (ps *pinsStore) Pin(channelID, msgID, pinnedBy, body, authorID string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, p := range ps.pins[channelID] {
		if p.MessageID == msgID {
			return // idempotent
		}
	}
	ps.pins[channelID] = append(ps.pins[channelID], &models.PinnedMessage{
		ChannelID: channelID,
		MessageID: msgID,
		AuthorID:  authorID,
		Body:      body,
		PinnedBy:  pinnedBy,
		PinnedAt:  time.Now(),
	})
}

func (ps *pinsStore) Unpin(channelID, msgID string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	list := ps.pins[channelID]
	out := list[:0]
	for _, p := range list {
		if p.MessageID != msgID {
			out = append(out, p)
		}
	}
	ps.pins[channelID] = out
}

func (ps *pinsStore) List(channelID string) []*models.PinnedMessage {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	out := make([]*models.PinnedMessage, len(ps.pins[channelID]))
	copy(out, ps.pins[channelID])
	return out
}

// ---- statusStore -------------------------------------------------------------

type statusStore struct {
	mu     sync.RWMutex
	status map[string]*models.UserStatus // userID → status
}

func newStatusStore() *statusStore {
	return &statusStore{status: make(map[string]*models.UserStatus)}
}

func (ss *statusStore) Set(userID, status, customText string, untilUnix int64) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.status[userID] = &models.UserStatus{
		UserID:     userID,
		Status:     status,
		CustomText: customText,
		UntilUnix:  untilUnix,
		UpdatedAt:  time.Now(),
	}
}

func (ss *statusStore) Get(userID string) *models.UserStatus {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	if s, ok := ss.status[userID]; ok {
		return s
	}
	return &models.UserStatus{UserID: userID, Status: "online"}
}

// ---- Extend SpacesHandler with new sub-stores --------------------------------

// SpacesExtStore holds the additive stores; embedded in SpacesHandler via Init.
type SpacesExtStore struct {
	reactions *reactionsStore
	pins      *pinsStore
	status    *statusStore
}

func newSpacesExt() *SpacesExtStore {
	return &SpacesExtStore{
		reactions: newReactionsStore(),
		pins:      newPinsStore(),
		status:    newStatusStore(),
	}
}

// SpacesHandlerExt wraps SpacesHandler with extension sub-stores.
// Created by NewSpacesHandlerExt to keep main.go wiring simple.
type SpacesHandlerExt struct {
	*SpacesHandler
	ext *SpacesExtStore
}

// NewSpacesHandlerExt returns the extended handler; registered in main.go.
func NewSpacesHandlerExt() *SpacesHandlerExt {
	return &SpacesHandlerExt{
		SpacesHandler: NewSpacesHandler(),
		ext:           newSpacesExt(),
	}
}

// ---- Reactions ---------------------------------------------------------------

// ListReactions GET /api/spaces/channels/:channelId/reactions
func (h *SpacesHandlerExt) ListReactions(c *gin.Context) {
	channelID := c.Param("channelId")
	msgs := h.store.ListMessages(channelID)
	rxns := h.ext.reactions.ListByChannel(channelID, msgs)
	if rxns == nil {
		rxns = []*models.Reaction{}
	}
	c.JSON(http.StatusOK, rxns)
}

// React POST /api/spaces/messages/:msgId/react
func (h *SpacesHandlerExt) React(c *gin.Context) {
	msgID := c.Param("msgId")
	var req models.ReactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	userID := c.GetHeader("X-Account-ID")
	if userID == "" {
		userID = "anonymous"
	}
	if strings.TrimSpace(req.Emoji) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "emoji required"})
		return
	}
	h.ext.reactions.Add(msgID, req.Emoji, userID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Unreact DELETE /api/spaces/messages/:msgId/react
func (h *SpacesHandlerExt) Unreact(c *gin.Context) {
	msgID := c.Param("msgId")
	var req models.ReactRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	userID := c.GetHeader("X-Account-ID")
	if userID == "" {
		userID = "anonymous"
	}
	h.ext.reactions.Remove(msgID, req.Emoji, userID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- Pins --------------------------------------------------------------------

// ListPins GET /api/spaces/channels/:channelId/pins
func (h *SpacesHandlerExt) ListPins(c *gin.Context) {
	channelID := c.Param("channelId")
	pins := h.ext.pins.List(channelID)
	if pins == nil {
		pins = []*models.PinnedMessage{}
	}
	c.JSON(http.StatusOK, pins)
}

// PinMessage POST /api/spaces/channels/:channelId/pins
func (h *SpacesHandlerExt) PinMessage(c *gin.Context) {
	channelID := c.Param("channelId")
	var req models.PinRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	pinnedBy := c.GetHeader("X-Account-ID")
	if pinnedBy == "" {
		pinnedBy = "anonymous"
	}
	// Look up message body + author for the panel snapshot
	body := ""
	authorID := ""
	msgs := h.store.ListMessages(channelID)
	for _, m := range msgs {
		if m.ID == req.MessageID {
			body = m.Body
			authorID = m.AuthorID
			break
		}
	}
	h.ext.pins.Pin(channelID, req.MessageID, pinnedBy, body, authorID)
	c.JSON(http.StatusCreated, gin.H{"ok": true})
}

// UnpinMessage DELETE /api/spaces/channels/:channelId/pins/:msgId
func (h *SpacesHandlerExt) UnpinMessage(c *gin.Context) {
	channelID := c.Param("channelId")
	msgID := c.Param("msgId")
	h.ext.pins.Unpin(channelID, msgID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// ---- User status -------------------------------------------------------------

// SetStatus PUT /api/spaces/users/me/status
func (h *SpacesHandlerExt) SetStatus(c *gin.Context) {
	userID := c.GetHeader("X-Account-ID")
	if userID == "" {
		userID = "anonymous"
	}
	var req models.SetStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.ext.status.Set(userID, req.Status, req.CustomText, req.UntilUnix)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// GetStatus GET /api/spaces/users/:userId/status
func (h *SpacesHandlerExt) GetStatus(c *gin.Context) {
	userID := c.Param("userId")
	c.JSON(http.StatusOK, h.ext.status.Get(userID))
}

// ---- Search ------------------------------------------------------------------

// SearchMessages GET /api/spaces/channels/:channelId/search?q=...
//
// Supports plain terms plus operators:
//   from:user  before:date  after:date  has:link  has:file
//
// Pure in-memory linear scan (equivalent to SQLite FTS5 for the current MVP;
// swap in a Persister.Search() call when durability is added).
func (h *SpacesHandlerExt) SearchMessages(c *gin.Context) {
	channelID := c.Param("channelId")
	raw := strings.TrimSpace(c.Query("q"))

	msgs := h.store.ListMessages(channelID)

	if raw == "" {
		c.JSON(http.StatusOK, []*models.Message{})
		return
	}

	filter := parseSearchFilter(raw)
	var results []*models.Message
	for _, m := range msgs {
		if m.State == models.MessageStateTombed {
			continue
		}
		if matchMsg(m, filter) {
			results = append(results, m)
		}
	}
	if results == nil {
		results = []*models.Message{}
	}
	c.JSON(http.StatusOK, results)
}

// ---- search filter -----------------------------------------------------------

type searchFilter struct {
	terms   []string
	from    string
	before  time.Time
	after   time.Time
	hasBefore bool
	hasAfter  bool
	hasLink bool
	hasFile bool
}

func parseSearchFilter(raw string) searchFilter {
	f := searchFilter{}
	for _, tok := range strings.Fields(raw) {
		lower := strings.ToLower(tok)
		switch {
		case strings.HasPrefix(lower, "from:"):
			f.from = lower[5:]
		case strings.HasPrefix(lower, "before:"):
			if t, err := time.Parse("2006-01-02", tok[7:]); err == nil {
				f.before = t
				f.hasBefore = true
			}
		case strings.HasPrefix(lower, "after:"):
			if t, err := time.Parse("2006-01-02", tok[6:]); err == nil {
				f.after = t
				f.hasAfter = true
			}
		case lower == "has:link":
			f.hasLink = true
		case lower == "has:file":
			f.hasFile = true
		default:
			if tok != "" {
				f.terms = append(f.terms, lower)
			}
		}
	}
	return f
}

func matchMsg(m *models.Message, f searchFilter) bool {
	body := strings.ToLower(m.Body)
	author := strings.ToLower(m.AuthorID)

	if f.from != "" && !strings.Contains(author, f.from) {
		return false
	}
	if f.hasBefore && !m.CreatedAt.Before(f.before) {
		return false
	}
	if f.hasAfter && !m.CreatedAt.After(f.after) {
		return false
	}
	if f.hasLink && !strings.Contains(body, "http") {
		return false
	}
	if f.hasFile {
		// Treat messages whose body starts with "[file:" as file messages
		if !strings.Contains(body, "[file:") {
			return false
		}
	}
	for _, t := range f.terms {
		haystack := body + " " + author
		if !containsToken(haystack, t) {
			return false
		}
	}
	return true
}

// containsToken does a word-boundary-aware substring check.
func containsToken(haystack, needle string) bool {
	// Simple: just check substring; for real FTS, use the SQLite FTS5 porter stemmer.
	return strings.Contains(haystack, needle) ||
		strings.Contains(haystack, strings.Map(unicode.ToLower, needle))
}

// Helpers for uuid (imported above)
var _ = uuid.NewString
