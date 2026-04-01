package telemetry

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"vulos/backend/internal/wsutil"
	"vulos/backend/services/gpu"

	"github.com/gorilla/websocket"
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

// Handler returns an HTTP handler that upgrades to WebSocket and streams system telemetry.
// Connect via: ws://host:port/api/telemetry
func Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ws, err := wsutil.Upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[telemetry] websocket upgrade: %v", err)
			return
		}
		defer ws.Close()

		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		log.Printf("[telemetry] client connected")

		// Send initial stats immediately
		if err := sendStats(ws, collect()); err != nil {
			return
		}

		for {
			select {
			case <-ticker.C:
				if err := sendStats(ws, collect()); err != nil {
					log.Printf("[telemetry] client disconnected")
					return
				}
			}
		}
	}
}

func sendStats(ws *websocket.Conn, stats SystemStats) error {
	data, _ := json.Marshal(stats)
	return ws.WriteMessage(websocket.TextMessage, data)
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
	OSName       string `json:"os_name"`
	OSVersion    string `json:"os_version"`
	DeviceModel  string `json:"device_model"`
	Battery      int    `json:"battery"`
	Charging     bool   `json:"charging"`
	StorageTotalMB int    `json:"storage_total_mb"`
	StorageUsedMB  int    `json:"storage_used_mb"`
	GPUVendor      string `json:"gpu_vendor"`
	GPUDevice      string `json:"gpu_device"`
	GPUTier        string `json:"gpu_tier"`
	GPUEncoder     string `json:"gpu_encoder"`
	GPUCodec       string `json:"gpu_codec"`
	GPUAV1         bool   `json:"gpu_av1"`
	GPUPipeWire    bool   `json:"gpu_pipewire"`
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

	// OS version via /etc/os-release
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			v = strings.Trim(v, `"'`)
			switch k {
			case "PRETTY_NAME":
				info.OSName = v
			case "VERSION_ID":
				info.OSVersion = v
			}
		}
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

	// GPU
	g := gpu.Detect()
	info.GPUVendor = string(g.Vendor)
	info.GPUDevice = g.Device
	info.GPUTier = g.TierName
	info.GPUEncoder = g.Encoder
	info.GPUCodec = g.Codec
	info.GPUAV1 = g.HasAV1
	info.GPUPipeWire = g.HasPipeWire

	return info
}

// ProcessInfo represents a single running process.
type ProcessInfo struct {
	PID     int     `json:"pid"`
	Name    string  `json:"name"`
	User    string  `json:"user"`
	State   string  `json:"state"`
	CPU     float64 `json:"cpu"`
	MemRSS  uint64  `json:"mem_rss"`  // bytes
	MemPct  float64 `json:"mem_pct"`
	Threads int     `json:"threads"`
	Command string  `json:"command"`
}

// NetConn represents a network connection.
type NetConn struct {
	Proto      string `json:"proto"`
	LocalAddr  string `json:"local_addr"`
	LocalPort  int    `json:"local_port"`
	RemoteAddr string `json:"remote_addr"`
	RemotePort int    `json:"remote_port"`
	State      string `json:"state"`
	PID        int    `json:"pid"`
	Process    string `json:"process"`
}

// ProcessList reads all processes from /proc and returns them sorted by CPU desc.
func ProcessList() []ProcessInfo {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}

	// Get total memory for percentage calc
	var memTotal uint64
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		vals := parseMeminfo(string(data))
		memTotal = vals["MemTotal"]
	}

	// Get system uptime and clock ticks for CPU calc
	clkTck := 100.0 // sysconf(_SC_CLK_TCK), almost always 100 on Linux
	var systemUptime float64
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		parts := strings.Fields(string(data))
		if len(parts) > 0 {
			systemUptime, _ = strconv.ParseFloat(parts[0], 64)
		}
	}

	var procs []ProcessInfo

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}

		statPath := filepath.Join("/proc", e.Name(), "stat")
		statData, err := os.ReadFile(statPath)
		if err != nil {
			continue
		}

		p := parseProcStat(string(statData), pid, systemUptime, clkTck, memTotal)
		if p.Name == "" {
			continue
		}

		// Read cmdline for full command
		if cmdData, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline")); err == nil {
			cmd := strings.ReplaceAll(string(cmdData), "\x00", " ")
			cmd = strings.TrimSpace(cmd)
			if cmd != "" {
				p.Command = cmd
			}
		}
		if p.Command == "" {
			p.Command = p.Name
		}

		// Read user from status file
		if statusData, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status")); err == nil {
			for _, line := range strings.Split(string(statusData), "\n") {
				if strings.HasPrefix(line, "Uid:") {
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						p.User = uidToName(fields[1])
					}
					break
				}
			}
		}

		procs = append(procs, p)
	}

	sort.Slice(procs, func(i, j int) bool {
		return procs[i].CPU > procs[j].CPU
	})

	return procs
}

func parseProcStat(data string, pid int, systemUptime, clkTck float64, memTotal uint64) ProcessInfo {
	// /proc/[pid]/stat format: pid (comm) state ppid pgrp session tty_nr tpgid flags
	// minflt cminflt majflt cmajflt utime stime cutime cstime priority nice num_threads ...
	// Field indices (0-based after splitting):
	// comm is in parens, so find last ')' to split correctly
	p := ProcessInfo{PID: pid}

	lastParen := strings.LastIndex(data, ")")
	if lastParen < 0 || lastParen+2 >= len(data) {
		return p
	}

	// Extract name from between parens
	firstParen := strings.Index(data, "(")
	if firstParen >= 0 && lastParen > firstParen {
		p.Name = data[firstParen+1 : lastParen]
	}

	// Fields after ")"
	rest := strings.Fields(data[lastParen+2:])
	if len(rest) < 20 {
		return p
	}

	// State
	switch rest[0] {
	case "R":
		p.State = "running"
	case "S":
		p.State = "sleeping"
	case "D":
		p.State = "disk sleep"
	case "Z":
		p.State = "zombie"
	case "T":
		p.State = "stopped"
	case "I":
		p.State = "idle"
	default:
		p.State = rest[0]
	}

	// utime(13) + stime(14) — indices in rest are 11 and 12
	utime, _ := strconv.ParseFloat(rest[11], 64)
	stime, _ := strconv.ParseFloat(rest[12], 64)
	starttime, _ := strconv.ParseFloat(rest[19], 64)

	totalTime := utime + stime
	seconds := systemUptime - (starttime / clkTck)
	if seconds > 0 {
		p.CPU = (totalTime / clkTck / seconds) * 100
	}

	// Threads — index 17 in rest
	p.Threads, _ = strconv.Atoi(rest[17])

	// RSS — index 21 in rest (pages)
	rssPages, _ := strconv.ParseUint(rest[21], 10, 64)
	pageSize := uint64(os.Getpagesize())
	p.MemRSS = rssPages * pageSize
	if memTotal > 0 {
		p.MemPct = float64(p.MemRSS) / float64(memTotal) * 100
	}

	return p
}

// NetworkConnections reads /proc/net/tcp and /proc/net/tcp6 for active connections.
func NetworkConnections() []NetConn {
	var conns []NetConn
	conns = append(conns, parseNetFile("/proc/net/tcp", "tcp")...)
	conns = append(conns, parseNetFile("/proc/net/tcp6", "tcp6")...)
	conns = append(conns, parseNetFile("/proc/net/udp", "udp")...)
	conns = append(conns, parseNetFile("/proc/net/udp6", "udp6")...)

	// Build pid->name map for inode lookup
	inodePID := buildInodePIDMap()

	for i := range conns {
		if name, ok := inodePID[conns[i].PID]; ok {
			conns[i].Process = name
		}
	}

	return conns
}

func parseNetFile(path, proto string) []NetConn {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	var conns []NetConn

	tcpStates := map[string]string{
		"01": "ESTABLISHED", "02": "SYN_SENT", "03": "SYN_RECV",
		"04": "FIN_WAIT1", "05": "FIN_WAIT2", "06": "TIME_WAIT",
		"07": "CLOSE", "08": "CLOSE_WAIT", "09": "LAST_ACK",
		"0A": "LISTEN", "0B": "CLOSING",
	}

	for _, line := range lines[1:] { // skip header
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}

		localAddr, localPort := parseHexAddr(fields[1], strings.Contains(proto, "6"))
		remoteAddr, remotePort := parseHexAddr(fields[2], strings.Contains(proto, "6"))

		state := fields[3]
		if s, ok := tcpStates[state]; ok {
			state = s
		}

		inode, _ := strconv.Atoi(fields[9])

		conns = append(conns, NetConn{
			Proto:      proto,
			LocalAddr:  localAddr,
			LocalPort:  localPort,
			RemoteAddr: remoteAddr,
			RemotePort: remotePort,
			State:      state,
			PID:        inode, // temporarily store inode, resolve to PID later
		})
	}
	return conns
}

func parseHexAddr(s string, isV6 bool) (string, int) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return "", 0
	}
	port, _ := strconv.ParseInt(parts[1], 16, 32)

	if isV6 {
		// IPv6 — 32 hex chars
		hex := parts[0]
		if len(hex) == 32 {
			// Check if it's an IPv4-mapped address (::ffff:x.x.x.x)
			if hex[:24] == "0000000000000000FFFF0000" || hex[:24] == "000000000000000000000000" {
				ipHex := hex[24:]
				if len(ipHex) == 8 {
					a, _ := strconv.ParseUint(ipHex[6:8], 16, 8)
					b, _ := strconv.ParseUint(ipHex[4:6], 16, 8)
					c, _ := strconv.ParseUint(ipHex[2:4], 16, 8)
					d, _ := strconv.ParseUint(ipHex[0:2], 16, 8)
					return fmt.Sprintf("%d.%d.%d.%d", a, b, c, d), int(port)
				}
			}
			return "[::]", int(port)
		}
		return hex, int(port)
	}

	// IPv4 — stored as little-endian hex
	hex := parts[0]
	if len(hex) == 8 {
		a, _ := strconv.ParseUint(hex[6:8], 16, 8)
		b, _ := strconv.ParseUint(hex[4:6], 16, 8)
		c, _ := strconv.ParseUint(hex[2:4], 16, 8)
		d, _ := strconv.ParseUint(hex[0:2], 16, 8)
		return fmt.Sprintf("%d.%d.%d.%d", a, b, c, d), int(port)
	}
	return hex, int(port)
}

func buildInodePIDMap() map[int]string {
	result := make(map[int]string)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return result
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fdPath := filepath.Join("/proc", e.Name(), "fd")
		fds, err := os.ReadDir(fdPath)
		if err != nil {
			continue
		}
		var name string
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdPath, fd.Name()))
			if err != nil {
				continue
			}
			if strings.HasPrefix(link, "socket:[") {
				inode, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]"))
				if inode > 0 {
					if name == "" {
						// Read process name
						if comm, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm")); err == nil {
							name = strings.TrimSpace(string(comm))
						} else {
							name = strconv.Itoa(pid)
						}
					}
					result[inode] = fmt.Sprintf("%s (%d)", name, pid)
				}
			}
		}
	}
	return result
}

func uidToName(uid string) string {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return uid
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 4)
		if len(parts) >= 3 && parts[2] == uid {
			return parts[0]
		}
	}
	return uid
}

// ProcessHandler returns an HTTP handler for /api/system/processes
func ProcessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, _ := json.Marshal(ProcessList())
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}
}

// NetworkHandler returns an HTTP handler for /api/system/network
func NetworkHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, _ := json.Marshal(NetworkConnections())
		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	}
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
