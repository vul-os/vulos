package main

// routes_assistant.go — the Vulos sovereign mail assistant (the wedge).
//
// A private AI assistant that lives in the shell and reasons over the user's
// MAIL, running on the user's own instance with NO third-party data egress by
// default. Skills: summarize inbox / a thread, draft a reply, "what needs my
// attention today", and natural-language search over mail.
//
// Model calls go through services/ai (the seam the llmux gateway sits behind).
// The sovereign guarantee is enforced in services/assistant.Guard: mail content
// only reaches an on-instance (loopback/LAN/unix) model, or an endpoint the
// operator explicitly authorized via VULOS_ASSISTANT_ALLOW_EXTERNAL=1.
//
// Endpoints (all session-authed via X-User-ID set by the auth middleware):
//
//	GET  /api/assistant/status    — sovereignty tier + mail-source state (for the UI badge)
//	GET  /api/assistant/home      — Home surface aggregate: brief + agenda + activity + sovereignty
//	POST /api/assistant/tier      — operator picks the sovereignty tier; body {tier}
//	POST /api/assistant/chat      — SSE stream; body {message, history?}
//	POST /api/assistant/summarize — body {scope:"inbox"|"thread", uid?, folder?}
//	POST /api/assistant/draft     — body {uid, folder?, instructions?, save?}
//	POST /api/assistant/attention — no body; prioritized triage
//	POST /api/assistant/search    — body {q}
//	POST /api/assistant/agent     — tool-using turn; body {message, history?} → {answer|proposal, steps}
//	POST /api/assistant/agent/stream — SSE twin of /agent; streams the final answer token-by-token
//	POST /api/assistant/execute   — run an approved proposal by id; body {id} → {executed, result}
//	POST /api/assistant/triage    — direct user-initiated triage; body {message_id, action, ...}

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"vulos/backend/internal/obs"
	"vulos/backend/services/ai"
	"vulos/backend/services/assistant"
	"vulos/backend/services/files"
	"vulos/backend/services/notify"
)

// assistantDeps bundles what the assistant routes need. The mail source and
// egress policy are resolved once from the environment at registration.
type assistantDeps struct {
	model         assistant.Completer // the model seam (satisfied by *ai.Service; a fake in tests)
	cfg           ai.Config
	source        assistant.MailSource
	allowExternal bool
	brokerHeaders map[string]string
	index         *assistant.MailIndex      // optional on-instance semantic index; nil ⇒ lexical
	ledger        *assistant.ProposalLedger // server-side store of mutating proposals awaiting approval
	files         assistant.FilesSource     // optional READ-ONLY Drive seam (wave 55); nil ⇒ file tools off
	reminders     assistant.RemindersSource // optional durable reminders seam (wave 62); nil ⇒ reminder tools off
}

// registerAssistantRoutes wires the assistant endpoints into mux. svc/cfg are
// the same ai.Service + ai.DefaultConfig() used elsewhere in main. index is the
// optional on-instance semantic mail index (nil ⇒ lexical retrieval).
func registerAssistantRoutes(mux *http.ServeMux, svc *ai.Service, cfg ai.Config, index *assistant.MailIndex, filesSvc *files.Service, reminders *assistant.RemindersStore) {
	deps := assistantDeps{
		model:         svc,
		cfg:           cfg,
		allowExternal: os.Getenv("VULOS_ASSISTANT_ALLOW_EXTERNAL") == "1",
		brokerHeaders: assistantBrokerHeaders(),
		index:         index,
		ledger:        assistant.NewProposalLedger(),
	}
	// Durable REMINDERS seam (wave 62): the per-user SQLite store backs
	// list_reminders / set_reminder / cancel_reminder. nil ⇒ the reminder tools
	// degrade to "reminders unavailable" (they return an unavailable result).
	if reminders != nil {
		deps.reminders = reminders
	}
	// READ-ONLY files awareness (wave 55): wrap the OS Files service in the
	// in-process adapter so find_file/read_file can search+read the user's OWN
	// Drive, ACL-enforced by files.Service. nil svc ⇒ the seam degrades to "no
	// files available" (the file tools return an empty/unavailable result).
	if filesSvc != nil {
		deps.files = assistant.NewOSFilesSource(assistantFilesBackend{svc: filesSvc})
	}
	// Mail read path: the local LilMail /v1 API when configured, else an
	// in-memory fixture inbox so the assistant is demoable/testable offline.
	if url := os.Getenv("VULOS_MAIL_URL"); url != "" {
		deps.source = assistant.NewLilmailSource(url)
	} else {
		deps.source = assistant.NewFixtureSource()
	}
	registerAssistantRoutesWithDeps(mux, deps)
}

// registerAssistantRoutesWithDeps wires the endpoints given a fully-built deps
// bundle. Split out from registerAssistantRoutes so tests can inject a scripted
// model, a fixture mail source, and their own ledger to exercise the
// /agent→/execute round-trip and the forged/expired/cross-user rejections.
func registerAssistantRoutesWithDeps(mux *http.ServeMux, deps assistantDeps) {
	if deps.ledger == nil {
		deps.ledger = assistant.NewProposalLedger()
	}
	// mu guards deps.cfg.Tier, which the /api/assistant/tier picker mutates at
	// runtime so the operator can choose "where your AI runs" without a restart.
	var mu sync.RWMutex
	newAssistant := func() *assistant.Assistant {
		mu.RLock()
		cfg := deps.cfg
		allowExternal := deps.allowExternal
		mu.RUnlock()
		return assistant.New(deps.model, cfg, deps.source, allowExternal).
			WithIndex(deps.index).WithFiles(deps.files).WithReminders(deps.reminders)
	}
	authOf := func(r *http.Request) assistant.Auth {
		return assistant.Auth{
			Cookie: r.Header.Get("Cookie"),
			Broker: deps.brokerHeaders,
			UserID: r.Header.Get("X-User-ID"),
		}
	}

	// GET /api/assistant/status — no mail content, safe pre-auth-check surface.
	// The sovereignty block carries the honest current tier + label + the tiers
	// the operator may pick (see POST /api/assistant/tier).
	mux.HandleFunc("GET /api/assistant/status", func(w http.ResponseWriter, r *http.Request) {
		a := newAssistant()
		sv := a.Sovereignty()
		writeJSON(w, map[string]any{
			"tier":           sv.Tier,
			"label":          sv.Label,
			"sovereignty":    sv,
			"tier_options":   assistantTierOptions(),
			"mail_source":    a.MailName(),
			"semantic_index": a.Indexed(),
		})
	})

	// POST /api/assistant/tier — the operator picks the sovereignty tier the
	// endpoint is declared to sit in ("where your AI runs"). This is the config
	// knob (mirrors VULOS_AI_TIER) applied at runtime. Loopback endpoints stay
	// "local" no matter what is declared, and brokered/external still require the
	// egress opt-in — so this cannot weaken the guarantee, only label it.
	mux.HandleFunc("POST /api/assistant/tier", func(w http.ResponseWriter, r *http.Request) {
		if !assistantAuthed(w, r) {
			return
		}
		var body struct {
			Tier string `json:"tier"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		t := assistant.NormalizeTier(body.Tier)
		if t == "" {
			writeErr(w, 400, "tier must be one of local, sovereign, brokered, external")
			return
		}
		mu.Lock()
		deps.cfg.Tier = string(t)
		mu.Unlock()
		a := newAssistant()
		sv := a.Sovereignty()
		writeJSON(w, map[string]any{
			"tier":         sv.Tier,
			"label":        sv.Label,
			"sovereignty":  sv,
			"tier_options": assistantTierOptions(),
		})
	})

	// GET /api/assistant/home — the proactive Home surface aggregate: a curated
	// "what needs you today" brief (the guarded Attention skill), today's agenda
	// (/v1 calendar, with a freshness flag), calendar invites awaiting the user's
	// RSVP (wave-40, READ-ONLY), a light recent-activity feed, and the sovereignty
	// posture — all in one round-trip. Sections fail independently into their own
	// *_error fields so Home always renders (e.g. brief shows "assistant offline",
	// never a crash). The single model call is Attention(), which goes through
	// Guard(); the agenda + invites are pure on-box /v1 reads (no egress).
	mux.HandleFunc("GET /api/assistant/home", func(w http.ResponseWriter, r *http.Request) {
		if !assistantAuthed(w, r) {
			return
		}
		writeJSON(w, newAssistant().Home(r.Context(), authOf(r)))
	})

	// POST /api/assistant/summarize
	mux.HandleFunc("POST /api/assistant/summarize", func(w http.ResponseWriter, r *http.Request) {
		if !assistantAuthed(w, r) {
			return
		}
		var body struct {
			Scope  string `json:"scope"`
			UID    string `json:"uid"`
			Folder string `json:"folder"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		a := newAssistant()
		var (
			out string
			err error
		)
		if body.Scope == "thread" || body.UID != "" {
			if body.UID == "" {
				writeErr(w, 400, "uid required for thread summary")
				return
			}
			out, err = a.SummarizeThread(r.Context(), authOf(r), body.UID, body.Folder)
		} else {
			out, err = a.SummarizeInbox(r.Context(), authOf(r))
		}
		assistantRespond(w, out, err)
	})

	// POST /api/assistant/draft
	mux.HandleFunc("POST /api/assistant/draft", func(w http.ResponseWriter, r *http.Request) {
		if !assistantAuthed(w, r) {
			return
		}
		var body struct {
			UID          string `json:"uid"`
			Folder       string `json:"folder"`
			Instructions string `json:"instructions"`
			Save         bool   `json:"save"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.UID == "" {
			writeErr(w, 400, "uid required")
			return
		}
		a := newAssistant()
		var (
			out string
			err error
		)
		if body.Save {
			out, err = a.SaveDraftReply(r.Context(), authOf(r), body.UID, body.Folder, body.Instructions)
		} else {
			out, err = a.DraftReply(r.Context(), authOf(r), body.UID, body.Folder, body.Instructions)
		}
		if err != nil {
			assistantRespond(w, out, err)
			return
		}
		writeJSON(w, map[string]any{"draft": out, "saved": body.Save})
	})

	// POST /api/assistant/attention
	mux.HandleFunc("POST /api/assistant/attention", func(w http.ResponseWriter, r *http.Request) {
		if !assistantAuthed(w, r) {
			return
		}
		out, err := newAssistant().Attention(r.Context(), authOf(r))
		assistantRespond(w, out, err)
	})

	// POST /api/assistant/search
	mux.HandleFunc("POST /api/assistant/search", func(w http.ResponseWriter, r *http.Request) {
		if !assistantAuthed(w, r) {
			return
		}
		var body struct {
			Q string `json:"q"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.TrimSpace(body.Q) == "" {
			writeErr(w, 400, "q required")
			return
		}
		res, err := newAssistant().Search(r.Context(), authOf(r), body.Q)
		if err != nil {
			assistantErr(w, err)
			return
		}
		writeJSON(w, res)
	})

	// GET /api/mail/search?q=&limit= — WAVE-12: the FAST, non-LLM mail lookup
	// behind the ⌘K command palette's "Mail" section. Unlike POST
	// /api/assistant/search (which grounds an LLM answer over the hits), this
	// returns the raw matching messages directly from the mail source
	// (lilmail's /v1/search behind the box), so it's cheap enough to call live
	// on every debounced keystroke. Degrades to an empty list on any source
	// error so the palette stays responsive.
	mux.HandleFunc("GET /api/mail/search", func(w http.ResponseWriter, r *http.Request) {
		if !assistantAuthed(w, r) {
			return
		}
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			writeJSON(w, map[string]any{"messages": []assistant.Message{}})
			return
		}
		limit := 8
		if n := r.URL.Query().Get("limit"); n != "" {
			if v, err := strconv.Atoi(n); err == nil && v > 0 && v <= 25 {
				limit = v
			}
		}
		msgs, err := deps.source.Search(r.Context(), authOf(r), "", q, limit)
		if err != nil {
			// Honest degradation: report the failure but don't 500 the palette.
			writeJSON(w, map[string]any{"messages": []assistant.Message{}, "error": err.Error()})
			return
		}
		if msgs == nil {
			msgs = []assistant.Message{}
		}
		writeJSON(w, map[string]any{"messages": msgs})
	})

	// POST /api/assistant/agent — the TOOL-USING turn. The model may call
	// curated tools (search/read/draft/compose freely; send/schedule/contact/
	// triage are PROPOSED and gated). Returns either {answer, steps} or
	// {proposal, steps}. Mutating actions never execute here — the client must
	// approve a proposal and POST it back to /api/assistant/execute.
	mux.HandleFunc("POST /api/assistant/agent", func(w http.ResponseWriter, r *http.Request) {
		if !assistantAuthed(w, r) {
			return
		}
		var body struct {
			Message string       `json:"message"`
			History []ai.Message `json:"history"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.TrimSpace(body.Message) == "" {
			writeErr(w, 400, "message required")
			return
		}
		res, err := newAssistant().AgentTurn(r.Context(), authOf(r), body.Message, body.History)
		if err != nil {
			assistantErr(w, err)
			return
		}
		// SERVER-SIDE PROPOSAL LEDGER: a mutating proposal is stored server-side,
		// keyed by its opaque id and bound to this session's user, before it is
		// shown to the client. /execute later runs the SERVER-STORED args by id —
		// the client never gets to supply the args that actually run, so a forged
		// proposal cannot execute. The client still receives id + summary + args
		// (for display) exactly as before.
		if res.Proposal != nil {
			deps.ledger.Put(r.Header.Get("X-User-ID"), *res.Proposal)
			obs.AssistantProposalsPending.Set(float64(deps.ledger.Len()))
		}
		writeJSON(w, res)
	})

	// POST /api/assistant/agent/stream — the STREAMING twin of /agent. Same tool
	// loop, same confirmation gate, same up-front egress Guard; the only
	// difference is the FINAL natural-language answer is streamed token-by-token
	// as SSE so the UI feels live. Event protocol (one JSON object per SSE data:
	// frame):
	//
	//	{"type":"status","tool":"search_mail","content":"using search_mail…"}
	//	{"type":"token","content":"…partial answer…"}   (repeated)
	//	{"type":"proposal","proposal":{id,tool,summary,args,from_content,warning},"steps":[…]}
	//	{"type":"done"}                                 (terminal, success)
	//	{"type":"error","error":"…"}                    (terminal, failure)
	//
	// SECURITY (unchanged from /agent): mutating actions NEVER execute here — a
	// terminal proposal is stored SERVER-SIDE in the ledger (bound to this
	// session's user) BEFORE the client sees it, and only /execute runs it by id.
	// Guard runs before any model call, so a blocked tier streams a single error
	// event with zero model calls. Untrusted mail is wrapped as data in the loop.
	mux.HandleFunc("POST /api/assistant/agent/stream", func(w http.ResponseWriter, r *http.Request) {
		if !assistantAuthed(w, r) {
			return
		}
		var body struct {
			Message string       `json:"message"`
			History []ai.Message `json:"history"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.TrimSpace(body.Message) == "" {
			writeErr(w, 400, "message required")
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		flusher, _ := w.(http.Flusher)
		userID := r.Header.Get("X-User-ID")
		send := func(v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
			if flusher != nil {
				flusher.Flush()
			}
		}
		emit := func(ev assistant.AgentStreamEvent) {
			// SERVER-SIDE PROPOSAL LEDGER: a terminal mutating proposal is stored
			// server-side, keyed by its opaque id and bound to this session's user,
			// BEFORE it is streamed to the client — identical to POST /agent. The
			// client never supplies the args that run; /execute reads them from the
			// ledger by id, so a forged proposal cannot execute.
			if ev.Type == "proposal" && ev.Proposal != nil {
				deps.ledger.Put(userID, *ev.Proposal)
				obs.AssistantProposalsPending.Set(float64(deps.ledger.Len()))
			}
			send(ev)
		}

		err := newAssistant().AgentTurnStream(r.Context(), authOf(r), body.Message, body.History, emit)
		if err != nil {
			// Egress-blocked / retrieval / model errors: terminal error event.
			// Guard runs before any model call, so a blocked tier reaches here
			// having streamed nothing and made zero model calls.
			send(assistant.AgentStreamEvent{Type: "error", Error: err.Error()})
			return
		}
		send(assistant.AgentStreamEvent{Type: "done"})
	})

	// POST /api/assistant/execute — run a PREVIOUSLY-PROPOSED mutating action
	// AFTER the user approved it in the UI. This is the second half of the
	// confirmation round-trip; it performs a local /v1 write only (no model
	// call, no mail egress).
	//
	// SECURITY: the body is now just {"id":"prop_…"}. The action's ARGS are NOT
	// read from the request — they are looked up from the server-side ledger by
	// id (bound to this session's user, TTL-limited, single-use). A forged
	// proposal cannot execute because the client cannot fabricate a valid
	// id→args mapping. Unknown/expired/used/wrong-user ids are rejected 4xx and
	// nothing runs.
	mux.HandleFunc("POST /api/assistant/execute", func(w http.ResponseWriter, r *http.Request) {
		if !assistantAuthed(w, r) {
			return
		}
		var body struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id := strings.TrimSpace(body.ID)
		if id == "" {
			writeErr(w, 400, "id required")
			return
		}
		p, err := deps.ledger.Take(r.Header.Get("X-User-ID"), id)
		if err != nil {
			// Fail closed: unknown/expired/used → 404; cross-user → 403. Never
			// falls through to executing client-supplied args.
			if err == assistant.ErrProposalOwner {
				writeErr(w, 403, "proposal does not belong to this session")
				return
			}
			writeErr(w, 404, "proposal not found, expired, or already used")
			return
		}
		// A proposal left the ledger (approved+executed) — reflect the smaller
		// backlog in the exported gauge.
		obs.AssistantProposalsPending.Set(float64(deps.ledger.Len()))
		result, err := newAssistant().ExecuteProposal(r.Context(), authOf(r), p)
		if err != nil {
			assistantErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"executed": true, "result": result, "at": time.Now().UTC()})
	})

	// POST /api/assistant/triage — DIRECT, user-initiated triage from a surface
	// where the user clicked an explicit action on a message they can see (the
	// Home "snooze" affordance). This is NOT an LLM proposal, so it does not go
	// through the proposal ledger: it is a deterministic, session-authed mailbox
	// state change (archive/snooze/label only — it cannot send mail). Keeping it
	// separate lets /execute stay strictly ledger-only, so the dangerous
	// capability (sending mail from a forged proposal) remains impossible.
	// Body: {message_id, action, until?, label?, folder?}.
	mux.HandleFunc("POST /api/assistant/triage", func(w http.ResponseWriter, r *http.Request) {
		if !assistantAuthed(w, r) {
			return
		}
		var body struct {
			MessageID string `json:"message_id"`
			Action    string `json:"action"`
			Until     string `json:"until"`
			Label     string `json:"label"`
			Folder    string `json:"folder"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.TrimSpace(body.MessageID) == "" || strings.TrimSpace(body.Action) == "" {
			writeErr(w, 400, "message_id and action required")
			return
		}
		switch body.Action {
		case "archive", "snooze", "label": // allowlist: no send/delete here
		default:
			writeErr(w, 400, "action must be archive, snooze, or label")
			return
		}
		p := assistant.Proposal{Tool: "triage", Args: map[string]any{
			"message_id": body.MessageID,
			"action":     body.Action,
			"until":      body.Until,
			"label":      body.Label,
			"folder":     body.Folder,
		}}
		result, err := newAssistant().ExecuteProposal(r.Context(), authOf(r), p)
		if err != nil {
			assistantErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"executed": true, "result": result, "at": time.Now().UTC()})
	})

	// GET /api/assistant/reminders — the user's OWN pending reminders for the
	// shell surface (soonest first). Pure on-box, user-scoped store read: the
	// store filters by X-User-ID, so a user only ever sees their own reminders.
	// ?all=1 includes already-fired/cancelled ones.
	mux.HandleFunc("GET /api/assistant/reminders", func(w http.ResponseWriter, r *http.Request) {
		if !assistantAuthed(w, r) {
			return
		}
		includeDone := r.URL.Query().Get("all") == "1"
		rs, err := newAssistant().ListReminders(r.Context(), authOf(r), includeDone, 100)
		if err != nil {
			assistantErr(w, err)
			return
		}
		if rs == nil {
			rs = []assistant.Reminder{}
		}
		writeJSON(w, map[string]any{"reminders": rs})
	})

	// POST /api/assistant/reminders/cancel — DIRECT, user-initiated cancel/done of
	// a reminder the user can SEE in the surface (the done/cancel affordance). Like
	// /api/assistant/triage this is NOT an LLM proposal, so it does not go through
	// the proposal ledger: it is a deterministic, session-authed action on the
	// user's OWN reminder. The store scopes the cancel by X-User-ID, so a user
	// cannot cancel another's reminder even by guessing an id. Body: {id}.
	mux.HandleFunc("POST /api/assistant/reminders/cancel", func(w http.ResponseWriter, r *http.Request) {
		if !assistantAuthed(w, r) {
			return
		}
		var body struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if deps.reminders == nil {
			writeErr(w, 404, "reminders are not available")
			return
		}
		id := strings.TrimSpace(body.ID)
		if id == "" {
			writeErr(w, 400, "id required")
			return
		}
		rem, err := deps.reminders.CancelReminder(r.Header.Get("X-User-ID"), id)
		if err != nil {
			// Fail closed: unknown / not-this-user's / already-done → 404 (no oracle).
			writeErr(w, 404, "reminder not found")
			return
		}
		writeJSON(w, map[string]any{"cancelled": true, "reminder": rem, "at": time.Now().UTC()})
	})

	// POST /api/assistant/chat — SSE stream of the grounded answer.
	mux.HandleFunc("POST /api/assistant/chat", func(w http.ResponseWriter, r *http.Request) {
		if !assistantAuthed(w, r) {
			return
		}
		var body struct {
			Message string       `json:"message"`
			History []ai.Message `json:"history"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.TrimSpace(body.Message) == "" {
			writeErr(w, 400, "message required")
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		flusher, _ := w.(http.Flusher)
		send := func(v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
			if flusher != nil {
				flusher.Flush()
			}
		}

		a := newAssistant()
		err := a.ChatStream(r.Context(), authOf(r), body.Message, body.History, func(c ai.StreamChunk) {
			send(map[string]any{"content": c.Content, "done": c.Done})
		})
		if err != nil {
			// The stream may not have started (e.g. egress blocked / retrieval
			// error); emit the error as a terminal SSE event either way.
			send(map[string]any{"error": err.Error(), "done": true})
		}
	})
}

// assistantTierOptions lists the tiers the operator can pick in the UI, each
// with its honest label. "external" is intentionally omitted from the picker: it
// is the fail-closed bucket and is only ever reached via the explicit
// VULOS_ASSISTANT_ALLOW_EXTERNAL egress opt-in, not chosen as a posture.
func assistantTierOptions() []map[string]string {
	pick := []assistant.Tier{assistant.TierLocal, assistant.TierSovereign, assistant.TierBrokered}
	out := make([]map[string]string, 0, len(pick))
	for _, t := range pick {
		out = append(out, map[string]string{"tier": string(t), "label": assistant.TierLabel(t)})
	}
	return out
}

// assistantAuthed enforces a signed-in session (X-User-ID injected by the auth
// middleware). Returns false and writes 401 when absent.
func assistantAuthed(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("X-User-ID") == "" {
		writeErr(w, 401, "unauthorized")
		return false
	}
	return true
}

// assistantRespond writes {answer} or maps a known error to a status code.
func assistantRespond(w http.ResponseWriter, answer string, err error) {
	if err != nil {
		assistantErr(w, err)
		return
	}
	// A skill produced an answer, which means its up-front sovereignty Guard let
	// the model call through — count it (non-sensitive: no content, no user id).
	obs.AssistantGuardAllowedTotal.Inc()
	writeJSON(w, map[string]any{"answer": answer, "at": time.Now().UTC()})
}

func assistantErr(w http.ResponseWriter, err error) {
	if err == assistant.ErrEgressBlocked {
		// The sovereign guarantee fired: the model target was not on-instance and
		// external egress was not authorized. Count it so an operator can see the
		// Guard actively enforcing on their box.
		obs.AssistantGuardBlockedTotal.Inc()
		writeErr(w, 428, err.Error()) // 428 Precondition Required: configure a local model
		return
	}
	if strings.Contains(err.Error(), "not authenticated") {
		writeErr(w, 401, "mail not authenticated")
		return
	}
	writeErr(w, 502, err.Error())
}

// assistantFilesBackend adapts *files.Service to assistant.FilesBackend — the
// minimal READ-ONLY surface (Search + ReadContent) the assistant's find_file /
// read_file tools use. It is a thin translation layer: it converts files.Node →
// assistant.FileNode and files.Service.GetContent → ReadContent, and adds NO
// authority of its own — the ACL is enforced inside files.Service (Search only
// returns the user's own + shared-with-them nodes; GetContent 403s any node the
// user cannot read). It exposes NO write/delete/share method, so the assistant
// cannot mutate files through this seam.
type assistantFilesBackend struct {
	svc *files.Service
}

func (b assistantFilesBackend) Search(userID, query string, limit int) ([]assistant.FileNode, error) {
	nodes, err := b.svc.Search(userID, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]assistant.FileNode, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, toAssistantFileNode(n))
	}
	return out, nil
}

func (b assistantFilesBackend) ReadContent(ctx context.Context, userID, nodeID string) (io.ReadCloser, assistant.FileNode, int64, error) {
	rc, n, size, err := b.svc.GetContent(ctx, userID, nodeID)
	if err != nil {
		return nil, assistant.FileNode{}, 0, err
	}
	return rc, toAssistantFileNode(n), size, nil
}

func toAssistantFileNode(n *files.Node) assistant.FileNode {
	if n == nil {
		return assistant.FileNode{}
	}
	return assistant.FileNode{
		ID:          n.ID,
		Name:        n.Name,
		Path:        n.Path,
		IsDir:       n.IsDir,
		Size:        n.Size,
		ContentType: n.ContentType,
		UpdatedAt:   n.UpdatedAt,
	}
}

// reminderNotifier adapts *notify.Service to assistant.Notifier — the minimal
// seam the reminder scheduler uses to raise an OS notification when a reminder
// fires. It carries NO authority of its own: it just formats the fired
// reminder's (bounded, user-authored) text into a notification. The reminder
// text is placed in the notification Body as plain text (the notify layer and
// the shell's NotificationCenter render it escaped, never as HTML), and the
// target user id is carried in Subtype so a per-user client can route it.
type reminderNotifier struct {
	svc *notify.Service
}

func (n reminderNotifier) NotifyReminder(userID, reminderID, text string) {
	if n.svc == nil {
		return
	}
	n.svc.SendNotification(notify.Notification{
		Title:   "Reminder",
		Body:    text,
		Level:   notify.LevelInfo,
		Source:  "assistant",
		Type:    notify.TypeAlert,
		Subtype: "reminder:" + userID,
		// UserID scopes this notification to the reminder's owner: the reminder
		// text (the user's own words, or content pulled from an email via an
		// approved set_reminder) is PRIVATE and must not be broadcast to other
		// accounts sharing the box. Without this, the per-user isolation the
		// reminders store enforces would be undone at the notification hop
		// (NOTIF-USER-SCOPE). Delivery/list are then filtered by this id.
		UserID:   userID,
		Priority: notify.PriorityHigh,
	})
}

// assistantBrokerHeaders builds the X-Vulos-Mail-* header set for LilMail's
// CP-brokered credential mode from the environment. Empty when unconfigured, in
// which case LilMail falls back to session-cookie auth (forwarded per request).
func assistantBrokerHeaders() map[string]string {
	secret := os.Getenv("VULOS_MAIL_BROKER_SECRET")
	if secret == "" {
		return nil
	}
	h := map[string]string{"X-Vulos-Mail-Secret": secret}
	set := func(k, env string) {
		if v := os.Getenv(env); v != "" {
			h[k] = v
		}
	}
	set("X-Vulos-Mail-Provider", "VULOS_MAIL_BROKER_PROVIDER")
	set("X-Vulos-Mail-Email", "VULOS_MAIL_BROKER_EMAIL")
	set("X-Vulos-Mail-Username", "VULOS_MAIL_BROKER_USERNAME")
	set("X-Vulos-Mail-Auth", "VULOS_MAIL_BROKER_AUTH")
	set("X-Vulos-Mail-Imap-Host", "VULOS_MAIL_BROKER_IMAP_HOST")
	set("X-Vulos-Mail-Imap-Port", "VULOS_MAIL_BROKER_IMAP_PORT")
	set("X-Vulos-Mail-Smtp-Host", "VULOS_MAIL_BROKER_SMTP_HOST")
	set("X-Vulos-Mail-Smtp-Port", "VULOS_MAIL_BROKER_SMTP_PORT")
	return h
}
