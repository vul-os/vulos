package docs_export

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

func sampleDocJSON() []byte {
	doc := map[string]interface{}{
		"type": "doc",
		"content": []interface{}{
			map[string]interface{}{
				"type":  "heading",
				"attrs": map[string]interface{}{"level": 1},
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "Hello World"},
				},
			},
			map[string]interface{}{
				"type": "paragraph",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "This is a paragraph with some content."},
				},
			},
			map[string]interface{}{
				"type": "bulletList",
				"content": []interface{}{
					map[string]interface{}{
						"type": "listItem",
						"content": []interface{}{
							map[string]interface{}{
								"type": "paragraph",
								"content": []interface{}{
									map[string]interface{}{"type": "text", "text": "Bullet item one"},
								},
							},
						},
					},
					map[string]interface{}{
						"type": "listItem",
						"content": []interface{}{
							map[string]interface{}{
								"type": "paragraph",
								"content": []interface{}{
									map[string]interface{}{"type": "text", "text": "Bullet item two"},
								},
							},
						},
					},
				},
			},
			map[string]interface{}{
				"type": "codeBlock",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "fmt.Println(\"hello\")"},
				},
			},
			map[string]interface{}{
				"type": "blockquote",
				"content": []interface{}{
					map[string]interface{}{
						"type": "paragraph",
						"content": []interface{}{
							map[string]interface{}{"type": "text", "text": "A wise quote"},
						},
					},
				},
			},
			map[string]interface{}{
				"type":    "horizontalRule",
				"content": nil,
			},
			map[string]interface{}{
				"type":  "heading",
				"attrs": map[string]interface{}{"level": 2},
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "Section Two"},
				},
			},
			map[string]interface{}{
				"type": "paragraph",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "Final paragraph."},
				},
			},
		},
	}
	b, _ := json.Marshal(doc)
	return b
}

// ─── ParseDocJSON ─────────────────────────────────────────────────────────────

func TestParseDocJSON_ValidDoc(t *testing.T) {
	raw := sampleDocJSON()
	doc, err := ParseDocJSON(raw)
	if err != nil {
		t.Fatalf("ParseDocJSON returned error: %v", err)
	}
	if doc.Type != "doc" {
		t.Errorf("expected type 'doc', got %q", doc.Type)
	}
	if len(doc.Content) == 0 {
		t.Error("expected non-empty content")
	}
}

func TestParseDocJSON_InvalidJSON(t *testing.T) {
	_, err := ParseDocJSON([]byte("{not valid json"))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

// ─── ExtractParagraphs ────────────────────────────────────────────────────────

func TestExtractParagraphs_HeadingsAndParagraphs(t *testing.T) {
	raw := sampleDocJSON()
	doc, err := ParseDocJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	paras := ExtractParagraphs(doc)

	// Should have: H1, paragraph, 2 bullets, code, blockquote, hr, H2, paragraph
	if len(paras) < 8 {
		t.Errorf("expected at least 8 paragraphs, got %d", len(paras))
	}

	// First should be H1
	if paras[0].HeadingLevel != 1 {
		t.Errorf("expected first paragraph to be heading level 1, got %d", paras[0].HeadingLevel)
	}
	if paras[0].Text != "Hello World" {
		t.Errorf("expected H1 text 'Hello World', got %q", paras[0].Text)
	}

	// Bullet items should have IsBullet=true
	hasBullet := false
	for _, p := range paras {
		if p.IsBullet {
			hasBullet = true
			break
		}
	}
	if !hasBullet {
		t.Error("expected at least one bullet paragraph")
	}

	// Code block should have IsCode=true
	hasCode := false
	for _, p := range paras {
		if p.IsCode {
			hasCode = true
			break
		}
	}
	if !hasCode {
		t.Error("expected at least one code paragraph")
	}
}

// ─── GeneratePDF ─────────────────────────────────────────────────────────────

func TestGeneratePDF_ReturnsBytesStartingWithPDFHeader(t *testing.T) {
	raw := sampleDocJSON()
	doc, _ := ParseDocJSON(raw)
	paras := ExtractParagraphs(doc)

	pdfBytes, err := GeneratePDF("Test Document", paras)
	if err != nil {
		t.Fatalf("GeneratePDF returned error: %v", err)
	}
	if len(pdfBytes) < 10 {
		t.Fatalf("PDF output too short (%d bytes)", len(pdfBytes))
	}
	if !bytes.HasPrefix(pdfBytes, []byte("%PDF-")) {
		prefix := string(pdfBytes[:min(20, len(pdfBytes))])
		t.Errorf("PDF does not start with %%PDF- header; got: %s", prefix)
	}
}

func TestGeneratePDF_ContainsEOFMarker(t *testing.T) {
	paras := []Paragraph{
		{Text: "Simple document", HeadingLevel: 1},
		{Text: "Body text here.", HeadingLevel: 0},
	}
	pdfBytes, err := GeneratePDF("Simple", paras)
	if err != nil {
		t.Fatalf("GeneratePDF error: %v", err)
	}
	eofMarker := []byte("%\x25EOF")
	if !bytes.Contains(pdfBytes, eofMarker) {
		t.Error("PDF does not contain percent-percent-EOF marker")
	}
}

func TestGeneratePDF_EmptyDocument(t *testing.T) {
	pdfBytes, err := GeneratePDF("Empty", []Paragraph{})
	if err != nil {
		t.Fatalf("GeneratePDF(empty) error: %v", err)
	}
	if !bytes.HasPrefix(pdfBytes, []byte("%PDF-")) {
		t.Error("empty document PDF does not start with %%PDF-")
	}
}

// ─── GenerateDOCX ────────────────────────────────────────────────────────────

func isValidZip(data []byte) bool {
	_, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	return err == nil
}

func TestGenerateDOCX_ProducesValidZip(t *testing.T) {
	raw := sampleDocJSON()
	doc, _ := ParseDocJSON(raw)
	paras := ExtractParagraphs(doc)

	docxBytes, err := GenerateDOCX("Test Document", paras)
	if err != nil {
		t.Fatalf("GenerateDOCX error: %v", err)
	}
	if !isValidZip(docxBytes) {
		t.Error("GenerateDOCX output is not a valid ZIP")
	}
}

func TestGenerateDOCX_ContainsRequiredEntries(t *testing.T) {
	paras := []Paragraph{
		{Text: "Heading", HeadingLevel: 1},
		{Text: "Body",    HeadingLevel: 0},
	}
	docxBytes, err := GenerateDOCX("My Doc", paras)
	if err != nil {
		t.Fatalf("GenerateDOCX error: %v", err)
	}
	r, err := zip.NewReader(bytes.NewReader(docxBytes), int64(len(docxBytes)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	required := map[string]bool{
		"[Content_Types].xml":          false,
		"_rels/.rels":                  false,
		"word/document.xml":            false,
		"word/styles.xml":              false,
		"word/_rels/document.xml.rels": false,
	}
	for _, f := range r.File {
		if _, ok := required[f.Name]; ok {
			required[f.Name] = true
		}
	}
	for name, found := range required {
		if !found {
			t.Errorf("DOCX missing required entry: %s", name)
		}
	}
}

func TestGenerateDOCX_DocumentXMLContainsText(t *testing.T) {
	paras := []Paragraph{
		{Text: "Hello Vulos", HeadingLevel: 1},
		{Text: "World text",  HeadingLevel: 0},
	}
	docxBytes, err := GenerateDOCX("Test", paras)
	if err != nil {
		t.Fatalf("GenerateDOCX error: %v", err)
	}
	r, _ := zip.NewReader(bytes.NewReader(docxBytes), int64(len(docxBytes)))
	var docXML string
	for _, f := range r.File {
		if f.Name == "word/document.xml" {
			rc, _ := f.Open()
			var buf bytes.Buffer
			buf.ReadFrom(rc)
			rc.Close()
			docXML = buf.String()
		}
	}
	if !strings.Contains(docXML, "Hello Vulos") {
		t.Error("document.xml does not contain expected heading text 'Hello Vulos'")
	}
	if !strings.Contains(docXML, "World text") {
		t.Error("document.xml does not contain expected body text 'World text'")
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
