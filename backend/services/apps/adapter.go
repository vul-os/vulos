// Package apps wires Vulos OS into the shared Vulos Apps & Bots platform
// (github.com/vul-os/vulos-apps, appsplatform) and its MCP layer. It provides the
// OS ProductAdapter — the small product seam that teaches the product-agnostic
// platform (and, in a different shape, any MCP-speaking LLM/agent) how to act on
// and read from OS-level capabilities that are SAFE and useful for agents.
//
// The surface is deliberately conservative:
//
//	Act   — OS actions on behalf of the installing app (apps:write):
//	          files.folder.create   create a folder in the Drive
//	          files.write           create/overwrite a small text/binary file
//	          files.move            move / rename a node
//	          files.share           grant a principal a role on a node
//	          files.unshare         revoke a principal's role on a node
//	          files.delete          soft-delete a node
//	Read  — read-only OS content (apps:read):
//	          files.list            list a folder (target=folder id, empty=root)
//	          files.read            read a file's bytes (utf-8 or base64)
//	          files.versions        a node's version history
//	          files.shares          a node's ACL entries (owner only)
//	          files.shared-with-me  nodes shared with the caller
//	          apps                  installed OS apps (metadata only)
//	          system                basic, read-only system info
//
// EVERY Files operation goes through the existing ACL-gated Files service: the app
// acts on behalf of its installing OWNER account, so it can touch exactly the
// nodes that owner can — never more. There is NO arbitrary shell, no privileged
// host mutation, and no raw filesystem access; only the ACL-respecting Files
// control plane plus read-only app/system metadata are exposed.
//
// The platform owns authentication (vat_ app tokens), token hashing, product
// targeting, and method-level scope; this adapter owns the OS-native semantics
// and the per-node authorization (via files.Service, acting as the owner).
package apps

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"vulos/backend/services/files"

	"github.com/vul-os/vulos-apps/appsplatform"
	"github.com/vul-os/vulos-apps/mcp"
)

// grantTTL bounds the lifetime of the object-scoped storage grant minted for an
// agent file write. Short by design — the bytes land immediately after.
const grantTTL = 15 * time.Minute

// maxReadBytes caps how many bytes files.read returns inline so an agent cannot
// pull an unbounded object through the JSON-RPC/REST surface.
const maxReadBytes = 1 << 20 // 1 MiB

// AppInfo is the read-only, secret-free metadata view of one installed OS app.
type AppInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
	Type        string `json:"type,omitempty"`
	Category    string `json:"category,omitempty"`
	Author      string `json:"author,omitempty"`
	License     string `json:"license,omitempty"`
	Homepage    string `json:"homepage,omitempty"`
}

// AppLister enumerates installed OS apps. It is wired in the composition root
// (from appnet.ScanApps) so this package stays free of the appnet dependency and
// is trivially testable. nil ⇒ the "apps" read kind returns an empty list.
type AppLister func() []AppInfo

// SysInfoFunc returns conservative, read-only system info for agents. nil ⇒ the
// "system" read kind returns an empty object.
type SysInfoFunc func() map[string]any

// OSAdapter implements appsplatform.ProductAdapter (and the optional
// mcp.Descriptor) for Vulos OS.
type OSAdapter struct {
	files *files.Service
	apps  AppLister
	sys   SysInfoFunc
}

// NewOSAdapter builds the OS product adapter over the ACL-gated Files service and
// the read-only app/system info providers. Any dependency may be nil; the
// corresponding actions/kinds then degrade safely (Files ops return an error,
// apps/system return empty).
func NewOSAdapter(filesSvc *files.Service, apps AppLister, sys SysInfoFunc) *OSAdapter {
	return &OSAdapter{files: filesSvc, apps: apps, sys: sys}
}

// Compile-time assertions: the adapter satisfies the platform seam and the
// optional MCP Descriptor seam (so an LLM/agent gets a correctly described
// per-product MCP server — see MCPTools / MCPResources below).
var (
	_ appsplatform.ProductAdapter = (*OSAdapter)(nil)
	_ mcp.Descriptor              = (*OSAdapter)(nil)
)

// Product reports that this adapter serves the OS product. The platform lists and
// serves only apps that target "os".
func (a *OSAdapter) Product() string { return appsplatform.ProductOS }

// RequiredScope maps an action (Act) or kind (Read) to the scope it needs. Reads
// need apps:read; everything that mutates needs apps:write. An unknown action
// falls through to apps:write (fail-safe: deny unless the broader write grant is
// held).
func (a *OSAdapter) RequiredScope(actionOrKind string) string {
	switch actionOrKind {
	case "files.list", "files.read", "files.versions", "files.shares", "files.shared-with-me", "apps", "system":
		return appsplatform.ScopeAppsRead
	default:
		return appsplatform.ScopeAppsWrite
	}
}

// CanAccessTarget gates an app's access to a Files node target through the Files
// service's existing per-node ACL. A target-less op (list root, create at root,
// apps/system reads) is always reachable. A non-existent node yields exists=false
// (the platform returns 404); a node the app's owner cannot see yields
// allowed=false (403). The app acts as its installing owner, so this is exactly
// the access that owner is subject to.
func (a *OSAdapter) CanAccessTarget(app *appsplatform.App, target string) (allowed, exists bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return true, true
	}
	if a.files == nil {
		return false, false
	}
	role, err := a.files.EffectiveRole(effectiveAccount(app), target)
	if err != nil {
		// ErrNotFound (and any other lookup failure) → not accessible. Distinguish
		// not-found so the platform can 404 rather than 403.
		if errors.Is(err, files.ErrNotFound) {
			return false, false
		}
		return false, true
	}
	return role != "", true
}

// Act performs an OS action requested by an app at runtime (or an MCP tool call).
func (a *OSAdapter) Act(ctx context.Context, app *appsplatform.App, req appsplatform.ActionRequest, emit appsplatform.EmitFunc) (any, error) {
	acct := effectiveAccount(app)
	switch req.Action {
	case "files.folder.create":
		return a.createFolder(acct, req.Target, req.Payload, app, emit)
	case "files.write":
		return a.writeFile(ctx, acct, req.Target, req.Payload, app, emit)
	case "files.move":
		return a.moveNode(ctx, acct, req.Target, req.Payload, app, emit)
	case "files.share":
		return a.share(acct, req.Target, req.Payload, app, emit)
	case "files.unshare":
		return a.unshare(acct, req.Target, req.Payload, app, emit)
	case "files.delete":
		return a.deleteNode(acct, req.Target, app, emit)
	default:
		return nil, fmt.Errorf("os: unsupported action %q", req.Action)
	}
}

// Read returns read-only OS content visible to the app's owner.
func (a *OSAdapter) Read(ctx context.Context, app *appsplatform.App, req appsplatform.ReadRequest) (any, error) {
	acct := effectiveAccount(app)
	switch req.Kind {
	case "files.list":
		if err := a.requireFiles(); err != nil {
			return nil, err
		}
		nodes, err := a.files.List(acct, strings.TrimSpace(req.Target))
		if err != nil {
			return nil, err
		}
		return map[string]any{"nodes": nodes}, nil
	case "files.read":
		return a.readFile(ctx, acct, req.Target)
	case "files.versions":
		if err := a.requireFiles(); err != nil {
			return nil, err
		}
		vs, err := a.files.Versions(acct, strings.TrimSpace(req.Target))
		if err != nil {
			return nil, err
		}
		return map[string]any{"versions": vs}, nil
	case "files.shares":
		if err := a.requireFiles(); err != nil {
			return nil, err
		}
		acls, err := a.files.ListShares(acct, strings.TrimSpace(req.Target))
		if err != nil {
			return nil, err
		}
		return map[string]any{"shares": acls}, nil
	case "files.shared-with-me":
		if err := a.requireFiles(); err != nil {
			return nil, err
		}
		nodes, err := a.files.SharedWithMe(acct)
		if err != nil {
			return nil, err
		}
		return map[string]any{"nodes": nodes}, nil
	case "apps":
		var list []AppInfo
		if a.apps != nil {
			list = a.apps()
		}
		if list == nil {
			list = []AppInfo{}
		}
		return map[string]any{"apps": list}, nil
	case "system":
		if a.sys != nil {
			if info := a.sys(); info != nil {
				return info, nil
			}
		}
		return map[string]any{}, nil
	default:
		return nil, fmt.Errorf("os: unsupported read kind %q", req.Kind)
	}
}

// ---- MCP Descriptor ---------------------------------------------------------
//
// The optional mcp.Descriptor seam PUBLISHES this adapter's surface to the Vulos
// MCP layer so any LLM/agent can operate the OS over MCP with a vat_ app token.
// It is a different SHAPE over the EXACT same Act/Read seam the REST apps platform
// uses — the same per-node Files ACL (acting as the installing owner), the same
// scopes — so an MCP tool call is access-checked identically to a REST action.
// Tools mirror Act actions (apps:write); resources mirror Read kinds (apps:read).

// MCPTools returns the Act actions exposed as MCP tools. Every Files target is a
// node id, so the node-scoped tools accept a "target" which is gated through
// CanAccessTarget (the Files ACL) before Act runs.
func (a *OSAdapter) MCPTools() []mcp.ToolSpec {
	return []mcp.ToolSpec{
		{
			Action:        "files.folder.create",
			Description:   "Create a folder in the owner's Drive. target = the parent folder id (omit for the Drive root). Requires editor+ on a non-root parent.",
			AcceptsTarget: true,
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "Folder name (no path separators)."}
  },
  "required": ["name"]
}`),
		},
		{
			Action:        "files.write",
			Description:   "Create or overwrite a file in the owner's Drive with inline content. target = the parent folder id (omit for the Drive root). Content is UTF-8 text by default, or base64 when encoding=\"base64\". Requires editor+ on the parent.",
			AcceptsTarget: true,
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "name": {"type": "string", "description": "File name (no path separators)."},
    "content": {"type": "string", "description": "File content; UTF-8 text unless encoding is base64."},
    "encoding": {"type": "string", "enum": ["utf-8", "base64"], "description": "Encoding of content (default utf-8)."},
    "content_type": {"type": "string", "description": "MIME type (default text/plain; charset=utf-8)."}
  },
  "required": ["name", "content"]
}`),
		},
		{
			Action:        "files.move",
			Description:   "Move and/or rename a Drive node. target = the node id to move. Requires editor+ on the node and the destination; cross-owner moves are rejected.",
			AcceptsTarget: true,
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "new_parent_id": {"type": "string", "description": "Destination folder id (omit to keep the current parent)."},
    "new_name": {"type": "string", "description": "New name (omit to keep the current name)."}
  }
}`),
		},
		{
			Action:        "files.share",
			Description:   "Grant a principal (user id) a role on a node. target = the node id. Owner only; role is editor or viewer.",
			AcceptsTarget: true,
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "principal_id": {"type": "string", "description": "The user id to grant access to."},
    "role": {"type": "string", "enum": ["editor", "viewer"], "description": "Role to grant."}
  },
  "required": ["principal_id", "role"]
}`),
		},
		{
			Action:        "files.unshare",
			Description:   "Revoke a principal's direct role on a node. target = the node id. Owner only.",
			AcceptsTarget: true,
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "principal_id": {"type": "string", "description": "The user id whose access to revoke."}
  },
  "required": ["principal_id"]
}`),
		},
		{
			Action:        "files.delete",
			Description:   "Soft-delete a Drive node (and, for a folder, its subtree). target = the node id. Requires editor+.",
			AcceptsTarget: true,
			InputSchema:   json.RawMessage(`{"type": "object", "properties": {}}`),
		},
	}
}

// MCPResources returns the Read kinds exposed as MCP resources. Node-addressed
// kinds carry a target segment (vulos://os/<kind>/<node-id>) access-checked via
// the Files ACL before Read runs.
func (a *OSAdapter) MCPResources() []mcp.ResourceSpec {
	return []mcp.ResourceSpec{
		{
			Kind:          "files.list",
			Name:          "Drive listing",
			Description:   "List a Drive folder's children. Append a folder id (vulos://os/files.list/<id>) or omit it for the Drive root.",
			AcceptsTarget: true,
		},
		{
			Kind:          "files.read",
			Name:          "File contents",
			Description:   "Read a file's bytes (vulos://os/files.read/<id>). Returned as UTF-8 text or base64, capped at 1 MiB.",
			AcceptsTarget: true,
		},
		{
			Kind:          "files.versions",
			Name:          "File versions",
			Description:   "A node's version history (vulos://os/files.versions/<id>).",
			AcceptsTarget: true,
		},
		{
			Kind:          "files.shares",
			Name:          "Node ACL",
			Description:   "A node's direct ACL entries (vulos://os/files.shares/<id>). Owner only.",
			AcceptsTarget: true,
		},
		{
			Kind:        "files.shared-with-me",
			Name:        "Shared with me",
			Description: "Drive nodes shared with the owner.",
		},
		{
			Kind:        "apps",
			Name:        "Installed apps",
			Description: "Metadata for every installed OS app (read-only).",
		},
		{
			Kind:        "system",
			Name:        "System info",
			Description: "Basic, read-only OS/system information.",
		},
	}
}

// ---- actions ----------------------------------------------------------------

type folderCreatePayload struct {
	Name string `json:"name"`
}

func (a *OSAdapter) createFolder(acct, parent string, payload json.RawMessage, app *appsplatform.App, emit appsplatform.EmitFunc) (any, error) {
	if err := a.requireFiles(); err != nil {
		return nil, err
	}
	var p folderCreatePayload
	_ = json.Unmarshal(payload, &p)
	n, err := a.files.CreateFolder(acct, strings.TrimSpace(parent), strings.TrimSpace(p.Name))
	if err != nil {
		return nil, err
	}
	a.emitTo(emit, app, "files.folder.created", map[string]any{"node_id": n.ID, "name": n.Name, "path": n.Path})
	return n, nil
}

type writePayload struct {
	Name        string `json:"name"`
	Content     string `json:"content"`
	Encoding    string `json:"encoding"`
	ContentType string `json:"content_type"`
}

func (a *OSAdapter) writeFile(ctx context.Context, acct, parent string, payload json.RawMessage, app *appsplatform.App, emit appsplatform.EmitFunc) (any, error) {
	if err := a.requireFiles(); err != nil {
		return nil, err
	}
	var p writePayload
	_ = json.Unmarshal(payload, &p)
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return nil, fmt.Errorf("os: files.write requires a name")
	}
	data, err := decodeContent(p.Content, p.Encoding)
	if err != nil {
		return nil, err
	}
	ct := strings.TrimSpace(p.ContentType)
	if ct == "" {
		ct = "text/plain; charset=utf-8"
	}
	// UploadGrant locates/creates the node and ACL-gates a write grant (editor+ on
	// the parent). The OS-mediated data plane then stores the bytes, and Commit
	// records the version. Every step re-checks the ACL inside the Files service.
	node, _, err := a.files.UploadGrant(ctx, acct, strings.TrimSpace(parent), name, ct, grantTTL)
	if err != nil {
		return nil, err
	}
	etag, err := a.files.PutContent(ctx, acct, node.ID, bytes.NewReader(data), int64(len(data)), ct)
	if err != nil {
		return nil, err
	}
	v, err := a.files.Commit(acct, node.ID, int64(len(data)), ct, etag)
	if err != nil {
		return nil, err
	}
	a.emitTo(emit, app, "files.written", map[string]any{"node_id": node.ID, "name": node.Name, "size": len(data)})
	return map[string]any{"node_id": node.ID, "name": node.Name, "size": len(data), "version_id": v.ID}, nil
}

type movePayload struct {
	NewParentID string `json:"new_parent_id"`
	NewName     string `json:"new_name"`
}

func (a *OSAdapter) moveNode(ctx context.Context, acct, node string, payload json.RawMessage, app *appsplatform.App, emit appsplatform.EmitFunc) (any, error) {
	if err := a.requireFiles(); err != nil {
		return nil, err
	}
	node = strings.TrimSpace(node)
	if node == "" {
		return nil, fmt.Errorf("os: files.move requires a target node id")
	}
	var p movePayload
	_ = json.Unmarshal(payload, &p)
	n, err := a.files.Move(ctx, acct, node, strings.TrimSpace(p.NewParentID), strings.TrimSpace(p.NewName))
	if err != nil {
		return nil, err
	}
	a.emitTo(emit, app, "files.moved", map[string]any{"node_id": n.ID, "name": n.Name, "path": n.Path})
	return n, nil
}

type sharePayload struct {
	PrincipalID string `json:"principal_id"`
	Role        string `json:"role"`
}

func (a *OSAdapter) share(acct, node string, payload json.RawMessage, app *appsplatform.App, emit appsplatform.EmitFunc) (any, error) {
	if err := a.requireFiles(); err != nil {
		return nil, err
	}
	node = strings.TrimSpace(node)
	if node == "" {
		return nil, fmt.Errorf("os: files.share requires a target node id")
	}
	var p sharePayload
	_ = json.Unmarshal(payload, &p)
	e, err := a.files.Share(acct, node, strings.TrimSpace(p.PrincipalID), files.Role(strings.TrimSpace(p.Role)))
	if err != nil {
		return nil, err
	}
	a.emitTo(emit, app, "files.shared", map[string]any{"node_id": node, "principal_id": e.PrincipalID, "role": string(e.Role)})
	return e, nil
}

type unsharePayload struct {
	PrincipalID string `json:"principal_id"`
}

func (a *OSAdapter) unshare(acct, node string, payload json.RawMessage, app *appsplatform.App, emit appsplatform.EmitFunc) (any, error) {
	if err := a.requireFiles(); err != nil {
		return nil, err
	}
	node = strings.TrimSpace(node)
	if node == "" {
		return nil, fmt.Errorf("os: files.unshare requires a target node id")
	}
	var p unsharePayload
	_ = json.Unmarshal(payload, &p)
	if err := a.files.Unshare(acct, node, strings.TrimSpace(p.PrincipalID)); err != nil {
		return nil, err
	}
	a.emitTo(emit, app, "files.unshared", map[string]any{"node_id": node, "principal_id": strings.TrimSpace(p.PrincipalID)})
	return map[string]any{"node_id": node, "revoked": true}, nil
}

func (a *OSAdapter) deleteNode(acct, node string, app *appsplatform.App, emit appsplatform.EmitFunc) (any, error) {
	if err := a.requireFiles(); err != nil {
		return nil, err
	}
	node = strings.TrimSpace(node)
	if node == "" {
		return nil, fmt.Errorf("os: files.delete requires a target node id")
	}
	if err := a.files.Delete(acct, node); err != nil {
		return nil, err
	}
	a.emitTo(emit, app, "files.deleted", map[string]any{"node_id": node})
	return map[string]any{"node_id": node, "deleted": true}, nil
}

// ---- reads ------------------------------------------------------------------

func (a *OSAdapter) readFile(ctx context.Context, acct, node string) (any, error) {
	if err := a.requireFiles(); err != nil {
		return nil, err
	}
	node = strings.TrimSpace(node)
	if node == "" {
		return nil, fmt.Errorf("os: files.read requires a target node id")
	}
	rc, n, size, err := a.files.GetContent(ctx, acct, node)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxReadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("os: read file: %w", err)
	}
	truncated := false
	if len(data) > maxReadBytes {
		data = data[:maxReadBytes]
		truncated = true
	}
	out := map[string]any{"node": n, "size": size, "truncated": truncated}
	if utf8.Valid(data) {
		out["content"] = string(data)
		out["encoding"] = "utf-8"
	} else {
		out["content"] = base64.StdEncoding.EncodeToString(data)
		out["encoding"] = "base64"
	}
	return out, nil
}

// ---- helpers ----------------------------------------------------------------

// requireFiles guards Files-backed operations when the service is unavailable.
func (a *OSAdapter) requireFiles() error {
	if a.files == nil {
		return fmt.Errorf("os: files service unavailable")
	}
	return nil
}

// decodeContent decodes inline write content per its declared encoding.
func decodeContent(content, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "utf-8", "utf8", "text":
		return []byte(content), nil
	case "base64":
		b, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return nil, fmt.Errorf("os: invalid base64 content: %w", err)
		}
		return b, nil
	default:
		return nil, fmt.Errorf("os: unsupported content encoding %q", encoding)
	}
}

// effectiveAccount is the account an app acts as for OS authorization: its
// installing OWNER (so it inherits exactly that account's Files access). Apps
// installed without an owner (e.g. the OSS single-user default) fall back to the
// app's synthetic account id.
func effectiveAccount(app *appsplatform.App) string {
	if app != nil && strings.TrimSpace(app.OwnerID) != "" {
		return app.OwnerID
	}
	if app != nil {
		return app.AccountID()
	}
	return "self"
}

// emitTo fans an event to the originating app only (visibility predicate) so an
// OS action notifies the app's own runtime without leaking to others.
func (a *OSAdapter) emitTo(emit appsplatform.EmitFunc, app *appsplatform.App, eventType string, payload map[string]any) {
	if emit == nil || app == nil {
		return
	}
	emit(eventType, payload, func(other *appsplatform.App) bool {
		return other != nil && other.ID == app.ID
	})
}
