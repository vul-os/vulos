"""Vula OS - Calendar
Serves the Calendar web app and proxies event persistence to the
main Vula OS backend via /api/appdata/calendar/events.json.
"""
import http.server
import json
import os
import urllib.request
import urllib.error

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
VULOS_API = os.environ.get("VULOS_API", "http://localhost:8080")
APP_DIR = os.path.dirname(os.path.abspath(__file__))

EVENTS_URL = f"{VULOS_API}/api/appdata/calendar/events.json"


def _appdata_get():
    """Fetch events.json from the appdata API. Returns (data_bytes, status)."""
    try:
        with urllib.request.urlopen(EVENTS_URL, timeout=5) as resp:
            return resp.read(), resp.status
    except urllib.error.HTTPError as e:
        if e.code == 404:
            return b"[]", 200
        return None, e.code
    except Exception:
        return None, 502


def _appdata_put(body: bytes):
    """Write events.json to the appdata API. Returns (response_bytes, status)."""
    req = urllib.request.Request(
        EVENTS_URL,
        data=body,
        method="PUT",
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return resp.read(), resp.status
    except urllib.error.HTTPError as e:
        return None, e.code
    except Exception:
        return None, 502


class CalendarHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path in ("/", "/index.html"):
            self.serve_file("index.html", "text/html; charset=utf-8")
        elif self.path == "/icon.svg":
            self.serve_file("icon.svg", "image/svg+xml")
        elif self.path == "/api/events":
            data, status = _appdata_get()
            if data is None:
                self.send_error(status or 502)
                return
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        else:
            self.send_error(404)

    def do_PUT(self):
        if self.path == "/api/events":
            length = int(self.headers.get("Content-Length", 0))
            body = self.rfile.read(length) if length else b"[]"
            # Validate JSON before forwarding
            try:
                json.loads(body)
            except ValueError:
                self.send_error(400, "Bad JSON")
                return
            resp_data, status = _appdata_put(body)
            if resp_data is None:
                self.send_error(status or 502)
                return
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(resp_data)))
            self.end_headers()
            self.wfile.write(resp_data)
        else:
            self.send_error(404)

    def do_OPTIONS(self):
        self.send_response(204)
        self.end_headers()

    def serve_file(self, filename, content_type):
        try:
            with open(os.path.join(APP_DIR, filename), "rb") as f:
                data = f.read()
            self.send_response(200)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        except FileNotFoundError:
            self.send_error(404)

    def log_message(self, format, *args):
        pass


print(f"[calendar] Calendar on port {PORT} (appdata backend: {VULOS_API})")
http.server.HTTPServer(("0.0.0.0", PORT), CalendarHandler).serve_forever()
