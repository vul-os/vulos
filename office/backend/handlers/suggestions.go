package handlers

// OFFICE-27: Suggestions (track-changes) handler.
//
// REST endpoints (all scoped to a file):
//   GET    /api/files/:id/suggestions              → list suggestions
//   POST   /api/files/:id/suggestions              → create a suggestion
//   PUT    /api/files/:id/suggestions/:sid         → accept or reject
//   DELETE /api/files/:id/suggestions/:sid         → remove a suggestion

import (
	"net/http"

	"vulos-office/backend/models"
	"vulos-office/backend/storage"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type SuggestionHandler struct {
	store storage.Storage
}

func NewSuggestionHandler(store storage.Storage) *SuggestionHandler {
	return &SuggestionHandler{store: store}
}

// List returns all suggestions for a file.
func (h *SuggestionHandler) List(c *gin.Context) {
	fileID := c.Param("id")
	suggestions, err := h.store.ListSuggestions(fileID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if suggestions == nil {
		suggestions = []*models.Suggestion{}
	}
	c.JSON(http.StatusOK, suggestions)
}

// Create records a new suggestion (insert or delete proposal).
func (h *SuggestionHandler) Create(c *gin.Context) {
	var req models.CreateSuggestionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	sg := &models.Suggestion{
		ID:       uuid.New().String(),
		FileID:   c.Param("id"),
		Kind:     req.Kind,
		State:    models.SuggestionPending,
		AuthorID: req.AuthorID,
		From:     req.From,
		To:       req.To,
		Text:     req.Text,
		SeqClock: hlcNow(), // reuse from comments.go (same package)
	}

	if err := h.store.CreateSuggestion(sg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, sg)
}

// Update accepts or rejects a suggestion.
func (h *SuggestionHandler) Update(c *gin.Context) {
	fileID := c.Param("id")
	suggestionID := c.Param("sid")

	sg, err := h.store.GetSuggestion(fileID, suggestionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "suggestion not found"})
		return
	}

	var req models.UpdateSuggestionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.State != models.SuggestionAccepted && req.State != models.SuggestionRejected {
		c.JSON(http.StatusBadRequest, gin.H{"error": "state must be 'accepted' or 'rejected'"})
		return
	}

	sg.State = req.State
	sg.ReviewerID = req.ReviewerID
	sg.SeqClock = hlcNow()

	if err := h.store.UpdateSuggestion(sg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, sg)
}

// Delete removes a suggestion record.
func (h *SuggestionHandler) Delete(c *gin.Context) {
	fileID := c.Param("id")
	suggestionID := c.Param("sid")
	if err := h.store.DeleteSuggestion(fileID, suggestionID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "suggestion not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}
