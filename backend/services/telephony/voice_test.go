package telephony

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ----------------------------------------------------------------------------
// fakeVoiceDB — an in-memory VoiceDBusClient for testing
// ----------------------------------------------------------------------------

// fakeVoiceDB is a fully mockable VoiceDBusClient used by all voice tests.
// It never touches D-Bus, gdbus, or any hardware.
type fakeVoiceDB struct {
	mu        sync.Mutex
	avail     bool
	calls     []VoiceCall
	dialErr   error
	acceptErr error
	hangupErr error
	dtmfErr   error
	// dialledNumbers records every Dial call for assertion.
	dialledNumbers []string
	// acceptedPaths records every Accept call.
	acceptedPaths []string
	// hungupPaths records every Hangup call.
	hungupPaths []string
	// dtmfLog records every SendDTMF call as "path:tones".
	dtmfLog []string
}

func newFakeDB(avail bool, calls ...VoiceCall) *fakeVoiceDB {
	return &fakeVoiceDB{avail: avail, calls: calls}
}

func (f *fakeVoiceDB) Available() bool { return f.avail }

func (f *fakeVoiceDB) ListCalls(_ string) ([]VoiceCall, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]VoiceCall, len(f.calls))
	copy(cp, f.calls)
	return cp, nil
}

func (f *fakeVoiceDB) Dial(_ string, number string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dialErr != nil {
		return "", f.dialErr
	}
	path := fmt.Sprintf("/org/freedesktop/ModemManager1/Call/%d", len(f.calls))
	f.calls = append(f.calls, VoiceCall{
		Path:      path,
		State:     "dialing",
		Direction: "outgoing",
		Number:    number,
	})
	f.dialledNumbers = append(f.dialledNumbers, number)
	return path, nil
}

func (f *fakeVoiceDB) Accept(callPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acceptErr != nil {
		return f.acceptErr
	}
	for i, c := range f.calls {
		if c.Path == callPath {
			f.calls[i].State = "active"
		}
	}
	f.acceptedPaths = append(f.acceptedPaths, callPath)
	return nil
}

func (f *fakeVoiceDB) Hangup(callPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hangupErr != nil {
		return f.hangupErr
	}
	filtered := f.calls[:0]
	for _, c := range f.calls {
		if c.Path != callPath {
			filtered = append(filtered, c)
		}
	}
	f.calls = filtered
	f.hungupPaths = append(f.hungupPaths, callPath)
	return nil
}

func (f *fakeVoiceDB) SendDTMF(callPath, tones string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dtmfErr != nil {
		return f.dtmfErr
	}
	f.dtmfLog = append(f.dtmfLog, callPath+":"+tones)
	return nil
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

func newTestVC(db *fakeVoiceDB) *VoiceController {
	return newVoiceControllerWith(db, "/org/freedesktop/ModemManager1/Modem/0")
}

func newTestVCNoModem(db *fakeVoiceDB) *VoiceController {
	return newVoiceControllerWith(db, "")
}

func postJSON(t *testing.T, handler http.HandlerFunc, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func decodeVoiceResp(t *testing.T, rec *httptest.ResponseRecorder) voiceResponse {
	t.Helper()
	var resp voiceResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode voiceResponse: %v (body=%q)", err, rec.Body.String())
	}
	return resp
}

// ----------------------------------------------------------------------------
// VoiceDBusClient interface — unavailable client
// ----------------------------------------------------------------------------

// TestVoiceDBClientUnavailableListCalls verifies the production client's
// no-modem path returns an empty slice (not nil, not a panic).
func TestVoiceDBClientUnavailableListCalls(t *testing.T) {
	g := &gdbusVoiceClient{avail: false}
	calls, err := g.ListCalls("/org/freedesktop/ModemManager1/Modem/0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls == nil {
		t.Fatal("ListCalls returned nil; want empty slice")
	}
	if len(calls) != 0 {
		t.Errorf("want 0 calls, got %d", len(calls))
	}
}

func TestVoiceDBClientUnavailableDial(t *testing.T) {
	g := &gdbusVoiceClient{avail: false}
	_, err := g.Dial("/org/freedesktop/ModemManager1/Modem/0", "+1234567890")
	if err == nil {
		t.Fatal("expected error when unavailable, got nil")
	}
}

func TestVoiceDBClientUnavailableAccept(t *testing.T) {
	g := &gdbusVoiceClient{avail: false}
	if err := g.Accept("/org/freedesktop/ModemManager1/Call/0"); err == nil {
		t.Fatal("expected error when unavailable")
	}
}

func TestVoiceDBClientUnavailableHangup(t *testing.T) {
	g := &gdbusVoiceClient{avail: false}
	if err := g.Hangup("/org/freedesktop/ModemManager1/Call/0"); err == nil {
		t.Fatal("expected error when unavailable")
	}
}

func TestVoiceDBClientUnavailableSendDTMF(t *testing.T) {
	g := &gdbusVoiceClient{avail: false}
	if err := g.SendDTMF("/org/freedesktop/ModemManager1/Call/0", "123"); err == nil {
		t.Fatal("expected error when unavailable")
	}
}

// ----------------------------------------------------------------------------
// Dial endpoint
// ----------------------------------------------------------------------------

func TestDialSuccess(t *testing.T) {
	db := newFakeDB(true)
	vc := newTestVC(db)

	rec := postJSON(t, vc.handleDial, "/api/telephony/voice/dial",
		voiceDialRequest{Number: "+27831234567"})

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	resp := decodeVoiceResp(t, rec)
	if !resp.OK {
		t.Errorf("want ok=true, got error: %s", resp.Error)
	}
	if resp.CallPath == "" {
		t.Error("want non-empty call_path")
	}
	db.mu.Lock()
	dialled := db.dialledNumbers
	db.mu.Unlock()
	if len(dialled) != 1 || dialled[0] != "+27831234567" {
		t.Errorf("want dialled [+27831234567], got %v", dialled)
	}
}

func TestDialMethodNotAllowed(t *testing.T) {
	vc := newTestVC(newFakeDB(true))
	req := httptest.NewRequest(http.MethodGet, "/api/telephony/voice/dial", nil)
	rec := httptest.NewRecorder()
	vc.handleDial(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", rec.Code)
	}
}

func TestDialMissingNumber(t *testing.T) {
	vc := newTestVC(newFakeDB(true))
	rec := postJSON(t, vc.handleDial, "/api/telephony/voice/dial",
		map[string]string{"number": ""})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rec.Code)
	}
}

func TestDialNoModem(t *testing.T) {
	vc := newTestVCNoModem(newFakeDB(true))
	rec := postJSON(t, vc.handleDial, "/api/telephony/voice/dial",
		voiceDialRequest{Number: "+27831234567"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503, got %d", rec.Code)
	}
}

func TestDialDBError(t *testing.T) {
	db := newFakeDB(true)
	db.dialErr = fmt.Errorf("modem busy")
	vc := newTestVC(db)
	rec := postJSON(t, vc.handleDial, "/api/telephony/voice/dial",
		voiceDialRequest{Number: "+27831234567"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", rec.Code)
	}
}

// ----------------------------------------------------------------------------
// Answer endpoint
// ----------------------------------------------------------------------------

func TestAnswerSuccess(t *testing.T) {
	const callPath = "/org/freedesktop/ModemManager1/Call/0"
	db := newFakeDB(true, VoiceCall{
		Path:      callPath,
		State:     "ringing",
		Direction: "incoming",
		Number:    "+27831234567",
	})
	vc := newTestVC(db)

	rec := postJSON(t, vc.handleAnswer, "/api/telephony/voice/answer",
		voiceCallPathRequest{CallPath: callPath})

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	resp := decodeVoiceResp(t, rec)
	if !resp.OK {
		t.Errorf("want ok=true, got error: %s", resp.Error)
	}
	db.mu.Lock()
	accepted := db.acceptedPaths
	db.mu.Unlock()
	if len(accepted) != 1 || accepted[0] != callPath {
		t.Errorf("want accepted [%s], got %v", callPath, accepted)
	}
}

func TestAnswerMethodNotAllowed(t *testing.T) {
	vc := newTestVC(newFakeDB(true))
	req := httptest.NewRequest(http.MethodGet, "/api/telephony/voice/answer", nil)
	rec := httptest.NewRecorder()
	vc.handleAnswer(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", rec.Code)
	}
}

func TestAnswerMissingCallPath(t *testing.T) {
	vc := newTestVC(newFakeDB(true))
	rec := postJSON(t, vc.handleAnswer, "/api/telephony/voice/answer",
		voiceCallPathRequest{})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rec.Code)
	}
}

func TestAnswerDBError(t *testing.T) {
	db := newFakeDB(true)
	db.acceptErr = fmt.Errorf("call gone")
	vc := newTestVC(db)
	rec := postJSON(t, vc.handleAnswer, "/api/telephony/voice/answer",
		voiceCallPathRequest{CallPath: "/org/freedesktop/ModemManager1/Call/0"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", rec.Code)
	}
}

// ----------------------------------------------------------------------------
// Hangup endpoint
// ----------------------------------------------------------------------------

func TestHangupSuccess(t *testing.T) {
	const callPath = "/org/freedesktop/ModemManager1/Call/0"
	db := newFakeDB(true, VoiceCall{
		Path:      callPath,
		State:     "active",
		Direction: "outgoing",
		Number:    "+27831234567",
	})
	vc := newTestVC(db)

	rec := postJSON(t, vc.handleHangup, "/api/telephony/voice/hangup",
		voiceCallPathRequest{CallPath: callPath})

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	resp := decodeVoiceResp(t, rec)
	if !resp.OK {
		t.Errorf("want ok=true, got error: %s", resp.Error)
	}
	db.mu.Lock()
	hungs := db.hungupPaths
	db.mu.Unlock()
	if len(hungs) != 1 || hungs[0] != callPath {
		t.Errorf("want hungup [%s], got %v", callPath, hungs)
	}
}

func TestHangupMethodNotAllowed(t *testing.T) {
	vc := newTestVC(newFakeDB(true))
	req := httptest.NewRequest(http.MethodGet, "/api/telephony/voice/hangup", nil)
	rec := httptest.NewRecorder()
	vc.handleHangup(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", rec.Code)
	}
}

func TestHangupMissingCallPath(t *testing.T) {
	vc := newTestVC(newFakeDB(true))
	rec := postJSON(t, vc.handleHangup, "/api/telephony/voice/hangup",
		voiceCallPathRequest{})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rec.Code)
	}
}

func TestHangupDBError(t *testing.T) {
	db := newFakeDB(true)
	db.hangupErr = fmt.Errorf("already terminated")
	vc := newTestVC(db)
	rec := postJSON(t, vc.handleHangup, "/api/telephony/voice/hangup",
		voiceCallPathRequest{CallPath: "/org/freedesktop/ModemManager1/Call/0"})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", rec.Code)
	}
}

// ----------------------------------------------------------------------------
// DTMF endpoint
// ----------------------------------------------------------------------------

func TestDTMFSuccess(t *testing.T) {
	const callPath = "/org/freedesktop/ModemManager1/Call/0"
	db := newFakeDB(true, VoiceCall{Path: callPath, State: "active"})
	vc := newTestVC(db)

	rec := postJSON(t, vc.handleDTMF, "/api/telephony/voice/dtmf",
		voiceCallPathRequest{CallPath: callPath, Tones: "1234#"})

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	resp := decodeVoiceResp(t, rec)
	if !resp.OK {
		t.Errorf("want ok=true, got error: %s", resp.Error)
	}
	db.mu.Lock()
	log := db.dtmfLog
	db.mu.Unlock()
	if len(log) != 1 || log[0] != callPath+":1234#" {
		t.Errorf("dtmf log mismatch: %v", log)
	}
}

func TestDTMFMethodNotAllowed(t *testing.T) {
	vc := newTestVC(newFakeDB(true))
	req := httptest.NewRequest(http.MethodGet, "/api/telephony/voice/dtmf", nil)
	rec := httptest.NewRecorder()
	vc.handleDTMF(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", rec.Code)
	}
}

func TestDTMFMissingTones(t *testing.T) {
	vc := newTestVC(newFakeDB(true))
	rec := postJSON(t, vc.handleDTMF, "/api/telephony/voice/dtmf",
		voiceCallPathRequest{CallPath: "/org/freedesktop/ModemManager1/Call/0"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rec.Code)
	}
}

func TestDTMFMissingCallPath(t *testing.T) {
	vc := newTestVC(newFakeDB(true))
	rec := postJSON(t, vc.handleDTMF, "/api/telephony/voice/dtmf",
		voiceCallPathRequest{Tones: "1"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rec.Code)
	}
}

func TestDTMFDBError(t *testing.T) {
	db := newFakeDB(true)
	db.dtmfErr = fmt.Errorf("call not active")
	vc := newTestVC(db)
	rec := postJSON(t, vc.handleDTMF, "/api/telephony/voice/dtmf",
		voiceCallPathRequest{
			CallPath: "/org/freedesktop/ModemManager1/Call/0",
			Tones:    "9",
		})
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d", rec.Code)
	}
}

// ----------------------------------------------------------------------------
// List calls endpoint
// ----------------------------------------------------------------------------

func TestListCallsEmpty(t *testing.T) {
	vc := newTestVC(newFakeDB(true))
	req := httptest.NewRequest(http.MethodGet, "/api/telephony/voice/calls", nil)
	rec := httptest.NewRecorder()
	vc.handleListCalls(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	resp := decodeVoiceResp(t, rec)
	if !resp.OK {
		t.Errorf("want ok=true")
	}
	if resp.Calls == nil {
		t.Error("calls must not be null")
	}
	if len(resp.Calls) != 0 {
		t.Errorf("want 0 calls, got %d", len(resp.Calls))
	}
}

func TestListCallsWithActiveCall(t *testing.T) {
	db := newFakeDB(true, VoiceCall{
		Path:      "/org/freedesktop/ModemManager1/Call/0",
		State:     "active",
		Direction: "incoming",
		Number:    "+27831234567",
	})
	vc := newTestVC(db)
	req := httptest.NewRequest(http.MethodGet, "/api/telephony/voice/calls", nil)
	rec := httptest.NewRecorder()
	vc.handleListCalls(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	resp := decodeVoiceResp(t, rec)
	if len(resp.Calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(resp.Calls))
	}
	c := resp.Calls[0]
	if c.State != "active" {
		t.Errorf("want state=active, got %q", c.State)
	}
	if c.Direction != "incoming" {
		t.Errorf("want direction=incoming, got %q", c.Direction)
	}
	if c.Number != "+27831234567" {
		t.Errorf("want number=+27831234567, got %q", c.Number)
	}
}

func TestListCallsMethodNotAllowed(t *testing.T) {
	vc := newTestVC(newFakeDB(true))
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/calls", nil)
	rec := httptest.NewRecorder()
	vc.handleListCalls(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", rec.Code)
	}
}

// TestListCallsUnavailableDB ensures we get an empty list (not an error)
// when the D-Bus client reports unavailable.
func TestListCallsUnavailableDB(t *testing.T) {
	vc := newTestVC(newFakeDB(false))
	req := httptest.NewRequest(http.MethodGet, "/api/telephony/voice/calls", nil)
	rec := httptest.NewRecorder()
	vc.handleListCalls(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	resp := decodeVoiceResp(t, rec)
	if resp.Calls == nil {
		t.Error("calls must not be null")
	}
}

// ----------------------------------------------------------------------------
// RegisterVoiceHandlers — route registration
// ----------------------------------------------------------------------------

func TestRegisterVoiceHandlers(t *testing.T) {
	vc := newTestVC(newFakeDB(true))
	mux := http.NewServeMux()
	RegisterVoiceHandlers(mux, vc)

	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/telephony/voice/calls"},
		{http.MethodGet, "/api/telephony/voice/ws"},
		{http.MethodPost, "/api/telephony/voice/dial"},
		{http.MethodPost, "/api/telephony/voice/answer"},
		{http.MethodPost, "/api/telephony/voice/hangup"},
		{http.MethodPost, "/api/telephony/voice/dtmf"},
	}
	for _, r := range routes {
		req := httptest.NewRequest(r.method, r.path, bytes.NewReader([]byte("{}")))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Errorf("route %s %s not registered (got 404)", r.method, r.path)
		}
	}
}

// ----------------------------------------------------------------------------
// WebSocket — call-state push
// ----------------------------------------------------------------------------

// TestVoiceWSConnectsAndReceivesInitialState verifies that the WS endpoint
// upgrades, sends an initial call_state event, and closes cleanly.
func TestVoiceWSConnectsAndReceivesInitialState(t *testing.T) {
	db := newFakeDB(true, VoiceCall{
		Path:      "/org/freedesktop/ModemManager1/Call/0",
		State:     "ringing",
		Direction: "incoming",
		Number:    "+27831234567",
	})
	vc := newTestVC(db)
	mux := http.NewServeMux()
	RegisterVoiceHandlers(mux, vc)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/telephony/voice/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}

	var ev voiceEvent
	if err := json.Unmarshal(msg, &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Type != "call_state" {
		t.Errorf("want type=call_state, got %q", ev.Type)
	}
	if len(ev.Calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(ev.Calls))
	}
	if ev.Calls[0].State != "ringing" {
		t.Errorf("want state=ringing, got %q", ev.Calls[0].State)
	}
}

// TestVoiceWSBroadcastOnDial verifies that dialling a number triggers a
// call_state push to all connected WebSocket clients.
func TestVoiceWSBroadcastOnDial(t *testing.T) {
	db := newFakeDB(true)
	vc := newTestVC(db)
	mux := http.NewServeMux()
	RegisterVoiceHandlers(mux, vc)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Connect a WS subscriber before dialling.
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/telephony/voice/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	// Drain the initial call_state (empty).
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("initial read: %v", err)
	}

	// Issue a dial request over HTTP.
	dialURL := srv.URL + "/api/telephony/voice/dial"
	body, _ := json.Marshal(voiceDialRequest{Number: "+27831234567"})
	resp, err := http.Post(dialURL, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("dial POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dial want 200, got %d", resp.StatusCode)
	}

	// Expect a push with the new call.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ws push read: %v", err)
	}
	var ev voiceEvent
	if err := json.Unmarshal(msg, &ev); err != nil {
		t.Fatalf("unmarshal push: %v", err)
	}
	if ev.Type != "call_state" {
		t.Errorf("want type=call_state, got %q", ev.Type)
	}
	if len(ev.Calls) == 0 {
		t.Error("want at least 1 call in push after dial")
	}
}

// TestVoiceHubBroadcastNoPanic verifies that pushing to an empty hub does not panic.
func TestVoiceHubBroadcastNoPanic(t *testing.T) {
	hub := newVoiceHub()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("push panicked: %v", r)
		}
	}()
	hub.push([]byte(`{"type":"call_state","calls":[]}`))
}

// ----------------------------------------------------------------------------
// StartVoicePoller — background polling
// ----------------------------------------------------------------------------

// TestStartVoicePollerPushesState verifies the poller triggers broadcastCallState
// at least once within a short window.
func TestStartVoicePollerPushesState(t *testing.T) {
	db := newFakeDB(true, VoiceCall{
		Path:  "/org/freedesktop/ModemManager1/Call/0",
		State: "active",
	})
	vc := newTestVC(db)
	mux := http.NewServeMux()
	RegisterVoiceHandlers(mux, vc)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/telephony/voice/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	// Drain initial event.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("initial read: %v", err)
	}

	// Start the poller with a short interval.
	stop := make(chan struct{})
	defer close(stop)
	StartVoicePoller(vc, 50*time.Millisecond, stop)

	// Expect at least one poll-driven push within 500ms.
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("poller push not received: %v", err)
	}
	var ev voiceEvent
	if err := json.Unmarshal(msg, &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Type != "call_state" {
		t.Errorf("want call_state, got %q", ev.Type)
	}
}

// TestStartVoicePollerStops verifies the poller goroutine exits on stop signal.
func TestStartVoicePollerStops(t *testing.T) {
	vc := newTestVC(newFakeDB(false))
	stop := make(chan struct{})
	StartVoicePoller(vc, 10*time.Millisecond, stop)
	// Closing stop should not cause a panic or goroutine leak.
	close(stop)
	time.Sleep(50 * time.Millisecond) // allow goroutine to exit
}

// ----------------------------------------------------------------------------
// Enum / parsing helpers
// ----------------------------------------------------------------------------

func TestVoiceCallStateString(t *testing.T) {
	cases := map[int]string{
		callStateDialing:    "dialing",
		callStateRinging:    "ringing",
		callStateActive:     "active",
		callStateHeld:       "held",
		callStateWaiting:    "waiting",
		callStateTerminated: "terminated",
		callStateUnknown:    "unknown",
		99:                  "unknown",
	}
	for k, want := range cases {
		if got := voiceCallStateString(k); got != want {
			t.Errorf("voiceCallStateString(%d) = %q, want %q", k, got, want)
		}
	}
}

func TestCallDirectionString(t *testing.T) {
	if got := callDirectionString(callDirectionIncoming); got != "incoming" {
		t.Errorf("want incoming, got %q", got)
	}
	if got := callDirectionString(callDirectionOutgoing); got != "outgoing" {
		t.Errorf("want outgoing, got %q", got)
	}
	if got := callDirectionString(callDirectionUnknown); got != "unknown" {
		t.Errorf("want unknown, got %q", got)
	}
}

func TestParseCallPaths(t *testing.T) {
	out := "([objectpath '/org/freedesktop/ModemManager1/Call/0', objectpath '/org/freedesktop/ModemManager1/Call/1'],)"
	paths := parseCallPaths(out)
	if len(paths) != 2 {
		t.Fatalf("want 2 paths, got %d: %v", len(paths), paths)
	}
	if paths[0] != "/org/freedesktop/ModemManager1/Call/0" {
		t.Errorf("unexpected path[0]: %q", paths[0])
	}
	if paths[1] != "/org/freedesktop/ModemManager1/Call/1" {
		t.Errorf("unexpected path[1]: %q", paths[1])
	}
}

func TestParseCallPathsEmpty(t *testing.T) {
	paths := parseCallPaths("([],)")
	if len(paths) != 0 {
		t.Errorf("want 0 paths, got %d", len(paths))
	}
}

func TestExtractObjectPath(t *testing.T) {
	out := "(objectpath '/org/freedesktop/ModemManager1/Call/0',)\n"
	got := extractObjectPath(out)
	if got != "/org/freedesktop/ModemManager1/Call/0" {
		t.Errorf("got %q", got)
	}
}

func TestExtractObjectPathEmpty(t *testing.T) {
	got := extractObjectPath("()")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
