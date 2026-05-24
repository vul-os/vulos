package models

import "time"

type FileType string

const (
	FileTypeDoc   FileType = "doc"
	FileTypeSheet FileType = "sheet"
	FileTypeSlide FileType = "slide"
)

type File struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Type      FileType    `json:"type"`
	Content   interface{} `json:"content"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// FileVersion is an immutable snapshot of a file's content taken before each save.
type FileVersion struct {
	ID        string      `json:"id"`
	FileID    string      `json:"file_id"`
	Name      string      `json:"name"`      // file name at snapshot time
	Label     string      `json:"label,omitempty"` // optional user-defined label e.g. "v1 final draft"
	Content   interface{} `json:"content"`
	CreatedAt time.Time   `json:"created_at"`
}

// ActivityEventKind classifies an entry in the document activity feed.
type ActivityEventKind string

const (
	ActivityEdit    ActivityEventKind = "edit"
	ActivityComment ActivityEventKind = "comment"
	ActivitySign    ActivityEventKind = "sign"
	ActivitySnapshot ActivityEventKind = "snapshot"
)

// ActivityEvent is one entry in the per-document activity feed (OFFICE-28).
// It is derived/assembled by the handler — not stored as its own record.
type ActivityEvent struct {
	Kind      ActivityEventKind `json:"kind"`
	ID        string            `json:"id"`
	FileID    string            `json:"file_id"`
	Author    string            `json:"author,omitempty"` // display name / author_id / signer name
	Summary   string            `json:"summary"`          // human-readable description
	Label     string            `json:"label,omitempty"`  // snapshot label when kind=snapshot
	RefID     string            `json:"ref_id,omitempty"` // version id / comment id / signer id
	Timestamp time.Time         `json:"timestamp"`
}

// LabelVersionRequest is the request body for PUT /api/files/:id/versions/:vid/label.
type LabelVersionRequest struct {
	Label string `json:"label" binding:"required"`
}

type CreateFileRequest struct {
	Name    string      `json:"name" binding:"required"`
	Type    FileType    `json:"type" binding:"required"`
	Content interface{} `json:"content"`
}

type UpdateFileRequest struct {
	Name    string      `json:"name"`
	Content interface{} `json:"content"`
}

type LoginRequest struct {
	Password string `json:"password" binding:"required"`
}

type LoginResponse struct {
	Token   string `json:"token"`
	Message string `json:"message"`
}

type ErrorResponse struct {
	Error           string `json:"error"`
	RemainingAttempts int  `json:"remaining_attempts,omitempty"`
	LockedUntil     string `json:"locked_until,omitempty"`
}

type AuthStatusResponse struct {
	Enabled       bool `json:"enabled"`
	Authenticated bool `json:"authenticated"`
}

// ---- Comments (OFFICE-26) ----

// CommentAnchorType identifies what object a comment is attached to.
type CommentAnchorType string

const (
	AnchorTextRange CommentAnchorType = "text_range" // Docs: from/to character offsets
	AnchorCell      CommentAnchorType = "cell"       // Sheets: sheet/row/col
	AnchorSlide     CommentAnchorType = "slide"      // Slides: slide id
)

// CommentAnchor describes the location a comment is pinned to.
type CommentAnchor struct {
	Type CommentAnchorType `json:"type"`
	// text_range fields
	From int `json:"from,omitempty"`
	To   int `json:"to,omitempty"`
	// cell fields
	Sheet string `json:"sheet,omitempty"`
	Row   int    `json:"row,omitempty"`
	Col   int    `json:"col,omitempty"`
	// slide field
	SlideID string `json:"slide_id,omitempty"`
	// human-readable snapshot of the anchored text/cell (used if anchor orphans)
	Snapshot string `json:"snapshot,omitempty"`
}

// CommentState is the lifecycle of a comment thread.
type CommentState string

const (
	CommentOpen     CommentState = "open"
	CommentResolved CommentState = "resolved"
)

// Comment is the root of a thread, anchored to a file location.
// SeqClock is a CRDT HLC tick for LWW-merge across peers.
type Comment struct {
	ID        string        `json:"id"`
	FileID    string        `json:"file_id"`
	Anchor    CommentAnchor `json:"anchor"`
	AuthorID  string        `json:"author_id"`
	Body      string        `json:"body"`
	State     CommentState  `json:"state"`
	SeqClock  string        `json:"seq_clock"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

// CommentReply is a threaded reply to a Comment.
type CommentReply struct {
	ID        string    `json:"id"`
	CommentID string    `json:"comment_id"`
	FileID    string    `json:"file_id"`
	AuthorID  string    `json:"author_id"`
	Body      string    `json:"body"`
	SeqClock  string    `json:"seq_clock"`
	Deleted   bool      `json:"deleted,omitempty"` // tombstone
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CommentOp is the wire format for CRDT op exchange between peers/server.
type CommentOp struct {
	Op        string        `json:"op"` // "add_comment"|"edit_comment"|"resolve_comment"|"reopen_comment"|"add_reply"|"edit_reply"|"delete_reply"
	Comment   *Comment      `json:"comment,omitempty"`
	Reply     *CommentReply `json:"reply,omitempty"`
	AppliedAt string        `json:"applied_at"`
}

type CreateCommentRequest struct {
	Anchor   CommentAnchor `json:"anchor" binding:"required"`
	AuthorID string        `json:"author_id"`
	Body     string        `json:"body" binding:"required"`
}

type UpdateCommentRequest struct {
	Body  string       `json:"body"`
	State CommentState `json:"state"`
}

type CreateReplyRequest struct {
	AuthorID string `json:"author_id"`
	Body     string `json:"body" binding:"required"`
}

type UpdateReplyRequest struct {
	Body string `json:"body"`
}
