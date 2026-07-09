package main

// routes_models.go — the private-AI model-management surface (owner-only).
//
// This wires the on-box model manager (services/models) plus the llmux chat
// model list into a small owner-gated API the shell's Model Management settings
// panel consumes. It is the operator's window into "what AI is actually
// installed on my box" and the place to INSTALL the real tokenizer.json (or a
// model) that upgrades RAG from the degraded FNV fallback to genuine semantic
// retrieval.
//
// Endpoints (ALL owner/admin-gated — see requireOwner):
//
//	GET  /api/models              — installed local embedding models + RAG mode +
//	                                the chat models llmux exposes (best-effort)
//	POST /api/models/import       — install a validated .onnx or tokenizer.json
//	                                (multipart form: field "kind", file "artifact")
//	POST /api/models/download     — install a CURATED, PINNED catalog model in one
//	                                click (JSON body: {"id": "<catalog id>"})
//
// SECURITY:
//   - requireOwner enforces admin-only (the OS "owner"). X-User-ID is set only by
//     the auth middleware AFTER it strips any client-supplied copy, so this is a
//     real session identity, not a spoofable header.
//   - Import is bounded (per-kind byte cap), path-traversal-free (destination is
//     a fixed allowlisted filename, never client-derived), and content-sniffed.
//   - Download takes a catalog ID, NEVER a URL: the only addresses ever fetched
//     are the hardcoded, https + host-pinned catalog URLs (no SSRF surface). Each
//     artifact is SHA-256-verified fail-closed, size-bounded, content-sniffed and
//     atomically installed — identical guarantees to import, plus integrity.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"vulos/backend/internal/llmuxclient"
	"vulos/backend/internal/obs"
	"vulos/backend/services/models"
)

// llmuxModelsTimeout bounds the best-effort chat-model fetch so a slow/hung
// gateway can never stall the model-management page.
func llmuxModelsTimeout() time.Duration { return 5 * time.Second }

// isOwner reports whether the given OS user id is the box owner/admin. It
// mirrors the inline `authStore.GetProfile(id).Role != RoleAdmin` gate used
// throughout main.go, passed in as a func so this file needs no auth-store
// dependency and tests can inject their own predicate.
type isOwner func(userID string) bool

// registerModelRoutes wires the model-management endpoints. mgr manages the
// on-box embedding-model dir; llmux (may be nil) is used to list chat models;
// owner gates every route to the box owner/admin.
func registerModelRoutes(mux *http.ServeMux, mgr *models.Manager, llmux *llmuxclient.Client, owner isOwner) {
	gate := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !owner(r.Header.Get("X-User-ID")) {
				writeErr(w, 403, "owner only")
				return
			}
			h(w, r)
		}
	}

	// GET /api/models — the full model-management view.
	mux.HandleFunc("GET /api/models", gate(func(w http.ResponseWriter, r *http.Request) {
		listing, err := mgr.List()
		if err != nil {
			writeErr(w, 500, "list models: "+err.Error())
			return
		}
		// Keep the exported RAG-mode gauge in step with what the operator sees.
		obs.SetRAGMode(listing.RAGMode)

		resp := map[string]any{
			"embeddings": listing,
			// chat_models is best-effort: llmux owns chat model routing/provider
			// management, so we proxy its /v1/models list. A nil/unconfigured
			// gateway yields chat_models=null + a chat_models_error note, never a
			// hard failure — the embedding manager still renders.
			"chat_models": nil,
		}
		if llmux != nil {
			ctx, cancel := context.WithTimeout(r.Context(), llmuxModelsTimeout())
			defer cancel()
			if raw, mErr := llmux.Models(ctx); mErr != nil {
				resp["chat_models_error"] = "llmux gateway unavailable"
			} else {
				resp["chat_models"] = json.RawMessage(raw)
			}
		} else {
			resp["chat_models_error"] = "no llmux gateway configured (LLMUX_URL unset)"
		}
		writeJSON(w, resp)
	}))

	// POST /api/models/import — install a validated artifact.
	//
	// multipart/form-data:
	//   kind:     "model" | "tokenizer"
	//   artifact: the file bytes
	mux.HandleFunc("POST /api/models/import", gate(func(w http.ResponseWriter, r *http.Request) {
		// Bound the whole request body defensively before multipart parsing so a
		// malicious upload can't exhaust memory/disk during form parsing. The
		// per-kind cap inside models.Import is the authoritative limit; this outer
		// bound is model-cap + slack for multipart framing.
		r.Body = http.MaxBytesReader(w, r.Body, models.MaxModelBytes+(8<<20))

		if err := r.ParseMultipartForm(8 << 20); err != nil {
			writeErr(w, 400, "invalid multipart form")
			return
		}
		defer func() {
			if r.MultipartForm != nil {
				_ = r.MultipartForm.RemoveAll()
			}
		}()

		kind := r.FormValue("kind")
		if kind != models.ArtifactModel && kind != models.ArtifactTokenizer {
			writeErr(w, 400, `kind must be "model" or "tokenizer"`)
			return
		}

		file, _, err := r.FormFile("artifact")
		if err != nil {
			writeErr(w, 400, "artifact file required")
			return
		}
		defer file.Close()

		res, err := mgr.Import(kind, file)
		if err != nil {
			switch err {
			case models.ErrArtifactTooLarge:
				writeErr(w, 413, "artifact too large")
			case models.ErrBadArtifact:
				writeErr(w, 400, "artifact failed validation (not a valid "+kind+")")
			default:
				// Distinguish a validation failure (wrapped ErrBadArtifact) from an
				// internal error so the operator gets an actionable message.
				if isBadArtifact(err) {
					writeErr(w, 400, err.Error())
				} else {
					writeErr(w, 500, "import failed: "+err.Error())
				}
			}
			return
		}

		// Re-derive RAG mode after an import so the gauge + response reflect a
		// freshly-installed tokenizer immediately.
		if listing, lErr := mgr.List(); lErr == nil {
			obs.SetRAGMode(listing.RAGMode)
			writeJSON(w, map[string]any{"imported": res, "embeddings": listing})
			return
		}
		writeJSON(w, map[string]any{"imported": res})
	}))

	// POST /api/models/download — one-click install of a CURATED, PINNED model.
	//
	// JSON body: {"id": "<catalog id>"}
	//
	// SECURITY: the body carries ONLY a catalog id, never a URL. The id is looked
	// up in the hardcoded catalog (models.CatalogEntryByID); the only URLs fetched
	// are the pinned, https + host-pinned catalog URLs — there is no arbitrary-URL
	// / SSRF surface. Each fetched artifact is SHA-256-verified fail-closed,
	// size-bounded, content-sniffed and atomically installed.
	mux.HandleFunc("POST /api/models/download", gate(func(w http.ResponseWriter, r *http.Request) {
		// Small JSON body — a catalog id is a short string; cap it hard.
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
			writeErr(w, 400, `body must be {"id": "<catalog model id>"}`)
			return
		}

		// Verify the id is in the curated catalog BEFORE doing any work, so an
		// unknown id is a clean 404 rather than an opaque download failure.
		if _, ok := models.CatalogEntryByID(req.ID); !ok {
			writeErr(w, 404, "unknown catalog model id (see GET /api/models → catalog)")
			return
		}

		// Bound the whole download generously; individual artifacts are bounded
		// per-kind inside the manager.
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
		defer cancel()

		res, err := mgr.Download(ctx, req.ID, nil)
		if err != nil {
			switch {
			case errors.Is(err, models.ErrUnknownModel):
				writeErr(w, 404, "unknown catalog model id")
			case errors.Is(err, models.ErrChecksumMismatch):
				// Fail-closed: the fetched bytes did not match the pinned digest.
				writeErr(w, 502, "download failed integrity check (checksum mismatch) — nothing was installed")
			case errors.Is(err, models.ErrArtifactTooLarge):
				writeErr(w, 502, "download exceeded its size bound — nothing was installed")
			case errors.Is(err, models.ErrBadDownloadURL):
				writeErr(w, 500, "catalog misconfiguration: download URL not allowed")
			case isBadArtifact(err):
				writeErr(w, 502, "downloaded artifact failed validation — nothing was installed")
			default:
				writeErr(w, 502, "download failed: "+err.Error())
			}
			return
		}

		// Re-derive RAG mode so the gauge + response reflect the fresh install
		// (a full download lands model + tokenizer → semantic).
		obs.SetRAGMode(res.Listing.RAGMode)
		writeJSON(w, map[string]any{"downloaded": res, "embeddings": res.Listing})
	}))
}

// isBadArtifact reports whether err wraps models.ErrBadArtifact.
func isBadArtifact(err error) bool {
	return errors.Is(err, models.ErrBadArtifact)
}
