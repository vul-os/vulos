package telemetry

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vulos/backend/internal/proctl"
)

// This file is the HTTP surface for ending processes and for asking whether
// something is frozen. The decisions all live in internal/proctl; this is
// transport, authorisation wiring and JSON.

// Deps are the collaborators the process-control routes need, injected rather
// than imported so this package does not depend on auth, appnet or stream —
// and so the AUTHORISATION can be exercised in a test with a fake IsAdmin
// instead of a real session.
type Deps struct {
	// IsAdmin reports whether the request carries an admin session. Required:
	// a nil IsAdmin is treated as "nobody is admin", so a wiring mistake fails
	// closed rather than opening the kill endpoint to everyone.
	IsAdmin func(*http.Request) bool
	// ExecDisabled mirrors the operator kill-switch that already gates
	// /api/apps/launch and /api/exec. Ending processes is the same class of
	// capability and honours the same switch.
	ExecDisabled func() bool
	// Audit records a privileged action. Optional.
	Audit func(r *http.Request, route, detail string)
	// Controller performs the kill. Required.
	Controller *proctl.Controller
	// Apps lists every app subject the box is running, already annotated with
	// its responsiveness. Supplied by main.go, which is the only place that
	// can see appnet, the gateway and the stream pool at once.
	Apps func() []AppStatus
	// CloseApp ends one app by id. Returns an error the client can read.
	CloseApp func(appID string, force bool) error
}

// AppStatus is one row of GET /api/proc/apps — the surface the mobile app
// switcher consumes.
type AppStatus struct {
	AppID string `json:"app_id"`
	Name  string `json:"name"`
	// Kind is what sort of thing this is, because it determines what
	// "responding" can possibly mean. One of: process, stream, builtin.
	Kind    string `json:"kind"`
	Running bool   `json:"running"`
	// PID is the app's own process when it has one, else 0.
	PID    int    `json:"pid"`
	MemRSS uint64 `json:"mem_rss"`
	// Responding always carries its own Method, so a caller can tell a
	// measurement from an absence of one. See proctl.Responsiveness.
	Responding proctl.Responsiveness `json:"responding"`
	// Closable reports whether CloseApp can act on this row. False for
	// built-ins, which the shell closes itself with no server involved.
	Closable bool `json:"closable"`
}

// Kind values. Deliberately explicit strings rather than a bool pair: there are
// three, they behave differently, and a bool would force two of them to share.
const (
	KindProcess = "process"
	KindStream  = "stream"
	KindBuiltin = "builtin"
)

// RegisterProcessRoutes wires the process-control surface onto mux.
func RegisterProcessRoutes(mux *http.ServeMux, d Deps) {
	mux.HandleFunc("GET /api/system/processes", ProcessHandler())
	mux.HandleFunc("GET /api/system/network", NetworkHandler())
	mux.HandleFunc("POST /api/system/processes/signal", SignalHandler(d))
	mux.HandleFunc("GET /api/proc/apps", AppsHandler(d))
	mux.HandleFunc("POST /api/proc/apps/close", CloseAppHandler(d))
}

// admin is the role gate, failing closed on a nil IsAdmin.
func (d Deps) admin(r *http.Request) bool {
	return d.IsAdmin != nil && d.IsAdmin(r)
}

func (d Deps) audit(r *http.Request, route, detail string) {
	if d.Audit != nil {
		d.Audit(r, route, detail)
	}
}

// signalRequest is the wire shape of POST /api/system/processes/signal.
type signalRequest struct {
	PID int `json:"pid"`
	// Start is the starttime the client observed in the listing. REQUIRED.
	// Sent as a string as well as a number would be ambiguous; it is a plain
	// JSON number, and a pointer so "absent" is distinguishable from 0 — a
	// process with starttime 0 is possible (pid 1 on some kernels), and
	// treating absent as 0 would silently accept a request with no identity.
	Start   *uint64 `json:"start"`
	Mode    string  `json:"mode"`
	GraceMS *int    `json:"grace_ms"`
}

// SignalHandler ends an arbitrary process.
//
// AUTHORISATION, in the order it is applied:
//
//  1. The operator kill-switch (VULOS_DISABLE_EXEC), same as /api/apps/launch.
//  2. Admin role. This follows the precedent set by /api/apps/launch and
//     /api/apps/stop, and it is the right precedent rather than merely the
//     existing one: on the bare-metal path cmd/init runs as PID 1 with no
//     credential drop, so this endpoint can send SIGKILL as root to anything
//     on the box. There is no per-user process ownership model in Vulos to
//     scope it more finely — processes are box-level, not user-level — so a
//     narrower gate would have to be invented rather than enforced, and an
//     invented one is the kind that turns out not to be checked anywhere.
//  3. proctl's protect list, which binds admins too. Points 1 and 2 decide WHO
//     may ask; this decides WHAT may be ended, and it is the half that stops
//     the capability being self-destructive.
//  4. The (pid, starttime) identity check, which is not authorisation but is
//     the difference between ending the process the admin chose and ending
//     whichever process now holds that number.
//
// Every accepted request is audited before the signal is sent, so a kill that
// takes the box down is still in the log.
func SignalHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.ExecDisabled != nil && d.ExecDisabled() {
			writeProcErr(w, 503, "exec disabled by administrator", "")
			return
		}
		if !d.admin(r) {
			d.audit(r, "POST /api/system/processes/signal", "DENIED: not admin")
			writeProcErr(w, 403, "admin only", "")
			return
		}
		if d.Controller == nil {
			writeProcErr(w, 503, "process control unavailable", "")
			return
		}

		var req signalRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeProcErr(w, 400, "invalid request", "")
			return
		}
		if req.Start == nil {
			// Not pedantry. A pid on its own is a stale handle, and the ONLY
			// thing that makes it safe is the starttime the client saw. A
			// caller that cannot supply one is a caller that did not read the
			// process it is asking to end.
			writeProcErr(w, 400,
				"start is required: a pid alone does not identify a process, because the kernel reuses pids", "")
			return
		}

		mode := proctl.ModeQuit
		if strings.EqualFold(req.Mode, string(proctl.ModeForce)) {
			mode = proctl.ModeForce
		}
		grace := proctl.DefaultGrace
		if req.GraceMS != nil {
			grace = time.Duration(*req.GraceMS) * time.Millisecond
		}

		d.audit(r, "POST /api/system/processes/signal",
			"pid="+strconv.Itoa(req.PID)+" mode="+string(mode))

		res, err := d.Controller.Do(proctl.Request{
			PID: req.PID, Start: *req.Start, Mode: mode, Grace: grace,
		})
		switch {
		case err == nil:
			writeProcJSON(w, 200, res)
		case err == proctl.ErrNoSuchProcess:
			writeProcErr(w, 404, "no such process", "")
		case err == proctl.ErrIdentityMismatch:
			// 409, not 404: the pid EXISTS, it is simply not the process the
			// client selected. A 404 would read as "already gone" and invite a
			// retry, and a retry is exactly the wrong response — the pid now
			// belongs to a stranger.
			writeProcErr(w, 409,
				"that pid now belongs to a different process; refresh the list", "identity_mismatch")
		default:
			if den, ok := err.(*proctl.Denial); ok {
				writeProcErr(w, 403, den.Reason, den.Code)
				return
			}
			writeProcErr(w, 500, err.Error(), "")
		}
	}
}

// AppsHandler answers "what is running, and is it responding?".
//
// Readable by any authenticated user. Reading which apps are up is not a
// privileged capability — the app launcher already shows it — and the mobile
// app switcher needs it on every user's phone, not just an admin's.
func AppsHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		apps := []AppStatus{}
		if d.Apps != nil {
			apps = d.Apps()
		}
		writeProcJSON(w, 200, map[string]any{"apps": apps})
	}
}

// CloseAppHandler ends one app.
//
// ADMIN-ONLY, and this is the answer to the mobile agent's question rather
// than an oversight. An appnet app is a BOX-LEVEL singleton: one namespace,
// one process, shared by every user of the box. "Swipe it away on my phone"
// would stop it for everyone, including whoever is mid-edit in it on the
// desktop. The same reasoning already makes /api/apps/stop admin-only.
//
// What the app switcher can do for a non-admin, without any server call at
// all, is close the WINDOW — a built-in surface is a React view in that user's
// own shell, and dismissing it frees that user's memory without touching
// anyone else's session. Rows come back with Closable=false for exactly the
// kinds where the server must not be involved.
func CloseAppHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.ExecDisabled != nil && d.ExecDisabled() {
			writeProcErr(w, 503, "exec disabled by administrator", "")
			return
		}
		if !d.admin(r) {
			d.audit(r, "POST /api/proc/apps/close", "DENIED: not admin")
			writeProcErr(w, 403, "admin only", "")
			return
		}
		var req struct {
			AppID string `json:"app_id"`
			Force bool   `json:"force"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.AppID == "" {
			writeProcErr(w, 400, "app_id is required", "")
			return
		}
		if d.CloseApp == nil {
			writeProcErr(w, 503, "app control unavailable", "")
			return
		}
		d.audit(r, "POST /api/proc/apps/close", "app_id="+req.AppID+" force="+btoa(req.Force))
		if err := d.CloseApp(req.AppID, req.Force); err != nil {
			writeProcErr(w, 404, err.Error(), "")
			return
		}
		writeProcJSON(w, 200, map[string]any{
			"app_id": req.AppID, "outcome": "closed", "forced": req.Force,
		})
	}
}

// writeProcErr writes {"error":…} with an optional stable machine code.
//
// The SHAPE matters as much as the status: every /api/* service in this repo
// answers 5xx with a JSON body that parses cleanly, so a client doing
// `.then(r => r.json())` gets an object on failure and, if it then coerces
// that to a list, renders its own designed empty state. This body is
// deliberately an OBJECT and never an array, so a client that forgets to check
// r.ok cannot mistake a failure for "you have no processes".
func writeProcErr(w http.ResponseWriter, code int, msg, machineCode string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	body := map[string]string{"error": msg}
	if machineCode != "" {
		body["code"] = machineCode
	}
	json.NewEncoder(w).Encode(body)
}

func writeProcJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func btoa(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
