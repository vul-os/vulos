package telemetry

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/websocket"
)

// SystemStats is the telemetry payload streamed to clients.
type SystemStats struct {
	CPU        float64 `json:"cpu"`
	MemTotal   uint64  `json:"mem_total"`
	MemUsed    uint64  `json:"mem_used"`
	MemPercent float64 `json:"mem_percent"`
	Temp       float64 `json:"temp"`
	Battery    int     `json:"battery"`
	Charging   bool    `json:"charging"`
	NetRx      uint64  `json:"net_rx"`
	NetTx      uint64  `json:"net_tx"`
	Uptime     string  `json:"uptime"`
	Hostname   string  `json:"hostname"`
	NumCPU     int     `json:"num_cpu"`
	LoadAvg    string  `json:"load_avg"`
	Timestamp  int64   `json:"timestamp"`
}

// Handler returns a WebSocket handler that streams system telemetry.
// Connect via: ws://host:port/api/telemetry
func Handler() http.Handler {
	return websocket.Handler(func(ws *websocket.Conn) {
		ws.PayloadType = websocket.TextFrame
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		log.Printf("[telemetry] client connected")

		// Send initial stats immediately
		send(ws, collect())

		for {
			select {
			case <-ticker.C:
				if err := send(ws, collect()); err != nil {
					log.Printf("[telemetry] client disconnected")
					return
				}
			}
		}
	})
}

func send(ws *websocket.Conn, stats SystemStats) error {
	data, _ := json.Marshal(stats)
	_, err := ws.Write(data)
	return err
}

var prevIdle, prevTotal uint64

func collect() SystemStats {
	stats := SystemStats{
		NumCPU:    runtime.NumCPU(),
		Timestamp: time.Now().UnixMilli(),
	}

	stats.Hostname, _ = os.Hostname()

	// CPU usage from /proc/stat
	if data, err := os.ReadFile("/proc/stat"); err == nil {
		lines := strings.Split(string(data), "\n")
		if len(lines) > 0 && strings.HasPrefix(lines[0], "cpu ") {
			fields := strings.Fields(lines[0])
			if len(fields) >= 8 {
				var idle, total uint64
				for i, f := range fields[1:] {
					v, _ := strconv.ParseUint(f, 10, 64)
					total += v
					if i == 3 { // idle is 4th field
						idle = v
					}
				}
				if prevTotal > 0 {
					deltaTotal := total - prevTotal
					deltaIdle := idle - prevIdle
					if deltaTotal > 0 {
						stats.CPU = float64(deltaTotal-deltaIdle) / float64(deltaTotal) * 100
					}
				}
				prevIdle = idle
				prevTotal = total
			}
		}
	}

	// Memory from /proc/meminfo
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		vals := parseMeminfo(string(data))
		stats.MemTotal = vals["MemTotal"]
		available := vals["MemAvailable"]
		if available == 0 {
			available = vals["MemFree"] + vals["Buffers"] + vals["Cached"]
		}
		stats.MemUsed = stats.MemTotal - available
		if stats.MemTotal > 0 {
			stats.MemPercent = float64(stats.MemUsed) / float64(stats.MemTotal) * 100
		}
	}

	// Temperature from thermal zones
	matches, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	var maxTemp float64
	for _, m := range matches {
		if data, err := os.ReadFile(m); err == nil {
			v, _ := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
			t := v / 1000
			if t > maxTemp {
				maxTemp = t
			}
		}
	}
	stats.Temp = maxTemp

	// Battery
	stats.Battery, stats.Charging = readBattery()

	// Network counters
	if data, err := os.ReadFile("/proc/net/dev"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "lo:") || !strings.Contains(line, ":") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 10 {
				rx, _ := strconv.ParseUint(fields[1], 10, 64)
				tx, _ := strconv.ParseUint(fields[9], 10, 64)
				stats.NetRx += rx
				stats.NetTx += tx
			}
		}
	}

	// Uptime
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		parts := strings.Fields(string(data))
		if len(parts) > 0 {
			secs, _ := strconv.ParseFloat(parts[0], 64)
			d := time.Duration(secs * float64(time.Second))
			h := int(d.Hours())
			m := int(d.Minutes()) % 60
			stats.Uptime = strconv.Itoa(h) + "h" + strconv.Itoa(m) + "m"
		}
	}

	// Load average
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		parts := strings.Fields(string(data))
		if len(parts) >= 3 {
			stats.LoadAvg = parts[0] + " " + parts[1] + " " + parts[2]
		}
	}

	return stats
}

func parseMeminfo(data string) map[string]uint64 {
	vals := make(map[string]uint64)
	for _, line := range strings.Split(data, "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			key := strings.TrimSuffix(parts[0], ":")
			v, _ := strconv.ParseUint(parts[1], 10, 64)
			vals[key] = v * 1024 // convert kB to bytes
		}
	}
	return vals
}

// SysInfo is the payload for the About page.
type SysInfo struct {
	Hostname     string `json:"hostname"`
	Kernel       string `json:"kernel"`
	Arch         string `json:"arch"`
	CPUModel     string `json:"cpu_model"`
	CPUCores     int    `json:"cpu_cores"`
	MemTotalMB   int    `json:"mem_total_mb"`
	MemUsedMB    int    `json:"mem_used_mb"`
	MemPercent   float64 `json:"mem_percent"`
	Uptime       string `json:"uptime"`
	AlpineVer    string `json:"alpine_version"`
	DeviceModel  string `json:"device_model"`
	Battery      int    `json:"battery"`
	Charging     bool   `json:"charging"`
	StorageTotalMB int  `json:"storage_total_mb"`
	StorageUsedMB  int  `json:"storage_used_mb"`
}

// SystemInfo returns a one-shot system info snapshot for the About page.
func SystemInfo() SysInfo {
	info := SysInfo{
		CPUCores: runtime.NumCPU(),
		Arch:     runtime.GOARCH,
	}

	info.Hostname, _ = os.Hostname()

	// Kernel version
	if data, err := os.ReadFile("/proc/version"); err == nil {
		parts := strings.Fields(string(data))
		if len(parts) >= 3 {
			info.Kernel = parts[2]
		}
	}

	// CPU model from /proc/cpuinfo
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "model name") || strings.HasPrefix(line, "Model") || strings.HasPrefix(line, "Hardware") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					info.CPUModel = strings.TrimSpace(parts[1])
					break
				}
			}
		}
	}

	// Memory
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		vals := parseMeminfo(string(data))
		info.MemTotalMB = int(vals["MemTotal"] / (1024 * 1024))
		available := vals["MemAvailable"]
		if available == 0 {
			available = vals["MemFree"] + vals["Buffers"] + vals["Cached"]
		}
		used := vals["MemTotal"] - available
		info.MemUsedMB = int(used / (1024 * 1024))
		if vals["MemTotal"] > 0 {
			info.MemPercent = float64(used) / float64(vals["MemTotal"]) * 100
		}
	}

	// Uptime
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		parts := strings.Fields(string(data))
		if len(parts) > 0 {
			secs, _ := strconv.ParseFloat(parts[0], 64)
			d := time.Duration(secs * float64(time.Second))
			days := int(d.Hours()) / 24
			hours := int(d.Hours()) % 24
			mins := int(d.Minutes()) % 60
			if days > 0 {
				info.Uptime = strconv.Itoa(days) + "d " + strconv.Itoa(hours) + "h " + strconv.Itoa(mins) + "m"
			} else {
				info.Uptime = strconv.Itoa(hours) + "h " + strconv.Itoa(mins) + "m"
			}
		}
	}

	// Alpine version
	if data, err := os.ReadFile("/etc/alpine-release"); err == nil {
		info.AlpineVer = strings.TrimSpace(string(data))
	}

	// Device model (common on ARM/mobile devices)
	for _, path := range []string{"/sys/firmware/devicetree/base/model", "/sys/devices/virtual/dmi/id/product_name"} {
		if data, err := os.ReadFile(path); err == nil {
			info.DeviceModel = strings.TrimSpace(strings.ReplaceAll(string(data), "\x00", ""))
			break
		}
	}

	// Battery
	info.Battery, info.Charging = readBattery()

	// Root filesystem storage
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err == nil {
		info.StorageTotalMB = int(stat.Blocks * uint64(stat.Bsize) / (1024 * 1024))
		info.StorageUsedMB = int((stat.Blocks - stat.Bfree) * uint64(stat.Bsize) / (1024 * 1024))
	}

	return info
}

func readBattery() (int, bool) {
	base := "/sys/class/power_supply"
	entries, err := os.ReadDir(base)
	if err != nil {
		return -1, false
	}
	for _, e := range entries {
		typeData, err := os.ReadFile(filepath.Join(base, e.Name(), "type"))
		if err != nil || strings.TrimSpace(string(typeData)) != "Battery" {
			continue
		}
		pct := -1
		if capData, err := os.ReadFile(filepath.Join(base, e.Name(), "capacity")); err == nil {
			pct, _ = strconv.Atoi(strings.TrimSpace(string(capData)))
		}
		charging := false
		if statusData, err := os.ReadFile(filepath.Join(base, e.Name(), "status")); err == nil {
			s := strings.TrimSpace(string(statusData))
			charging = s == "Charging" || s == "Full"
		}
		return pct, charging
	}
	return -1, false
}
