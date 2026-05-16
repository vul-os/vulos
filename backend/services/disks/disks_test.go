package disks

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Pure-logic helpers extracted from disks.go for fixture-based testing.
// These mirror the real implementations without calling exec or syscall.
// ---------------------------------------------------------------------------

// parseMountLines mirrors the mount-output loop in GetStatus, minus the
// statMount syscall.  It returns only the structural (device/mount/fstype)
// fields so we can test filtering logic.
func parseMountLines(raw string) []Mount {
	seen := make(map[string]bool)
	var mounts []Mount

	for _, line := range strings.Split(raw, "\n") {
		parts := strings.Fields(line)
		if len(parts) < 5 || parts[1] != "on" || parts[3] != "type" {
			continue
		}
		device, mountPoint, fsType := parts[0], parts[2], parts[4]

		// Same virtual-fs skip list as the real code
		switch fsType {
		case "proc", "sysfs", "devpts", "tmpfs", "devtmpfs", "cgroup", "cgroup2",
			"securityfs", "debugfs", "tracefs", "fusectl", "configfs", "hugetlbfs",
			"mqueue", "pstore", "binfmt_misc", "autofs", "rpc_pipefs", "nfsd",
			"overlay", "nsfs", "efivarfs":
			continue
		}
		if strings.HasPrefix(device, "none") || strings.HasPrefix(device, "cgroup") {
			continue
		}
		if seen[mountPoint] {
			continue
		}
		seen[mountPoint] = true

		mounts = append(mounts, Mount{Device: device, MountPoint: mountPoint, FSType: fsType})
	}
	return mounts
}

// parseDuLines mirrors the DirBreakdown parsing loop, without exec.
func parseDuLines(raw string, basePath string) []DirUsage {
	var dirs []DirUsage
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		sizeMB, _ := strconv.ParseInt(parts[0], 10, 64)
		dirPath := strings.Join(parts[1:], " ")

		if filepath.Clean(dirPath) == filepath.Clean(basePath) {
			continue
		}

		dirs = append(dirs, DirUsage{
			Path:   dirPath,
			Name:   filepath.Base(dirPath),
			SizeMB: sizeMB,
		})
	}

	sort.Slice(dirs, func(i, j int) bool { return dirs[i].SizeMB > dirs[j].SizeMB })

	if len(dirs) > 20 {
		dirs = dirs[:20]
	}
	return dirs
}

// ---------------------------------------------------------------------------
// mount-output fixture
// ---------------------------------------------------------------------------

const mountFixture = `sysfs on /sys type sysfs (rw,nosuid,nodev,noexec,relatime)
proc on /proc type proc (rw,nosuid,nodev,noexec,relatime)
devtmpfs on /dev type devtmpfs (rw,nosuid,size=8192k,nr_inodes=4096,mode=755)
tmpfs on /dev/shm type tmpfs (rw,nosuid,nodev)
/dev/sda1 on / type ext4 (rw,relatime)
/dev/sda2 on /boot type ext4 (rw,relatime)
/dev/mapper/data on /data type xfs (rw,relatime)
none on /sys/fs/cgroup type cgroup2 (rw,nosuid,nodev,noexec,relatime)
overlay on /var/lib/docker/overlay2/abc type overlay (rw,relatime)
`

func TestParseMountLines_RealFSOnly(t *testing.T) {
	mounts := parseMountLines(mountFixture)
	// Should only contain ext4 (/  /boot) and xfs (/data)
	if len(mounts) != 3 {
		t.Fatalf("expected 3 real mounts, got %d: %+v", len(mounts), mounts)
	}
}

func TestParseMountLines_VirtualSkipped(t *testing.T) {
	mounts := parseMountLines(mountFixture)
	for _, m := range mounts {
		switch m.FSType {
		case "proc", "sysfs", "devpts", "tmpfs", "devtmpfs", "cgroup", "cgroup2",
			"securityfs", "debugfs", "tracefs", "fusectl", "configfs", "hugetlbfs",
			"mqueue", "pstore", "binfmt_misc", "autofs", "rpc_pipefs", "nfsd",
			"overlay", "nsfs", "efivarfs":
			t.Errorf("virtual filesystem %q leaked through: %+v", m.FSType, m)
		}
	}
}

func TestParseMountLines_DeviceAndFSType(t *testing.T) {
	mounts := parseMountLines(mountFixture)
	// First real mount is /dev/sda1 /
	if mounts[0].Device != "/dev/sda1" {
		t.Errorf("device: want /dev/sda1, got %q", mounts[0].Device)
	}
	if mounts[0].MountPoint != "/" {
		t.Errorf("mountpoint: want /, got %q", mounts[0].MountPoint)
	}
	if mounts[0].FSType != "ext4" {
		t.Errorf("fstype: want ext4, got %q", mounts[0].FSType)
	}
}

func TestParseMountLines_NoDuplicates(t *testing.T) {
	// Duplicate mount point should only appear once.
	dup := `/dev/sda1 on / type ext4 (rw)
/dev/sdb1 on / type ext4 (rw)
`
	mounts := parseMountLines(dup)
	if len(mounts) != 1 {
		t.Errorf("duplicate mount point should be deduplicated; got %d", len(mounts))
	}
}

func TestParseMountLines_CgroupPrefixSkipped(t *testing.T) {
	raw := `cgroup on /sys/fs/cgroup/memory type cgroup (rw)
/dev/nvme0n1p1 on /boot type vfat (rw)
`
	mounts := parseMountLines(raw)
	for _, m := range mounts {
		if strings.HasPrefix(m.Device, "cgroup") {
			t.Errorf("cgroup device should be skipped: %+v", m)
		}
	}
	if len(mounts) != 1 {
		t.Errorf("expected 1 mount, got %d", len(mounts))
	}
}

func TestParseMountLines_Empty(t *testing.T) {
	mounts := parseMountLines("")
	if len(mounts) != 0 {
		t.Errorf("expected empty result, got %+v", mounts)
	}
}

// ---------------------------------------------------------------------------
// du -m output fixture
// ---------------------------------------------------------------------------

const duFixture = `4096	/home
2048	/home/alice
512	/home/bob
1	/home/.config
4096	/home
`

func TestParseDuLines_SortedLargestFirst(t *testing.T) {
	dirs := parseDuLines(duFixture, "/home")
	for i := 1; i < len(dirs); i++ {
		if dirs[i].SizeMB > dirs[i-1].SizeMB {
			t.Errorf("not sorted at index %d: %d > %d", i, dirs[i].SizeMB, dirs[i-1].SizeMB)
		}
	}
}

func TestParseDuLines_SkipBasePath(t *testing.T) {
	dirs := parseDuLines(duFixture, "/home")
	for _, d := range dirs {
		if filepath.Clean(d.Path) == filepath.Clean("/home") {
			t.Errorf("base path /home should be excluded from results")
		}
	}
}

func TestParseDuLines_NameExtracted(t *testing.T) {
	dirs := parseDuLines(duFixture, "/home")
	if dirs[0].Name != "alice" {
		t.Errorf("expected alice (2048 MB), got %q", dirs[0].Name)
	}
}

func TestParseDuLines_Limit20(t *testing.T) {
	// Build a fixture with 25 entries
	var sb strings.Builder
	for i := 0; i < 25; i++ {
		sb.WriteString(strconv.Itoa(i+1) + "\t/base/dir" + strconv.Itoa(i) + "\n")
	}
	dirs := parseDuLines(sb.String(), "/base")
	if len(dirs) > 20 {
		t.Errorf("expected at most 20 entries, got %d", len(dirs))
	}
}

func TestParseDuLines_SizeParsing(t *testing.T) {
	dirs := parseDuLines(duFixture, "/home")
	// After sorting: alice=2048, bob=512, .config=1
	if dirs[0].SizeMB != 2048 {
		t.Errorf("alice: want 2048, got %d", dirs[0].SizeMB)
	}
	if dirs[1].SizeMB != 512 {
		t.Errorf("bob: want 512, got %d", dirs[1].SizeMB)
	}
}

func TestParseDuLines_PathWithSpaces(t *testing.T) {
	raw := "100\t/home/my user\n"
	dirs := parseDuLines(raw, "/home")
	if len(dirs) != 1 {
		t.Fatalf("expected 1 dir, got %d", len(dirs))
	}
	if dirs[0].Path != "/home/my user" {
		t.Errorf("path with space: want '/home/my user', got %q", dirs[0].Path)
	}
}
