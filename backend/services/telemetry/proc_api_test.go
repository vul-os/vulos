package telemetry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"vulos/backend/internal/proctl"
)

// fakeProcTree writes one process into a synthetic /proc so the handler can be
// driven end to end without a real process to kill.
func fakeProcTree(t *testing.T, pid, pgid int, comm string, start uint64, cmdline string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fields := []string{strconv.Itoa(pid), "(" + comm + ")", "S", "1", strconv.Itoa(pgid), strconv.Itoa(pgid)}
	for i := 7; i <= 24; i++ {
		if i == 22 {
			fields = append(fields, strconv.FormatUint(start, 10))
			continue
		}
		fields = append(fields, "0")
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(strings.Join(fields, " ")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmdline), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func post(t *testing.T, h http.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("POST", "/api/system/processes/signal", strings.NewReader(body)))
	return rec
}

// ---------------------------------------------------------------------------
// Authorisation
// ---------------------------------------------------------------------------

func TestSignal_NonAdminIsRefusedAndNothingIsSignalled(t *testing.T) {
	root := fakeProcTree(t, 4242, 4242, "victim", 111, "/usr/bin/victim\x00")
	c := proctl.New(root, proctl.Self{PID: 9, PGID: 9, SID: 9})
	var signals int
	c.Signal = func(int, syscall.Signal) error { signals++; return nil }

	rec := post(t, SignalHandler(Deps{
		IsAdmin: func(*http.Request) bool { return false }, Controller: c,
	}), `{"pid":4242,"start":111,"mode":"force"}`)

	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if signals != 0 {
		t.Fatalf("a non-admin caused %d signal(s) to be sent", signals)
	}
}

// A nil IsAdmin is a WIRING MISTAKE, and the endpoint must fail closed on it.
// The alternative — treating "no gate configured" as "no gate needed" — is how
// a privileged route ships open: the code reads as gated, and the gate is nil.
func TestSignal_NilIsAdminFailsClosed(t *testing.T) {
	root := fakeProcTree(t, 4242, 4242, "victim", 111, "/usr/bin/victim\x00")
	c := proctl.New(root, proctl.Self{PID: 9, PGID: 9, SID: 9})
	var signals int
	c.Signal = func(int, syscall.Signal) error { signals++; return nil }

	rec := post(t, SignalHandler(Deps{Controller: c}), `{"pid":4242,"start":111,"mode":"force"}`)
	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403 when IsAdmin is nil", rec.Code)
	}
	if signals != 0 {
		t.Fatalf("a request with no admin gate configured sent %d signal(s)", signals)
	}
}

func TestSignal_HonoursTheOperatorExecKillSwitch(t *testing.T) {
	root := fakeProcTree(t, 4242, 4242, "victim", 111, "/usr/bin/victim\x00")
	c := proctl.New(root, proctl.Self{PID: 9, PGID: 9, SID: 9})
	var signals int
	c.Signal = func(int, syscall.Signal) error { signals++; return nil }

	rec := post(t, SignalHandler(Deps{
		IsAdmin:      func(*http.Request) bool { return true },
		ExecDisabled: func() bool { return true },
		Controller:   c,
	}), `{"pid":4242,"start":111,"mode":"force"}`)

	if rec.Code != 503 {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if signals != 0 {
		t.Fatalf("the kill-switch was on and %d signal(s) still went out", signals)
	}
}

// The protect list binds admins. A role gate alone would let the one account
// that has it end pid 1 from a web page.
func TestSignal_AdminStillCannotEndPid1(t *testing.T) {
	root := fakeProcTree(t, 1, 1, "init", 1, "/sbin/init\x00")
	c := proctl.New(root, proctl.Self{PID: 9, PGID: 9, SID: 9})
	var signals int
	c.Signal = func(int, syscall.Signal) error { signals++; return nil }

	rec := post(t, SignalHandler(Deps{
		IsAdmin: func(*http.Request) bool { return true }, Controller: c,
	}), `{"pid":1,"start":1,"mode":"force"}`)

	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if signals != 0 {
		t.Fatalf("an admin request signalled pid 1 %d time(s)", signals)
	}
	var body map[string]string
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["code"] != "init" {
		t.Errorf("code = %q, want init — the UI needs a stable reason, not prose", body["code"])
	}
}

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

// The starttime is what makes a posted pid safe. Accepting a request without
// one would make the guard present in the code and absent from every call.
func TestSignal_RejectsARequestWithNoStarttime(t *testing.T) {
	root := fakeProcTree(t, 4242, 4242, "victim", 111, "/usr/bin/victim\x00")
	c := proctl.New(root, proctl.Self{PID: 9, PGID: 9, SID: 9})
	var signals int
	c.Signal = func(int, syscall.Signal) error { signals++; return nil }

	rec := post(t, SignalHandler(Deps{
		IsAdmin: func(*http.Request) bool { return true }, Controller: c,
	}), `{"pid":4242,"mode":"force"}`)

	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if signals != 0 {
		t.Fatalf("a request with no identity sent %d signal(s)", signals)
	}
}

// A stale pid gets 409, not 404. 404 reads as "already gone" and invites a
// retry; the pid now belongs to a stranger, so a retry is the worst response.
func TestSignal_StalePidIs409NotRetryable(t *testing.T) {
	root := fakeProcTree(t, 4242, 4242, "somethingelse", 999, "/usr/sbin/sshd\x00")
	c := proctl.New(root, proctl.Self{PID: 9, PGID: 9, SID: 9})
	var signals int
	c.Signal = func(int, syscall.Signal) error { signals++; return nil }

	rec := post(t, SignalHandler(Deps{
		IsAdmin: func(*http.Request) bool { return true }, Controller: c,
	}), `{"pid":4242,"start":111,"mode":"force"}`)

	if rec.Code != 409 {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if signals != 0 {
		t.Fatalf("a recycled pid was signalled %d time(s)", signals)
	}
}

func TestSignal_AdminForceQuitSendsSIGKILLAndReports(t *testing.T) {
	root := fakeProcTree(t, 4242, 4242, "frozen", 111, "/usr/bin/frozen\x00")
	c := proctl.New(root, proctl.Self{PID: 9, PGID: 9, SID: 9})
	var sent []syscall.Signal
	c.Signal = func(pid int, sig syscall.Signal) error {
		sent = append(sent, sig)
		os.RemoveAll(filepath.Join(root, "4242"))
		return nil
	}
	var audited []string
	rec := post(t, SignalHandler(Deps{
		IsAdmin:    func(*http.Request) bool { return true },
		Controller: c,
		Audit:      func(r *http.Request, route, detail string) { audited = append(audited, route+" "+detail) },
	}), `{"pid":4242,"start":111,"mode":"force"}`)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if len(sent) != 1 || sent[0] != syscall.SIGKILL {
		t.Fatalf("signals = %v, want [SIGKILL]", sent)
	}
	var res proctl.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Outcome != proctl.OutcomeKilled {
		t.Errorf("outcome = %q, want %q", res.Outcome, proctl.OutcomeKilled)
	}
	// The audit line must exist BEFORE the signal, or a kill that takes the
	// box down leaves no record of who asked for it.
	if len(audited) == 0 {
		t.Error("no audit entry was written for an accepted kill")
	}
}

// ---------------------------------------------------------------------------
// Error bodies — the shape a client cannot mistake for data
// ---------------------------------------------------------------------------

// Every /api/* service here answers 5xx with JSON that parses cleanly, so a
// client doing `.then(r => r.json())` gets a value on failure too. If that
// value were an ARRAY the client would render its designed empty state and
// tell the user their box has no processes.
func TestErrorBodiesAreObjectsNotArrays(t *testing.T) {
	root := fakeProcTree(t, 4242, 4242, "victim", 111, "/usr/bin/victim\x00")
	c := proctl.New(root, proctl.Self{PID: 9, PGID: 9, SID: 9})
	c.Signal = func(int, syscall.Signal) error { return nil }

	cases := map[string]*httptest.ResponseRecorder{
		"403 not admin": post(t, SignalHandler(Deps{Controller: c}), `{"pid":4242,"start":111}`),
		"400 no start": post(t, SignalHandler(Deps{
			IsAdmin: func(*http.Request) bool { return true }, Controller: c,
		}), `{"pid":4242}`),
		"409 stale": post(t, SignalHandler(Deps{
			IsAdmin: func(*http.Request) bool { return true }, Controller: c,
		}), `{"pid":4242,"start":7}`),
	}
	for name, rec := range cases {
		t.Run(name, func(t *testing.T) {
			body := strings.TrimSpace(rec.Body.String())
			if strings.HasPrefix(body, "[") {
				t.Fatalf("error body is a JSON ARRAY: %s — a client coercing this to a list shows an empty state", body)
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(body), &obj); err != nil {
				t.Fatalf("error body does not parse as an object: %s", body)
			}
			if obj["error"] == nil {
				t.Errorf("error body has no `error` key: %s", body)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// /api/proc/apps — the mobile app-switcher surface
// ---------------------------------------------------------------------------

func TestApps_ReadableWithoutAdmin(t *testing.T) {
	rows := []AppStatus{
		{AppID: "notes", Kind: KindProcess, Running: true, PID: 40,
			Responding: proctl.Responsiveness{Status: proctl.StatusResponding, Method: proctl.MethodHTTPProbe}, Closable: true},
		{AppID: "gimp", Kind: KindStream, Running: true,
			Responding: proctl.StreamUnknown(), Closable: true},
		{AppID: "activity", Kind: KindBuiltin,
			Responding: proctl.BuiltinNotApplicable(), Closable: false},
	}
	rec := httptest.NewRecorder()
	AppsHandler(Deps{IsAdmin: func(*http.Request) bool { return false }, Apps: func() []AppStatus { return rows }})(
		rec, httptest.NewRequest("GET", "/api/proc/apps", nil))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 — reading what is running is not a privileged capability", rec.Code)
	}
	var body struct{ Apps []AppStatus }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Apps) != 3 {
		t.Fatalf("got %d apps, want 3", len(body.Apps))
	}
	// Each kind must carry its own method — the whole point of the surface.
	byKind := map[string]AppStatus{}
	for _, a := range body.Apps {
		byKind[a.Kind] = a
	}
	if byKind[KindProcess].Responding.Method != proctl.MethodHTTPProbe {
		t.Errorf("process-backed app method = %q, want http_probe", byKind[KindProcess].Responding.Method)
	}
	if byKind[KindStream].Responding.Status != proctl.StatusUnknown {
		t.Errorf("streamed app status = %q, want unknown", byKind[KindStream].Responding.Status)
	}
	if byKind[KindBuiltin].Responding.Status != proctl.StatusNotApplicable {
		t.Errorf("built-in status = %q, want not_applicable", byKind[KindBuiltin].Responding.Status)
	}
	if byKind[KindBuiltin].Closable {
		t.Error("a built-in must not be marked server-closable; the shell closes its own windows")
	}
}

func TestApps_EmptyListSerialisesAsAnArrayNotNull(t *testing.T) {
	rec := httptest.NewRecorder()
	AppsHandler(Deps{})(rec, httptest.NewRequest("GET", "/api/proc/apps", nil))
	if !strings.Contains(rec.Body.String(), `"apps":[]`) {
		t.Fatalf("body = %s, want an empty ARRAY — null forces every client to nil-check", rec.Body.String())
	}
}

func TestCloseApp_NonAdminIsRefusedAndTheAppIsNotStopped(t *testing.T) {
	var closed []string
	rec := httptest.NewRecorder()
	CloseAppHandler(Deps{
		IsAdmin:  func(*http.Request) bool { return false },
		CloseApp: func(id string, force bool) error { closed = append(closed, id); return nil },
	})(rec, httptest.NewRequest("POST", "/api/proc/apps/close", strings.NewReader(`{"app_id":"notes"}`)))

	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if len(closed) != 0 {
		t.Fatalf("a non-admin closed %v — an appnet app is box-level and shared by every user", closed)
	}
}

func TestCloseApp_AdminClosesAndTheForceFlagIsForwarded(t *testing.T) {
	var gotID string
	var gotForce bool
	rec := httptest.NewRecorder()
	CloseAppHandler(Deps{
		IsAdmin:  func(*http.Request) bool { return true },
		CloseApp: func(id string, force bool) error { gotID, gotForce = id, force; return nil },
	})(rec, httptest.NewRequest("POST", "/api/proc/apps/close", strings.NewReader(`{"app_id":"notes","force":true}`)))

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if gotID != "notes" {
		t.Errorf("app_id = %q, want notes", gotID)
	}
	if !gotForce {
		t.Error("force was not forwarded; a Force Quit would have been a polite quit")
	}
}

// ---------------------------------------------------------------------------
// The PID column used to hold a socket inode
// ---------------------------------------------------------------------------

// parseNetFile parked the socket inode in NetConn.PID and then looked the
// owner map up by that same field, so PID kept the inode forever. On a busy
// box inodes are six- and seven-digit numbers, which look exactly like
// plausible pids — that is why it read as correct for as long as it did, and
// why nothing could ever act on a connection.
//
// This asserts the two are DIFFERENT fields carrying different values, which
// is the only shape in which the bug cannot come back.
func TestResolveSocketOwners_PIDIsAPidAndInodeIsAnInode(t *testing.T) {
	conns := []NetConn{
		{Proto: "tcp", LocalPort: 8080, Inode: 918273},
		{Proto: "tcp", LocalPort: 22, Inode: 918274},
		{Proto: "tcp", LocalPort: 443, Inode: 555555}, // no owner: TIME_WAIT
	}
	owners := map[int]SocketOwner{
		918273: {PID: 1201, Name: "vulos-server"},
		918274: {PID: 640, Name: "sshd"},
	}

	got := ResolveSocketOwners(conns, owners)

	if got[0].PID != 1201 || got[0].Process != "vulos-server" {
		t.Errorf("row 0 = pid %d %q, want 1201 vulos-server", got[0].PID, got[0].Process)
	}
	if got[0].Inode != 918273 {
		t.Errorf("row 0 inode = %d, want 918273 — the join key must survive the join", got[0].Inode)
	}
	if got[1].PID != 640 {
		t.Errorf("row 1 pid = %d, want 640", got[1].PID)
	}
	for i, c := range got {
		if c.PID != 0 && c.PID == c.Inode {
			t.Errorf("row %d has PID == Inode (%d): the PID column is holding a socket inode again", i, c.PID)
		}
	}
	// An unresolved socket reports pid 0, not an inode dressed as a pid.
	if got[2].PID != 0 {
		t.Errorf("row 2 pid = %d, want 0 for a socket with no owning process", got[2].PID)
	}
}

// ---------------------------------------------------------------------------
// "No processes" is never a true answer
// ---------------------------------------------------------------------------

// A box always has processes. A 200 carrying an empty list is therefore never
// correct here, and it is precisely what a client renders as its designed
// empty state — telling the user their box is running nothing. Holds on both
// platforms: where /proc is unreadable the answer must be a 503, and where it
// is readable the list must be non-empty.
func TestProcessHandler_NeverAnswers200WithAnEmptyList(t *testing.T) {
	rec := httptest.NewRecorder()
	ProcessHandler()(rec, httptest.NewRequest("GET", "/api/system/processes", nil))

	if rec.Code == 200 {
		var procs []ProcessInfo
		if err := json.Unmarshal(rec.Body.Bytes(), &procs); err != nil {
			t.Fatalf("200 body does not decode as a process list: %v", err)
		}
		if len(procs) == 0 {
			t.Fatal("200 with an empty process list — a client cannot tell this from a healthy box with nothing running")
		}
		return
	}
	if rec.Code != 503 {
		t.Fatalf("status = %d, want 200-with-processes or 503", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("503 body does not decode as an object: %s", rec.Body.String())
	}
	if body["code"] != "proc_unavailable" {
		t.Errorf("code = %q, want proc_unavailable", body["code"])
	}
}

// ---------------------------------------------------------------------------
// The listing tells the UI what it may not kill
// ---------------------------------------------------------------------------

// Protection is annotated on the LIST so a Quit control can be disabled with a
// reason. An enabled control that 403s on click teaches the user the feature is
// unreliable rather than that the process is off limits.
func TestProcessListFor_AnnotatesProtectionWhereItCanReadProc(t *testing.T) {
	procs := ProcessListFor(proctl.Self{PID: -1, PGID: -1, SID: -1})
	if len(procs) == 0 {
		t.Skip("no readable /proc on this host")
	}
	var sawInit, sawStart bool
	for _, p := range procs {
		if p.PID == 1 {
			sawInit = true
			if !p.Protected {
				t.Error("pid 1 is not marked protected in the listing")
			}
			if p.ProtectedReason == "" {
				t.Error("pid 1 is protected with no reason; the UI has nothing to show")
			}
		}
		if p.Start > 0 {
			sawStart = true
		}
	}
	if !sawInit {
		t.Error("no pid 1 in the listing")
	}
	if !sawStart {
		t.Error("no process carried a starttime — without it the client cannot form a safe kill request")
	}
}

// ---------------------------------------------------------------------------
// Wiring
// ---------------------------------------------------------------------------

// A handler nobody registered is a feature that does not exist. This drives the
// real mux, so a typo'd path or a route left out of RegisterProcessRoutes shows
// up as a 404 here rather than as a button that does nothing.
func TestRegisterProcessRoutes_EveryRouteIsReachableAndTheMutatingOnesAreGated(t *testing.T) {
	mux := http.NewServeMux()
	RegisterProcessRoutes(mux, Deps{
		IsAdmin:    func(*http.Request) bool { return false },
		Controller: proctl.New(t.TempDir(), proctl.Self{PID: 1, PGID: 1, SID: 1}),
		Apps:       func() []AppStatus { return nil },
		CloseApp:   func(string, bool) error { return nil },
	})

	reads := []string{"/api/system/processes", "/api/system/network", "/api/proc/apps"}
	for _, path := range reads {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code == 404 {
			t.Errorf("GET %s is not registered", path)
		}
		if rec.Code == 403 {
			t.Errorf("GET %s is admin-gated; reading what is running is not a privileged capability", path)
		}
	}

	writes := []string{"/api/system/processes/signal", "/api/proc/apps/close"}
	for _, path := range writes {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(`{"pid":1,"start":1,"app_id":"x"}`)))
		if rec.Code == 404 {
			t.Errorf("POST %s is not registered", path)
			continue
		}
		if rec.Code != 403 {
			t.Errorf("POST %s answered %d for a non-admin, want 403", path, rec.Code)
		}
	}
}
