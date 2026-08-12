"""Vulos OS — Image Editor
Canvas-based image editor with crop, rotate, flip, resize, filters, and annotation.
Saves output to ~/.vulos/pictures/.
"""
import http.server
import json
import os
import base64
import mimetypes
from urllib.parse import urlparse, parse_qs, unquote

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
VULOS_API = os.environ.get("VULOS_API", "http://localhost:8080")
PICTURES_DIR = os.environ.get("PICTURES_DIR", os.path.expanduser("~/.vulos/pictures"))
APP_DIR = os.path.dirname(os.path.abspath(__file__))
os.makedirs(PICTURES_DIR, exist_ok=True)


def list_pictures():
    """Return a list of image files in the pictures directory."""
    images = []
    exts = {".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp", ".svg"}
    try:
        for fname in sorted(os.listdir(PICTURES_DIR)):
            if os.path.splitext(fname)[1].lower() in exts:
                path = os.path.join(PICTURES_DIR, fname)
                stat = os.stat(path)
                images.append({
                    "name": fname,
                    "size": stat.st_size,
                    "modified": stat.st_mtime,
                })
    except OSError:
        pass
    return images


def save_picture(filename, data_b64):
    """Save a base64-encoded image to the pictures directory.
    Returns the final filename used.
    """
    if not filename:
        import time
        filename = f"edited_{int(time.time() * 1000)}.png"

    # Strip any directory traversal
    filename = os.path.basename(filename)

    # Decode the base64 data URL if needed
    if "," in data_b64:
        data_b64 = data_b64.split(",", 1)[1]

    data = base64.b64decode(data_b64)
    dest = os.path.join(PICTURES_DIR, filename)
    with open(dest, "wb") as f:
        f.write(data)
    return filename


class ImageEditorHandler(http.server.BaseHTTPRequestHandler):
    def do_OPTIONS(self):
        self.send_response(204)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Content-Type")
        self.end_headers()

    def do_GET(self):
        parsed = urlparse(self.path)
        path = parsed.path

        if path == "/":
            self.serve_file(os.path.join(APP_DIR, "index.html"), "text/html")
        elif path == "/api/pictures":
            self.send_json(list_pictures())
        elif path.startswith("/pictures/"):
            self.serve_picture(path[len("/pictures/"):])
        else:
            self.send_error(404)

    def do_POST(self):
        parsed = urlparse(self.path)
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length).decode("utf-8", errors="replace") if length else ""

        if parsed.path == "/api/save":
            try:
                data = json.loads(body)
                filename = data.get("filename", "")
                image_data = data.get("data", "")
                if not image_data:
                    self.send_error_json(400, "No image data provided")
                    return
                saved = save_picture(filename, image_data)
                self.send_json({"status": "ok", "filename": saved})
            except Exception as e:
                self.send_error_json(500, str(e))
        else:
            self.send_error(404)

    def serve_picture(self, filename):
        filename = os.path.basename(unquote(filename))
        path = os.path.join(PICTURES_DIR, filename)
        path = os.path.realpath(path)
        if not path.startswith(os.path.realpath(PICTURES_DIR)):
            self.send_error(403)
            return
        if not os.path.isfile(path):
            self.send_error(404)
            return
        mime, _ = mimetypes.guess_type(path)
        self.send_response(200)
        self.send_header("Content-Type", mime or "application/octet-stream")
        self.send_header("Content-Length", str(os.path.getsize(path)))
        self.send_header("Cache-Control", "public, max-age=3600")
        self.end_headers()
        with open(path, "rb") as f:
            while chunk := f.read(65536):
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

    def send_error_json(self, code, message):
        body = json.dumps({"error": message}).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        pass


print(f"[image-editor] Image Editor on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), ImageEditorHandler).serve_forever()
