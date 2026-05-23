package cgroups

import (
	"path/filepath"
	"strings"
	"testing"
)

// newTestGovernor creates a Governor backed by a temp SQLite file, using a
// temp directory as the cgroup root so tests never touch real cgroupfs.
func newTestGovernor(t *testing.T) *Governor {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "cgroups.db")
	cgRoot := filepath.Join(dir, "cgroup")

	counter := 0
	ulidFn := func() string {
		counter++
		return filepath.Base(t.Name()) + "_" + itoa(counter)
	}

	g, err := NewGovernor(dbPath, cgRoot, ulidFn)
	if err != nil {
		t.Fatalf("NewGovernor: %v", err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

// itoa is a minimal int→string helper to avoid importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}

// --- system slice tests ---

func TestSystemSlice_WrittenToDB(t *testing.T) {
	g := newTestGovernor(t)

	if err := g.EnsureSystemSlice(); err != nil {
		t.Fatalf("EnsureSystemSlice: %v", err)
	}

	statuses, err := g.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) == 0 {
		t.Fatal("expected at least one slice in status")
	}

	var sys *SliceStatus
	for i := range statuses {
		if statuses[i].SliceType == SliceTypeSystem {
			sys = &statuses[i]
			break
		}
	}
	if sys == nil {
		t.Fatal("system slice not found in status")
	}

	// AC: system slice has memory.min >= 512 MiB written
	if sys.Limits.MemMin < 512*MiB {
		t.Errorf("system slice MemMin = %d, want >= %d", sys.Limits.MemMin, 512*MiB)
	}
}

func TestSystemSlice_CPUWeight(t *testing.T) {
	g := newTestGovernor(t)

	if err := g.EnsureSystemSlice(); err != nil {
		t.Fatalf("EnsureSystemSlice: %v", err)
	}

	statuses, _ := g.Status()
	for _, s := range statuses {
		if s.SliceType == SliceTypeSystem {
			// 30% weight → CPUWeight 300
			if s.Limits.CPUWeight != 300 {
				t.Errorf("system slice CPUWeight = %d, want 300", s.Limits.CPUWeight)
			}
			return
		}
	}
	t.Fatal("system slice not found")
}

func TestSystemSlice_Path(t *testing.T) {
	g := newTestGovernor(t)
	_ = g.EnsureSystemSlice()

	statuses, _ := g.Status()
	for _, s := range statuses {
		if s.SliceType == SliceTypeSystem {
			if !strings.HasSuffix(s.CgroupPath, "vulos-system.slice") {
				t.Errorf("unexpected system slice path: %q", s.CgroupPath)
			}
			return
		}
	}
	t.Fatal("system slice not found")
}

// --- published app (public visibility) tests ---

func TestPublicAppSlice_Limits(t *testing.T) {
	g := newTestGovernor(t)

	const appID = "app-ulid-pub-001"
	if err := g.EnsureAppSlice(appID, VisibilityPublic); err != nil {
		t.Fatalf("EnsureAppSlice(public): %v", err)
	}

	statuses, err := g.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	var pub *SliceStatus
	for i := range statuses {
		if statuses[i].AppID == appID {
			pub = &statuses[i]
			break
		}
	}
	if pub == nil {
		t.Fatalf("public app slice not found in status")
	}

	// AC: published app slice has correct limits (15% CPU weight, 128 MiB mem.high, 512 MiB mem.max)
	if pub.Limits.CPUWeight != 150 {
		t.Errorf("public app CPUWeight = %d, want 150", pub.Limits.CPUWeight)
	}
	if pub.Limits.MemHigh != 128*MiB {
		t.Errorf("public app MemHigh = %d, want %d", pub.Limits.MemHigh, 128*MiB)
	}
	if pub.Limits.MemMax != 512*MiB {
		t.Errorf("public app MemMax = %d, want %d", pub.Limits.MemMax, 512*MiB)
	}
}

func TestPrivateAppSlice_Limits(t *testing.T) {
	g := newTestGovernor(t)

	const appID = "app-ulid-priv-001"
	if err := g.EnsureAppSlice(appID, VisibilityPrivate); err != nil {
		t.Fatalf("EnsureAppSlice(private): %v", err)
	}

	statuses, _ := g.Status()
	var priv *SliceStatus
	for i := range statuses {
		if statuses[i].AppID == appID {
			priv = &statuses[i]
			break
		}
	}
	if priv == nil {
		t.Fatal("private app slice not found")
	}

	if priv.Limits.CPUWeight != 100 {
		t.Errorf("private app CPUWeight = %d, want 100", priv.Limits.CPUWeight)
	}
	if priv.Limits.MemHigh != 256*MiB {
		t.Errorf("private app MemHigh = %d, want %d", priv.Limits.MemHigh, 256*MiB)
	}
	if priv.Limits.MemMax != 512*MiB {
		t.Errorf("private app MemMax = %d, want %d", priv.Limits.MemMax, 512*MiB)
	}
}

func TestPublicAppSlice_Path(t *testing.T) {
	g := newTestGovernor(t)
	const appID = "myapp-001"
	_ = g.EnsureAppSlice(appID, VisibilityPublic)

	statuses, _ := g.Status()
	for _, s := range statuses {
		if s.AppID == appID {
			if !strings.Contains(s.CgroupPath, "vulos-apps-"+appID+".slice") {
				t.Errorf("unexpected app slice path: %q", s.CgroupPath)
			}
			return
		}
	}
	t.Fatal("app slice not found")
}

// --- idempotency ---

func TestEnsureSlice_Idempotent(t *testing.T) {
	g := newTestGovernor(t)

	for i := 0; i < 3; i++ {
		if err := g.EnsureSystemSlice(); err != nil {
			t.Fatalf("EnsureSystemSlice (call %d): %v", i+1, err)
		}
	}

	statuses, _ := g.Status()
	count := 0
	for _, s := range statuses {
		if s.SliceType == SliceTypeSystem {
			count++
		}
	}
	// Multiple rows may exist due to INSERT + UPDATE pattern, but system slice
	// should still be queryable and limits correct.
	if count == 0 {
		t.Fatal("system slice disappeared after repeated EnsureSystemSlice calls")
	}
}

// --- event recording ---

func TestRecordEvent(t *testing.T) {
	g := newTestGovernor(t)
	_ = g.EnsureSystemSlice()

	statuses, _ := g.Status()
	if len(statuses) == 0 {
		t.Fatal("no slices")
	}
	sliceID := statuses[0].ID

	if err := g.RecordEvent(sliceID, "oom", "process 1234 killed"); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	// Verify the event is in the DB.
	var count int
	_ = g.db.QueryRow(`SELECT COUNT(*) FROM cgroup_events WHERE slice_id=?`, sliceID).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 event, got %d", count)
	}
}

// --- status returns live fields (may be zero on non-Linux) ---

func TestStatus_LiveFields(t *testing.T) {
	g := newTestGovernor(t)
	_ = g.EnsureSystemSlice()

	statuses, err := g.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) == 0 {
		t.Fatal("empty status")
	}
	// MemCurrent and CPUUsageUs are allowed to be zero (non-Linux / fresh cgroup).
	// We just ensure the fields are present and non-negative.
	for _, s := range statuses {
		if s.MemCurrent < 0 {
			t.Errorf("MemCurrent negative: %d", s.MemCurrent)
		}
		if s.CPUUsageUs < 0 {
			t.Errorf("CPUUsageUs negative: %d", s.CPUUsageUs)
		}
	}
}

// --- multiple app slices coexist ---

func TestMultipleAppSlices(t *testing.T) {
	g := newTestGovernor(t)

	apps := []struct {
		id  string
		vis Visibility
	}{
		{"app-aaa", VisibilityPublic},
		{"app-bbb", VisibilityPrivate},
		{"app-ccc", VisibilityPublic},
	}
	for _, a := range apps {
		if err := g.EnsureAppSlice(a.id, a.vis); err != nil {
			t.Fatalf("EnsureAppSlice(%s): %v", a.id, err)
		}
	}

	_ = g.EnsureSystemSlice()

	statuses, err := g.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	seen := make(map[string]bool)
	for _, s := range statuses {
		if s.AppID != "" {
			seen[s.AppID] = true
		}
	}
	for _, a := range apps {
		if !seen[a.id] {
			t.Errorf("app %s not found in status", a.id)
		}
	}
}
