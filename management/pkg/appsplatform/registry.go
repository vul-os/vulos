package appsplatform

import (
	"errors"
	"time"
)

// ErrNotFound is returned by Registry lookups/mutations when no app matches.
var ErrNotFound = errors.New("appsplatform: app not found")

// CreateParams carries the caller-supplied fields for installing an app. The
// registry assigns the id, secrets, and incoming-webhook id.
type CreateParams struct {
	Name            string
	Icon            string
	Description     string
	OwnerID         string
	OrgID           string
	Scopes          []string
	Products        []string
	Events          []string
	SlashCommands   []SlashCommand
	WebhookURL      string
	DefaultTarget   string
	IncomingEnabled bool          // mint an enabled incoming webhook (default true)
	TokenTTL        time.Duration // if > 0, the initial token expires after this duration
}

// UpdateParams carries the mutable fields for an update. Nil pointers mean
// "leave unchanged"; a non-nil pointer (even to an empty value) replaces the
// field.
type UpdateParams struct {
	Name            *string
	Icon            *string
	Description     *string
	Scopes          *[]string
	Products        *[]string
	Events          *[]string
	SlashCommands   *[]SlashCommand
	WebhookURL      *string
	DefaultTarget   *string
	IncomingEnabled *bool
	TokenTTL        *time.Duration // if non-nil, updates the app-level TTL policy (0 = disable)
}

// Created bundles a freshly-installed app with its one-time plaintext secrets.
type Created struct {
	App           *App
	Token         string // plaintext app token — shown once
	SigningSecret string // plaintext signing secret — shown once
}

// Registry is THE SEAM for app storage and lookup.
//
// The STANDALONE DEFAULT (StandaloneRegistry, store_standalone.go) lives in this
// package and is backed by SQLite with an in-memory fallback. A Vulos Cloud
// developer console / control plane implements this SAME interface in a separate
// package this core never imports; the product's main.go decides which to wire
// (see seam.go).
type Registry interface {
	// Create persists a new app, returning it alongside its one-time plaintext
	// token and signing secret.
	Create(p CreateParams) (*Created, error)

	// Get returns the app by id, or ErrNotFound.
	Get(id string) (*App, error)

	// GetByTokenHash returns the app whose token hash matches, or ErrNotFound.
	// Used by the token-auth middleware — callers pass HashToken(plaintext).
	// Implementations MUST compare in constant time.
	GetByTokenHash(tokenHash string) (*App, error)

	// GetByIncomingWebhookID returns the app owning an incoming-webhook id.
	GetByIncomingWebhookID(webhookID string) (*App, error)

	// List returns apps visible to owner. When isAdmin is true ALL apps are
	// returned regardless of owner (admins manage everything).
	List(owner string, isAdmin bool) ([]*App, error)

	// Update mutates the named fields of an app and returns the updated app.
	Update(id string, p UpdateParams) (*App, error)

	// Delete removes an app. Deleting an unknown id returns ErrNotFound.
	Delete(id string) error

	// RotateToken mints a new app token, stores its hash, and returns the new
	// plaintext token (shown once).
	RotateToken(id string) (string, error)

	// RotateSecret mints a new signing secret and returns its plaintext (shown
	// once). It is stored as-is so outbound events can be signed.
	RotateSecret(id string) (string, error)

	// ResolveSlashCommand finds the app + command that owns a slash command name
	// (without the leading slash), scoped to a product. ok is false when no app
	// targeting product registered it.
	ResolveSlashCommand(product, name string) (*App, *SlashCommand, bool)

	// AllSlashCommands returns every registered slash command for apps targeting
	// product (for composer autocomplete), annotated with its owning app id.
	AllSlashCommands(product string) []RegisteredCommand
}

// RegisteredCommand is a slash command annotated with its owning app id, for the
// composer autocomplete surface.
type RegisteredCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	AppID       string `json:"app_id"`
}
