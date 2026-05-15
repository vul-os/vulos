"""Vula OS — System Info app.
Read-only dashboard: polls existing backend endpoints and /proc//sys directly.
"""
import http.server
import json
import os
import platform
import socket
import time
import urllib.request
import urllib.error

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
VULOS_API = os.environ.get("VULOS_API", "http://localhost:8080")
APP_DIR = os.path.dirname(os.path.abspath(__file__))


def _fetch(path, timeout=3):
    """Fetch JSON from the Vulos backend. Returns parsed dict/list or None."""
    try:
        with urllib.request.urlopen(VULOS_API + path, timeout=timeout) as resp:
            return json.loads(resp.read())
    except Exception:
        return None


# ---------------------------------------------------------------------------
# Data collectors — use backend endpoints first, fall back to /proc / /sys
# ---------------------------------------------------------------------------

def get_system_info():
    """Pull the one-shot system info snapshot from the backend."""
    data = _fetch("/api/system/info")
    if data:
        return data

    # Fallback: read directly from /proc and /sys
    info = {}

    # Hostname
    try:
        info["hostname"] = socket.gethostname()
    except Exception:
        info["hostname"] = "unknown"

    # Arch / OS
    info["arch"] = platform.machine()
    info["cpu_cores"] = os.cpu_count() or 0

    # Kernel
    try:
        with open("/proc/version") as f:
            parts = f.read().split()
            info["kernel"] = parts[2] if len(parts) >= 3 else "unknown"
    except Exception:
        info["kernel"] = platform.release()

    # CPU model
    info["cpu_model"] = ""
    try:
        with open("/proc/cpuinfo") as f:
            for line in f:
                if line.startswith("model name") or line.startswith("Model"):
                    info["cpu_model"] = line.split(":", 1)[1].strip()
                    break
    except Exception:
        pass

    # Memory
    info["mem_total_mb"] = 0
    info["mem_used_mb"] = 0
    info["mem_percent"] = 0.0
    try:
        vals = {}
        with open("/proc/meminfo") as f:
            for line in f:
                parts = line.split()
                if len(parts) >= 2:
                    vals[parts[0].rstrip(":")] = int(parts[1]) * 1024
        total = vals.get("MemTotal", 0)
        available = vals.get("MemAvailable", 0) or (
            vals.get("MemFree", 0) + vals.get("Buffers", 0) + vals.get("Cached", 0)
        )
        used = total - available
        info["mem_total_mb"] = total // (1024 * 1024)
        info["mem_used_mb"] = used // (1024 * 1024)
        if total:
            info["mem_percent"] = round(used / total * 100, 1)
    except Exception:
        pass

    # Uptime
    info["uptime"] = ""
    try:
        with open("/proc/uptime") as f:
            secs = float(f.read().split()[0])
        days = int(secs // 86400)
        hours = int((secs % 86400) // 3600)
        mins = int((secs % 3600) // 60)
        if days:
            info["uptime"] = f"{days}d {hours}h {mins}m"
        else:
            info["uptime"] = f"{hours}h {mins}m"
    except Exception:
        pass

    # OS release
    info["os_name"] = ""
    info["os_version"] = ""
    try:
        with open("/etc/os-release") as f:
            for line in f:
                k, _, v = line.partition("=")
                v = v.strip().strip('"\'')
                if k == "PRETTY_NAME":
                    info["os_name"] = v
                elif k == "VERSION_ID":
                    info["os_version"] = v
    except Exception:
        pass

    # Device model
    info["device_model"] = ""
    for path in [
        "/sys/firmware/devicetree/base/model",
        "/sys/devices/virtual/dmi/id/product_name",
    ]:
        try:
            with open(path) as f:
                info["device_model"] = f.read().replace("\x00", "").strip()
            break
        except Exception:
            pass

    # Battery
    info["battery"] = -1
    info["charging"] = False
    try:
        base = "/sys/class/power_supply"
        for entry in os.listdir(base):
            type_path = os.path.join(base, entry, "type")
            try:
                with open(type_path) as f:
                    if f.read().strip() != "Battery":
                        continue
            except Exception:
                continue
            try:
                with open(os.path.join(base, entry, "capacity")) as f:
                    info["battery"] = int(f.read().strip())
            except Exception:
                pass
            try:
                with open(os.path.join(base, entry, "status")) as f:
                    s = f.read().strip()
                    info["charging"] = s in ("Charging", "Full")
            except Exception:
                pass
            break
    except Exception:
        pass

    # Storage (root FS)
    info["storage_total_mb"] = 0
    info["storage_used_mb"] = 0
    try:
        st = os.statvfs("/")
        total = st.f_blocks * st.f_frsize
        free = st.f_bfree * st.f_frsize
        used = total - free
        info["storage_total_mb"] = total // (1024 * 1024)
        info["storage_used_mb"] = used // (1024 * 1024)
    except Exception:
        pass

    # GPU — no backend, no /proc path, leave blank
    info["gpu_vendor"] = ""
    info["gpu_device"] = ""
    info["gpu_tier"] = ""

    return info


def get_disks():
    """Return disk mount list from backend, or statvfs fallback."""
    data = _fetch("/api/disks")
    if data:
        return data.get("mounts", [])

    # Fallback: parse /proc/mounts
    mounts = []
    try:
        with open("/proc/mounts") as f:
            for line in f:
                parts = line.split()
                if len(parts) < 4:
                    continue
                device, mount_point, fs_type = parts[0], parts[1], parts[2]
                if fs_type in ("proc", "sysfs", "devtmpfs", "devpts", "tmpfs",
                               "cgroup", "cgroup2", "pstore", "debugfs",
                               "securityfs", "hugetlbfs", "mqueue",
                               "fusectl", "configfs", "binfmt_misc",
                               "overlay", "nsfs"):
                    continue
                if not device.startswith("/dev/") and device not in ("overlay",):
                    continue
                try:
                    st = os.statvfs(mount_point)
                    total = st.f_blocks * st.f_frsize
                    free = st.f_bfree * st.f_frsize
                    used = total - free
                    pct = round(used / total * 100, 1) if total else 0
                    mounts.append({
                        "device": device,
                        "mount_point": mount_point,
                        "fs_type": fs_type,
                        "total_mb": total // (1024 * 1024),
                        "used_mb": used // (1024 * 1024),
                        "free_mb": free // (1024 * 1024),
                        "percent": pct,
                    })
                except Exception:
                    pass
    except Exception:
        pass
    return mounts


def get_network_interfaces():
    """Return live network interfaces with IPs from /proc/net/if_inet6 + /proc/net/fib_trie."""
    ifaces = {}

    # Read interface stats from /proc/net/dev
    try:
        with open("/proc/net/dev") as f:
            for line in f:
                line = line.strip()
                if ":" not in line:
                    continue
                name, _, rest = line.partition(":")
                name = name.strip()
                fields = rest.split()
                if len(fields) < 10:
                    continue
                ifaces[name] = {
                    "name": name,
                    "rx_bytes": int(fields[0]),
                    "tx_bytes": int(fields[8]),
                    "addresses": [],
                }
    except Exception:
        pass

    # Collect IPv4 addresses via ip command or /proc/net/fib_trie
    try:
        import subprocess
        result = subprocess.run(
            ["ip", "-o", "addr", "show"],
            capture_output=True, text=True, timeout=3
        )
        for line in result.stdout.splitlines():
            parts = line.split()
            if len(parts) < 4:
                continue
            iface = parts[1]
            family = parts[2]
            addr = parts[3].split("/")[0]
            if iface in ifaces:
                ifaces[iface]["addresses"].append({"family": family, "address": addr})
            elif iface:
                ifaces[iface] = {"name": iface, "rx_bytes": 0, "tx_bytes": 0,
                                 "addresses": [{"family": family, "address": addr}]}
    except Exception:
        # Fallback: read /proc/net/if_inet6 for IPv6
        try:
            with open("/proc/net/if_inet6") as f:
                for line in f:
                    parts = line.split()
                    if len(parts) < 6:
                        continue
                    hex_addr = parts[0]
                    name = parts[5]
                    # convert 32-char hex to IPv6
                    groups = [hex_addr[i:i+4] for i in range(0, 32, 4)]
                    addr = ":".join(groups)
                    if name in ifaces:
                        ifaces[name]["addresses"].append({"family": "inet6", "address": addr})
        except Exception:
            pass

    # Filter out loopback for display but keep it if it has an address
    result = [v for k, v in ifaces.items() if k != "lo"]
    return result


def get_telemetry_snapshot():
    """Read live CPU/RAM/uptime from /proc (not from backend WebSocket)."""
    snapshot = {
        "cpu": 0.0,
        "mem_total": 0,
        "mem_used": 0,
        "mem_percent": 0.0,
        "uptime": "",
        "load_avg": "",
    }

    # CPU — two-sample method
    def read_cpu():
        try:
            with open("/proc/stat") as f:
                line = f.readline()
            fields = line.split()[1:]
            total = sum(int(x) for x in fields)
            idle = int(fields[3])
            return total, idle
        except Exception:
            return 0, 0

    t1, i1 = read_cpu()
    time.sleep(0.15)
    t2, i2 = read_cpu()
    dt = t2 - t1
    if dt > 0:
        snapshot["cpu"] = round((dt - (i2 - i1)) / dt * 100, 1)

    # Memory
    try:
        vals = {}
        with open("/proc/meminfo") as f:
            for line in f:
                parts = line.split()
                if len(parts) >= 2:
                    vals[parts[0].rstrip(":")] = int(parts[1]) * 1024
        total = vals.get("MemTotal", 0)
        available = vals.get("MemAvailable", 0) or (
            vals.get("MemFree", 0) + vals.get("Buffers", 0) + vals.get("Cached", 0)
        )
        used = total - available
        snapshot["mem_total"] = total
        snapshot["mem_used"] = used
        if total:
            snapshot["mem_percent"] = round(used / total * 100, 1)
    except Exception:
        pass

    # Uptime
    try:
        with open("/proc/uptime") as f:
            secs = float(f.read().split()[0])
        days = int(secs // 86400)
        hours = int((secs % 86400) // 3600)
        mins = int((secs % 3600) // 60)
        snapshot["uptime"] = f"{days}d {hours}h {mins}m" if days else f"{hours}h {mins}m"
    except Exception:
        pass

    # Load average
    try:
        with open("/proc/loadavg") as f:
            parts = f.read().split()
            if len(parts) >= 3:
                snapshot["load_avg"] = f"{parts[0]} {parts[1]} {parts[2]}"
    except Exception:
        pass

    return snapshot


# ---------------------------------------------------------------------------
# HTTP handler
# ---------------------------------------------------------------------------

class SystemInfoHandler(http.server.BaseHTTPRequestHandler):

    def do_GET(self):
        if self.path == "/":
            self._serve_file(os.path.join(APP_DIR, "index.html"), "text/html")
        elif self.path == "/icon.svg":
            self._serve_file(os.path.join(APP_DIR, "icon.svg"), "image/svg+xml")
        elif self.path == "/api/info":
            self._send_json(get_system_info())
        elif self.path == "/api/disks":
            self._send_json(get_disks())
        elif self.path == "/api/network":
            self._send_json(get_network_interfaces())
        elif self.path == "/api/live":
            self._send_json(get_telemetry_snapshot())
        else:
            self.send_error(404)

    def _serve_file(self, filepath, content_type):
        try:
            with open(filepath, "rb") as f:
                data = f.read()
            self.send_response(200)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        except FileNotFoundError:
            self.send_error(404)

    def _send_json(self, data):
        body = json.dumps(data).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        pass  # suppress per-request logs


if __name__ == "__main__":
    print(f"[system-info] System Info on port {PORT}")
    http.server.HTTPServer(("0.0.0.0", PORT), SystemInfoHandler).serve_forever()
