// Package appsplatform is the Apps & Bots face of the Vulos integration seam: a
// product-agnostic library every first-party Vulos product (Office, Board,
// Files, and the Vulos OS) adopts as its "apps & bots place", mounted cell-local
// alongside the product. The Vulos OS is the shell and the place these
// per-product surfaces are consolidated into one view. The companion mcp package
// puts a Model Context Protocol (agent) face over the SAME ProductAdapter and
// vat_ token.
//
// An App is the unit (a bot IS an app): apps target one or more products, and a
// product mounts the platform's HTTP handler set with a small ProductAdapter that
// supplies how to post/act into ITS own surface (office tool / board op / files
// op / OS file op).
//
// Open-core seam discipline
// -------------------------
// The Registry is an INTERFACE with a STANDALONE DEFAULT in this package
// (store_standalone.go), backed by pure-Go SQLite with an in-memory fallback. A
// Vulos Cloud control plane / developer console implements the SAME Registry in
// a SEPARATE package that this core NEVER imports — only a product's composition
// root wires it, and only when explicitly selected (see seam.go). Removing the
// cloud package therefore never breaks the core build.
package appsplatform

import (
	"strings"
	"time"
)

// Product identifiers an app can target. The same app may target several. These
// are the first-party products that adopt the seam.
const (
	ProductOffice = "office"
	ProductBoard  = "board"
	ProductFiles  = "files"
	ProductOS     = "os"
)

// ValidProducts is the closed set of product identifiers the platform knows.
var ValidProducts = map[string]bool{
	ProductOffice: true,
	ProductBoard:  true,
	ProductFiles:  true,
	ProductOS:     true,
}

// Generic, product-agnostic scopes. Products MAY define their own additional
// scope strings (see ScopeSet) — these two are always understood. A product that
// needs finer granularity registers its own scopes with NewScopeSet (e.g.
// docs:write for Office, files:read for Files).
const (
	ScopeAppsRead  = "apps:read"  // read product content the app can see
	ScopeAppsWrite = "apps:write" // act / post into the product surface
)

// SlashCommand is a slash command an app registers. Name is stored WITHOUT the
// leading slash (e.g. "deploy", not "/deploy").
type SlashCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// IncomingWebhook is the unauthenticated inbound webhook for an app. The ID in
// the URL is itself the secret. Disabled apps reject incoming posts.
type IncomingWebhook struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	URL     string `json:"url,omitempty"` // populated in summaries (relative path)
}

// App is a registered app/bot manifest.
//
// Secrets (never serialized via Summary):
//   - TokenHash is the sha256 hex of the app token (Bearer secret). The
//     plaintext is shown ONCE at create/rotate time and never stored.
//   - SigningSecret is stored envelope-encrypted at rest when a
//     SigningSecretEncryptor is configured (see WithSigningSecretEncryptor); in
//     memory it is always the plaintext the signing layer consumes.
type App struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Icon          string          `json:"icon"`        // emoji or icon URL/name
	Description   string          `json:"description"` // human description
	OwnerID       string          `json:"owner_id"`    // creator account id
	OrgID         string          `json:"org_id"`      // tenant; empty in OSS / standalone
	Scopes        []string        `json:"scopes"`
	Products      []string        `json:"products"`       // which products it targets
	Events        []string        `json:"events"`         // subscribed event types ([] = all)
	SlashCommands []SlashCommand  `json:"slash_commands"` //
	WebhookURL    string          `json:"webhook_url"`    // outbound signed events (optional)
	Incoming      IncomingWebhook `json:"incoming_webhook"`
	DefaultTarget string          `json:"default_target"` // generic fallback target (channel/folder/room/doc)
	CreatedAt     time.Time       `json:"created_at"`

	// Token timing — exposed in Summary for operator visibility.
	TokenIssuedAt  time.Time `json:"token_issued_at"`  // when the current token was issued
	TokenExpiresAt time.Time `json:"token_expires_at"` // zero = never expires

	// Secrets — never serialized in API responses (see Summary).
	TokenHash     string        `json:"-"`
	SigningSecret string        `json:"-"`
	TokenTTL      time.Duration `json:"-"` // app-level TTL policy; 0 = no TTL
}

// AccountID is the synthetic author/membership id an app posts and is addressed
// under: "app:<id>".
func (a *App) AccountID() string { return AppAccountID(a.ID) }

// AppAccountID returns the synthetic account id for an app id.
func AppAccountID(id string) string { return "app:" + id }

// HasScope reports whether the app was granted scope.
func (a *App) HasScope(scope string) bool {
	for _, s := range a.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// TargetsProduct reports whether the app targets product. An app with NO
// products targets nothing (and is hidden from every product's GET /api/apps).
func (a *App) TargetsProduct(product string) bool {
	for _, p := range a.Products {
		if p == product {
			return true
		}
	}
	return false
}

// SubscribesTo reports whether the app should receive an event of eventType. An
// empty Events list means "all event types" (the default).
func (a *App) SubscribesTo(eventType string) bool {
	if len(a.Events) == 0 {
		return true
	}
	for _, e := range a.Events {
		if e == eventType {
			return true
		}
	}
	return false
}

// Mentions reports whether body addresses this app, either by its @name token or
// by its canonical <@app:<id>> mention form (case-insensitive on the name).
func (a *App) Mentions(body string) bool {
	if a.Name != "" {
		needle := "@" + strings.ToLower(a.Name)
		if strings.Contains(strings.ToLower(body), needle) {
			return true
		}
	}
	return strings.Contains(body, "<@"+a.AccountID()+">")
}

// Summary is the secret-free public view of an app. It is exactly the shape
// returned by GET /api/apps — the consolidation contract the OS reads.
type Summary struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Icon          string          `json:"icon"`
	Description   string          `json:"description"`
	Scopes        []string        `json:"scopes"`
	Products      []string        `json:"products"`
	Events        []string        `json:"events"`
	SlashCommands []SlashCommand  `json:"slash_commands"`
	WebhookURL    string          `json:"webhook_url"`
	Incoming      IncomingWebhook `json:"incoming_webhook"`
	OwnerID       string          `json:"owner_id"`
	DefaultTarget string          `json:"default_target,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	// Token timing (informational; helps operators detect approaching expiry).
	TokenIssuedAt  time.Time `json:"token_issued_at,omitempty"`
	TokenExpiresAt time.Time `json:"token_expires_at,omitempty"`
}

// IncomingWebhookPath is the relative URL (path) clients POST to for an incoming
// webhook id, under the given mount base path (e.g. "/api/apps").
func IncomingWebhookPath(basePath, webhookID string) string {
	return strings.TrimRight(basePath, "/") + "/hooks/" + webhookID
}

// ToSummary builds the secret-free Summary for an app. basePath is the mount
// path used to render the incoming-webhook URL.
func (a *App) ToSummary(basePath string) Summary {
	inc := a.Incoming
	if inc.ID != "" {
		inc.URL = IncomingWebhookPath(basePath, inc.ID)
	}
	return Summary{
		ID:             a.ID,
		Name:           a.Name,
		Icon:           a.Icon,
		Description:    a.Description,
		Scopes:         nonNilStr(a.Scopes),
		Products:       nonNilStr(a.Products),
		Events:         nonNilStr(a.Events),
		SlashCommands:  nonNilCmds(a.SlashCommands),
		WebhookURL:     a.WebhookURL,
		Incoming:       inc,
		OwnerID:        a.OwnerID,
		DefaultTarget:  a.DefaultTarget,
		CreatedAt:      a.CreatedAt,
		TokenIssuedAt:  a.TokenIssuedAt,
		TokenExpiresAt: a.TokenExpiresAt,
	}
}

func nonNilStr(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func nonNilCmds(in []SlashCommand) []SlashCommand {
	if in == nil {
		return []SlashCommand{}
	}
	return in
}

// NormalizeProducts trims, lowercases, de-dupes and validates a product list,
// returning the cleaned list or an error naming the first unknown product.
func NormalizeProducts(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool)
	for _, p := range in {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		if !ValidProducts[p] {
			return nil, &ProductError{Product: p}
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, nil
}

// NormalizeEvents trims, lowercases and de-dupes an event-subscription list.
// Event type strings are product-defined so they are not validated here.
func NormalizeEvents(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool)
	for _, e := range in {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

// NormalizeSlashCommands trims command names (stripping a leading slash) and
// drops entries with empty/duplicate names.
func NormalizeSlashCommands(in []SlashCommand) []SlashCommand {
	out := make([]SlashCommand, 0, len(in))
	seen := make(map[string]bool)
	for _, c := range in {
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(c.Name), "/")))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, SlashCommand{Name: name, Description: strings.TrimSpace(c.Description)})
	}
	return out
}

// ProductError reports an unknown product identifier.
type ProductError struct{ Product string }

func (e *ProductError) Error() string { return "appsplatform: unknown product: " + e.Product }

// ScopeError reports an unknown scope string.
type ScopeError struct{ Scope string }

func (e *ScopeError) Error() string { return "appsplatform: unknown scope: " + e.Scope }

// ScopeSet is the closed set of scope strings a registry accepts. Unknown
// scopes are rejected at create/update time so a typo never silently grants (or
// appears to grant) access. Products extend it with their own scopes.
type ScopeSet map[string]bool

// NewScopeSet builds a ScopeSet from the given scope strings.
func NewScopeSet(scopes ...string) ScopeSet {
	s := make(ScopeSet, len(scopes))
	for _, sc := range scopes {
		s[sc] = true
	}
	return s
}

// DefaultScopeSet is the platform's built-in scope set: the generic apps:read /
// apps:write. Products extend it with their own product-specific scopes via
// NewScopeSet when they need finer granularity.
func DefaultScopeSet() ScopeSet {
	return NewScopeSet(ScopeAppsRead, ScopeAppsWrite)
}

// Normalize trims, lowercases, de-dupes and validates a scope list against the
// set, returning the cleaned list or an error naming the first unknown scope.
func (s ScopeSet) Normalize(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool)
	for _, sc := range in {
		sc = strings.ToLower(strings.TrimSpace(sc))
		if sc == "" || seen[sc] {
			continue
		}
		if !s[sc] {
			return nil, &ScopeError{Scope: sc}
		}
		seen[sc] = true
		out = append(out, sc)
	}
	return out, nil
}
