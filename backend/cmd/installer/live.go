//go:build linux

// Command vulos-install is the Vulos OS bare-metal installer CLI.
//
// Usage:
//
//	vulos-install --live /dev/sdX
//
// The --live flag writes a bootable EFI System Partition (ESP) to the
// named block device so the USB drive can boot a Vulos live environment.
//
// # What it does
//
//  1. Partitions the target device as GPT: 512 MiB FAT32 ESP (partition 1)
//     + remainder ext4 data (partition 2).
//  2. Formats the ESP as FAT32 with label "EFI".
//  3. Copies the live squashfs, initramfs, kernel, trust-anchor key, and
//     OS bucket URL from the running system onto the ESP.
//  4. Installs the systemd-boot EFI stub via bootctl so that UEFI firmware
//     finds EFI/BOOT/bootx64.efi on the removable-media path.
//  5. Writes a loader/entries/vulos-live.conf boot entry that passes
//     vulos.live=1 and toram to the kernel so the live session runs
//     entirely from RAM (NETB-03 toram convention).
//
// The squashfs is sha256-verified against stable.json before write (when
// the manifest is present on the live medium) to ensure the USB image
// matches the published manifest — mirroring the netboot iPXE path.
//
// Build:
//
//	GOOS=linux GOARCH=amd64 go build -o vulos-install ./cmd/installer
//
// On darwin, `go build ./...` skips this file because of the linux build tag.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"vulos/backend/internal/installer"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[vulos-install] ")

	liveTarget := flag.String("live", "", "Block device to write a bootable live USB (e.g. /dev/sdb)")
	squashfsPath := flag.String("squashfs", "", "Override source squashfs path (default: /run/live/medium/vulos/os-core.squashfs)")
	manifestPath := flag.String("manifest", "", "Override stable.json manifest path for squashfs digest check")
	flag.Parse()

	if *liveTarget == "" {
		fmt.Fprintln(os.Stderr, "usage: vulos-install --live /dev/sdX")
		flag.Usage()
		os.Exit(1)
	}

	// Require root: ESP writing needs mount, parted, mkfs.
	if os.Getuid() != 0 {
		fmt.Fprintln(os.Stderr, "error: vulos-install must be run as root")
		os.Exit(1)
	}

	// Warn loudly: this is a destructive operation.
	fmt.Printf("WARNING: All data on %s will be overwritten.\n", *liveTarget)
	fmt.Printf("Press Enter to continue, Ctrl-C to abort...\n")
	// Read one byte (Enter) to confirm.
	buf := make([]byte, 1)
	if _, err := os.Stdin.Read(buf); err != nil {
		fmt.Fprintf(os.Stderr, "stdin read: %v\n", err)
		os.Exit(1)
	}

	// Set up context with signal handling so Ctrl-C triggers a clean exit.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Progress channel: print progress percentages to stdout.
	progress := make(chan int, 128)
	go func() {
		last := -1
		for pct := range progress {
			if pct != last {
				fmt.Printf("\r[%3d%%]", pct)
				last = pct
			}
		}
		fmt.Println()
	}()

	cfg := installer.Config{
		TargetDisk:   *liveTarget,
		SquashfsPath: *squashfsPath,
		ManifestPath: *manifestPath,
		Progress:     progress,
	}

	log.Printf("writing bootable live USB to %s", cfg.TargetDisk)
	if err := installer.WriteBootableESP(ctx, cfg); err != nil {
		close(progress)
		log.Fatalf("FAILED: %v", err)
	}
	close(progress)
	log.Printf("done — %s is now a bootable Vulos live USB", cfg.TargetDisk)
}
