"""Vula OS — Screenshot & Screen Capture
Save screenshots and screen recordings to ~/.vulos/screenshots/.
Supports: getDisplayMedia screenshots, .webm screen recording, canvas annotation,
region crop post-capture, save to disk, clipboard copy.
"""
import http.server
import json
import os
import time
import base64
from datetime import datetime
from urllib.parse import urlparse, parse_qs

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
APP_DIR = os.path.dirname(os.path.abspath(__file__))
SCREENSHOTS_DIR = os.path.expanduser("~/.vulos/screenshots")
os.makedirs(SCREENSHOTS_DIR, exist_ok=True)


def ts_filename(ext):
    """Generate a timestamped filename."""
    ts = datetime.now().strftime("%Y-%m-%d_%H-%M-%S")
    return f"screenshot_{ts}{ext}"


def list_screenshots():
    items = []
    for fname in sorted(os.listdir(SCREENSHOTS_DIR), reverse=True):
        if not (fname.endswith(".png") or fname.endswith(".webm")):
            continue
        path = os.path.join(SCREENSHOTS_DIR, fname)
        stat = os.stat(path)
        items.append({
            "name": fname,
            "size": stat.st_size,
            "modified": stat.st_mtime,
            "type": "video" if fname.endswith(".webm") else "image",
        })
    return items


def save_screenshot(data_url):
    """Decode a base64 data URL (image/png) and save to disk."""
    if not data_url.startswith("data:image/"):
        raise ValueError("Expected image data URL")
    header, b64 = data_url.split(",", 1)
    raw = base64.b64decode(b64)
    fname = ts_filename(".png")
    path = os.path.join(SCREENSHOTS_DIR, fname)
    with open(path, "wb") as f:
        f.write(raw)
    return fname


def save_recording(data_bytes, mime):
    """Save raw .webm bytes to disk."""
    fname = ts_filename(".webm")
    path = os.path.join(SCREENSHOTS_DIR, fname)
    with open(path, "wb") as f:
        f.write(data_bytes)
    return fname


def delete_screenshot(name):
    """Delete a screenshot by filename (no path traversal)."""
    if os.sep in name or name.startswith("."):
        raise ValueError("Invalid filename")
    path = os.path.join(SCREENSHOTS_DIR, name)
    if os.path.isfile(path):
        os.remove(path)


class ScreenshotHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        parsed = urlparse(self.path)
        path = parsed.path

        if path == "/":
            self.serve_file(os.path.join(APP_DIR, "index.html"), "text/html")
        elif path == "/icon.svg":
            self.serve_file(os.path.join(APP_DIR, "icon.svg"), "image/svg+xml")
        elif path == "/api/screenshots":
            self.send_json(list_screenshots())
        elif path.startswith("/api/file/"):
            self.serve_screenshot(path[len("/api/file/"):])
        else:
            self.send_error(404)

    def do_POST(self):
        parsed = urlparse(self.path)
        path = parsed.path
        length = int(self.headers.get("Content-Length", 0))

        if path == "/api/save/screenshot":
            # Body is JSON: { "dataUrl": "data:image/png;base64,..." }
            body = json.loads(self.rfile.read(length).decode())
            try:
                fname = save_screenshot(body["dataUrl"])
                self.send_json({"name": fname, "status": "saved"})
            except Exception as e:
                self.send_error_json(str(e))

        elif path == "/api/save/recording":
            # Body is raw .webm bytes; Content-Type is video/webm
            raw = self.rfile.read(length)
            mime = self.headers.get("Content-Type", "video/webm")
            try:
                fname = save_recording(raw, mime)
                self.send_json({"name": fname, "status": "saved"})
            except Exception as e:
                self.send_error_json(str(e))

        elif path == "/api/delete":
            body = json.loads(self.rfile.read(length).decode())
            try:
                delete_screenshot(body["name"])
                self.send_json({"status": "deleted"})
            except Exception as e:
                self.send_error_json(str(e))

        else:
            self.send_error(404)

    def do_OPTIONS(self):
        self.send_response(204)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Content-Type")
        self.end_headers()

    def serve_screenshot(self, name):
        """Serve a saved screenshot or recording file."""
        if os.sep in name or name.startswith("."):
            self.send_error(403)
            return
        path = os.path.join(SCREENSHOTS_DIR, name)
        if not os.path.isfile(path):
            self.send_error(404)
            return
        if name.endswith(".webm"):
            mime = "video/webm"
        elif name.endswith(".png"):
            mime = "image/png"
        else:
            mime = "application/octet-stream"
        size = os.path.getsize(path)
        self.send_response(200)
        self.send_header("Content-Type", mime)
        self.send_header("Content-Length", str(size))
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        with open(path, "rb") as f:
            while True:
                chunk = f.read(65536)
                if not chunk:
                    break
                self.wfile.write(chunk)

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

    def send_error_json(self, message):
        body = json.dumps({"error": message}).encode()
        self.send_response(400)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        pass


print(f"[screenshot] Screenshot & Screen Capture on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), ScreenshotHandler).serve_forever()
