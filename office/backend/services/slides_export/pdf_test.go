package slides_export

import (
	"bytes"
	"strings"
	"testing"
)

// TestStripHTML verifies that HTML tags and entities are removed.
func TestStripHTML(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`<p><strong>Hello</strong> world</p>`, `Hello world`},
		{`<ul><li>Item 1</li><li>Item 2</li></ul>`, `Item 1 Item 2`},
		{`&amp; &lt; &gt; &quot;`, `& < > "`},
		{``, ``},
		{`plain text`, `plain text`},
		{`<script>alert("xss")</script>`, `alert("xss")`},
	}
	for _, tc := range cases {
		got := strings.TrimSpace(stripHTML(tc.in))
		if got != strings.TrimSpace(tc.want) {
			t.Errorf("stripHTML(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

// TestHexToRGB verifies colour parsing.
func TestHexToRGB(t *testing.T) {
	cases := []struct{ hex string; r, g, b uint8 }{
		{"#1a1a2e", 0x1a, 0x1a, 0x2e},
		{"ffffff", 0xff, 0xff, 0xff},
		{"000000", 0x00, 0x00, 0x00},
		{"", 0xff, 0xff, 0xff}, // fallback to white
		{"zzzzzz", 0xff, 0xff, 0xff},
	}
	for _, tc := range cases {
		r, g, b := hexToRGB(tc.hex)
		if r != tc.r || g != tc.g || b != tc.b {
			t.Errorf("hexToRGB(%q) = (%d,%d,%d); want (%d,%d,%d)", tc.hex, r, g, b, tc.r, tc.g, tc.b)
		}
	}
}

// TestRenderPDF verifies that a multi-slide deck produces a valid PDF byte stream.
func TestRenderPDF(t *testing.T) {
	deck := Deck{
		Title: "Test Deck",
		Slides: []Slide{
			{Title: "Slide One", Content: "<p>Hello <strong>world</strong></p>", Background: "#1a1a2e"},
			{Title: "Slide Two", Content: "<ul><li>Point A</li><li>Point B</li></ul>", Background: ""},
			{Title: "", Content: "<p>No title slide</p>", Background: "#ffffff"},
		},
	}

	data, err := RenderPDF(deck)
	if err != nil {
		t.Fatalf("RenderPDF returned error: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("RenderPDF returned empty bytes")
	}
	// PDF magic bytes: %PDF-
	if !bytes.HasPrefix(data, []byte("%PDF-")) {
		t.Fatalf("output does not start with %%PDF- header; got %q", string(data[:min(20, len(data))]))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
