package spaces_test

import (
	"testing"

	"vulos-office/backend/spaces"
	"vulos-office/backend/models"
)

// openStore returns a SpacesStore backed by an in-memory NullPersister.
func openStore(t *testing.T, nodeID string) *spaces.SpacesStore {
	t.Helper()
	p := spaces.NewNullPersister()
	s, err := spaces.Open(nodeID, p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

// -------------------------------------------------------------------------
// Basic channel + message lifecycle
// -------------------------------------------------------------------------

func TestCreateChannelAndSendMessage(t *testing.T) {
	s := openStore(t, "node-A")

	ch, err := s.CreateChannel("general", models.ChannelTypePublic, "alice")
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if ch.ID == "" {
		t.Fatal("expected non-empty channel ID")
	}

	msg, err := s.SendMessage(ch.ID, "alice", "Hello, world!", "")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if msg.Body != "Hello, world!" {
		t.Errorf("body mismatch: %q", msg.Body)
	}
	if msg.State != models.MessageStateActive {
		t.Errorf("expected active, got %s", msg.State)
	}
	if msg.SeqClock == "" {
		t.Error("expected non-empty SeqClock")
	}
}

func TestThreadReply(t *testing.T) {
	s := openStore(t, "node-A")
	ch, _ := s.CreateChannel("general", models.ChannelTypePublic, "alice")
	root, _ := s.SendMessage(ch.ID, "alice", "Root message", "")
	reply, err := s.SendMessage(ch.ID, "bob", "Thread reply", root.ID)
	if err != nil {
		t.Fatalf("thread reply: %v", err)
	}
	if reply.ThreadParent != root.ID {
		t.Errorf("thread_parent mismatch: got %s, want %s", reply.ThreadParent, root.ID)
	}
}

// -------------------------------------------------------------------------
// Edit + Tombstone (CRDT convergence)
// -------------------------------------------------------------------------

func TestEditMessage(t *testing.T) {
	s := openStore(t, "node-A")
	ch, _ := s.CreateChannel("general", models.ChannelTypePublic, "alice")
	msg, _ := s.SendMessage(ch.ID, "alice", "Original", "")

	edited, err := s.EditMessage(ch.ID, msg.ID, "Updated body")
	if err != nil {
		t.Fatalf("EditMessage: %v", err)
	}
	if edited.Body != "Updated body" {
		t.Errorf("expected 'Updated body', got %q", edited.Body)
	}
	if edited.State != models.MessageStateEdited {
		t.Errorf("expected edited state, got %s", edited.State)
	}
	if edited.SeqClock <= msg.SeqClock {
		t.Errorf("edited SeqClock should be > original: %s vs %s", edited.SeqClock, msg.SeqClock)
	}
}

func TestDeleteMessageTombstone(t *testing.T) {
	s := openStore(t, "node-A")
	ch, _ := s.CreateChannel("general", models.ChannelTypePublic, "alice")
	msg, _ := s.SendMessage(ch.ID, "alice", "To be deleted", "")

	if err := s.DeleteMessage(ch.ID, msg.ID); err != nil {
		t.Fatalf("DeleteMessage: %v", err)
	}

	msgs := s.ListMessages(ch.ID)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 tombstoned message, got %d", len(msgs))
	}
	if msgs[0].State != models.MessageStateTombed {
		t.Errorf("expected deleted state, got %s", msgs[0].State)
	}
	if msgs[0].Body != "" {
		t.Errorf("tombstoned body should be empty, got %q", msgs[0].Body)
	}
}

func TestCannotEditTombstone(t *testing.T) {
	s := openStore(t, "node-A")
	ch, _ := s.CreateChannel("general", models.ChannelTypePublic, "alice")
	msg, _ := s.SendMessage(ch.ID, "alice", "To be deleted", "")
	s.DeleteMessage(ch.ID, msg.ID) //nolint

	_, err := s.EditMessage(ch.ID, msg.ID, "Ghost edit")
	if err == nil {
		t.Fatal("expected error editing a tombstoned message, got nil")
	}
}

// -------------------------------------------------------------------------
// CRDT convergence across two replicas
// -------------------------------------------------------------------------

func TestAppendConvergesTwoReplicas(t *testing.T) {
	pA := spaces.NewNullPersister()
	pB := spaces.NewNullPersister()
	nodeA, _ := spaces.Open("node-A", pA)
	nodeB, _ := spaces.Open("node-B", pB)

	// Both replicas start with the same channel (as they would after initial sync).
	ch, _ := nodeA.CreateChannel("sync-test", models.ChannelTypePublic, "alice")
	// Bootstrap B with the same channel id so both peers can send independently.
	nodeB.CreateChannelWithID(ch.ID, "sync-test", models.ChannelTypePublic, "alice") //nolint

	// Alice sends on A, Bob sends on B (peers offline — no sync yet).
	msgA, _ := nodeA.SendMessage(ch.ID, "alice", "from alice", "")
	msgB, _ := nodeB.SendMessage(ch.ID, "bob", "from bob", "")

	// Export ops from each replica and apply to the other (gossip sync).
	opsFromA, _ := nodeA.ExportOps(ch.ID, "")
	opsFromB, _ := nodeB.ExportOps(ch.ID, "")

	if err := nodeB.MergeOps(opsFromA); err != nil {
		t.Fatalf("nodeB.MergeOps(A): %v", err)
	}
	if err := nodeA.MergeOps(opsFromB); err != nil {
		t.Fatalf("nodeA.MergeOps(B): %v", err)
	}

	msgsA := nodeA.ListMessages(ch.ID)
	msgsB := nodeB.ListMessages(ch.ID)

	if len(msgsA) != 2 {
		t.Errorf("nodeA: expected 2 messages, got %d", len(msgsA))
	}
	if len(msgsB) != 2 {
		t.Errorf("nodeB: expected 2 messages, got %d", len(msgsB))
	}

	// Both replicas must contain both message ids.
	ids := func(msgs []*models.Message) map[string]bool {
		m := make(map[string]bool)
		for _, msg := range msgs {
			m[msg.ID] = true
		}
		return m
	}
	idsA := ids(msgsA)
	idsB := ids(msgsB)
	for _, id := range []string{msgA.ID, msgB.ID} {
		if !idsA[id] {
			t.Errorf("nodeA missing message %s", id)
		}
		if !idsB[id] {
			t.Errorf("nodeB missing message %s", id)
		}
	}
}

func TestTombstoneConverges(t *testing.T) {
	pA := spaces.NewNullPersister()
	pB := spaces.NewNullPersister()
	nodeA, _ := spaces.Open("node-A", pA)
	nodeB, _ := spaces.Open("node-B", pB)

	ch, _ := nodeA.CreateChannel("tomb-test", models.ChannelTypePublic, "alice")

	// Alice sends a message on A.
	msg, _ := nodeA.SendMessage(ch.ID, "alice", "delete me", "")

	// Sync append to B.
	opsA, _ := nodeA.ExportOps(ch.ID, "")
	nodeB.MergeOps(opsA) //nolint

	// B receives the message; now A deletes it while B hasn't yet.
	nodeA.DeleteMessage(ch.ID, msg.ID) //nolint

	// Sync tombstone to B.
	opsA2, _ := nodeA.ExportOps(ch.ID, "")
	nodeB.MergeOps(opsA2) //nolint

	for _, msgs := range [][]*models.Message{nodeA.ListMessages(ch.ID), nodeB.ListMessages(ch.ID)} {
		if len(msgs) != 1 {
			t.Fatalf("expected 1 message, got %d", len(msgs))
		}
		if msgs[0].State != models.MessageStateTombed {
			t.Errorf("expected tombstoned, got %s", msgs[0].State)
		}
	}
}

func TestEditConvergesLWW(t *testing.T) {
	pA := spaces.NewNullPersister()
	pB := spaces.NewNullPersister()
	nodeA, _ := spaces.Open("node-A", pA)
	nodeB, _ := spaces.Open("node-B", pB)

	ch, _ := nodeA.CreateChannel("edit-test", models.ChannelTypePublic, "alice")
	msg, _ := nodeA.SendMessage(ch.ID, "alice", "v1", "")

	// Bootstrap B with the append op.
	opsA, _ := nodeA.ExportOps(ch.ID, "")
	nodeB.MergeOps(opsA) //nolint

	// Both nodes edit independently (concurrent edits).
	editedA, _ := nodeA.EditMessage(ch.ID, msg.ID, "v2-from-A")
	editedB, _ := nodeB.EditMessage(ch.ID, msg.ID, "v2-from-B")

	// Cross-sync.
	opsA2, _ := nodeA.ExportOps(ch.ID, editedA.SeqClock)
	// include the edit op itself
	allA, _ := nodeA.ExportOps(ch.ID, "")
	allB, _ := nodeB.ExportOps(ch.ID, "")

	nodeA.MergeOps(allB) //nolint
	nodeB.MergeOps(allA) //nolint
	_ = opsA2

	msgsA := nodeA.ListMessages(ch.ID)
	msgsB := nodeB.ListMessages(ch.ID)
	if len(msgsA) == 0 || len(msgsB) == 0 {
		t.Fatal("expected messages after merge")
	}

	// Both replicas must agree on the final body (LWW → higher SeqClock wins).
	bodyA := msgsA[0].Body
	bodyB := msgsB[0].Body
	if bodyA != bodyB {
		t.Errorf("replicas diverged: A=%q B=%q", bodyA, bodyB)
	}
	// Must be one of the two edits.
	if bodyA != "v2-from-A" && bodyA != "v2-from-B" {
		t.Errorf("unexpected body after LWW merge: %q", bodyA)
	}
	_ = editedB
}

// -------------------------------------------------------------------------
// Idempotency
// -------------------------------------------------------------------------

func TestMergeOpsIsIdempotent(t *testing.T) {
	s := openStore(t, "node-A")
	ch, _ := s.CreateChannel("idem", models.ChannelTypePublic, "alice")
	s.SendMessage(ch.ID, "alice", "hi", "") //nolint

	ops, _ := s.ExportOps(ch.ID, "")
	// Apply the same ops twice.
	s.MergeOps(ops) //nolint
	s.MergeOps(ops) //nolint

	msgs := s.ListMessages(ch.ID)
	if len(msgs) != 1 {
		t.Errorf("idempotency: expected 1 message, got %d", len(msgs))
	}
}

// -------------------------------------------------------------------------
// Membership
// -------------------------------------------------------------------------

func TestMembership(t *testing.T) {
	s := openStore(t, "node-A")
	ch, _ := s.CreateChannel("private-room", models.ChannelTypePrivate, "alice")

	m, err := s.AddMember(ch.ID, "bob")
	if err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if m.AccountID != "bob" {
		t.Errorf("expected bob, got %s", m.AccountID)
	}

	// Idempotent add.
	m2, err := s.AddMember(ch.ID, "bob")
	if err != nil {
		t.Fatalf("AddMember idempotent: %v", err)
	}
	if m2.ID != m.ID {
		t.Errorf("expected same membership id on idempotent add")
	}

	members := s.ListMembers(ch.ID)
	if len(members) != 1 {
		t.Errorf("expected 1 member, got %d", len(members))
	}
}
