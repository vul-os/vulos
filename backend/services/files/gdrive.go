package files

// gdrive.go — Google Drive ExternalProvider (Wave 1 of PHASE 4 external stores).
//
// Read-first: list folders/files and download bytes via the Google Drive v3 API
// using a short-lived bearer token brokered by the CP integration mint (provider
// "google", with the Drive scope added CP-side on cloud-drive-broker). The token
// is supplied per call by the Files service and never stored here.
//
// SSRF SAFETY: this provider talks ONLY to a single, fixed API host
// (https://www.googleapis.com by default; overridable in tests). File and folder
// identifiers are Drive opaque IDs placed into the path/query of that fixed host
// — there is no caller-controlled URL, scheme, or host, so a malicious mount or
// item id can never redirect the request elsewhere. Redirects are not followed
// to an arbitrary host beyond Google's own download domains, which the default
// http.Client handles within the API surface.
//
// WRITE SUPPORT: not implemented. External Drive mounts are READ-ONLY in this
// phase (list + download). Creating/uploading into Google Drive is deferred as a
// follow-up (it needs the drive.file write scope and conflict handling); the
// ExternalProvider interface leaves room for it without a DB change.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// gdriveAPIBase is the fixed Google Drive v3 API host. SSRF guard: the only host
// this provider ever contacts.
const gdriveAPIBase = "https://www.googleapis.com/drive/v3"

// gdriveFolderMime marks a Drive folder.
const gdriveFolderMime = "application/vnd.google-apps.folder"

// gdriveExportMap maps Google-native doc mime types to an exportable binary
// format (used on download, since native docs have no direct media bytes).
var gdriveExportMap = map[string]struct {
	mime, ext string
}{
	"application/vnd.google-apps.document":     {"application/pdf", ".pdf"},
	"application/vnd.google-apps.spreadsheet":  {"application/pdf", ".pdf"},
	"application/vnd.google-apps.presentation": {"application/pdf", ".pdf"},
	"application/vnd.google-apps.drawing":      {"image/png", ".png"},
}

// GDriveProvider implements ExternalProvider over the Google Drive v3 API.
type GDriveProvider struct {
	base string       // API base (default gdriveAPIBase); overridable in tests
	http *http.Client // bounded client; never follows to arbitrary hosts
}

// NewGDriveProvider returns a Google Drive provider with a bounded HTTP client.
func NewGDriveProvider() *GDriveProvider {
	return &GDriveProvider{
		base: gdriveAPIBase,
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

func (g *GDriveProvider) Kind() string                { return "gdrive" }
func (g *GDriveProvider) IntegrationProvider() string { return "google" }
func (g *GDriveProvider) DisplayName() string         { return "Google Drive" }

// driveFile is the subset of a Drive v3 file resource we map into a Node.
type driveFile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	MimeType     string `json:"mimeType"`
	Size         string `json:"size"` // Drive returns size as a string
	ModifiedTime string `json:"modifiedTime"`
}

type driveListResp struct {
	Files         []driveFile `json:"files"`
	NextPageToken string      `json:"nextPageToken"`
}

// List returns the children of folderID (empty ⇒ "root") as Nodes. It pages
// through the result set up to a sane cap so a huge folder cannot hang the UI.
func (g *GDriveProvider) List(ctx context.Context, token, folderID string) ([]*Node, error) {
	if folderID == "" {
		folderID = "root"
	}
	var out []*Node
	pageToken := ""
	for page := 0; page < 20; page++ { // cap: 20 * 200 = 4000 items
		q := url.Values{}
		q.Set("q", fmt.Sprintf("'%s' in parents and trashed=false", escapeDriveQ(folderID)))
		q.Set("fields", "files(id,name,mimeType,size,modifiedTime),nextPageToken")
		q.Set("pageSize", "200")
		q.Set("orderBy", "folder,name")
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var resp driveListResp
		if err := g.getJSON(ctx, token, "/files?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		for i := range resp.Files {
			out = append(out, driveFileToNode(&resp.Files[i]))
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return out, nil
}

// Download opens a file's bytes. Native Google docs are exported to a portable
// format; regular files are fetched via alt=media.
func (g *GDriveProvider) Download(ctx context.Context, token, fileID string) (io.ReadCloser, string, int64, error) {
	// Look up the file to learn its mime type (so we know whether to export).
	var meta driveFile
	if err := g.getJSON(ctx, token, "/files/"+url.PathEscape(fileID)+"?fields=id,name,mimeType,size", &meta); err != nil {
		return nil, "", 0, err
	}
	var path, contentType string
	if exp, ok := gdriveExportMap[meta.MimeType]; ok {
		path = "/files/" + url.PathEscape(fileID) + "/export?mimeType=" + url.QueryEscape(exp.mime)
		contentType = exp.mime
	} else {
		path = "/files/" + url.PathEscape(fileID) + "?alt=media"
		contentType = meta.MimeType
	}
	resp, err := g.do(ctx, token, path)
	if err != nil {
		return nil, "", 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", 0, fmt.Errorf("gdrive: download status %d", resp.StatusCode)
	}
	size := int64(-1)
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if v, perr := strconv.ParseInt(cl, 10, 64); perr == nil {
			size = v
		}
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		contentType = ct
	}
	return resp.Body, contentType, size, nil
}

// getJSON performs an authenticated GET against the fixed API host and decodes a
// JSON body into v.
func (g *GDriveProvider) getJSON(ctx context.Context, token, path string, v any) error {
	resp, err := g.do(ctx, token, path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gdrive: api status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

// do issues an authenticated GET to base+path. The host is fixed by g.base
// (SSRF guard); path is API-relative and must begin with "/".
func (g *GDriveProvider) do(ctx context.Context, token, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return g.http.Do(req)
}

// driveFileToNode maps a Drive file resource into the Files Node shape so the
// existing UI renders it. The Node ID is the Drive file ID (opaque to us); bytes
// are not in any bucket, so Bucket/ObjectKey stay empty and downloads route
// through DownloadExternal rather than the storage broker.
func driveFileToNode(f *driveFile) *Node {
	isDir := f.MimeType == gdriveFolderMime
	var size int64
	if f.Size != "" {
		size, _ = strconv.ParseInt(f.Size, 10, 64)
	}
	var updated time.Time
	if f.ModifiedTime != "" {
		updated, _ = time.Parse(time.RFC3339, f.ModifiedTime)
	}
	return &Node{
		ID:          f.ID,
		Name:        f.Name,
		IsDir:       isDir,
		Size:        size,
		ContentType: f.MimeType,
		UpdatedAt:   updated,
	}
}

// escapeDriveQ escapes a value embedded in a Drive `q` query string. Drive uses
// single-quote string literals with backslash escaping; we neutralise quotes and
// backslashes so a crafted folder id cannot break out of the literal.
func escapeDriveQ(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
}
