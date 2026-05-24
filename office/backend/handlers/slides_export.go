// slides_export.go — SLIDES-07: export endpoints for slide decks.
//
// Routes (registered in main.go):
//   GET  /api/slides/:id/export?format=pdf
//   GET  /api/slides/:id/export?format=pptx   (stub — see note below)
//
// PDF: rendered server-side with pure-Go gopdf (no CGO).
// PPTX: client-side via pptxgenjs; the backend stub returns 501 with a
//       "Coming soon" message so the frontend falls back to the JS path.
//
// Slide content is fetched from the file store (the slide deck is stored
// as a JSON blob in the File.Content field).

package handlers

import (
	"encoding/json"
	"net/http"

	"vulos-office/backend/services/slides_export"
	"vulos-office/backend/storage"

	"github.com/gin-gonic/gin"
)

// SlidesExportHandler handles PDF/PPTX export for slide decks.
type SlidesExportHandler struct {
	store storage.Storage
}

// NewSlidesExportHandler constructs the handler.
func NewSlidesExportHandler(store storage.Storage) *SlidesExportHandler {
	return &SlidesExportHandler{store: store}
}

// Export handles GET /api/slides/:id/export?format=pdf|pptx
func (h *SlidesExportHandler) Export(c *gin.Context) {
	fileID := c.Param("id")
	format := c.DefaultQuery("format", "pdf")

	// Fetch the file from storage.
	file, err := h.store.GetFile(fileID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}

	// Decode the slides JSON from File.Content (interface{}).
	// File.Content is stored as a json.RawMessage / interface{}; re-marshal to bytes.
	contentBytes, err := json.Marshal(file.Content)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not read deck content"})
		return
	}

	var rawDeck struct {
		Title  string                   `json:"title"`
		Slides []slides_export.Slide    `json:"slides"`
	}
	if err := json.Unmarshal(contentBytes, &rawDeck); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid slide deck format"})
		return
	}
	if len(rawDeck.Slides) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "deck has no slides"})
		return
	}
	if rawDeck.Title == "" {
		rawDeck.Title = file.Name
	}

	switch format {
	case "pdf":
		h.exportPDF(c, rawDeck.Title, rawDeck.Slides)
	case "pptx":
		// PPTX is handled client-side via pptxgenjs.
		// Return 501 so the frontend falls back to its JS export path.
		c.JSON(http.StatusNotImplemented, gin.H{
			"error":   "PPTX server export not implemented — use client-side export",
			"message": "The PPTX export is handled in your browser via pptxgenjs.",
		})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported format — use pdf or pptx"})
	}
}

func (h *SlidesExportHandler) exportPDF(c *gin.Context, title string, slides []slides_export.Slide) {
	deck := slides_export.Deck{
		Title:  title,
		Slides: slides,
	}

	pdfBytes, err := slides_export.RenderPDF(deck)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "PDF generation failed: " + err.Error()})
		return
	}

	// Sanitise filename — strip path separators.
	safeName := slidesSanitiseFilename(title)
	if safeName == "" {
		safeName = "presentation"
	}

	c.Header("Content-Disposition", `attachment; filename="`+safeName+`.pdf"`)
	c.Header("Content-Type", "application/pdf")
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "application/pdf", pdfBytes)
}

// slidesSanitiseFilename strips characters unsafe in Content-Disposition filenames.
func slidesSanitiseFilename(s string) string {
	out := make([]byte, 0, len(s))
	for _, b := range []byte(s) {
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') ||
			b == '-' || b == '_' || b == '.' || b == ' ' {
			out = append(out, b)
		}
	}
	return string(out)
}
