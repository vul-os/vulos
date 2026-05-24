package handlers

// OFFICE-28: Document activity feed + named snapshots from op-log.
//
// Routes:
//   GET  /api/files/:id/activity              — chronological merged feed
//   POST /api/files/:id/versions              — create a named snapshot
//   PUT  /api/files/:id/versions/:vid/label   — set/update label on an existing version

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"vulos-office/backend/models"
	"vulos-office/backend/storage"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ActivityHandler serves the activity feed and named-snapshot endpoints.
type ActivityHandler struct {
	store storage.Storage
}

func NewActivityHandler(store storage.Storage) *ActivityHandler {
	return &ActivityHandler{store: store}
}

// GetActivity handles GET /api/files/:id/activity.
// It merges version snapshots, comments, and signing audit events into a
// single chronological feed of ActivityEvent objects.
func (h *ActivityHandler) GetActivity(c *gin.Context) {
	fileID := c.Param("id")

	if _, err := h.store.GetFile(fileID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}

	var events []models.ActivityEvent

	// --- Version / edit events ---
	versions, _ := h.store.ListVersions(fileID)
	for _, v := range versions {
		kind := models.ActivityEdit
		summary := fmt.Sprintf("Document edited — saved as \"%s\"", v.Name)
		if v.Label != "" {
			kind = models.ActivitySnapshot
			summary = fmt.Sprintf("Named snapshot: %s", v.Label)
		}
		events = append(events, models.ActivityEvent{
			Kind:      kind,
			ID:        "v-" + v.ID,
			FileID:    fileID,
			Summary:   summary,
			Label:     v.Label,
			RefID:     v.ID,
			Timestamp: v.CreatedAt,
		})
	}

	// --- Comment events ---
	comments, _ := h.store.ListComments(fileID)
	for _, cm := range comments {
		author := cm.AuthorID
		if author == "" {
			author = "anonymous"
		}
		events = append(events, models.ActivityEvent{
			Kind:      models.ActivityComment,
			ID:        "c-" + cm.ID,
			FileID:    fileID,
			Author:    author,
			Summary:   fmt.Sprintf("Comment added by %s", author),
			RefID:     cm.ID,
			Timestamp: cm.CreatedAt,
		})
	}

	// --- Signing audit events (if any envelopes reference this file) ---
	envelopes, _ := h.store.ListEnvelopes()
	for _, env := range envelopes {
		if env.SourceFileID != fileID {
			continue
		}
		auditEvents, _ := h.store.ListAuditEvents(env.ID)
		for _, ae := range auditEvents {
			events = append(events, models.ActivityEvent{
				Kind:      models.ActivitySign,
				ID:        "s-" + ae.ID,
				FileID:    fileID,
				Author:    ae.Identity,
				Summary:   fmt.Sprintf("Signing event: %s by %s", ae.Action, ae.Identity),
				RefID:     ae.ID,
				Timestamp: ae.Timestamp,
			})
		}
	}

	// Sort chronologically (oldest first).
	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})

	if events == nil {
		events = []models.ActivityEvent{}
	}
	c.JSON(http.StatusOK, events)
}

// CreateNamedSnapshot handles POST /api/files/:id/versions.
// Body: { "label": "v1 final draft" }
// Creates a snapshot of the current file content with the given label.
type createSnapshotRequest struct {
	Label string `json:"label" binding:"required"`
}

func (h *ActivityHandler) CreateNamedSnapshot(c *gin.Context) {
	fileID := c.Param("id")

	file, err := h.store.GetFile(fileID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}

	var req createSnapshotRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	snap := &models.FileVersion{
		ID:        uuid.New().String(),
		FileID:    fileID,
		Name:      file.Name,
		Label:     req.Label,
		Content:   file.Content,
		CreatedAt: time.Now(),
	}

	if err := h.store.CreateVersion(snap); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_ = h.store.PruneVersions(fileID, storage.DefaultVersionCap)

	c.JSON(http.StatusCreated, snap)
}

// LabelVersion handles PUT /api/files/:id/versions/:vid/label.
// Body: { "label": "v1 final draft" }
func (h *ActivityHandler) LabelVersion(c *gin.Context) {
	fileID := c.Param("id")
	versionID := c.Param("vid")

	var req models.LabelVersionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if err := h.store.LabelVersion(fileID, versionID, req.Label); err != nil {
		if err.Error() == "version not found" {
			c.JSON(http.StatusNotFound, gin.H{"error": "version not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	v, _ := h.store.GetVersion(fileID, versionID)
	c.JSON(http.StatusOK, v)
}
