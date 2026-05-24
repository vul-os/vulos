package handlers

import (
	"net/http"
	"sync"

	"vulos-office/backend/spaces"
	"vulos-office/backend/models"

	"github.com/gin-gonic/gin"
)

// SpacesHandler exposes a REST façade over the in-process SpacesStore.
// The SpacesStore is the single source of truth; no separate DB is needed
// for the MVP (OFFICE-60 wires in durable Persisters).
type SpacesHandler struct {
	mu    sync.RWMutex
	store *spaces.SpacesStore
}

func NewSpacesHandler() *SpacesHandler {
	p := spaces.NewNullPersister()
	s, _ := spaces.Open("server", p)
	h := &SpacesHandler{store: s}
	// Seed a default general channel so the UI has something to show.
	_, _ = s.CreateChannelWithID("general", "general", models.ChannelTypePublic, "system")
	return h
}

// -------------------------------------------------------------------------
// Channels
// -------------------------------------------------------------------------

func (h *SpacesHandler) ListChannels(c *gin.Context) {
	chs := h.store.ListChannels()
	if chs == nil {
		chs = []*models.Channel{}
	}
	c.JSON(http.StatusOK, chs)
}

func (h *SpacesHandler) CreateChannel(c *gin.Context) {
	var req models.CreateChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctype := req.Type
	if ctype == "" {
		ctype = models.ChannelTypePublic
	}
	requester := c.GetHeader("X-Account-ID")
	if requester == "" {
		requester = "anonymous"
	}
	ch, err := h.store.CreateChannel(req.Name, ctype, requester)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Auto-join the creator.
	if len(req.Members) == 0 {
		req.Members = []string{requester}
	}
	for _, accountID := range req.Members {
		_, _ = h.store.AddMember(ch.ID, accountID)
	}
	c.JSON(http.StatusCreated, ch)
}

// -------------------------------------------------------------------------
// Membership
// -------------------------------------------------------------------------

func (h *SpacesHandler) JoinChannel(c *gin.Context) {
	channelID := c.Param("channelId")
	accountID := c.GetHeader("X-Account-ID")
	if accountID == "" {
		accountID = "anonymous"
	}
	m, err := h.store.AddMember(channelID, accountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, m)
}

func (h *SpacesHandler) ListMembers(c *gin.Context) {
	channelID := c.Param("channelId")
	members := h.store.ListMembers(channelID)
	if members == nil {
		members = []*models.Membership{}
	}
	c.JSON(http.StatusOK, members)
}

// -------------------------------------------------------------------------
// Messages
// -------------------------------------------------------------------------

func (h *SpacesHandler) ListMessages(c *gin.Context) {
	channelID := c.Param("channelId")
	msgs := h.store.ListMessages(channelID)
	if msgs == nil {
		msgs = []*models.Message{}
	}
	c.JSON(http.StatusOK, msgs)
}

func (h *SpacesHandler) SendMessage(c *gin.Context) {
	channelID := c.Param("channelId")
	var req models.SendMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	authorID := c.GetHeader("X-Account-ID")
	if authorID == "" {
		authorID = "anonymous"
	}
	msg, err := h.store.SendMessage(channelID, authorID, req.Body, req.ThreadParent)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, msg)
}

func (h *SpacesHandler) EditMessage(c *gin.Context) {
	channelID := c.Param("channelId")
	msgID := c.Param("msgId")
	var req models.EditMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	msg, err := h.store.EditMessage(channelID, msgID, req.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, msg)
}

func (h *SpacesHandler) DeleteMessage(c *gin.Context) {
	channelID := c.Param("channelId")
	msgID := c.Param("msgId")
	if err := h.store.DeleteMessage(channelID, msgID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// -------------------------------------------------------------------------
// Read-state
// -------------------------------------------------------------------------

func (h *SpacesHandler) MarkRead(c *gin.Context) {
	channelID := c.Param("channelId")
	accountID := c.GetHeader("X-Account-ID")
	if accountID == "" {
		accountID = "anonymous"
	}
	var body struct {
		Clock string `json:"clock"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Clock == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "clock required"})
		return
	}
	if err := h.store.MarkRead(accountID, channelID, body.Clock); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *SpacesHandler) GetReadState(c *gin.Context) {
	channelID := c.Param("channelId")
	accountID := c.GetHeader("X-Account-ID")
	if accountID == "" {
		accountID = "anonymous"
	}
	rs, err := h.store.GetReadState(accountID, channelID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rs)
}

// -------------------------------------------------------------------------
// CRDT op sync (pull/push for cold-joiner catch-up)
// -------------------------------------------------------------------------

func (h *SpacesHandler) ExportOps(c *gin.Context) {
	channelID := c.Param("channelId")
	afterClock := c.Query("after")
	ops, err := h.store.ExportOps(channelID, afterClock)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if ops == nil {
		ops = []*models.MessageOp{}
	}
	c.JSON(http.StatusOK, ops)
}

func (h *SpacesHandler) MergeOps(c *gin.Context) {
	var ops []*models.MessageOp
	if err := c.ShouldBindJSON(&ops); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.store.MergeOps(ops); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"applied": len(ops)})
}
