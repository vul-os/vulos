package files

// external.go — FILES PHASE 4: external stores mounted as virtual "drives".
//
// A user can connect an external store (Google Drive, Dropbox, Google Cloud
// Storage) so it appears in the Files app alongside their per-user bucket Drive.
// The OS NEVER holds the provider's long-lived refresh token: it mints a
// SHORT-LIVED access token on demand from the cloud integration broker (the same
// INTEG-SEC-01 path used for app integration injection) and uses it for exactly
// one provider API call, then drops it. Tokens are never persisted to the Files
// DB.
//
// What the DB stores is just the MOUNT: a (owner, provider, name, config) row in
// files_external_mounts — a pointer to "this user has connected provider X",
// plus opaque non-secret config (e.g. a GCS bucket + prefix). All secrets stay
// in the CP broker. Listing/reading/writing hits the provider's API through an
// ExternalProvider implementation that is SSRF-safe (it talks ONLY to the
// provider's fixed API host(s) — see gdrive.go, dropbox.go, gcs.go).
//
// Open-core / degrade: external mounts require the integration broker (cloud) or
// BYO provider creds. When the seam is not wired (TokenSource nil or no
// providers registered) every external method returns ErrExternalUnavailable and
// the Files app disables the "Connect" action. Local Drive + peer-share are
// completely unaffected — there is NO hard cloud dependency in core.
//
// WRITE support: providers that report Writable() expose create-folder + upload
// in addition to list + download. Writes apply a ConflictPolicy (rename-on-
// collision by default; overwrite or fail on request) so an upload can never
// silently clobber an existing object unless the caller asks for it.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"vulos/backend/internal/ulid"
)

// Sentinel errors for the external-mount surface. ErrExternalUnavailable means
// the seam is not wired (open-core standalone, or no broker) — callers map it to
// a "not available" UI state, never a hard failure of the rest of Files.
var (
	ErrExternalUnavailable  = errors.New("files: external mounts unavailable")
	ErrExternalProvider     = errors.New("files: unknown external provider")
	ErrExternalNotConnected = errors.New("files: external account not connected")
	ErrExternalReadOnly     = errors.New("files: external store is read-only")
	ErrExternalConflict     = errors.New("files: external name already exists")
)

// ConflictPolicy controls what an upload/create does when an item of the same
// name already exists in the target folder.
//
//   - ConflictRename (default): keep both — the new item is renamed with a
//     " (n)" suffix. Safe and non-destructive; nothing is ever overwritten.
//   - ConflictOverwrite: replace the existing item's bytes in place (by the
//     provider's own id/object key). Explicit, destructive-by-request.
//   - ConflictFail: do nothing and return ErrExternalConflict.
type ConflictPolicy string

const (
	ConflictRename    ConflictPolicy = "rename"
	ConflictOverwrite ConflictPolicy = "overwrite"
	ConflictFail      ConflictPolicy = "fail"
)

// normalizeConflict maps an unset/unknown policy to the safe default (rename).
func normalizeConflict(p ConflictPolicy) ConflictPolicy {
	switch p {
	case ConflictOverwrite, ConflictFail:
		return p
	default:
		return ConflictRename
	}
}

// ExternalMount is a user's connection to an external store, surfaced as a
// virtual drive. It is a pointer only — no provider secrets live here. Config is
// opaque, non-secret per-mount data (e.g. GCS bucket/prefix) persisted as JSON.
// Writable is derived from the provider at read time (not stored).
type ExternalMount struct {
	ID        string            `json:"id"`
	OwnerID   string            `json:"owner_id"`
	Provider  string            `json:"provider"` // mount kind, e.g. "gdrive"
	Name      string            `json:"name"`     // display name, e.g. "Google Drive"
	Config    map[string]string `json:"config,omitempty"`
	Writable  bool              `json:"writable"`
	CreatedAt time.Time         `json:"created_at"`
}

// TokenSource mints a short-lived bearer access token for an external integration
// provider (e.g. "google"). In production it is backed by the cloud integration
// broker client (integrations.Client.MintToken); nil ⇒ external mounts are
// unavailable. The returned token is used for a single provider call and dropped
// — it is NEVER persisted.
type TokenSource interface {
	// MintToken returns a short-lived access token for integrationProvider, or an
	// error. A "not connected" condition should surface so Connect can report it.
	MintToken(ctx context.Context, integrationProvider string) (string, error)
}

// ProviderCall carries the per-call context for an ExternalProvider operation: a
// fresh short-lived bearer Token plus the mount's opaque Config (e.g. the GCS
// bucket/prefix). Token is never persisted; Config is the stored mount row.
type ProviderCall struct {
	Token  string
	Config map[string]string
}

// cfg returns the config value for key, or "".
func (c ProviderCall) cfg(key string) string { return c.Config[key] }

// ExternalProvider lists, reads and (when Writable) writes one kind of external
// store given a short-lived bearer token. Implementations MUST be SSRF-safe:
// they may contact ONLY the provider's fixed API host(s), never an attacker-
// influenced URL.
type ExternalProvider interface {
	// Kind is the mount provider id this implementation serves (e.g. "gdrive").
	Kind() string
	// IntegrationProvider is the CP integration token provider to mint for this
	// store (e.g. "google" for Drive, "dropbox", "gcs"). The CP broker maps it to
	// the scope set the store needs (Drive write, Dropbox, devstorage).
	IntegrationProvider() string
	// DisplayName is the default human label for a new mount (e.g. "Google Drive").
	DisplayName() string
	// Writable reports whether this provider supports CreateFolder + Upload.
	Writable() bool
	// ConfigFields describes connect-time config the UI must collect (e.g. a GCS
	// bucket name). Empty for providers that need none.
	ConfigFields() []ProviderConfigField
	// ValidateConfig validates + normalizes connect-time config, returning the
	// config to persist (no secrets). Providers that need none return (nil, nil).
	ValidateConfig(in map[string]string) (map[string]string, error)
	// List returns the children of folderID (empty ⇒ provider/config root) mapped
	// into the Files Node shape so the existing UI renders them.
	List(ctx context.Context, call ProviderCall, folderID string) ([]*Node, error)
	// Download opens a file's bytes. Returns (reader, contentType, size). size may
	// be -1 when the provider does not report it up front.
	Download(ctx context.Context, call ProviderCall, fileID string) (io.ReadCloser, string, int64, error)
	// CreateFolder creates a folder named name under parentID (empty ⇒ root) and
	// returns the created Node. Writable providers only.
	CreateFolder(ctx context.Context, call ProviderCall, parentID, name string) (*Node, error)
	// Upload writes size bytes from r as name under parentID, applying policy on a
	// name collision, and returns the resulting Node. Writable providers only.
	Upload(ctx context.Context, call ProviderCall, parentID, name, contentType string, size int64, r io.Reader, policy ConflictPolicy) (*Node, error)
}

// ProviderConfigField describes one connect-time config input for a provider.
type ProviderConfigField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Required bool   `json:"required"`
}

// ProviderInfo describes a connectable external provider for the UI.
type ProviderInfo struct {
	Kind         string                `json:"kind"`
	DisplayName  string                `json:"display_name"`
	Writable     bool                  `json:"writable"`
	ConfigFields []ProviderConfigField `json:"config_fields,omitempty"`
}

// WithExternal wires the external-store seam onto the Service. tokenSource mints
// short-lived provider tokens; providers are the available store kinds. Either
// being empty/nil leaves external mounts unavailable (ErrExternalUnavailable).
// Returns s for chaining.
func (s *Service) WithExternal(tokenSource TokenSource, providers ...ExternalProvider) *Service {
	s.extTokens = tokenSource
	if s.extProviders == nil {
		s.extProviders = map[string]ExternalProvider{}
	}
	for _, p := range providers {
		if p != nil {
			s.extProviders[p.Kind()] = p
		}
	}
	return s
}

// ExternalEnabled reports whether the external-mount seam is wired (a token
// source AND at least one provider). The Files app uses this to enable/disable
// the "Connect" action.
func (s *Service) ExternalEnabled() bool {
	return s.extTokens != nil && len(s.extProviders) > 0
}

// AvailableProviders returns the registered provider kinds (for the UI's connect
// menu). Empty when the seam is not wired.
func (s *Service) AvailableProviders() []ProviderInfo {
	if !s.ExternalEnabled() {
		return nil
	}
	out := make([]ProviderInfo, 0, len(s.extProviders))
	for kind, p := range s.extProviders {
		out = append(out, ProviderInfo{
			Kind:         kind,
			DisplayName:  p.DisplayName(),
			Writable:     p.Writable(),
			ConfigFields: p.ConfigFields(),
		})
	}
	return out
}

// provider resolves a registered provider by kind, or ErrExternalProvider.
func (s *Service) provider(kind string) (ExternalProvider, error) {
	if !s.ExternalEnabled() {
		return nil, ErrExternalUnavailable
	}
	p, ok := s.extProviders[kind]
	if !ok {
		return nil, ErrExternalProvider
	}
	return p, nil
}

// stampMount fills the derived (non-stored) fields on a mount, currently the
// Writable flag from its provider. Unknown providers stay non-writable.
func (s *Service) stampMount(m *ExternalMount) {
	if p, ok := s.extProviders[m.Provider]; ok {
		m.Writable = p.Writable()
	}
}

// mintExternal mints a fresh short-lived token for a provider's integration. A
// "not connected" result is translated to ErrExternalNotConnected so callers can
// distinguish it from a transport failure.
func (s *Service) mintExternal(ctx context.Context, p ExternalProvider) (string, error) {
	if s.extTokens == nil {
		return "", ErrExternalUnavailable
	}
	tok, err := s.extTokens.MintToken(ctx, p.IntegrationProvider())
	if err != nil {
		return "", err
	}
	if tok == "" {
		return "", ErrExternalNotConnected
	}
	return tok, nil
}

// callFor mints a fresh token and packages it with the mount's config so the
// provider has everything it needs for one call.
func (s *Service) callFor(ctx context.Context, p ExternalProvider, m *ExternalMount) (ProviderCall, error) {
	tok, err := s.mintExternal(ctx, p)
	if err != nil {
		return ProviderCall{}, err
	}
	return ProviderCall{Token: tok, Config: m.Config}, nil
}

// ConnectExternal records a mount for userID against provider. It validates the
// supplied config against the provider, then verifies the account can actually
// mint a token for the provider's integration (i.e. the user has connected it
// CP-side) so we never create a dead mount. name defaults to the provider's
// display name. Returns the created mount.
func (s *Service) ConnectExternal(ctx context.Context, userID, provider, name string, config map[string]string) (*ExternalMount, error) {
	p, err := s.provider(provider)
	if err != nil {
		return nil, err
	}
	cfg, err := p.ValidateConfig(config)
	if err != nil {
		return nil, err
	}
	// Prove connectivity: a successful mint means the CP broker has a refresh
	// token for this account/provider. The token itself is discarded here.
	if _, err := s.mintExternal(ctx, p); err != nil {
		return nil, err
	}
	if name == "" {
		name = p.DisplayName()
	}
	if err := validName(name); err != nil {
		return nil, err
	}
	m := &ExternalMount{
		ID:        ulid.NewULID(),
		OwnerID:   userID,
		Provider:  provider,
		Name:      name,
		Config:    cfg,
		CreatedAt: time.Now(),
	}
	if err := s.insertMount(m); err != nil {
		return nil, err
	}
	s.stampMount(m)
	s.audit(userID, "external.connect", m.ID, provider)
	return m, nil
}

// ListExternalMounts returns userID's connected external drives (newest first).
// Returns an empty slice (not an error) when the seam is wired but the user has
// connected nothing. When the seam is not wired it returns nil, nil so the UI
// simply shows no external drives.
func (s *Service) ListExternalMounts(userID string) ([]ExternalMount, error) {
	if !s.ExternalEnabled() {
		return nil, nil
	}
	mounts, err := s.listMounts(userID)
	if err != nil {
		return nil, err
	}
	for i := range mounts {
		s.stampMount(&mounts[i])
	}
	return mounts, nil
}

// DisconnectExternal removes a mount owned by userID. The CP-side connection is
// untouched (that is managed in the integrations settings); this only removes
// the virtual drive from Files.
func (s *Service) DisconnectExternal(userID, mountID string) error {
	m, err := s.ownedMount(userID, mountID)
	if err != nil {
		return err
	}
	if err := s.deleteMount(m.ID); err != nil {
		return err
	}
	s.audit(userID, "external.disconnect", m.ID, m.Provider)
	return nil
}

// ownedMount fetches a mount and enforces that userID owns it (404 otherwise, to
// avoid cross-user enumeration).
func (s *Service) ownedMount(userID, mountID string) (*ExternalMount, error) {
	m, err := s.getMount(mountID)
	if err != nil {
		return nil, err
	}
	if m.OwnerID != userID {
		return nil, ErrNotFound
	}
	return m, nil
}

// ListExternal lists the children of folderID (empty ⇒ root) inside the mounted
// external store mountID, mapped into Node shape. Requires the caller to own the
// mount. A fresh short-lived token is minted for the single provider call.
func (s *Service) ListExternal(ctx context.Context, userID, mountID, folderID string) ([]*Node, error) {
	m, err := s.ownedMount(userID, mountID)
	if err != nil {
		return nil, err
	}
	p, err := s.provider(m.Provider)
	if err != nil {
		return nil, err
	}
	call, err := s.callFor(ctx, p, m)
	if err != nil {
		return nil, err
	}
	nodes, err := p.List(ctx, call, folderID)
	if err != nil {
		return nil, fmt.Errorf("files: external list: %w", err)
	}
	// Stamp ownership + the mount as the conceptual parent so the UI can attribute
	// rows. External node IDs are the provider's own file IDs (opaque to us).
	for _, n := range nodes {
		n.OwnerID = userID
		if n.ParentID == "" {
			n.ParentID = mountID
		}
	}
	s.audit(userID, "external.list", mountID, folderID)
	return nodes, nil
}

// DownloadExternal opens a file's bytes from the mounted external store. Requires
// ownership of the mount. The caller MUST Close the returned reader.
func (s *Service) DownloadExternal(ctx context.Context, userID, mountID, fileID string) (io.ReadCloser, string, int64, error) {
	m, err := s.ownedMount(userID, mountID)
	if err != nil {
		return nil, "", 0, err
	}
	p, err := s.provider(m.Provider)
	if err != nil {
		return nil, "", 0, err
	}
	if fileID == "" {
		return nil, "", 0, fmt.Errorf("%w: file id required", ErrInvalid)
	}
	call, err := s.callFor(ctx, p, m)
	if err != nil {
		return nil, "", 0, err
	}
	rc, ct, size, err := p.Download(ctx, call, fileID)
	if err != nil {
		return nil, "", 0, fmt.Errorf("files: external download: %w", err)
	}
	s.audit(userID, "external.download", mountID, fileID)
	return rc, ct, size, nil
}

// writableProvider resolves a writable provider for an owned mount, returning
// ErrExternalReadOnly for read-only providers.
func (s *Service) writableProvider(userID, mountID string) (*ExternalMount, ExternalProvider, error) {
	m, err := s.ownedMount(userID, mountID)
	if err != nil {
		return nil, nil, err
	}
	p, err := s.provider(m.Provider)
	if err != nil {
		return nil, nil, err
	}
	if !p.Writable() {
		return nil, nil, ErrExternalReadOnly
	}
	return m, p, nil
}

// CreateFolderExternal creates a folder named name under parentID (empty ⇒ root)
// in a writable mounted store. Requires ownership; read-only mounts return
// ErrExternalReadOnly.
func (s *Service) CreateFolderExternal(ctx context.Context, userID, mountID, parentID, name string) (*Node, error) {
	m, p, err := s.writableProvider(userID, mountID)
	if err != nil {
		return nil, err
	}
	if err := validName(name); err != nil {
		return nil, err
	}
	call, err := s.callFor(ctx, p, m)
	if err != nil {
		return nil, err
	}
	node, err := p.CreateFolder(ctx, call, parentID, name)
	if err != nil {
		return nil, fmt.Errorf("files: external create folder: %w", err)
	}
	node.OwnerID = userID
	if node.ParentID == "" {
		node.ParentID = mountID
	}
	s.audit(userID, "external.mkdir", mountID, name)
	return node, nil
}

// UploadExternal writes size bytes from r as name under parentID in a writable
// mounted store, applying policy on a name collision. Requires ownership;
// read-only mounts return ErrExternalReadOnly.
func (s *Service) UploadExternal(ctx context.Context, userID, mountID, parentID, name, contentType string, size int64, r io.Reader, policy ConflictPolicy) (*Node, error) {
	m, p, err := s.writableProvider(userID, mountID)
	if err != nil {
		return nil, err
	}
	if err := validName(name); err != nil {
		return nil, err
	}
	call, err := s.callFor(ctx, p, m)
	if err != nil {
		return nil, err
	}
	node, err := p.Upload(ctx, call, parentID, name, contentType, size, r, normalizeConflict(policy))
	if err != nil {
		return nil, fmt.Errorf("files: external upload: %w", err)
	}
	node.OwnerID = userID
	if node.ParentID == "" {
		node.ParentID = mountID
	}
	s.audit(userID, "external.upload", mountID, node.Name)
	return node, nil
}

// uniqueName returns name if exists(name) is false, otherwise the first
// "base (n)<ext>" candidate that does not exist. Shared collision helper for the
// rename ConflictPolicy across providers. The extension is preserved so renamed
// files keep their type association.
func uniqueName(name string, exists func(string) bool) string {
	if !exists(name) {
		return name
	}
	ext := path.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i < 1000; i++ {
		cand := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if !exists(cand) {
			return cand
		}
	}
	// Pathological fallback — astronomically unlikely; keep it collision-proof.
	return fmt.Sprintf("%s (%d)%s", base, time.Now().UnixNano(), ext)
}
