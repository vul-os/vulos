//go:build linux

package installer

// livesource.go — find the OS image on the running live system.
//
// # The bug this closes
//
// The installer defaulted to /run/live/medium/vulos/os-core.squashfs, a path
// nothing on a Vulos live image ever creates. build.sh writes the OS as a plain
// file at /image.squashfs on the ext4 partition labelled VULOS-LIVE-DATA, and
// that is the only file on it.
//
// So the documented primary install route — boot the USB, run vulos-install
// --disk — could not read the image it was supposed to copy, even once the
// binary itself shipped. A default path that matches no image is not a default;
// it is a guess that was never checked against the thing being built.
//
// # Why a search rather than one corrected constant
//
// The image is reachable under more than one path depending on how the box got
// here, and which one is live is not knowable from inside the process:
//
//   - a Debian live-boot medium really does mount at /run/live/medium;
//   - a Vulos live USB has the data partition mounted by the initramfs, which
//     then overlays the merged root over that mount point — so the plain file is
//     reachable at /image.squashfs while the partition itself may be shadowed;
//   - the partition can always be found by LABEL, which is the one identifier
//     build.sh actually guarantees.
//
// Trying each in order and reporting which one answered is both more robust and
// more debuggable than picking one and being wrong on the other two. The error
// when none match lists every path tried, because "squashfs not found" with no
// list is the least actionable message a person can be handed while standing at
// a machine they just wiped.

import (
	"fmt"
	"os"
	"path/filepath"
)

// liveDataLabel is the filesystem label build.sh gives the live data partition.
// It is the only stable identifier for the image's location — paths vary with
// how the medium was booted, a label does not.
const liveDataLabel = "VULOS-LIVE-DATA"

// squashfsCandidates are the paths an OS image is found at, most specific
// first. Order matters only for reporting: the first that EXISTS wins, and a
// live-boot medium's own path is preferred when both are present because that
// one is mounted read-only by design.
// squashfsCandidatesFn is the seam the tests swap. The real paths are absolute
// and root-owned, so a test using them would need root or would assert nothing.
var squashfsCandidatesFn = squashfsCandidates

func squashfsCandidates() []string {
	return []string{
		liveSquashfsPath,                                   // Debian live-boot medium
		"/image.squashfs",                                  // Vulos live USB, post-pivot
		"/run/vulos/medium/image.squashfs",                 // explicit mount, if one exists
		filepath.Join("/dev/disk/by-label", liveDataLabel), // placeholder; see resolveSquashfs
	}
}

// resolveSquashfs returns the path of the OS image on this live system, and the
// reason it was chosen.
//
// The by-label entry is handled separately rather than stat'd like the others:
// /dev/disk/by-label/<L> is a BLOCK DEVICE, not a file, so a plain existence
// check would "find" it and then hand the caller something it cannot copy. When
// the device exists but nothing is mounted from it, that is worth saying
// explicitly — the fix is a mount, not a different path, and a caller told
// "not found" would go looking in the wrong place.
func resolveSquashfs() (path string, why string, err error) {
	var tried []string
	for _, c := range squashfsCandidatesFn() {
		if c == filepath.Join("/dev/disk/by-label", liveDataLabel) {
			continue // handled below
		}
		tried = append(tried, c)
		st, statErr := os.Stat(c)
		if statErr == nil && !st.IsDir() && st.Size() > 0 {
			return c, "found on the live medium", nil
		}
	}

	dev := filepath.Join("/dev/disk/by-label", liveDataLabel)
	if _, statErr := os.Stat(dev); statErr == nil {
		return "", "", fmt.Errorf(
			"the OS image was not found at any known path (tried %v), but the live data partition IS present at %s — "+
				"it is not mounted, so mount it and pass --squashfs <mountpoint>/image.squashfs",
			tried, dev)
	}

	return "", "", fmt.Errorf(
		"the OS image was not found on this system (tried %v, and no partition labelled %s is attached). "+
			"If you are not booted from a Vulos live medium, pass --squashfs explicitly",
		tried, liveDataLabel)
}
