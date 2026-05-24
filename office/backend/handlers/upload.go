package handlers

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"vulos-office/backend/config"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var allowedTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
	"image/svg+xml": true,
}

type UploadHandler struct {
	uploadsDir string
}

func NewUploadHandler(cfg *config.Config) *UploadHandler {
	os.MkdirAll(cfg.Server.UploadsDir, 0755)
	return &UploadHandler{uploadsDir: cfg.Server.UploadsDir}
}

func (h *UploadHandler) Upload(c *gin.Context) {
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no file provided"})
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	if !allowedTypes[contentType] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported file type"})
		return
	}

	ext := filepath.Ext(header.Filename)
	if ext == "" {
		ext = mimeToExt(contentType)
	}

	filename := uuid.New().String() + ext
	dst := filepath.Join(h.uploadsDir, filename)

	buf := make([]byte, 10<<20) // 10MB max
	n, _ := file.Read(buf)
	if n == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty file"})
		return
	}

	if err := os.WriteFile(dst, buf[:n], 0644); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save file"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"url":      fmt.Sprintf("/api/uploads/%s", filename),
		"filename": filename,
	})
}

func (h *UploadHandler) Serve(c *gin.Context) {
	filename := c.Param("filename")
	// Prevent path traversal
	if strings.Contains(filename, "..") || strings.Contains(filename, "/") {
		c.Status(http.StatusBadRequest)
		return
	}
	c.File(filepath.Join(h.uploadsDir, filename))
}

func mimeToExt(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	default:
		return ".bin"
	}
}
