package handlers

import (
	"net/http"

	"vulos-office/backend/models"
	"vulos-office/backend/storage"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type FileHandler struct {
	store storage.Storage
}

func NewFileHandler(store storage.Storage) *FileHandler {
	return &FileHandler{store: store}
}

func (h *FileHandler) List(c *gin.Context) {
	files, err := h.store.ListFiles()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if files == nil {
		files = []*models.File{}
	}
	c.JSON(http.StatusOK, files)
}

func (h *FileHandler) Get(c *gin.Context) {
	file, err := h.store.GetFile(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}
	c.JSON(http.StatusOK, file)
}

func (h *FileHandler) Create(c *gin.Context) {
	var req models.CreateFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	file := &models.File{
		ID:      uuid.New().String(),
		Name:    req.Name,
		Type:    req.Type,
		Content: req.Content,
	}

	if err := h.store.CreateFile(file); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, file)
}

func (h *FileHandler) Update(c *gin.Context) {
	var req models.UpdateFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	file := &models.File{
		ID:      c.Param("id"),
		Name:    req.Name,
		Content: req.Content,
	}

	if err := h.store.UpdateFile(file); err != nil {
		if err.Error() == "file not found" {
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	updated, _ := h.store.GetFile(file.ID)
	c.JSON(http.StatusOK, updated)
}

func (h *FileHandler) Delete(c *gin.Context) {
	if err := h.store.DeleteFile(c.Param("id")); err != nil {
		if err.Error() == "file not found" {
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}
