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
//	POST /api/assistant/execute   — run an approved proposal; body {proposal} → {executed, result}

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"vulos/backend/services/ai"
	"vulos/backend/services/assistant"
)

// assistantDeps bundles what the assistant routes need. The mail source and
// egress policy are resolved once from the environment at registration.
type assistantDeps struct {
	svc           *ai.Service
	cfg           ai.Config
	source        assistant.MailSource
	allowExternal bool
	brokerHeaders map[string]string
	index         *assistant.MailIndex // optional on-instance semantic index; nil ⇒ lexical
}

// registerAssistantRoutes wires the assistant endpoints into mux. svc/cfg are
// the same ai.Service + ai.DefaultConfig() used elsewhere in main. index is the
// optional on-instance semantic mail index (nil ⇒ lexical retrieval).
func registerAssistantRoutes(mux *http.ServeMux, svc *ai.Service, cfg ai.Config, index *assistant.MailIndex) {
	deps := assistantDeps{
		svc:           svc,
		cfg:           cfg,
		allowExternal: os.Getenv("VULOS_ASSISTANT_ALLOW_EXTERNAL") == "1",
		brokerHeaders: assistantBrokerHeaders(),
		index:         index,
	}
	// Mail read path: the local LilMail /v1 API when configured, else an
	// in-memory fixture inbox so the assistant is demoable/testable offline.
	if url := os.Getenv("VULOS_MAIL_URL"); url != "" {
		deps.source = assistant.NewLilmailSource(url)
	} else {
		deps.source = assistant.NewFixtureSource()
	}

	// mu guards deps.cfg.Tier, which the /api/assistant/tier picker mutates at
	// runtime so the operator can choose "where your AI runs" without a restart.
	var mu sync.RWMutex
	newAssistant := func() *assistant.Assistant {
		mu.RLock()
		cfg := deps.cfg
		allowExternal := deps.allowExternal
		mu.RUnlock()
		return assistant.New(deps.svc, cfg, deps.source, allowExternal).WithIndex(deps.index)
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
	// (/v1 calendar), a light recent-activity feed, and the sovereignty posture —
	// all in one round-trip. Sections fail independently into their own *_error
	// fields so Home always renders (e.g. brief shows "assistant offline", never a
	// crash). The single model call is Attention(), which goes through Guard().
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
		writeJSON(w, res)
	})

	// POST /api/assistant/execute — run a PREVIOUSLY-PROPOSED mutating action
	// AFTER the user approved it in the UI. This is the second half of the
	// confirmation round-trip; it performs a local /v1 write only (no model
	// call, no mail egress). Body: the proposal object returned by /agent.
	mux.HandleFunc("POST /api/assistant/execute", func(w http.ResponseWriter, r *http.Request) {
		if !assistantAuthed(w, r) {
			return
		}
		var p assistant.Proposal
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErr(w, 400, "invalid proposal")
			return
		}
		if strings.TrimSpace(p.Tool) == "" {
			writeErr(w, 400, "proposal.tool required")
			return
		}
		result, err := newAssistant().ExecuteProposal(r.Context(), authOf(r), p)
		if err != nil {
			assistantErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"executed": true, "result": result, "at": time.Now().UTC()})
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
	writeJSON(w, map[string]any{"answer": answer, "at": time.Now().UTC()})
}

func assistantErr(w http.ResponseWriter, err error) {
	if err == assistant.ErrEgressBlocked {
		writeErr(w, 428, err.Error()) // 428 Precondition Required: configure a local model
		return
	}
	if strings.Contains(err.Error(), "not authenticated") {
		writeErr(w, 401, "mail not authenticated")
		return
	}
	writeErr(w, 502, err.Error())
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
