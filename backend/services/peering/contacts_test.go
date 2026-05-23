// contacts_test.go — unit tests for the ContactStore.
//
// Coverage:
//   - Happy-path: Add → Approve → Can → Block → Unblock → Remove
//   - State graph: invalid transitions return errors
//   - Permissions: Can() reflects granted/revoked perms; UpdatePermissions
//   - Persistence: reload from disk produces identical in-memory state
//   - Concurrent access: parallel readers/writers do not race (run with -race)
package peering

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// newTempStore creates a ContactStore backed by a temporary directory that is
// automatically removed at test teardown.
func newTempStore(t *testing.T) *ContactStore {
	t.Helper()
	dir := t.TempDir()
	cs, err := NewContactStore(dir)
	if err != nil {
		t.Fatalf("NewContactStore: %v", err)
	}
	return cs
}

// reloadStore reopens the store from the same path as src.
func reloadStore(t *testing.T, src *ContactStore) *ContactStore {
	t.Helper()
	// The filePath is ~/.vulos/peering/contacts.json under the temp dir.
	// Home is two levels up: <tmp>/.vulos/peering → <tmp>
	home := filepath.Dir(filepath.Dir(filepath.Dir(src.filePath)))
	cs2, err := NewContactStore(home)
	if err != nil {
		t.Fatalf("reload ContactStore: %v", err)
	}
	return cs2
}

const (
	aliceID = "vula:ed25519:AliceAliceAliceAlice"
	bobID   = "vula:ed25519:BobBobBobBobBobBobBo"
	carolID = "vula:ed25519:CarolCarolCarolCarol"
)

// ─── NewContactStore ──────────────────────────────────────────────────────────

func TestNewContactStore_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	cs, err := NewContactStore(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fp := cs.filePath
	data, err := os.ReadFile(fp)
	if err != nil {
		t.Fatalf("contacts.json not created: %v", err)
	}

	var ds diskStore
	if err := json.Unmarshal(data, &ds); err != nil {
		t.Fatalf("contacts.json invalid JSON: %v", err)
	}

	if ds.Contacts == nil {
		t.Error("expected contacts list, got nil")
	}
}

func TestNewContactStore_IdempotentInit(t *testing.T) {
	dir := t.TempDir()

	cs1, err := NewContactStore(dir)
	if err != nil {
		t.Fatalf("first init: %v", err)
	}
	if err := cs1.Add(aliceID, "Alice", "alice.vulos.org:8080"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Second init should load existing data.
	cs2, err := NewContactStore(dir)
	if err != nil {
		t.Fatalf("second init: %v", err)
	}

	c, ok := cs2.Get(aliceID)
	if !ok {
		t.Fatal("contact not found after reload")
	}
	if c.DisplayName != "Alice" {
		t.Errorf("display name: got %q want %q", c.DisplayName, "Alice")
	}
}

// ─── Add ─────────────────────────────────────────────────────────────────────

func TestAdd_Happy(t *testing.T) {
	cs := newTempStore(t)

	if err := cs.Add(aliceID, "Alice", "alice.vulos.org:8080"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	c, ok := cs.Get(aliceID)
	if !ok {
		t.Fatal("Get: contact not found after Add")
	}
	if c.State != StatePending {
		t.Errorf("state: got %q want %q", c.State, StatePending)
	}
	if c.VulaID != aliceID {
		t.Errorf("VulaID mismatch")
	}
	if c.DisplayName != "Alice" {
		t.Errorf("DisplayName: got %q want %q", c.DisplayName, "Alice")
	}
	if c.AddedAt.IsZero() {
		t.Error("AddedAt not set")
	}
}

func TestAdd_EmptyID(t *testing.T) {
	cs := newTempStore(t)
	if err := cs.Add("", "Alice", ""); err == nil {
		t.Fatal("expected error for empty ID")
	}
}

func TestAdd_Duplicate(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck
	if err := cs.Add(aliceID, "Alice2", ""); err == nil {
		t.Fatal("expected error for duplicate Add")
	}
}

// ─── Approve ─────────────────────────────────────────────────────────────────

func TestApprove_PendingToApproved(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck

	if err := cs.Approve(aliceID, DefaultPerms()); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	c, _ := cs.Get(aliceID)
	if c.State != StateApproved {
		t.Errorf("state: got %q want %q", c.State, StateApproved)
	}
	if c.ApprovedAt.IsZero() {
		t.Error("ApprovedAt not set")
	}
	if len(c.Permissions) != 4 {
		t.Errorf("permissions count: got %d want 4", len(c.Permissions))
	}
}

func TestApprove_UpdatePermissions(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "")             //nolint:errcheck
	cs.Approve(aliceID, DefaultPerms())      //nolint:errcheck
	cs.Approve(aliceID, []Perm{PermMessage}) // reduce perms

	c, _ := cs.Get(aliceID)
	if len(c.Permissions) != 1 || c.Permissions[0] != PermMessage {
		t.Errorf("permissions: got %v want [message]", c.Permissions)
	}
}

func TestApprove_BlockedContact(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck
	cs.Block(aliceID)            //nolint:errcheck

	if err := cs.Approve(aliceID, DefaultPerms()); err == nil {
		t.Fatal("expected error approving a blocked contact")
	}
}

func TestApprove_UnknownContact(t *testing.T) {
	cs := newTempStore(t)
	if err := cs.Approve(aliceID, DefaultPerms()); err == nil {
		t.Fatal("expected error approving non-existent contact")
	}
}

func TestApprove_EmptyPerms(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck
	if err := cs.Approve(aliceID, []Perm{}); err == nil {
		t.Fatal("expected error for empty perms")
	}
}

func TestApprove_InvalidPerm(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck
	if err := cs.Approve(aliceID, []Perm{"fly"}); err == nil {
		t.Fatal("expected error for unknown permission")
	}
}

func TestApprove_DeduplicatesPerms(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "")                                    //nolint:errcheck
	cs.Approve(aliceID, []Perm{PermMessage, PermMessage, PermCall}) //nolint:errcheck

	c, _ := cs.Get(aliceID)
	if len(c.Permissions) != 2 {
		t.Errorf("expected 2 deduped perms, got %d: %v", len(c.Permissions), c.Permissions)
	}
}

// ─── Block ────────────────────────────────────────────────────────────────────

func TestBlock_PendingToBlocked(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck

	if err := cs.Block(aliceID); err != nil {
		t.Fatalf("Block: %v", err)
	}

	c, _ := cs.Get(aliceID)
	if c.State != StateBlocked {
		t.Errorf("state: got %q want %q", c.State, StateBlocked)
	}
	if c.BlockedAt.IsZero() {
		t.Error("BlockedAt not set")
	}
	if len(c.Permissions) != 0 {
		t.Errorf("permissions should be cleared on block, got %v", c.Permissions)
	}
}

func TestBlock_ApprovedToBlocked(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "")        //nolint:errcheck
	cs.Approve(aliceID, DefaultPerms()) //nolint:errcheck

	if err := cs.Block(aliceID); err != nil {
		t.Fatalf("Block approved contact: %v", err)
	}

	c, _ := cs.Get(aliceID)
	if c.State != StateBlocked {
		t.Errorf("state: got %q want %q", c.State, StateBlocked)
	}
}

func TestBlock_Idempotent(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck
	cs.Block(aliceID)            //nolint:errcheck

	// Second block should be a no-op, not an error.
	if err := cs.Block(aliceID); err != nil {
		t.Fatalf("second Block should be idempotent: %v", err)
	}
}

func TestBlock_NotFound(t *testing.T) {
	cs := newTempStore(t)
	if err := cs.Block(aliceID); err == nil {
		t.Fatal("expected error blocking non-existent contact")
	}
}

// ─── Unblock ─────────────────────────────────────────────────────────────────

func TestUnblock_BlockedToPending(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "")        //nolint:errcheck
	cs.Approve(aliceID, DefaultPerms()) //nolint:errcheck
	cs.Block(aliceID)                   //nolint:errcheck

	if err := cs.Unblock(aliceID); err != nil {
		t.Fatalf("Unblock: %v", err)
	}

	c, _ := cs.Get(aliceID)
	if c.State != StatePending {
		t.Errorf("state after Unblock: got %q want %q", c.State, StatePending)
	}
	if len(c.Permissions) != 0 {
		t.Errorf("permissions should remain cleared after Unblock, got %v", c.Permissions)
	}
}

func TestUnblock_NotBlocked(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck

	if err := cs.Unblock(aliceID); err == nil {
		t.Fatal("expected error unblocking a pending contact")
	}
}

func TestUnblock_NotFound(t *testing.T) {
	cs := newTempStore(t)
	if err := cs.Unblock(aliceID); err == nil {
		t.Fatal("expected error unblocking non-existent contact")
	}
}

// ─── Remove ──────────────────────────────────────────────────────────────────

func TestRemove_Pending(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck

	if err := cs.Remove(aliceID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, ok := cs.Get(aliceID); ok {
		t.Fatal("contact still present after Remove")
	}
}

func TestRemove_Approved(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "")        //nolint:errcheck
	cs.Approve(aliceID, DefaultPerms()) //nolint:errcheck
	cs.Remove(aliceID)                  //nolint:errcheck

	if _, ok := cs.Get(aliceID); ok {
		t.Fatal("approved contact still present after Remove")
	}
}

func TestRemove_Blocked(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck
	cs.Block(aliceID)            //nolint:errcheck
	cs.Remove(aliceID)           //nolint:errcheck

	if _, ok := cs.Get(aliceID); ok {
		t.Fatal("blocked contact still present after Remove")
	}
}

func TestRemove_NotFound(t *testing.T) {
	cs := newTempStore(t)
	if err := cs.Remove(aliceID); err == nil {
		t.Fatal("expected error removing non-existent contact")
	}
}

// ─── UpdateServer ─────────────────────────────────────────────────────────────

func TestUpdateServer(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck

	if err := cs.UpdateServer(aliceID, "new-host.vulos.org:9090"); err != nil {
		t.Fatalf("UpdateServer: %v", err)
	}

	c, _ := cs.Get(aliceID)
	if c.Server != "new-host.vulos.org:9090" {
		t.Errorf("server: got %q want %q", c.Server, "new-host.vulos.org:9090")
	}
}

func TestUpdateServer_NotFound(t *testing.T) {
	cs := newTempStore(t)
	if err := cs.UpdateServer(aliceID, "x"); err == nil {
		t.Fatal("expected error updating server for non-existent contact")
	}
}

// ─── UpdateVumailAddress ──────────────────────────────────────────────────────

func TestUpdateVumailAddress_Happy(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck

	if err := cs.UpdateVumailAddress(aliceID, "alice@vulos.org"); err != nil {
		t.Fatalf("UpdateVumailAddress: %v", err)
	}

	c, _ := cs.Get(aliceID)
	if c.VumailAddress != "alice@vulos.org" {
		t.Errorf("vumail_address: got %q want %q", c.VumailAddress, "alice@vulos.org")
	}
}

func TestUpdateVumailAddress_Clear(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "")                    //nolint:errcheck
	cs.UpdateVumailAddress(aliceID, "alice@vulos.org") //nolint:errcheck

	// Clearing the address with an empty string should be allowed.
	if err := cs.UpdateVumailAddress(aliceID, ""); err != nil {
		t.Fatalf("UpdateVumailAddress clear: %v", err)
	}

	c, _ := cs.Get(aliceID)
	if c.VumailAddress != "" {
		t.Errorf("vumail_address should be empty after clear, got %q", c.VumailAddress)
	}
}

func TestUpdateVumailAddress_NotFound(t *testing.T) {
	cs := newTempStore(t)
	if err := cs.UpdateVumailAddress(aliceID, "alice@vulos.org"); err == nil {
		t.Fatal("expected error updating Vulos identity address for non-existent contact")
	}
}

func TestUpdateVumailAddress_EmptyID(t *testing.T) {
	cs := newTempStore(t)
	if err := cs.UpdateVumailAddress("", "alice@vulos.org"); err == nil {
		t.Fatal("expected error for empty vulaID")
	}
}

func TestUpdateVumailAddress_Persistence(t *testing.T) {
	dir := t.TempDir()
	cs, _ := NewContactStore(dir)
	cs.Add(aliceID, "Alice", "")                        //nolint:errcheck
	cs.UpdateVumailAddress(aliceID, "alice@vulos.org") //nolint:errcheck

	cs2, err := NewContactStore(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	c, ok := cs2.Get(aliceID)
	if !ok {
		t.Fatal("Alice not found after reload")
	}
	if c.VumailAddress != "alice@vulos.org" {
		t.Errorf("vumail_address after reload: got %q want %q", c.VumailAddress, "alice@vulos.org")
	}
}

// ─── LookupByVumailAddress ────────────────────────────────────────────────────

func TestLookupByVumailAddress_Found(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "")                        //nolint:errcheck
	cs.UpdateVumailAddress(aliceID, "alice@vulos.org") //nolint:errcheck

	c, ok := cs.LookupByVumailAddress("alice@vulos.org")
	if !ok {
		t.Fatal("LookupByVumailAddress: expected to find alice")
	}
	if c.VulaID != aliceID {
		t.Errorf("VulaID: got %q want %q", c.VulaID, aliceID)
	}
}

func TestLookupByVumailAddress_CaseInsensitive(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "")                        //nolint:errcheck
	cs.UpdateVumailAddress(aliceID, "alice@vulos.org") //nolint:errcheck

	c, ok := cs.LookupByVumailAddress("Alice@VULOS.ORG")
	if !ok {
		t.Fatal("LookupByVumailAddress: case-insensitive lookup failed")
	}
	if c.VulaID != aliceID {
		t.Errorf("VulaID: got %q want %q", c.VulaID, aliceID)
	}
}

func TestLookupByVumailAddress_NotFound(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck
	// No Vulos identity address set.

	_, ok := cs.LookupByVumailAddress("alice@vulos.org")
	if ok {
		t.Error("expected not found for contact with no Vulos identity address")
	}
}

func TestLookupByVumailAddress_EmptyAddress(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "")                        //nolint:errcheck
	cs.UpdateVumailAddress(aliceID, "alice@vulos.org") //nolint:errcheck

	_, ok := cs.LookupByVumailAddress("")
	if ok {
		t.Error("empty address should never match")
	}
}

func TestLookupByVumailAddress_ReturnsSnapshot(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "")                        //nolint:errcheck
	cs.Approve(aliceID, DefaultPerms())                 //nolint:errcheck
	cs.UpdateVumailAddress(aliceID, "alice@vulos.org") //nolint:errcheck

	c, ok := cs.LookupByVumailAddress("alice@vulos.org")
	if !ok {
		t.Fatal("not found")
	}
	// Mutate the snapshot; the store must be unaffected.
	c.VumailAddress = "hacked@evil.org"
	c.Permissions = nil

	c2, _ := cs.Get(aliceID)
	if c2.VumailAddress != "alice@vulos.org" {
		t.Errorf("store mutated via snapshot: vumail_address=%q", c2.VumailAddress)
	}
	if len(c2.Permissions) == 0 {
		t.Error("store mutated via snapshot: permissions cleared")
	}
}

// ─── UpdatePermissions ────────────────────────────────────────────────────────

func TestUpdatePermissions_Approved(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "")        //nolint:errcheck
	cs.Approve(aliceID, DefaultPerms()) //nolint:errcheck

	if err := cs.UpdatePermissions(aliceID, []Perm{PermCall, PermVideo}); err != nil {
		t.Fatalf("UpdatePermissions: %v", err)
	}

	if cs.Can(aliceID, PermMessage) {
		t.Error("PermMessage should have been revoked")
	}
	if cs.Can(aliceID, PermMedia) {
		t.Error("PermMedia should have been revoked")
	}
	if !cs.Can(aliceID, PermCall) {
		t.Error("PermCall should still be granted")
	}
	if !cs.Can(aliceID, PermVideo) {
		t.Error("PermVideo should still be granted")
	}
}

func TestUpdatePermissions_NotApproved(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck

	if err := cs.UpdatePermissions(aliceID, []Perm{PermMessage}); err == nil {
		t.Fatal("expected error updating perms on pending contact")
	}
}

// ─── List ─────────────────────────────────────────────────────────────────────

func TestList_Empty(t *testing.T) {
	cs := newTempStore(t)
	if l := cs.List(); len(l) != 0 {
		t.Errorf("expected empty list, got %d", len(l))
	}
}

func TestList_MultipleContacts(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck
	cs.Add(bobID, "Bob", "")     //nolint:errcheck

	contacts := cs.List()
	if len(contacts) != 2 {
		t.Errorf("expected 2 contacts, got %d", len(contacts))
	}
}

func TestListByState(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "")        //nolint:errcheck
	cs.Add(bobID, "Bob", "")            //nolint:errcheck
	cs.Add(carolID, "Carol", "")        //nolint:errcheck
	cs.Approve(aliceID, DefaultPerms()) //nolint:errcheck
	cs.Block(bobID)                     //nolint:errcheck
	// Carol stays pending.

	if got := cs.ListByState(StatePending); len(got) != 1 || got[0].VulaID != carolID {
		t.Errorf("pending list: got %v", got)
	}
	if got := cs.ListByState(StateApproved); len(got) != 1 || got[0].VulaID != aliceID {
		t.Errorf("approved list: got %v", got)
	}
	if got := cs.ListByState(StateBlocked); len(got) != 1 || got[0].VulaID != bobID {
		t.Errorf("blocked list: got %v", got)
	}
}

// ─── Predicates ──────────────────────────────────────────────────────────────

func TestIsApproved(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "")        //nolint:errcheck
	cs.Add(bobID, "Bob", "")            //nolint:errcheck
	cs.Approve(aliceID, DefaultPerms()) //nolint:errcheck
	cs.Block(bobID)                     //nolint:errcheck

	if !cs.IsApproved(aliceID) {
		t.Error("Alice should be approved")
	}
	if cs.IsApproved(bobID) {
		t.Error("Bob should not be approved (blocked)")
	}
	if cs.IsApproved(carolID) {
		t.Error("Carol should not be approved (unknown)")
	}
}

func TestCan_ReflectsGrants(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "")                        //nolint:errcheck
	cs.Approve(aliceID, []Perm{PermMessage, PermMedia}) //nolint:errcheck

	if !cs.Can(aliceID, PermMessage) {
		t.Error("expected PermMessage granted")
	}
	if !cs.Can(aliceID, PermMedia) {
		t.Error("expected PermMedia granted")
	}
	if cs.Can(aliceID, PermCall) {
		t.Error("PermCall should not be granted")
	}
	if cs.Can(aliceID, PermVideo) {
		t.Error("PermVideo should not be granted")
	}
}

func TestCan_NotApproved(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck

	for _, p := range allPerms {
		if cs.Can(aliceID, p) {
			t.Errorf("pending contact should not have perm %q", p)
		}
	}
}

func TestCan_Blocked(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "")        //nolint:errcheck
	cs.Approve(aliceID, DefaultPerms()) //nolint:errcheck
	cs.Block(aliceID)                   //nolint:errcheck

	for _, p := range allPerms {
		if cs.Can(aliceID, p) {
			t.Errorf("blocked contact should not have perm %q", p)
		}
	}
}

// ─── State graph enforcement ──────────────────────────────────────────────────

func TestStateGraph_FullCycle(t *testing.T) {
	cs := newTempStore(t)

	// pending → approved
	cs.Add(aliceID, "Alice", "")        //nolint:errcheck
	cs.Approve(aliceID, DefaultPerms()) //nolint:errcheck
	if !cs.IsApproved(aliceID) {
		t.Fatal("step1: should be approved")
	}

	// approved → blocked
	cs.Block(aliceID) //nolint:errcheck
	c, _ := cs.Get(aliceID)
	if c.State != StateBlocked {
		t.Fatal("step2: should be blocked")
	}

	// blocked → pending (unblock)
	cs.Unblock(aliceID) //nolint:errcheck
	c, _ = cs.Get(aliceID)
	if c.State != StatePending {
		t.Fatal("step3: should be pending again")
	}

	// pending → blocked (skip approve)
	cs.Block(aliceID) //nolint:errcheck
	c, _ = cs.Get(aliceID)
	if c.State != StateBlocked {
		t.Fatal("step4: should be blocked")
	}

	// blocked → removed
	cs.Remove(aliceID) //nolint:errcheck
	if _, ok := cs.Get(aliceID); ok {
		t.Fatal("step5: should be removed")
	}
}

func TestStateGraph_CannotApproveBlocked(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck
	cs.Block(aliceID)            //nolint:errcheck

	if err := cs.Approve(aliceID, DefaultPerms()); err == nil {
		t.Error("should not approve a blocked contact directly")
	}
	// Must unblock first.
	cs.Unblock(aliceID) //nolint:errcheck
	if err := cs.Approve(aliceID, DefaultPerms()); err != nil {
		t.Errorf("approve after unblock: %v", err)
	}
}

// ─── Persistence ─────────────────────────────────────────────────────────────

func TestPersistence_SurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	cs, _ := NewContactStore(dir)

	cs.Add(aliceID, "Alice", "alice.vulos.org:8080")   //nolint:errcheck
	cs.Approve(aliceID, []Perm{PermMessage, PermCall}) //nolint:errcheck
	cs.Add(bobID, "Bob", "bob.vulos.org:8080")         //nolint:errcheck
	cs.Block(bobID)                                    //nolint:errcheck

	// Simulate process restart.
	cs2, err := NewContactStore(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	// Alice
	a, ok := cs2.Get(aliceID)
	if !ok {
		t.Fatal("Alice not found after reload")
	}
	if a.State != StateApproved {
		t.Errorf("Alice state: got %q want approved", a.State)
	}
	if a.DisplayName != "Alice" {
		t.Errorf("Alice display name: got %q", a.DisplayName)
	}
	if a.Server != "alice.vulos.org:8080" {
		t.Errorf("Alice server: got %q", a.Server)
	}
	if !cs2.Can(aliceID, PermMessage) {
		t.Error("Alice should have PermMessage after reload")
	}
	if cs2.Can(aliceID, PermMedia) {
		t.Error("Alice should NOT have PermMedia after reload")
	}

	// Bob
	b, ok := cs2.Get(bobID)
	if !ok {
		t.Fatal("Bob not found after reload")
	}
	if b.State != StateBlocked {
		t.Errorf("Bob state: got %q want blocked", b.State)
	}
}

func TestPersistence_Timestamps(t *testing.T) {
	dir := t.TempDir()
	cs, _ := NewContactStore(dir)

	before := time.Now().UTC().Add(-time.Second)
	cs.Add(aliceID, "Alice", "")        //nolint:errcheck
	cs.Approve(aliceID, DefaultPerms()) //nolint:errcheck
	cs.Block(aliceID)                   //nolint:errcheck
	after := time.Now().UTC().Add(time.Second)

	cs2, _ := NewContactStore(dir)
	c, _ := cs2.Get(aliceID)

	if c.AddedAt.Before(before) || c.AddedAt.After(after) {
		t.Errorf("AddedAt out of range: %v", c.AddedAt)
	}
	if c.ApprovedAt.Before(before) || c.ApprovedAt.After(after) {
		t.Errorf("ApprovedAt out of range: %v", c.ApprovedAt)
	}
	if c.BlockedAt.Before(before) || c.BlockedAt.After(after) {
		t.Errorf("BlockedAt out of range: %v", c.BlockedAt)
	}
}

func TestPersistence_AtomicWrite(t *testing.T) {
	// Verifies that the contacts.json path is the final destination (no tmp
	// file left behind).
	dir := t.TempDir()
	cs, _ := NewContactStore(dir)
	cs.Add(aliceID, "Alice", "") //nolint:errcheck

	peeringDir := filepath.Join(dir, ".vulos", "peering")
	entries, err := os.ReadDir(peeringDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "contacts.json" {
			t.Errorf("unexpected file in peering dir: %s", e.Name())
		}
	}
}

// ─── Snapshot isolation ───────────────────────────────────────────────────────

// TestGetReturnsSnapshot verifies that mutating the returned *Contact does not
// affect the store's internal state.
func TestGetReturnsSnapshot(t *testing.T) {
	cs := newTempStore(t)
	cs.Add(aliceID, "Alice", "")        //nolint:errcheck
	cs.Approve(aliceID, DefaultPerms()) //nolint:errcheck

	c, _ := cs.Get(aliceID)
	c.DisplayName = "Hacked"
	c.Permissions = nil

	c2, _ := cs.Get(aliceID)
	if c2.DisplayName != "Alice" {
		t.Errorf("store mutated via snapshot: name=%q", c2.DisplayName)
	}
	if len(c2.Permissions) == 0 {
		t.Error("store mutated via snapshot: permissions cleared")
	}
}

// ─── Concurrent access ────────────────────────────────────────────────────────

// TestConcurrent exercises concurrent reads and writes to catch data races.
// Run with: go test -race ./services/peering/...
func TestConcurrent(t *testing.T) {
	cs := newTempStore(t)

	const goroutines = 20
	var wg sync.WaitGroup

	// Writers: add contacts with unique IDs.
	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("vula:ed25519:concurrent%04d", i)
			cs.Add(id, "Peer", "")              //nolint:errcheck
			cs.Approve(id, []Perm{PermMessage}) //nolint:errcheck
			cs.Can(id, PermMessage)
			cs.IsApproved(id)
			cs.List()
		}()
	}

	// Readers: run concurrently with writers.
	for i := 0; i < goroutines/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cs.List()
			cs.ListByState(StatePending)
			cs.IsApproved(aliceID)
		}()
	}

	wg.Wait()
}

// ─── DefaultPerms ─────────────────────────────────────────────────────────────

func TestDefaultPerms_ContainsAll(t *testing.T) {
	dp := DefaultPerms()
	if len(dp) != len(allPerms) {
		t.Errorf("DefaultPerms length: got %d want %d", len(dp), len(allPerms))
	}
	pset := make(map[Perm]bool, len(dp))
	for _, p := range dp {
		pset[p] = true
	}
	for _, p := range allPerms {
		if !pset[p] {
			t.Errorf("DefaultPerms missing %q", p)
		}
	}
}

func TestDefaultPerms_IndependentCopies(t *testing.T) {
	a := DefaultPerms()
	b := DefaultPerms()
	a[0] = "mutated"
	if b[0] == "mutated" {
		t.Error("DefaultPerms returns shared slice")
	}
}
