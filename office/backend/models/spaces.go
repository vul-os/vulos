package models

import "time"

// ChannelType distinguishes public/private channels from direct-message threads.
type ChannelType string

const (
	ChannelTypePublic  ChannelType = "public"
	ChannelTypePrivate ChannelType = "private"
	ChannelTypeDM      ChannelType = "dm" // direct-message; members list is authoritative
)

// MessageState captures whether a message is live, edited, or tombstoned.
type MessageState string

const (
	MessageStateActive  MessageState = "active"
	MessageStateEdited  MessageState = "edited"
	MessageStateTombed  MessageState = "deleted" // tombstone; body cleared, converges via CRDT
)

// Channel is a named conversation space (public, private, or DM).
type Channel struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Type      ChannelType `json:"type"`
	CreatedBy string      `json:"created_by"` // Vulos account id of creator
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// Membership records that a peer belongs to a channel.
type Membership struct {
	ID        string    `json:"id"`
	ChannelID string    `json:"channel_id"`
	AccountID string    `json:"account_id"` // Vulos account id
	JoinedAt  time.Time `json:"joined_at"`
}

// Message is a single unit of content in a channel or a thread.
// CRDT identity: (ChannelID, ID) is globally unique.
// Convergence rules:
//   - Append: new ID wins (LWW by HLCT timestamp on SeqClock).
//   - Edit:   highest SeqClock for same ID wins; body replaced.
//   - Delete: tombstone (State=deleted) is terminal; never un-deleted.
type Message struct {
	ID           string       `json:"id"`
	ChannelID    string       `json:"channel_id"`
	ThreadParent string       `json:"thread_parent,omitempty"` // id of the root message; "" = top-level
	AuthorID     string       `json:"author_id"`
	Body         string       `json:"body"`
	State        MessageState `json:"state"`
	// SeqClock is a hybrid logical clock value used by the CRDT merge function.
	// Format: "<wall-unix-ms>-<counter>-<node-id>" — string-sortable, globally unique.
	SeqClock  string    `json:"seq_clock"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ReadState records the furthest-read SeqClock per account per channel.
// Used for unread-count derivation; converges via LWW (highest SeqClock wins).
type ReadState struct {
	AccountID    string    `json:"account_id"`
	ChannelID    string    `json:"channel_id"`
	LastReadClock string   `json:"last_read_clock"` // SeqClock of last-read message
	UpdatedAt    time.Time `json:"updated_at"`
}

// --- CRDT op types (used by the Go store and mirrored in src/lib/crdt/messages.js) ---

// MessageOpType enumerates the CRDT operations that can be applied to messages.
type MessageOpType string

const (
	MessageOpAppend  MessageOpType = "append"  // new message
	MessageOpEdit    MessageOpType = "edit"    // replace body; SeqClock must be higher
	MessageOpTombstone MessageOpType = "tombstone" // delete; terminal
)

// MessageOp is a single CRDT operation on the message log.
// Ops are the unit of exchange between replicas.
type MessageOp struct {
	Op        MessageOpType `json:"op"`
	ChannelID string        `json:"channel_id"`
	Msg       Message       `json:"msg"`
	// AppliedAt is set by the receiving replica; not part of the causal identity.
	AppliedAt time.Time `json:"applied_at,omitempty"`
}

// --- request/response helpers ---

type CreateChannelRequest struct {
	Name    string      `json:"name" binding:"required"`
	Type    ChannelType `json:"type"`
	Members []string    `json:"members"` // for DMs / private channels
}

type SendMessageRequest struct {
	Body         string `json:"body" binding:"required"`
	ThreadParent string `json:"thread_parent,omitempty"`
}

type EditMessageRequest struct {
	Body string `json:"body" binding:"required"`
}

// ---- Reactions (OFFICE-SPACES-1) ----

// Reaction records one emoji reaction by one user on one message.
type Reaction struct {
	MessageID string    `json:"message_id"`
	Emoji     string    `json:"emoji"`
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

type ReactRequest struct {
	Emoji     string `json:"emoji"     binding:"required"`
	ChannelID string `json:"channel_id" binding:"required"`
}

// ---- Pins (OFFICE-SPACES-6) ----

// PinnedMessage records a message pinned in a channel.
type PinnedMessage struct {
	ChannelID string    `json:"channel_id"`
	MessageID string    `json:"message_id"`
	AuthorID  string    `json:"author_id"` // author of the original message
	Body      string    `json:"body"`      // body snapshot for the pinned panel
	PinnedBy  string    `json:"pinned_by"`
	PinnedAt  time.Time `json:"pinned_at"`
}

type PinRequest struct {
	MessageID string `json:"message_id" binding:"required"`
}

// ---- User status (OFFICE-SPACES-4) ----

// UserStatus persists per-user presence status.
type UserStatus struct {
	UserID     string    `json:"user_id"`
	Status     string    `json:"status"`      // online | away | busy | dnd
	CustomText string    `json:"custom_text"`
	UntilUnix  int64     `json:"until_unix"`  // 0 = indefinite
	UpdatedAt  time.Time `json:"updated_at"`
}

type SetStatusRequest struct {
	Status     string `json:"status"      binding:"required"`
	CustomText string `json:"custom_text"`
	UntilUnix  int64  `json:"until_unix"`
}

// Channel extended with description (OFFICE-SPACES-9)
type ChannelExt struct {
	Channel
	Description string `json:"description,omitempty"`
	MemberCount int    `json:"member_count,omitempty"`
	PinCount    int    `json:"pin_count,omitempty"`
}
