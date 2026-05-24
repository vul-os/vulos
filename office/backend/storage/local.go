package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"vulos-office/backend/config"
	"vulos-office/backend/models"
)

type LocalStorage struct {
	dataDir        string
	versionsDir    string
	envelopesDir   string
	signersDir     string
	auditDir       string
	commentsDir    string
	repliesDir     string
	sealedDir      string // OFFICE-46: sealed PDF store
	suggestionsDir string // OFFICE-27: track-changes sidecar
}

func NewLocalStorage(cfg *config.Config) (*LocalStorage, error) {
	dir := cfg.Server.DataDir
	for _, sub := range []string{"", "versions", "envelopes", "signers", "audit", "comments", "replies", "sealed", "suggestions"} {
		d := filepath.Join(dir, sub)
		if err := os.MkdirAll(d, 0755); err != nil {
			return nil, fmt.Errorf("create dir %s: %w", d, err)
		}
	}
	return &LocalStorage{
		dataDir:        dir,
		versionsDir:    filepath.Join(dir, "versions"),
		envelopesDir:   filepath.Join(dir, "envelopes"),
		signersDir:     filepath.Join(dir, "signers"),
		auditDir:       filepath.Join(dir, "audit"),
		commentsDir:    filepath.Join(dir, "comments"),
		repliesDir:     filepath.Join(dir, "replies"),
		sealedDir:      filepath.Join(dir, "sealed"),
		suggestionsDir: filepath.Join(dir, "suggestions"),
	}, nil
}

// ---- helpers ----

func (s *LocalStorage) filePath(id string) string {
	return filepath.Join(s.dataDir, id+".json")
}

func (s *LocalStorage) versionPath(fileID, versionID string) string {
	return filepath.Join(s.versionsDir, fileID+"_"+versionID+".json")
}

func (s *LocalStorage) envelopePath(id string) string {
	return filepath.Join(s.envelopesDir, id+".json")
}

func (s *LocalStorage) signerPath(id string) string {
	return filepath.Join(s.signersDir, id+".json")
}

// auditDir/<envelopeID>/<eventID>.json — kept in a per-envelope sub-directory
// so ListAuditEvents can scan only relevant files.
func (s *LocalStorage) auditEventPath(envelopeID, eventID string) string {
	return filepath.Join(s.auditDir, envelopeID, eventID+".json")
}

// ============================================================
// File CRUD
// ============================================================

func (s *LocalStorage) ListFiles() ([]*models.File, error) {
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		return nil, err
	}

	var files []*models.File
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-5]
		file, err := s.GetFile(id)
		if err != nil {
			continue
		}
		files = append(files, file)
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].UpdatedAt.After(files[j].UpdatedAt)
	})
	return files, nil
}

func (s *LocalStorage) GetFile(id string) (*models.File, error) {
	data, err := os.ReadFile(s.filePath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found")
		}
		return nil, err
	}
	var file models.File
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	return &file, nil
}

func (s *LocalStorage) CreateFile(file *models.File) error {
	file.CreatedAt = time.Now()
	file.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath(file.ID), data, 0644)
}

func (s *LocalStorage) UpdateFile(file *models.File) error {
	existing, err := s.GetFile(file.ID)
	if err != nil {
		return err
	}

	// Snapshot the current content before overwriting.
	snap := &models.FileVersion{
		ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		FileID:    existing.ID,
		Name:      existing.Name,
		Content:   existing.Content,
		CreatedAt: time.Now(),
	}
	_ = s.CreateVersion(snap)
	_ = s.PruneVersions(existing.ID, DefaultVersionCap)

	file.CreatedAt = existing.CreatedAt
	file.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath(file.ID), data, 0644)
}

func (s *LocalStorage) DeleteFile(id string) error {
	if err := os.Remove(s.filePath(id)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("file not found")
		}
		return err
	}
	return nil
}

// ============================================================
// Version history (OFFICE-08)
// ============================================================

func (s *LocalStorage) CreateVersion(v *models.FileVersion) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.versionPath(v.FileID, v.ID), data, 0644)
}

func (s *LocalStorage) ListVersions(fileID string) ([]*models.FileVersion, error) {
	entries, err := os.ReadDir(s.versionsDir)
	if err != nil {
		return nil, err
	}
	prefix := fileID + "_"
	var versions []*models.FileVersion
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) || filepath.Ext(name) != ".json" {
			continue
		}
		base := strings.TrimSuffix(name, ".json")
		versionID := strings.TrimPrefix(base, prefix)
		v, err := s.GetVersion(fileID, versionID)
		if err != nil {
			continue
		}
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].CreatedAt.After(versions[j].CreatedAt)
	})
	return versions, nil
}

func (s *LocalStorage) GetVersion(fileID, versionID string) (*models.FileVersion, error) {
	data, err := os.ReadFile(s.versionPath(fileID, versionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("version not found")
		}
		return nil, err
	}
	var v models.FileVersion
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func (s *LocalStorage) PruneVersions(fileID string, cap int) error {
	versions, err := s.ListVersions(fileID)
	if err != nil {
		return err
	}
	// versions is newest-first; remove tail beyond cap
	for i := cap; i < len(versions); i++ {
		_ = os.Remove(s.versionPath(fileID, versions[i].ID))
	}
	return nil
}

// LabelVersion sets a user-defined label on a version (OFFICE-28).
func (s *LocalStorage) LabelVersion(fileID, versionID, label string) error {
	v, err := s.GetVersion(fileID, versionID)
	if err != nil {
		return err
	}
	v.Label = label
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.versionPath(fileID, versionID), data, 0644)
}

// ============================================================
// Signing — Envelope CRUD (OFFICE-40)
// ============================================================

func (s *LocalStorage) CreateEnvelope(env *models.Envelope) error {
	now := time.Now()
	env.CreatedAt = now
	env.UpdatedAt = now
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.envelopePath(env.ID), data, 0644)
}

func (s *LocalStorage) GetEnvelope(id string) (*models.Envelope, error) {
	data, err := os.ReadFile(s.envelopePath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("envelope not found")
		}
		return nil, err
	}
	var env models.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

func (s *LocalStorage) ListEnvelopes() ([]*models.Envelope, error) {
	entries, err := os.ReadDir(s.envelopesDir)
	if err != nil {
		return nil, err
	}
	var envs []*models.Envelope
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-5]
		env, err := s.GetEnvelope(id)
		if err != nil {
			continue
		}
		envs = append(envs, env)
	}
	sort.Slice(envs, func(i, j int) bool {
		return envs[i].UpdatedAt.After(envs[j].UpdatedAt)
	})
	return envs, nil
}

func (s *LocalStorage) UpdateEnvelope(env *models.Envelope) error {
	existing, err := s.GetEnvelope(env.ID)
	if err != nil {
		return err
	}
	env.CreatedAt = existing.CreatedAt
	env.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.envelopePath(env.ID), data, 0644)
}

func (s *LocalStorage) DeleteEnvelope(id string) error {
	if err := os.Remove(s.envelopePath(id)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("envelope not found")
		}
		return err
	}
	return nil
}

// ============================================================
// Signing — Signer management (OFFICE-40)
// ============================================================

func (s *LocalStorage) UpsertSigner(sg *models.Signer) error {
	now := time.Now()
	// If the signer already exists, preserve CreatedAt.
	if existing, err := s.GetSigner(sg.ID); err == nil {
		sg.CreatedAt = existing.CreatedAt
	} else {
		sg.CreatedAt = now
	}
	sg.UpdatedAt = now
	data, err := json.MarshalIndent(sg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.signerPath(sg.ID), data, 0644)
}

func (s *LocalStorage) GetSigner(id string) (*models.Signer, error) {
	data, err := os.ReadFile(s.signerPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("signer not found")
		}
		return nil, err
	}
	var sg models.Signer
	if err := json.Unmarshal(data, &sg); err != nil {
		return nil, err
	}
	return &sg, nil
}

func (s *LocalStorage) ListSignersByEnvelope(envelopeID string) ([]*models.Signer, error) {
	entries, err := os.ReadDir(s.signersDir)
	if err != nil {
		return nil, err
	}
	var signers []*models.Signer
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-5]
		sg, err := s.GetSigner(id)
		if err != nil {
			continue
		}
		if sg.EnvelopeID == envelopeID {
			signers = append(signers, sg)
		}
	}
	sort.Slice(signers, func(i, j int) bool {
		return signers[i].Order < signers[j].Order
	})
	return signers, nil
}

// ============================================================
// Signing — Token index (OFFICE-42)
// ============================================================
// Tokens are stored as <dataDir>/tokens/<token>.json → {envelope_id, signer_id}.

func (s *LocalStorage) tokenPath(token string) string {
	return filepath.Join(s.dataDir, "tokens", token+".json")
}

type localTokenRef struct {
	EnvelopeID string `json:"envelope_id"`
	SignerID   string `json:"signer_id"`
}

func (s *LocalStorage) StoreSignerToken(token, envelopeID, signerID string) error {
	dir := filepath.Join(s.dataDir, "tokens")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create tokens dir: %w", err)
	}
	ref := localTokenRef{EnvelopeID: envelopeID, SignerID: signerID}
	data, err := json.Marshal(ref)
	if err != nil {
		return err
	}
	return os.WriteFile(s.tokenPath(token), data, 0644)
}

func (s *LocalStorage) ResolveToken(token string) (string, string, error) {
	data, err := os.ReadFile(s.tokenPath(token))
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("token not found")
		}
		return "", "", err
	}
	var ref localTokenRef
	if err := json.Unmarshal(data, &ref); err != nil {
		return "", "", err
	}
	return ref.EnvelopeID, ref.SignerID, nil
}

// ============================================================
// Signing — Append-only audit log (OFFICE-40)
// ============================================================
// AppendAuditEvent writes a new, immutable event file.
// There is intentionally no UpdateAuditEvent or DeleteAuditEvent method.
func (s *LocalStorage) AppendAuditEvent(ev *models.AuditEvent) error {
	dir := filepath.Join(s.auditDir, ev.EnvelopeID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create audit envelope dir: %w", err)
	}
	path := s.auditEventPath(ev.EnvelopeID, ev.ID)
	// Guard: never overwrite an existing audit event.
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("audit event %s already exists (append-only)", ev.ID)
	}
	data, err := json.MarshalIndent(ev, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0444) // read-only permissions reinforce immutability
}

func (s *LocalStorage) ListAuditEvents(envelopeID string) ([]*models.AuditEvent, error) {
	dir := filepath.Join(s.auditDir, envelopeID)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil // no events yet — valid state
	}
	if err != nil {
		return nil, err
	}
	var events []*models.AuditEvent
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var ev models.AuditEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			continue
		}
		events = append(events, &ev)
	}
	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})
	return events, nil
}

// ============================================================
// Sealed PDF store (OFFICE-46)
// ============================================================

func (s *LocalStorage) StoreSealedPDF(envelopeID string, data []byte) error {
	path := filepath.Join(s.sealedDir, envelopeID+".pdf")
	return os.WriteFile(path, data, 0644)
}

func (s *LocalStorage) GetSealedPDF(envelopeID string) ([]byte, error) {
	path := filepath.Join(s.sealedDir, envelopeID+".pdf")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("sealed PDF not found")
		}
		return nil, err
	}
	return data, nil
}

// ============================================================
// Comments (OFFICE-26)
// ============================================================
// comments/<fileID>/<commentID>.json
// replies/<commentID>/<replyID>.json

func (s *LocalStorage) commentPath(fileID, commentID string) string {
	return filepath.Join(s.commentsDir, fileID, commentID+".json")
}

func (s *LocalStorage) replyPath(commentID, replyID string) string {
	return filepath.Join(s.repliesDir, commentID, replyID+".json")
}

func (s *LocalStorage) CreateComment(c *models.Comment) error {
	c.CreatedAt = time.Now()
	c.UpdatedAt = time.Now()
	dir := filepath.Join(s.commentsDir, c.FileID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.commentPath(c.FileID, c.ID), data, 0644)
}

func (s *LocalStorage) GetComment(fileID, commentID string) (*models.Comment, error) {
	data, err := os.ReadFile(s.commentPath(fileID, commentID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("comment not found")
		}
		return nil, err
	}
	var c models.Comment
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *LocalStorage) ListComments(fileID string) ([]*models.Comment, error) {
	dir := filepath.Join(s.commentsDir, fileID)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var comments []*models.Comment
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-5]
		c, err := s.GetComment(fileID, id)
		if err != nil {
			continue
		}
		comments = append(comments, c)
	}
	sort.Slice(comments, func(i, j int) bool {
		return comments[i].CreatedAt.Before(comments[j].CreatedAt)
	})
	return comments, nil
}

func (s *LocalStorage) UpdateComment(c *models.Comment) error {
	existing, err := s.GetComment(c.FileID, c.ID)
	if err != nil {
		return err
	}
	c.CreatedAt = existing.CreatedAt
	c.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.commentPath(c.FileID, c.ID), data, 0644)
}

func (s *LocalStorage) DeleteComment(fileID, commentID string) error {
	if err := os.Remove(s.commentPath(fileID, commentID)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("comment not found")
		}
		return err
	}
	return nil
}

func (s *LocalStorage) CreateReply(r *models.CommentReply) error {
	r.CreatedAt = time.Now()
	r.UpdatedAt = time.Now()
	dir := filepath.Join(s.repliesDir, r.CommentID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.replyPath(r.CommentID, r.ID), data, 0644)
}

func (s *LocalStorage) GetReply(commentID, replyID string) (*models.CommentReply, error) {
	data, err := os.ReadFile(s.replyPath(commentID, replyID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("reply not found")
		}
		return nil, err
	}
	var r models.CommentReply
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *LocalStorage) ListReplies(commentID string) ([]*models.CommentReply, error) {
	dir := filepath.Join(s.repliesDir, commentID)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var replies []*models.CommentReply
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-5]
		r, err := s.GetReply(commentID, id)
		if err != nil {
			continue
		}
		replies = append(replies, r)
	}
	sort.Slice(replies, func(i, j int) bool {
		return replies[i].CreatedAt.Before(replies[j].CreatedAt)
	})
	return replies, nil
}

func (s *LocalStorage) UpdateReply(r *models.CommentReply) error {
	existing, err := s.GetReply(r.CommentID, r.ID)
	if err != nil {
		return err
	}
	r.CreatedAt = existing.CreatedAt
	r.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.replyPath(r.CommentID, r.ID), data, 0644)
}

// ============================================================
// Meetings (OFFICE-65) — local JSON store
// ============================================================

func (s *LocalStorage) meetingPath(id string) string {
	return filepath.Join(s.dataDir, "meetings", id+".json")
}

func (s *LocalStorage) meetingsDir() string {
	return filepath.Join(s.dataDir, "meetings")
}

func (s *LocalStorage) ensureMeetingsDir() error {
	return os.MkdirAll(s.meetingsDir(), 0755)
}

func (s *LocalStorage) CreateMeeting(m *models.Meeting) error {
	if err := s.ensureMeetingsDir(); err != nil {
		return err
	}
	now := time.Now()
	m.CreatedAt = now
	m.UpdatedAt = now
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.meetingPath(m.ID), data, 0644)
}

func (s *LocalStorage) GetMeeting(id string) (*models.Meeting, error) {
	data, err := os.ReadFile(s.meetingPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("meeting not found")
		}
		return nil, err
	}
	var m models.Meeting
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *LocalStorage) ListMeetings() ([]*models.Meeting, error) {
	dir := s.meetingsDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var meetings []*models.Meeting
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-5]
		m, err := s.GetMeeting(id)
		if err != nil {
			continue
		}
		meetings = append(meetings, m)
	}
	sort.Slice(meetings, func(i, j int) bool {
		return meetings[i].CreatedAt.After(meetings[j].CreatedAt)
	})
	return meetings, nil
}

func (s *LocalStorage) UpdateMeeting(m *models.Meeting) error {
	existing, err := s.GetMeeting(m.ID)
	if err != nil {
		return err
	}
	m.CreatedAt = existing.CreatedAt
	m.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.meetingPath(m.ID), data, 0644)
}

func (s *LocalStorage) DeleteMeeting(id string) error {
	if err := os.Remove(s.meetingPath(id)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("meeting not found")
		}
		return err
	}
	return nil
}

// ============================================================
// Suggestions / track-changes (OFFICE-27)
// ============================================================
// suggestions/<fileID>/<suggestionID>.json

func (s *LocalStorage) suggestionPath(fileID, suggestionID string) string {
	return filepath.Join(s.suggestionsDir, fileID, suggestionID+".json")
}

func (s *LocalStorage) CreateSuggestion(sg *models.Suggestion) error {
	sg.CreatedAt = time.Now()
	sg.UpdatedAt = time.Now()
	dir := filepath.Join(s.suggestionsDir, sg.FileID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.suggestionPath(sg.FileID, sg.ID), data, 0644)
}

func (s *LocalStorage) GetSuggestion(fileID, suggestionID string) (*models.Suggestion, error) {
	data, err := os.ReadFile(s.suggestionPath(fileID, suggestionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("suggestion not found")
		}
		return nil, err
	}
	var sg models.Suggestion
	if err := json.Unmarshal(data, &sg); err != nil {
		return nil, err
	}
	return &sg, nil
}

func (s *LocalStorage) ListSuggestions(fileID string) ([]*models.Suggestion, error) {
	dir := filepath.Join(s.suggestionsDir, fileID)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var suggestions []*models.Suggestion
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-5]
		sg, err := s.GetSuggestion(fileID, id)
		if err != nil {
			continue
		}
		suggestions = append(suggestions, sg)
	}
	sort.Slice(suggestions, func(i, j int) bool {
		return suggestions[i].CreatedAt.Before(suggestions[j].CreatedAt)
	})
	return suggestions, nil
}

func (s *LocalStorage) UpdateSuggestion(sg *models.Suggestion) error {
	existing, err := s.GetSuggestion(sg.FileID, sg.ID)
	if err != nil {
		return err
	}
	sg.CreatedAt = existing.CreatedAt
	sg.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(sg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.suggestionPath(sg.FileID, sg.ID), data, 0644)
}

func (s *LocalStorage) DeleteSuggestion(fileID, suggestionID string) error {
	if err := os.Remove(s.suggestionPath(fileID, suggestionID)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("suggestion not found")
		}
		return err
	}
	return nil
}

