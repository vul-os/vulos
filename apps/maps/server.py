"""Vula OS — Maps
Serves the Maps app with OSM tiles, Nominatim search, OSRM routing and
favourites persisted via /api/appdata.
"""
import http.server
import json
import os
import urllib.request
import urllib.error
import urllib.parse

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
VULOS_API = os.environ.get("VULOS_API", "http://localhost:8080")
APP_DIR = os.path.dirname(os.path.abspath(__file__))

NOMINATIM_BASE = "https://nominatim.openstreetmap.org"
OSRM_BASE = "https://router.project-osrm.org"

CONTENT_TYPES = {
    ".html": "text/html; charset=utf-8",
    ".css":  "text/css",
    ".js":   "application/javascript",
    ".json": "application/json",
    ".svg":  "image/svg+xml",
    ".png":  "image/png",
    ".ico":  "image/x-icon",
}

FAVOURITES_PATH = "favourites.json"


def appdata_url(rel_path):
    return f"{VULOS_API}/api/appdata/maps/{rel_path}"


def load_favourites():
    try:
        req = urllib.request.Request(appdata_url(FAVOURITES_PATH))
        with urllib.request.urlopen(req, timeout=3) as resp:
            return json.loads(resp.read().decode())
    except Exception:
        return []


def save_favourites(data):
    body = json.dumps(data).encode()
    req = urllib.request.Request(
        appdata_url(FAVOURITES_PATH),
        data=body,
        method="PUT",
        headers={"Content-Type": "application/json", "Content-Length": str(len(body))},
    )
    try:
        urllib.request.urlopen(req, timeout=3)
        return True
    except Exception:
        return False


def proxy_get(url, extra_headers=None):
    """Fetch a URL and return (status, headers_dict, body_bytes)."""
    req = urllib.request.Request(url, headers={"User-Agent": "VulaOS-Maps/0.1"})
    if extra_headers:
        for k, v in extra_headers.items():
            req.add_header(k, v)
    try:
        with urllib.request.urlopen(req, timeout=8) as resp:
            return resp.status, dict(resp.headers), resp.read()
    except urllib.error.HTTPError as e:
        return e.code, {}, b""
    except Exception:
        return 502, {}, b""


class MapsHandler(http.server.BaseHTTPRequestHandler):
    def do_OPTIONS(self):
        self.send_response(204)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Content-Type")
        self.end_headers()

    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        qs = urllib.parse.parse_qs(parsed.query)

        # ── Static files ──────────────────────────────────────────────────────
        if path == "/" or path == "/index.html":
            self.serve_file(os.path.join(APP_DIR, "index.html"), "text/html; charset=utf-8")
            return

        if path.startswith("/vendor/"):
            rel = path.lstrip("/")
            full = os.path.join(APP_DIR, *rel.split("/"))
            ext = os.path.splitext(full)[1].lower()
            ct = CONTENT_TYPES.get(ext, "application/octet-stream")
            self.serve_file(full, ct)
            return

        # ── Favourites ────────────────────────────────────────────────────────
        if path == "/api/favourites":
            self.send_json(load_favourites())
            return

        # ── Nominatim search proxy ────────────────────────────────────────────
        if path == "/api/search":
            q = qs.get("q", [""])[0]
            if not q:
                self.send_json([])
                return
            url = (
                NOMINATIM_BASE
                + "/search?format=json&limit=8&addressdetails=1&q="
                + urllib.parse.quote(q)
            )
            status, _, body = proxy_get(url)
            if status == 200:
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Access-Control-Allow-Origin", "*")
                self.end_headers()
                self.wfile.write(body)
            else:
                self.send_json([])
            return

        # ── OSRM routing proxy ────────────────────────────────────────────────
        if path == "/api/route":
            # ?profile=driving&from_lat=&from_lon=&to_lat=&to_lon=
            profile  = qs.get("profile", ["driving"])[0]
            from_lat = qs.get("from_lat", [""])[0]
            from_lon = qs.get("from_lon", [""])[0]
            to_lat   = qs.get("to_lat",   [""])[0]
            to_lon   = qs.get("to_lon",   [""])[0]
            if not all([from_lat, from_lon, to_lat, to_lon]):
                self.send_error_json(400, "Missing coordinates")
                return
            coords = f"{from_lon},{from_lat};{to_lon},{to_lat}"
            url = (
                f"{OSRM_BASE}/route/v1/{profile}/{coords}"
                "?overview=full&geometries=geojson&steps=true"
            )
            status, _, body = proxy_get(url)
            if status == 200:
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Access-Control-Allow-Origin", "*")
                self.end_headers()
                self.wfile.write(body)
            else:
                self.send_error_json(502, "Routing service unavailable")
            return

        self.send_error(404)

    def do_POST(self):
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        length = int(self.headers.get("Content-Length", 0))
        body_bytes = self.rfile.read(length) if length else b""

        if path == "/api/favourites":
            try:
                fav = json.loads(body_bytes.decode())
            except Exception:
                self.send_error_json(400, "Invalid JSON")
                return
            favs = load_favourites()
            # Avoid duplicates by name+coords
            for f in favs:
                if f.get("name") == fav.get("name") and f.get("lat") == fav.get("lat"):
                    self.send_json({"status": "exists"})
                    return
            favs.append(fav)
            save_favourites(favs)
            self.send_json({"status": "saved", "count": len(favs)})
            return

        self.send_error(404)

    def do_DELETE(self):
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path

        if path.startswith("/api/favourites/"):
            try:
                idx = int(path.split("/api/favourites/")[1])
            except ValueError:
                self.send_error_json(400, "Invalid index")
                return
            favs = load_favourites()
            if 0 <= idx < len(favs):
                favs.pop(idx)
                save_favourites(favs)
            self.send_json({"status": "deleted", "count": len(favs)})
            return

        self.send_error(404)

    # ── Helpers ────────────────────────────────────────────────────────────────

    def serve_file(self, filepath, content_type):
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

    def send_json(self, data):
        body = json.dumps(data).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
        self.wfile.write(body)

    def send_error_json(self, code, message):
        body = json.dumps({"error": message}).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        pass


print(f"[maps] Maps on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), MapsHandler).serve_forever()
