"""Vula OS — Camera
Serves the camera UI and handles photo/video saves to ~/.vulos/data/pictures and videos.
"""
import base64
import http.server
import json
import os
import time
from urllib.parse import urlparse

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
APP_DIR = os.path.dirname(os.path.abspath(__file__))

# Data directories
home = os.path.expanduser("~")
PICTURES_DIR = os.environ.get("VULOS_PICTURES_DIR", os.path.join(home, ".vulos", "data", "pictures"))
VIDEOS_DIR = os.environ.get("VULOS_VIDEOS_DIR", os.path.join(home, ".vulos", "data", "videos"))
os.makedirs(PICTURES_DIR, exist_ok=True)
os.makedirs(VIDEOS_DIR, exist_ok=True)


def timestamp_filename(prefix, ext):
    ts = time.strftime("%Y%m%d_%H%M%S")
    return f"{prefix}_{ts}{ext}"


class CameraHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        parsed = urlparse(self.path)
        path = parsed.path

        if path == "/" or path == "":
            self.serve_file(os.path.join(APP_DIR, "index.html"), "text/html")
        elif path == "/vulos-tokens.css":
            self.serve_file(os.path.join(APP_DIR, "..", "_shared", "vulos-tokens.css"), "text/css")
        else:
            self.send_error(404)

    def do_POST(self):
        parsed = urlparse(self.path)
        path = parsed.path
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length) if length else b""

        if path == "/api/save-photo":
            self.handle_save_photo(body)
        elif path == "/api/save-video":
            self.handle_save_video(body)
        else:
            self.send_error(404)

    def do_OPTIONS(self):
        self.send_response(200)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Content-Type")
        self.end_headers()

    def handle_save_photo(self, body):
        """Save a base64-encoded PNG snapshot to ~/.vulos/data/pictures."""
        try:
            data = json.loads(body)
            image_data = data.get("data", "")
            # Strip data URI prefix if present: "data:image/png;base64,..."
            if "," in image_data:
                image_data = image_data.split(",", 1)[1]
            raw = base64.b64decode(image_data)
            filename = timestamp_filename("photo", ".png")
            filepath = os.path.join(PICTURES_DIR, filename)
            with open(filepath, "wb") as f:
                f.write(raw)
            self.send_json({"status": "saved", "filename": filename, "path": filepath, "size": len(raw)})
        except Exception as e:
            self.send_json_error(500, str(e))

    def handle_save_video(self, body):
        """Save a raw video blob (webm) to ~/.vulos/data/videos."""
        try:
            # Body is raw binary (Content-Type: video/webm or application/octet-stream)
            # But we also support JSON with base64 for consistency
            content_type = self.headers.get("Content-Type", "")
            if "json" in content_type:
                data = json.loads(body)
                video_data = data.get("data", "")
                if "," in video_data:
                    video_data = video_data.split(",", 1)[1]
                raw = base64.b64decode(video_data)
            else:
                raw = body

            filename = timestamp_filename("video", ".webm")
            filepath = os.path.join(VIDEOS_DIR, filename)
            with open(filepath, "wb") as f:
                f.write(raw)
            self.send_json({"status": "saved", "filename": filename, "path": filepath, "size": len(raw)})
        except Exception as e:
            self.send_json_error(500, str(e))

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
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
        self.wfile.write(json.dumps(data).encode())

    def send_json_error(self, code, message):
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
        self.wfile.write(json.dumps({"error": message}).encode())

    def log_message(self, format, *args):
        pass


print(f"[camera] Camera on port {PORT}")
print(f"[camera] Photos -> {PICTURES_DIR}")
print(f"[camera] Videos -> {VIDEOS_DIR}")
http.server.HTTPServer(("0.0.0.0", PORT), CameraHandler).serve_forever()
