// Package telephony — SMS send/receive via ModemManager D-Bus + HTTP API.
//
// Architecture
// ────────────
//
//	SMSMessaging (interface)
//	  ↓ real: mmSMSMessaging  (gdbus CLI)
//	  ↓ mock: MockSMSMessaging (injected in tests)
//
//	SMSService
//	  - holds an SMSStore (SQLite) and a broadcastFn (WS push)
//	  - exposes HTTP handlers registered by RegisterSMSHandlers
//	  - StartIncomingSMSListener polls/signals for inbound SMS
//
// Endpoints (all prefixed /api/telephony):
//
//	POST   /sms/send          body: {modem_path, to, body}
//	GET    /sms/list          ?peer=<number>&limit=<n>
//	DELETE /sms/delete        ?id=<int>
//	GET    /sms/threads       thread-grouped view
//	GET    /sms/search        ?q=<term>&limit=<n>
//
// Incoming SMS signal listener pushes a "sms_received" WebSocket event to
// every connected client and persists the message to the store.
package telephony

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ─── D-Bus abstraction ───────────────────────────────────────────────────────

// SMSMessaging is the testable boundary around ModemManager Messaging D-Bus
// calls. The real implementation shells out to gdbus; tests inject a mock.
type SMSMessaging interface {
	// SendSMS sends a message via the modem at modemPath and returns the new
	// D-Bus SMS object path on success.
	SendSMS(modemPath, number, text string) (string, error)

	// ListSMSPaths returns the D-Bus object paths of all SMS objects held by
	// ModemManager for the given modem.
	ListSMSPaths(modemPath string) ([]string, error)

	// GetSMS fetches the properties (Number, Text, Timestamp, State,
	// PDUType/Direction) of one SMS object.
	GetSMS(smsPath string) (SMSProperties, error)

	// DeleteSMS deletes the SMS object from the modem's storage.
	DeleteSMS(modemPath, smsPath string) error
}

// SMSProperties holds the D-Bus property values for a single SMS object.
type SMSProperties struct {
	Number    string // phone number (sender for received, recipient for sent)
	Text      string
	State     int    // MMSmsState enum value
	PDUType   int    // MMSmsPduType — 0=unknown,1=deliver(incoming),2=submit(outgoing)
	Timestamp string // ISO-8601 from ModemManager
}

// ─── Real gdbus implementation ───────────────────────────────────────────────

// mmSMSMessaging implements SMSMessaging via gdbus CLI (same approach as
// modemmanager.go to avoid cgo).
type mmSMSMessaging struct{}

const mmMessagingIface = "org.freedesktop.ModemManager1.Modem.Messaging"

func (m *mmSMSMessaging) SendSMS(modemPath, number, text string) (string, error) {
	// Create the SMS object
	out, err := exec.Command("gdbus", "call", "--system",
		"--dest", mmService,
		"--object-path", modemPath,
		"--method", mmMessagingIface+".Create",
		fmt.Sprintf("{'number': <%q>, 'text': <%q>}", number, text),
	).Output()
	if err != nil {
		return "", fmt.Errorf("sms: create: %w", err)
	}
	smsPath := extractObjectPath(string(out))
	if smsPath == "" {
		return "", fmt.Errorf("sms: create returned no path: %s", out)
	}

	// Send it
	_, err = exec.Command("gdbus", "call", "--system",
		"--dest", mmService,
		"--object-path", smsPath,
		"--method", "org.freedesktop.ModemManager1.Sms.Send",
	).Output()
	if err != nil {
		return smsPath, fmt.Errorf("sms: send: %w", err)
	}
	return smsPath, nil
}

func (m *mmSMSMessaging) ListSMSPaths(modemPath string) ([]string, error) {
	out, err := exec.Command("gdbus", "call", "--system",
		"--dest", mmService,
		"--object-path", modemPath,
		"--method", mmMessagingIface+".List",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("sms: list: %w", err)
	}
	return parseSMSPaths(string(out)), nil
}

func (m *mmSMSMessaging) GetSMS(smsPath string) (SMSProperties, error) {
	out, err := exec.Command("gdbus", "call", "--system",
		"--dest", mmService,
		"--object-path", smsPath,
		"--method", "org.freedesktop.DBus.Properties.GetAll",
		"org.freedesktop.ModemManager1.Sms",
	).Output()
	if err != nil {
		return SMSProperties{}, fmt.Errorf("sms: get %s: %w", smsPath, err)
	}
	return parseSMSProps(string(out)), nil
}

func (m *mmSMSMessaging) DeleteSMS(modemPath, smsPath string) error {
	_, err := exec.Command("gdbus", "call", "--system",
		"--dest", mmService,
		"--object-path", modemPath,
		"--method", mmMessagingIface+".Delete",
		fmt.Sprintf("'%s'", smsPath),
	).Output()
	return err
}

// ─── SMS Service ─────────────────────────────────────────────────────────────

// SMSService handles SMS send/receive/storage.
type SMSService struct {
	store       *SMSStore
	messaging   SMSMessaging
	broadcastFn func([]byte) // sends a JSON payload to all WS clients
}

// NewSMSService constructs an SMSService.
//   - store: open SMSStore (caller owns Close)
//   - messaging: real mmSMSMessaging or test mock
//   - broadcastFn: the Service.broadcast method (or nil in tests)
func NewSMSService(store *SMSStore, messaging SMSMessaging, broadcastFn func([]byte)) *SMSService {
	if broadcastFn == nil {
		broadcastFn = func([]byte) {}
	}
	return &SMSService{
		store:       store,
		messaging:   messaging,
		broadcastFn: broadcastFn,
	}
}

// RegisterSMSHandlers mounts the SMS REST endpoints onto mux.
// The orchestrator (main.go or test) calls this — no main.go edits needed.
func RegisterSMSHandlers(mux *http.ServeMux, svc *SMSService) {
	mux.HandleFunc("/api/telephony/sms/send", svc.handleSend)
	mux.HandleFunc("/api/telephony/sms/list", svc.handleList)
	mux.HandleFunc("/api/telephony/sms/delete", svc.handleDelete)
	mux.HandleFunc("/api/telephony/sms/threads", svc.handleThreads)
	mux.HandleFunc("/api/telephony/sms/search", svc.handleSearch)
	log.Printf("[telephony/sms] registered SMS endpoints")
}

// StartIncomingSMSListener polls ModemManager for new inbound SMS every
// pollInterval, persists them to the store, and pushes a WebSocket event.
// It runs until stopCh is closed.
func (s *SMSService) StartIncomingSMSListener(modemPath string, pollInterval time.Duration, stopCh <-chan struct{}) {
	if pollInterval <= 0 {
		pollInterval = 15 * time.Second
	}
	// Track D-Bus SMS paths we've already seen to avoid duplicate inserts.
	seen := make(map[string]bool)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			s.pollIncoming(modemPath, seen)
		}
	}
}

// pollIncoming fetches new inbound SMS objects from the modem and processes
// any paths not yet in the seen set.
func (s *SMSService) pollIncoming(modemPath string, seen map[string]bool) {
	paths, err := s.messaging.ListSMSPaths(modemPath)
	if err != nil {
		log.Printf("[telephony/sms] poll list error: %v", err)
		return
	}
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true

		props, err := s.messaging.GetSMS(p)
		if err != nil {
			log.Printf("[telephony/sms] get sms %s: %v", p, err)
			continue
		}
		// PDUType 1 = MM_SMS_PDU_TYPE_DELIVER (incoming)
		if props.PDUType != 1 {
			continue
		}
		s.persistAndPush(modemPath, props)
	}
}

// InjectIncomingSMS is the entry point for signal-based delivery (or tests):
// persist the message and push it over WebSocket.
func (s *SMSService) InjectIncomingSMS(modemPath string, props SMSProperties) {
	s.persistAndPush(modemPath, props)
}

func (s *SMSService) persistAndPush(modemPath string, props SMSProperties) {
	rec := SMSRecord{
		ModemPath:   modemPath,
		Peer:        props.Number,
		Direction:   "in",
		Body:        props.Text,
		State:       "received",
		TimestampMs: time.Now().UnixMilli(),
	}
	id, err := s.store.Insert(rec)
	if err != nil {
		log.Printf("[telephony/sms] store insert: %v", err)
		return
	}
	rec.ID = id
	s.pushEvent("sms_received", rec)
}

func (s *SMSService) pushEvent(evType string, data interface{}) {
	ev := Event{Type: evType, Data: data}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	s.broadcastFn(b)
}

// ─── HTTP Handlers ────────────────────────────────────────────────────────────

// sendRequest is the JSON body for POST /api/telephony/sms/send.
type sendRequest struct {
	ModemPath string `json:"modem_path"`
	To        string `json:"to"`
	Body      string `json:"body"`
}

func (s *SMSService) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req sendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.To == "" || req.Body == "" {
		http.Error(w, "to and body are required", http.StatusBadRequest)
		return
	}
	if req.ModemPath == "" {
		req.ModemPath = mmPath + "/Modem/0"
	}

	smsPath, err := s.messaging.SendSMS(req.ModemPath, req.To, req.Body)
	state := "sent"
	if err != nil {
		log.Printf("[telephony/sms] send error: %v", err)
		state = "failed"
	}

	rec := SMSRecord{
		ModemPath:   req.ModemPath,
		Peer:        req.To,
		Direction:   "out",
		Body:        req.Body,
		State:       state,
		TimestampMs: time.Now().UnixMilli(),
	}
	id, storeErr := s.store.Insert(rec)
	if storeErr != nil {
		log.Printf("[telephony/sms] store insert after send: %v", storeErr)
	} else {
		rec.ID = id
	}

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": err.Error(),
			"state": state,
		})
		return
	}

	s.pushEvent("sms_sent", map[string]interface{}{
		"sms_path": smsPath,
		"record":   rec,
	})
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"sms_path": smsPath,
		"record":   rec,
	})
}

func (s *SMSService) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	peer := r.URL.Query().Get("peer")
	limit := queryInt(r, "limit", 0)

	records, err := s.store.List(peer, limit)
	if err != nil {
		http.Error(w, "store error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, records)
}

func (s *SMSService) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	idStr := r.URL.Query().Get("id")
	if idStr == "" {
		http.Error(w, "id query param required", http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "id must be an integer", http.StatusBadRequest)
		return
	}
	if err := s.store.Delete(id); err != nil {
		http.Error(w, "store error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *SMSService) handleThreads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	threads, err := s.store.Threads()
	if err != nil {
		http.Error(w, "store error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, threads)
}

func (s *SMSService) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "q query param required", http.StatusBadRequest)
		return
	}
	limit := queryInt(r, "limit", 0)
	records, err := s.store.Search(q, limit)
	if err != nil {
		http.Error(w, "store error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, records)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[telephony/sms] json encode: %v", err)
	}
}

func queryInt(r *http.Request, key string, def int) int {
	if s := r.URL.Query().Get(key); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			return v
		}
	}
	return def
}

// parseSMSPaths extracts SMS object paths from the Messaging.List output.
// ModemManager returns a GLib variant array; paths appear as:
//
//	[objectpath '/org/freedesktop/ModemManager1/SMS/0', '/org/.../SMS/1']
//
// We scan for every occurrence of the SMS path prefix in the output.
func parseSMSPaths(out string) []string {
	const prefix = "/org/freedesktop/ModemManager1/SMS/"
	var paths []string
	seen := map[string]bool{}
	remaining := out
	for {
		idx := strings.Index(remaining, prefix)
		if idx < 0 {
			break
		}
		rest := remaining[idx:]
		// Find the end of this path: first char that is not a path char
		end := strings.IndexAny(rest, "'\", )\n\t")
		if end < 0 {
			end = len(rest)
		}
		candidate := rest[:end]
		remaining = rest[1:] // advance past this prefix start
		// Validate: last segment must be a number
		parts := strings.Split(candidate, "/")
		last := parts[len(parts)-1]
		if _, err := strconv.Atoi(last); err == nil && !seen[candidate] {
			paths = append(paths, candidate)
			seen[candidate] = true
		}
	}
	return paths
}

// parseSMSProps parses gdbus Properties.GetAll output for an SMS object.
// Real gdbus output puts each property on its own newline; some test fixtures
// may have all properties on one line separated by commas, so we tokenise on
// both newlines and commas.
func parseSMSProps(out string) SMSProperties {
	var p SMSProperties
	// Replace commas that separate properties with newlines so we can use a
	// single pass regardless of the output format.
	normalised := strings.ReplaceAll(out, ", '", "\n'")
	for _, segment := range strings.Split(normalised, "\n") {
		segment = strings.TrimSpace(segment)
		// Strip leading punctuation like ({
		segment = strings.TrimLeft(segment, "({[")
		switch {
		case strings.HasPrefix(segment, "'Number':"):
			p.Number = extractStringVal(segment)
		case strings.HasPrefix(segment, "'Text':"):
			p.Text = extractStringVal(segment)
		case strings.HasPrefix(segment, "'State':"):
			p.State = extractIntVal(segment)
		case strings.HasPrefix(segment, "'PduType':"):
			p.PDUType = extractIntVal(segment)
		case strings.HasPrefix(segment, "'Timestamp':"):
			p.Timestamp = extractStringVal(segment)
		}
	}
	return p
}
