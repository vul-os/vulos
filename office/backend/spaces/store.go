// Package spaces implements the CRDT-synced message store for Vulos Spaces
// (channels, DMs, threads, messages).  It is pure-Go with no CGO.
//
// Convergence model
// -----------------
// Messages are identified by (ChannelID, ID).  Each message carries a
// SeqClock in the form "<wall-unix-ms>-<counter>-<nodeID>" which is
// lexicographically comparable and globally unique.
//
//   - Append  – insert if ID is unknown to the replica.
//   - Edit    – replace body when incoming SeqClock > stored SeqClock for
//               the same message ID.
//   - Tombstone – permanently delete; once a message is tombstoned its
//                 state can never be changed back.
//
// ApplyOp / MergeOps implement the merge function.  They are safe to call
// from multiple goroutines.
package spaces

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"vulos-office/backend/models"

	"github.com/google/uuid"
)

// -------------------------------------------------------------------------
// Hybrid Logical Clock (simple wall+counter variant)
// -------------------------------------------------------------------------

type hlc struct {
	mu      sync.Mutex
	wallMs  int64
	counter uint32
	nodeID  string
}

func newHLC(nodeID string) *hlc {
	if nodeID == "" {
		nodeID = uuid.NewString()[:8]
	}
	return &hlc{nodeID: nodeID}
}

// Tick returns the next SeqClock value, guaranteed > any previously returned
// value on this node.
func (h *hlc) Tick() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now().UnixMilli()
	if now > h.wallMs {
		h.wallMs = now
		h.counter = 0
	} else {
		h.counter++
	}
	return fmt.Sprintf("%020d-%010d-%s", h.wallMs, h.counter, h.nodeID)
}

// Receive advances the HLC past a received remote clock value.
func (h *hlc) Receive(remote string) {
	parts := strings.SplitN(remote, "-", 3)
	if len(parts) < 2 {
		return
	}
	var remoteWall int64
	var remoteCounter uint32
	fmt.Sscanf(parts[0], "%d", &remoteWall)
	fmt.Sscanf(parts[1], "%d", &remoteCounter)

	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now().UnixMilli()
	switch {
	case remoteWall > h.wallMs && remoteWall > now:
		h.wallMs = remoteWall
		h.counter = remoteCounter + 1
	case remoteWall == h.wallMs:
		if remoteCounter >= h.counter {
			h.counter = remoteCounter + 1
		}
	default:
		if now > h.wallMs {
			h.wallMs = now
			h.counter = 0
		} else {
			h.counter++
		}
	}
}

// -------------------------------------------------------------------------
// SpacesStore – in-memory CRDT replica with pluggable persistence
// -------------------------------------------------------------------------

// Persister is the interface the store calls to durably write state.
// Implementations in local.go and postgres.go satisfy this interface.
type Persister interface {
	// Channels
	SaveChannel(ch *models.Channel) error
	ListChannels() ([]*models.Channel, error)
	GetChannel(id string) (*models.Channel, error)
	DeleteChannel(id string) error

	// Memberships
	SaveMembership(m *models.Membership) error
	ListMemberships(channelID string) ([]*models.Membership, error)
	DeleteMembership(channelID, accountID string) error

	// Messages (append-only; edits/tombstones are upserts keyed by id)
	SaveMessage(msg *models.Message) error
	ListMessages(channelID string) ([]*models.Message, error)
	GetMessage(channelID, id string) (*models.Message, error)

	// Ops log (append-only – for cold-joiner replay)
	AppendOp(op *models.MessageOp) error
	ListOps(channelID string, afterClock string) ([]*models.MessageOp, error)

	// ReadState
	SaveReadState(rs *models.ReadState) error
	GetReadState(accountID, channelID string) (*models.ReadState, error)
}

// SpacesStore is a CRDT message store for one node/replica.
type SpacesStore struct {
	mu      sync.RWMutex
	clock   *hlc
	nodeID  string
	persist Persister

	// in-memory indexes (rebuilt from Persister on Open)
	channels map[string]*models.Channel             // channelID → Channel
	members  map[string]map[string]*models.Membership // channelID → accountID → Membership
	messages map[string]map[string]*models.Message  // channelID → msgID → Message
}

// Open creates a SpacesStore, loads state from the Persister, and is ready to use.
func Open(nodeID string, p Persister) (*SpacesStore, error) {
	s := &SpacesStore{
		clock:    newHLC(nodeID),
		nodeID:   nodeID,
		persist:  p,
		channels: make(map[string]*models.Channel),
		members:  make(map[string]map[string]*models.Membership),
		messages: make(map[string]map[string]*models.Message),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SpacesStore) load() error {
	chs, err := s.persist.ListChannels()
	if err != nil {
		return fmt.Errorf("spaces load channels: %w", err)
	}
	for _, ch := range chs {
		s.channels[ch.ID] = ch
		mems, err := s.persist.ListMemberships(ch.ID)
		if err != nil {
			return fmt.Errorf("spaces load memberships %s: %w", ch.ID, err)
		}
		s.members[ch.ID] = make(map[string]*models.Membership)
		for _, m := range mems {
			s.members[ch.ID][m.AccountID] = m
		}
		msgs, err := s.persist.ListMessages(ch.ID)
		if err != nil {
			return fmt.Errorf("spaces load messages %s: %w", ch.ID, err)
		}
		s.messages[ch.ID] = make(map[string]*models.Message)
		for _, msg := range msgs {
			s.messages[ch.ID][msg.ID] = msg
		}
	}
	return nil
}

// -------------------------------------------------------------------------
// Channel management
// -------------------------------------------------------------------------

func (s *SpacesStore) CreateChannel(name string, ctype models.ChannelType, createdBy string) (*models.Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ch := &models.Channel{
		ID:        uuid.NewString(),
		Name:      name,
		Type:      ctype,
		CreatedBy: createdBy,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := s.persist.SaveChannel(ch); err != nil {
		return nil, err
	}
	s.channels[ch.ID] = ch
	s.members[ch.ID] = make(map[string]*models.Membership)
	s.messages[ch.ID] = make(map[string]*models.Message)
	return ch, nil
}

// CreateChannelWithID creates a channel with a caller-supplied ID.
// Used when bootstrapping a replica that already knows the channel id from
// a peer (e.g. in tests or after initial channel-sync).
func (s *SpacesStore) CreateChannelWithID(id, name string, ctype models.ChannelType, createdBy string) (*models.Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.channels[id]; ok {
		return existing, nil // idempotent
	}
	ch := &models.Channel{
		ID:        id,
		Name:      name,
		Type:      ctype,
		CreatedBy: createdBy,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := s.persist.SaveChannel(ch); err != nil {
		return nil, err
	}
	s.channels[ch.ID] = ch
	s.members[ch.ID] = make(map[string]*models.Membership)
	s.messages[ch.ID] = make(map[string]*models.Message)
	return ch, nil
}

func (s *SpacesStore) GetChannel(id string) (*models.Channel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ch, ok := s.channels[id]
	return ch, ok
}

func (s *SpacesStore) ListChannels() []*models.Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.Channel, 0, len(s.channels))
	for _, ch := range s.channels {
		out = append(out, ch)
	}
	return out
}

// -------------------------------------------------------------------------
// Membership management
// -------------------------------------------------------------------------

func (s *SpacesStore) AddMember(channelID, accountID string) (*models.Membership, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.channels[channelID]; !ok {
		return nil, fmt.Errorf("channel not found: %s", channelID)
	}
	if s.members[channelID] == nil {
		s.members[channelID] = make(map[string]*models.Membership)
	}
	if m, exists := s.members[channelID][accountID]; exists {
		return m, nil // idempotent
	}
	m := &models.Membership{
		ID:        uuid.NewString(),
		ChannelID: channelID,
		AccountID: accountID,
		JoinedAt:  time.Now(),
	}
	if err := s.persist.SaveMembership(m); err != nil {
		return nil, err
	}
	s.members[channelID][accountID] = m
	return m, nil
}

func (s *SpacesStore) ListMembers(channelID string) []*models.Membership {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*models.Membership, 0)
	for _, m := range s.members[channelID] {
		out = append(out, m)
	}
	return out
}

// -------------------------------------------------------------------------
// Message operations (local sends)
// -------------------------------------------------------------------------

func (s *SpacesStore) SendMessage(channelID, authorID, body, threadParent string) (*models.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.channels[channelID]; !ok {
		return nil, fmt.Errorf("channel not found: %s", channelID)
	}
	now := time.Now()
	msg := &models.Message{
		ID:           uuid.NewString(),
		ChannelID:    channelID,
		ThreadParent: threadParent,
		AuthorID:     authorID,
		Body:         body,
		State:        models.MessageStateActive,
		SeqClock:     s.clock.Tick(),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	op := &models.MessageOp{
		Op:        models.MessageOpAppend,
		ChannelID: channelID,
		Msg:       *msg,
		AppliedAt: now,
	}
	if err := s.applyLocal(op); err != nil {
		return nil, err
	}
	return msg, nil
}

func (s *SpacesStore) EditMessage(channelID, msgID, newBody string) (*models.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	msgs := s.messages[channelID]
	if msgs == nil {
		return nil, fmt.Errorf("channel not found: %s", channelID)
	}
	existing, ok := msgs[msgID]
	if !ok {
		return nil, fmt.Errorf("message not found: %s", msgID)
	}
	if existing.State == models.MessageStateTombed {
		return nil, fmt.Errorf("cannot edit a deleted message")
	}
	updated := *existing
	updated.Body = newBody
	updated.State = models.MessageStateEdited
	updated.SeqClock = s.clock.Tick()
	updated.UpdatedAt = time.Now()

	op := &models.MessageOp{
		Op:        models.MessageOpEdit,
		ChannelID: channelID,
		Msg:       updated,
		AppliedAt: time.Now(),
	}
	if err := s.applyLocal(op); err != nil {
		return nil, err
	}
	return &updated, nil
}

func (s *SpacesStore) DeleteMessage(channelID, msgID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	msgs := s.messages[channelID]
	if msgs == nil {
		return fmt.Errorf("channel not found: %s", channelID)
	}
	existing, ok := msgs[msgID]
	if !ok {
		return fmt.Errorf("message not found: %s", msgID)
	}
	tombed := *existing
	tombed.Body = "" // clear body on tombstone
	tombed.State = models.MessageStateTombed
	tombed.SeqClock = s.clock.Tick()
	tombed.UpdatedAt = time.Now()

	op := &models.MessageOp{
		Op:        models.MessageOpTombstone,
		ChannelID: channelID,
		Msg:       tombed,
		AppliedAt: time.Now(),
	}
	return s.applyLocal(op)
}

// ListMessages returns messages in a channel sorted by SeqClock ascending.
// Thread replies are included; callers may filter by ThreadParent.
func (s *SpacesStore) ListMessages(channelID string) []*models.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	msgs := s.messages[channelID]
	out := make([]*models.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].SeqClock < out[j].SeqClock
	})
	return out
}

// -------------------------------------------------------------------------
// CRDT merge – apply ops from a remote replica
// -------------------------------------------------------------------------

// MergeOps applies a batch of ops received from a peer.  It is idempotent
// and commutative: applying the same ops in any order converges to the same
// state.
func (s *SpacesStore) MergeOps(ops []*models.MessageOp) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, op := range ops {
		if err := s.applyRemote(op); err != nil {
			return err
		}
	}
	return nil
}

// ExportOps returns all ops for channelID with SeqClock > afterClock for
// cold-joiner or catch-up sync.
func (s *SpacesStore) ExportOps(channelID, afterClock string) ([]*models.MessageOp, error) {
	return s.persist.ListOps(channelID, afterClock)
}

// -------------------------------------------------------------------------
// internal helpers (caller must hold s.mu)
// -------------------------------------------------------------------------

func (s *SpacesStore) applyLocal(op *models.MessageOp) error {
	if err := s.applyToIndex(op); err != nil {
		return err
	}
	if err := s.persist.SaveMessage(&op.Msg); err != nil {
		return err
	}
	return s.persist.AppendOp(op)
}

func (s *SpacesStore) applyRemote(op *models.MessageOp) error {
	s.clock.Receive(op.Msg.SeqClock)
	return s.applyLocal(op)
}

// applyToIndex applies the CRDT merge rule to the in-memory index.
func (s *SpacesStore) applyToIndex(op *models.MessageOp) error {
	chID := op.ChannelID
	if _, ok := s.channels[chID]; !ok {
		// Auto-create a channel skeleton for the remote op so the store
		// stays consistent; full channel metadata comes via channel sync.
		s.channels[chID] = &models.Channel{ID: chID, Name: chID}
		s.members[chID] = make(map[string]*models.Membership)
		s.messages[chID] = make(map[string]*models.Message)
	}
	if s.messages[chID] == nil {
		s.messages[chID] = make(map[string]*models.Message)
	}

	msg := op.Msg
	existing, exists := s.messages[chID][msg.ID]

	switch op.Op {
	case models.MessageOpAppend:
		if !exists {
			s.messages[chID][msg.ID] = &msg
		}
		// If already present, do nothing (append is idempotent).

	case models.MessageOpEdit:
		if !exists {
			// Remote edit for unknown message — store it as active.
			s.messages[chID][msg.ID] = &msg
			return nil
		}
		// Tombstone is terminal; do not un-delete.
		if existing.State == models.MessageStateTombed {
			return nil
		}
		// LWW: highest SeqClock wins.
		if msg.SeqClock > existing.SeqClock {
			s.messages[chID][msg.ID] = &msg
		}

	case models.MessageOpTombstone:
		// Tombstone always wins, regardless of SeqClock.
		if !exists {
			s.messages[chID][msg.ID] = &msg
			return nil
		}
		// Apply tombstone body-clearing.
		tombed := *existing
		tombed.State = models.MessageStateTombed
		tombed.Body = ""
		if msg.SeqClock > tombed.SeqClock {
			tombed.SeqClock = msg.SeqClock
		}
		tombed.UpdatedAt = msg.UpdatedAt
		s.messages[chID][msg.ID] = &tombed

	default:
		return fmt.Errorf("unknown op type: %s", op.Op)
	}
	return nil
}

// -------------------------------------------------------------------------
// ReadState helpers
// -------------------------------------------------------------------------

func (s *SpacesStore) MarkRead(accountID, channelID, clock string) error {
	rs := &models.ReadState{
		AccountID:    accountID,
		ChannelID:    channelID,
		LastReadClock: clock,
		UpdatedAt:    time.Now(),
	}
	return s.persist.SaveReadState(rs)
}

func (s *SpacesStore) GetReadState(accountID, channelID string) (*models.ReadState, error) {
	return s.persist.GetReadState(accountID, channelID)
}

// -------------------------------------------------------------------------
// NullPersister – in-memory-only backend (for tests / single-session mode)
// -------------------------------------------------------------------------

// NullPersister is a Persister that stores everything in memory.
// It satisfies the interface without any disk or DB dependency.
type NullPersister struct {
	mu         sync.Mutex
	channels   map[string]*models.Channel
	memberships map[string][]*models.Membership
	messages   map[string]map[string]*models.Message // channelID → msgID → msg
	ops        map[string][]*models.MessageOp        // channelID → ops
	readStates map[string]*models.ReadState          // "accountID:channelID" → rs
}

func NewNullPersister() *NullPersister {
	return &NullPersister{
		channels:    make(map[string]*models.Channel),
		memberships: make(map[string][]*models.Membership),
		messages:    make(map[string]map[string]*models.Message),
		ops:         make(map[string][]*models.MessageOp),
		readStates:  make(map[string]*models.ReadState),
	}
}

func (p *NullPersister) SaveChannel(ch *models.Channel) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.channels[ch.ID] = ch
	return nil
}

func (p *NullPersister) ListChannels() ([]*models.Channel, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*models.Channel, 0, len(p.channels))
	for _, ch := range p.channels {
		out = append(out, ch)
	}
	return out, nil
}

func (p *NullPersister) GetChannel(id string) (*models.Channel, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ch, ok := p.channels[id]; ok {
		return ch, nil
	}
	return nil, fmt.Errorf("channel not found: %s", id)
}

func (p *NullPersister) DeleteChannel(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.channels, id)
	return nil
}

func (p *NullPersister) SaveMembership(m *models.Membership) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.memberships[m.ChannelID] = append(p.memberships[m.ChannelID], m)
	return nil
}

func (p *NullPersister) ListMemberships(channelID string) ([]*models.Membership, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.memberships[channelID], nil
}

func (p *NullPersister) DeleteMembership(channelID, accountID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	list := p.memberships[channelID]
	out := list[:0]
	for _, m := range list {
		if m.AccountID != accountID {
			out = append(out, m)
		}
	}
	p.memberships[channelID] = out
	return nil
}

func (p *NullPersister) SaveMessage(msg *models.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.messages[msg.ChannelID] == nil {
		p.messages[msg.ChannelID] = make(map[string]*models.Message)
	}
	p.messages[msg.ChannelID][msg.ID] = msg
	return nil
}

func (p *NullPersister) ListMessages(channelID string) ([]*models.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*models.Message, 0)
	for _, m := range p.messages[channelID] {
		out = append(out, m)
	}
	return out, nil
}

func (p *NullPersister) GetMessage(channelID, id string) (*models.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if m, ok := p.messages[channelID][id]; ok {
		return m, nil
	}
	return nil, fmt.Errorf("message not found: %s", id)
}

func (p *NullPersister) AppendOp(op *models.MessageOp) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ops[op.ChannelID] = append(p.ops[op.ChannelID], op)
	return nil
}

func (p *NullPersister) ListOps(channelID string, afterClock string) ([]*models.MessageOp, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*models.MessageOp
	for _, op := range p.ops[channelID] {
		if op.Msg.SeqClock > afterClock {
			out = append(out, op)
		}
	}
	return out, nil
}

func (p *NullPersister) SaveReadState(rs *models.ReadState) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := rs.AccountID + ":" + rs.ChannelID
	// LWW: only update if incoming clock is newer
	if existing, ok := p.readStates[key]; ok {
		if rs.LastReadClock <= existing.LastReadClock {
			return nil
		}
	}
	p.readStates[key] = rs
	return nil
}

func (p *NullPersister) GetReadState(accountID, channelID string) (*models.ReadState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := accountID + ":" + channelID
	if rs, ok := p.readStates[key]; ok {
		return rs, nil
	}
	return &models.ReadState{AccountID: accountID, ChannelID: channelID}, nil
}
