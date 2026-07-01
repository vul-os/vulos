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
//	GET  /api/assistant/status    — sovereignty + mail-source state (for the UI badge)
//	POST /api/assistant/chat      — SSE stream; body {message, history?}
//	POST /api/assistant/summarize — body {scope:"inbox"|"thread", uid?, folder?}
//	POST /api/assistant/draft     — body {uid, folder?, instructions?, save?}
//	POST /api/assistant/attention — no body; prioritized triage
//	POST /api/assistant/search    — body {q}

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
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

	newAssistant := func() *assistant.Assistant {
		return assistant.New(deps.svc, deps.cfg, deps.source, deps.allowExternal).WithIndex(deps.index)
	}
	authOf := func(r *http.Request) assistant.Auth {
		return assistant.Auth{
			Cookie: r.Header.Get("Cookie"),
			Broker: deps.brokerHeaders,
			UserID: r.Header.Get("X-User-ID"),
		}
	}

	// GET /api/assistant/status — no mail content, safe pre-auth-check surface.
	mux.HandleFunc("GET /api/assistant/status", func(w http.ResponseWriter, r *http.Request) {
		a := newAssistant()
		writeJSON(w, map[string]any{
			"sovereignty":    a.Sovereignty(),
			"mail_source":    a.MailName(),
			"semantic_index": a.Indexed(),
		})
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
