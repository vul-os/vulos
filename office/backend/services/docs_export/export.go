// Package docs_export provides server-side PDF and DOCX export for Docs files.
//
// PDF strategy: pure-Go PDF generation using a minimal layout engine based on
// the standard library + gopdf (signintech/gopdf). This avoids any Chromium
// dependency. We extract plain-text + heading structure from the stored TipTap
// JSON and render it as paginated paragraphs.
//
// DOCX strategy: hand-rolled minimal OOXML writer. We assemble the required
// ZIP entries (word/document.xml, [Content_Types].xml, _rels/.rels,
// word/_rels/document.xml.rels, word/styles.xml) from Go text/template. This
// avoids any third-party Go DOCX library whose license may be incompatible
// with the project's MIT licence.
package docs_export

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"
)

// ─── TipTap JSON model (minimal, enough for export) ──────────────────────────

// Node represents a TipTap ProseMirror node in the stored JSON.
type Node struct {
	Type    string                 `json:"type"`
	Attrs   map[string]interface{} `json:"attrs,omitempty"`
	Content []Node                 `json:"content,omitempty"`
	Text    string                 `json:"text,omitempty"`
	Marks   []Mark                 `json:"marks,omitempty"`
}

// Mark represents a TipTap inline mark.
type Mark struct {
	Type  string                 `json:"type"`
	Attrs map[string]interface{} `json:"attrs,omitempty"`
}

// DocJSON is the root TipTap document node.
type DocJSON struct {
	Type    string `json:"type"`
	Content []Node `json:"content"`
}

// ParseDocJSON parses a raw JSON byte slice into a DocJSON.
func ParseDocJSON(raw []byte) (*DocJSON, error) {
	// The content field may be stored as a JSON object with a "type":"doc" wrapper.
	var doc DocJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("docs_export: parse doc json: %w", err)
	}
	return &doc, nil
}

// ─── Text extraction ──────────────────────────────────────────────────────────

// Paragraph is a logical paragraph/heading used by both PDF and DOCX renderers.
type Paragraph struct {
	Text        string
	HeadingLevel int  // 0 = normal, 1–6 = heading
	IsBullet    bool
	IsCode      bool
	IsBlockquote bool
}

// ExtractParagraphs walks the TipTap node tree and returns flat paragraphs.
func ExtractParagraphs(doc *DocJSON) []Paragraph {
	var out []Paragraph
	for _, n := range doc.Content {
		out = append(out, extractNode(n)...)
	}
	return out
}

func extractNode(n Node) []Paragraph {
	switch n.Type {
	case "paragraph":
		return []Paragraph{{Text: extractText(n.Content), HeadingLevel: 0}}
	case "heading":
		level := 1
		if v, ok := n.Attrs["level"]; ok {
			switch vt := v.(type) {
			case float64:
				level = int(vt)
			case int:
				level = vt
			}
		}
		return []Paragraph{{Text: extractText(n.Content), HeadingLevel: level}}
	case "bulletList", "orderedList", "taskList":
		var items []Paragraph
		for _, item := range n.Content {
			for _, child := range item.Content {
				sub := extractNode(child)
				for i := range sub {
					sub[i].IsBullet = true
				}
				items = append(items, sub...)
			}
		}
		return items
	case "codeBlock":
		return []Paragraph{{Text: extractText(n.Content), IsCode: true}}
	case "blockquote":
		var ps []Paragraph
		for _, child := range n.Content {
			sub := extractNode(child)
			for i := range sub {
				sub[i].IsBlockquote = true
			}
			ps = append(ps, sub...)
		}
		return ps
	case "horizontalRule":
		return []Paragraph{{Text: strings.Repeat("─", 40)}}
	default:
		// Recurse for unknown container nodes
		var ps []Paragraph
		for _, child := range n.Content {
			ps = append(ps, extractNode(child)...)
		}
		return ps
	}
}

// extractText recursively collects text from inline nodes.
func extractText(nodes []Node) string {
	var sb strings.Builder
	for _, n := range nodes {
		if n.Type == "text" {
			sb.WriteString(n.Text)
		}
		if len(n.Content) > 0 {
			sb.WriteString(extractText(n.Content))
		}
	}
	return sb.String()
}

// ─── PDF export ──────────────────────────────────────────────────────────────
// We generate a minimal but valid PDF manually. The format:
//   - BT / ET blocks containing Tf + Td + Tj operators
//   - Page size: A4 (595 × 842 pt)
//   - Font: Helvetica (PDF standard font, no embedding required)
//
// This is intentionally minimal — enough to produce a valid, readable PDF
// without a heavy dependency. For rich formatting (images, complex tables)
// a proper PDF library would be needed; this covers the 90% use-case.

const (
	pageWidth    = 595.28
	pageHeight   = 841.89
	marginLeft   = 72.0
	marginRight  = 72.0
	marginTop    = 72.0
	marginBottom = 72.0
	bodyWidth    = pageWidth - marginLeft - marginRight
)

// fontSizeForPara returns the PDF font size for a paragraph type.
func fontSizeForPara(p Paragraph) float64 {
	switch p.HeadingLevel {
	case 1:
		return 22
	case 2:
		return 18
	case 3:
		return 15
	case 4:
		return 13
	case 5:
		return 12
	case 6:
		return 11
	default:
		if p.IsCode {
			return 9
		}
		return 11
	}
}

func lineHeightForPara(p Paragraph) float64 {
	return fontSizeForPara(p) * 1.4
}

// wrapWords wraps text to a maximum width in characters (rough).
// Proper PDF measurement would require glyph metrics; we approximate at
// ~0.55 * fontSize per character for Helvetica.
func wrapWords(text string, maxWidth, fontSize float64) []string {
	if text == "" {
		return []string{""}
	}
	charWidth := fontSize * 0.55
	maxChars := int(maxWidth / charWidth)
	if maxChars < 1 {
		maxChars = 1
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}
	var lines []string
	var cur strings.Builder
	for _, w := range words {
		if cur.Len() == 0 {
			cur.WriteString(w)
		} else if cur.Len()+1+utf8.RuneCountInString(w) <= maxChars {
			cur.WriteByte(' ')
			cur.WriteString(w)
		} else {
			lines = append(lines, cur.String())
			cur.Reset()
			cur.WriteString(w)
		}
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}

// pdfEscapeString escapes a string for use in PDF Tj operator.
func pdfEscapeString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "(", "\\(")
	s = strings.ReplaceAll(s, ")", "\\)")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\n", "\\n")
	// Strip non-printable ASCII for safety (basic ASCII-only rendering)
	var sb strings.Builder
	for _, r := range s {
		if r >= 32 && r < 127 {
			sb.WriteRune(r)
		} else {
			sb.WriteRune(' ')
		}
	}
	return sb.String()
}

// GeneratePDF produces a minimal PDF byte slice from the document paragraphs.
func GeneratePDF(title string, paragraphs []Paragraph) ([]byte, error) {
	// We build the PDF object-by-object.
	var buf bytes.Buffer
	objOffsets := []int{}

	write := func(s string) { buf.WriteString(s) }
	writef := func(format string, args ...interface{}) { fmt.Fprintf(&buf, format, args...) }

	write("%PDF-1.4\n")
	write("%\xe2\xe3\xcf\xd3\n") // binary marker

	// Helper to start an object
	startObj := func(n int) {
		objOffsets = append(objOffsets, buf.Len())
		writef("%d 0 obj\n", n)
	}
	endObj := func() { write("endobj\n") }

	// Object 1: Catalog
	startObj(1)
	write("<< /Type /Catalog /Pages 2 0 R >>\n")
	endObj()

	// We'll collect page streams; build them first, then write pages tree.
	type pageStream struct {
		streamLen int
		data      string
	}

	var pages []pageStream

	// Lay out text across pages
	type lineItem struct {
		x, y     float64
		text     string
		fontSize float64
		bold     bool
	}

	y := pageHeight - marginTop
	var currentPageLines []lineItem

	flushPage := func() {
		if len(currentPageLines) == 0 && len(pages) > 0 {
			return
		}
		var sb strings.Builder
		for _, li := range currentPageLines {
			fontName := "F1"
			if li.bold {
				fontName = "F2"
			}
			fmt.Fprintf(&sb, "BT\n/%s %g Tf\n%g %g Td\n(%s) Tj\nET\n",
				fontName, li.fontSize, li.x, li.y, li.text)
		}
		data := sb.String()
		pages = append(pages, pageStream{streamLen: len(data), data: data})
		currentPageLines = nil
		y = pageHeight - marginTop
	}

	newPage := func() {
		flushPage()
	}

	addLine := func(text string, x, lineY, fontSize float64, bold bool) {
		currentPageLines = append(currentPageLines, lineItem{
			x: x, y: lineY, text: pdfEscapeString(text),
			fontSize: fontSize, bold: bold,
		})
	}

	spacingBefore := func(p Paragraph) float64 {
		if p.HeadingLevel > 0 {
			return fontSizeForPara(p) * 0.5
		}
		return 4
	}

	for _, p := range paragraphs {
		fontSize := fontSizeForPara(p)
		lh := lineHeightForPara(p)
		bold := p.HeadingLevel > 0
		x := marginLeft
		if p.IsBullet {
			x = marginLeft + 12
		}
		if p.IsBlockquote {
			x = marginLeft + 20
		}

		// Space before paragraph
		y -= spacingBefore(p)
		if y < marginBottom {
			newPage()
			y = pageHeight - marginTop
		}

		prefix := ""
		if p.IsBullet {
			prefix = "• "
		}
		lines := wrapWords(prefix+p.Text, bodyWidth-(x-marginLeft), fontSize)
		for _, line := range lines {
			if y < marginBottom+lh {
				newPage()
				y = pageHeight - marginTop
			}
			y -= lh
			addLine(line, x, y, fontSize, bold)
		}
		// Extra space after headings
		if p.HeadingLevel > 0 {
			y -= fontSize * 0.3
		}
	}
	flushPage()

	if len(pages) == 0 {
		pages = append(pages, pageStream{data: "", streamLen: 0})
	}

	// Object 2: Pages tree (placeholder, will reference page objects)
	// Page objects start at 3
	nPages := len(pages)
	pageObjNums := make([]int, nPages)
	streamObjNums := make([]int, nPages)
	nextObj := 3
	for i := range pages {
		pageObjNums[i] = nextObj
		nextObj++
		streamObjNums[i] = nextObj
		nextObj++
	}
	// Font objects
	fontRegObjNum := nextObj
	nextObj++
	fontBoldObjNum := nextObj
	nextObj++

	startObj(2)
	var kidRefs strings.Builder
	for _, n := range pageObjNums {
		fmt.Fprintf(&kidRefs, "%d 0 R ", n)
	}
	writef("<< /Type /Pages /Kids [%s] /Count %d >>\n", strings.TrimSpace(kidRefs.String()), nPages)
	endObj()

	// Font objects
	startObj(fontRegObjNum)
	write("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>\n")
	endObj()

	startObj(fontBoldObjNum)
	write("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>\n")
	endObj()

	// Page + stream objects
	for i, pg := range pages {
		startObj(pageObjNums[i])
		writef("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %g %g]\n", pageWidth, pageHeight)
		writef("   /Resources << /Font << /F1 %d 0 R /F2 %d 0 R >> >>\n", fontRegObjNum, fontBoldObjNum)
		writef("   /Contents %d 0 R >>\n", streamObjNums[i])
		endObj()

		startObj(streamObjNums[i])
		writef("<< /Length %d >>\n", pg.streamLen)
		write("stream\n")
		write(pg.data)
		write("\nendstream\n")
		endObj()
	}

	// xref table
	xrefOffset := buf.Len()
	totalObjs := nextObj
	write("xref\n")
	writef("0 %d\n", totalObjs)
	write("0000000000 65535 f \n") // object 0

	// Build a lookup: obj number → offset in objOffsets slice index
	// objOffsets[0] = obj 1, objOffsets[1] = obj 2, etc.
	for i := 1; i < totalObjs; i++ {
		idx := i - 1
		if idx < len(objOffsets) {
			writef("%010d 00000 n \n", objOffsets[idx])
		} else {
			write("0000000000 65535 f \n")
		}
	}

	write("trailer\n")
	writef("<< /Size %d /Root 1 0 R >>\n", totalObjs)
	write("startxref\n")
	writef("%d\n", xrefOffset)
	write("%%EOF\n")

	return buf.Bytes(), nil
}

// ─── DOCX export ─────────────────────────────────────────────────────────────
// We hand-roll a minimal OOXML DOCX. Required files:
//   [Content_Types].xml
//   _rels/.rels
//   word/document.xml
//   word/_rels/document.xml.rels
//   word/styles.xml

const contentTypesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml"
    ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
  <Override PartName="/word/styles.xml"
    ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
</Types>`

const relsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1"
    Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument"
    Target="word/document.xml"/>
</Relationships>`

const wordRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1"
    Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles"
    Target="styles.xml"/>
</Relationships>`

const stylesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:style w:type="paragraph" w:styleId="Normal"><w:name w:val="Normal"/>
    <w:pPr><w:spacing w:after="160"/></w:pPr>
    <w:rPr><w:sz w:val="22"/><w:szCs w:val="22"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/>
    <w:pPr><w:outlineLvl w:val="0"/><w:spacing w:before="240" w:after="120"/></w:pPr>
    <w:rPr><w:b/><w:sz w:val="44"/><w:szCs w:val="44"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/>
    <w:pPr><w:outlineLvl w:val="1"/><w:spacing w:before="200" w:after="100"/></w:pPr>
    <w:rPr><w:b/><w:sz w:val="36"/><w:szCs w:val="36"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading3"><w:name w:val="heading 3"/>
    <w:pPr><w:outlineLvl w:val="2"/><w:spacing w:before="160" w:after="80"/></w:pPr>
    <w:rPr><w:b/><w:sz w:val="30"/><w:szCs w:val="30"/></w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Code"><w:name w:val="Code"/>
    <w:rPr><w:rFonts w:ascii="Courier New" w:hAnsi="Courier New"/><w:sz w:val="18"/></w:rPr>
  </w:style>
</w:styles>`

var docxTmpl = template.Must(template.New("docx").Funcs(template.FuncMap{
	"xml": func(s string) string {
		s = strings.ReplaceAll(s, "&", "&amp;")
		s = strings.ReplaceAll(s, "<", "&lt;")
		s = strings.ReplaceAll(s, ">", "&gt;")
		s = strings.ReplaceAll(s, "\"", "&quot;")
		return s
	},
	"styleID": func(p Paragraph) string {
		switch p.HeadingLevel {
		case 1:
			return "Heading1"
		case 2:
			return "Heading2"
		case 3:
			return "Heading3"
		default:
			if p.IsCode {
				return "Code"
			}
			return "Normal"
		}
	},
	"indentLeft": func(p Paragraph) string {
		if p.IsBullet {
			return `<w:ind w:left="720"/>`
		}
		if p.IsBlockquote {
			return `<w:ind w:left="1080"/>`
		}
		return ""
	},
	"bulletRun": func(p Paragraph) string {
		if p.IsBullet {
			return `<w:r><w:t xml:space="preserve">• </w:t></w:r>`
		}
		return ""
	},
}).Parse(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
{{range .Paragraphs}}    <w:p>
      <w:pPr>
        <w:pStyle w:val="{{styleID .}}"/>
        {{indentLeft .}}
      </w:pPr>
      {{bulletRun .}}<w:r><w:t xml:space="preserve">{{xml .Text}}</w:t></w:r>
    </w:p>
{{end}}    <w:sectPr>
      <w:pgSz w:w="12240" w:h="15840"/>
      <w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/>
    </w:sectPr>
  </w:body>
</w:document>`))

// docxTemplateData is passed to the document template.
type docxTemplateData struct {
	Title      string
	Date       string
	Paragraphs []Paragraph
}

// GenerateDOCX produces a valid OOXML DOCX ZIP byte slice.
func GenerateDOCX(title string, paragraphs []Paragraph) ([]byte, error) {
	// Render document.xml
	var docBuf bytes.Buffer
	if err := docxTmpl.Execute(&docBuf, docxTemplateData{
		Title:      title,
		Date:       time.Now().Format("2006-01-02"),
		Paragraphs: paragraphs,
	}); err != nil {
		return nil, fmt.Errorf("docs_export: render document.xml: %w", err)
	}

	// Assemble ZIP
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)

	entries := map[string]string{
		"[Content_Types].xml":          contentTypesXML,
		"_rels/.rels":                  relsXML,
		"word/_rels/document.xml.rels": wordRelsXML,
		"word/styles.xml":              stylesXML,
		"word/document.xml":            docBuf.String(),
	}
	// Write in deterministic order
	order := []string{
		"[Content_Types].xml",
		"_rels/.rels",
		"word/_rels/document.xml.rels",
		"word/styles.xml",
		"word/document.xml",
	}
	for _, name := range order {
		w, err := zw.Create(name)
		if err != nil {
			return nil, fmt.Errorf("docs_export: zip create %s: %w", name, err)
		}
		if _, err := w.Write([]byte(entries[name])); err != nil {
			return nil, fmt.Errorf("docs_export: zip write %s: %w", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("docs_export: zip close: %w", err)
	}
	return zipBuf.Bytes(), nil
}
