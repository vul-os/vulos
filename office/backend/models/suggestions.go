package models

// OFFICE-27: Suggestion (track-changes) model.
//
// A Suggestion records a proposed insertion or deletion in a Docs document.
// It is stored as a sidecar (analogous to Comments) and never modifies the
// base document until a reviewer accepts it.
//
// SeqClock is an HLC tick for CRDT LWW-merge across peers — the same clock
// scheme used by Comments and Spaces messages.

import "time"

// SuggestionKind distinguishes an insertion proposal from a deletion proposal.
type SuggestionKind string

const (
	SuggestionInsert SuggestionKind = "insert" // text was typed / pasted
	SuggestionDelete SuggestionKind = "delete" // text was removed
)

// SuggestionState tracks the reviewer decision.
type SuggestionState string

const (
	SuggestionPending  SuggestionState = "pending"
	SuggestionAccepted SuggestionState = "accepted"
	SuggestionRejected SuggestionState = "rejected"
)

// Suggestion is the wire/store record for one pending change.
type Suggestion struct {
	ID       string          `json:"id"`
	FileID   string          `json:"file_id"`
	Kind     SuggestionKind  `json:"kind"`
	State    SuggestionState `json:"state"`
	AuthorID string          `json:"author_id"`
	// From / To are the character-offset range in the base document text that
	// this suggestion targets. For an insert, From == To (cursor position).
	// For a delete, From < To is the range to remove.
	From     int    `json:"from"`
	To       int    `json:"to"`
	// Text is the proposed insertion text (empty for a delete suggestion).
	Text     string `json:"text"`
	SeqClock string `json:"seq_clock"`
	// ReviewerID is set when a reviewer accepts or rejects.
	ReviewerID string `json:"reviewer_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// SuggestionOp is the CRDT wire format for peer exchange.
type SuggestionOp struct {
	Op         string      `json:"op"` // "add"|"accept"|"reject"
	Suggestion *Suggestion `json:"suggestion"`
	AppliedAt  string      `json:"applied_at"`
}

// CreateSuggestionRequest is the POST body for creating a new suggestion.
type CreateSuggestionRequest struct {
	Kind     SuggestionKind `json:"kind" binding:"required"`
	AuthorID string         `json:"author_id"`
	From     int            `json:"from"`
	To       int            `json:"to"`
	Text     string         `json:"text"`
}

// UpdateSuggestionRequest is the PUT body for accepting or rejecting.
type UpdateSuggestionRequest struct {
	State      SuggestionState `json:"state" binding:"required"`
	ReviewerID string          `json:"reviewer_id"`
}
