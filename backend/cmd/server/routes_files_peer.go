package main

// routes_files_peer.go — FILES PHASE 2B HTTP surface for OS Peer-Share
// (Mechanism B, bucket-less box-to-box).
//
// Two classes of route:
//
//   - Session-authed (identity from X-User-ID, like the rest of /api/files):
//       POST /api/files/peer/issue        issue a signed capability + link
//       POST /api/files/peer/revoke       revoke an issued capability
//       GET  /api/files/peer/shares       list capabilities on a node
//       POST /api/files/peer/redeem       redeem a pasted capability link
//       GET  /api/files/peer/received     list redeemed items
//       GET  /api/files/peer/received/get stream a redeemed item's bytes
//       POST /api/files/peer/save         promote a redeemed item into Drive
//
//   - PUBLIC (NO session — the capability + a recipient-signed fetch proof ARE
//     the authentication; this is the box-to-box reach endpoint a remote OS
//     calls). Registered separately and listed in auth.publicPaths:
//       POST /api/files/peer/serve        stream bytes for a verified capability

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vulos/backend/services/files"
)

// requestBaseURL reconstructs this box's externally reachable base URL from the
// issuing request, honouring reverse-proxy headers. It is embedded into issued
// capabilities so the recipient knows where to stream the bytes from.
func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = strings.TrimSpace(strings.Split(p, ",")[0])
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host = strings.TrimSpace(strings.Split(h, ",")[0])
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

// registerFilesPeerRoutes wires the session-authed peer-share endpoints. svc may
// be nil (Files disabled) — every endpoint then returns 503.
func registerFilesPeerRoutes(mux *http.ServeMux, svc *files.Service) {
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

	mux.HandleFunc("POST /api/files/peer/issue", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			NodeID     string `json:"node_id"`
			Access     string `json:"access"`
			Recipient  string `json:"recipient"`
			TTLSeconds int64  `json:"ttl_seconds"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		ownerAddr := requestBaseURL(r)
		cap, link, err := svc.IssueCapability(uid, req.NodeID, files.Role(req.Access), req.Recipient, ownerAddr, time.Duration(req.TTLSeconds)*time.Second)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"capability": cap, "link": link})
	}))

	mux.HandleFunc("POST /api/files/peer/revoke", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			ID string `json:"id"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		if err := svc.RevokePeerShare(uid, req.ID); err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]string{"status": "revoked"})
	}))

	mux.HandleFunc("GET /api/files/peer/shares", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		shares, err := svc.ListPeerShares(uid, r.URL.Query().Get("node"))
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"shares": shares})
	}))

	mux.HandleFunc("POST /api/files/peer/redeem", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			Link string `json:"link"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		item, err := svc.RedeemCapability(r.Context(), uid, req.Link)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, item)
	}))

	mux.HandleFunc("GET /api/files/peer/received", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		items, err := svc.ListReceived(uid)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"items": items})
	}))

	mux.HandleFunc("GET /api/files/peer/received/get", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		rc, item, err := svc.GetReceived(uid, r.URL.Query().Get("id"))
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		defer rc.Close()
		name := item.Name
		if item.IsDir {
			name += ".tar"
			w.Header().Set("Content-Type", "application/x-tar")
		} else if item.ContentType != "" {
			w.Header().Set("Content-Type", item.ContentType)
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(name)+"\"")
		io.Copy(w, rc)
	}))

	mux.HandleFunc("POST /api/files/peer/save", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			ID       string `json:"id"`
			ParentID string `json:"parent_id"`
			Name     string `json:"name"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		n, err := svc.SaveReceivedToDrive(r.Context(), uid, req.ID, req.ParentID, req.Name)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, n)
	}))

	// GET /api/files/peer/folder-tar?node=<id> — WAVE-7 sealed-FOLDER source.
	//
	// Streams a tar archive of the owner's folder subtree so the sharer's CLIENT can
	// SEAL it (VSEAL1) for a content-blind remote share (the client packs the tar
	// with metadata is_dir=true, seals to the recipient's content key, and posts the
	// ciphertext to issue-sealed). The relaying cell never sees the tar plaintext.
	// Session-authed (owner/viewer); the node must be a folder.
	mux.HandleFunc("GET /api/files/peer/folder-tar", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		rc, err := svc.FolderTar(r.Context(), uid, r.URL.Query().Get("node"))
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", "application/x-tar")
		io.Copy(w, rc)
	}))

	// POST /api/files/peer/issue-sealed — WAVE-3 CONTENT-BLIND remote share.
	//
	// The sharer's CLIENT seals the file (src/lib/contentSeal.js) to the recipient's
	// PUBLISHED content key and posts it here as multipart/form-data:
	//   fields: node_id, email, access(=viewer|editor), ttl_seconds
	//   file:   sealed   (the VSEAL1 envelope bytes)
	// The server resolves the recipient, verifies the seal is addressed to their
	// published key, mints a SEALED capability, and delivers it to the recipient's
	// server intake. FAIL CLOSED: a recipient with no published content key is
	// refused (ErrRecipientNoContentKey) — never a plaintext fallback.
	mux.HandleFunc("POST /api/files/peer/issue-sealed", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		// Cap the sealed upload generously (whole-file / sealed folder tar).
		r.Body = http.MaxBytesReader(w, r.Body, 256<<20)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeErr(w, 400, "invalid multipart form")
			return
		}
		nodeID := r.FormValue("node_id")
		email := r.FormValue("email")
		access := files.Role(r.FormValue("access"))
		var ttl time.Duration
		if v := r.FormValue("ttl_seconds"); v != "" {
			if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
				ttl = time.Duration(secs) * time.Second
			}
		}
		f, _, err := r.FormFile("sealed")
		if err != nil {
			writeErr(w, 400, "missing 'sealed' file part")
			return
		}
		defer f.Close()
		res, err := svc.ShareSealedByEmail(r.Context(), uid, nodeID, email, access, requestBaseURL(r), ttl, f)
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, res)
	}))

	// POST /api/files/peer/receive — B→A bridge intake for OFF-BOX redemption.
	//
	// The Vulos cell redeems a peershare capability on an account's behalf (it
	// custodies the account's cloud-home key, signs the fetch proof, and PULLs the
	// bytes from the sharer's /api/files/peer/serve). It then posts the redeemed
	// bytes HERE so they land directly in the account's Drive on the cell's own
	// Files service — no local RedeemCapability/ReceivedItem round-trip. The
	// recipient account is the X-User-ID (the cell sets it to the account ID). The
	// body matches cloudhome.httpDriveStager exactly:
	//   { parent_id, name, content_type, is_dir, bytes }
	// where bytes is the file content (or, for is_dir, the tar archive the serve
	// endpoint streams for a folder). base64 is the JSON wire form of []byte.
	mux.HandleFunc("POST /api/files/peer/receive", guard(func(w http.ResponseWriter, r *http.Request, uid string) {
		var req struct {
			ParentID    string `json:"parent_id"`
			Name        string `json:"name"`
			ContentType string `json:"content_type"`
			IsDir       bool   `json:"is_dir"`
			Bytes       []byte `json:"bytes"`
		}
		// Generous cap: a received document can be large (whole-file / folder tar).
		if !decodeJSONLimited(w, r, &req, 256<<20) {
			return
		}
		n, err := svc.SaveBytesToDrive(r.Context(), uid, req.ParentID, req.Name, req.ContentType, req.IsDir, bytes.NewReader(req.Bytes))
		if err != nil {
			writeFilesErr(w, err)
			return
		}
		writeJSON(w, n)
	}))
}

// registerFilesPeerServe wires the PUBLIC box-to-box serve endpoint. It performs
// NO session check: the capability and a recipient-signed fetch proof inside the
// body are the authentication, fully re-verified by the service. The path is in
// auth.publicPaths so the auth middleware does not 401 a remote box.
func registerFilesPeerServe(mux *http.ServeMux, svc *files.Service) {
	mux.HandleFunc("POST "+files.PeerServePath, func(w http.ResponseWriter, r *http.Request) {
		if svc == nil {
			writeErr(w, 503, "files service unavailable")
			return
		}
		var req files.PeerFetchRequest
		// Capability + proof are small; cap the JSON envelope tightly.
		if !decodeJSONLimited(w, r, &req, 256<<10) {
			return
		}
		rc, size, cap, err := svc.ServeCapability(r.Context(), req)
		if err != nil {
			// Coarse mapping: anything not 503 is a flat 403 with no detail so a
			// remote box cannot probe WHY a capability was refused.
			if err == files.ErrPeerUnavailable {
				writeErr(w, 503, "peer-share unavailable")
				return
			}
			writeErr(w, 403, "forbidden")
			return
		}
		defer rc.Close()
		if cap.ContentType != "" && !cap.IsDir {
			w.Header().Set("Content-Type", cap.ContentType)
		} else if cap.IsDir {
			w.Header().Set("Content-Type", "application/x-tar")
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
		}
		if size >= 0 {
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		}
		io.Copy(w, rc)
	})
}
