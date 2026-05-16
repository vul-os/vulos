// groups.go — group/room definition, membership management, and message fan-out (PEER-18).
//
// # Overview
//
// A group is a named list of approved Vula IDs. No central server owns the group —
// every member's server stores the group definition and every message is fanned
// out server-to-server to each member individually.
//
// # Storage layout
//
//	~/.vulos/peering/groups/
//	  └── <group_id>.json    (one file per group)
//
// Each file is a JSON-encoded GroupDef.
//
// # Envelope types
//
//	TypeGroupDef     — distributed to all members when a group is created or updated
//	TypeGroupMessage — fan-out message; one signed envelope per recipient
//
// # Policies
//
//	PolicyOpen   — any member may add new members
//	PolicyAdminOnly — only the creator may add new members
//
// # Routes registered by RegisterGroupHandlers
//
//	POST /api/peering/groups                         — create a group
//	GET  /api/peering/groups                         — list groups the local node belongs to
//	GET  /api/peering/groups/{group_id}              — get group definition
//	POST /api/peering/groups/{group_id}/members      — add a member
//	POST /api/peering/groups/{group_id}/send         — send a message to the group
//	POST /api/peering/inbound/group-def              — receive a group definition from a peer
//	POST /api/peering/inbound/group-message          — receive a group message from a peer
//
// # Usage
//
//	store, _ := peering.NewGroupStore(home)
//	api := peering.NewGroupAPI(store, contacts, inbox, client, hub, priv, vulaID)
//	api.RegisterGroupHandlers(mux)
package peering

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ─── Envelope type constants ───────────────────────────────────────────────────

const (
	// TypeGroupDef carries a GroupDef to a member after group creation or member addition.
	TypeGroupDef = "group-def"

	// TypeGroupMessage is a fan-out message addressed to a group.
	TypeGroupMessage = "group-message"
)

// ─── Group policy ─────────────────────────────────────────────────────────────

// GroupPolicy governs who may add new members to a group.
type GroupPolicy string

const (
	// PolicyOpen allows any member to add new members.
	PolicyOpen GroupPolicy = "open"

	// PolicyAdminOnly restricts member additions to the group creator.
	PolicyAdminOnly GroupPolicy = "admin_only"
)

// ─── GroupDef ─────────────────────────────────────────────────────────────────

// GroupDef is the authoritative definition of a group. It is stored on every
// member's server and distributed to new members when they are added.
type GroupDef struct {
	// ID is the globally unique group identifier (UUIDv4).
	ID string `json:"id"`

	// Name is the human-readable group name.
	Name string `json:"name"`

	// CreatorVulaID is the Vula ID of the member who created the group.
	CreatorVulaID string `json:"creator_vula_id"`

	// Members is the ordered list of member Vula IDs.
	// The creator is always the first entry.
	Members []string `json:"members"`

	// Policy controls who may add new members.
	Policy GroupPolicy `json:"policy"`

	// CreatedAt is when the group was created (creator's clock).
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt is when the group definition was last updated.
	UpdatedAt time.Time `json:"updated_at"`
}

// hasMember reports whether vulaID is in the group's member list.
func (g *GroupDef) hasMember(vulaID string) bool {
	for _, m := range g.Members {
		if m == vulaID {
			return true
		}
	}
	return false
}

// ─── GroupMessagePayload ──────────────────────────────────────────────────────

// GroupMessagePayload is the Payload of a TypeGroupMessage envelope.
type GroupMessagePayload struct {
	// GroupID identifies the group this message was sent to.
	GroupID string `json:"group_id"`

	// Type is the content type: "text", "image", "file", etc.
	Type string `json:"type"`

	// Body is the plain-text body for "text" messages.
	Body string `json:"body,omitempty"`
}

// ─── GroupStore ───────────────────────────────────────────────────────────────

// GroupStore persists group definitions to ~/.vulos/peering/groups/.
// Each group is stored as <group_id>.json. Thread-safe.
type GroupStore struct {
	root string // absolute path to ~/.vulos/peering/groups/
	mu   sync.RWMutex
}

// NewGroupStore creates a GroupStore backed by
// filepath.Join(home, ".vulos", "peering", "groups").
//
// The directory is created idempotently.
func NewGroupStore(home string) (*GroupStore, error) {
	root := filepath.Join(home, ".vulos", "peering", "groups")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, fmt.Errorf("peering/groups: mkdir %s: %w", root, err)
	}
	return &GroupStore{root: root}, nil
}

// Save writes def to disk atomically. If the file already exists it is
// overwritten (idempotent for the same group_id).
func (s *GroupStore) Save(def GroupDef) error {
	if def.ID == "" {
		return fmt.Errorf("peering/groups: Save: group ID must not be empty")
	}

	data, err := json.MarshalIndent(def, "", "  ")
	if err != nil {
		return fmt.Errorf("peering/groups: Save: marshal %s: %w", def.ID, err)
	}

	dst := filepath.Join(s.root, sanitizePath(def.ID)+".json")

	s.mu.Lock()
	defer s.mu.Unlock()

	return groupAtomicWrite(s.root, dst, data)
}

// Get returns the group definition for id, or (zero, false) if not found.
func (s *GroupStore) Get(id string) (GroupDef, bool) {
	path := filepath.Join(s.root, sanitizePath(id)+".json")

	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return GroupDef{}, false
	}
	var def GroupDef
	if err := json.Unmarshal(data, &def); err != nil {
		return GroupDef{}, false
	}
	return def, true
}

// List returns all group definitions stored locally.
func (s *GroupStore) List() ([]GroupDef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("peering/groups: List: read dir: %w", err)
	}

	var defs []GroupDef
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.root, entry.Name()))
		if err != nil {
			continue
		}
		var def GroupDef
		if err := json.Unmarshal(data, &def); err != nil {
			continue
		}
		defs = append(defs, def)
	}
	return defs, nil
}

// groupAtomicWrite writes data to dst using a temp file + rename within dir.
func groupAtomicWrite(dir, dst string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".grp-*.json.tmp")
	if err != nil {
		return fmt.Errorf("peering/groups: create temp: %w", err)
	}
	tmpName := tmp.Name()

	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmpName)
		if werr != nil {
			return fmt.Errorf("peering/groups: write temp: %w", werr)
		}
		return fmt.Errorf("peering/groups: close temp: %w", cerr)
	}

	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("peering/groups: rename to %s: %w", dst, err)
	}
	return nil
}

// ─── GroupAPI ─────────────────────────────────────────────────────────────────

// GroupAPI bundles the dependencies for group HTTP handlers.
// Obtain one via NewGroupAPI; the zero value is not usable.
type GroupAPI struct {
	groups *GroupStore
	store  *ContactStore
	inbox  *InboxStore
	client *PeerClient
	hub    *Hub
	priv   ed25519.PrivateKey
	vulaID string // local node's Vula ID
}

// NewGroupAPI constructs a GroupAPI.
//
//   - groups  — group definition store
//   - store   — contacts store for permission checks and peer server lookups
//   - inbox   — inbox/outbox storage for group messages
//   - client  — outbound HTTP client for server-to-server delivery
//   - hub     — WebSocket hub for real-time browser pushes (may be nil in tests)
//   - priv    — local Ed25519 private key used to sign outbound envelopes
//   - vulaID  — canonical Vula ID of the local node
func NewGroupAPI(
	groups *GroupStore,
	store *ContactStore,
	inbox *InboxStore,
	client *PeerClient,
	hub *Hub,
	priv ed25519.PrivateKey,
	vulaID string,
) *GroupAPI {
	return &GroupAPI{
		groups: groups,
		store:  store,
		inbox:  inbox,
		client: client,
		hub:    hub,
		priv:   priv,
		vulaID: vulaID,
	}
}

// RegisterGroupHandlers wires all group-related routes onto mux.
//
// Routes registered:
//
//	POST /api/peering/groups
//	GET  /api/peering/groups
//	GET  /api/peering/groups/{group_id}
//	POST /api/peering/groups/{group_id}/members
//	POST /api/peering/groups/{group_id}/send
//	POST /api/peering/inbound/group-def
//	POST /api/peering/inbound/group-message
//
// The inbound routes must be wrapped in InboundMiddleware by the caller:
//
//	inboundMux := http.NewServeMux()
//	inboundMux.HandleFunc("POST /api/peering/inbound/group-def",
//	    api.HandleInboundGroupDef)
//	inboundMux.HandleFunc("POST /api/peering/inbound/group-message",
//	    api.HandleInboundGroupMessage)
//	mux.Handle("/api/peering/inbound/", peering.InboundMiddleware(store, inboundMux))
func RegisterGroupHandlers(mux *http.ServeMux, api *GroupAPI) {
	// Browser-facing routes.
	mux.HandleFunc("POST /api/peering/groups", api.handleCreateGroup)
	mux.HandleFunc("GET /api/peering/groups", api.handleListGroups)
	mux.HandleFunc("GET /api/peering/groups/{group_id}", api.handleGetGroup)
	mux.HandleFunc("POST /api/peering/groups/{group_id}/members", api.handleAddMember)
	mux.HandleFunc("POST /api/peering/groups/{group_id}/send", api.handleSendGroupMessage)

	// Inbound server-to-server routes (must be under InboundMiddleware).
	mux.HandleFunc("POST /api/peering/inbound/group-def", api.HandleInboundGroupDef)
	mux.HandleFunc("POST /api/peering/inbound/group-message", api.HandleInboundGroupMessage)
}

// ─── Create group ─────────────────────────────────────────────────────────────

// createGroupRequest is the JSON body for POST /api/peering/groups.
type createGroupRequest struct {
	// Name is the human-readable group name.
	Name string `json:"name"`

	// Members is a list of peer Vula IDs to invite (must be approved contacts).
	Members []string `json:"members"`

	// Policy controls who may add new members. Defaults to PolicyOpen.
	Policy GroupPolicy `json:"policy,omitempty"`
}

// handleCreateGroup implements POST /api/peering/groups.
//
// Steps:
//  1. Parse and validate the request.
//  2. Verify all members are approved contacts.
//  3. Create the GroupDef (creator is prepended automatically).
//  4. Persist locally.
//  5. Distribute the GroupDef to all members via fan-out.
func (a *GroupAPI) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var req createGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid request body: " + err.Error(),
		})
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "group name must not be empty",
		})
		return
	}

	// Validate and deduplicate members; creator is added automatically.
	memberSet := make(map[string]struct{})
	memberSet[a.vulaID] = struct{}{} // creator is always a member
	for _, m := range req.Members {
		if m == "" || m == a.vulaID {
			continue
		}
		if !a.store.IsApproved(m) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "member is not an approved contact",
				"peer":  m,
			})
			return
		}
		memberSet[m] = struct{}{}
	}

	// Build ordered member list: creator first, then the rest.
	members := []string{a.vulaID}
	for _, m := range req.Members {
		if m == "" || m == a.vulaID {
			continue
		}
		if _, seen := memberSet[m]; seen {
			// Only add if not already added (dedup preserving request order).
			alreadyAdded := false
			for _, existing := range members {
				if existing == m {
					alreadyAdded = true
					break
				}
			}
			if !alreadyAdded {
				members = append(members, m)
			}
		}
	}

	policy := req.Policy
	if policy == "" {
		policy = PolicyOpen
	}
	if policy != PolicyOpen && policy != PolicyAdminOnly {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "unknown policy; use 'open' or 'admin_only'",
		})
		return
	}

	now := time.Now().UTC()
	def := GroupDef{
		ID:            uuid.New().String(),
		Name:          req.Name,
		CreatorVulaID: a.vulaID,
		Members:       members,
		Policy:        policy,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	// Persist locally before distributing.
	if err := a.groups.Save(def); err != nil {
		log.Printf("[peering/groups] save group %s: %v", def.ID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to persist group",
		})
		return
	}

	// Distribute the group definition to all non-local members.
	a.distributeGroupDef(r.Context(), def)

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":      def.ID,
		"name":    def.Name,
		"members": def.Members,
		"policy":  string(def.Policy),
	})
}

// ─── List / Get group ─────────────────────────────────────────────────────────

// handleListGroups implements GET /api/peering/groups.
func (a *GroupAPI) handleListGroups(w http.ResponseWriter, r *http.Request) {
	defs, err := a.groups.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to list groups",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"groups": defs,
		"count":  len(defs),
	})
}

// handleGetGroup implements GET /api/peering/groups/{group_id}.
func (a *GroupAPI) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("group_id")
	if groupID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "group_id is required"})
		return
	}

	def, ok := a.groups.Get(groupID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "group not found"})
		return
	}

	writeJSON(w, http.StatusOK, def)
}

// ─── Add member ───────────────────────────────────────────────────────────────

// addMemberRequest is the JSON body for POST /api/peering/groups/{group_id}/members.
type addMemberRequest struct {
	// VulaID is the Vula ID of the member to add.
	VulaID string `json:"vula_id"`
}

// handleAddMember implements POST /api/peering/groups/{group_id}/members.
//
// Policy gate:
//   - PolicyOpen: any member of the group may add another approved contact.
//   - PolicyAdminOnly: only the group creator may add members.
func (a *GroupAPI) handleAddMember(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("group_id")
	if groupID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "group_id is required"})
		return
	}

	def, ok := a.groups.Get(groupID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "group not found"})
		return
	}

	// Local node must be a member to add someone.
	if !def.hasMember(a.vulaID) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "local node is not a member of this group",
		})
		return
	}

	// Policy gate.
	if def.Policy == PolicyAdminOnly && def.CreatorVulaID != a.vulaID {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "group policy is admin_only; only the creator may add members",
		})
		return
	}

	var req addMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid request body: " + err.Error(),
		})
		return
	}
	if req.VulaID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "vula_id is required"})
		return
	}
	if def.hasMember(req.VulaID) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "member already in group",
		})
		return
	}

	// The new member must be an approved contact of the local node.
	if req.VulaID != a.vulaID && !a.store.IsApproved(req.VulaID) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "target is not an approved contact",
			"peer":  req.VulaID,
		})
		return
	}

	// Update the group definition.
	def.Members = append(def.Members, req.VulaID)
	def.UpdatedAt = time.Now().UTC()

	if err := a.groups.Save(def); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to persist updated group",
		})
		return
	}

	// Distribute the updated definition to ALL current members (including the new one).
	a.distributeGroupDef(r.Context(), def)

	writeJSON(w, http.StatusOK, map[string]any{
		"id":      def.ID,
		"members": def.Members,
	})
}

// ─── Send group message ───────────────────────────────────────────────────────

// sendGroupMessageRequest is the JSON body for POST /api/peering/groups/{group_id}/send.
type sendGroupMessageRequest struct {
	// Body is the plain-text content of the message.
	Body string `json:"body"`
}

// handleSendGroupMessage implements POST /api/peering/groups/{group_id}/send.
//
// The message is fan-out to every member (except the sender) via individual
// signed envelopes. A copy is stored in the local inbox/outbox.
func (a *GroupAPI) handleSendGroupMessage(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("group_id")
	if groupID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "group_id is required"})
		return
	}

	def, ok := a.groups.Get(groupID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "group not found"})
		return
	}

	if !def.hasMember(a.vulaID) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "local node is not a member of this group",
		})
		return
	}

	var req sendGroupMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid request body: " + err.Error(),
		})
		return
	}
	if strings.TrimSpace(req.Body) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must not be empty"})
		return
	}

	msgID := uuid.New().String()
	now := time.Now().UTC()

	payload, err := json.Marshal(GroupMessagePayload{
		GroupID: groupID,
		Type:    "text",
		Body:    req.Body,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "marshal payload: " + err.Error(),
		})
		return
	}

	// Fan-out: one signed envelope per recipient (not the sender).
	var deliveryErrors []string
	for _, memberID := range def.Members {
		if memberID == a.vulaID {
			continue // don't send to ourselves
		}

		contact, exists := a.store.Get(memberID)
		if !exists || contact.Server == "" {
			log.Printf("[peering/groups] member %s has no server address; skipping", memberID)
			deliveryErrors = append(deliveryErrors,
				fmt.Sprintf("member %s: no server address", memberID))
			continue
		}

		env, err := NewEnvelope(msgID+"-"+sanitizePath(memberID), a.vulaID, memberID, TypeGroupMessage, json.RawMessage(payload))
		if err != nil {
			log.Printf("[peering/groups] build envelope for %s: %v", memberID, err)
			continue
		}
		env.Timestamp = now.Format(time.RFC3339)

		if err := env.Sign(a.priv); err != nil {
			log.Printf("[peering/groups] sign envelope for %s: %v", memberID, err)
			continue
		}

		baseURL := "https://" + contact.Server
		if err := a.client.Post(r.Context(), baseURL, TypeGroupMessage, env); err != nil {
			log.Printf("[peering/groups] delivery to %s failed: %v", baseURL, err)
			deliveryErrors = append(deliveryErrors,
				fmt.Sprintf("member %s: %s", memberID, err.Error()))
		}
	}

	// Store a local copy in the outbox (sender side).
	convID := groupConversationID(groupID)
	stored := StoredMessage{
		ID:        msgID,
		ConvID:    convID,
		From:      a.vulaID,
		To:        groupID,
		Type:      "text",
		Body:      req.Body,
		Timestamp: now,
		Direction: DirectionOut,
	}
	if err := a.inbox.StoreMessage(stored); err != nil {
		log.Printf("[peering/groups] outbox store error: %v", err)
	}

	// Push real-time frame to sender's browser tabs.
	a.pushGroupFrame(groupID, stored)

	resp := map[string]any{
		"id":       msgID,
		"group_id": groupID,
		"status":   "delivered",
	}
	if len(deliveryErrors) > 0 {
		resp["delivery_errors"] = deliveryErrors
		resp["status"] = "partial"
	}
	writeJSON(w, http.StatusCreated, resp)
}

// ─── Inbound: receive a group definition ──────────────────────────────────────

// HandleInboundGroupDef implements POST /api/peering/inbound/group-def.
//
// InboundMiddleware has already verified the envelope signature and confirmed
// the sender is in the approved contacts list.
//
// This handler stores the received group definition, replacing any existing
// definition for the same group_id. This allows group updates (member additions)
// to propagate.
func (a *GroupAPI) HandleInboundGroupDef(w http.ResponseWriter, r *http.Request) {
	env, ok := r.Context().Value(EnvelopeKey).(*Envelope)
	if !ok || env == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "envelope not found in context — ensure InboundMiddleware is applied",
		})
		return
	}

	var def GroupDef
	if err := json.Unmarshal(env.Payload, &def); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid group-def payload: " + err.Error(),
		})
		return
	}
	if def.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "group_id must not be empty",
		})
		return
	}

	// Validate: the sender must be the group creator or an existing member.
	if def.CreatorVulaID != env.From && !def.hasMember(env.From) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "sender is not a member or creator of the group",
		})
		return
	}

	if err := a.groups.Save(def); err != nil {
		log.Printf("[peering/groups] inbound group-def save %s: %v", def.ID, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to store group definition",
		})
		return
	}

	log.Printf("[peering/groups] stored group-def %s (%q) from %s", def.ID, def.Name, env.From)
	writeJSON(w, http.StatusOK, map[string]any{"status": "received"})
}

// ─── Inbound: receive a group message ─────────────────────────────────────────

// HandleInboundGroupMessage implements POST /api/peering/inbound/group-message.
//
// InboundMiddleware has already verified the envelope signature and confirmed
// the sender is in the approved contacts list.
//
// The handler:
//  1. Reads the pre-verified *Envelope from the request context.
//  2. Decodes the GroupMessagePayload.
//  3. Verifies the sender is a member of the referenced group.
//  4. Stores the message in the local inbox.
//  5. Pushes a real-time WebSocket frame to the local user's browser tabs.
func (a *GroupAPI) HandleInboundGroupMessage(w http.ResponseWriter, r *http.Request) {
	env, ok := r.Context().Value(EnvelopeKey).(*Envelope)
	if !ok || env == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "envelope not found in context — ensure InboundMiddleware is applied",
		})
		return
	}

	var mp GroupMessagePayload
	if err := json.Unmarshal(env.Payload, &mp); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid group-message payload: " + err.Error(),
		})
		return
	}
	if mp.GroupID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "group_id must not be empty in payload",
		})
		return
	}

	// Verify the sender is a known member of the group.
	def, ok := a.groups.Get(mp.GroupID)
	if !ok {
		// We may not have the group definition yet; store the message anyway
		// and log a warning.
		log.Printf("[peering/groups] inbound group-message for unknown group %s from %s", mp.GroupID, env.From)
	} else if !def.hasMember(env.From) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "sender is not a member of the group",
			"group": mp.GroupID,
		})
		return
	}

	ts, err := time.Parse(time.RFC3339, env.Timestamp)
	if err != nil {
		ts = time.Now().UTC()
	}

	// Extract the base message ID (strip the per-recipient suffix appended during fan-out).
	msgID := groupBaseMessageID(env.ID)

	convID := groupConversationID(mp.GroupID)
	stored := StoredMessage{
		ID:        msgID,
		ConvID:    convID,
		From:      env.From,
		To:        mp.GroupID,
		Type:      mp.Type,
		Body:      mp.Body,
		Timestamp: ts,
		Direction: DirectionIn,
	}

	if err := a.inbox.StoreMessage(stored); err != nil {
		log.Printf("[peering/groups] inbox store error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to store message",
		})
		return
	}

	log.Printf("[peering/groups] stored group message %s in group %s from %s", msgID, mp.GroupID, env.From)
	a.pushGroupFrame(mp.GroupID, stored)

	writeJSON(w, http.StatusOK, map[string]any{"status": "received"})
}

// ─── Fan-out helpers ──────────────────────────────────────────────────────────

// distributeGroupDef sends the group definition to all members except the local
// node. Delivery failures are logged but do not abort the operation.
func (a *GroupAPI) distributeGroupDef(ctx context.Context, def GroupDef) {
	payload, err := json.Marshal(def)
	if err != nil {
		log.Printf("[peering/groups] distributeGroupDef marshal %s: %v", def.ID, err)
		return
	}

	for _, memberID := range def.Members {
		if memberID == a.vulaID {
			continue
		}

		contact, exists := a.store.Get(memberID)
		if !exists || contact.Server == "" {
			log.Printf("[peering/groups] member %s has no server address; cannot distribute group-def", memberID)
			continue
		}

		envID := uuid.New().String()
		env, err := NewEnvelope(envID, a.vulaID, memberID, TypeGroupDef, json.RawMessage(payload))
		if err != nil {
			log.Printf("[peering/groups] distributeGroupDef build envelope for %s: %v", memberID, err)
			continue
		}

		if err := env.Sign(a.priv); err != nil {
			log.Printf("[peering/groups] distributeGroupDef sign for %s: %v", memberID, err)
			continue
		}

		baseURL := "https://" + contact.Server

		// Use a background goroutine so one slow peer doesn't stall the others.
		go func(url, mID string, e *Envelope) {
			if err := a.client.Post(context.Background(), url, TypeGroupDef, e); err != nil {
				log.Printf("[peering/groups] distribute group-def to %s: %v", mID, err)
			}
		}(baseURL, memberID, env)
	}
}

// groupConversationID returns the inbox conversation directory name for a group.
// Format: "group:<group_id>" so it cannot collide with peer-to-peer conv IDs
// (which have the form "vula:ed25519:<b58>_vula:ed25519:<b58>").
func groupConversationID(groupID string) string {
	return "group:" + groupID
}

// groupBaseMessageID extracts the base message ID from a per-recipient fan-out
// envelope ID. During fan-out the envelope ID is set to "<uuidv4>-<sanitizedMemberID>"
// where sanitizedMemberID is the output of sanitizePath(memberVulaID).
//
// A UUIDv4 is exactly 36 characters (32 hex + 4 dashes). A fan-out ID is always
// longer than 36 characters because the sanitized member suffix is appended.
// If the envelope ID is exactly 36 characters long it is a plain UUID; return it
// unchanged. Otherwise take the first 36 characters as the base message ID.
func groupBaseMessageID(envID string) string {
	const uuidLen = 36
	if len(envID) <= uuidLen {
		return envID
	}
	// The 37th character must be '-' (the separator we appended in fan-out).
	if envID[uuidLen] != '-' {
		return envID
	}
	return envID[:uuidLen]
}

// pushGroupFrame encodes stored and broadcasts a group-message frame to
// all of the local user's connected browser tabs.
func (a *GroupAPI) pushGroupFrame(groupID string, msg StoredMessage) {
	if a.hub == nil {
		return
	}

	payload, err := json.Marshal(map[string]any{
		"event":     "group-message",
		"group_id":  groupID,
		"id":        msg.ID,
		"from":      msg.From,
		"type":      msg.Type,
		"body":      msg.Body,
		"timestamp": msg.Timestamp.Format(time.RFC3339),
		"direction": string(msg.Direction),
	})
	if err != nil {
		log.Printf("[peering/groups] pushGroupFrame marshal error: %v", err)
		return
	}

	a.hub.Broadcast(Frame{
		Channel: ChannelMessage,
		From:    msg.From,
		Payload: json.RawMessage(payload),
	})
}
