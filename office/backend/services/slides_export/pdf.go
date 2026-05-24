// Package slides_export generates PDF exports from slide deck data.
//
// Implementation: pure-Go PDF writer (no CGO required).
// Uses github.com/signintech/gopdf with the LiberationSerif TTF font
// (embedded from golang.org/x/image) to render each slide as one A4-landscape page.
//
// Each page layout:
//   - Slide background (solid colour fill or white)
//   - Slide title    — top-left, 26 pt
//   - Slide content  — body text, HTML-stripped, 13 pt
//   - Slide number   — bottom-right, 9 pt
//   - Page border    — thin rule around the content area
package slides_export

import (
	"bytes"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/signintech/gopdf"
	_ "embed"

	ximage "golang.org/x/image/font/gofont/goregular"
)

// Slide is the minimal representation of a slide needed for PDF rendering.
type Slide struct {
	Title      string `json:"title"`
	Content    string `json:"content"` // HTML; stripped to plain text for PDF
	Notes      string `json:"notes"`
	Background string `json:"background"` // CSS hex color, e.g. "#1a1a2e"
}

// Deck is the top-level structure sent in the export request.
type Deck struct {
	Title  string  `json:"title"`
	Slides []Slide `json:"slides"`
}

// slideBounds: A4 landscape in pts (1 pt = 1/72 in).
const (
	pageW = 841.89 // A4 landscape width  (297 mm)
	pageH = 595.28 // A4 landscape height (210 mm)

	marginL = 44.0
	marginT = 38.0
	marginR = 44.0
	marginB = 38.0

	titleFontSize   = 24.0
	bodyFontSize    = 12.0
	pageNumFontSize = 9.0

	titleLineH = 32.0
	bodyLineH  = 17.0
)

var (
	htmlTagRe  = regexp.MustCompile(`<[^>]+>`)
	multiSpace = regexp.MustCompile(`[ \t]+`)
)

// goRegularTTF returns the Go-Regular TTF bytes from golang.org/x/image.
// This is a free (BSD-licensed) font embedded at link time — no file I/O.
func goRegularTTF() []byte {
	// ximage.TTF is a []byte exported by golang.org/x/image/font/gofont/goregular.
	return ximage.TTF
}

// stripHTML removes HTML tags and decodes entities, returning plain text.
func stripHTML(src string) string {
	// Replace block-level closing tags with a newline so paragraph breaks survive.
	src = regexp.MustCompile(`</?(p|br|li|div|h[1-6])\b[^>]*>`).ReplaceAllString(src, " ")
	s := htmlTagRe.ReplaceAllString(src, "")
	s = html.UnescapeString(s)
	s = multiSpace.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			blank++
			if blank <= 1 {
				out = append(out, "")
			}
		} else {
			blank = 0
			out = append(out, l)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// hexToRGB parses "#rrggbb" or "rrggbb" → (r,g,b uint8).
// Falls back to white for empty / invalid strings.
func hexToRGB(hex string) (uint8, uint8, uint8) {
	hex = strings.TrimPrefix(strings.ToLower(hex), "#")
	if len(hex) != 6 {
		return 255, 255, 255
	}
	// Validate all characters are hex digits.
	for _, c := range hex {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return 255, 255, 255
		}
	}
	var r, g, b uint8
	fmt.Sscanf(hex[:2], "%02x", &r)
	fmt.Sscanf(hex[2:4], "%02x", &g)
	fmt.Sscanf(hex[4:6], "%02x", &b)
	return r, g, b
}

// isDark returns true if the colour is dark enough to warrant white text.
func isDark(r, g, b uint8) bool {
	lum := 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
	return lum < 140
}

// wrapText splits text into lines no wider than maxChars (simple word-wrap).
func wrapText(text string, maxChars int) []string {
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			lines = append(lines, "")
			continue
		}
		words := strings.Fields(para)
		line := ""
		for _, w := range words {
			candidate := line
			if candidate != "" {
				candidate += " "
			}
			candidate += w
			if len(candidate) <= maxChars {
				line = candidate
			} else {
				if line != "" {
					lines = append(lines, line)
				}
				line = w
			}
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// addFont registers the embedded Go-Regular TTF font under the given family name.
func addFont(pdf *gopdf.GoPdf, family string) error {
	return pdf.AddTTFFontByReader(family, bytes.NewReader(goRegularTTF()))
}

// RenderPDF builds a PDF containing one page per slide and returns the bytes.
func RenderPDF(deck Deck) ([]byte, error) {
	pdf := gopdf.GoPdf{}
	pdf.Start(gopdf.Config{
		PageSize: *gopdf.PageSizeA4Landscape,
		Unit:     gopdf.Unit_PT,
	})

	// Register font families using the embedded TTF.
	// gopdf's TTF fonts do not have a separate bold variant here; we use the
	// same font at a larger size for "titles" to simulate heading weight.
	const fontFamily = "GoRegular"
	if err := addFont(&pdf, fontFamily); err != nil {
		return nil, fmt.Errorf("slide export: add font: %w", err)
	}

	for i, slide := range deck.Slides {
		pdf.AddPage()

		// Background fill.
		bgR, bgG, bgB := hexToRGB(slide.Background)
		pdf.SetFillColor(bgR, bgG, bgB)
		pdf.Rectangle(0, 0, pageW, pageH, "F", 0, 0)

		// Choose text colour based on background luminance.
		var textR, textG, textB uint8
		if isDark(bgR, bgG, bgB) {
			textR, textG, textB = 240, 240, 255
		} else {
			textR, textG, textB = 20, 20, 30
		}

		// ── Title ────────────────────────────────────────────────────────────
		if slide.Title != "" {
			if err := pdf.SetFont(fontFamily, "", titleFontSize); err != nil {
				return nil, fmt.Errorf("set title font: %w", err)
			}
			pdf.SetTextColor(textR, textG, textB)
			pdf.SetX(marginL)
			pdf.SetY(marginT)
			titleText := slide.Title
			if len(titleText) > 80 {
				titleText = titleText[:77] + "..."
			}
			if err := pdf.Cell(nil, titleText); err != nil {
				return nil, fmt.Errorf("title cell: %w", err)
			}
		}

		// ── Body ─────────────────────────────────────────────────────────────
		bodyText := stripHTML(slide.Content)
		if bodyText != "" {
			if err := pdf.SetFont(fontFamily, "", bodyFontSize); err != nil {
				return nil, fmt.Errorf("set body font: %w", err)
			}
			if isDark(bgR, bgG, bgB) {
				pdf.SetTextColor(190, 190, 210)
			} else {
				pdf.SetTextColor(80, 80, 100)
			}

			bodyY := marginT + titleLineH + 8.0
			if slide.Title == "" {
				bodyY = marginT
			}
			maxBodyY := pageH - marginB - pageNumFontSize - 16.0

			lines := wrapText(bodyText, 90)
			for _, line := range lines {
				if bodyY >= maxBodyY {
					break
				}
				pdf.SetX(marginL)
				pdf.SetY(bodyY)
				if line != "" {
					if err := pdf.Cell(nil, line); err != nil {
						return nil, fmt.Errorf("body cell: %w", err)
					}
				}
				bodyY += bodyLineH
			}
		}

		// ── Page number ───────────────────────────────────────────────────────
		if err := pdf.SetFont(fontFamily, "", pageNumFontSize); err != nil {
			return nil, fmt.Errorf("set page num font: %w", err)
		}
		if isDark(bgR, bgG, bgB) {
			pdf.SetTextColor(100, 100, 120)
		} else {
			pdf.SetTextColor(160, 160, 180)
		}
		numStr := fmt.Sprintf("%d / %d", i+1, len(deck.Slides))
		pdf.SetX(pageW - marginR - 60)
		pdf.SetY(pageH - marginB - pageNumFontSize)
		if err := pdf.Cell(nil, numStr); err != nil {
			return nil, fmt.Errorf("page num cell: %w", err)
		}

		// ── Thin border ───────────────────────────────────────────────────────
		pdf.SetStrokeColor(textR/4, textG/4, textB/4)
		pdf.SetLineWidth(0.4)
		pdf.Rectangle(marginL/2, marginT/2, pageW-marginR/2, pageH-marginB/2, "D", 0, 0)
	}

	return pdf.GetBytesPdf(), nil
}
