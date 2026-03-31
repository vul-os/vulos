package appnet

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TrafficStats holds byte counters for an app's namespace.
type TrafficStats struct {
	AppID      string    `json:"app_id"`
	RxBytes    uint64    `json:"rx_bytes"`
	TxBytes    uint64    `json:"tx_bytes"`
	LastActive time.Time `json:"last_active"`
	IdleFor    string    `json:"idle_for"`
}

// TrafficMonitor watches iptables byte counters on each app's veth
// to detect idle apps. Uses the kernel's own accounting — zero overhead.
type TrafficMonitor struct {
	mu       sync.Mutex
	previous map[string]uint64 // appID -> last known rx+tx total
	lastSeen map[string]time.Time
}

func NewTrafficMonitor() *TrafficMonitor {
	return &TrafficMonitor{
		previous: make(map[string]uint64),
		lastSeen: make(map[string]time.Time),
	}
}

// Sample reads the current byte counters for an app's host-side veth.
// Uses /sys/class/net/<veth>/statistics/ which is free (no exec).
func (tm *TrafficMonitor) Sample(ns *Namespace) TrafficStats {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	rx := readSysCounter(ns.VethHost, "rx_bytes")
	tx := readSysCounter(ns.VethHost, "tx_bytes")
	total := rx + tx

	stats := TrafficStats{
		AppID:   ns.AppID,
		RxBytes: rx,
		TxBytes: tx,
	}

	prev, hasPrev := tm.previous[ns.AppID]
	if !hasPrev {
		// First sample — mark as active now
		tm.previous[ns.AppID] = total
		tm.lastSeen[ns.AppID] = time.Now()
		stats.LastActive = time.Now()
		stats.IdleFor = "0s"
		return stats
	}

	if total != prev {
		// Traffic happened since last sample
		tm.previous[ns.AppID] = total
		tm.lastSeen[ns.AppID] = time.Now()
	}

	stats.LastActive = tm.lastSeen[ns.AppID]
	stats.IdleFor = time.Since(stats.LastActive).Truncate(time.Second).String()
	return stats
}

// IdleSince returns how long an app has been idle (no traffic).
func (tm *TrafficMonitor) IdleSince(appID string) time.Duration {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if t, ok := tm.lastSeen[appID]; ok {
		return time.Since(t)
	}
	return 0
}

// Forget removes tracking for a stopped app.
func (tm *TrafficMonitor) Forget(appID string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	delete(tm.previous, appID)
	delete(tm.lastSeen, appID)
}

// readSysCounter reads a network counter from sysfs via direct file I/O.
func readSysCounter(iface, counter string) uint64 {
	path := fmt.Sprintf("/sys/class/net/%s/statistics/%s", iface, counter)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	val, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	return val
}

// SampleAll reads traffic stats for all active namespaces.
func (tm *TrafficMonitor) SampleAll(mgr *Manager) []TrafficStats {
	nsList := mgr.List()
	stats := make([]TrafficStats, 0, len(nsList))
	for _, ns := range nsList {
		stats = append(stats, tm.Sample(ns))
	}
	return stats
}

// FindIdle returns app IDs that have been idle longer than the threshold.
func (tm *TrafficMonitor) FindIdle(mgr *Manager, threshold time.Duration) []string {
	tm.SampleAll(mgr)
	tm.mu.Lock()
	defer tm.mu.Unlock()

	var idle []string
	for appID, lastSeen := range tm.lastSeen {
		if time.Since(lastSeen) > threshold {
			idle = append(idle, appID)
		}
	}
	return idle
}

// WatchAndKill runs a loop that checks for idle apps and stops them.
func (tm *TrafficMonitor) WatchAndKill(ctx context.Context, launcher *Launcher, mgr *Manager, idleTimeout time.Duration, checkInterval time.Duration) {
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			idle := tm.FindIdle(mgr, idleTimeout)
			for _, appID := range idle {
				log.Printf("[energy] app %s idle for >%s — stopping", appID, idleTimeout)
				if err := launcher.Stop(ctx, appID); err != nil {
					log.Printf("[energy] failed to stop %s: %v", appID, err)
				}
				tm.Forget(appID)
			}
		}
	}
}
