package appnet

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FlatpakCache tracks installed Flatpak apps with a lightweight TTL cache.
// Avoids shelling out on every registry list call.
type FlatpakCache struct {
	mu      sync.Mutex
	ids     map[string]bool // flatpak app ID → installed
	updated time.Time
	ttl     time.Duration
}

var flatpakCache = &FlatpakCache{ttl: 10 * time.Second}

// InstalledFlatpaks returns the set of installed Flatpak application IDs.
// Results are cached for 10s to keep listing lightweight.
func InstalledFlatpaks() map[string]bool {
	flatpakCache.mu.Lock()
	defer flatpakCache.mu.Unlock()

	if time.Since(flatpakCache.updated) < flatpakCache.ttl && flatpakCache.ids != nil {
		return flatpakCache.ids
	}

	ids := make(map[string]bool)
	out, err := exec.Command("flatpak", "list", "--app", "--columns=application").Output()
	if err != nil {
		log.Printf("[flatpak] list failed: %v", err)
		flatpakCache.ids = ids
		flatpakCache.updated = time.Now()
		return ids
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			ids[line] = true
		}
	}
	flatpakCache.ids = ids
	flatpakCache.updated = time.Now()
	return ids
}

// InvalidateFlatpakCache forces the next InstalledFlatpaks call to re-query.
func InvalidateFlatpakCache() {
	flatpakCache.mu.Lock()
	flatpakCache.updated = time.Time{}
	flatpakCache.mu.Unlock()
}

// IsFlatpakInstalled checks whether a specific Flatpak app is installed.
func IsFlatpakInstalled(flatpakID string) bool {
	return InstalledFlatpaks()[flatpakID]
}

// FlatpakInstall installs an app from Flathub.
func FlatpakInstall(ctx context.Context, flatpakID string) error {
	log.Printf("[flatpak] installing %s", flatpakID)
	cmd := exec.CommandContext(ctx, "flatpak", "install", "-y", "--noninteractive", "flathub", flatpakID)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[flatpak] install %s failed: %s", flatpakID, string(out))
		return err
	}
	InvalidateFlatpakCache()

	// Ensure user data dirs exist with correct ownership for all system users.
	// Flatpak's bwrap sandbox needs ~/.var/app/{id}/{cache,config,data} owned by the user.
	ensureFlatpakUserDirs(flatpakID)

	log.Printf("[flatpak] installed %s", flatpakID)
	return nil
}

// ensureFlatpakUserDirs creates ~/.var/app/{flatpakID}/ dirs for all users in /home.
func ensureFlatpakUserDirs(flatpakID string) {
	entries, err := os.ReadDir("/home")
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		username := e.Name()
		base := filepath.Join("/home", username, ".var", "app", flatpakID)
		for _, sub := range []string{"cache", "config", "data"} {
			os.MkdirAll(filepath.Join(base, sub), 0755)
		}
		// Fix ownership to the user
		exec.Command("chown", "-R", username+":"+username, filepath.Join("/home", username, ".var")).Run()
	}
}

// FlatpakUninstall removes a Flatpak app and its data.
func FlatpakUninstall(ctx context.Context, flatpakID string) error {
	log.Printf("[flatpak] uninstalling %s", flatpakID)
	cmd := exec.CommandContext(ctx, "flatpak", "uninstall", "-y", "--noninteractive", flatpakID)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("[flatpak] uninstall %s failed: %s", flatpakID, string(out))
		return err
	}
	// Clean up unused runtimes
	exec.Command("flatpak", "uninstall", "-y", "--unused").Run()
	InvalidateFlatpakCache()
	log.Printf("[flatpak] uninstalled %s", flatpakID)
	return nil
}

// FlatpakRunCommand returns the shell command to launch a flatpak app.
func FlatpakRunCommand(flatpakID string) string {
	return "flatpak run " + flatpakID
}
