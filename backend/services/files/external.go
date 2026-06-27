package files

// external.go — FILES PHASE 4: external stores mounted as virtual "drives".
//
// A user can connect an external store (Google Drive in Wave 1) so it appears in
// the Files app alongside their per-user bucket Drive. The OS NEVER holds the
// provider's long-lived refresh token: it mints a SHORT-LIVED access token on
// demand from the cloud integration broker (the same INTEG-SEC-01 path used for
// app integration injection) and uses it for exactly one provider API call, then
// drops it. Tokens are never persisted to the Files DB.
//
// What the DB stores is just the MOUNT: a (owner, provider, name) row in
// files_external_mounts — a pointer to "this user has connected provider X".
// All secrets stay in the CP broker. Listing/reading hits the provider's API
// through an ExternalProvider implementation that is SSRF-safe (it talks ONLY to
// the provider's fixed API host — see gdrive.go).
//
// Open-core / degrade: external mounts require the integration broker (cloud) or
// BYO provider creds. When the seam is not wired (TokenSource nil or no
// providers registered) every external method returns ErrExternalUnavailable and
// the Files app disables the "Connect" action. Local Drive + peer-share are
// completely unaffected — there is NO hard cloud dependency in core.
//
// Read-first scope: list folders/files + download. Write support (create/upload
// into the external store) is deliberately deferred — see ProviderReadOnly and
// the report. The provider interface leaves room for it without a schema change.

import (
	"context"
	"errors"
	"fmt"
	"io"
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
)

// ExternalMount is a user's connection to an external store, surfaced as a
// virtual drive. It is a pointer only — no provider secrets live here.
type ExternalMount struct {
	ID        string    `json:"id"`
	OwnerID   string    `json:"owner_id"`
	Provider  string    `json:"provider"` // mount kind, e.g. "gdrive"
	Name      string    `json:"name"`     // display name, e.g. "Google Drive"
	CreatedAt time.Time `json:"created_at"`
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

// ExternalProvider lists and reads from one kind of external store given a
// short-lived bearer token. Implementations MUST be SSRF-safe: they may contact
// ONLY the provider's fixed API host(s), never an attacker-influenced URL.
type ExternalProvider interface {
	// Kind is the mount provider id this implementation serves (e.g. "gdrive").
	Kind() string
	// IntegrationProvider is the CP integration token provider to mint for this
	// store (e.g. "google" — the Drive access token rides the google integration).
	IntegrationProvider() string
	// DisplayName is the default human label for a new mount (e.g. "Google Drive").
	DisplayName() string
	// List returns the children of folderID (empty ⇒ provider root) mapped into
	// the Files Node shape so the existing UI renders them. token is a fresh
	// short-lived bearer.
	List(ctx context.Context, token, folderID string) ([]*Node, error)
	// Download opens a file's bytes. Returns (reader, contentType, size). size may
	// be -1 when the provider does not report it up front.
	Download(ctx context.Context, token, fileID string) (io.ReadCloser, string, int64, error)
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
		out = append(out, ProviderInfo{Kind: kind, DisplayName: p.DisplayName()})
	}
	return out
}

// ProviderInfo describes a connectable external provider for the UI.
type ProviderInfo struct {
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name"`
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

// ConnectExternal records a mount for userID against provider. It first verifies
// the account can actually mint a token for the provider's integration (i.e. the
// user has connected it CP-side) so we never create a dead mount. name defaults
// to the provider's display name. Returns the created mount.
func (s *Service) ConnectExternal(ctx context.Context, userID, provider, name string) (*ExternalMount, error) {
	p, err := s.provider(provider)
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
		CreatedAt: time.Now(),
	}
	if err := s.insertMount(m); err != nil {
		return nil, err
	}
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
	return s.listMounts(userID)
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
	tok, err := s.mintExternal(ctx, p)
	if err != nil {
		return nil, err
	}
	nodes, err := p.List(ctx, tok, folderID)
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
	tok, err := s.mintExternal(ctx, p)
	if err != nil {
		return nil, "", 0, err
	}
	rc, ct, size, err := p.Download(ctx, tok, fileID)
	if err != nil {
		return nil, "", 0, fmt.Errorf("files: external download: %w", err)
	}
	s.audit(userID, "external.download", mountID, fileID)
	return rc, ct, size, nil
}
