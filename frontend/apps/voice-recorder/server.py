"""Vulos OS — Voice Recorder
Record audio from microphone, list recordings with timestamps, save via appdata API.
"""
import http.server
import json
import mimetypes
import os
import time
import urllib.parse

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
VULOS_API = os.environ.get("VULOS_API", "http://localhost:8080")
DATA_DIR = os.environ.get(
    "VOICE_RECORDER_DIR",
    os.path.expanduser("~/.vulos/data/voice-recorder"),
)
APP_DIR = os.path.dirname(os.path.abspath(__file__))
os.makedirs(DATA_DIR, exist_ok=True)

AUDIO_EXTS = {".webm", ".ogg", ".wav", ".mp4", ".m4a"}


def _safe_path(rel: str) -> str | None:
    """Resolve rel relative to DATA_DIR; return None if it escapes the dir."""
    full = os.path.realpath(os.path.join(DATA_DIR, rel))
    if not full.startswith(os.path.realpath(DATA_DIR) + os.sep):
        return None
    return full


def list_recordings():
    recordings = []
    for fname in sorted(os.listdir(DATA_DIR), reverse=True):
        ext = os.path.splitext(fname)[1].lower()
        if ext not in AUDIO_EXTS:
            continue
        path = os.path.join(DATA_DIR, fname)
        try:
            stat = os.stat(path)
        except OSError:
            continue
        recordings.append({
            "id": fname,
            "name": fname,
            "size": stat.st_size,
            "created": stat.st_mtime,
            "created_iso": time.strftime(
                "%Y-%m-%dT%H:%M:%S", time.localtime(stat.st_mtime)
            ),
            "duration_hint": None,  # browser provides duration via Audio element
        })
    return recordings


class VoiceRecorderHandler(http.server.BaseHTTPRequestHandler):
    def do_OPTIONS(self):
        self.send_response(204)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Content-Type")
        self.end_headers()

    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path

        if path == "/":
            self.serve_file(os.path.join(APP_DIR, "index.html"), "text/html")
        elif path == "/api/recordings":
            self.send_json(list_recordings())
        elif path.startswith("/audio/"):
            self.serve_audio(path[len("/audio/"):])
        else:
            self.send_error(404)

    def do_POST(self):
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path

        if path == "/api/recordings":
            # Save a new recording blob sent as raw body.
            # Expected query params: ?name=<filename>
            params = urllib.parse.parse_qs(parsed.query)
            name = params.get("name", [None])[0]
            if not name:
                ts = time.strftime("%Y%m%d_%H%M%S")
                name = f"recording_{ts}.webm"
            # Sanitise filename
            name = os.path.basename(name)
            ext = os.path.splitext(name)[1].lower()
            if ext not in AUDIO_EXTS:
                name = name + ".webm"

            length = int(self.headers.get("Content-Length", 0))
            if length == 0:
                self.send_error(400, "Empty body")
                return

            dest = os.path.join(DATA_DIR, name)
            with open(dest, "wb") as f:
                remaining = length
                while remaining > 0:
                    chunk = self.rfile.read(min(65536, remaining))
                    if not chunk:
                        break
                    f.write(chunk)
                    remaining -= len(chunk)

            stat = os.stat(dest)
            self.send_json({
                "id": name,
                "name": name,
                "size": stat.st_size,
                "created": stat.st_mtime,
                "created_iso": time.strftime(
                    "%Y-%m-%dT%H:%M:%S", time.localtime(stat.st_mtime)
                ),
            })
        else:
            self.send_error(404)

    def do_DELETE(self):
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path

        if path.startswith("/api/recordings/"):
            fname = urllib.parse.unquote(path[len("/api/recordings/"):])
            full = _safe_path(fname)
            if full is None:
                self.send_error(403)
                return
            if os.path.isfile(full):
                os.remove(full)
                self.send_json({"status": "deleted"})
            else:
                self.send_error(404)
        else:
            self.send_error(404)

    def serve_audio(self, rel: str):
        rel = urllib.parse.unquote(rel)
        full = _safe_path(rel)
        if full is None or not os.path.isfile(full):
            self.send_error(404)
            return
        mime, _ = mimetypes.guess_type(full)
        size = os.path.getsize(full)

        # Support Range requests for seek
        range_header = self.headers.get("Range")
        if range_header and range_header.startswith("bytes="):
            byte_range = range_header[6:].split("-")
            start = int(byte_range[0]) if byte_range[0] else 0
            end = int(byte_range[1]) if len(byte_range) > 1 and byte_range[1] else size - 1
            end = min(end, size - 1)
            length = end - start + 1
            self.send_response(206)
            self.send_header("Content-Type", mime or "audio/webm")
            self.send_header("Content-Length", str(length))
            self.send_header("Content-Range", f"bytes {start}-{end}/{size}")
            self.send_header("Accept-Ranges", "bytes")
            self.end_headers()
            with open(full, "rb") as f:
                f.seek(start)
                remaining = length
                while remaining > 0:
                    chunk = f.read(min(65536, remaining))
                    if not chunk:
                        break
                    self.wfile.write(chunk)
                    remaining -= len(chunk)
        else:
            self.send_response(200)
            self.send_header("Content-Type", mime or "audio/webm")
            self.send_header("Content-Length", str(size))
            self.send_header("Accept-Ranges", "bytes")
            self.end_headers()
            with open(full, "rb") as f:
                while chunk := f.read(65536):
                    self.wfile.write(chunk)

    def serve_file(self, filepath: str, content_type: str):
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

    def log_message(self, fmt, *args):
        pass  # Silence request logs


print(f"[voice-recorder] Voice Recorder on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), VoiceRecorderHandler).serve_forever()
