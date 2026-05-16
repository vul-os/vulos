// Package telephony — D-Bus voice calls via ModemManager.
//
// Architecture
// ────────────
//
//	VoiceDBusClient (interface)
//	  ↓ real: voiceMmClient     (gdbus CLI, no cgo)
//	  ↓ mock: injected in tests
//
//	VoiceController
//	  - holds a VoiceDBusClient and a broadcastFn (WS push)
//	  - exposes HTTP handlers wired by RegisterVoiceHandlers
//	  - StartVoicePoller: background loop that polls call state and pushes
//	    "call_state" events to all connected WebSocket clients
//
// Endpoints (all prefixed /api/telephony/voice):
//
//	POST   /dial               body: {modem_path, number}
//	POST   /answer             body: {modem_path, call_path}
//	POST   /hangup             body: {modem_path, call_path}
//	POST   /dtmf               body: {modem_path, call_path, tones}
//	GET    /calls              ?modem_path=<path>
//
// Audio path is excluded — voice audio is handled by the existing
// WebRTC/PipeWire pipeline and is NOT part of this service.
package telephony

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ─── D-Bus constants ─────────────────────────────────────────────────────────

const voiceMMService = "org.freedesktop.ModemManager1"
const voiceVoiceIface = "org.freedesktop.ModemManager1.Modem.Voice"
const voiceCallIface = "org.freedesktop.ModemManager1.Call"

// ─── Call state enum ─────────────────────────────────────────────────────────

// VoiceCallState mirrors MMCallState from ModemManager's enum.
type VoiceCallState int

const (
	VoiceCallStateUnknown    VoiceCallState = 0
	VoiceCallStateDialing    VoiceCallState = 1
	VoiceCallStateRingback   VoiceCallState = 2
	VoiceCallStateRinging    VoiceCallState = 3
	VoiceCallStateActive     VoiceCallState = 4
	VoiceCallStateHeld       VoiceCallState = 5
	VoiceCallStateWaiting    VoiceCallState = 6
	VoiceCallStateTerminated VoiceCallState = 7
)

// voiceCallStateString converts a MMCallState integer to a human-readable label.
func voiceCallStateString(s VoiceCallState) string {
	switch s {
	case VoiceCallStateDialing:
		return "dialing"
	case VoiceCallStateRingback:
		return "ringback"
	case VoiceCallStateRinging:
		return "ringing"
	case VoiceCallStateActive:
		return "active"
	case VoiceCallStateHeld:
		return "held"
	case VoiceCallStateWaiting:
		return "waiting"
	case VoiceCallStateTerminated:
		return "terminated"
	default:
		return "unknown"
	}
}

// VoiceCallDirection mirrors MMCallDirection.
type VoiceCallDirection int

const (
	VoiceCallDirectionUnknown  VoiceCallDirection = 0
	VoiceCallDirectionIncoming VoiceCallDirection = 1
	VoiceCallDirectionOutgoing VoiceCallDirection = 2
)

// voiceCallDirectionString converts a direction int to "incoming"/"outgoing"/"unknown".
func voiceCallDirectionString(d VoiceCallDirection) string {
	switch d {
	case VoiceCallDirectionIncoming:
		return "incoming"
	case VoiceCallDirectionOutgoing:
		return "outgoing"
	default:
		return "unknown"
	}
}

// ─── VoiceCall ───────────────────────────────────────────────────────────────

// VoiceCall describes a single call object returned by the API.
type VoiceCall struct {
	Path      string `json:"path"`
	Number    string `json:"number"`
	State     string `json:"state"`
	Direction string `json:"direction"`
}

// ─── D-Bus abstraction (mockable) ────────────────────────────────────────────

// VoiceDBusClient is the testable boundary around ModemManager Voice D-Bus
// calls. The real implementation shells out to gdbus; tests inject a mock.
type VoiceDBusClient interface {
	// VoiceDial initiates an outgoing call and returns the new call's D-Bus path.
	VoiceDial(modemPath, number string) (string, error)

	// VoiceAnswer accepts an incoming call.
	VoiceAnswer(modemPath, callPath string) error

	// VoiceHangup terminates an active or incoming call.
	VoiceHangup(modemPath, callPath string) error

	// VoiceSendDTMF sends DTMF tones on an active call.
	VoiceSendDTMF(modemPath, callPath, tones string) error

	// VoiceListCalls returns all call objects held by the modem.
	VoiceListCalls(modemPath string) ([]VoiceCall, error)

	// VoiceAvailable reports whether the D-Bus + ModemManager stack is reachable.
	VoiceAvailable() bool
}

// ─── Real gdbus implementation ───────────────────────────────────────────────

// voiceMmClient implements VoiceDBusClient via gdbus CLI (no cgo required).
type voiceMmClient struct {
	avail bool
}

// voiceNewMmClient probes gdbus availability and returns a real client.
func voiceNewMmClient() *voiceMmClient {
	c := &voiceMmClient{}
	out, err := exec.Command("gdbus",
		"call", "--system",
		"--dest", voiceMMService,
		"--object-path", "/org/freedesktop/ModemManager1",
		"--method", "org.freedesktop.DBus.Peer.Ping",
	).CombinedOutput()
	if err == nil && !strings.Contains(string(out), "Error") {
		c.avail = true
	}
	return c
}

func (c *voiceMmClient) VoiceAvailable() bool { return c.avail }

// VoiceDial creates a call via org.freedesktop.ModemManager1.Modem.Voice.CreateCall
// and immediately dials it.
func (c *voiceMmClient) VoiceDial(modemPath, number string) (string, error) {
	if !c.avail {
		return "", voiceErrUnavailable()
	}
	// CreateCall returns an object path for the new call.
	out, err := exec.Command("gdbus", "call", "--system",
		"--dest", voiceMMService,
		"--object-path", modemPath,
		"--method", voiceVoiceIface+".CreateCall",
		fmt.Sprintf("{'number': <%q>}", number),
	).Output()
	if err != nil {
		return "", fmt.Errorf("voice: CreateCall: %w", err)
	}
	callPath := voiceExtractObjectPath(string(out))
	if callPath == "" {
		return "", fmt.Errorf("voice: CreateCall returned no path: %s", out)
	}

	// Start the call.
	_, err = exec.Command("gdbus", "call", "--system",
		"--dest", voiceMMService,
		"--object-path", callPath,
		"--method", voiceCallIface+".Start",
	).Output()
	if err != nil {
		return callPath, fmt.Errorf("voice: Call.Start: %w", err)
	}
	return callPath, nil
}

// VoiceAnswer accepts an incoming call via org.freedesktop.ModemManager1.Call.Accept.
func (c *voiceMmClient) VoiceAnswer(modemPath, callPath string) error {
	if !c.avail {
		return voiceErrUnavailable()
	}
	_, err := exec.Command("gdbus", "call", "--system",
		"--dest", voiceMMService,
		"--object-path", callPath,
		"--method", voiceCallIface+".Accept",
	).Output()
	if err != nil {
		return fmt.Errorf("voice: Call.Accept: %w", err)
	}
	return nil
}

// VoiceHangup terminates a call via org.freedesktop.ModemManager1.Call.Hangup.
func (c *voiceMmClient) VoiceHangup(modemPath, callPath string) error {
	if !c.avail {
		return voiceErrUnavailable()
	}
	_, err := exec.Command("gdbus", "call", "--system",
		"--dest", voiceMMService,
		"--object-path", callPath,
		"--method", voiceCallIface+".Hangup",
	).Output()
	if err != nil {
		return fmt.Errorf("voice: Call.Hangup: %w", err)
	}
	return nil
}

// VoiceSendDTMF sends DTMF tones via org.freedesktop.ModemManager1.Call.SendDtmf.
func (c *voiceMmClient) VoiceSendDTMF(modemPath, callPath, tones string) error {
	if !c.avail {
		return voiceErrUnavailable()
	}
	_, err := exec.Command("gdbus", "call", "--system",
		"--dest", voiceMMService,
		"--object-path", callPath,
		"--method", voiceCallIface+".SendDtmf",
		fmt.Sprintf("'%s'", tones),
	).Output()
	if err != nil {
		return fmt.Errorf("voice: Call.SendDtmf: %w", err)
	}
	return nil
}

// VoiceListCalls enumerates call objects via
// org.freedesktop.ModemManager1.Modem.Voice.ListCalls and fetches properties
// for each.
func (c *voiceMmClient) VoiceListCalls(modemPath string) ([]VoiceCall, error) {
	if !c.avail {
		return nil, voiceErrUnavailable()
	}
	out, err := exec.Command("gdbus", "call", "--system",
		"--dest", voiceMMService,
		"--object-path", modemPath,
		"--method", voiceVoiceIface+".ListCalls",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("voice: ListCalls: %w", err)
	}
	paths := voiceParseObjectPaths(string(out))
	calls := make([]VoiceCall, 0, len(paths))
	for _, p := range paths {
		props, err := voiceFetchCallProps(p)
		if err != nil {
			log.Printf("[voice] fetchCallProps %s: %v", p, err)
			continue
		}
		calls = append(calls, props)
	}
	return calls, nil
}

// voiceFetchCallProps reads the Number, State, and Direction properties of a
// call object via gdbus introspection of its interface properties.
func voiceFetchCallProps(callPath string) (VoiceCall, error) {
	out, err := exec.Command("gdbus", "introspect", "--system",
		"--dest", voiceMMService,
		"--object-path", callPath,
		"--only-properties",
	).Output()
	if err != nil {
		// Fall back to an empty record rather than failing the whole list.
		return VoiceCall{Path: callPath}, nil
	}
	return voiceParseCallProps(callPath, string(out)), nil
}

// voiceParseCallProps parses the text output of gdbus introspect --only-properties
// for a Call object into a VoiceCall.
func voiceParseCallProps(callPath, out string) VoiceCall {
	vc := VoiceCall{Path: callPath}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Number") {
			// e.g.  Number = '+44123456789';
			vc.Number = voiceExtractStringProp(line)
		} else if strings.HasPrefix(line, "State") {
			// e.g.  State = 4;
			state := VoiceCallState(voiceExtractIntProp(line))
			vc.State = voiceCallStateString(state)
		} else if strings.HasPrefix(line, "Direction") {
			dir := VoiceCallDirection(voiceExtractIntProp(line))
			vc.Direction = voiceCallDirectionString(dir)
		}
	}
	if vc.State == "" {
		vc.State = voiceCallStateString(VoiceCallStateUnknown)
	}
	if vc.Direction == "" {
		vc.Direction = voiceCallDirectionString(VoiceCallDirectionUnknown)
	}
	return vc
}

// ─── Parsing helpers (voice-prefixed; no clash with sms.go / modemmanager.go) ──

// voiceExtractObjectPath parses the first D-Bus object path from gdbus output.
// gdbus call output looks like: (objectclass '/org/freedesktop/ModemManager1/Call/0',)
func voiceExtractObjectPath(out string) string {
	for _, line := range strings.Split(out, "\n") {
		s := strings.TrimSpace(line)
		idx := strings.Index(s, "'/org/freedesktop/")
		if idx < 0 {
			idx = strings.Index(s, "\"/org/freedesktop/")
		}
		if idx >= 0 {
			rest := s[idx+1:]
			end := strings.IndexAny(rest, "'\",)")
			if end > 0 {
				return rest[:end]
			}
		}
	}
	return ""
}

// voiceParseObjectPaths extracts all D-Bus object paths from gdbus ListCalls output.
// The output is a GVariant tuple containing an array of object paths, e.g.:
//
//	([objectpath '/org/freedesktop/ModemManager1/Call/0', ...],)
func voiceParseObjectPaths(out string) []string {
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		for {
			idx := strings.Index(line, "'/org/freedesktop/")
			if idx < 0 {
				idx = strings.Index(line, "\"/org/freedesktop/")
			}
			if idx < 0 {
				break
			}
			rest := line[idx+1:]
			end := strings.IndexAny(rest, "'\",)")
			if end <= 0 {
				break
			}
			paths = append(paths, rest[:end])
			line = rest[end:]
		}
	}
	return paths
}

// voiceExtractStringProp extracts the value from a gdbus property line like:
//
//	Number = '+44123456789';
func voiceExtractStringProp(line string) string {
	eq := strings.Index(line, "=")
	if eq < 0 {
		return ""
	}
	val := strings.TrimSpace(line[eq+1:])
	val = strings.Trim(val, "';\"")
	return val
}

// voiceExtractIntProp extracts an integer from a gdbus property line like:
//
//	State = 4;
func voiceExtractIntProp(line string) int {
	eq := strings.Index(line, "=")
	if eq < 0 {
		return 0
	}
	val := strings.TrimSpace(line[eq+1:])
	val = strings.TrimRight(val, ";")
	var n int
	fmt.Sscanf(val, "%d", &n)
	return n
}

// voiceErrUnavailable returns the standard error for when ModemManager is absent.
func voiceErrUnavailable() error {
	return fmt.Errorf("voice: ModemManager D-Bus not available")
}

// ─── Request/response types ───────────────────────────────────────────────────

// voiceDialRequest is the JSON body for POST /api/telephony/voice/dial.
type voiceDialRequest struct {
	ModemPath string `json:"modem_path"`
	Number    string `json:"number"`
}

// voiceCallPathRequest is the JSON body for answer/hangup.
type voiceCallPathRequest struct {
	ModemPath string `json:"modem_path"`
	CallPath  string `json:"call_path"`
}

// voiceDTMFRequest is the JSON body for POST /api/telephony/voice/dtmf.
type voiceDTMFRequest struct {
	ModemPath string `json:"modem_path"`
	CallPath  string `json:"call_path"`
	Tones     string `json:"tones"`
}

// voiceOKResponse is the standard success response body.
type voiceOKResponse struct {
	OK       bool   `json:"ok"`
	CallPath string `json:"call_path,omitempty"`
}

// voiceCallStateEvent is the WS push payload for call_state events.
type voiceCallStateEvent struct {
	Type  string      `json:"type"`
	Calls []VoiceCall `json:"calls"`
}

// ─── VoiceController ─────────────────────────────────────────────────────────

// VoiceController wires HTTP handlers to the VoiceDBusClient and manages
// broadcast of call-state events over the shared WebSocket hub.
type VoiceController struct {
	dbus        VoiceDBusClient
	broadcastFn func([]byte) // pushes a JSON message to all WS clients
	mu          sync.Mutex
	lastCalls   []VoiceCall // cached for diff-based push
}

// voiceNewController creates a VoiceController using the real ModemManager client.
func voiceNewController() *VoiceController {
	return voiceNewControllerWith(voiceNewMmClient(), nil)
}

// voiceNewControllerWith creates a VoiceController with an injected client and
// broadcastFn; intended for testing and orchestrator wiring.
func voiceNewControllerWith(dbus VoiceDBusClient, broadcastFn func([]byte)) *VoiceController {
	return &VoiceController{
		dbus:        dbus,
		broadcastFn: broadcastFn,
		lastCalls:   []VoiceCall{},
	}
}

// NewVoiceController creates a production VoiceController using the real gdbus
// ModemManager client. The broadcastFn is set after construction by the
// orchestrator (typically s.broadcast from telephony.Service).
func NewVoiceController() *VoiceController {
	return voiceNewController()
}

// SetVoiceBroadcastFn sets the broadcast function used to push WS events.
// Call this before StartVoicePoller.
func (vc *VoiceController) SetVoiceBroadcastFn(fn func([]byte)) {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	vc.broadcastFn = fn
}

// RegisterVoiceHandlers mounts the voice endpoints onto mux.
// This is the exported orchestrator entry point.
func RegisterVoiceHandlers(mux *http.ServeMux, vc *VoiceController) {
	mux.HandleFunc("/api/telephony/voice/dial", vc.voiceHandleDial)
	mux.HandleFunc("/api/telephony/voice/answer", vc.voiceHandleAnswer)
	mux.HandleFunc("/api/telephony/voice/hangup", vc.voiceHandleHangup)
	mux.HandleFunc("/api/telephony/voice/dtmf", vc.voiceHandleDTMF)
	mux.HandleFunc("/api/telephony/voice/calls", vc.voiceHandleListCalls)
	log.Printf("[voice] registered /api/telephony/voice/{dial,answer,hangup,dtmf,calls}")
}

// ─── HTTP handlers ────────────────────────────────────────────────────────────

// voiceHandleDial handles POST /api/telephony/voice/dial.
func (vc *VoiceController) voiceHandleDial(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req voiceDialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		voiceJSONError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.ModemPath == "" || req.Number == "" {
		voiceJSONError(w, "modem_path and number are required", http.StatusBadRequest)
		return
	}

	callPath, err := vc.dbus.VoiceDial(req.ModemPath, req.Number)
	if err != nil {
		log.Printf("[voice] dial %s: %v", req.Number, err)
		voiceJSONError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Push an immediate call_state update so the WS clients see the dialing state.
	vc.voicePushCallState(req.ModemPath)

	voiceJSONOK(w, voiceOKResponse{OK: true, CallPath: callPath})
}

// voiceHandleAnswer handles POST /api/telephony/voice/answer.
func (vc *VoiceController) voiceHandleAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req voiceCallPathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		voiceJSONError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.ModemPath == "" || req.CallPath == "" {
		voiceJSONError(w, "modem_path and call_path are required", http.StatusBadRequest)
		return
	}

	if err := vc.dbus.VoiceAnswer(req.ModemPath, req.CallPath); err != nil {
		log.Printf("[voice] answer %s: %v", req.CallPath, err)
		voiceJSONError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	vc.voicePushCallState(req.ModemPath)
	voiceJSONOK(w, voiceOKResponse{OK: true})
}

// voiceHandleHangup handles POST /api/telephony/voice/hangup.
func (vc *VoiceController) voiceHandleHangup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req voiceCallPathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		voiceJSONError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.ModemPath == "" || req.CallPath == "" {
		voiceJSONError(w, "modem_path and call_path are required", http.StatusBadRequest)
		return
	}

	if err := vc.dbus.VoiceHangup(req.ModemPath, req.CallPath); err != nil {
		log.Printf("[voice] hangup %s: %v", req.CallPath, err)
		voiceJSONError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	vc.voicePushCallState(req.ModemPath)
	voiceJSONOK(w, voiceOKResponse{OK: true})
}

// voiceHandleDTMF handles POST /api/telephony/voice/dtmf.
func (vc *VoiceController) voiceHandleDTMF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req voiceDTMFRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		voiceJSONError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.ModemPath == "" || req.CallPath == "" || req.Tones == "" {
		voiceJSONError(w, "modem_path, call_path, and tones are required", http.StatusBadRequest)
		return
	}

	if err := vc.dbus.VoiceSendDTMF(req.ModemPath, req.CallPath, req.Tones); err != nil {
		log.Printf("[voice] dtmf %s %q: %v", req.CallPath, req.Tones, err)
		voiceJSONError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	voiceJSONOK(w, voiceOKResponse{OK: true})
}

// voiceHandleListCalls handles GET /api/telephony/voice/calls.
func (vc *VoiceController) voiceHandleListCalls(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	modemPath := r.URL.Query().Get("modem_path")
	if modemPath == "" {
		voiceJSONError(w, "modem_path query parameter is required", http.StatusBadRequest)
		return
	}

	calls, err := vc.dbus.VoiceListCalls(modemPath)
	if err != nil {
		log.Printf("[voice] list-calls: %v", err)
		voiceJSONError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if calls == nil {
		calls = []VoiceCall{}
	}
	voiceJSONOK(w, calls)
}

// ─── WS push ─────────────────────────────────────────────────────────────────

// voicePushCallState fetches current call list for modemPath and broadcasts
// a "call_state" event to all WebSocket clients. Errors are logged and ignored
// so that a missing modem never crashes the server.
func (vc *VoiceController) voicePushCallState(modemPath string) {
	calls, err := vc.dbus.VoiceListCalls(modemPath)
	if err != nil {
		// Not fatal; D-Bus may be absent in dev/CI.
		calls = []VoiceCall{}
	}
	vc.mu.Lock()
	vc.lastCalls = calls
	fn := vc.broadcastFn
	vc.mu.Unlock()
	if fn == nil {
		return
	}
	ev := voiceCallStateEvent{Type: "call_state", Calls: calls}
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	fn(data)
}

// ─── Background poller ────────────────────────────────────────────────────────

// StartVoicePoller starts a background goroutine that polls ModemManager for
// call-state updates and pushes "call_state" WebSocket events to all connected
// clients. It returns immediately; polling runs until stopCh is closed.
//
// modemPath is the D-Bus path of the modem to watch.
// pollInterval controls how often the modem is queried; 5 s is a reasonable default.
func (vc *VoiceController) StartVoicePoller(modemPath string, pollInterval time.Duration, stopCh <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				vc.voicePushCallState(modemPath)
			}
		}
	}()
}

// ─── JSON response helpers (voice-prefixed) ───────────────────────────────────

// voiceJSONOK writes a 200 JSON response.
func voiceJSONOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[voice] encode response: %v", err)
	}
}

// voiceJSONError writes a JSON error response with the given HTTP status code.
func voiceJSONError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
