package files

// gdrive.go — Google Drive ExternalProvider (PHASE 4 external stores).
//
// list folders/files, download bytes, AND (PHASE 4 write) create folders +
// upload files via the Google Drive v3 API using a short-lived bearer token
// brokered by the CP integration mint (provider "google"). Writes use the
// drive.file write scope brokered CP-side; the token is supplied per call by the
// Files service and never stored here.
//
// SSRF SAFETY: this provider talks ONLY to fixed Google API hosts — the Drive v3
// API base (https://www.googleapis.com/drive/v3) and the matching upload base
// (https://www.googleapis.com/upload/drive/v3), both overridable as ONE host in
// tests. File and folder identifiers are Drive opaque IDs placed into the
// path/query of those fixed hosts — there is no caller-controlled URL, scheme,
// or host, so a malicious mount or item id can never redirect the request
// elsewhere.
//
// WRITE / CONFLICTS: Upload applies a ConflictPolicy. rename (default) lists the
// target folder for the name and, on a hit, uploads under "name (n)"; overwrite
// updates the bytes of the existing file IN PLACE by its Drive id (a new revision
// — never a duplicate); fail returns ErrExternalConflict. CreateFolder posts a
// folder resource parented under parentID.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// gdriveAPIBase is the fixed Google Drive v3 API host. SSRF guard: the only host
// this provider ever contacts (the upload base shares the same host).
const gdriveAPIBase = "https://www.googleapis.com/drive/v3"

// gdriveUploadBase is the fixed Drive v3 upload endpoint (same host as the API).
const gdriveUploadBase = "https://www.googleapis.com/upload/drive/v3"

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

// gdriveImportExportMap maps Google-native doc mime types to the portable Office
// (or image) format used ON IMPORT. Unlike gdriveExportMap (which uses PDF for
// the live-view mount download), import preserves an EDITABLE format so the owned
// copy round-trips into Ofisi: Docs→docx, Sheets→xlsx, Slides→pptx,
// Drawings→png. A native doc has no media bytes, so it MUST be exported; regular
// files are copied as-is.
var gdriveImportExportMap = map[string]struct {
	mime, ext string
}{
	"application/vnd.google-apps.document":     {"application/vnd.openxmlformats-officedocument.wordprocessingml.document", ".docx"},
	"application/vnd.google-apps.spreadsheet":  {"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".xlsx"},
	"application/vnd.google-apps.presentation": {"application/vnd.openxmlformats-officedocument.presentationml.presentation", ".pptx"},
	"application/vnd.google-apps.drawing":      {"image/png", ".png"},
}

// GDriveProvider implements ExternalProvider over the Google Drive v3 API.
type GDriveProvider struct {
	base       string       // API base (default gdriveAPIBase); overridable in tests
	uploadBase string       // upload base (default gdriveUploadBase); overridable in tests
	http       *http.Client // bounded client; never follows to arbitrary hosts
}

// NewGDriveProvider returns a Google Drive provider with a bounded HTTP client.
func NewGDriveProvider() *GDriveProvider {
	return &GDriveProvider{
		base:       gdriveAPIBase,
		uploadBase: gdriveUploadBase,
		http:       &http.Client{Timeout: 60 * time.Second},
	}
}

func (g *GDriveProvider) Kind() string                        { return "gdrive" }
func (g *GDriveProvider) IntegrationProvider() string         { return "google" }
func (g *GDriveProvider) DisplayName() string                 { return "Google Drive" }
func (g *GDriveProvider) Writable() bool                      { return true }
func (g *GDriveProvider) ConfigFields() []ProviderConfigField { return nil }
func (g *GDriveProvider) ValidateConfig(map[string]string) (map[string]string, error) {
	return nil, nil
}

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
func (g *GDriveProvider) List(ctx context.Context, call ProviderCall, folderID string) ([]*Node, error) {
	files, err := g.listFiles(ctx, call.Token, folderID)
	if err != nil {
		return nil, err
	}
	out := make([]*Node, 0, len(files))
	for i := range files {
		out = append(out, driveFileToNode(&files[i]))
	}
	return out, nil
}

// listFiles returns the raw Drive file resources under folderID.
func (g *GDriveProvider) listFiles(ctx context.Context, token, folderID string) ([]driveFile, error) {
	if folderID == "" {
		folderID = "root"
	}
	var out []driveFile
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
		out = append(out, resp.Files...)
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return out, nil
}

// findChild returns the first non-trashed child of folderID named name, or nil.
func (g *GDriveProvider) findChild(ctx context.Context, token, folderID, name string) (*driveFile, error) {
	files, err := g.listFiles(ctx, token, folderID)
	if err != nil {
		return nil, err
	}
	for i := range files {
		if files[i].Name == name {
			return &files[i], nil
		}
	}
	return nil, nil
}

// Download opens a file's bytes. Native Google docs are exported to a portable
// format; regular files are fetched via alt=media.
func (g *GDriveProvider) Download(ctx context.Context, call ProviderCall, fileID string) (io.ReadCloser, string, int64, error) {
	token := call.Token
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

// CreateFolder creates a folder named name under parentID (empty ⇒ "root").
func (g *GDriveProvider) CreateFolder(ctx context.Context, call ProviderCall, parentID, name string) (*Node, error) {
	if parentID == "" {
		parentID = "root"
	}
	meta := map[string]any{
		"name":     name,
		"mimeType": gdriveFolderMime,
		"parents":  []string{parentID},
	}
	body, _ := json.Marshal(meta)
	var f driveFile
	if err := g.postJSON(ctx, call.Token, g.base+"/files?fields=id,name,mimeType,size,modifiedTime", "application/json", bytes.NewReader(body), &f); err != nil {
		return nil, err
	}
	return driveFileToNode(&f), nil
}

// Upload writes bytes as name under parentID applying policy on a collision.
func (g *GDriveProvider) Upload(ctx context.Context, call ProviderCall, parentID, name, contentType string, _ int64, r io.Reader, policy ConflictPolicy) (*Node, error) {
	token := call.Token
	if parentID == "" {
		parentID = "root"
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	// Resolve the collision policy by inspecting the target folder.
	existing, err := g.findChild(ctx, token, parentID, name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		switch policy {
		case ConflictFail:
			return nil, ErrExternalConflict
		case ConflictOverwrite:
			return g.uploadMedia(ctx, token, http.MethodPatch,
				g.uploadBase+"/files/"+url.PathEscape(existing.ID)+"?uploadType=multipart&fields=id,name,mimeType,size,modifiedTime",
				nil, name, contentType, r)
		default: // ConflictRename — pick a free name in this folder.
			files, lerr := g.listFiles(ctx, token, parentID)
			if lerr != nil {
				return nil, lerr
			}
			taken := map[string]bool{}
			for i := range files {
				taken[files[i].Name] = true
			}
			name = uniqueName(name, func(n string) bool { return taken[n] })
		}
	}

	// Create a new file parented under parentID (multipart: metadata + media).
	return g.uploadMedia(ctx, token, http.MethodPost,
		g.uploadBase+"/files?uploadType=multipart&fields=id,name,mimeType,size,modifiedTime",
		[]string{parentID}, name, contentType, r)
}

// uploadMedia performs a Drive v3 multipart upload (metadata part + media part).
// parents is set only when creating (POST); an in-place overwrite (PATCH) passes
// nil parents so the file stays where it is.
func (g *GDriveProvider) uploadMedia(ctx context.Context, token, method, fullURL string, parents []string, name, contentType string, r io.Reader) (*Node, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	// Part 1: file metadata as JSON.
	meta := map[string]any{"name": name}
	if len(parents) > 0 {
		meta["parents"] = parents
	}
	metaJSON, _ := json.Marshal(meta)
	metaHdr := textproto.MIMEHeader{"Content-Type": {"application/json; charset=UTF-8"}}
	mp, err := mw.CreatePart(metaHdr)
	if err != nil {
		return nil, err
	}
	if _, err := mp.Write(metaJSON); err != nil {
		return nil, err
	}

	// Part 2: media bytes.
	mediaHdr := textproto.MIMEHeader{"Content-Type": {contentType}}
	med, err := mw.CreatePart(mediaHdr)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(med, r); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	ctHeader := "multipart/related; boundary=" + mw.Boundary()
	var f driveFile
	if err := g.postJSONMethod(ctx, token, method, fullURL, ctHeader, &buf, &f); err != nil {
		return nil, err
	}
	return driveFileToNode(&f), nil
}

// OpenForImport opens a file's bytes for the IMPORT engine. A Google-native
// document is EXPORTED to a portable Office/image format (and its name gets the
// matching extension) so the imported copy is editable; a regular file streams
// via alt=media unchanged. The mime type comes from the listed node's
// ContentType (driveFileToNode stamps the Drive mimeType there). Implements
// files.ImportSource alongside the existing Kind/IntegrationProvider/List.
func (g *GDriveProvider) OpenForImport(ctx context.Context, call ProviderCall, file *Node) (io.ReadCloser, string, string, int64, error) {
	token := call.Token
	name := file.Name
	var apiPath, contentType string
	if exp, ok := gdriveImportExportMap[file.ContentType]; ok {
		apiPath = "/files/" + url.PathEscape(file.ID) + "/export?mimeType=" + url.QueryEscape(exp.mime)
		contentType = exp.mime
		if !strings.HasSuffix(strings.ToLower(name), exp.ext) {
			name += exp.ext
		}
	} else {
		apiPath = "/files/" + url.PathEscape(file.ID) + "?alt=media"
		contentType = file.ContentType
	}
	resp, err := g.do(ctx, token, apiPath)
	if err != nil {
		return nil, "", "", 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", "", 0, fmt.Errorf("gdrive: import open status %d", resp.StatusCode)
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
	return resp.Body, name, contentType, size, nil
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

// postJSON POSTs a body to a fixed-host URL and decodes the JSON response.
func (g *GDriveProvider) postJSON(ctx context.Context, token, fullURL, contentType string, body io.Reader, v any) error {
	return g.postJSONMethod(ctx, token, http.MethodPost, fullURL, contentType, body, v)
}

// postJSONMethod issues an authenticated write (POST/PATCH) to a fixed-host URL
// and decodes the JSON response into v.
func (g *GDriveProvider) postJSONMethod(ctx context.Context, token, method, fullURL, contentType string, body io.Reader, v any) error {
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gdrive: write status %d", resp.StatusCode)
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
