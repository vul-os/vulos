package ddos

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBodySizeLimiter_AuthRouteSmallBodyAllowed(t *testing.T) {
	mw := BodySizeLimiter(DefaultBodySizeConfig)
	body := bytes.Repeat([]byte("x"), 100) // well under 8KB
	var readErr error
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, len(body)+1)
		_, readErr = r.Body.Read(buf)
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	r.RemoteAddr = "1.2.3.4:80"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	_ = readErr // EOF is fine
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 got %d", w.Code)
	}
}

// TestBodySizeLimiter_PatchCovered verifies that PATCH requests are now subject
// to the body-size cap (fix for the missing-PATCH gap: BodySizeLimiter
// previously only covered POST and PUT).
func TestBodySizeLimiter_PatchCovered(t *testing.T) {
	// Set a tiny limit via a custom config so the test is fast and deterministic.
	cfg := BodySizeConfig{
		Routes: []BodySizeRoute{
			{Prefix: "/", MaxSize: 64},
		},
	}
	mw := BodySizeLimiter(cfg)

	// Body is 128 bytes — exceeds the 64-byte cap.
	bigBody := bytes.Repeat([]byte("x"), 128)
	var gotErr error
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 256)
		_, gotErr = r.Body.Read(buf)
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest(http.MethodPatch, "/api/mail/keydir/discoverable", bytes.NewReader(bigBody))
	r.RemoteAddr = "1.2.3.4:80"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	// The Read inside the handler must have returned an error (MaxBytesError).
	if gotErr == nil {
		t.Fatal("expected MaxBytesError reading oversized PATCH body, got nil")
	}
}

func TestBodySizeLimiter_ResolveBodyLimit(t *testing.T) {
	cases := []struct {
		path string
		want int64
	}{
		{"/api/auth/login", bodySizeAuth},
		{"/jmap/session", bodySizeJMAP},
		{"/api/mail/submit/message", bodySizeMail},
		{"/api/some/other", bodySizeDefault},
	}
	for _, tc := range cases {
		got := resolveBodyLimit(DefaultBodySizeConfig, tc.path)
		if got != tc.want {
			t.Errorf("path=%s want=%d got=%d", tc.path, tc.want, got)
		}
	}
}
