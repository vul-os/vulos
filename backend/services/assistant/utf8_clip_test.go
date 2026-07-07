package assistant

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// clipUTF8 must never split a multi-byte rune: for every cut length the result
// is valid UTF-8 and within the byte budget. A naive s[:n] would produce invalid
// UTF-8 whenever n lands inside a rune.
func TestClipUTF8IsRuneSafe(t *testing.T) {
	s := strings.Repeat("é", 100) // 2-byte runes → every odd cut splits one
	for n := 0; n <= len(s)+2; n++ {
		got := clipUTF8(s, n)
		if !utf8.ValidString(got) {
			t.Fatalf("clipUTF8(%d) produced invalid UTF-8: %q", n, got)
		}
		if len(got) > n && n >= 0 {
			t.Fatalf("clipUTF8(%d) len %d exceeds budget", n, len(got))
		}
	}
	if clipUTF8("hello", 100) != "hello" {
		t.Fatal("clipUTF8 should return short strings unchanged")
	}
	if clipUTF8("hello", 0) != "" {
		t.Fatal("clipUTF8 with a zero budget should be empty")
	}
}

// embedText must clip on a rune boundary so the on-instance ONNX embedder (which
// reads the text as UTF-8 on stdin) never receives a split rune. Before the fix
// a byte-boundary cut at indexEmbedChars produced invalid UTF-8, which the Python
// helper rejects with UnicodeDecodeError — silently dropping any long non-ASCII
// message from the semantic index. The clip keeps embedText valid UTF-8 for a
// range of non-ASCII bodies that each exceed the char cap.
func TestEmbedTextAlwaysValidUTF8(t *testing.T) {
	for _, rn := range []string{"字", "é", "😀"} { // 3-, 2-, and 4-byte runes
		body := strings.Repeat(rn, 5000)
		got := embedText(Message{Subject: "s", Body: body})
		if !utf8.ValidString(got) {
			t.Fatalf("embedText(%q body) returned invalid UTF-8", rn)
		}
		if len(got) > indexEmbedChars {
			t.Fatalf("embedText(%q body) exceeded the cap: %d", rn, len(got))
		}
	}
}
