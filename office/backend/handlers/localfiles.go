package handlers

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// File extensions mapped to Vulos app types
var extToType = map[string]string{
	".doc":  "doc",
	".docx": "doc",
	".txt":  "doc",
	".md":   "doc",
	".rtf":  "doc",
	".odt":  "doc",
	".xls":  "sheet",
	".xlsx": "sheet",
	".csv":  "sheet",
	".ods":  "sheet",
	".ppt":  "slide",
	".pptx": "slide",
	".odp":  "slide",
	".pdf":  "pdf",
}

type LocalFile struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Ext      string `json:"ext"`
	AppType  string `json:"appType"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"` // unix ms
}

type LocalFilesHandler struct {
	scanDirs []string
}

func NewLocalFilesHandler() *LocalFilesHandler {
	home, _ := os.UserHomeDir()
	return &LocalFilesHandler{
		scanDirs: []string{
			filepath.Join(home, "Documents"),
			filepath.Join(home, "Downloads"),
			filepath.Join(home, "Desktop"),
		},
	}
}

func (h *LocalFilesHandler) Scan(c *gin.Context) {
	var results []LocalFile

	for _, dir := range h.scanDirs {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			// Skip hidden files and deep nested dirs (max 2 levels)
			rel, _ := filepath.Rel(dir, path)
			if strings.HasPrefix(info.Name(), ".") {
				return nil
			}
			depth := strings.Count(rel, string(os.PathSeparator))
			if depth > 2 {
				return filepath.SkipDir
			}

			ext := strings.ToLower(filepath.Ext(info.Name()))
			appType, ok := extToType[ext]
			if !ok {
				return nil
			}

			results = append(results, LocalFile{
				Name:     info.Name(),
				Path:     path,
				Ext:      ext,
				AppType:  appType,
				Size:     info.Size(),
				Modified: info.ModTime().UnixMilli(),
			})
			return nil
		})
	}

	if results == nil {
		results = []LocalFile{}
	}
	c.JSON(http.StatusOK, results)
}

func (h *LocalFilesHandler) Serve(c *gin.Context) {
	path := c.Query("path")
	if path == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path required"})
		return
	}

	// Security: path must be under one of the allowed scan dirs
	allowed := false
	for _, dir := range h.scanDirs {
		if strings.HasPrefix(filepath.Clean(path), filepath.Clean(dir)) {
			allowed = true
			break
		}
	}
	if !allowed {
		c.JSON(http.StatusForbidden, gin.H{"error": "path not allowed"})
		return
	}

	// Prevent path traversal
	if strings.Contains(path, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid path"})
		return
	}

	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}

	c.Header("Content-Disposition", `inline; filename="`+filepath.Base(path)+`"`)
	c.File(path)
}
