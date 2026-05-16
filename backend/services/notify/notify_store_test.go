package notify

// notify_store_test.go — NOTIF-02 store + DND-state tests.

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestStore_RoundTrip verifies that appended notifications survive an
// OpenStore → Append → reopen cycle.
func TestStore_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db", "notifications.json")

	s1, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	s1.Append(&Notification{ID: "a", Title: "first", CreatedAt: time.Now()})
	s1.Append(&Notification{ID: "b", Title: "second", CreatedAt: time.Now()})
	s1.Close()

	s2, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen OpenStore: %v", err)
	}
	if s2.Len() != 2 {
		t.Fatalf("after reload Len = %d, want 2", s2.Len())
	}
	got := s2.List(0) // newest first
	if got[0].ID != "b" || got[1].ID != "a" {
		t.Errorf("List order = [%s %s], want [b a]", got[0].ID, got[1].ID)
	}
}

// TestStore_FilePerms verifies the on-disk file is created with 0600.
func TestStore_FilePerms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notifications.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	s.Append(&Notification{ID: "x", CreatedAt: time.Now()})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file perm = %o, want 600", perm)
	}
}

// TestStore_MissingFileFreshStore verifies a missing file yields an empty
// store with no error.
func TestStore_MissingFileFreshStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does", "not", "exist.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore on missing file should not error, got %v", err)
	}
	if s.Len() != 0 {
		t.Errorf("fresh store Len = %d, want 0", s.Len())
	}
}

// TestStore_CorruptFileFreshStore verifies a corrupt file yields an empty
// store with no error.
func TestStore_CorruptFileFreshStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notifications.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore on corrupt file should not error, got %v", err)
	}
	if s.Len() != 0 {
		t.Errorf("corrupt-recovery store Len = %d, want 0", s.Len())
	}
}

// TestStore_RetentionCap verifies that Append enforces the default cap (500).
func TestStore_RetentionCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notifications.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	for i := 0; i < defaultMaxN+50; i++ {
		s.Append(&Notification{ID: "n", CreatedAt: time.Now()})
	}
	if s.Len() != defaultMaxN {
		t.Errorf("Len after over-cap appends = %d, want %d", s.Len(), defaultMaxN)
	}
}

// TestStore_RetentionAge verifies that Prune drops entries older than maxAge.
func TestStore_RetentionAge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notifications.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	s.Append(&Notification{ID: "old", CreatedAt: time.Now().Add(-48 * time.Hour)})
	s.Append(&Notification{ID: "new", CreatedAt: time.Now()})

	s.Prune(0, 24*time.Hour) // drop anything older than 24h
	if s.Len() != 1 {
		t.Fatalf("Len after age prune = %d, want 1", s.Len())
	}
	if got := s.List(0); got[0].ID != "new" {
		t.Errorf("survivor = %s, want 'new'", got[0].ID)
	}
}

// TestStore_PruneCap verifies the explicit cap bound of Prune.
func TestStore_PruneCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notifications.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	for i := 0; i < 10; i++ {
		s.Append(&Notification{ID: "k", CreatedAt: time.Now()})
	}
	s.Prune(3, 0)
	if s.Len() != 3 {
		t.Errorf("Len after cap prune = %d, want 3", s.Len())
	}
}

// TestStore_MarkRead verifies MarkRead / MarkAllRead persist.
func TestStore_MarkRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notifications.json")
	s, _ := OpenStore(path)
	s.Append(&Notification{ID: "r1", CreatedAt: time.Now()})
	s.Append(&Notification{ID: "r2", CreatedAt: time.Now()})

	s.MarkRead("r1")
	reopened, _ := OpenStore(path)
	for _, n := range reopened.List(0) {
		if n.ID == "r1" && !n.Read {
			t.Error("r1 should be read after reload")
		}
		if n.ID == "r2" && n.Read {
			t.Error("r2 should still be unread")
		}
	}

	s.MarkAllRead()
	all, _ := OpenStore(path)
	for _, n := range all.List(0) {
		if !n.Read {
			t.Errorf("notification %s should be read after MarkAllRead", n.ID)
		}
	}
}

// TestStore_NilReceiverSafe verifies every method is safe on a nil *Store.
func TestStore_NilReceiverSafe(t *testing.T) {
	var s *Store
	// None of these should panic.
	s.Append(&Notification{ID: "x"})
	s.Close()
	s.MarkRead("x")
	s.MarkAllRead()
	s.Prune(1, time.Hour)
	if s.Len() != 0 {
		t.Errorf("nil Store Len = %d, want 0", s.Len())
	}
	if s.List(10) != nil {
		t.Error("nil Store List should return nil")
	}
}

// TestService_NilStoreNoPanic verifies a Service with no store attached still
// delivers via the in-memory path without panicking.
func TestService_NilStoreNoPanic(t *testing.T) {
	svc := New() // store is nil
	n := svc.SendNotification(Notification{Title: "hello", Type: TypeAlert})
	if n == nil {
		t.Fatal("SendNotification returned nil")
	}
	if got := svc.List(0); len(got) != 1 {
		t.Errorf("in-memory history len = %d, want 1", len(got))
	}
}

// TestService_StorePersistsViaSend verifies SetStore + SendNotification path.
func TestService_StorePersistsViaSend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notifications.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	svc := New()
	svc.SetStore(store)

	svc.SendNotification(Notification{Title: "persisted", Type: TypeAlert})

	if store.Len() != 1 {
		t.Errorf("store Len after Send = %d, want 1", store.Len())
	}
	// In-memory path must remain intact too.
	if len(svc.List(0)) != 1 {
		t.Error("in-memory history should still hold the notification")
	}

	reloaded, _ := OpenStore(path)
	if reloaded.Len() != 1 {
		t.Errorf("reloaded store Len = %d, want 1", reloaded.Len())
	}
}

// TestStore_ConcurrentAppend verifies 50 concurrent Appends yield 50 entries.
func TestStore_ConcurrentAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notifications.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Append(&Notification{ID: "c", CreatedAt: time.Now()})
		}()
	}
	wg.Wait()

	if s.Len() != 50 {
		t.Errorf("Len after 50 concurrent appends = %d, want 50", s.Len())
	}
}

// TestDND_LazyInit verifies DND() lazily initialises to mode "off" and never
// returns nil.
func TestDND_LazyInit(t *testing.T) {
	svc := New()
	d := svc.DND()
	if d == nil {
		t.Fatal("DND() returned nil")
	}
	mode, until, sched := d.Get()
	if mode != DNDOff {
		t.Errorf("lazy DND mode = %q, want %q", mode, DNDOff)
	}
	if !until.IsZero() {
		t.Errorf("lazy DND until = %v, want zero", until)
	}
	if sched != nil {
		t.Errorf("lazy DND schedule = %v, want nil", sched)
	}
	// Repeated calls return the same instance.
	if svc.DND() != d {
		t.Error("DND() should return a stable instance")
	}
}

// TestDND_SetGet verifies Set/Get round-trip including schedule copy semantics.
func TestDND_SetGet(t *testing.T) {
	svc := New()
	d := svc.DND()

	deadline := time.Now().Add(2 * time.Hour)
	in := []DNDWindow{{Start: "22:00", End: "07:00", Days: []int{1, 2, 3, 4, 5}}}
	d.Set(DNDPriority, deadline, in)

	mode, until, out := d.Get()
	if mode != DNDPriority {
		t.Errorf("mode = %q, want %q", mode, DNDPriority)
	}
	if !until.Equal(deadline) {
		t.Errorf("until = %v, want %v", until, deadline)
	}
	if len(out) != 1 || out[0].Start != "22:00" || out[0].End != "07:00" || len(out[0].Days) != 5 {
		t.Errorf("schedule round-trip mismatch: %+v", out)
	}

	// Mutating the returned copy must not affect stored state.
	out[0].Start = "00:00"
	_, _, again := d.Get()
	if again[0].Start != "22:00" {
		t.Error("Get returned a shared slice; should be a defensive copy")
	}

	// Empty mode normalises to off.
	d.Set("", time.Time{}, nil)
	if m, _, _ := d.Get(); m != DNDOff {
		t.Errorf("empty mode should normalise to off, got %q", m)
	}
}

// TestDND_NilStateSafe verifies the state holder is nil-safe.
func TestDND_NilStateSafe(t *testing.T) {
	var d *dndState
	d.Set(DNDTotal, time.Now(), []DNDWindow{{Start: "00:00"}}) // must not panic
	mode, until, sched := d.Get()
	if mode != DNDOff || !until.IsZero() || sched != nil {
		t.Errorf("nil dndState Get = (%q,%v,%v), want (off, zero, nil)", mode, until, sched)
	}
}
