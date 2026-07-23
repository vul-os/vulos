// Package webhost implements the box-side static web-hosting service — the
// "publish a built site from your own box" surface behind
//
//	https://<site>.<user>.os.vulos.org
//
// A box owner deploys a pre-built static bundle (a tar.gz of a Vite/Next
// export, an mkdocs build, a plain HTML tree, …). The box extracts it into a
// per-user, per-site directory and serves it with an SPA fallback (serve.go).
// Custom apex/subdomains attach on top (domain.go) and terminate TLS via a
// swappable cert backend (cert.go).
//
// SECURITY MODEL. This package has exactly two attacker-facing surfaces and
// both are treated as hostile by default:
//
//  1. Deploy() extracts an ATTACKER-SUPPLIED tar.gz. A malicious archive is
//     the threat model, not an edge case: entries are rejected if they carry
//     an absolute path, a ".." component, or a symlink/hardlink/device type,
//     and extraction is hard-capped on both total decompressed bytes and file
//     count so a decompression bomb cannot exhaust the disk. Every write goes
//     through hardenedJoin, which re-verifies the resolved path stays inside
//     the site root even after Clean. See extractTarGz.
//
//  2. serve.go serves attacker-influenced URL paths out of that same tree,
//     with its own traversal guard, dotfile refusal, and no directory
//     listing. See ServeSite.
//
// SCOPING. Everything is keyed by userID (the auth-middleware-stamped
// X-User-ID — never client input at the HTTP layer) then by site name, so on
// a multi-user self-host box one owner can never read, overwrite, or delete
// another owner's sites. Both identifiers are pattern-validated before they
// ever touch the filesystem.
//
// ON-DISK LAYOUT (rooted at the Service's base dir):
//
//	<root>/<userID>/<site>/htdocs/     — the served static tree (site root)
//	<root>/<userID>/<site>/meta.json   — domains + deploy metadata (NEVER served)
//
// meta.json lives one level ABOVE htdocs on purpose: the serving handler is
// rooted at htdocs, so site metadata and pending ACME tokens are structurally
// unreachable over HTTP.
package webhost

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Default extraction caps. These are the disk-exhaustion guardrails for the
// hostile-archive threat model; a real fleet can tighten them per tier via
// options.
const (
	defaultMaxBundleBytes int64 = 512 << 20  // 512 MiB decompressed, per bundle
	defaultMaxFiles       int   = 50000       // regular files + dirs, per bundle
	defaultMaxPathLen     int   = 4096         // per-entry name length ceiling
	defaultMaxUserBytes   int64 = 2 << 30      // 2 GiB total on disk, per user
	defaultMaxUserSites   int   = 25           // distinct sites, per user
)

// siteNamePattern matches a safe site slug: lowercase, digit-or-letter start,
// then letters/digits/hyphens. Deliberately narrow — a site name becomes a DNS
// label (<site>.<user>.os.vulos.org) and a filesystem path segment, so it must
// be safe for both.
var siteNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// userIDPattern matches a Vulos user id. User ids come from
// base64.RawURLEncoding (services/auth.generateID), whose alphabet is exactly
// [A-Za-z0-9_-]; bounded length guards against pathological header values.
var userIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Domain is a custom domain attached to a site.
type Domain struct {
	Name   string `json:"name"`
	Status string `json:"status"` // DomainStatus* (see domain.go)
}

// Site describes one published site owned by a user.
type Site struct {
	Name      string   `json:"name"`
	UserID    string   `json:"-"`
	UpdatedTS int64    `json:"updated_ts"` // Unix seconds of last Deploy
	Bytes     int64    `json:"bytes"`      // total decompressed size on disk
	Domains   []Domain `json:"domains"`
}

// siteMeta is the persisted per-site record (meta.json). Kept out of the
// served tree.
type siteMeta struct {
	UpdatedTS int64    `json:"updated_ts"`
	Bytes     int64    `json:"bytes"`
	Domains   []Domain `json:"domains"`
	// AcmeTokens maps a custom domain -> its acme-dns delegation label (the
	// random subdomain the owner CNAMEs _acme-challenge.<domain> at). It is not
	// a secret — it is meant to be published in DNS — but it lives out of the
	// served tree and off the public Domain type all the same.
	AcmeTokens map[string]string `json:"acme_tokens,omitempty"`
}

// Service is the web-hosting store. It is safe for concurrent use.
type Service struct {
	root           string
	maxBundleBytes int64
	maxFiles       int
	maxUserBytes   int64 // aggregate on-disk cap across ALL of a user's sites
	maxUserSites   int   // distinct-site cap per user
	instanceIPs    []string     // A records handed out for attached domains
	certs          CertProvider // TLS issuance seam (cert.go)

	// mu serializes only the fast, local commit step of a deploy (the htdocs
	// swap + meta write) and the quota check — NEVER the network-bound tar
	// extraction, which runs lock-free so one slow upload can't block every
	// other tenant's deploy.
	mu sync.Mutex
}

// Option configures a Service.
type Option func(*Service)

// WithMaxBundleBytes overrides the total decompressed-size cap for Deploy.
func WithMaxBundleBytes(n int64) Option {
	return func(s *Service) {
		if n > 0 {
			s.maxBundleBytes = n
		}
	}
}

// WithMaxFiles overrides the file-count cap for Deploy.
func WithMaxFiles(n int) Option {
	return func(s *Service) {
		if n > 0 {
			s.maxFiles = n
		}
	}
}

// WithInstanceIPs sets the A records reported to owners attaching a custom
// domain. Empty (the default) is honest: the box does not claim an address it
// cannot prove it answers on.
func WithInstanceIPs(ips ...string) Option {
	return func(s *Service) { s.instanceIPs = append([]string(nil), ips...) }
}

// WithCertProvider installs the TLS-issuance backend for custom domains. The
// default is the honest no-op provider (cert.go) which issues nothing.
func WithCertProvider(cp CertProvider) Option {
	return func(s *Service) {
		if cp != nil {
			s.certs = cp
		}
	}
}

// WithMaxUserBytes overrides the aggregate on-disk cap across all of one user's
// sites (disk-exhaustion guard for the multi-tenant box).
func WithMaxUserBytes(n int64) Option {
	return func(s *Service) {
		if n > 0 {
			s.maxUserBytes = n
		}
	}
}

// WithMaxUserSites overrides the distinct-site cap per user.
func WithMaxUserSites(n int) Option {
	return func(s *Service) {
		if n > 0 {
			s.maxUserSites = n
		}
	}
}

// New creates a Service rooted at root. If root is empty it falls back to
// $VULOS_WEB_ROOT, then to <box data dir>/web. The root is taken as a
// parameter so tests can point it at a temp dir.
func New(root string, opts ...Option) *Service {
	if root == "" {
		root = defaultRoot()
	}
	s := &Service{
		root:           root,
		maxBundleBytes: defaultMaxBundleBytes,
		maxFiles:       defaultMaxFiles,
		maxUserBytes:   defaultMaxUserBytes,
		maxUserSites:   defaultMaxUserSites,
		certs:          NoopCertProvider{},
	}
	for _, o := range opts {
		o(s)
	}
	os.MkdirAll(s.root, 0o755)
	return s
}

// defaultRoot picks a base dir when none is supplied.
func defaultRoot() string {
	if v := os.Getenv("VULOS_WEB_ROOT"); v != "" {
		return v
	}
	if v := os.Getenv("VULOS_DATA_DIR"); v != "" {
		return filepath.Join(v, "web")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".vulos", "web")
	}
	return filepath.Join(os.TempDir(), "vulos-web")
}

var (
	// ErrInvalidUser is returned when the user id fails validation.
	ErrInvalidUser = errors.New("webhost: invalid user id")
	// ErrInvalidSite is returned when the site name fails validation.
	ErrInvalidSite = errors.New("webhost: invalid site name")
	// ErrNotFound is returned when a site does not exist.
	ErrNotFound = errors.New("webhost: site not found")
	// ErrBundleTooLarge is returned when extraction exceeds the size/file caps.
	ErrBundleTooLarge = errors.New("webhost: bundle exceeds size or file-count limit")
	// ErrUnsafeEntry is returned when an archive entry tries to escape the
	// site root or is a disallowed (symlink/hardlink/device) type.
	ErrUnsafeEntry = errors.New("webhost: unsafe archive entry")
	// ErrQuotaExceeded is returned when a deploy would push the user past their
	// aggregate byte or site-count budget.
	ErrQuotaExceeded = errors.New("webhost: per-user storage or site-count quota exceeded")
)

// ValidSiteName reports whether name is a usable site slug.
func ValidSiteName(name string) bool { return siteNamePattern.MatchString(name) }

// userDir returns the (cleaned) directory holding all of userID's sites.
func (s *Service) userDir(userID string) (string, error) {
	if !userIDPattern.MatchString(userID) {
		return "", ErrInvalidUser
	}
	return filepath.Join(s.root, userID), nil
}

// siteBase returns <root>/<userID>/<site> — the container for htdocs + meta.
func (s *Service) siteBase(userID, site string) (string, error) {
	ud, err := s.userDir(userID)
	if err != nil {
		return "", err
	}
	if !siteNamePattern.MatchString(site) {
		return "", ErrInvalidSite
	}
	return filepath.Join(ud, site), nil
}

// siteRoot returns the served static root (htdocs) for a site, creating it if
// requested. This is the directory serve.go is rooted at.
func (s *Service) siteRoot(userID, site string, create bool) (string, error) {
	base, err := s.siteBase(userID, site)
	if err != nil {
		return "", err
	}
	root := filepath.Join(base, "htdocs")
	if create {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return "", err
		}
	}
	return root, nil
}

func (s *Service) metaPath(userID, site string) (string, error) {
	base, err := s.siteBase(userID, site)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "meta.json"), nil
}

// Deploy extracts bundle (a tar.gz of a built static site) into the user's
// site directory, replacing any previous content atomically. bundle is treated
// as hostile: see extractTarGz for the traversal / type / size guards.
//
// Existing custom-domain attachments (meta.json) are preserved across a
// redeploy — only the served files are replaced.
func (s *Service) Deploy(userID, site string, bundle io.Reader) error {
	base, err := s.siteBase(userID, site)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}

	// Extract into a sibling temp dir, then swap. A partial/failed extraction
	// never becomes the live site.
	//
	// CRITICAL: extraction is done WITHOUT holding s.mu. It reads the bundle
	// straight off the (attacker-paced) request socket, so holding the
	// process-wide lock across it would let one slow/slowloris upload block
	// EVERY other tenant's deploy for the whole transfer. Only the fast commit
	// below is serialized.
	tmp, err := os.MkdirTemp(base, ".deploy-*")
	if err != nil {
		return err
	}
	// Ensure the temp dir is gone on any early return.
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(tmp)
		}
	}()

	bytes, err := extractTarGz(bundle, tmp, s.maxBundleBytes, s.maxFiles)
	if err != nil {
		return err
	}

	// Everything from here — quota check + the htdocs swap + meta write — is the
	// fast, local, serialized commit step.
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.checkUserQuota(userID, site, bytes); err != nil {
		return err
	}

	// Swap htdocs <- tmp.
	htdocs := filepath.Join(base, "htdocs")
	old := filepath.Join(base, ".htdocs-old")
	os.RemoveAll(old)
	if _, statErr := os.Stat(htdocs); statErr == nil {
		if err := os.Rename(htdocs, old); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, htdocs); err != nil {
		// Best-effort restore of the previous tree.
		os.Rename(old, htdocs)
		return err
	}
	committed = true
	os.RemoveAll(old)

	// Update metadata, preserving existing domains.
	meta, _ := s.loadMeta(userID, site) // ignore load error -> fresh meta
	meta.UpdatedTS = time.Now().Unix()
	meta.Bytes = bytes
	return s.saveMeta(userID, site, meta)
}

// checkUserQuota rejects a deploy that would push userID past their aggregate
// byte budget or distinct-site count. newBytes is the size of the incoming
// bundle for `site`; the site being (re)deployed is excluded from the existing
// total since its old content is about to be replaced. Called under s.mu.
func (s *Service) checkUserQuota(userID, site string, newBytes int64) error {
	sites, err := s.ListSites(userID)
	if err != nil {
		return err
	}
	var otherBytes int64
	exists := false
	for _, st := range sites {
		if st.Name == site {
			exists = true
			continue // replaced, not additive
		}
		otherBytes += st.Bytes
	}
	// A brand-new site would push the distinct-site count up by one.
	if !exists && len(sites) >= s.maxUserSites {
		return ErrQuotaExceeded
	}
	if otherBytes+newBytes > s.maxUserBytes {
		return ErrQuotaExceeded
	}
	return nil
}

// ListSites returns every site owned by userID, newest-updated first.
func (s *Service) ListSites(userID string) ([]Site, error) {
	ud, err := s.userDir(userID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(ud)
	if err != nil {
		if os.IsNotExist(err) {
			return []Site{}, nil
		}
		return nil, err
	}
	out := make([]Site, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || !siteNamePattern.MatchString(e.Name()) {
			continue
		}
		meta, err := s.loadMeta(userID, e.Name())
		if err != nil {
			// A directory with no readable meta is a half-formed site; skip it
			// rather than surfacing a phantom.
			continue
		}
		out = append(out, Site{
			Name:      e.Name(),
			UserID:    userID,
			UpdatedTS: meta.UpdatedTS,
			Bytes:     meta.Bytes,
			Domains:   meta.Domains,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedTS > out[j].UpdatedTS })
	return out, nil
}

// GetSite returns a single site, or ErrNotFound.
func (s *Service) GetSite(userID, site string) (Site, error) {
	base, err := s.siteBase(userID, site)
	if err != nil {
		return Site{}, err
	}
	if _, err := os.Stat(base); err != nil {
		return Site{}, ErrNotFound
	}
	meta, err := s.loadMeta(userID, site)
	if err != nil {
		return Site{}, ErrNotFound
	}
	return Site{
		Name:      site,
		UserID:    userID,
		UpdatedTS: meta.UpdatedTS,
		Bytes:     meta.Bytes,
		Domains:   meta.Domains,
	}, nil
}

// DeleteSite removes a site and all of its content and metadata.
func (s *Service) DeleteSite(userID, site string) error {
	base, err := s.siteBase(userID, site)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(base); err != nil {
		return ErrNotFound
	}
	return os.RemoveAll(base)
}

// SiteURL returns the canonical box URL for a site.
func (s *Service) SiteURL(userID, site string) string {
	return fmt.Sprintf("https://%s.%s.os.vulos.org", site, userID)
}

// loadMeta reads meta.json for a site. A missing file yields a zero-value
// siteMeta and no error (a freshly created site directory).
func (s *Service) loadMeta(userID, site string) (siteMeta, error) {
	mp, err := s.metaPath(userID, site)
	if err != nil {
		return siteMeta{}, err
	}
	b, err := os.ReadFile(mp)
	if err != nil {
		if os.IsNotExist(err) {
			return siteMeta{Domains: []Domain{}}, nil
		}
		return siteMeta{}, err
	}
	var m siteMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return siteMeta{}, err
	}
	if m.Domains == nil {
		m.Domains = []Domain{}
	}
	return m, nil
}

// saveMeta atomically persists meta.json for a site.
func (s *Service) saveMeta(userID, site string, m siteMeta) error {
	mp, err := s.metaPath(userID, site)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(mp), 0o755); err != nil {
		return err
	}
	if m.Domains == nil {
		m.Domains = []Domain{}
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := mp + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, mp)
}

// extractTarGz decompresses and untars src into dest, returning the total
// number of bytes written. It enforces the full hostile-archive contract:
//
//   - total decompressed bytes <= maxBytes  (decompression-bomb guard)
//   - entry count <= maxFiles               (inode-exhaustion guard)
//   - entry name length <= defaultMaxPathLen
//   - only regular files and directories are materialized; symlinks,
//     hardlinks, char/block devices, and fifos are REJECTED outright
//     (rejected, not skipped — a bundle that carries one is malformed/hostile
//     and we refuse the whole deploy)
//   - every entry path is validated by hardenedJoin, which rejects absolute
//     paths and ".." components and re-checks that the cleaned result stays
//     within dest
func extractTarGz(src io.Reader, dest string, maxBytes int64, maxFiles int) (int64, error) {
	gz, err := gzip.NewReader(src)
	if err != nil {
		return 0, fmt.Errorf("webhost: not a gzip stream: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var total int64
	var count int

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return total, fmt.Errorf("webhost: tar read: %w", err)
		}

		count++
		if count > maxFiles {
			return total, ErrBundleTooLarge
		}
		if len(hdr.Name) > defaultMaxPathLen {
			return total, ErrUnsafeEntry
		}

		// Reject disallowed entry types up front. Only dirs and regular files
		// ever reach the filesystem.
		switch hdr.Typeflag {
		case tar.TypeDir, tar.TypeReg, tar.TypeRegA:
			// allowed
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			// pax metadata, no payload to write
			continue
		default:
			// symlink, hardlink, char/block device, fifo, …
			return total, ErrUnsafeEntry
		}

		target, err := hardenedJoin(dest, hdr.Name)
		if err != nil {
			return total, err
		}

		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return total, err
			}
			continue
		}

		// Regular file: ensure parent exists, then copy under a byte budget.
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return total, err
		}
		remaining := maxBytes - total
		if remaining <= 0 {
			return total, ErrBundleTooLarge
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return total, err
		}
		// LimitReader to remaining+1: if we can read one byte past the budget,
		// the archive is over-cap and we bail. This bounds writes regardless of
		// the (untrusted) hdr.Size.
		n, err := io.Copy(f, io.LimitReader(tr, remaining+1))
		closeErr := f.Close()
		if err != nil {
			return total, err
		}
		if closeErr != nil {
			return total, closeErr
		}
		total += n
		if total > maxBytes {
			return total, ErrBundleTooLarge
		}
	}
	return total, nil
}

// hardenedJoin joins name under root and guarantees the result cannot escape
// root. It rejects absolute paths and any ".." component before joining, then
// re-verifies the cleaned path is still prefixed by root. This is the single
// choke point every archive entry passes through.
func hardenedJoin(root, name string) (string, error) {
	if name == "" {
		return "", ErrUnsafeEntry
	}
	// Normalize separators (tar uses forward slashes) and reject absolutes.
	name = filepath.FromSlash(name)
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return "", ErrUnsafeEntry
	}
	// Reject any traversal component explicitly, before Clean can collapse it.
	for _, seg := range strings.Split(filepath.ToSlash(name), "/") {
		if seg == ".." {
			return "", ErrUnsafeEntry
		}
	}
	joined := filepath.Join(root, name)
	cleaned := filepath.Clean(joined)
	if cleaned != root && !strings.HasPrefix(cleaned, root+string(filepath.Separator)) {
		return "", ErrUnsafeEntry
	}
	return cleaned, nil
}
