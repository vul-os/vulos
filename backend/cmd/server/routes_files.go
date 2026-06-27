package main

// routes_files.go — FILES-FOUNDATION HTTP control plane (Phase 1).
//
// Session-authed OS Files API. Identity ALWAYS comes from the session-derived
// X-User-ID header injected by auth.Handler.Middleware — never from the request
// body. Each handler delegates to services/files, which enforces the ACL and
// (for grants) calls the storage broker ONLY after authorization. Unauthorized
// callers receive 403/404 and NO grant.
//
// Endpoints (all under /api/files/):
//   GET    /api/files/list?parent=<id>          list folder (root if parent empty)
//   POST   /api/files/folder                    create folder
//   POST   /api/files/upload-grant              create/locate file + write grant
//   POST   /api/files/download-grant            read grant for a file
//   POST   /api/files/commit                    finalize a version after upload
//   POST   /api/files/move                      move/rename
//   POST   /api/files/delete                    soft-delete
//   GET    /api/files/versions?node=<id>        list versions
//   POST   /api/files/share                     grant a user a role
//   POST   /api/files/unshare                   revoke a user's role
//   GET    /api/files/shares?node=<id>          list a node's ACL entries
//   GET    /api/files/shared-with-me            nodes shared with the caller
//   POST   /api/files/share-link               create an expiring link
//   POST   /api/files/share-link/revoke        revoke a link
//   GET    /api/files/share-links?node=<id>    list a node's links
//   POST   /api/files/redeem-link              redeem a link → read grant

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"vulos/backend/services/files"
)

// maxUploadBytes caps a single OS-mediated content upload (data plane). Large
// objects in cloud mode go direct-to-bucket via the presigned grant and never
// pass through here.
const maxUploadBytes = 512 << 20 // 512 MiB

// filesGrantTTL bounds how long a minted file grant lives. Short by design.
const filesGrantTTL = 15 * time.Minute

// registerFilesRoutes wires the Files control plane onto mux. svc may be nil
// (e.g. DB init failed); in that case every endpoint returns 503 so the rest of
// the OS still boots.
func registerFilesRoutes(mux *http.ServeMux, svc *files.Service) {
	guard := func(h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if svc == nil {
				writeErr(w, 503, "files service unavailable")
				return
			}
			userID := r.Header.Get("X-User-ID")
			if userID == "" {
				writeErr(w, 401, "unauthorized")
				return
			}
			h(w, r, userID)
		}
	}

	mux.HandleFunc("GET /api/files/list", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		nodes, err := svc.List(uid, r.URL.Query().Get("parent"))
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"nodes": nodes})
	}))

	mux.HandleFunc("POST /api/files/folder", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			ParentID string `json:"parent_id"`
			Name     string `json:"name"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		n, err := svc.CreateFolder(uid, req.ParentID, req.Name)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, n)
	}))

	mux.HandleFunc("POST /api/files/upload-grant", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			ParentID    string `json:"parent_id"`
			Name        string `json:"name"`
			ContentType string `json:"content_type"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		n, grant, err := svc.UploadGrant(r.Context(), uid, req.ParentID, req.Name, req.ContentType, filesGrantTTL)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"node": n, "grant": grant})
	}))

	mux.HandleFunc("POST /api/files/download-grant", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			NodeID string `json:"node_id"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		n, grant, err := svc.DownloadGrant(r.Context(), uid, req.NodeID, filesGrantTTL)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"node": n, "grant": grant})
	}))

	// OS-mediated data plane (PHASE-2A). The Files app uploads/downloads bytes
	// here when a direct presigned PUT/GET is unavailable — chiefly STANDALONE
	// (local-FS) mode, where the browser cannot touch a filesystem path, and the
	// object-scoped STS case. Both are ACL-gated by the service before any byte
	// moves. In cloud-with-presigned mode the app talks to the bucket directly and
	// these never run.
	mux.HandleFunc("PUT /api/files/content", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		nodeID := r.URL.Query().Get("node")
		if nodeID == "" {
			writeErr(w, 400, "node required")
			return
		}
		body := http.MaxBytesReader(w, r.Body, maxUploadBytes)
		defer body.Close()
		size := r.ContentLength
		etag, err := svc.PutContent(r.Context(), uid, nodeID, body, size, r.Header.Get("Content-Type"))
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"etag": etag, "size": size})
	}))

	mux.HandleFunc("GET /api/files/content", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		rc, n, size, err := svc.GetContent(r.Context(), uid, r.URL.Query().Get("node"))
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		defer rc.Close()
		if n.ContentType != "" {
			w.Header().Set("Content-Type", n.ContentType)
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		if size >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		}
		w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(n.Name)+"\"")
		io.Copy(w, rc)
	}))

	mux.HandleFunc("POST /api/files/commit", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			NodeID      string `json:"node_id"`
			Size        int64  `json:"size"`
			ContentType string `json:"content_type"`
			ETag        string `json:"etag"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		v, err := svc.Commit(uid, req.NodeID, req.Size, req.ContentType, req.ETag)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, v)
	}))

	mux.HandleFunc("POST /api/files/move", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			NodeID      string `json:"node_id"`
			NewParentID string `json:"new_parent_id"`
			NewName     string `json:"new_name"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		n, err := svc.Move(r.Context(), uid, req.NodeID, req.NewParentID, req.NewName)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, n)
	}))

	mux.HandleFunc("POST /api/files/delete", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			NodeID string `json:"node_id"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		if err := svc.Delete(uid, req.NodeID); err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]string{"status": "deleted"})
	}))

	mux.HandleFunc("GET /api/files/versions", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		vs, err := svc.Versions(uid, r.URL.Query().Get("node"))
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"versions": vs})
	}))

	mux.HandleFunc("POST /api/files/share", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			NodeID      string `json:"node_id"`
			PrincipalID string `json:"principal_id"`
			Role        string `json:"role"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		e, err := svc.Share(uid, req.NodeID, req.PrincipalID, files.Role(req.Role))
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, e)
	}))

	mux.HandleFunc("POST /api/files/unshare", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			NodeID      string `json:"node_id"`
			PrincipalID string `json:"principal_id"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		if err := svc.Unshare(uid, req.NodeID, req.PrincipalID); err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]string{"status": "revoked"})
	}))

	mux.HandleFunc("GET /api/files/shares", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		acls, err := svc.ListShares(uid, r.URL.Query().Get("node"))
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"shares": acls})
	}))

	mux.HandleFunc("GET /api/files/shared-with-me", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		nodes, err := svc.SharedWithMe(uid)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"nodes": nodes})
	}))

	mux.HandleFunc("POST /api/files/share-link", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			NodeID     string `json:"node_id"`
			Role       string `json:"role"`
			TTLSeconds int64  `json:"ttl_seconds"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		l, err := svc.CreateLink(uid, req.NodeID, files.Role(req.Role), time.Duration(req.TTLSeconds)*time.Second)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, l)
	}))

	mux.HandleFunc("POST /api/files/share-link/revoke", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			Token string `json:"token"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		if err := svc.RevokeLink(uid, req.Token); err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]string{"status": "revoked"})
	}))

	mux.HandleFunc("GET /api/files/share-links", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		links, err := svc.ListLinks(uid, r.URL.Query().Get("node"))
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"links": links})
	}))

	mux.HandleFunc("POST /api/files/redeem-link", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			Token string `json:"token"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		n, grant, err := svc.RedeemLink(r.Context(), uid, req.Token, filesGrantTTL)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"node": n, "grant": grant})
	}))
}

// sanitizeFilename strips characters unsafe for a Content-Disposition filename
// (quotes, path separators, control bytes) so the header cannot be broken or
// used for traversal. Names are already length/traversal-validated server-side.
func sanitizeFilename(name string) string {
	out := make([]rune, 0, len(name))
	for _, c := range name {
		if c < 0x20 || c == '"' || c == '\\' || c == '/' {
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return "download"
	}
	return string(out)
}

// decodeJSON reads a small JSON body into v, writing a 400 on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeJSONLimited(w, r, v, 64<<10)
}

// decodeJSONLimited reads a JSON body capped at max bytes into v, 400 on failure.
func decodeJSONLimited(w http.ResponseWriter, r *http.Request, v any, max int64) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, max)).Decode(v); err != nil {
		writeErr(w, 400, "invalid request body")
		return false
	}
	return true
}

// writeFilesErr maps service sentinel errors to HTTP status codes. Forbidden and
// not-found both surface as 404 for read-ish access denials would leak existence;
// here we keep 403 for explicit permission failures and 404 for missing nodes,
// matching the rest of the OS API (IDOR handlers 404 on cross-user reads).
func writeFilesErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, files.ErrNotFound):
		writeErr(w, 404, "not found")
	case errors.Is(err, files.ErrForbidden):
		writeErr(w, 403, "forbidden")
	case errors.Is(err, files.ErrLinkInactive):
		writeErr(w, 410, "share link expired or revoked")
	case errors.Is(err, files.ErrCapability):
		writeErr(w, 403, "invalid or expired capability")
	case errors.Is(err, files.ErrPeerUnavailable):
		writeErr(w, 503, "peer-share unavailable")
	case errors.Is(err, files.ErrInvalid):
		writeErr(w, 400, err.Error())
	default:
		writeErr(w, 500, "internal error")
	}
}
