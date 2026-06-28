package files

// onedrive.go — Microsoft OneDrive import source over the Microsoft Graph v1.0
// API (FILES IMPORT engine). It lists folders/files and opens file bytes for
// copying into the user's Drive using a short-lived bearer token brokered by the
// CP integration mint (provider "microsoft").
//
// OneDrive vs Google Drive on import: OneDrive stores Office documents
// (Word/Excel/PowerPoint) as REAL binary .docx/.xlsx/.pptx files, so — unlike
// Google's native Docs — there is NOTHING to export; every item is downloaded
// as-is via the item's /content endpoint. That is the entire reason OneDrive
// needs only List + OpenForImport (a plain copy) while Google needs an export
// mapping.
//
// SSRF SAFETY: this source talks ONLY to the fixed Microsoft Graph host
// (https://graph.microsoft.com/v1.0), overridable as ONE base in tests. Item
// identifiers are Graph opaque IDs placed into the path of that fixed host —
// there is no caller-controlled URL, scheme, or host. The /content endpoint
// 302-redirects to a Microsoft-owned download host; the bounded client follows
// it, and paging (@odata.nextLink) is only followed when it points back at the
// same fixed host.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// graphAPIBase is the fixed Microsoft Graph v1.0 host. SSRF guard: the only host
// this source contacts (apart from Graph's own 302 download redirects).
const graphAPIBase = "https://graph.microsoft.com/v1.0"

// OneDriveProvider implements files.ImportSource over Microsoft Graph.
type OneDriveProvider struct {
	base string // Graph base (default graphAPIBase); overridable in tests
	http *http.Client
}

// NewOneDriveProvider returns a OneDrive import source with a bounded client.
func NewOneDriveProvider() *OneDriveProvider {
	return &OneDriveProvider{
		base: graphAPIBase,
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

func (o *OneDriveProvider) Kind() string                { return "onedrive" }
func (o *OneDriveProvider) IntegrationProvider() string { return "microsoft" }
func (o *OneDriveProvider) DisplayName() string         { return "Microsoft OneDrive" }

// graphItem is the subset of a Graph driveItem we map into a Node.
type graphItem struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Size           int64           `json:"size"`
	LastModifiedAt string          `json:"lastModifiedDateTime"`
	Folder         *graphFolder    `json:"folder"`
	File           *graphFileFacet `json:"file"`
}

type graphFolder struct {
	ChildCount int `json:"childCount"`
}

type graphFileFacet struct {
	MimeType string `json:"mimeType"`
}

type graphListResp struct {
	Value    []graphItem `json:"value"`
	NextLink string      `json:"@odata.nextLink"`
}

// List returns the children of folderID (empty ⇒ the drive root) as Nodes.
func (o *OneDriveProvider) List(ctx context.Context, call ProviderCall, folderID string) ([]*Node, error) {
	var apiPath string
	if folderID == "" {
		apiPath = "/me/drive/root/children"
	} else {
		apiPath = "/me/drive/items/" + url.PathEscape(folderID) + "/children"
	}
	apiPath += "?$top=200&$select=id,name,size,lastModifiedDateTime,folder,file"
	full := o.base + apiPath
	var out []*Node
	for page := 0; page < 50; page++ { // cap: 50 * 200 = 10000 items
		var resp graphListResp
		if err := o.getJSON(ctx, call.Token, full, &resp); err != nil {
			return nil, err
		}
		for i := range resp.Value {
			out = append(out, graphItemToNode(&resp.Value[i]))
		}
		if resp.NextLink == "" || !o.sameHost(resp.NextLink) {
			break
		}
		full = resp.NextLink // already absolute; host-checked above
	}
	return out, nil
}

// OpenForImport opens a OneDrive file's bytes for copying. OneDrive Office files
// are already binary docx/xlsx/pptx, so there is no export step — the bytes are
// streamed from the item's /content endpoint as-is. Implements
// files.ImportSource.
func (o *OneDriveProvider) OpenForImport(ctx context.Context, call ProviderCall, file *Node) (io.ReadCloser, string, string, int64, error) {
	full := o.base + "/me/drive/items/" + url.PathEscape(file.ID) + "/content"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, "", "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+call.Token)
	resp, err := o.http.Do(req)
	if err != nil {
		return nil, "", "", 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", "", 0, fmt.Errorf("onedrive: content status %d", resp.StatusCode)
	}
	contentType := file.ContentType
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		contentType = ct
	}
	return resp.Body, file.Name, contentType, file.Size, nil
}

// getJSON performs an authenticated GET against a fixed-host Graph URL and
// decodes a JSON body into v. fullURL must be on the Graph host.
func (o *OneDriveProvider) getJSON(ctx context.Context, token, fullURL string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := o.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("onedrive: api status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

// sameHost reports whether candidate has the same scheme+host as the configured
// base — the SSRF guard for following a paging @odata.nextLink.
func (o *OneDriveProvider) sameHost(candidate string) bool {
	bu, err := url.Parse(o.base)
	if err != nil {
		return false
	}
	cu, err := url.Parse(candidate)
	if err != nil {
		return false
	}
	return cu.Scheme == bu.Scheme && cu.Host == bu.Host
}

// graphItemToNode maps a Graph driveItem into the Files Node shape. The Node ID
// is the Graph item id (opaque to us); the mime type rides ContentType.
func graphItemToNode(it *graphItem) *Node {
	isDir := it.Folder != nil
	mime := ""
	if it.File != nil {
		mime = it.File.MimeType
	}
	var updated time.Time
	if it.LastModifiedAt != "" {
		updated, _ = time.Parse(time.RFC3339, it.LastModifiedAt)
	}
	return &Node{
		ID:          it.ID,
		Name:        it.Name,
		IsDir:       isDir,
		Size:        it.Size,
		ContentType: mime,
		UpdatedAt:   updated,
	}
}
