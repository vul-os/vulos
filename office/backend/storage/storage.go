package storage

import (
	"vulos-office/backend/config"
	"vulos-office/backend/models"
)

// DefaultVersionCap is the maximum number of versions retained per file.
const DefaultVersionCap = 50

type Storage interface {
	// --- File CRUD ---
	ListFiles() ([]*models.File, error)
	GetFile(id string) (*models.File, error)
	CreateFile(file *models.File) error
	UpdateFile(file *models.File) error
	DeleteFile(id string) error

	// --- Version history (OFFICE-08 / OFFICE-28) ---
	// Snapshots created before each UpdateFile.
	ListVersions(fileID string) ([]*models.FileVersion, error)
	GetVersion(fileID, versionID string) (*models.FileVersion, error)
	CreateVersion(v *models.FileVersion) error
	// PruneVersions removes oldest versions so at most cap remain.
	PruneVersions(fileID string, cap int) error
	// LabelVersion sets a human-readable label on an existing version (OFFICE-28).
	LabelVersion(fileID, versionID, label string) error

	// --- Signing envelope CRUD (OFFICE-40) ---
	CreateEnvelope(env *models.Envelope) error
	GetEnvelope(id string) (*models.Envelope, error)
	ListEnvelopes() ([]*models.Envelope, error)
	UpdateEnvelope(env *models.Envelope) error
	DeleteEnvelope(id string) error

	// --- Signer management (OFFICE-40) ---
	UpsertSigner(s *models.Signer) error
	GetSigner(id string) (*models.Signer, error)
	ListSignersByEnvelope(envelopeID string) ([]*models.Signer, error)

	// --- Append-only audit log (OFFICE-40) ---
	// AppendAuditEvent inserts a new event; implementations MUST NOT
	// expose any update or delete path for audit records.
	AppendAuditEvent(ev *models.AuditEvent) error
	ListAuditEvents(envelopeID string) ([]*models.AuditEvent, error)

	// --- Signer token index (OFFICE-42) ---
	// StoreSignerToken persists a token → {envelopeID, signerID} mapping.
	StoreSignerToken(token, envelopeID, signerID string) error
	// ResolveToken looks up a token and returns the envelope + signer it scopes.
	ResolveToken(token string) (envelopeID, signerID string, err error)

	// --- Sealed PDF store (OFFICE-46) ---
	// StoreSealedPDF persists the final sealed PDF bytes for an envelope.
	StoreSealedPDF(envelopeID string, data []byte) error
	// GetSealedPDF retrieves previously stored sealed PDF bytes.
	// Returns an error if not yet generated.
	GetSealedPDF(envelopeID string) ([]byte, error)

	// --- Comments (OFFICE-26) ---
	CreateComment(c *models.Comment) error
	GetComment(fileID, commentID string) (*models.Comment, error)
	ListComments(fileID string) ([]*models.Comment, error)
	UpdateComment(c *models.Comment) error
	DeleteComment(fileID, commentID string) error

	CreateReply(r *models.CommentReply) error
	GetReply(commentID, replyID string) (*models.CommentReply, error)
	ListReplies(commentID string) ([]*models.CommentReply, error)
	UpdateReply(r *models.CommentReply) error

	// --- Scheduled meetings (OFFICE-65) ---
	CreateMeeting(m *models.Meeting) error
	GetMeeting(id string) (*models.Meeting, error)
	ListMeetings() ([]*models.Meeting, error)
	UpdateMeeting(m *models.Meeting) error
	DeleteMeeting(id string) error

	// --- Suggestions / track-changes (OFFICE-27) ---
	CreateSuggestion(s *models.Suggestion) error
	GetSuggestion(fileID, suggestionID string) (*models.Suggestion, error)
	ListSuggestions(fileID string) ([]*models.Suggestion, error)
	UpdateSuggestion(s *models.Suggestion) error
	DeleteSuggestion(fileID, suggestionID string) error
}

func New(cfg *config.Config) (Storage, error) {
	switch cfg.Storage.Type {
	case "postgres":
		return NewPostgresStorage(cfg)
	default:
		return NewLocalStorage(cfg)
	}
}
