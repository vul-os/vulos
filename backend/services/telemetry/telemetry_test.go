package telemetry

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// SystemStats — struct shape / JSON serialisation
// ---------------------------------------------------------------------------

func TestSystemStats_JSONFieldNames(t *testing.T) {
	s := SystemStats{
		CPU:        42.5,
		MemTotal:   8 * 1024 * 1024 * 1024,
		MemUsed:    2 * 1024 * 1024 * 1024,
		MemPercent: 25.0,
		Temp:       55.3,
		Battery:    80,
		Charging:   true,
		NetRx:      1000,
		NetTx:      500,
		Uptime:     "3h12m",
		Hostname:   "vulos-device",
		NumCPU:     4,
		LoadAvg:    "0.42 0.38 0.31",
		Timestamp:  1716000000000,
	}

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal(SystemStats): %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	requiredKeys := []string{
		"cpu", "mem_total", "mem_used", "mem_percent",
		"temp", "battery", "charging",
		"net_rx", "net_tx", "uptime", "hostname",
		"num_cpu", "load_avg", "timestamp",
	}
	for _, k := range requiredKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("SystemStats JSON missing key %q", k)
		}
	}
}

func TestSystemStats_RoundTrip(t *testing.T) {
	orig := SystemStats{
		CPU:        12.34,
		MemTotal:   4096,
		MemUsed:    1024,
		MemPercent: 25.0,
		Battery:    60,
		Charging:   false,
		NetRx:      9999,
		NetTx:      1111,
		Hostname:   "test-host",
		NumCPU:     2,
		LoadAvg:    "1.00 0.50 0.25",
		Timestamp:  1234567890000,
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got SystemStats
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.CPU != orig.CPU {
		t.Errorf("CPU: want %v, got %v", orig.CPU, got.CPU)
	}
	if got.MemTotal != orig.MemTotal {
		t.Errorf("MemTotal: want %v, got %v", orig.MemTotal, got.MemTotal)
	}
	if got.Hostname != orig.Hostname {
		t.Errorf("Hostname: want %q, got %q", orig.Hostname, got.Hostname)
	}
	if got.Timestamp != orig.Timestamp {
		t.Errorf("Timestamp: want %v, got %v", orig.Timestamp, got.Timestamp)
	}
}

// ---------------------------------------------------------------------------
// parseMeminfo — feed fixture strings, assert parsed map
// ---------------------------------------------------------------------------

func TestParseMeminfo_BasicValues(t *testing.T) {
	fixture := `MemTotal:       16384000 kB
MemFree:         4096000 kB
MemAvailable:    8192000 kB
Buffers:          512000 kB
Cached:          2048000 kB
SwapTotal:       4096000 kB
SwapFree:        4096000 kB
`
	vals := parseMeminfo(fixture)

	// Values are converted from kB to bytes (×1024).
	cases := map[string]uint64{
		"MemTotal":     16384000 * 1024,
		"MemFree":      4096000 * 1024,
		"MemAvailable": 8192000 * 1024,
		"Buffers":      512000 * 1024,
		"Cached":       2048000 * 1024,
	}
	for key, want := range cases {
		if got := vals[key]; got != want {
			t.Errorf("parseMeminfo[%s]: want %d, got %d", key, want, got)
		}
	}
}

func TestParseMeminfo_EmptyInput(t *testing.T) {
	vals := parseMeminfo("")
	if len(vals) != 0 {
		t.Errorf("empty input should yield empty map, got %v", vals)
	}
}

func TestParseMeminfo_MalformedLines(t *testing.T) {
	fixture := `MemTotal:       8192 kB
BadLine
: NoKey 100 kB
MemFree:        2048 kB
`
	vals := parseMeminfo(fixture)
	if vals["MemTotal"] != 8192*1024 {
		t.Errorf("MemTotal: want %d, got %d", 8192*1024, vals["MemTotal"])
	}
	if vals["MemFree"] != 2048*1024 {
		t.Errorf("MemFree: want %d, got %d", 2048*1024, vals["MemFree"])
	}
	// Malformed lines should be silently ignored.
}

// ---------------------------------------------------------------------------
// parseProcStat — feed fixture /proc/[pid]/stat strings
// ---------------------------------------------------------------------------

// buildStatLine creates a synthetic /proc/[pid]/stat line.
// Format: pid (name) state ppid pgrp session tty_nr tpgid flags
//
//	minflt cminflt majflt cmajflt utime stime cutime cstime
//	priority nice num_threads itrealvalue starttime vsize rss ...
//
// Indices in the "rest" slice (fields after the closing paren):
//
//	0=state 1=ppid 2=pgrp 3=session 4=tty_nr 5=tpgid 6=flags
//	7=minflt 8=cminflt 9=majflt 10=cmajflt 11=utime 12=stime
//	13=cutime 14=cstime 15=priority 16=nice 17=num_threads
//	18=itrealvalue 19=starttime 20=vsize 21=rss
func makeStatLine(pid int, name, state string, utime, stime, threads int, starttime, rssPages int) string {
	// Build a line with enough fields after the closing paren (need >=20).
	// We set zeros for all unused fields.
	return statLine(pid, name, state, utime, stime, threads, starttime, rssPages)
}

func statLine(pid int, name, state string, utime, stime, threads, starttime, rssPages int) string {
	// fields[0..21] after closing paren:
	// state ppid pgrp session tty_nr tpgid flags minflt cminflt majflt cmajflt
	// utime stime cutime cstime priority nice num_threads itrealvalue starttime
	// vsize rss
	rest := make([]int, 22)
	rest[0] = 0 // placeholder (state is string, injected separately)
	rest[11] = utime
	rest[12] = stime
	rest[17] = threads
	rest[19] = starttime
	rest[21] = rssPages

	// Build the string manually.
	line := itoa(pid) + " (" + name + ") " + state
	for i := 1; i < 22; i++ {
		line += " " + itoa(rest[i])
	}
	return line
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := ""
	if n < 0 {
		neg = "-"
		n = -n
	}
	buf := make([]byte, 0, 12)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return neg + string(buf)
}

func TestParseProcStat_BasicProcess(t *testing.T) {
	// utime=200, stime=100 ticks; 2 threads; starttime=100 ticks; 10 rss pages
	// systemUptime=10s, clkTck=100 → process age = 10 - 100/100 = 9s
	// CPU = (200+100)/100 / 9 * 100 ≈ 33.3%
	line := makeStatLine(42, "myproc", "S", 200, 100, 2, 100, 10)

	p := parseProcStat(line, 42, 10.0, 100.0, 4*1024*1024*1024)

	if p.PID != 42 {
		t.Errorf("PID: want 42, got %d", p.PID)
	}
	if p.Name != "myproc" {
		t.Errorf("Name: want myproc, got %q", p.Name)
	}
	if p.State != "sleeping" {
		t.Errorf("State: want sleeping, got %q", p.State)
	}
	if p.Threads != 2 {
		t.Errorf("Threads: want 2, got %d", p.Threads)
	}
	if p.CPU <= 0 {
		t.Errorf("CPU should be >0, got %f", p.CPU)
	}
	if p.MemRSS == 0 {
		t.Error("MemRSS should be >0 for 10 rss pages")
	}
	if p.MemPct <= 0 {
		t.Errorf("MemPct should be >0, got %f", p.MemPct)
	}
}

func TestParseProcStat_States(t *testing.T) {
	cases := []struct {
		code string
		want string
	}{
		{"R", "running"},
		{"S", "sleeping"},
		{"D", "disk sleep"},
		{"Z", "zombie"},
		{"T", "stopped"},
		{"I", "idle"},
		{"X", "X"}, // unknown → pass through raw
	}

	for _, tc := range cases {
		line := makeStatLine(1, "proc", tc.code, 0, 0, 1, 0, 0)
		p := parseProcStat(line, 1, 100.0, 100.0, 0)
		if p.State != tc.want {
			t.Errorf("state %q: want %q, got %q", tc.code, tc.want, p.State)
		}
	}
}

func TestParseProcStat_NameWithParens(t *testing.T) {
	// Process names can contain parens themselves, e.g. "(sd-pam)"
	// parseProcStat uses LastIndex(")") so it should handle this.
	line := makeStatLine(99, "proc(foo)", "R", 0, 0, 1, 0, 0)
	p := parseProcStat(line, 99, 100.0, 100.0, 0)
	if p.Name != "proc(foo)" {
		t.Errorf("Name with inner parens: want %q, got %q", "proc(foo)", p.Name)
	}
}

func TestParseProcStat_TooShortReturnsEmpty(t *testing.T) {
	p := parseProcStat("123 (short) R 0", 123, 10.0, 100.0, 0)
	if p.Name != "short" {
		t.Errorf("name should be extracted even when rest is short, got %q", p.Name)
	}
	// CPU / threads / rss should be zero (rest < 20 fields)
	if p.CPU != 0 {
		t.Errorf("CPU should be 0 for short line, got %f", p.CPU)
	}
}

func TestParseProcStat_MissingParenReturnsBlankName(t *testing.T) {
	p := parseProcStat("no parens here at all", 1, 10.0, 100.0, 0)
	if p.Name != "" {
		t.Errorf("Name should be empty when no parens found, got %q", p.Name)
	}
}

// ---------------------------------------------------------------------------
// parseMeminfo — memory calculation logic (used by collect + SystemInfo)
// ---------------------------------------------------------------------------

func TestParseMeminfo_MemUsedCalculation(t *testing.T) {
	fixture := `MemTotal:       4096 kB
MemAvailable:   2048 kB
MemFree:         512 kB
Buffers:         256 kB
Cached:          512 kB
`
	vals := parseMeminfo(fixture)
	total := vals["MemTotal"]
	avail := vals["MemAvailable"]
	used := total - avail

	wantTotal := uint64(4096 * 1024)
	wantUsed := uint64(2048 * 1024)

	if total != wantTotal {
		t.Errorf("MemTotal: want %d, got %d", wantTotal, total)
	}
	if used != wantUsed {
		t.Errorf("MemUsed (Total-Available): want %d, got %d", wantUsed, used)
	}
}

// ---------------------------------------------------------------------------
// SysInfo struct — JSON field names
// ---------------------------------------------------------------------------

func TestSysInfo_JSONFieldNames(t *testing.T) {
	si := SysInfo{
		Hostname:       "vulos",
		Kernel:         "6.1.0",
		Arch:           "arm64",
		CPUModel:       "Cortex-A55",
		CPUCores:       8,
		MemTotalMB:     4096,
		MemUsedMB:      1024,
		MemPercent:     25.0,
		Uptime:         "1d 2h 30m",
		OSName:         "Vula OS",
		OSVersion:      "1.0",
		Battery:        75,
		Charging:       false,
		StorageTotalMB: 32768,
		StorageUsedMB:  8192,
	}

	data, err := json.Marshal(si)
	if err != nil {
		t.Fatalf("json.Marshal(SysInfo): %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	required := []string{
		"hostname", "kernel", "arch", "cpu_model", "cpu_cores",
		"mem_total_mb", "mem_used_mb", "mem_percent",
		"uptime", "os_name", "os_version",
		"battery", "charging",
		"storage_total_mb", "storage_used_mb",
		"gpu_vendor", "gpu_device", "gpu_tier",
		"gpu_encoder", "gpu_codec", "gpu_av1", "gpu_pipewire",
	}
	for _, k := range required {
		if _, ok := m[k]; !ok {
			t.Errorf("SysInfo JSON missing key %q", k)
		}
	}
}

// ---------------------------------------------------------------------------
// ProcessInfo / NetConn structs — JSON field names
// ---------------------------------------------------------------------------

func TestProcessInfo_JSONFieldNames(t *testing.T) {
	p := ProcessInfo{
		PID:     1234,
		Name:    "myapp",
		User:    "root",
		State:   "running",
		CPU:     1.5,
		MemRSS:  1048576,
		MemPct:  0.25,
		Threads: 4,
		Command: "myapp --foo",
	}

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("json.Marshal(ProcessInfo): %v", err)
	}
	var m map[string]interface{}
	json.Unmarshal(data, &m)

	for _, k := range []string{"pid", "name", "user", "state", "cpu", "mem_rss", "mem_pct", "threads", "command"} {
		if _, ok := m[k]; !ok {
			t.Errorf("ProcessInfo JSON missing key %q", k)
		}
	}
}

func TestNetConn_JSONFieldNames(t *testing.T) {
	nc := NetConn{
		Proto:      "tcp",
		LocalAddr:  "127.0.0.1",
		LocalPort:  8080,
		RemoteAddr: "192.168.1.1",
		RemotePort: 443,
		State:      "ESTABLISHED",
		PID:        999,
		Process:    "myapp (999)",
	}

	data, err := json.Marshal(nc)
	if err != nil {
		t.Fatalf("json.Marshal(NetConn): %v", err)
	}
	var m map[string]interface{}
	json.Unmarshal(data, &m)

	for _, k := range []string{"proto", "local_addr", "local_port", "remote_addr", "remote_port", "state", "pid", "process"} {
		if _, ok := m[k]; !ok {
			t.Errorf("NetConn JSON missing key %q", k)
		}
	}
}
