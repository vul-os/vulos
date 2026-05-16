package telephony

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// ─── mockVoiceDBusClient ─────────────────────────────────────────────────────

// mockVoiceDBusClient is an in-process VoiceDBusClient for tests; no hardware.
type mockVoiceDBusClient struct {
	mu        sync.Mutex
	avail     bool
	calls     []VoiceCall
	dialErr   error
	answerErr error
	hangupErr error
	dtmfErr   error
	listErr   error

	// recorded calls for assertion
	dialedNumber string
	answeredPath string
	hungupPath   string
	dtmfPath     string
	dtmfTones    string
}

func newMockVoiceClient() *mockVoiceDBusClient {
	return &mockVoiceDBusClient{avail: true}
}

func (m *mockVoiceDBusClient) VoiceAvailable() bool { return m.avail }

func (m *mockVoiceDBusClient) VoiceDial(modemPath, number string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dialedNumber = number
	if m.dialErr != nil {
		return "", m.dialErr
	}
	path := "/org/freedesktop/ModemManager1/Call/0"
	m.calls = append(m.calls, VoiceCall{
		Path:      path,
		Number:    number,
		State:     voiceCallStateString(VoiceCallStateDialing),
		Direction: voiceCallDirectionString(VoiceCallDirectionOutgoing),
	})
	return path, nil
}

func (m *mockVoiceDBusClient) VoiceAnswer(modemPath, callPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.answeredPath = callPath
	if m.answerErr != nil {
		return m.answerErr
	}
	for i, c := range m.calls {
		if c.Path == callPath {
			m.calls[i].State = voiceCallStateString(VoiceCallStateActive)
		}
	}
	return nil
}

func (m *mockVoiceDBusClient) VoiceHangup(modemPath, callPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hungupPath = callPath
	if m.hangupErr != nil {
		return m.hangupErr
	}
	filtered := m.calls[:0]
	for _, c := range m.calls {
		if c.Path != callPath {
			filtered = append(filtered, c)
		}
	}
	m.calls = filtered
	return nil
}

func (m *mockVoiceDBusClient) VoiceSendDTMF(modemPath, callPath, tones string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dtmfPath = callPath
	m.dtmfTones = tones
	return m.dtmfErr
}

func (m *mockVoiceDBusClient) VoiceListCalls(modemPath string) ([]VoiceCall, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	out := make([]VoiceCall, len(m.calls))
	copy(out, m.calls)
	return out, nil
}

// ─── voiceControllerWith helper ──────────────────────────────────────────────

func voiceControllerWith(mock *mockVoiceDBusClient) (*VoiceController, *[][]byte) {
	var mu sync.Mutex
	var broadcasts [][]byte
	broadcastFn := func(data []byte) {
		mu.Lock()
		broadcasts = append(broadcasts, data)
		mu.Unlock()
	}
	vc := voiceNewControllerWith(mock, broadcastFn)
	return vc, &broadcasts
}

// ─── Enum helpers ─────────────────────────────────────────────────────────────

func TestVoiceCallStateString(t *testing.T) {
	cases := []struct {
		state VoiceCallState
		want  string
	}{
		{VoiceCallStateUnknown, "unknown"},
		{VoiceCallStateDialing, "dialing"},
		{VoiceCallStateRingback, "ringback"},
		{VoiceCallStateRinging, "ringing"},
		{VoiceCallStateActive, "active"},
		{VoiceCallStateHeld, "held"},
		{VoiceCallStateWaiting, "waiting"},
		{VoiceCallStateTerminated, "terminated"},
		{VoiceCallState(99), "unknown"},
	}
	for _, tc := range cases {
		got := voiceCallStateString(tc.state)
		if got != tc.want {
			t.Errorf("voiceCallStateString(%d) = %q, want %q", tc.state, got, tc.want)
		}
	}
}

func TestVoiceCallDirectionString(t *testing.T) {
	cases := []struct {
		dir  VoiceCallDirection
		want string
	}{
		{VoiceCallDirectionUnknown, "unknown"},
		{VoiceCallDirectionIncoming, "incoming"},
		{VoiceCallDirectionOutgoing, "outgoing"},
		{VoiceCallDirection(99), "unknown"},
	}
	for _, tc := range cases {
		got := voiceCallDirectionString(tc.dir)
		if got != tc.want {
			t.Errorf("voiceCallDirectionString(%d) = %q, want %q", tc.dir, got, tc.want)
		}
	}
}

// ─── Parsing helpers ─────────────────────────────────────────────────────────

func TestVoiceExtractObjectPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{
			in:   "(<objectpath '/org/freedesktop/ModemManager1/Call/0'>,)",
			want: "/org/freedesktop/ModemManager1/Call/0",
		},
		{
			in:   "(objectpath \"/org/freedesktop/ModemManager1/Call/1\",)",
			want: "/org/freedesktop/ModemManager1/Call/1",
		},
		{in: "error: something", want: ""},
		{in: "", want: ""},
	}
	for _, tc := range cases {
		got := voiceExtractObjectPath(tc.in)
		if got != tc.want {
			t.Errorf("voiceExtractObjectPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestVoiceParseObjectPaths(t *testing.T) {
	out := "([objectpath '/org/freedesktop/ModemManager1/Call/0', objectpath '/org/freedesktop/ModemManager1/Call/1'],)"
	paths := voiceParseObjectPaths(out)
	if len(paths) != 2 {
		t.Fatalf("expected 2 paths, got %d: %v", len(paths), paths)
	}
	if paths[0] != "/org/freedesktop/ModemManager1/Call/0" {
		t.Errorf("paths[0] = %q", paths[0])
	}
	if paths[1] != "/org/freedesktop/ModemManager1/Call/1" {
		t.Errorf("paths[1] = %q", paths[1])
	}
}

func TestVoiceParseObjectPathsEmpty(t *testing.T) {
	paths := voiceParseObjectPaths("([],)")
	if len(paths) != 0 {
		t.Errorf("expected 0 paths, got %v", paths)
	}
}

func TestVoiceExtractStringProp(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Number = '+44123456789';", "+44123456789"},
		{"Number = 'unknown';", "unknown"},
		{"no equals sign", ""},
	}
	for _, tc := range cases {
		got := voiceExtractStringProp(tc.in)
		if got != tc.want {
			t.Errorf("voiceExtractStringProp(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestVoiceExtractIntProp(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"State = 4;", 4},
		{"Direction = 2;", 2},
		{"State = 0;", 0},
		{"no equals", 0},
	}
	for _, tc := range cases {
		got := voiceExtractIntProp(tc.in)
		if got != tc.want {
			t.Errorf("voiceExtractIntProp(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestVoiceParseCallProps(t *testing.T) {
	out := `
  Number = '+12345678';
  State = 4;
  Direction = 2;
`
	got := voiceParseCallProps("/org/freedesktop/ModemManager1/Call/0", out)
	if got.Path != "/org/freedesktop/ModemManager1/Call/0" {
		t.Errorf("Path = %q", got.Path)
	}
	if got.Number != "+12345678" {
		t.Errorf("Number = %q", got.Number)
	}
	if got.State != "active" {
		t.Errorf("State = %q", got.State)
	}
	if got.Direction != "outgoing" {
		t.Errorf("Direction = %q", got.Direction)
	}
}

func TestVoiceParseCallPropsDefaults(t *testing.T) {
	got := voiceParseCallProps("/path/call/0", "")
	if got.State != "unknown" {
		t.Errorf("default State = %q, want 'unknown'", got.State)
	}
	if got.Direction != "unknown" {
		t.Errorf("default Direction = %q, want 'unknown'", got.Direction)
	}
}

// ─── Dial handler ─────────────────────────────────────────────────────────────

func TestVoiceHandleDial_Success(t *testing.T) {
	mock := newMockVoiceClient()
	vc, broadcasts := voiceControllerWith(mock)

	body := `{"modem_path":"/org/freedesktop/ModemManager1/Modem/0","number":"+1234567890"}`
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/dial", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	vc.voiceHandleDial(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp voiceOKResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.OK {
		t.Error("expected OK=true")
	}
	if resp.CallPath != "/org/freedesktop/ModemManager1/Call/0" {
		t.Errorf("CallPath = %q", resp.CallPath)
	}
	if mock.dialedNumber != "+1234567890" {
		t.Errorf("dialed = %q", mock.dialedNumber)
	}
	// Should have pushed a call_state WS event.
	if len(*broadcasts) == 0 {
		t.Error("expected at least one broadcast after dial")
	}
}

func TestVoiceHandleDial_WrongMethod(t *testing.T) {
	mock := newMockVoiceClient()
	vc, _ := voiceControllerWith(mock)
	req := httptest.NewRequest(http.MethodGet, "/api/telephony/voice/dial", nil)
	rr := httptest.NewRecorder()
	vc.voiceHandleDial(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

func TestVoiceHandleDial_MissingFields(t *testing.T) {
	mock := newMockVoiceClient()
	vc, _ := voiceControllerWith(mock)

	cases := []string{
		`{"modem_path":"","number":"+1"}`,
		`{"modem_path":"/path","number":""}`,
		`{}`,
	}
	for _, body := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/dial", bytes.NewBufferString(body))
		rr := httptest.NewRecorder()
		vc.voiceHandleDial(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body=%s: expected 400, got %d", body, rr.Code)
		}
	}
}

func TestVoiceHandleDial_BadJSON(t *testing.T) {
	mock := newMockVoiceClient()
	vc, _ := voiceControllerWith(mock)
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/dial", bytes.NewBufferString("{bad"))
	rr := httptest.NewRecorder()
	vc.voiceHandleDial(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestVoiceHandleDial_DBusError(t *testing.T) {
	mockErr := newMockVoiceClient()
	mockErr.dialErr = voiceErrUnavailable()
	vc, _ := voiceControllerWith(mockErr)
	body := `{"modem_path":"/m","number":"+1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/dial", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	vc.voiceHandleDial(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}

// ─── Answer handler ───────────────────────────────────────────────────────────

func TestVoiceHandleAnswer_Success(t *testing.T) {
	mock := newMockVoiceClient()
	mock.calls = []VoiceCall{{
		Path:      "/org/freedesktop/ModemManager1/Call/0",
		Number:    "+999",
		State:     "ringing",
		Direction: "incoming",
	}}
	vc, _ := voiceControllerWith(mock)

	body := `{"modem_path":"/m","call_path":"/org/freedesktop/ModemManager1/Call/0"}`
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/answer", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	vc.voiceHandleAnswer(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if mock.answeredPath != "/org/freedesktop/ModemManager1/Call/0" {
		t.Errorf("answeredPath = %q", mock.answeredPath)
	}
}

func TestVoiceHandleAnswer_WrongMethod(t *testing.T) {
	mock := newMockVoiceClient()
	vc, _ := voiceControllerWith(mock)
	req := httptest.NewRequest(http.MethodGet, "/api/telephony/voice/answer", nil)
	rr := httptest.NewRecorder()
	vc.voiceHandleAnswer(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

func TestVoiceHandleAnswer_MissingFields(t *testing.T) {
	mock := newMockVoiceClient()
	vc, _ := voiceControllerWith(mock)
	body := `{"modem_path":"","call_path":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/answer", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	vc.voiceHandleAnswer(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestVoiceHandleAnswer_DBusError(t *testing.T) {
	mock := newMockVoiceClient()
	mock.answerErr = voiceErrUnavailable()
	vc, _ := voiceControllerWith(mock)
	body := `{"modem_path":"/m","call_path":"/c"}`
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/answer", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	vc.voiceHandleAnswer(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}

func TestVoiceHandleAnswer_BadJSON(t *testing.T) {
	mock := newMockVoiceClient()
	vc, _ := voiceControllerWith(mock)
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/answer", bytes.NewBufferString("{bad"))
	rr := httptest.NewRecorder()
	vc.voiceHandleAnswer(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

// ─── Hangup handler ───────────────────────────────────────────────────────────

func TestVoiceHandleHangup_Success(t *testing.T) {
	mock := newMockVoiceClient()
	mock.calls = []VoiceCall{{
		Path:  "/org/freedesktop/ModemManager1/Call/0",
		State: "active",
	}}
	vc, _ := voiceControllerWith(mock)

	body := `{"modem_path":"/m","call_path":"/org/freedesktop/ModemManager1/Call/0"}`
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/hangup", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	vc.voiceHandleHangup(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if mock.hungupPath != "/org/freedesktop/ModemManager1/Call/0" {
		t.Errorf("hungupPath = %q", mock.hungupPath)
	}
	// Call should be removed after hangup.
	if len(mock.calls) != 0 {
		t.Errorf("expected 0 calls after hangup, got %d", len(mock.calls))
	}
}

func TestVoiceHandleHangup_WrongMethod(t *testing.T) {
	mock := newMockVoiceClient()
	vc, _ := voiceControllerWith(mock)
	req := httptest.NewRequest(http.MethodGet, "/api/telephony/voice/hangup", nil)
	rr := httptest.NewRecorder()
	vc.voiceHandleHangup(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

func TestVoiceHandleHangup_MissingFields(t *testing.T) {
	mock := newMockVoiceClient()
	vc, _ := voiceControllerWith(mock)
	body := `{"modem_path":"/m","call_path":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/hangup", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	vc.voiceHandleHangup(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestVoiceHandleHangup_DBusError(t *testing.T) {
	mock := newMockVoiceClient()
	mock.hangupErr = voiceErrUnavailable()
	vc, _ := voiceControllerWith(mock)
	body := `{"modem_path":"/m","call_path":"/c"}`
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/hangup", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	vc.voiceHandleHangup(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}

func TestVoiceHandleHangup_BadJSON(t *testing.T) {
	mock := newMockVoiceClient()
	vc, _ := voiceControllerWith(mock)
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/hangup", bytes.NewBufferString("{bad"))
	rr := httptest.NewRecorder()
	vc.voiceHandleHangup(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

// ─── DTMF handler ─────────────────────────────────────────────────────────────

func TestVoiceHandleDTMF_Success(t *testing.T) {
	mock := newMockVoiceClient()
	vc, _ := voiceControllerWith(mock)

	body := `{"modem_path":"/m","call_path":"/c","tones":"1234"}`
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/dtmf", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	vc.voiceHandleDTMF(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if mock.dtmfTones != "1234" {
		t.Errorf("dtmfTones = %q", mock.dtmfTones)
	}
}

func TestVoiceHandleDTMF_WrongMethod(t *testing.T) {
	mock := newMockVoiceClient()
	vc, _ := voiceControllerWith(mock)
	req := httptest.NewRequest(http.MethodGet, "/api/telephony/voice/dtmf", nil)
	rr := httptest.NewRecorder()
	vc.voiceHandleDTMF(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

func TestVoiceHandleDTMF_MissingTones(t *testing.T) {
	mock := newMockVoiceClient()
	vc, _ := voiceControllerWith(mock)
	body := `{"modem_path":"/m","call_path":"/c","tones":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/dtmf", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	vc.voiceHandleDTMF(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestVoiceHandleDTMF_DBusError(t *testing.T) {
	mock := newMockVoiceClient()
	mock.dtmfErr = voiceErrUnavailable()
	vc, _ := voiceControllerWith(mock)
	body := `{"modem_path":"/m","call_path":"/c","tones":"9"}`
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/dtmf", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	vc.voiceHandleDTMF(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}

func TestVoiceHandleDTMF_BadJSON(t *testing.T) {
	mock := newMockVoiceClient()
	vc, _ := voiceControllerWith(mock)
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/dtmf", bytes.NewBufferString("{bad"))
	rr := httptest.NewRecorder()
	vc.voiceHandleDTMF(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

// ─── ListCalls handler ────────────────────────────────────────────────────────

func TestVoiceHandleListCalls_Success(t *testing.T) {
	mock := newMockVoiceClient()
	mock.calls = []VoiceCall{
		{Path: "/c/0", Number: "+1", State: "active", Direction: "outgoing"},
	}
	vc, _ := voiceControllerWith(mock)

	req := httptest.NewRequest(http.MethodGet, "/api/telephony/voice/calls?modem_path=/m", nil)
	rr := httptest.NewRecorder()
	vc.voiceHandleListCalls(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var calls []VoiceCall
	if err := json.Unmarshal(rr.Body.Bytes(), &calls); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Number != "+1" {
		t.Errorf("Number = %q", calls[0].Number)
	}
}

func TestVoiceHandleListCalls_WrongMethod(t *testing.T) {
	mock := newMockVoiceClient()
	vc, _ := voiceControllerWith(mock)
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/calls", nil)
	rr := httptest.NewRecorder()
	vc.voiceHandleListCalls(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rr.Code)
	}
}

func TestVoiceHandleListCalls_MissingModemPath(t *testing.T) {
	mock := newMockVoiceClient()
	vc, _ := voiceControllerWith(mock)
	req := httptest.NewRequest(http.MethodGet, "/api/telephony/voice/calls", nil)
	rr := httptest.NewRecorder()
	vc.voiceHandleListCalls(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestVoiceHandleListCalls_Empty(t *testing.T) {
	mock := newMockVoiceClient()
	vc, _ := voiceControllerWith(mock)
	req := httptest.NewRequest(http.MethodGet, "/api/telephony/voice/calls?modem_path=/m", nil)
	rr := httptest.NewRecorder()
	vc.voiceHandleListCalls(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var calls []VoiceCall
	if err := json.Unmarshal(rr.Body.Bytes(), &calls); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("expected 0 calls, got %v", calls)
	}
}

func TestVoiceHandleListCalls_DBusError(t *testing.T) {
	mock := newMockVoiceClient()
	mock.listErr = voiceErrUnavailable()
	vc, _ := voiceControllerWith(mock)
	req := httptest.NewRequest(http.MethodGet, "/api/telephony/voice/calls?modem_path=/m", nil)
	rr := httptest.NewRecorder()
	vc.voiceHandleListCalls(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}

// ─── RegisterVoiceHandlers ────────────────────────────────────────────────────

func TestVoiceRegisterHandlers(t *testing.T) {
	mock := newMockVoiceClient()
	vc, _ := voiceControllerWith(mock)
	mux := http.NewServeMux()
	RegisterVoiceHandlers(mux, vc)

	endpoints := []string{
		"/api/telephony/voice/dial",
		"/api/telephony/voice/answer",
		"/api/telephony/voice/hangup",
		"/api/telephony/voice/dtmf",
		"/api/telephony/voice/calls",
	}
	for _, ep := range endpoints {
		req := httptest.NewRequest(http.MethodGet, ep, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code == http.StatusNotFound {
			t.Errorf("endpoint %s returned 404 — not registered", ep)
		}
	}
}

// ─── WS broadcast integration ─────────────────────────────────────────────────

func TestVoiceDialBroadcastsCallState(t *testing.T) {
	mock := newMockVoiceClient()
	vc, broadcasts := voiceControllerWith(mock)

	body := `{"modem_path":"/m","number":"+999"}`
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/dial", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	vc.voiceHandleDial(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("dial failed: %s", rr.Body.String())
	}
	if len(*broadcasts) == 0 {
		t.Fatal("no broadcast after dial")
	}
	var ev voiceCallStateEvent
	if err := json.Unmarshal((*broadcasts)[0], &ev); err != nil {
		t.Fatalf("decode broadcast: %v", err)
	}
	if ev.Type != "call_state" {
		t.Errorf("event type = %q, want 'call_state'", ev.Type)
	}
	if len(ev.Calls) == 0 {
		t.Error("expected at least one call in broadcast")
	}
}

func TestVoiceAnswerBroadcastsCallState(t *testing.T) {
	mock := newMockVoiceClient()
	mock.calls = []VoiceCall{{Path: "/c/0", Number: "+1", State: "ringing", Direction: "incoming"}}
	vc, broadcasts := voiceControllerWith(mock)

	body := `{"modem_path":"/m","call_path":"/c/0"}`
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/answer", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	vc.voiceHandleAnswer(rr, req)

	if len(*broadcasts) == 0 {
		t.Fatal("no broadcast after answer")
	}
	var ev voiceCallStateEvent
	if err := json.Unmarshal((*broadcasts)[0], &ev); err != nil {
		t.Fatalf("decode broadcast: %v", err)
	}
	if ev.Type != "call_state" {
		t.Errorf("event type = %q", ev.Type)
	}
}

func TestVoiceHangupBroadcastsCallState(t *testing.T) {
	mock := newMockVoiceClient()
	mock.calls = []VoiceCall{{Path: "/c/0", Number: "+1", State: "active"}}
	vc, broadcasts := voiceControllerWith(mock)

	body := `{"modem_path":"/m","call_path":"/c/0"}`
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/hangup", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	vc.voiceHandleHangup(rr, req)

	if len(*broadcasts) == 0 {
		t.Fatal("no broadcast after hangup")
	}
}

// ─── VoicePoller ─────────────────────────────────────────────────────────────

func TestVoiceStartPollerBroadcastsOnTick(t *testing.T) {
	mock := newMockVoiceClient()
	mock.calls = []VoiceCall{{Path: "/c/0", Number: "+1", State: "active"}}
	vc, broadcasts := voiceControllerWith(mock)

	stopCh := make(chan struct{})
	vc.StartVoicePoller("/m", 20*time.Millisecond, stopCh)

	// Wait for at least one tick.
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case <-deadline:
			t.Fatal("poller never broadcast within timeout")
		default:
			if len(*broadcasts) > 0 {
				goto done
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
done:
	close(stopCh)

	var ev voiceCallStateEvent
	if err := json.Unmarshal((*broadcasts)[0], &ev); err != nil {
		t.Fatalf("decode broadcast: %v", err)
	}
	if ev.Type != "call_state" {
		t.Errorf("type = %q", ev.Type)
	}
}

func TestVoiceStartPollerStopsOnClose(t *testing.T) {
	mock := newMockVoiceClient()
	vc, broadcasts := voiceControllerWith(mock)

	stopCh := make(chan struct{})
	vc.StartVoicePoller("/m", 10*time.Millisecond, stopCh)
	close(stopCh)

	// Wait a bit and record how many broadcasts happened.
	time.Sleep(50 * time.Millisecond)
	before := len(*broadcasts)
	time.Sleep(50 * time.Millisecond)
	after := len(*broadcasts)

	if after != before {
		t.Errorf("poller kept broadcasting after stop: before=%d after=%d", before, after)
	}
}

// ─── SetVoiceBroadcastFn ──────────────────────────────────────────────────────

func TestVoiceSetBroadcastFn(t *testing.T) {
	mock := newMockVoiceClient()
	vc := voiceNewControllerWith(mock, nil)

	var called bool
	vc.SetVoiceBroadcastFn(func(data []byte) { called = true })

	body := `{"modem_path":"/m","number":"+1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/dial", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	vc.voiceHandleDial(rr, req)

	if !called {
		t.Error("broadcast fn not called after dial")
	}
}

// ─── NewVoiceController (production constructor) ──────────────────────────────

func TestVoiceNewVoiceController_NoHW(t *testing.T) {
	// Should not panic even when ModemManager is absent.
	vc := NewVoiceController()
	if vc == nil {
		t.Fatal("NewVoiceController returned nil")
	}
	if vc.dbus == nil {
		t.Fatal("dbus client is nil")
	}
	// Production client unavailable in CI (no D-Bus) — that's fine.
	// Endpoints return 503, never panic.
	mux := http.NewServeMux()
	RegisterVoiceHandlers(mux, vc)

	body := `{"modem_path":"/m","number":"+1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/voice/dial", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK && rr.Code != http.StatusServiceUnavailable {
		t.Errorf("unexpected status %d", rr.Code)
	}
}

// ─── voicePushCallState with nil broadcastFn (no panic) ──────────────────────

func TestVoicePushCallState_NilBroadcast(t *testing.T) {
	mock := newMockVoiceClient()
	vc := voiceNewControllerWith(mock, nil)
	// Should not panic.
	vc.voicePushCallState("/m")
}

func TestVoicePushCallState_DBusError(t *testing.T) {
	mock := newMockVoiceClient()
	mock.listErr = voiceErrUnavailable()
	var called bool
	vc := voiceNewControllerWith(mock, func(data []byte) { called = true })
	vc.voicePushCallState("/m")
	// broadcastFn still called with empty list even when D-Bus fails.
	if !called {
		t.Error("broadcastFn not called despite listErr")
	}
}
