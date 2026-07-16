// files.go — the cloud Files (Drive) control plane.
//
// Routes registered:
//
//	GET  /api/files/list?parent=<node-id>   — list a folder ("" = Drive root)
//	POST /api/files/folder                  — create a folder (metadata only)
//	POST /api/files/move                    — move and/or rename (re-keys bytes)
//	POST /api/files/delete                  — soft delete
//	POST /api/files/upload-grant            — find-or-create a file + presigned PUT
//	POST /api/files/commit                  — record a version after the bytes land
//	POST /api/files/download-grant          — presigned GET
//	GET  /api/files/versions?node=<node-id> — version history, newest first
//
// IDENTITY. Every route resolves the caller with auth.RequireSession, and only
// that. The OS's Files control plane trusts an X-User-ID header because its own
// middleware injects it; on the CP an inbound header is attacker-controlled, so
// none is read. RequireSession also rejects the app-identity credential class
// outright (SECURITY-C1), which is what keeps a proxied app backend out of a
// Drive that spans every app's prefix — this is deliberately NOT storageCaller
// (routes_storage.go), which accepts app tokens by design.
//
// SELF-ONLY. The CP has no ACLs and no sharing: internal/files re-checks that
// every node it touches is owned by the session user, and the root listing is
// owner-filtered in SQL.
//
// BYTES. The browser PUTs/GETs the account's unified bucket directly with a
// short-lived presigned grant; bytes never transit the CP. The bucket must
// therefore expose ETag to the Workspace origin (see the CORS policy note in
// DEPLOY-CHECKLIST.md).
//
// 404/503 ARE LOAD-BEARING to the Workspace client: either flips the whole
// surface to a calm "Files isn't available" screen. So they are emitted ONLY for
// a genuinely missing node (404) or a Files service that failed to open (503) —
// never for a working-but-empty Drive, which is always 200 with an empty list.
package cproutes

import (
	"errors"
	"net/http"
	"time"

	"github.com/vul-os/vulos-management/pkg/billingport"
	"github.com/vul-os/vulos-management/pkg/files"
	"github.com/vul-os/vulos-management/pkg/httpx"
	"github.com/vul-os/vulos-management/pkg/storage"
)

// filesGrantTTL bounds how long a minted Files grant lives. Short by design —
// the client re-requests on expiry.
const filesGrantTTL = 15 * time.Minute

// RegisterFiles wires the Files control plane onto mux. svc may be nil
// (the DB failed to open), in which case every route answers 503.
//
// COORDINATOR: sh is the *storageHandlers value from routes_storage.go (borrows
// the storage plane's quota / hosting-kind / bucket-ownership gates). That type
// is owned by the storage route set — unify once routes_storage.go lands in
// cproutes.
func RegisterFiles(mux *http.ServeMux, sh *storageHandlers, svc *files.Service) {
	h := &filesHandlers{sh: sh, svc: svc}

	mux.HandleFunc("GET /api/files/list", h.list)
	mux.HandleFunc("POST /api/files/folder", h.createFolder)
	mux.HandleFunc("POST /api/files/move", h.move)
	mux.HandleFunc("POST /api/files/delete", h.delete)
	mux.HandleFunc("POST /api/files/upload-grant", h.uploadGrant)
	mux.HandleFunc("POST /api/files/commit", h.commit)
	mux.HandleFunc("POST /api/files/download-grant", h.downloadGrant)
	mux.HandleFunc("GET /api/files/versions", h.versions)
}

// filesHandlers groups the Files HTTP handlers. It borrows storageHandlers for
// the storage plane's own gates (quota, hosting kind, bucket ownership) rather
// than restating them.
type filesHandlers struct {
	sh  *storageHandlers // COORDINATOR: needs storageHandlers (routes_storage.go)
	svc *files.Service
}

// caller resolves the session user, or writes the 401/403/503 and returns "".
func (h *filesHandlers) caller(w http.ResponseWriter, r *http.Request) string {
	if h.svc == nil {
		httpx.Err(w, http.StatusServiceUnavailable, "files service unavailable")
		return ""
	}
	u := h.sh.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return ""
	}
	return u.ID
}

// ---------------------------------------------------------------------------
// GET /api/files/list?parent=
// ---------------------------------------------------------------------------

func (h *filesHandlers) list(w http.ResponseWriter, r *http.Request) {
	userID := h.caller(w, r)
	if userID == "" {
		return
	}
	nodes, err := h.svc.List(r.Context(), userID, r.URL.Query().Get("parent"))
	if err != nil {
		h.mapErr(w, err)
		return
	}
	httpx.JSON(w, map[string]any{"nodes": nodes})
}

// ---------------------------------------------------------------------------
// POST /api/files/folder
// ---------------------------------------------------------------------------

type filesFolderRequest struct {
	ParentID string `json:"parent_id"`
	Name     string `json:"name"`
}

func (h *filesHandlers) createFolder(w http.ResponseWriter, r *http.Request) {
	userID := h.caller(w, r)
	if userID == "" {
		return
	}
	var req filesFolderRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	n, err := h.svc.CreateFolder(r.Context(), userID, req.ParentID, req.Name)
	if err != nil {
		h.mapErr(w, err)
		return
	}
	httpx.JSON(w, n)
}

// ---------------------------------------------------------------------------
// POST /api/files/move
// ---------------------------------------------------------------------------

// filesMoveRequest carries both a move and a rename: an empty NewParentID keeps
// the parent (a rename), an empty NewName keeps the name (a move).
type filesMoveRequest struct {
	NodeID      string `json:"node_id"`
	NewParentID string `json:"new_parent_id"`
	NewName     string `json:"new_name"`
}

func (h *filesHandlers) move(w http.ResponseWriter, r *http.Request) {
	userID := h.caller(w, r)
	if userID == "" {
		return
	}
	var req filesMoveRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	n, err := h.svc.Move(r.Context(), userID, req.NodeID, req.NewParentID, req.NewName)
	if err != nil {
		h.mapErr(w, err)
		return
	}
	httpx.JSON(w, n)
}

// ---------------------------------------------------------------------------
// POST /api/files/delete
// ---------------------------------------------------------------------------

type filesNodeRequest struct {
	NodeID string `json:"node_id"`
}

func (h *filesHandlers) delete(w http.ResponseWriter, r *http.Request) {
	userID := h.caller(w, r)
	if userID == "" {
		return
	}
	var req filesNodeRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	if err := h.svc.Delete(r.Context(), userID, req.NodeID); err != nil {
		h.mapErr(w, err)
		return
	}
	httpx.JSON(w, map[string]string{"status": "deleted"})
}

// ---------------------------------------------------------------------------
// POST /api/files/upload-grant
// ---------------------------------------------------------------------------

type filesUploadGrantRequest struct {
	ParentID    string `json:"parent_id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
}

type filesGrantResponse struct {
	Node  *files.Node       `json:"node"`
	Grant files.ObjectGrant `json:"grant"`
}

// uploadGrant is the ONE write path into a Drive, so it carries the write-side
// gates: the storage quota (the same single gate presignOp runs — see
// BILLING-LOCATION-01), then lazy bucket provisioning, and only then the node +
// grant. Over quota is refused BEFORE the node exists, so a blocked upload leaves
// no pending row behind.
func (h *filesHandlers) uploadGrant(w http.ResponseWriter, r *http.Request) {
	userID := h.caller(w, r)
	if userID == "" {
		return
	}
	var req filesUploadGrantRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}

	// Same gate, same tolerance as presignOp: metering is best-effort, so an
	// error reading usage lets the write through rather than blocking a paying
	// account on a sampler outage. A self-host/BYO account is exempt entirely
	// (STORAGE-LOCATION-$0-01).
	//
	// The storage quota is enforced through the billingport.EntitlementResolver
	// seam; a self-host/BYO account is exempt (NoopResolver returns unlimited).
	if h.sh.billing != nil {
		hostingKind := h.sh.hostingKindFor(r.Context(), userID)
		if sample, err := h.sh.svc.LatestUsage(r.Context(), userID); err == nil {
			if qerr := h.sh.billing.CheckStorageQuota(r.Context(), userID, sample.SizeBytes, string(hostingKind)); errors.Is(qerr, billingport.ErrQuotaExceeded) {
				httpx.Err(w, http.StatusPaymentRequired, "storage quota exceeded for your current tier")
				return
			}
		}
	}

	// A cloud account that has never provisioned a box has no bucket yet, and a
	// presigned PUT at a bucket that does not exist fails at the browser with an
	// opaque S3 error. Provision lazily here — idempotent, and it also writes the
	// config row callerOwnsBucket resolves the managed pin from.
	//
	// COORDINATOR: needs storageport.StorageProvisioner — bucket provisioning is
	// brokered through h.sh.svc (storage.Service). boxULID is a composition-root
	// helper (boxid.go).
	if _, err := h.sh.svc.ProvisionBucketIfAbsent(r.Context(), userID, boxULID(userID), storage.ProvisionOptions{}); err != nil {
		h.sh.mapErr(w, err)
		return
	}

	n, grant, err := h.svc.UploadGrant(r.Context(), userID, req.ParentID, req.Name, req.ContentType, filesGrantTTL)
	if err != nil {
		h.mapErr(w, err)
		return
	}
	httpx.JSON(w, filesGrantResponse{Node: n, Grant: grant})
}

// ---------------------------------------------------------------------------
// POST /api/files/commit
// ---------------------------------------------------------------------------

type filesCommitRequest struct {
	NodeID      string `json:"node_id"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	ETag        string `json:"etag"`
}

func (h *filesHandlers) commit(w http.ResponseWriter, r *http.Request) {
	userID := h.caller(w, r)
	if userID == "" {
		return
	}
	var req filesCommitRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	v, err := h.svc.Commit(r.Context(), userID, req.NodeID, req.Size, req.ContentType, req.ETag)
	if err != nil {
		h.mapErr(w, err)
		return
	}
	httpx.JSON(w, v)
}

// ---------------------------------------------------------------------------
// POST /api/files/download-grant
// ---------------------------------------------------------------------------

func (h *filesHandlers) downloadGrant(w http.ResponseWriter, r *http.Request) {
	userID := h.caller(w, r)
	if userID == "" {
		return
	}
	var req filesNodeRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	n, grant, err := h.svc.DownloadGrant(r.Context(), userID, req.NodeID, filesGrantTTL)
	if err != nil {
		h.mapErr(w, err)
		return
	}
	httpx.JSON(w, filesGrantResponse{Node: n, Grant: grant})
}

// ---------------------------------------------------------------------------
// GET /api/files/versions?node=
// ---------------------------------------------------------------------------

func (h *filesHandlers) versions(w http.ResponseWriter, r *http.Request) {
	userID := h.caller(w, r)
	if userID == "" {
		return
	}
	vs, err := h.svc.Versions(r.Context(), userID, r.URL.Query().Get("node"))
	if err != nil {
		h.mapErr(w, err)
		return
	}
	httpx.JSON(w, map[string]any{"versions": vs})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// mapErr maps the Files sentinels to status codes, and delegates anything else
// (chiefly a storage-provider failure raised while minting a grant) to the
// storage plane's own mapping, so a Tigris outage reads the same here as it does
// on /api/storage/*.
func (h *filesHandlers) mapErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, files.ErrNotFound):
		httpx.Err(w, http.StatusNotFound, err.Error())
	case errors.Is(err, files.ErrForbidden):
		httpx.Err(w, http.StatusForbidden, err.Error())
	case errors.Is(err, files.ErrInvalid):
		httpx.Err(w, http.StatusBadRequest, err.Error())
	default:
		h.sh.mapErr(w, err)
	}
}
