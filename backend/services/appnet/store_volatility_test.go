package appnet

// SYNC-APPS-02: does the directory this box installs apps into survive a reboot?
//
// The tables below are the real shapes, not invented ones. The overlay entry is
// what scripts/initramfs/vulos-live produces on all three overlay boot paths
// (live-USB, live-ESP, netboot-installed): lower = squashfs/dm-verity, upper +
// work in ONE tmpfs at /run/vulos/rw. The plain-disk entry is what a `--disk`
// install produces. Getting these two apart is the whole job — a classifier that
// stops at "the mount is `overlay`" calls the volatile box durable, which is
// exactly the blindness that let a box re-download every app on every boot
// without anything noticing.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mountsOverlayLive is /proc/self/mounts on a live/netboot-installed box.
const mountsOverlayLive = `sysfs /sys sysfs rw,nosuid,nodev,noexec,relatime 0 0
proc /proc proc rw,nosuid,nodev,noexec,relatime 0 0
udev /dev devtmpfs rw,nosuid,relatime,size=1988416k,nr_inodes=497104,mode=755 0 0
overlay / overlay rw,relatime,lowerdir=/run/vulos/lower,upperdir=/run/vulos/rw/upper,workdir=/run/vulos/rw/work 0 0
tmpfs-rw /run/vulos/rw tmpfs rw,relatime,mode=755 0 0
tmpfs /run tmpfs rw,nosuid,nodev,mode=755 0 0
/dev/sda2 /var/cache/vulos ext4 rw,relatime 0 0
`

// mountsPlainDisk is /proc/self/mounts on a `--disk` install: a real root.
const mountsPlainDisk = `sysfs /sys sysfs rw,nosuid,nodev,noexec,relatime 0 0
/dev/sda2 / ext4 rw,relatime 0 0
tmpfs /run tmpfs rw,nosuid,nodev,mode=755 0 0
/dev/sda1 /boot/efi vfat rw,relatime 0 0
`

func TestAppStorageOnAnOverlayRootIsVolatile(t *testing.T) {
	volatile, detail := classifyMountVolatility(mountsOverlayLive, "/root/.vulos/apps")
	if !volatile {
		t.Fatalf("classifyMountVolatility(live overlay, /root/.vulos/apps) = durable — the app dir is inside the tmpfs upper "+
			"at /run/vulos/rw and does NOT survive a reboot (detail=%q)", detail)
	}
	// The detail is read by a human at another box, so it must name the mount
	// it decided on, not just assert an adjective.
	for _, want := range []string{"overlay at /", "/run/vulos/rw/upper", "tmpfs"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %q does not mention %q — a reason a user cannot act on is not a reason", detail, want)
		}
	}
}

// TestRamfsIsVolatileToo — tmpfs is the one vulos-live mounts, but an initramfs
// that never pivots leaves / as ramfs, and ramfs is RAM by definition. A
// classifier that knew only the string "tmpfs" would call that box durable.
func TestRamfsIsVolatileToo(t *testing.T) {
	mounts := "rootfs / ramfs rw 0 0\n"
	volatile, detail := classifyMountVolatility(mounts, "/root/.vulos/apps")
	if !volatile {
		t.Fatal("a ramfs root was classified durable — ramfs is RAM, and apps written to it are gone at the next boot")
	}
	if !strings.Contains(detail, "ramfs") {
		t.Errorf("detail %q does not name ramfs", detail)
	}
}

func TestAppStorageOnAPlainDiskIsDurable(t *testing.T) {
	if volatile, detail := classifyMountVolatility(mountsPlainDisk, "/root/.vulos/apps"); volatile {
		t.Fatalf("classifyMountVolatility(plain --disk root, /root/.vulos/apps) = volatile (%q) — a real ext4 root keeps its apps, "+
			"and calling it volatile would put a false reason on a replicated row", detail)
	}
}

// TestAPersistentDataDirUnderAnOverlayRootIsDurable is the case that must keep
// working the day someone makes the app dir persistent — an operator setting
// VULOS_DATA_DIR to a mounted volume today, or an initramfs bind-mount later.
// The classifier answers from the mount table, so that change alone flips this
// box to durable and every behaviour keyed on it stops firing. That is the
// property that makes this fix become UNNECESSARY rather than WRONG.
func TestAPersistentDataDirUnderAnOverlayRootIsDurable(t *testing.T) {
	mounts := mountsOverlayLive + "/dev/sdb1 /mnt/persist ext4 rw,relatime 0 0\n"
	if volatile, detail := classifyMountVolatility(mounts, "/mnt/persist/apps"); volatile {
		t.Fatalf("an ext4 volume mounted under an overlay root was classified volatile (%q) — the longest-prefix mount for "+
			"/mnt/persist/apps is /mnt/persist, not /", detail)
	}
}

// TestNearlyMatchingMountPointDoesNotCount pins the prefix rule: /rootfs-backup
// is not inside /root.
func TestNearlyMatchingMountPointDoesNotCount(t *testing.T) {
	mounts := "/dev/sda2 / ext4 rw 0 0\ntmpfs /root tmpfs rw 0 0\n"
	if volatile, _ := classifyMountVolatility(mounts, "/rootfs-backup/apps"); volatile {
		t.Error("/rootfs-backup/apps matched the tmpfs mounted at /root — string prefix is not path containment")
	}
	if volatile, _ := classifyMountVolatility(mounts, "/root/.vulos/apps"); !volatile {
		t.Error("/root/.vulos/apps did NOT match the tmpfs mounted at /root")
	}
}

// TestALowerOnlyOverlayIsNotDurable — an overlay with no upperdir is read-only.
// It is not the tmpfs case, but it is certainly not a place installs persist.
func TestALowerOnlyOverlayIsNotDurable(t *testing.T) {
	mounts := "overlay / overlay ro,relatime,lowerdir=/run/vulos/lower 0 0\n"
	volatile, detail := classifyMountVolatility(mounts, "/root/.vulos/apps")
	if !volatile {
		t.Fatal("a lower-only (read-only) overlay was classified durable")
	}
	if !strings.Contains(detail, "no writable upper") {
		t.Errorf("detail %q should say the overlay has no writable upper layer", detail)
	}
}

// TestUnknownStorageIsNotClaimedVolatile — no mount table (darwin, a stripped
// container) must produce silence, not a fabricated reason. The reason string
// travels to other boxes on a replicated row; inventing one is worse than
// having none.
func TestUnknownStorageIsNotClaimedVolatile(t *testing.T) {
	if volatile, detail := classifyMountVolatility("", "/root/.vulos/apps"); volatile || detail != "" {
		t.Errorf("empty mount table gave (%v, %q), want (false, \"\")", volatile, detail)
	}
	store := NewAppStore(t.TempDir())
	old := mountsPath
	mountsPath = filepath.Join(t.TempDir(), "no-such-mounts")
	defer func() { mountsPath = old }()
	if volatile, detail := store.StorageVolatility(); volatile || detail != "" {
		t.Errorf("StorageVolatility with an unreadable mount table gave (%v, %q), want (false, \"\")", volatile, detail)
	}
}

// TestStorageVolatilityReadsTheMountTable wires the method to the classifier —
// without this, every test above could pass while StorageVolatility returned a
// constant.
func TestStorageVolatilityReadsTheMountTable(t *testing.T) {
	dir := t.TempDir()
	appsDir := filepath.Join(dir, "root", ".vulos", "apps")
	table := filepath.Join(dir, "mounts")
	if err := os.WriteFile(table, []byte("tmpfs-rw "+filepath.Join(dir, "root")+" tmpfs rw,relatime 0 0\n"), 0o644); err != nil {
		t.Fatalf("write mount table: %v", err)
	}
	old := mountsPath
	mountsPath = table
	defer func() { mountsPath = old }()

	store := NewAppStore(appsDir)
	volatile, detail := store.StorageVolatility()
	if !volatile {
		t.Fatalf("StorageVolatility() = durable for an appsDir inside a tmpfs mount (detail=%q)", detail)
	}
	if !strings.Contains(detail, "tmpfs") {
		t.Errorf("detail %q does not name the filesystem", detail)
	}
}

// TestOctalEscapesInMountPathsAreDecoded — the kernel escapes spaces as \040.
func TestOctalEscapesInMountPathsAreDecoded(t *testing.T) {
	mounts := "tmpfs /mnt/my\\040data tmpfs rw 0 0\n"
	if volatile, _ := classifyMountVolatility(mounts, "/mnt/my data/apps"); !volatile {
		t.Error(`a mount point written as /mnt/my\040data did not match /mnt/my data`)
	}
}
