package telephony

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ─── Mock D-Bus layer ─────────────────────────────────────────────────────────

// mockSMSMessaging implements SMSMessaging without any real D-Bus or hardware.
type mockSMSMessaging struct {
	sent      []sentCall
	paths     []string
	smsByPath map[string]SMSProperties
	sendErr   error
}

type sentCall struct {
	modemPath string
	number    string
	text      string
}

func (m *mockSMSMessaging) SendSMS(modemPath, number, text string) (string, error) {
	if m.sendErr != nil {
		return "", m.sendErr
	}
	m.sent = append(m.sent, sentCall{modemPath, number, text})
	return "/org/freedesktop/ModemManager1/SMS/99", nil
}

func (m *mockSMSMessaging) ListSMSPaths(modemPath string) ([]string, error) {
	return m.paths, nil
}

func (m *mockSMSMessaging) GetSMS(smsPath string) (SMSProperties, error) {
	if p, ok := m.smsByPath[smsPath]; ok {
		return p, nil
	}
	return SMSProperties{}, fmt.Errorf("mock: sms path %s not found", smsPath)
}

func (m *mockSMSMessaging) DeleteSMS(modemPath, smsPath string) error {
	return nil
}

// ─── Test helpers ─────────────────────────────────────────────────────────────

func newTestSMSService(t *testing.T) (*SMSService, *SMSStore, *mockSMSMessaging) {
	t.Helper()
	store, err := OpenSMSStoreInMemory()
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	var pushed [][]byte
	broadcastFn := func(b []byte) { pushed = append(pushed, b) }
	mock := &mockSMSMessaging{
		smsByPath: make(map[string]SMSProperties),
	}
	svc := NewSMSService(store, mock, broadcastFn)
	return svc, store, mock
}

// ─── SMSStore unit tests ──────────────────────────────────────────────────────

func TestSMSStore_InsertAndList(t *testing.T) {
	store, err := OpenSMSStoreInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now().UnixMilli()
	records := []SMSRecord{
		{Peer: "+15551234567", Direction: "in", Body: "Hello", State: "received", TimestampMs: now - 2000},
		{Peer: "+15551234567", Direction: "out", Body: "Hi back", State: "sent", TimestampMs: now - 1000},
		{Peer: "+15559999999", Direction: "in", Body: "From other", State: "received", TimestampMs: now},
	}
	for i := range records {
		id, err := store.Insert(records[i])
		if err != nil {
			t.Fatalf("insert[%d]: %v", i, err)
		}
		if id == 0 {
			t.Fatalf("insert[%d]: expected non-zero id", i)
		}
	}

	// All messages
	all, err := store.List("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(all))
	}
	// Newest-first
	if all[0].TimestampMs < all[1].TimestampMs {
		t.Error("expected newest-first ordering")
	}

	// Filter by peer
	byPeer, err := store.List("+15551234567", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(byPeer) != 2 {
		t.Fatalf("expected 2 messages for peer, got %d", len(byPeer))
	}

	// Limit
	limited, err := store.List("", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 {
		t.Fatalf("expected 2 with limit, got %d", len(limited))
	}
}

func TestSMSStore_Delete(t *testing.T) {
	store, err := OpenSMSStoreInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	id, _ := store.Insert(SMSRecord{Peer: "+1", Direction: "in", Body: "del me", State: "received"})
	if err := store.Delete(id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	all, _ := store.List("", 0)
	if len(all) != 0 {
		t.Fatalf("expected 0 after delete, got %d", len(all))
	}
	// Idempotent
	if err := store.Delete(id); err != nil {
		t.Fatalf("second delete should not error: %v", err)
	}
}

func TestSMSStore_Threads(t *testing.T) {
	store, err := OpenSMSStoreInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now().UnixMilli()
	msgs := []SMSRecord{
		{Peer: "+1", Direction: "in", Body: "a", State: "received", TimestampMs: now - 3000},
		{Peer: "+1", Direction: "out", Body: "b", State: "sent", TimestampMs: now - 2000},
		{Peer: "+1", Direction: "in", Body: "c", State: "received", TimestampMs: now - 1000},
		{Peer: "+2", Direction: "in", Body: "x", State: "received", TimestampMs: now},
	}
	for i := range msgs {
		if _, err := store.Insert(msgs[i]); err != nil {
			t.Fatalf("insert[%d]: %v", i, err)
		}
	}

	threads, err := store.Threads()
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 2 {
		t.Fatalf("expected 2 threads, got %d", len(threads))
	}
	// "+2" should be first (most recent)
	if threads[0].Peer != "+2" {
		t.Errorf("expected +2 first, got %s", threads[0].Peer)
	}

	var peer1 *SMSThread
	for i := range threads {
		if threads[i].Peer == "+1" {
			peer1 = &threads[i]
		}
	}
	if peer1 == nil {
		t.Fatal("thread for +1 not found")
	}
	if peer1.MessageCount != 3 {
		t.Errorf("expected 3 messages for +1, got %d", peer1.MessageCount)
	}
	if peer1.LastBody != "c" {
		t.Errorf("expected last body 'c', got %q", peer1.LastBody)
	}
}

func TestSMSStore_Search(t *testing.T) {
	store, err := OpenSMSStoreInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	msgs := []SMSRecord{
		{Peer: "+1", Direction: "in", Body: "Hello world", State: "received"},
		{Peer: "+2", Direction: "in", Body: "Goodbye world", State: "received"},
		{Peer: "+3", Direction: "in", Body: "Hello again", State: "received"},
		{Peer: "+4", Direction: "in", Body: "Nothing here", State: "received"},
	}
	for i := range msgs {
		store.Insert(msgs[i])
	}

	results, err := store.Search("Hello", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 search results for 'Hello', got %d", len(results))
	}

	// Case-insensitive
	results2, err := store.Search("hello", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(results2) != 2 {
		t.Fatalf("expected 2 case-insensitive results, got %d", len(results2))
	}

	// With limit
	limitRes, err := store.Search("world", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limitRes) != 1 {
		t.Fatalf("expected 1 result with limit, got %d", len(limitRes))
	}
}

// ─── HTTP Handler tests ───────────────────────────────────────────────────────

func TestHandleSend_Success(t *testing.T) {
	svc, store, mock := newTestSMSService(t)
	mux := http.NewServeMux()
	RegisterSMSHandlers(mux, svc)

	body := `{"modem_path":"/org/freedesktop/ModemManager1/Modem/0","to":"+15551234567","body":"Test message"}`
	req := httptest.NewRequest(http.MethodPost, "/api/telephony/sms/send", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(mock.sent) != 1 {
		t.Fatalf("expected 1 sent call, got %d", len(mock.sent))
	}
	if mock.sent[0].number != "+15551234567" {
		t.Errorf("expected to=+15551234567, got %s", mock.sent[0].number)
	}

	// Verify persisted
	all, _ := store.List("", 0)
	if len(all) != 1 {
		t.Fatalf("expected 1 stored message, got %d", len(all))
	}
	if all[0].Direction != "out" {
		t.Errorf("expected direction=out, got %s", all[0].Direction)
	}
}

func TestHandleSend_MissingFields(t *testing.T) {
	svc, _, _ := newTestSMSService(t)
	mux := http.NewServeMux()
	RegisterSMSHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodPost, "/api/telephony/sms/send",
		bytes.NewBufferString(`{"to":"+1"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandleSend_WrongMethod(t *testing.T) {
	svc, _, _ := newTestSMSService(t)
	mux := http.NewServeMux()
	RegisterSMSHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/telephony/sms/send", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandleList(t *testing.T) {
	svc, store, _ := newTestSMSService(t)
	mux := http.NewServeMux()
	RegisterSMSHandlers(mux, svc)

	now := time.Now().UnixMilli()
	store.Insert(SMSRecord{Peer: "+1", Direction: "in", Body: "A", State: "received", TimestampMs: now - 1000})
	store.Insert(SMSRecord{Peer: "+2", Direction: "in", Body: "B", State: "received", TimestampMs: now})

	req := httptest.NewRequest(http.MethodGet, "/api/telephony/sms/list", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var results []SMSRecord
	if err := json.NewDecoder(rec.Body).Decode(&results); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// Filter by peer
	req2 := httptest.NewRequest(http.MethodGet, "/api/telephony/sms/list?peer=%2B1", nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	var filtered []SMSRecord
	json.NewDecoder(rec2.Body).Decode(&filtered)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 filtered, got %d", len(filtered))
	}
}

func TestHandleDelete(t *testing.T) {
	svc, store, _ := newTestSMSService(t)
	mux := http.NewServeMux()
	RegisterSMSHandlers(mux, svc)

	id, _ := store.Insert(SMSRecord{Peer: "+1", Direction: "in", Body: "bye", State: "received"})

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/telephony/sms/delete?id=%d", id), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	all, _ := store.List("", 0)
	if len(all) != 0 {
		t.Fatalf("expected 0 after delete, got %d", len(all))
	}
}

func TestHandleDelete_MissingID(t *testing.T) {
	svc, _, _ := newTestSMSService(t)
	mux := http.NewServeMux()
	RegisterSMSHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodDelete, "/api/telephony/sms/delete", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandleThreads(t *testing.T) {
	svc, store, _ := newTestSMSService(t)
	mux := http.NewServeMux()
	RegisterSMSHandlers(mux, svc)

	now := time.Now().UnixMilli()
	store.Insert(SMSRecord{Peer: "+1", Direction: "in", Body: "x", State: "received", TimestampMs: now - 2000})
	store.Insert(SMSRecord{Peer: "+1", Direction: "out", Body: "y", State: "sent", TimestampMs: now - 1000})
	store.Insert(SMSRecord{Peer: "+2", Direction: "in", Body: "z", State: "received", TimestampMs: now})

	req := httptest.NewRequest(http.MethodGet, "/api/telephony/sms/threads", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var threads []SMSThread
	if err := json.NewDecoder(rec.Body).Decode(&threads); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(threads) != 2 {
		t.Fatalf("expected 2 threads, got %d", len(threads))
	}
	if threads[0].Peer != "+2" {
		t.Errorf("expected +2 first (most recent), got %s", threads[0].Peer)
	}
}

func TestHandleSearch(t *testing.T) {
	svc, store, _ := newTestSMSService(t)
	mux := http.NewServeMux()
	RegisterSMSHandlers(mux, svc)

	store.Insert(SMSRecord{Peer: "+1", Direction: "in", Body: "find me please", State: "received"})
	store.Insert(SMSRecord{Peer: "+2", Direction: "in", Body: "nothing relevant", State: "received"})

	req := httptest.NewRequest(http.MethodGet, "/api/telephony/sms/search?q=find+me", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var results []SMSRecord
	json.NewDecoder(rec.Body).Decode(&results)
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
}

func TestHandleSearch_MissingQuery(t *testing.T) {
	svc, _, _ := newTestSMSService(t)
	mux := http.NewServeMux()
	RegisterSMSHandlers(mux, svc)

	req := httptest.NewRequest(http.MethodGet, "/api/telephony/sms/search", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// ─── Incoming SMS / WebSocket push ───────────────────────────────────────────

func TestInjectIncomingSMS_PersistsAndPushes(t *testing.T) {
	var pushed [][]byte
	store, _ := OpenSMSStoreInMemory()
	defer store.Close()
	mock := &mockSMSMessaging{smsByPath: make(map[string]SMSProperties)}
	svc := NewSMSService(store, mock, func(b []byte) { pushed = append(pushed, b) })

	props := SMSProperties{
		Number:  "+15550000001",
		Text:    "Incoming test message",
		PDUType: 1,
		State:   0,
	}
	svc.InjectIncomingSMS("/org/freedesktop/ModemManager1/Modem/0", props)

	// Persisted
	all, _ := store.List("", 0)
	if len(all) != 1 {
		t.Fatalf("expected 1 stored, got %d", len(all))
	}
	if all[0].Direction != "in" {
		t.Errorf("expected direction=in, got %s", all[0].Direction)
	}
	if all[0].Body != "Incoming test message" {
		t.Errorf("unexpected body: %s", all[0].Body)
	}

	// WS push
	if len(pushed) != 1 {
		t.Fatalf("expected 1 WS push, got %d", len(pushed))
	}
	var ev Event
	if err := json.Unmarshal(pushed[0], &ev); err != nil {
		t.Fatalf("unmarshal push: %v", err)
	}
	if ev.Type != "sms_received" {
		t.Errorf("expected event type sms_received, got %s", ev.Type)
	}
}

// ─── pollIncoming tests ───────────────────────────────────────────────────────

func TestPollIncoming_FiltersOutgoing(t *testing.T) {
	store, _ := OpenSMSStoreInMemory()
	defer store.Close()
	mock := &mockSMSMessaging{
		paths: []string{
			"/org/freedesktop/ModemManager1/SMS/1",
			"/org/freedesktop/ModemManager1/SMS/2",
		},
		smsByPath: map[string]SMSProperties{
			"/org/freedesktop/ModemManager1/SMS/1": {Number: "+1", Text: "in msg", PDUType: 1},
			"/org/freedesktop/ModemManager1/SMS/2": {Number: "+2", Text: "out msg", PDUType: 2},
		},
	}
	svc := NewSMSService(store, mock, nil)

	seen := make(map[string]bool)
	svc.pollIncoming("/org/freedesktop/ModemManager1/Modem/0", seen)

	all, _ := store.List("", 0)
	if len(all) != 1 {
		t.Fatalf("expected 1 incoming message persisted, got %d", len(all))
	}
	if all[0].Peer != "+1" {
		t.Errorf("expected peer +1, got %s", all[0].Peer)
	}
	// seen should have both paths recorded to avoid re-processing
	if len(seen) != 2 {
		t.Errorf("expected 2 seen paths, got %d", len(seen))
	}
}

func TestPollIncoming_DeduplicatesOnSecondPoll(t *testing.T) {
	store, _ := OpenSMSStoreInMemory()
	defer store.Close()
	mock := &mockSMSMessaging{
		paths: []string{"/org/freedesktop/ModemManager1/SMS/1"},
		smsByPath: map[string]SMSProperties{
			"/org/freedesktop/ModemManager1/SMS/1": {Number: "+1", Text: "msg", PDUType: 1},
		},
	}
	svc := NewSMSService(store, mock, nil)
	seen := make(map[string]bool)

	svc.pollIncoming("/org/freedesktop/ModemManager1/Modem/0", seen)
	svc.pollIncoming("/org/freedesktop/ModemManager1/Modem/0", seen)

	all, _ := store.List("", 0)
	if len(all) != 1 {
		t.Fatalf("expected 1 stored (no dup), got %d", len(all))
	}
}

// ─── Helper unit tests ────────────────────────────────────────────────────────

func TestExtractObjectPathSMS(t *testing.T) {
	out := `(objectpath '/org/freedesktop/ModemManager1/SMS/0',)`
	path := extractObjectPath(out)
	if path != "/org/freedesktop/ModemManager1/SMS/0" {
		t.Errorf("extractObjectPath got %q", path)
	}
}

func TestParseSMSPaths(t *testing.T) {
	out := `([objectpath '/org/freedesktop/ModemManager1/SMS/0', '/org/freedesktop/ModemManager1/SMS/1'],)`
	paths := parseSMSPaths(out)
	if len(paths) != 2 {
		t.Fatalf("expected 2 paths, got %d: %v", len(paths), paths)
	}
}

func TestParseSMSProps(t *testing.T) {
	out := `({'Number': <'"+15551234567"'>, 'Text': <'hello'>, 'State': <uint32 3>, 'PduType': <uint32 1>, 'Timestamp': <'2024-01-01T12:00:00Z'>},)`
	props := parseSMSProps(out)
	if props.PDUType != 1 {
		t.Errorf("expected PDUType=1, got %d", props.PDUType)
	}
}
