package airouter

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestStore returns a Store backed by a temp SQLite file.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	s, err := NewStore(filepath.Join(dir, "airouter.db"), key)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// --- Config store tests ---

func TestConfigRoundTrip(t *testing.T) {
	s := newTestStore(t)

	cfg, err := s.GetConfig()
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	// Default mode is byo.
	if cfg.Mode != ModeBYO {
		t.Errorf("default mode: got %q, want %q", cfg.Mode, ModeBYO)
	}

	want := Config{Mode: ModeCloud, ActiveModel: "gpt-4o"}
	if err := s.SetConfig(want); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	got, err := s.GetConfig()
	if err != nil {
		t.Fatalf("GetConfig after set: %v", err)
	}
	if got.Mode != want.Mode || got.ActiveModel != want.ActiveModel {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestConfigSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = 42
	}
	dbPath := filepath.Join(dir, "airouter.db")

	s1, err := NewStore(dbPath, key)
	if err != nil {
		t.Fatalf("NewStore (first open): %v", err)
	}
	if err := s1.SetConfig(Config{Mode: ModeBYO, ActiveModel: "claude-opus-4"}); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	s1.Close()

	s2, err := NewStore(dbPath, key)
	if err != nil {
		t.Fatalf("NewStore (second open): %v", err)
	}
	defer s2.Close()

	cfg, err := s2.GetConfig()
	if err != nil {
		t.Fatalf("GetConfig after reopen: %v", err)
	}
	if cfg.ActiveModel != "claude-opus-4" {
		t.Errorf("ActiveModel: got %q, want %q", cfg.ActiveModel, "claude-opus-4")
	}
}

// --- Provider store tests ---

func TestProviderCRUD(t *testing.T) {
	s := newTestStore(t)

	p := Provider{
		ID:          "prov-001",
		DisplayName: "OpenAI",
		BaseURL:     "https://api.openai.com",
		APIKeyEnc:   "sk-secret",
		Models:      []string{"gpt-4o", "gpt-3.5-turbo"},
	}
	if err := s.AddProvider(p); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}

	got, err := s.GetProvider("prov-001")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.DisplayName != "OpenAI" {
		t.Errorf("DisplayName: got %q, want %q", got.DisplayName, "OpenAI")
	}
	if got.APIKeyEnc != "sk-secret" {
		t.Errorf("APIKey: got %q, want %q", got.APIKeyEnc, "sk-secret")
	}
	if len(got.Models) != 2 || got.Models[0] != "gpt-4o" {
		t.Errorf("Models: got %v", got.Models)
	}

	// Update.
	got.DisplayName = "OpenAI Updated"
	got.APIKeyEnc = "sk-newsecret"
	if err := s.UpdateProvider(got); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	got2, _ := s.GetProvider("prov-001")
	if got2.DisplayName != "OpenAI Updated" || got2.APIKeyEnc != "sk-newsecret" {
		t.Errorf("after update: %+v", got2)
	}

	// List.
	list, err := s.ListProviders()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListProviders: %v, len=%d", err, len(list))
	}

	// Delete.
	if err := s.DeleteProvider("prov-001"); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	list2, _ := s.ListProviders()
	if len(list2) != 0 {
		t.Errorf("expected empty list after delete, got %d", len(list2))
	}
}

func TestProviderSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = 99
	}
	dbPath := filepath.Join(dir, "airouter.db")

	s1, _ := NewStore(dbPath, key)
	_ = s1.AddProvider(Provider{
		ID:          "p1",
		DisplayName: "Test",
		BaseURL:     "http://localhost:8000",
		APIKeyEnc:   "test-key",
		Models:      []string{"local-model"},
	})
	s1.Close()

	s2, err := NewStore(dbPath, key)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	list, err := s2.ListProviders()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListProviders after reopen: %v, len=%d", err, len(list))
	}
	if list[0].APIKeyEnc != "test-key" {
		t.Errorf("APIKeyEnc not preserved: got %q", list[0].APIKeyEnc)
	}
}

// --- BYO mode routing tests ---

func TestBYOMode_RoundTrip(t *testing.T) {
	// Stub OpenAI-compatible endpoint.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "not found", 404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer stub.Close()

	s := newTestStore(t)
	_ = s.SetConfig(Config{Mode: ModeBYO, ActiveModel: "test-model"})
	_ = s.AddProvider(Provider{
		ID:          "stub-prov",
		DisplayName: "Stub",
		BaseURL:     stub.URL,
		APIKeyEnc:   "test-key",
		Models:      []string{"test-model"},
	})

	router := NewRouter(s)
	stream, err := router.Route(context.Background(), ChatRequest{
		Model:    "test-model",
		Messages: []Message{{Role: "user", Content: "hello"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	content, err := DrainSSE(stream)
	if err != nil {
		t.Fatalf("DrainSSE: %v", err)
	}
	if content != "hello" {
		t.Errorf("content: got %q, want %q", content, "hello")
	}
}

func TestBYOMode_FallsBackToFirstProvider(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer stub.Close()

	s := newTestStore(t)
	_ = s.AddProvider(Provider{
		ID:          "default-prov",
		DisplayName: "Default",
		BaseURL:     stub.URL,
		APIKeyEnc:   "",
		Models:      []string{"default-model"},
	})

	router := NewRouter(s)
	// Request a model not listed in any provider — falls back to first provider.
	stream, err := router.Route(context.Background(), ChatRequest{
		Model:    "unknown-model",
		Messages: []Message{{Role: "user", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("Route with unknown model: %v", err)
	}
	stream.Close()
}

func TestBYOMode_NoProviders_Error(t *testing.T) {
	s := newTestStore(t)
	router := NewRouter(s)
	_, err := router.Route(context.Background(), ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error when no providers configured")
	}
	if !strings.Contains(err.Error(), "no providers") {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- Cloud mode routing tests ---

func TestCloudMode_SendsDeviceCertHeader(t *testing.T) {
	var gotCertHeader string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCertHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"cloud-ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer stub.Close()

	// Override the cloud proxy URL to our stub.
	os.Setenv("VULOS_AI_PROXY_URL", stub.URL)
	defer os.Unsetenv("VULOS_AI_PROXY_URL")

	s := newTestStore(t)
	_ = s.SetConfig(Config{Mode: ModeCloud, ActiveModel: "cloud-model"})

	router := NewRouter(s)
	router.DeviceCert = []byte("fake-device-cert-pem")

	stream, err := router.Route(context.Background(), ChatRequest{
		Model:    "cloud-model",
		Messages: []Message{{Role: "user", Content: "hello from cloud"}},
	})
	if err != nil {
		t.Fatalf("Route (cloud): %v", err)
	}
	content, err := DrainSSE(stream)
	if err != nil {
		t.Fatalf("DrainSSE (cloud): %v", err)
	}
	if content != "cloud-ok" {
		t.Errorf("content: got %q, want %q", content, "cloud-ok")
	}
	if !strings.HasPrefix(gotCertHeader, "VulosDevice ") {
		t.Errorf("Authorization header: got %q, expected VulosDevice prefix", gotCertHeader)
	}
}

func TestCloudMode_StreamsProxyResponse(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Multi-chunk stream.
		for _, chunk := range []string{"Hello", " ", "World"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", chunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer stub.Close()

	os.Setenv("VULOS_AI_PROXY_URL", stub.URL)
	defer os.Unsetenv("VULOS_AI_PROXY_URL")

	s := newTestStore(t)
	_ = s.SetConfig(Config{Mode: ModeCloud})
	router := NewRouter(s)
	router.DeviceCert = []byte("cert")

	stream, err := router.Route(context.Background(), ChatRequest{
		Model:    "any",
		Messages: []Message{{Role: "user", Content: "stream test"}},
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	all, err := io.ReadAll(stream)
	stream.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !strings.Contains(string(all), "Hello") {
		t.Errorf("expected streamed content, got: %s", all)
	}
}

// --- Encryption round-trip ---

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	s := &Store{encryptKey: key}

	plaintext := "sk-supersecret-api-key-12345"
	enc, err := s.encryptKey32(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if enc == plaintext {
		t.Fatal("ciphertext must differ from plaintext")
	}

	dec, err := s.decryptKey32(enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if dec != plaintext {
		t.Errorf("round-trip: got %q, want %q", dec, plaintext)
	}
}

func TestEncryptEmptyKey(t *testing.T) {
	key := make([]byte, 32)
	s := &Store{encryptKey: key}
	enc, err := s.encryptKey32("")
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	if enc != "" {
		t.Errorf("expected empty ciphertext for empty plaintext, got %q", enc)
	}
	dec, err := s.decryptKey32("")
	if err != nil {
		t.Fatalf("decrypt empty: %v", err)
	}
	if dec != "" {
		t.Errorf("expected empty decryption, got %q", dec)
	}
}

// --- Model list helpers ---

func TestJoinSplitModels(t *testing.T) {
	cases := []struct {
		models []string
		joined string
	}{
		{nil, ""},
		{[]string{"gpt-4o"}, "gpt-4o"},
		{[]string{"gpt-4o", "gpt-3.5"}, "gpt-4o,gpt-3.5"},
	}
	for _, tc := range cases {
		joined := joinModels(tc.models)
		if joined != tc.joined {
			t.Errorf("joinModels(%v) = %q, want %q", tc.models, joined, tc.joined)
		}
		got := splitModels(joined)
		if len(got) != len(tc.models) {
			t.Errorf("splitModels(%q) = %v, want %v", joined, got, tc.models)
		}
	}
}
