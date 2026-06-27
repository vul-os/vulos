package files

// dropbox.go — Dropbox ExternalProvider (PHASE 4 external stores).
//
// list folders/files, download, upload, and create folders via the Dropbox v2
// HTTP API using a short-lived bearer token brokered by the CP integration mint
// (provider "dropbox"). The token is supplied per call by the Files service and
// never stored here.
//
// SSRF SAFETY: this provider talks ONLY to the two fixed Dropbox hosts — the RPC
// host (https://api.dropboxapi.com) for metadata operations and the content host
// (https://content.dropboxapi.com) for byte transfer. Both are overridable as
// ONE base in tests. Dropbox paths are placed in the JSON request body / the
// Dropbox-API-Arg header against those fixed hosts — there is no caller-
// controlled URL, scheme, or host.
//
// WRITE / CONFLICTS: Upload maps the ConflictPolicy onto Dropbox's native upload
// modes — rename (default) ⇒ mode=add + autorename (Dropbox picks a free name);
// overwrite ⇒ mode=overwrite (replaces bytes in place); fail ⇒ mode=add without
// autorename (Dropbox 409 ⇒ ErrExternalConflict).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Fixed Dropbox hosts. SSRF guard: the only hosts this provider contacts.
const (
	dropboxAPIBase     = "https://api.dropboxapi.com"
	dropboxContentBase = "https://content.dropboxapi.com"
)

// DropboxProvider implements ExternalProvider over the Dropbox v2 API.
type DropboxProvider struct {
	apiBase     string // RPC host (default dropboxAPIBase); overridable in tests
	contentBase string // content host (default dropboxContentBase); overridable in tests
	http        *http.Client
}

// NewDropboxProvider returns a Dropbox provider with a bounded HTTP client.
func NewDropboxProvider() *DropboxProvider {
	return &DropboxProvider{
		apiBase:     dropboxAPIBase,
		contentBase: dropboxContentBase,
		http:        &http.Client{Timeout: 60 * time.Second},
	}
}

func (d *DropboxProvider) Kind() string                        { return "dropbox" }
func (d *DropboxProvider) IntegrationProvider() string         { return "dropbox" }
func (d *DropboxProvider) DisplayName() string                 { return "Dropbox" }
func (d *DropboxProvider) Writable() bool                      { return true }
func (d *DropboxProvider) ConfigFields() []ProviderConfigField { return nil }
func (d *DropboxProvider) ValidateConfig(map[string]string) (map[string]string, error) {
	return nil, nil
}

// dropboxEntry is the subset of a Dropbox metadata resource we map into a Node.
type dropboxEntry struct {
	Tag            string `json:".tag"` // "file" | "folder" | "deleted"
	Name           string `json:"name"`
	PathDisplay    string `json:"path_display"`
	PathLower      string `json:"path_lower"`
	ID             string `json:"id"`
	Size           int64  `json:"size"`
	ServerModified string `json:"server_modified"`
}

type dropboxListResp struct {
	Entries []dropboxEntry `json:"entries"`
	Cursor  string         `json:"cursor"`
	HasMore bool           `json:"has_more"`
}

// List returns the children of folderID (Dropbox path; empty ⇒ root) as Nodes.
func (d *DropboxProvider) List(ctx context.Context, call ProviderCall, folderID string) ([]*Node, error) {
	// Dropbox uses "" for the root, and "/Folder" for everything else.
	path := normalizeDropboxFolder(folderID)
	var out []*Node
	var resp dropboxListResp
	if err := d.rpc(ctx, call.Token, "/2/files/list_folder",
		map[string]any{"path": path, "limit": 2000}, &resp); err != nil {
		return nil, err
	}
	for {
		for i := range resp.Entries {
			if n := dropboxEntryToNode(&resp.Entries[i]); n != nil {
				out = append(out, n)
			}
		}
		if !resp.HasMore || len(out) >= 10000 {
			break
		}
		resp = dropboxListResp{}
		if err := d.rpc(ctx, call.Token, "/2/files/list_folder/continue",
			map[string]any{"cursor": resp.Cursor}, &resp); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Download opens a file's bytes by its Dropbox path.
func (d *DropboxProvider) Download(ctx context.Context, call ProviderCall, fileID string) (io.ReadCloser, string, int64, error) {
	arg, _ := json.Marshal(map[string]any{"path": fileID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.contentBase+"/2/files/download", nil)
	if err != nil {
		return nil, "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+call.Token)
	req.Header.Set("Dropbox-API-Arg", string(arg))
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, "", 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", 0, fmt.Errorf("dropbox: download status %d", resp.StatusCode)
	}
	size := int64(-1)
	// Dropbox returns file metadata (incl. size) in the result header.
	if hdr := resp.Header.Get("Dropbox-API-Result"); hdr != "" {
		var meta dropboxEntry
		if json.Unmarshal([]byte(hdr), &meta) == nil && meta.Size > 0 {
			size = meta.Size
		}
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	return resp.Body, ct, size, nil
}

// CreateFolder creates a folder named name under parentID (Dropbox path).
func (d *DropboxProvider) CreateFolder(ctx context.Context, call ProviderCall, parentID, name string) (*Node, error) {
	path := joinDropboxPath(parentID, name)
	var resp struct {
		Metadata dropboxEntry `json:"metadata"`
	}
	if err := d.rpc(ctx, call.Token, "/2/files/create_folder_v2",
		map[string]any{"path": path, "autorename": false}, &resp); err != nil {
		return nil, err
	}
	if resp.Metadata.Tag == "" {
		resp.Metadata.Tag = "folder"
	}
	return dropboxEntryToNode(&resp.Metadata), nil
}

// Upload writes bytes as name under parentID applying policy on a collision.
func (d *DropboxProvider) Upload(ctx context.Context, call ProviderCall, parentID, name, contentType string, _ int64, r io.Reader, policy ConflictPolicy) (*Node, error) {
	path := joinDropboxPath(parentID, name)
	mode := "add"
	autorename := false
	switch policy {
	case ConflictOverwrite:
		mode = "overwrite"
	case ConflictFail:
		mode = "add"
	default: // ConflictRename
		mode = "add"
		autorename = true
	}
	arg, _ := json.Marshal(map[string]any{
		"path":       path,
		"mode":       mode,
		"autorename": autorename,
		"mute":       true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.contentBase+"/2/files/upload", r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+call.Token)
	req.Header.Set("Dropbox-API-Arg", string(arg))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// A conflict under mode=add (fail policy) surfaces as 409.
	if resp.StatusCode == http.StatusConflict {
		return nil, ErrExternalConflict
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dropbox: upload status %d", resp.StatusCode)
	}
	var meta dropboxEntry
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&meta); err != nil {
		return nil, err
	}
	if meta.Tag == "" {
		meta.Tag = "file"
	}
	return dropboxEntryToNode(&meta), nil
}

// rpc performs an authenticated JSON-RPC POST to the fixed Dropbox API host.
func (d *DropboxProvider) rpc(ctx context.Context, token, path string, body any, v any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.apiBase+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dropbox: api %s status %d", path, resp.StatusCode)
	}
	if v == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

// dropboxEntryToNode maps a Dropbox metadata entry into the Files Node shape. The
// Node ID is the entry's path_display (Dropbox accepts a path wherever it accepts
// an id), so navigation and download round-trip the path. Deleted tombstones map
// to nil and are skipped.
func dropboxEntryToNode(e *dropboxEntry) *Node {
	if e.Tag == "deleted" {
		return nil
	}
	id := e.PathDisplay
	if id == "" {
		id = e.PathLower
	}
	var updated time.Time
	if e.ServerModified != "" {
		updated, _ = time.Parse(time.RFC3339, e.ServerModified)
	}
	return &Node{
		ID:        id,
		Name:      e.Name,
		IsDir:     e.Tag == "folder",
		Size:      e.Size,
		UpdatedAt: updated,
	}
}

// normalizeDropboxFolder maps a folder id into a Dropbox folder path: the root is
// the empty string; a leading slash is ensured for any non-root folder.
func normalizeDropboxFolder(folderID string) string {
	folderID = strings.TrimRight(folderID, "/")
	if folderID == "" {
		return ""
	}
	if !strings.HasPrefix(folderID, "/") {
		return "/" + folderID
	}
	return folderID
}

// joinDropboxPath builds the destination path for an item named name under the
// parent folder path (empty ⇒ root).
func joinDropboxPath(parentID, name string) string {
	parent := strings.TrimRight(normalizeDropboxFolder(parentID), "/")
	return parent + "/" + name
}
