package files

// import.go — FILES IMPORT ENGINE.
//
// IMPORT vs MOUNT — the load-bearing distinction:
//
//   - MOUNT (external.go): a LIVE READ-THROUGH VIEW of a connected provider. The
//     bytes stay at the provider; every list/download hits the provider's API
//     with a freshly-minted token. Disconnect the integration (or delete the
//     upstream file) and the view is gone. Nothing is owned by Vulos.
//
//   - IMPORT (this file): a one-way COPY INTO OWNED STORAGE. Files/folders are
//     pulled from the provider and WRITTEN as Vulos-owned copies into the user's
//     canonical Drive area ("<owner>/drive/..."). The copy PERSISTS after the
//     integration is disconnected or the upstream file is deleted — it is the
//     user's, in their own bucket, indexed in files_nodes like any other file.
//
// Google-native documents (Docs/Sheets/Slides/Drawings) have no direct media
// bytes, so on import they are EXPORTED to a portable Office/image format (docx/
// xlsx/pptx/png) — see gdrive.go's OpenForImport + gdriveImportExportMap. Regular
// files (and OneDrive's already-binary Office files) are copied as-is.
//
// mode=sync supports an idempotent, ADDITIVE-ONLY re-pull: re-running a job
// copies anything new upstream but NEVER deletes the Vulos copy when the upstream
// file disappears (one-way provider→Vulos). Re-pull is resumable + deduped via
// files_import_items, which remembers provider-file-id → vulos-node-id per
// (owner, provider).
//
// Security: the box mints a SHORT-LIVED per-call token from the CP integration
// broker (reusing the external-mount TokenSource) and never persists it. The
// import sources are SSRF host-locked per provider (they talk only to the fixed
// Google/Microsoft API hosts). Jobs are ownership-scoped — a job is only visible
// to and runnable/deletable by its owner. Degrade: with no broker / no sources
// registered, ImportEnabled() is false and the Import action is "not available".

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"vulos/backend/internal/ulid"
)

// Sentinel errors for the import surface. ErrImportUnavailable means the seam is
// not wired (no broker / no sources) — callers map it to a "not available" UI
// state, never a hard failure of the rest of Files.
var (
	ErrImportUnavailable = errors.New("files: import unavailable")
	ErrImportProvider    = errors.New("files: unknown import provider")
)

// Import job kinds / modes / statuses.
const (
	ImportKindFiles    = "files"    // copy into owned Drive storage
	ImportKindContacts = "contacts" // bulk-import into lilmail CardDAV store
	ImportKindCalendar = "calendar" // bulk-import into lilmail CalDAV store

	ImportModeOnce = "once" // copy once and stop
	ImportModeSync = "sync" // additive-only re-pull supported

	ImportStatusPending = "pending"
	ImportStatusRunning = "running"
	ImportStatusDone    = "done"
	ImportStatusError   = "error"
)

// importMaxDepth bounds folder recursion against a pathological provider tree.
const importMaxDepth = 64

// ImportJob is a single import the user has started: pull provider files/folders
// into their Drive. It is ownership-scoped (only OwnerID sees/controls it).
type ImportJob struct {
	ID         string     `json:"id"`
	OwnerID    string     `json:"owner_id"`
	Provider   string     `json:"provider"` // import source kind, e.g. "gdrive"
	Kind       string     `json:"kind"`     // "files"
	Source     string     `json:"source"`   // provider folder id, or "" = everything
	Mode       string     `json:"mode"`     // once | sync
	Status     string     `json:"status"`   // pending|running|done|error
	Imported   int        `json:"imported"` // files copied in the last run
	Skipped    int        `json:"skipped"`  // already-present files (dedup) in the last run
	Errors     int        `json:"errors"`   // per-item failures in the last run
	Error      string     `json:"error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSyncAt *time.Time `json:"last_sync_at,omitempty"`
}

// ImportSource is the READ side an import job pulls from. The real providers
// (Google Drive, OneDrive) implement it; tests supply a fake. It deliberately
// overlaps ExternalProvider's List so a mount provider can also be an import
// source, plus OpenForImport, which exports native docs to a portable format.
//
// Implementations MUST be SSRF-safe: they may contact ONLY the provider's fixed
// API host(s), never an attacker-influenced URL.
type ImportSource interface {
	// Kind is the import source id (e.g. "gdrive"). Used as the job's provider
	// and the registry key.
	Kind() string
	// IntegrationProvider is the CP integration token provider to mint for this
	// source (e.g. "google" for Drive, "microsoft" for OneDrive).
	IntegrationProvider() string
	// List returns the children of folderID (empty ⇒ provider root) mapped into
	// Node shape. The Node's ContentType carries the provider mime type so the
	// engine/source can decide on export.
	List(ctx context.Context, call ProviderCall, folderID string) ([]*Node, error)
	// OpenForImport opens a file's bytes for copying into the Drive. For a native
	// document it EXPORTS to a portable format and returns the rewritten name
	// (with the right extension) + content type; a regular file streams as-is.
	// size may be -1 when the provider does not report it up front. The caller
	// MUST Close the returned reader.
	OpenForImport(ctx context.Context, call ProviderCall, file *Node) (rc io.ReadCloser, name, contentType string, size int64, err error)
}

// WithImport wires the import seam onto the Service. It reuses the external-mount
// TokenSource (set via WithExternal) to mint short-lived provider tokens, so call
// WithExternal first (or independently set a token source). sources are the
// available import source kinds. Returns s for chaining.
func (s *Service) WithImport(sources ...ImportSource) *Service {
	if s.importSources == nil {
		s.importSources = map[string]ImportSource{}
	}
	for _, src := range sources {
		if src != nil {
			s.importSources[src.Kind()] = src
		}
	}
	return s
}

// ImportEnabled reports whether the import seam is wired (a token source AND at
// least one source). The Files app uses this to enable/disable the Import action.
func (s *Service) ImportEnabled() bool {
	return s.extTokens != nil && len(s.importSources) > 0
}

// AvailableImportSources returns the registered import sources for the UI's
// import menu. Empty when the seam is not wired.
func (s *Service) AvailableImportSources() []ProviderInfo {
	if !s.ImportEnabled() {
		return nil
	}
	out := make([]ProviderInfo, 0, len(s.importSources))
	for kind, src := range s.importSources {
		out = append(out, ProviderInfo{Kind: kind, DisplayName: importDisplayName(src)})
	}
	return out
}

// importDisplayName returns a human label for a source (its DisplayName when it
// is also an ExternalProvider, else a capitalised kind).
func importDisplayName(src ImportSource) string {
	if p, ok := src.(interface{ DisplayName() string }); ok {
		return p.DisplayName()
	}
	return src.Kind()
}

// importSource resolves a registered source by its Kind, falling back to its
// IntegrationProvider so callers may pass either "gdrive" or "google".
func (s *Service) importSource(provider string) (ImportSource, error) {
	if !s.ImportEnabled() {
		return nil, ErrImportUnavailable
	}
	if src, ok := s.importSources[provider]; ok {
		return src, nil
	}
	for _, src := range s.importSources {
		if src.IntegrationProvider() == provider {
			return src, nil
		}
	}
	return nil, ErrImportProvider
}

// importCall mints a FRESH short-lived token for the source's integration and
// packages it for one provider call. Tokens are never persisted. A "not
// connected" result surfaces as ErrExternalNotConnected.
// H3 fix: ownerID is forwarded so the broker returns per-user tokens.
func (s *Service) importCall(ctx context.Context, src ImportSource, ownerID string) (ProviderCall, error) {
	if s.extTokens == nil {
		return ProviderCall{}, ErrImportUnavailable
	}
	tok, err := s.extTokens.MintToken(ctx, src.IntegrationProvider(), ownerID)
	if err != nil {
		return ProviderCall{}, err
	}
	if tok == "" {
		return ProviderCall{}, ErrExternalNotConnected
	}
	return ProviderCall{Token: tok}, nil
}

// StartImport validates + records a new import job for ownerID and proves the
// account can mint a token for the source's integration (so we never create a
// dead job). It does NOT run the copy — call RunImportJob (the HTTP layer kicks
// it off in the background; tests call it synchronously).
func (s *Service) StartImport(ctx context.Context, ownerID, provider, source, mode string) (*ImportJob, error) {
	if !s.ImportEnabled() {
		return nil, ErrImportUnavailable
	}
	src, err := s.importSource(provider)
	if err != nil {
		return nil, err
	}
	// Prove connectivity: a successful mint means the account has connected the
	// provider CP-side. The token is discarded here.
	if _, err := s.importCall(ctx, src, ownerID); err != nil {
		return nil, err
	}
	job := &ImportJob{
		ID:        ulid.NewULID(),
		OwnerID:   ownerID,
		Provider:  src.Kind(),
		Kind:      importDataKind(src),
		Source:    source,
		Mode:      normalizeImportMode(mode),
		Status:    ImportStatusPending,
		CreatedAt: time.Now(),
	}
	if err := s.insertImportJob(job); err != nil {
		return nil, err
	}
	s.audit(ownerID, "import.start", job.ID, src.Kind()+":"+job.Mode)
	return job, nil
}

// importDataKind returns the data kind for an ImportSource. Sources that
// implement the optional DataKind() interface return their own kind (e.g.
// "contacts", "calendar"); all others default to ImportKindFiles. This keeps
// the existing GDriveProvider/OneDriveProvider unchanged.
func importDataKind(src ImportSource) string {
	type dker interface{ DataKind() string }
	if dk, ok := src.(dker); ok {
		return dk.DataKind()
	}
	return ImportKindFiles
}

// normalizeImportMode maps an unset/unknown mode to the safe default (once).
func normalizeImportMode(m string) string {
	if m == ImportModeSync {
		return ImportModeSync
	}
	return ImportModeOnce
}

// ListImportJobs returns ownerID's import jobs (newest first). Returns an empty
// slice when the seam is wired but there are none; nil,nil when not wired.
func (s *Service) ListImportJobs(ownerID string) ([]ImportJob, error) {
	if !s.ImportEnabled() {
		return nil, nil
	}
	return s.listImportJobs(ownerID)
}

// DeleteImportJob removes a job (and its import-item map) owned by ownerID. The
// imported COPIES in the Drive are left untouched — they are the user's files
// now; deleting the job just stops tracking the import. 404 on cross-owner.
func (s *Service) DeleteImportJob(ownerID, jobID string) error {
	job, err := s.getImportJob(jobID)
	if err != nil {
		return err
	}
	if job.OwnerID != ownerID {
		return ErrNotFound
	}
	if err := s.deleteImportJob(jobID); err != nil {
		return err
	}
	s.deleteImportItemsForJob(jobID)
	s.audit(ownerID, "import.delete", jobID, job.Provider)
	return nil
}

// RunImportJob executes (or re-pulls) jobID: walk the source from job.Source and
// copy everything new into the owner's Drive. It is IDEMPOTENT — already-imported
// items are skipped via files_import_items — so a mode=sync job can be re-run for
// an ADDITIVE-ONLY refresh (it never deletes the Vulos copy). Per-item failures
// are counted and logged but do not abort the whole job; a fatal failure (e.g.
// the source vanished) marks the job status=error.
//
// This is intended to run in the background (the HTTP POST returns immediately
// and the UI polls job status). Pass a background context, not the request's.
func (s *Service) RunImportJob(ctx context.Context, jobID string) error {
	job, err := s.getImportJob(jobID)
	if err != nil {
		return err
	}
	src, err := s.importSource(job.Provider)
	if err != nil {
		s.finishImport(job, importCounts{}, err)
		return err
	}
	s.setImportStatus(job.ID, ImportStatusRunning, "")
	var counts importCounts
	var walkErr error
	switch job.Kind {
	case ImportKindContacts, ImportKindCalendar:
		counts, walkErr = s.runPIMImport(ctx, src, job)
	default:
		walkErr = s.importWalk(ctx, src, job, job.Source, "", 0, &counts)
	}
	s.finishImport(job, counts, walkErr)
	if walkErr != nil {
		return walkErr
	}
	return nil
}

// importCounts tallies one run.
type importCounts struct{ imported, skipped, errors int }

// finishImport records the run's outcome: counts, status, and (on success)
// last_sync_at. A fatal error sets status=error with the message.
func (s *Service) finishImport(job *ImportJob, c importCounts, fatal error) {
	status := ImportStatusDone
	msg := ""
	var syncAt *time.Time
	if fatal != nil {
		status = ImportStatusError
		msg = fatal.Error()
	} else {
		now := time.Now()
		syncAt = &now
	}
	if err := s.updateImportResult(job.ID, status, msg, c, syncAt); err != nil {
		log.Printf("[files-import] persist result failed for %s: %v", job.ID, err)
	}
	s.audit(job.OwnerID, "import.run", job.ID,
		fmt.Sprintf("status=%s imported=%d skipped=%d errors=%d", status, c.imported, c.skipped, c.errors))
}

// importWalk recursively copies the children of externalFolderID (empty ⇒ source
// root) into the Vulos folder vulosParentID (empty ⇒ Drive root) for the job's
// owner. Folders are find-or-created (and remembered) so a re-pull merges into
// the same tree instead of duplicating; files are copied once and remembered.
func (s *Service) importWalk(ctx context.Context, src ImportSource, job *ImportJob, externalFolderID, vulosParentID string, depth int, counts *importCounts) error {
	if depth > importMaxDepth {
		return fmt.Errorf("files: import folder depth exceeds %d", importMaxDepth)
	}
	listCall, err := s.importCall(ctx, src, job.OwnerID)
	if err != nil {
		return err
	}
	children, err := src.List(ctx, listCall, externalFolderID)
	if err != nil {
		return fmt.Errorf("files: import list: %w", err)
	}
	for _, child := range children {
		if child == nil || child.ID == "" {
			continue
		}
		if child.IsDir {
			folder, err := s.importEnsureFolder(job, child, vulosParentID)
			if err != nil {
				counts.errors++
				log.Printf("[files-import] folder %q failed: %v", child.Name, err)
				continue
			}
			if err := s.importWalk(ctx, src, job, child.ID, folder.ID, depth+1, counts); err != nil {
				return err
			}
			continue
		}
		if err := s.importFile(ctx, src, job, child, vulosParentID, counts); err != nil {
			// A per-file failure is recorded and skipped, never fatal.
			counts.errors++
			log.Printf("[files-import] file %q failed: %v", child.Name, err)
		}
	}
	return nil
}

// importEnsureFolder returns the Vulos folder a provider folder maps to under
// vulosParentID, creating it on first import and reusing the mapped node on
// re-pull (additive — no duplicate folders).
func (s *Service) importEnsureFolder(job *ImportJob, ext *Node, vulosParentID string) (*Node, error) {
	if nodeID, _, ok := s.getImportItem(job.OwnerID, job.Provider, ext.ID); ok {
		if n, err := s.getNode(nodeID); err == nil && n.IsDir {
			return n, nil // re-pull: reuse the existing folder
		}
	}
	n, err := s.importCreateFolder(job.OwnerID, vulosParentID, ext.Name)
	if err != nil {
		return nil, err
	}
	if err := s.putImportItem(job.OwnerID, job.Provider, ext.ID, n.ID, true, job.ID); err != nil {
		return nil, err
	}
	return n, nil
}

// importFile copies one provider file into the Drive under vulosParentID. If the
// file was already imported (mapping present + node still exists) it is SKIPPED
// (idempotent re-pull). Otherwise it exports/streams the bytes, writes a Vulos
// copy (rename-on-collision), records a version, and remembers the mapping.
func (s *Service) importFile(ctx context.Context, src ImportSource, job *ImportJob, ext *Node, vulosParentID string, counts *importCounts) error {
	if nodeID, _, ok := s.getImportItem(job.OwnerID, job.Provider, ext.ID); ok {
		if _, err := s.getNode(nodeID); err == nil {
			counts.skipped++
			return nil // already imported, copy still present → additive no-op
		}
	}
	call, err := s.importCall(ctx, src, job.OwnerID)
	if err != nil {
		return err
	}
	rc, name, contentType, size, err := src.OpenForImport(ctx, call, ext)
	if err != nil {
		return err
	}
	defer rc.Close()
	n, err := s.importWriteFile(ctx, job.OwnerID, vulosParentID, name, contentType, size, rc)
	if err != nil {
		return err
	}
	if err := s.putImportItem(job.OwnerID, job.Provider, ext.ID, n.ID, false, job.ID); err != nil {
		return err
	}
	counts.imported++
	return nil
}

// importCreateFolder find-or-creates a folder named name under vulosParentID for
// ownerID. An existing folder of that name is reused (additive merge); a name
// already taken by a FILE forces a rename so the folder still lands.
func (s *Service) importCreateFolder(ownerID, parentID, name string) (*Node, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	parentPath, err := s.importParentPath(ownerID, parentID)
	if err != nil {
		return nil, err
	}
	if existing, err := s.childByName(ownerID, parentID, name); err == nil {
		if existing.IsDir {
			return existing, nil
		}
		name = s.importUniqueChildName(ownerID, parentID, name)
	}
	now := time.Now()
	n := &Node{
		ID:        ulid.NewULID(),
		OwnerID:   ownerID,
		ParentID:  parentID,
		Name:      name,
		IsDir:     true,
		Path:      joinPath(parentPath, name),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.insertNode(n); err != nil {
		return nil, err
	}
	return n, nil
}

// importWriteFile creates a file node under vulosParentID for ownerID, streams r
// into its canonical Drive object via the broker, and records a version. The
// name is made unique against existing siblings (rename-on-collision), matching
// the external-provider conflict approach.
func (s *Service) importWriteFile(ctx context.Context, ownerID, parentID, name, contentType string, size int64, r io.Reader) (*Node, error) {
	if s.broker == nil {
		return nil, fmt.Errorf("files: storage broker unavailable")
	}
	if err := validName(name); err != nil {
		return nil, err
	}
	parentPath, err := s.importParentPath(ownerID, parentID)
	if err != nil {
		return nil, err
	}
	name = s.importUniqueChildName(ownerID, parentID, name)
	path := joinPath(parentPath, name)
	now := time.Now()
	n := &Node{
		ID:          ulid.NewULID(),
		OwnerID:     ownerID,
		ParentID:    parentID,
		Name:        name,
		IsDir:       false,
		Bucket:      s.bucketFn(ownerID),
		ObjectKey:   driveKey(ownerID, path),
		Path:        path,
		ContentType: contentType,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.insertNode(n); err != nil {
		return nil, err
	}
	etag, err := s.broker.PutContent(ctx, ownerID, n.Bucket, n.ObjectKey, r, size, contentType)
	if err != nil {
		// Roll the index back so a failed copy leaves no phantom node.
		s.softDelete(n.ID)
		return nil, fmt.Errorf("files: import write: %w", err)
	}
	v := Version{
		ID:          ulid.NewULID(),
		NodeID:      n.ID,
		VersionKey:  n.ObjectKey,
		ContentType: contentType,
		ETag:        etag,
		CreatedBy:   ownerID,
		CreatedAt:   now,
	}
	if size >= 0 {
		v.Size = size
		n.Size = size
	}
	if err := s.insertVersion(v); err != nil {
		return nil, err
	}
	n.CurrentVersionID = v.ID
	if err := s.touchNode(n); err != nil {
		return nil, err
	}
	return n, nil
}

// importParentPath returns the Drive-relative path of vulosParentID for ownerID,
// or "" for the Drive root. It enforces that a non-root parent is a folder owned
// by ownerID so an import can only write into the owner's own tree.
func (s *Service) importParentPath(ownerID, parentID string) (string, error) {
	if parentID == "" {
		return "", nil
	}
	p, err := s.getNode(parentID)
	if err != nil {
		return "", err
	}
	if p.OwnerID != ownerID || !p.IsDir {
		return "", ErrInvalid
	}
	return p.Path, nil
}

// importUniqueChildName returns name if free under parentID, else the first
// "base (n)<ext>" candidate that is free — the same rename-on-collision helper
// the external providers use, applied to the Drive index.
func (s *Service) importUniqueChildName(ownerID, parentID, name string) string {
	return uniqueName(name, func(cand string) bool {
		_, err := s.childByName(ownerID, parentID, cand)
		return err == nil
	})
}
