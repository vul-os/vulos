package handlers

import (
	"net/http"
	"time"

	"vulos-office/backend/models"
	"vulos-office/backend/storage"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// VersionHandler serves GET /api/files/:id/versions and
// POST /api/files/:id/versions/:vid/restore.
type VersionHandler struct {
	store storage.Storage
}

func NewVersionHandler(store storage.Storage) *VersionHandler {
	return &VersionHandler{store: store}
}

// ListVersions handles GET /api/files/:id/versions.
func (h *VersionHandler) ListVersions(c *gin.Context) {
	fileID := c.Param("id")
	// Verify file exists.
	if _, err := h.store.GetFile(fileID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}

	versions, err := h.store.ListVersions(fileID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if versions == nil {
		versions = []*models.FileVersion{}
	}
	c.JSON(http.StatusOK, versions)
}

// RestoreVersion handles POST /api/files/:id/versions/:vid/restore.
// It creates a new snapshot of the current content, then replaces the
// file content with the chosen version's content.
func (h *VersionHandler) RestoreVersion(c *gin.Context) {
	fileID := c.Param("id")
	versionID := c.Param("vid")

	file, err := h.store.GetFile(fileID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}

	v, err := h.store.GetVersion(fileID, versionID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "version not found"})
		return
	}

	// Snapshot the current state before restore so it can be undone.
	snap := &models.FileVersion{
		ID:        uuid.New().String(),
		FileID:    fileID,
		Name:      file.Name,
		Content:   file.Content,
		CreatedAt: time.Now(),
	}
	_ = h.store.CreateVersion(snap)
	_ = h.store.PruneVersions(fileID, storage.DefaultVersionCap)

	// Write the restored content back.
	file.Content = v.Content
	file.Name = v.Name
	if err := h.store.UpdateFile(file); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	updated, _ := h.store.GetFile(fileID)
	c.JSON(http.StatusOK, updated)
}
