package models

import "time"

// FieldType enumerates the kinds of fields a signer can fill.
type FieldType string

const (
	FieldTypeSignature FieldType = "signature"
	FieldTypeInitial   FieldType = "initial"
	FieldTypeDate      FieldType = "date"
	FieldTypeName      FieldType = "name"
	FieldTypeText      FieldType = "text"
)

// EnvelopeStatus tracks the lifecycle of a signing envelope.
type EnvelopeStatus string

const (
	EnvelopeStatusDraft     EnvelopeStatus = "draft"
	EnvelopeStatusSent      EnvelopeStatus = "sent"
	EnvelopeStatusCompleted EnvelopeStatus = "completed"
	EnvelopeStatusDeclined  EnvelopeStatus = "declined"
	EnvelopeStatusVoided    EnvelopeStatus = "voided"
)

// SigningOrderMode controls whether signers sign sequentially or in parallel.
type SigningOrderMode string

const (
	SigningOrderSequential SigningOrderMode = "sequential"
	SigningOrderParallel   SigningOrderMode = "parallel"
)

// SignerStatus tracks an individual signer's progress.
type SignerStatus string

const (
	SignerStatusPending   SignerStatus = "pending"
	SignerStatusSent      SignerStatus = "sent"
	SignerStatusViewed    SignerStatus = "viewed"
	SignerStatusSigned    SignerStatus = "signed"
	SignerStatusDeclined  SignerStatus = "declined"
)

// AuditAction enumerates all recordable actions on an envelope.
type AuditAction string

const (
	AuditActionCreated   AuditAction = "created"
	AuditActionSent      AuditAction = "sent"
	AuditActionViewed    AuditAction = "viewed"
	AuditActionSigned    AuditAction = "signed"
	AuditActionDeclined  AuditAction = "declined"
	AuditActionCompleted AuditAction = "completed" // OFFICE-46: all signers done, PDF sealed
	AuditActionVoided    AuditAction = "voided"    // OFFICE-45: envelope cancelled by owner
)

// Envelope is the top-level signing request container.
type Envelope struct {
	ID           string           `json:"id"`
	SourceFileID string           `json:"source_file_id"`
	Title        string           `json:"title"`
	Status       EnvelopeStatus   `json:"status"`
	OrderMode    SigningOrderMode  `json:"order_mode"`
	Fields       []*SigningField   `json:"fields"`
	Signers      []*Signer        `json:"signers"`
	CreatedAt    time.Time        `json:"created_at"`
	UpdatedAt    time.Time        `json:"updated_at"`

	// OFFICE-46: populated when the sealed PDF has been generated.
	FinalDocHash string     `json:"final_doc_hash,omitempty"`
	SealedAt     *time.Time `json:"sealed_at,omitempty"`
}

// SigningField describes a fill-in area placed on a specific page of the PDF.
type SigningField struct {
	ID       string    `json:"id"`
	Page     int       `json:"page"`      // 1-based
	X        float64   `json:"x"`         // points from left
	Y        float64   `json:"y"`         // points from top
	W        float64   `json:"w"`         // width in points
	H        float64   `json:"h"`         // height in points
	Type     FieldType `json:"type"`
	Required bool      `json:"required"`
	SignerID string    `json:"signer_id"` // assigned signer
	Value    string    `json:"value,omitempty"` // filled value after signing
}

// Signer represents one recipient who must act on the envelope.
type Signer struct {
	ID          string      `json:"id"`
	EnvelopeID  string      `json:"envelope_id"`
	Name        string      `json:"name"`
	Email       string      `json:"email"`     // email or Vulos account address
	AccountID   string      `json:"account_id,omitempty"` // Vulos account id if known
	Order       int         `json:"order"`     // 1-based; ties = parallel within that order
	Status      SignerStatus `json:"status"`
	Token       string      `json:"token,omitempty"`      // scoped signing link token
	TokenExpiry *time.Time  `json:"token_expiry,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// AuditEvent is an immutable record appended to the audit log.
// No update or delete is ever permitted — enforced at the storage layer.
type AuditEvent struct {
	ID             string      `json:"id"`
	EnvelopeID     string      `json:"envelope_id"`
	SignerID        string      `json:"signer_id,omitempty"`
	Action         AuditAction `json:"action"`
	Timestamp      time.Time   `json:"timestamp"`
	IP             string      `json:"ip,omitempty"`
	Identity       string      `json:"identity,omitempty"`      // Vulos account / email / link identity
	DocHashBefore  string      `json:"doc_hash_before,omitempty"`
	DocHashAfter   string      `json:"doc_hash_after,omitempty"`
	Token          string      `json:"token,omitempty"`          // Ed25519 signature token (OFFICE-44)
	PrevEventHash  string      `json:"prev_event_hash,omitempty"` // hash-chain link (OFFICE-44)
}

// --- request/response helpers ---

type CreateEnvelopeRequest struct {
	SourceFileID string           `json:"source_file_id" binding:"required"`
	Title        string           `json:"title" binding:"required"`
	OrderMode    SigningOrderMode  `json:"order_mode"`
}

type UpdateEnvelopeRequest struct {
	Title     string           `json:"title"`
	Status    EnvelopeStatus   `json:"status"`
	OrderMode SigningOrderMode  `json:"order_mode"`
}
