// envelope.go — signed canonical-JSON message envelope (PEER-03).
//
// Every message that crosses a Vula server boundary is wrapped in a signed
// envelope: the payload is serialised to a deterministic canonical-JSON byte
// string (sorted keys, no extra whitespace), signed with the sender's Ed25519
// private key, and the resulting signature is embedded in the envelope's
// "signature" field.
//
// Canonical JSON rules
//
//   - Object keys are sorted lexicographically (byte order of the UTF-8 key).
//   - No insignificant whitespace (no spaces, no newlines).
//   - The "signature" field is excluded from the bytes that are signed/verified.
//   - Arrays preserve their element order.
//   - Nested objects are also canonicalised recursively.
//
// Wire format
//
//	{
//	  "id":        "<string>",         // UUIDv7 or similar
//	  "from":      "<vula_id>",        // sender's Vula ID
//	  "to":        "<vula_id>",        // recipient Vula ID (empty for feed entries)
//	  "type":      "<string>",         // message|contact-request|signaling|feed
//	  "timestamp": "<RFC3339>",
//	  "payload":   <JSON object>,      // type-specific body
//	  "signature": "<base64url(sig)>"  // Ed25519 sig over canonical bytes of this object *without* the "signature" field
//	}
//
// Usage
//
//	// Sign
//	env, err := peering.NewEnvelope("msg-id", fromVulaID, toVulaID, peering.TypeMessage, body)
//	if err != nil { ... }
//	if err := env.Sign(priv); err != nil { ... }
//	wire, _ := json.Marshal(env)
//
//	// Verify
//	var env peering.Envelope
//	if err := json.Unmarshal(wire, &env); err != nil { ... }
//	if err := env.Verify(); err != nil { ... }
package peering

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// ─── Envelope type constants ───────────────────────────────────────────────────

const (
	// TypeMessage is a regular text/media message between two peers.
	TypeMessage = "message"

	// TypeContactRequest is sent when one peer requests to be added to another's
	// allow list.
	TypeContactRequest = "contact-request"

	// TypeSignaling carries WebRTC SDP or ICE signaling data for call setup.
	TypeSignaling = "signaling"

	// TypeFeed is an entry in a signed, append-only feed (see PEERING.md § Signed Feeds).
	TypeFeed = "feed"
)

// ─── Envelope ─────────────────────────────────────────────────────────────────

// Envelope is the signed canonical-JSON wrapper for all cross-node peering
// messages.  The zero value is not useful; use NewEnvelope.
type Envelope struct {
	// ID uniquely identifies this message (UUIDv7 recommended).
	ID string `json:"id"`

	// From is the sender's Vula ID ("vula:ed25519:<base58>").
	From string `json:"from"`

	// To is the recipient's Vula ID. Empty for feed entries or broadcasts.
	To string `json:"to,omitempty"`

	// Type is one of the Type* constants defined in this file.
	Type string `json:"type"`

	// Timestamp is the sender-side creation time (RFC 3339 with second precision).
	Timestamp string `json:"timestamp"`

	// Payload is the type-specific body. It must be a valid JSON object or
	// null. The envelope's canonical bytes are computed from the whole Envelope
	// (including Payload) minus the Signature field.
	Payload json.RawMessage `json:"payload"`

	// Signature is the base64url (no-padding) encoding of the Ed25519 signature
	// over the canonical bytes of this Envelope with the "signature" field absent.
	// Set by Sign; verified by Verify.
	Signature string `json:"signature,omitempty"`
}

// NewEnvelope constructs an Envelope with the current UTC time, leaving
// Signature empty.  payload must be valid JSON (or nil for a null payload).
func NewEnvelope(id, from, to, msgType string, payload json.RawMessage) (*Envelope, error) {
	if id == "" {
		return nil, errors.New("envelope: id must not be empty")
	}
	if from == "" {
		return nil, errors.New("envelope: from must not be empty")
	}
	if msgType == "" {
		return nil, errors.New("envelope: type must not be empty")
	}
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}
	return &Envelope{
		ID:        id,
		From:      from,
		To:        to,
		Type:      msgType,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   payload,
	}, nil
}

// ─── Canonical JSON serialisation ─────────────────────────────────────────────

// canonicalBytes returns the deterministic canonical-JSON encoding of the
// Envelope with the "signature" field omitted.  This is the byte string that
// is signed by the sender and verified by the recipient.
//
// The encoding is produced by (1) unmarshalling the envelope into a generic
// map, (2) deleting the "signature" key, and (3) re-marshalling with sorted
// keys via canonicaliseValue.
func (e *Envelope) canonicalBytes() ([]byte, error) {
	// Round-trip through a generic map so we can delete "signature".
	raw, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("envelope canonical: marshal: %w", err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("envelope canonical: unmarshal to map: %w", err)
	}

	// Remove the signature field — it must not be part of the signed bytes.
	delete(m, "signature")

	return canonicaliseObject(m)
}

// canonicaliseValue recursively canonicalises a JSON value decoded via
// json.Unmarshal into the canonical representation.
//
// For map[string]json.RawMessage the keys are sorted lexicographically.
// For []json.RawMessage the element order is preserved.
// All other values (string, number, bool, null) are re-encoded verbatim.
func canonicaliseValue(v json.RawMessage) ([]byte, error) {
	// Determine the kind of JSON value.
	if len(v) == 0 {
		return []byte("null"), nil
	}
	switch v[0] {
	case '{':
		// JSON object — sort keys.
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(v, &obj); err != nil {
			return nil, err
		}
		return canonicaliseObject(obj)
	case '[':
		// JSON array — preserve element order, recurse into values.
		var arr []json.RawMessage
		if err := json.Unmarshal(v, &arr); err != nil {
			return nil, err
		}
		return canonicaliseArray(arr)
	default:
		// Scalar (string, number, bool, null) — re-encode via json.Marshal to
		// normalise any whitespace or floating-point representation.
		var scalar any
		if err := json.Unmarshal(v, &scalar); err != nil {
			return nil, err
		}
		return json.Marshal(scalar)
	}
}

// canonicaliseObject encodes a map as a JSON object with lexicographically
// sorted keys, recursing into nested values.
func canonicaliseObject(obj map[string]json.RawMessage) ([]byte, error) {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := []byte{'{'}
	for i, k := range keys {
		if i > 0 {
			out = append(out, ',')
		}
		keyJSON, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		out = append(out, keyJSON...)
		out = append(out, ':')
		valJSON, err := canonicaliseValue(obj[k])
		if err != nil {
			return nil, err
		}
		out = append(out, valJSON...)
	}
	out = append(out, '}')
	return out, nil
}

// canonicaliseArray encodes a JSON array, recursing into each element.
func canonicaliseArray(arr []json.RawMessage) ([]byte, error) {
	out := []byte{'['}
	for i, elem := range arr {
		if i > 0 {
			out = append(out, ',')
		}
		elemJSON, err := canonicaliseValue(elem)
		if err != nil {
			return nil, err
		}
		out = append(out, elemJSON...)
	}
	out = append(out, ']')
	return out, nil
}

// ─── Sign / Verify ────────────────────────────────────────────────────────────

// Sign computes the canonical bytes of the envelope (signature field excluded),
// signs them with priv (an Ed25519 private key), and stores the base64url
// (no-padding) encoded signature in e.Signature.
//
// priv must be the private key that corresponds to the Vula ID in e.From.
// The caller is responsible for that invariant.
func (e *Envelope) Sign(priv ed25519.PrivateKey) error {
	if len(priv) != ed25519.PrivateKeySize {
		return fmt.Errorf("envelope sign: invalid private key length %d", len(priv))
	}

	canonical, err := e.canonicalBytes()
	if err != nil {
		return fmt.Errorf("envelope sign: %w", err)
	}

	sig := ed25519.Sign(priv, canonical)
	e.Signature = base64.RawURLEncoding.EncodeToString(sig)
	return nil
}

// Verify checks the envelope's signature.
//
// It derives the sender's public key from e.From (a Vula ID of the form
// "vula:ed25519:<base58>"), recomputes the canonical bytes (signature field
// excluded), and confirms that e.Signature is a valid Ed25519 signature over
// those bytes.
//
// Returns a non-nil error if the Vula ID is malformed, the signature is absent
// or corrupt, or the signature does not match the payload.
func (e *Envelope) Verify() error {
	if e.Signature == "" {
		return errors.New("envelope verify: signature is absent")
	}

	pub, err := decodeVulaID(e.From)
	if err != nil {
		return fmt.Errorf("envelope verify: %w", err)
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(e.Signature)
	if err != nil {
		return fmt.Errorf("envelope verify: malformed signature encoding: %w", err)
	}
	if len(sigBytes) != ed25519.SignatureSize {
		return fmt.Errorf("envelope verify: signature length %d, want %d", len(sigBytes), ed25519.SignatureSize)
	}

	canonical, err := e.canonicalBytes()
	if err != nil {
		return fmt.Errorf("envelope verify: %w", err)
	}

	if !ed25519.Verify(pub, canonical, sigBytes) {
		return errors.New("envelope verify: signature mismatch")
	}
	return nil
}

// ─── Payload shapes ───────────────────────────────────────────────────────────
//
// These structs describe the typed bodies that go into Envelope.Payload.
// They are not required by the sign/verify logic (which treats Payload as an
// opaque JSON blob) but are provided as a reference for producers and consumers.

// MessagePayload is the body of a TypeMessage envelope.
//
//	type:  "text" | "image" | "video" | "audio" | "file" | "location" | "contact"
//	body:  human-readable content (plain text for "text" type)
type MessagePayload struct {
	Type        string          `json:"type"`
	Body        string          `json:"body,omitempty"`
	Attachments []Attachment    `json:"attachments,omitempty"`
	Meta        json.RawMessage `json:"meta,omitempty"`
}

// Attachment describes a reference to a media file transferred separately.
type Attachment struct {
	Hash     string `json:"hash"`      // sha256:<hex>
	Size     int64  `json:"size"`      // bytes
	MIMEType string `json:"mime_type"` // e.g. "image/webp"
	Name     string `json:"name,omitempty"`
}

// ContactRequestPayload is the body of a TypeContactRequest envelope.
type ContactRequestPayload struct {
	DisplayName string `json:"display_name"`
	Message     string `json:"message,omitempty"` // optional intro message
	ServerAddr  string `json:"server_addr"`       // <host>:<port> for reply
	// VumailAddress is the sender's Vulos identity ("user@vulos.org" or
	// "user@custom-domain").  Including it in the signed contact card means
	// the recipient automatically populates the identity address field on the new
	// contact entry without a separate relay lookup.  Optional — peers that
	// have not yet claimed a Vulos identity omit this field.
	VumailAddress string `json:"vumail_address,omitempty"`
}

// SignalingPayload is the body of a TypeSignaling envelope.
//
//	kind: "offer" | "answer" | "ice-candidate" | "hangup"
type SignalingPayload struct {
	Kind      string          `json:"kind"`
	CallID    string          `json:"call_id"`
	SessionID string          `json:"session_id,omitempty"`
	Data      json.RawMessage `json:"data"` // SDP or ICE candidate JSON
}

// FeedPayload is the body of a TypeFeed envelope.
type FeedPayload struct {
	FeedID    string          `json:"feed_id"`
	Sequence  int64           `json:"sequence"`
	PrevHash  string          `json:"prev_hash,omitempty"` // sha256 of previous entry's canonical bytes
	EntryType string          `json:"entry_type"`          // "post" | "retraction" | etc.
	Body      json.RawMessage `json:"body"`
}
