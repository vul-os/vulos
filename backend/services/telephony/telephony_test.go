package telephony

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// -----------------------------------------------------------------------
// mmClient unit tests
// -----------------------------------------------------------------------

// TestMMClientNoDBus verifies that newMMClient() returns a client with
// available=false when gdbus is absent (the test environment).
func TestMMClientNoDBus(t *testing.T) {
	c := newMMClient()
	// In CI / Docker without D-Bus, available must be false and ListModems
	// must return an empty (non-nil) slice — never panic.
	modems := c.ListModems()
	if modems == nil {
		t.Fatal("ListModems() returned nil; want empty slice")
	}
	// available may be true if the test host genuinely has ModemManager —
	// we only assert the absence case when it's actually absent.
	if c.available {
		t.Log("ModemManager reachable — skipping absence assertions")
		return
	}
	if len(modems) != 0 {
		t.Errorf("expected 0 modems without D-Bus, got %d", len(modems))
	}
}

// TestListModemsEmptyWhenUnavailable uses an explicitly unavailable client.
func TestListModemsEmptyWhenUnavailable(t *testing.T) {
	c := &mmClient{available: false}
	modems := c.ListModems()
	if modems == nil {
		t.Fatal("ListModems() returned nil; want empty non-nil slice")
	}
	if len(modems) != 0 {
		t.Errorf("want 0 modems, got %d", len(modems))
	}
}

// -----------------------------------------------------------------------
// Service / HTTP handler tests
// -----------------------------------------------------------------------

func newTestService() *Service {
	return &Service{
		mm:      &mmClient{available: false},
		clients: make(map[*websocket.Conn]bool),
	}
}

// TestStatusEndpointOK checks that the status endpoint returns 200 and
// valid JSON with a non-null modems array even when no modem is present.
func TestStatusEndpointOK(t *testing.T) {
	svc := newTestService()
	req := httptest.NewRequest(http.MethodGet, "/api/telephony/status", nil)
	rec := httptest.NewRecorder()

	svc.handleStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("want application/json content-type, got %q", ct)
	}

	var resp StatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	if resp.Modems == nil {
		t.Error("modems field is null; want empty array")
	}
	if resp.DBusOK {
		t.Error("dbus_ok should be false for unavailable client")
	}
	if resp.Timestamp <= 0 {
		t.Error("timestamp should be positive")
	}
}

// TestStatusEndpointMethodNotAllowed ensures non-GET requests are rejected.
func TestStatusEndpointMethodNotAllowed(t *testing.T) {
	svc := newTestService()
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/status", nil)
	rec := httptest.NewRecorder()

	svc.handleStatus(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
}

// TestRegisterHandlers verifies that routes are correctly registered on a mux.
func TestRegisterHandlers(t *testing.T) {
	svc := newTestService()
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux)

	for _, path := range []string{"/api/telephony/status", "/api/telephony/ws"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// ws endpoint returns 400 (bad WS handshake) via httptest — that's fine.
		// Status endpoint returns 200. Both prove the route exists (not 404).
		if rec.Code == http.StatusNotFound {
			t.Errorf("path %s not registered (got 404)", path)
		}
	}
}

// TestWebSocketConnects verifies that the WS endpoint upgrades a connection,
// sends an initial "modems_updated" event, and closes cleanly.
func TestWebSocketConnects(t *testing.T) {
	svc := newTestService()
	mux := http.NewServeMux()
	svc.RegisterHandlers(mux)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/telephony/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	// Expect initial modems_updated message within 2 s.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}

	var ev Event
	if err := json.Unmarshal(msg, &ev); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if ev.Type != "modems_updated" {
		t.Errorf("want type=modems_updated, got %q", ev.Type)
	}
}

// TestBroadcastNoPanic ensures broadcast() doesn't panic when there are no
// connected clients.
func TestBroadcastNoPanic(t *testing.T) {
	svc := newTestService()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("broadcast panicked: %v", r)
		}
	}()
	svc.broadcast([]byte(`{"type":"test"}`))
}

// -----------------------------------------------------------------------
// enum / parsing helper tests
// -----------------------------------------------------------------------

func TestModemStateString(t *testing.T) {
	cases := map[int]string{
		3:  "disabled",
		8:  "registered",
		11: "connected",
		0:  "unknown",
	}
	for k, want := range cases {
		if got := modemStateString(k); got != want {
			t.Errorf("modemStateString(%d) = %q, want %q", k, got, want)
		}
	}
}

func TestAccessTechString(t *testing.T) {
	// LTE = bit 14
	if got := accessTechString(1 << 14); got != "lte" {
		t.Errorf("want lte, got %q", got)
	}
	// 5G-NR = bit 15
	if got := accessTechString(1 << 15); got != "5g-nr" {
		t.Errorf("want 5g-nr, got %q", got)
	}
}

func TestExtractStringVal(t *testing.T) {
	line := "  'Manufacturer': <'Sierra Wireless'>,"
	if got := extractStringVal(line); got != "Sierra Wireless" {
		t.Errorf("got %q, want %q", got, "Sierra Wireless")
	}
}

func TestExtractIntVal(t *testing.T) {
	line := "  'State': <uint32 8>,"
	if got := extractIntVal(line); got != 8 {
		t.Errorf("got %d, want 8", got)
	}
}

func TestParseModemPaths(t *testing.T) {
	output := `({'org.freedesktop.ModemManager1.Modem': {'Path': <'/org/freedesktop/ModemManager1/Modem/0'>}},)`
	paths := parseModemPaths(output)
	if len(paths) != 1 {
		t.Fatalf("want 1 path, got %d: %v", len(paths), paths)
	}
	if paths[0] != "/org/freedesktop/ModemManager1/Modem/0" {
		t.Errorf("unexpected path: %q", paths[0])
	}
}

func TestParseModemPathsEmpty(t *testing.T) {
	paths := parseModemPaths("({})")
	if paths == nil {
		// nil is acceptable for empty output
		return
	}
	if len(paths) != 0 {
		t.Errorf("expected 0 paths, got %d", len(paths))
	}
}
