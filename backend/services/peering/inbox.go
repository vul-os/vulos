// inbox.go — persistent inbox/outbox storage for peering messages (PEER-14).
//
// # Storage layout
//
//	~/.vulos/peering/inbox/
//	  └── <conv_id>/
//	      └── <timestamp>_<msg_id>.json   (one file per message, sorted by name)
//
// The conv_id directory name is produced by ConversationID() in messages.go,
// which sorts two Vula IDs lexicographically and joins them with "_".
//
// Each message file contains a JSON-encoded StoredMessage.  Files are named
// with an RFC3339 prefix (using '!' instead of ':' for cross-platform
// compatibility) followed by the message ID so directory listings are
// chronologically ordered.
//
// # Thread safety
//
// InboxStore is safe for concurrent use. All reads use directory listings;
// all writes create new files atomically (temp file + rename within the same
// directory) so no in-memory locking is required beyond what the OS provides.
//
// # Conversation list
//
// ListConversations scans the inbox root for sub-directories. Each sub-directory
// is a conversation.  The most-recently-modified file within a conversation
// directory determines the conversation's last-activity timestamp.
//
// # Usage
//
//	store, err := peering.NewInboxStore(home)
//	if err != nil { ... }
//
//	err = store.StoreMessage(msg)       // persist a message
//	msgs, err := store.GetMessages(id) // retrieve history
//	convs, err := store.ListConversations()
package peering

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ─── Direction ────────────────────────────────────────────────────────────────

// Direction indicates whether a stored message was sent or received.
type Direction string

const (
	// DirectionIn marks a message received from a remote peer.
	DirectionIn Direction = "in"

	// DirectionOut marks a message sent by the local user.
	DirectionOut Direction = "out"
)

// ─── StoredMessage ────────────────────────────────────────────────────────────

// StoredMessage is the on-disk representation of a single peering message.
// Both inbound messages (inbox) and outbound messages (outbox) use this struct;
// the Direction field distinguishes them.
type StoredMessage struct {
	// ID is the message's UUIDv4 identifier (set by the sender).
	ID string `json:"id"`

	// ConvID is the canonical conversation ID (ConversationID(a, b)).
	ConvID string `json:"conv_id"`

	// From is the sender's Vula ID.
	From string `json:"from"`

	// To is the recipient's Vula ID.
	To string `json:"to"`

	// Type is the message type ("text", "image", etc.).
	Type string `json:"type"`

	// Body is the plain-text body for "text" messages.
	Body string `json:"body,omitempty"`

	// Timestamp is when the message was created (sender-side clock).
	Timestamp time.Time `json:"timestamp"`

	// Direction is "in" (received) or "out" (sent).
	Direction Direction `json:"direction"`
}

// ─── ConversationSummary ──────────────────────────────────────────────────────

// ConversationSummary is returned by ListConversations.
type ConversationSummary struct {
	// ConvID is the canonical conversation identifier.
	ConvID string `json:"conv_id"`

	// LastActivity is the timestamp of the most recent message.
	LastActivity time.Time `json:"last_activity"`

	// MessageCount is the total number of stored messages.
	MessageCount int `json:"message_count"`
}

// ─── InboxStore ──────────────────────────────────────────────────────────────

// InboxStore manages on-disk storage of peering messages under
// ~/.vulos/peering/inbox/.
//
// The zero value is not usable; obtain one via NewInboxStore.
type InboxStore struct {
	// root is the absolute path to ~/.vulos/peering/inbox/
	root string
}

// NewInboxStore creates an InboxStore backed by
// filepath.Join(home, ".vulos", "peering", "inbox").
//
// The inbox directory is created idempotently on the first call.
func NewInboxStore(home string) (*InboxStore, error) {
	root := filepath.Join(home, ".vulos", "peering", "inbox")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("peering/inbox: mkdir %s: %w", root, err)
	}
	return &InboxStore{root: root}, nil
}

// ─── Write ────────────────────────────────────────────────────────────────────

// StoreMessage persists msg to disk under the conversation sub-directory.
//
// The file is written atomically (temp file + rename). If a message with the
// same ID already exists in the same conversation, StoreMessage is a no-op
// (idempotent delivery).
func (s *InboxStore) StoreMessage(msg StoredMessage) error {
	if msg.ID == "" {
		return fmt.Errorf("peering/inbox: StoreMessage: message ID must not be empty")
	}
	if msg.ConvID == "" {
		return fmt.Errorf("peering/inbox: StoreMessage: conv_id must not be empty")
	}

	convDir := filepath.Join(s.root, sanitizePath(msg.ConvID))
	if err := os.MkdirAll(convDir, 0700); err != nil {
		return fmt.Errorf("peering/inbox: mkdir conv dir %s: %w", convDir, err)
	}

	// File name: <timestamp-safe>_<msg_id>.json
	// Use '!' in place of ':' so the name is valid on all platforms.
	tsStr := strings.ReplaceAll(msg.Timestamp.UTC().Format(time.RFC3339), ":", "!")
	filename := fmt.Sprintf("%s_%s.json", tsStr, sanitizePath(msg.ID))
	dst := filepath.Join(convDir, filename)

	// Idempotency: if the file already exists, skip write.
	if _, err := os.Stat(dst); err == nil {
		return nil
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("peering/inbox: marshal message %s: %w", msg.ID, err)
	}

	// Atomic write: temp file in the same directory + rename.
	tmp, err := os.CreateTemp(convDir, ".msg-*.json.tmp")
	if err != nil {
		return fmt.Errorf("peering/inbox: create temp file: %w", err)
	}
	tmpName := tmp.Name()

	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmpName)
		if werr != nil {
			return fmt.Errorf("peering/inbox: write temp: %w", werr)
		}
		return fmt.Errorf("peering/inbox: close temp: %w", cerr)
	}

	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("peering/inbox: rename to %s: %w", dst, err)
	}

	return nil
}

// ─── Read ─────────────────────────────────────────────────────────────────────

// GetMessages returns all stored messages for convID, sorted oldest-first.
//
// If no conversation directory exists, an empty slice is returned (not an error).
func (s *InboxStore) GetMessages(convID string) ([]StoredMessage, error) {
	convDir := filepath.Join(s.root, sanitizePath(convID))

	entries, err := os.ReadDir(convDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("peering/inbox: read conv dir %s: %w", convDir, err)
	}

	var msgs []StoredMessage
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		// Skip temp files left by a crash.
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		path := filepath.Join(convDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			// Log and skip corrupt/missing file rather than aborting.
			continue
		}

		var m StoredMessage
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		msgs = append(msgs, m)
	}

	// os.ReadDir returns entries in directory order (alphabetical), which for
	// our timestamp-prefixed filenames is chronological.  Sort explicitly to
	// be safe.
	sort.Slice(msgs, func(i, j int) bool {
		return msgs[i].Timestamp.Before(msgs[j].Timestamp)
	})

	return msgs, nil
}

// ListConversations returns a summary of every conversation in the inbox,
// sorted by LastActivity descending (most recent first).
func (s *InboxStore) ListConversations() ([]ConversationSummary, error) {
	entries, err := os.ReadDir(s.root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("peering/inbox: read inbox root: %w", err)
	}

	var summaries []ConversationSummary

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		convID := entry.Name()
		convDir := filepath.Join(s.root, convID)

		msgEntries, err := os.ReadDir(convDir)
		if err != nil {
			continue
		}

		var count int
		var lastActivity time.Time

		for _, me := range msgEntries {
			if me.IsDir() || !strings.HasSuffix(me.Name(), ".json") {
				continue
			}
			if strings.HasPrefix(me.Name(), ".") {
				continue
			}
			count++

			// Extract timestamp from filename prefix for cheap last-activity
			// computation (avoids reading every file).
			ts, err := timestampFromFilename(me.Name())
			if err == nil && ts.After(lastActivity) {
				lastActivity = ts
			}
		}

		if count == 0 {
			continue
		}

		summaries = append(summaries, ConversationSummary{
			ConvID:       convID,
			LastActivity: lastActivity,
			MessageCount: count,
		})
	}

	// Sort newest-first.
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].LastActivity.After(summaries[j].LastActivity)
	})

	return summaries, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// sanitizePath replaces characters that are unsafe in file/directory names with
// a safe equivalent.  This prevents path traversal via crafted Vula IDs or
// conversation IDs.
//
// Allowed characters: letters, digits, '-', '_', ':'.
// The '.' character is intentionally excluded to prevent ".." path traversal
// sequences.  Everything else is replaced with '-'.
func sanitizePath(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == ':' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

// timestampFromFilename recovers the UTC time encoded at the start of a message
// filename.  The format is:
//
//	<RFC3339 with '!' replacing ':'>_<uuid>.json
//
// e.g. 2026-05-16T12!00!00Z_some-uuid.json
//
// Returns an error if the prefix cannot be parsed.
func timestampFromFilename(name string) (time.Time, error) {
	// Strip the .json suffix.
	base := strings.TrimSuffix(name, ".json")

	// Find the split between timestamp and message-id.
	// Timestamp ends before the last '_' that is followed by what looks like
	// a UUID (contains '-' characters). Actually the simpler approach: split
	// on the first '_' that is NOT inside the timestamp portion.
	//
	// RFC3339 with '!' substituted: 2026-05-16T12!00!00Z  (19 chars + optional timezone)
	// We know RFC3339 UTC is exactly 20 chars (2006-01-02T15!04!05Z).
	// Split after the 20th character (or at the next '_' after position 19).

	if len(base) < 20 {
		return time.Time{}, fmt.Errorf("filename too short")
	}

	// Find the underscore separating the timestamp from the message ID.
	// The timestamp portion contains '-', 'T', and '!' (replacing ':').
	// The first '_' character marks the separator.
	idx := strings.Index(base, "_")
	if idx < 0 {
		return time.Time{}, fmt.Errorf("no separator found")
	}

	tsStr := base[:idx]
	// Restore ':' from '!'.
	tsStr = strings.ReplaceAll(tsStr, "!", ":")

	t, err := time.Parse(time.RFC3339, tsStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", tsStr, err)
	}
	return t, nil
}
