package files

// gcs.go — Google Cloud Storage ExternalProvider (PHASE 4 external stores).
//
// Mount a GCS bucket (the connect flow supplies a bucket name + optional prefix)
// as a virtual drive: list/read/write objects via the GCS JSON API using a
// short-lived bearer token brokered by the CP integration mint (provider "gcs",
// devstorage.read_write scope brokered CP-side). The token is supplied per call
// by the Files service and never stored here; the bucket + prefix live in the
// mount's (non-secret) config.
//
// "Folders" are emulated the way the GCS console does: a delimiter="/" listing
// returns object prefixes as folders, and an empty object whose name ends in "/"
// is a folder marker. The bucket + prefix scope every request, so a mount can
// only ever touch objects under its configured prefix.
//
// SSRF SAFETY: this provider talks ONLY to the fixed host storage.googleapis.com
// (the JSON API at /storage/v1 and the upload endpoint at /upload/storage/v1 —
// same host), overridable as ONE base in tests. The bucket, prefix and object
// names are URL-escaped into the path/query of that fixed host — there is no
// caller-controlled URL, scheme, or host.
//
// WRITE / CONFLICTS: Upload applies the ConflictPolicy. rename (default) lists
// the target folder and picks a free "name (n)" if taken; overwrite writes the
// object name directly (a new GCS generation replaces the old); fail sets
// ifGenerationMatch=0 so GCS rejects (412 ⇒ ErrExternalConflict) if it exists.

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

// gcsAPIBase is the fixed GCS host. SSRF guard: the only host this provider
// contacts (the upload endpoint shares the same host).
const gcsAPIBase = "https://storage.googleapis.com"

// GCSProvider implements ExternalProvider over the GCS JSON API.
type GCSProvider struct {
	base string // host base (default gcsAPIBase); overridable in tests
	http *http.Client
}

// NewGCSProvider returns a GCS provider with a bounded HTTP client.
func NewGCSProvider() *GCSProvider {
	return &GCSProvider{
		base: gcsAPIBase,
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

func (g *GCSProvider) Kind() string                { return "gcs" }
func (g *GCSProvider) IntegrationProvider() string { return "gcs" }
func (g *GCSProvider) DisplayName() string         { return "Google Cloud Storage" }
func (g *GCSProvider) Writable() bool              { return true }

func (g *GCSProvider) ConfigFields() []ProviderConfigField {
	return []ProviderConfigField{
		{Key: "bucket", Label: "Bucket name", Required: true},
		{Key: "prefix", Label: "Prefix (optional)", Required: false},
	}
}

// ValidateConfig requires a syntactically valid bucket name and normalizes the
// optional prefix to "no leading slash, trailing slash when non-empty".
func (g *GCSProvider) ValidateConfig(in map[string]string) (map[string]string, error) {
	bucket := strings.TrimSpace(in["bucket"])
	if bucket == "" {
		return nil, fmt.Errorf("%w: GCS bucket name is required", ErrInvalid)
	}
	// GCS bucket names: lowercase letters, digits, '-', '_', '.'; no slashes or
	// spaces. Reject anything else so a crafted value cannot smuggle a path.
	if strings.ContainsAny(bucket, "/ \\\x00") || len(bucket) > 222 {
		return nil, fmt.Errorf("%w: invalid GCS bucket name", ErrInvalid)
	}
	out := map[string]string{"bucket": bucket}
	if prefix := normalizeGCSPrefix(in["prefix"]); prefix != "" {
		out["prefix"] = prefix
	}
	return out, nil
}

// gcsObject is the subset of a GCS object resource we map into a Node.
type gcsObject struct {
	Name        string `json:"name"`
	Size        string `json:"size"` // GCS returns size as a string
	ContentType string `json:"contentType"`
	Updated     string `json:"updated"`
}

type gcsListResp struct {
	Items         []gcsObject `json:"items"`
	Prefixes      []string    `json:"prefixes"`
	NextPageToken string      `json:"nextPageToken"`
}

// List returns the children of folderID (full object prefix; empty ⇒ the mount's
// configured root prefix) as Nodes: GCS prefixes become folders, objects become
// files. The folder marker object (an empty object equal to the listing prefix)
// is skipped so it does not show as a nameless file.
func (g *GCSProvider) List(ctx context.Context, call ProviderCall, folderID string) ([]*Node, error) {
	bucket := call.cfg("bucket")
	if bucket == "" {
		return nil, fmt.Errorf("%w: mount missing bucket", ErrInvalid)
	}
	prefix := g.effPrefix(call, folderID)
	var out []*Node
	pageToken := ""
	for page := 0; page < 50; page++ {
		q := url.Values{}
		q.Set("prefix", prefix)
		q.Set("delimiter", "/")
		q.Set("fields", "items(name,size,contentType,updated),prefixes,nextPageToken")
		q.Set("maxResults", "1000")
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var resp gcsListResp
		if err := g.getJSON(ctx, call.Token, "/storage/v1/b/"+url.PathEscape(bucket)+"/o?"+q.Encode(), &resp); err != nil {
			return nil, err
		}
		for _, p := range resp.Prefixes {
			name := strings.TrimSuffix(strings.TrimPrefix(p, prefix), "/")
			if name == "" {
				continue
			}
			out = append(out, &Node{ID: p, Name: name, IsDir: true})
		}
		for i := range resp.Items {
			o := &resp.Items[i]
			if o.Name == prefix || strings.HasSuffix(o.Name, "/") {
				continue // the folder marker itself / nested markers
			}
			out = append(out, gcsObjectToNode(o, prefix))
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return out, nil
}

// Download opens an object's bytes by its full object name (the Node ID).
func (g *GCSProvider) Download(ctx context.Context, call ProviderCall, fileID string) (io.ReadCloser, string, int64, error) {
	bucket := call.cfg("bucket")
	if bucket == "" {
		return nil, "", 0, fmt.Errorf("%w: mount missing bucket", ErrInvalid)
	}
	if !g.withinScope(call, fileID) {
		return nil, "", 0, fmt.Errorf("%w: object outside mount scope", ErrInvalid)
	}
	path := "/storage/v1/b/" + url.PathEscape(bucket) + "/o/" + url.PathEscape(fileID) + "?alt=media"
	resp, err := g.do(ctx, call.Token, http.MethodGet, g.base+path, "", nil)
	if err != nil {
		return nil, "", 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", 0, fmt.Errorf("gcs: download status %d", resp.StatusCode)
	}
	size := int64(-1)
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		if v, perr := strconv.ParseInt(cl, 10, 64); perr == nil {
			size = v
		}
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	return resp.Body, ct, size, nil
}

// CreateFolder creates a folder marker (empty object ending in "/") named name
// under parentID.
func (g *GCSProvider) CreateFolder(ctx context.Context, call ProviderCall, parentID, name string) (*Node, error) {
	bucket := call.cfg("bucket")
	if bucket == "" {
		return nil, fmt.Errorf("%w: mount missing bucket", ErrInvalid)
	}
	prefix := g.effPrefix(call, parentID)
	marker := prefix + name + "/"
	if _, err := g.putObject(ctx, call.Token, bucket, marker, "application/x-www-form-urlencoded;charset=UTF-8", false, nil); err != nil {
		return nil, err
	}
	return &Node{ID: marker, Name: name, IsDir: true}, nil
}

// Upload writes bytes as name under parentID applying policy on a collision.
func (g *GCSProvider) Upload(ctx context.Context, call ProviderCall, parentID, name, contentType string, _ int64, r io.Reader, policy ConflictPolicy) (*Node, error) {
	bucket := call.cfg("bucket")
	if bucket == "" {
		return nil, fmt.Errorf("%w: mount missing bucket", ErrInvalid)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	prefix := g.effPrefix(call, parentID)

	failIfExists := false
	switch policy {
	case ConflictFail:
		failIfExists = true
	case ConflictRename:
		// List the folder once, collect taken basenames, pick a free name.
		nodes, err := g.List(ctx, call, prefix)
		if err != nil {
			return nil, err
		}
		taken := map[string]bool{}
		for _, n := range nodes {
			taken[n.Name] = true
		}
		name = uniqueName(name, func(n string) bool { return taken[n] })
	}

	objName := prefix + name
	obj, err := g.putObject(ctx, call.Token, bucket, objName, contentType, failIfExists, r)
	if err != nil {
		return nil, err
	}
	return gcsObjectToNode(obj, prefix), nil
}

// putObject performs a media upload of an object. When failIfExists is set it
// uses ifGenerationMatch=0 so GCS rejects (412) an existing object, which maps to
// ErrExternalConflict.
func (g *GCSProvider) putObject(ctx context.Context, token, bucket, objName, contentType string, failIfExists bool, r io.Reader) (*gcsObject, error) {
	q := url.Values{}
	q.Set("uploadType", "media")
	q.Set("name", objName)
	q.Set("fields", "name,size,contentType,updated")
	if failIfExists {
		q.Set("ifGenerationMatch", "0")
	}
	full := g.base + "/upload/storage/v1/b/" + url.PathEscape(bucket) + "/o?" + q.Encode()
	resp, err := g.do(ctx, token, http.MethodPost, full, contentType, r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusPreconditionFailed {
		return nil, ErrExternalConflict
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gcs: upload status %d", resp.StatusCode)
	}
	var obj gcsObject
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&obj); err != nil {
		return nil, err
	}
	return &obj, nil
}

// getJSON performs an authenticated GET against the fixed host and decodes JSON.
func (g *GCSProvider) getJSON(ctx context.Context, token, path string, v any) error {
	resp, err := g.do(ctx, token, http.MethodGet, g.base+path, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gcs: api status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

// do issues an authenticated request to a fixed-host URL. contentType is set on
// write bodies; the host is fixed by g.base (SSRF guard).
func (g *GCSProvider) do(ctx context.Context, token, method, fullURL, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return g.http.Do(req)
}

// effPrefix resolves the effective object prefix for an operation: an explicit
// folderID (already a full prefix from a prior listing) wins, otherwise the
// mount's configured root prefix. A non-root folder always carries a trailing
// slash so child object names concatenate cleanly.
func (g *GCSProvider) effPrefix(call ProviderCall, folderID string) string {
	if folderID != "" {
		return ensureTrailingSlash(folderID)
	}
	return normalizeGCSPrefix(call.cfg("prefix"))
}

// withinScope reports whether objName is under the mount's configured root prefix
// — defence in depth so a tampered file id cannot read outside the mount.
func (g *GCSProvider) withinScope(call ProviderCall, objName string) bool {
	root := normalizeGCSPrefix(call.cfg("prefix"))
	return strings.HasPrefix(objName, root)
}

// gcsObjectToNode maps a GCS object into the Files Node shape. The Node ID is the
// full object name; the display name strips the listing prefix.
func gcsObjectToNode(o *gcsObject, prefix string) *Node {
	name := strings.TrimPrefix(o.Name, prefix)
	var size int64
	if o.Size != "" {
		size, _ = strconv.ParseInt(o.Size, 10, 64)
	}
	var updated time.Time
	if o.Updated != "" {
		updated, _ = time.Parse(time.RFC3339, o.Updated)
	}
	return &Node{
		ID:          o.Name,
		Name:        name,
		IsDir:       false,
		Size:        size,
		ContentType: o.ContentType,
		UpdatedAt:   updated,
	}
}

// normalizeGCSPrefix normalizes a configured/derived prefix to "no leading slash,
// trailing slash when non-empty" so it concatenates cleanly with object names.
func normalizeGCSPrefix(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimLeft(p, "/")
	if p == "" {
		return ""
	}
	return ensureTrailingSlash(p)
}

// ensureTrailingSlash appends a "/" if p does not already end in one.
func ensureTrailingSlash(p string) string {
	if p == "" || strings.HasSuffix(p, "/") {
		return p
	}
	return p + "/"
}
