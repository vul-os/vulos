// Voice call control for ModemManager via D-Bus.
//
// This file implements dial/answer/hangup/DTMF operations using the
// org.freedesktop.ModemManager1.Modem.Voice interface and a background
// listener that polls call state and pushes events to WebSocket clients.
//
// All D-Bus access goes through the gdbus CLI (same pattern as modemmanager.go)
// so there is no cgo dependency and the code builds/tests on any host.
//
// Audio path is intentionally excluded: routing modem audio into PipeWire
// is a separate concern (see MOBILE.md § Call Audio Streaming).
package telephony

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"vulos/backend/internal/wsutil"
)

// ----------------------------------------------------------------------------
// D-Bus constants for org.freedesktop.ModemManager1.Modem.Voice
// ----------------------------------------------------------------------------

const (
	voiceIface = "org.freedesktop.ModemManager1.Modem.Voice"

	// MMCallState values (MM_CALL_STATE_*)
	callStateUnknown    = 0
	callStateDialing    = 1
	callStateRinging    = 2
	callStateActive     = 3
	callStateHeld       = 4
	callStateWaiting    = 5
	callStateTerminated = 6

	// MMCallDirection values
	callDirectionUnknown  = 0
	callDirectionIncoming = 1
	callDirectionOutgoing = 2
)

// ----------------------------------------------------------------------------
// VoiceCall — the data model for a single call
// ----------------------------------------------------------------------------

// VoiceCall represents one call object returned by ModemManager.
type VoiceCall struct {
	// Path is the D-Bus object path, e.g. /org/freedesktop/ModemManager1/Call/0.
	Path string `json:"path"`

	// State is a human-readable call state string (see voiceCallStateString).
	State string `json:"state"`

	// Direction is "incoming" or "outgoing".
	Direction string `json:"direction"`

	// Number is the remote party phone number.
	Number string `json:"number"`
}

// ----------------------------------------------------------------------------
// VoiceDBusClient — mockable interface for the D-Bus Voice operations
// ----------------------------------------------------------------------------

// VoiceDBusClient abstracts the D-Bus Voice interface so tests can inject a
// fake implementation without requiring ModemManager or gdbus to be present.
type VoiceDBusClient interface {
	// Available reports whether the D-Bus Voice interface can be reached.
	Available() bool

	// ListCalls returns all call objects currently tracked by ModemManager.
	ListCalls(modemPath string) ([]VoiceCall, error)

	// Dial creates an outgoing call to number on the given modem. Returns the
	// new call object path on success.
	Dial(modemPath, number string) (string, error)

	// Accept answers an incoming call.
	Accept(callPath string) error

	// Hangup terminates a call.
	Hangup(callPath string) error

	// SendDTMF sends one or more DTMF tones on an active call.
	SendDTMF(callPath, tones string) error
}

// ----------------------------------------------------------------------------
// gdbusVoiceClient — production VoiceDBusClient backed by the gdbus CLI
// ----------------------------------------------------------------------------

// gdbusVoiceClient implements VoiceDBusClient via the gdbus CLI tool.
// It requires gdbus and ModemManager to be present; otherwise Available()
// returns false and every method returns an error without panicking.
type gdbusVoiceClient struct {
	avail bool
}

// newGdbusVoiceClient probes whether gdbus and ModemManager are reachable
// and returns a client. The returned value is always non-nil.
func newGdbusVoiceClient() *gdbusVoiceClient {
	c := &gdbusVoiceClient{}
	if _, err := exec.LookPath("gdbus"); err != nil {
		log.Printf("[telephony/voice] gdbus not found — voice calls disabled")
		return c
	}
	// Reuse the existing MM probe: if mmClient.available we trust the bus is up.
	probe := newMMClient()
	c.avail = probe.available
	if !c.avail {
		log.Printf("[telephony/voice] ModemManager unreachable — voice calls disabled")
	}
	return c
}

// Available reports whether the D-Bus Voice interface can be reached.
func (g *gdbusVoiceClient) Available() bool { return g.avail }

// ListCalls returns all call objects tracked by ModemManager for modemPath.
func (g *gdbusVoiceClient) ListCalls(modemPath string) ([]VoiceCall, error) {
	if !g.avail {
		return []VoiceCall{}, nil
	}

	// org.freedesktop.ModemManager1.Modem.Voice.ListCalls() → array of object paths
	out, err := gdbusCall(mmService, modemPath, voiceIface, "ListCalls")
	if err != nil {
		return nil, fmt.Errorf("ListCalls on %s: %w", modemPath, err)
	}

	paths := parseCallPaths(out)
	calls := make([]VoiceCall, 0, len(paths))
	for _, p := range paths {
		call, err := g.fetchCall(p)
		if err != nil {
			log.Printf("[telephony/voice] fetchCall %s: %v", p, err)
			continue
		}
		calls = append(calls, call)
	}
	return calls, nil
}

// Dial creates an outgoing call on modemPath to number.
// It calls org.freedesktop.ModemManager1.Modem.Voice.CreateCall then
// org.freedesktop.ModemManager1.Call.Start.
func (g *gdbusVoiceClient) Dial(modemPath, number string) (string, error) {
	if !g.avail {
		return "", fmt.Errorf("ModemManager not available")
	}

	// GLib variant dict: {'number': <'NUMBER'>}
	arg := fmt.Sprintf("{'number': <%q>}", number)
	out, err := gdbusCallWithArg(mmService, modemPath, voiceIface, "CreateCall", arg)
	if err != nil {
		return "", fmt.Errorf("CreateCall: %w", err)
	}

	callPath := extractObjectPath(out)
	if callPath == "" {
		return "", fmt.Errorf("CreateCall returned no call path")
	}

	if _, err := gdbusCall(mmService, callPath, "org.freedesktop.ModemManager1.Call", "Start"); err != nil {
		return "", fmt.Errorf("Start call %s: %w", callPath, err)
	}

	log.Printf("[telephony/voice] dialing %s via %s", number, callPath)
	return callPath, nil
}

// Accept answers an incoming call identified by callPath.
func (g *gdbusVoiceClient) Accept(callPath string) error {
	if !g.avail {
		return fmt.Errorf("ModemManager not available")
	}
	_, err := gdbusCall(mmService, callPath, "org.freedesktop.ModemManager1.Call", "Accept")
	if err != nil {
		return fmt.Errorf("Accept %s: %w", callPath, err)
	}
	log.Printf("[telephony/voice] accepted call %s", callPath)
	return nil
}

// Hangup terminates the call at callPath.
func (g *gdbusVoiceClient) Hangup(callPath string) error {
	if !g.avail {
		return fmt.Errorf("ModemManager not available")
	}
	_, err := gdbusCall(mmService, callPath, "org.freedesktop.ModemManager1.Call", "Hangup")
	if err != nil {
		return fmt.Errorf("Hangup %s: %w", callPath, err)
	}
	log.Printf("[telephony/voice] hung up %s", callPath)
	return nil
}

// SendDTMF sends tones on the active call at callPath.
func (g *gdbusVoiceClient) SendDTMF(callPath, tones string) error {
	if !g.avail {
		return fmt.Errorf("ModemManager not available")
	}
	_, err := gdbusCallWithArg(mmService, callPath,
		"org.freedesktop.ModemManager1.Call", "SendDtmf", fmt.Sprintf("%q", tones))
	if err != nil {
		return fmt.Errorf("SendDtmf %s tones=%q: %w", callPath, tones, err)
	}
	log.Printf("[telephony/voice] DTMF %q on %s", tones, callPath)
	return nil
}

// fetchCall reads properties for a single call object path.
func (g *gdbusVoiceClient) fetchCall(callPath string) (VoiceCall, error) {
	c := VoiceCall{Path: callPath}
	out, err := gdbusGetAll(mmService, callPath, "org.freedesktop.ModemManager1.Call")
	if err != nil {
		return c, err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "'Number':"):
			c.Number = extractStringVal(line)
		case strings.HasPrefix(line, "'State':"):
			c.State = voiceCallStateString(extractIntVal(line))
		case strings.HasPrefix(line, "'Direction':"):
			c.Direction = callDirectionString(extractIntVal(line))
		}
	}
	return c, nil
}

// ----------------------------------------------------------------------------
// VoiceHub — WebSocket broadcast hub for call-state events
// ----------------------------------------------------------------------------

// voiceWSConn is the minimal interface that VoiceHub needs from a gorilla
// WebSocket connection. Extracted so tests can inject a fake.
type voiceWSConn interface {
	WriteMessage(messageType int, data []byte) error
	ReadMessage() (messageType int, p []byte, err error)
	Close() error
}

// VoiceHub broadcasts call-state JSON events to all subscribed WS clients.
// It is separate from the telephony.Service modem hub so voice subscribers
// can connect independently (e.g. a dedicated dialler tab).
type VoiceHub struct {
	mu      sync.RWMutex
	clients map[voiceWSConn]bool
}

// newVoiceHub creates an empty VoiceHub.
func newVoiceHub() *VoiceHub {
	return &VoiceHub{clients: make(map[voiceWSConn]bool)}
}

// add registers a WebSocket connection as a subscriber.
func (h *VoiceHub) add(c voiceWSConn) {
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
}

// remove deregisters a connection.
func (h *VoiceHub) remove(c voiceWSConn) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// push broadcasts data to all subscribed clients, pruning dead connections.
func (h *VoiceHub) push(data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
			c.Close()
			delete(h.clients, c)
		}
	}
}

// clientCount returns the number of connected clients.
func (h *VoiceHub) clientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// ----------------------------------------------------------------------------
// VoiceController — top-level orchestrator
// ----------------------------------------------------------------------------

// VoiceController wires together the D-Bus voice client, the WS hub, and the
// HTTP handlers. RegisterVoiceHandlers is the public entry point.
type VoiceController struct {
	db        VoiceDBusClient
	hub       *VoiceHub
	modemPath string // D-Bus path of the modem to use, e.g. .../Modem/0
}

// NewVoiceController creates a VoiceController with the production gdbus
// backend. modemPath is the D-Bus object path of the modem to use (may be
// empty when no modem is present — operations will return an error).
func NewVoiceController(modemPath string) *VoiceController {
	return &VoiceController{
		db:        newGdbusVoiceClient(),
		hub:       newVoiceHub(),
		modemPath: modemPath,
	}
}

// newVoiceControllerWith creates a VoiceController with an injectable
// VoiceDBusClient. Used exclusively by tests.
func newVoiceControllerWith(db VoiceDBusClient, modemPath string) *VoiceController {
	return &VoiceController{
		db:        db,
		hub:       newVoiceHub(),
		modemPath: modemPath,
	}
}

// RegisterVoiceHandlers mounts voice endpoints onto mux and is the sole
// integration point for MOBILE-03. Callers must NOT edit main.go.
//
// Endpoints:
//
//	POST /api/telephony/voice/dial    — {"number":"<e164>"}
//	POST /api/telephony/voice/answer  — {"call_path":"<path>"}
//	POST /api/telephony/voice/hangup  — {"call_path":"<path>"}
//	POST /api/telephony/voice/dtmf    — {"call_path":"<path>","tones":"<digits>"}
//	GET  /api/telephony/voice/calls   — list active calls (JSON)
//	GET  /api/telephony/voice/ws      — WebSocket; pushes "call_state" events
func RegisterVoiceHandlers(mux *http.ServeMux, vc *VoiceController) {
	mux.HandleFunc("/api/telephony/voice/dial", vc.handleDial)
	mux.HandleFunc("/api/telephony/voice/answer", vc.handleAnswer)
	mux.HandleFunc("/api/telephony/voice/hangup", vc.handleHangup)
	mux.HandleFunc("/api/telephony/voice/dtmf", vc.handleDTMF)
	mux.HandleFunc("/api/telephony/voice/calls", vc.handleListCalls)
	mux.HandleFunc("/api/telephony/voice/ws", vc.handleVoiceWS)
	log.Printf("[telephony/voice] registered voice endpoints")
}

// StartVoicePoller launches a goroutine that polls call state every interval
// and pushes call_state events to all connected WebSocket clients.
// Send to stop (or close it) to terminate the loop.
func StartVoicePoller(vc *VoiceController, interval time.Duration, stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				vc.broadcastCallState()
			}
		}
	}()
}

// ----------------------------------------------------------------------------
// HTTP request/response types
// ----------------------------------------------------------------------------

// voiceDialRequest is the JSON body for POST /api/telephony/voice/dial.
type voiceDialRequest struct {
	Number string `json:"number"`
}

// voiceCallPathRequest is shared by answer / hangup / dtmf endpoints.
type voiceCallPathRequest struct {
	CallPath string `json:"call_path"`
	Tones    string `json:"tones,omitempty"` // only for DTMF
}

// voiceResponse is the standard JSON response body for voice control endpoints.
// Calls is never null in JSON — zero-length results use an empty array.
type voiceResponse struct {
	OK       bool        `json:"ok"`
	CallPath string      `json:"call_path,omitempty"`
	Calls    []VoiceCall `json:"calls"`
	Error    string      `json:"error,omitempty"`
}

// voiceEvent is the WebSocket message pushed on call-state changes.
type voiceEvent struct {
	Type  string      `json:"type"` // always "call_state"
	Calls []VoiceCall `json:"calls"`
}

// ----------------------------------------------------------------------------
// HTTP handlers
// ----------------------------------------------------------------------------

func (vc *VoiceController) handleDial(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonVoiceError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req voiceDialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Number == "" {
		jsonVoiceError(w, `body must be {"number":"<e164>"}`, http.StatusBadRequest)
		return
	}
	if vc.modemPath == "" {
		jsonVoiceError(w, "no modem available", http.StatusServiceUnavailable)
		return
	}
	callPath, err := vc.db.Dial(vc.modemPath, req.Number)
	if err != nil {
		log.Printf("[telephony/voice] dial: %v", err)
		jsonVoiceError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	vc.broadcastCallState()
	jsonVoiceOK(w, voiceResponse{OK: true, CallPath: callPath})
}

func (vc *VoiceController) handleAnswer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonVoiceError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req voiceCallPathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CallPath == "" {
		jsonVoiceError(w, `body must be {"call_path":"<path>"}`, http.StatusBadRequest)
		return
	}
	if err := vc.db.Accept(req.CallPath); err != nil {
		log.Printf("[telephony/voice] answer: %v", err)
		jsonVoiceError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	vc.broadcastCallState()
	jsonVoiceOK(w, voiceResponse{OK: true, CallPath: req.CallPath})
}

func (vc *VoiceController) handleHangup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonVoiceError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req voiceCallPathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CallPath == "" {
		jsonVoiceError(w, `body must be {"call_path":"<path>"}`, http.StatusBadRequest)
		return
	}
	if err := vc.db.Hangup(req.CallPath); err != nil {
		log.Printf("[telephony/voice] hangup: %v", err)
		jsonVoiceError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	vc.broadcastCallState()
	jsonVoiceOK(w, voiceResponse{OK: true, CallPath: req.CallPath})
}

func (vc *VoiceController) handleDTMF(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonVoiceError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req voiceCallPathRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CallPath == "" || req.Tones == "" {
		jsonVoiceError(w, `body must be {"call_path":"<path>","tones":"<digits>"}`, http.StatusBadRequest)
		return
	}
	if err := vc.db.SendDTMF(req.CallPath, req.Tones); err != nil {
		log.Printf("[telephony/voice] dtmf: %v", err)
		jsonVoiceError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonVoiceOK(w, voiceResponse{OK: true, CallPath: req.CallPath})
}

func (vc *VoiceController) handleListCalls(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonVoiceError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	calls := vc.currentCalls()
	jsonVoiceOK(w, voiceResponse{OK: true, Calls: calls})
}

// handleVoiceWS upgrades to WebSocket and subscribes the client to call-state
// push events. The client immediately receives the current call list.
func (vc *VoiceController) handleVoiceWS(w http.ResponseWriter, r *http.Request) {
	ws, err := wsutil.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[telephony/voice] ws upgrade: %v", err)
		return
	}
	defer ws.Close()

	// Send current state immediately on connect.
	vc.pushToConn(ws)

	vc.hub.add(ws)
	log.Printf("[telephony/voice] ws client connected (total %d)", vc.hub.clientCount())

	// Drain client messages (pings, close frames) until disconnect.
	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			break
		}
	}

	vc.hub.remove(ws)
	log.Printf("[telephony/voice] ws client disconnected (total %d)", vc.hub.clientCount())
}

// ----------------------------------------------------------------------------
// Internal helpers
// ----------------------------------------------------------------------------

// currentCalls returns the live call list; returns empty slice on any error.
func (vc *VoiceController) currentCalls() []VoiceCall {
	if vc.modemPath == "" || !vc.db.Available() {
		return []VoiceCall{}
	}
	calls, err := vc.db.ListCalls(vc.modemPath)
	if err != nil {
		log.Printf("[telephony/voice] ListCalls: %v", err)
		return []VoiceCall{}
	}
	return calls
}

// broadcastCallState fetches the current call list and pushes to all WS clients.
func (vc *VoiceController) broadcastCallState() {
	calls := vc.currentCalls()
	ev := voiceEvent{Type: "call_state", Calls: calls}
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	vc.hub.push(data)
}

// pushToConn sends the current call state to a single connection.
func (vc *VoiceController) pushToConn(c voiceWSConn) {
	calls := vc.currentCalls()
	ev := voiceEvent{Type: "call_state", Calls: calls}
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_ = c.WriteMessage(websocket.TextMessage, data)
}

func jsonVoiceOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonVoiceError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(voiceResponse{OK: false, Error: msg})
}

// ----------------------------------------------------------------------------
// D-Bus gdbus helpers (voice-specific; modemmanager.go provides the base ones)
// ----------------------------------------------------------------------------

// gdbusCallWithArg calls a D-Bus method passing a single GLib-variant argument.
func gdbusCallWithArg(service, path, iface, method, arg string) (string, error) {
	out, err := exec.Command("gdbus", "call",
		"--system",
		"--dest", service,
		"--object-path", path,
		"--method", iface+"."+method,
		arg,
	).Output()
	return string(out), err
}

// parseCallPaths extracts Call object paths from gdbus ListCalls output.
// Expected format: ([objectpath '/org/freedesktop/ModemManager1/Call/0', ...],)
// Multiple paths may appear on the same line, so we scan the full output for
// every occurrence of the Call prefix rather than splitting by line.
func parseCallPaths(out string) []string {
	const prefix = "/org/freedesktop/ModemManager1/Call/"
	var paths []string
	seen := map[string]bool{}

	remaining := out
	for {
		start := strings.Index(remaining, prefix)
		if start < 0 {
			break
		}
		rest := remaining[start:]

		// Advance past this occurrence for the next iteration.
		remaining = rest[len(prefix):]

		// Trim the path at the first delimiter character.
		end := len(rest)
		for _, ch := range []byte("'\",)] \n\t") {
			if idx := strings.IndexByte(rest, ch); idx > 0 && idx < end {
				end = idx
			}
		}
		path := rest[:end]

		// Validate: last segment must be a numeric modem/call index.
		parts := strings.Split(path, "/")
		if len(parts) == 0 {
			continue
		}
		if _, err := strconv.Atoi(parts[len(parts)-1]); err != nil {
			continue
		}
		if !seen[path] {
			paths = append(paths, path)
			seen[path] = true
		}
	}
	return paths
}

// extractObjectPath extracts the first ModemManager object path from gdbus output.
// Used to get the call path returned by CreateCall.
func extractObjectPath(out string) string {
	const marker = "/org/freedesktop/ModemManager1/"
	start := strings.Index(out, marker)
	if start < 0 {
		return ""
	}
	rest := out[start:]
	for _, ch := range []string{"'", "\"", ",", ")", " ", "\n"} {
		if idx := strings.Index(rest, ch); idx > 0 {
			rest = rest[:idx]
		}
	}
	return rest
}

// ----------------------------------------------------------------------------
// Enum conversions
// ----------------------------------------------------------------------------

// voiceCallStateString converts a MMCallState integer to a readable label.
func voiceCallStateString(state int) string {
	switch state {
	case callStateDialing:
		return "dialing"
	case callStateRinging:
		return "ringing"
	case callStateActive:
		return "active"
	case callStateHeld:
		return "held"
	case callStateWaiting:
		return "waiting"
	case callStateTerminated:
		return "terminated"
	default:
		return "unknown"
	}
}

// callDirectionString converts a MMCallDirection integer to a readable label.
func callDirectionString(dir int) string {
	switch dir {
	case callDirectionIncoming:
		return "incoming"
	case callDirectionOutgoing:
		return "outgoing"
	default:
		return "unknown"
	}
}
