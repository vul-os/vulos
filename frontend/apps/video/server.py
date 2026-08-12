"""Vulos OS — Video Player
Serves the video player UI and streams local video files with byte-range support.
Scans ~/.vulos/videos/ (and user-configured directories) for video files.
"""
import http.server
import json
import mimetypes
import os
import urllib.parse
from urllib.parse import urlparse, parse_qs, unquote

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
VIDEO_DIR = os.environ.get("VIDEO_DIR", os.path.expanduser("~/.vulos/videos"))
APP_DIR = os.path.dirname(os.path.abspath(__file__))
STATE_DIR = os.path.expanduser("~/.vulos/data/.video")
os.makedirs(STATE_DIR, exist_ok=True)
os.makedirs(VIDEO_DIR, exist_ok=True)

VIDEO_EXTS = {".mp4", ".webm", ".mkv", ".avi", ".m4v", ".mov", ".ogv"}
MIME_OVERRIDES = {
    ".mkv": "video/x-matroska",
    ".avi": "video/x-msvideo",
    ".m4v": "video/mp4",
    ".ogv": "video/ogg",
}

# Queue persistence
QUEUE_PATH = os.path.join(STATE_DIR, "queue.json")


def load_queue():
    try:
        with open(QUEUE_PATH) as f:
            return json.load(f)
    except Exception:
        return []


def save_queue(queue):
    with open(QUEUE_PATH, "w") as f:
        json.dump(queue, f)


def scan_videos(root, limit=500):
    """Recursively scan root for video files, newest first."""
    videos = []
    if not os.path.isdir(root):
        return videos
    for dirpath, _, filenames in os.walk(root):
        for fname in filenames:
            ext = os.path.splitext(fname)[1].lower()
            if ext not in VIDEO_EXTS:
                continue
            path = os.path.join(dirpath, fname)
            try:
                stat = os.stat(path)
            except OSError:
                continue
            rel = os.path.relpath(path, root)
            videos.append({
                "name": fname,
                "path": rel,
                "size": stat.st_size,
                "modified": stat.st_mtime,
                "ext": ext,
            })
            if len(videos) >= limit:
                break
        if len(videos) >= limit:
            break
    videos.sort(key=lambda v: v["modified"], reverse=True)
    return videos


def resolve_safe(root, rel):
    """Resolve a relative path under root safely, rejecting traversal."""
    full = os.path.realpath(os.path.join(root, unquote(rel)))
    if not full.startswith(os.path.realpath(root)):
        return None
    return full


class VideoHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        parsed = urlparse(self.path)
        path = parsed.path
        params = parse_qs(parsed.query)

        if path == "/" or path == "/index.html":
            self.serve_static("index.html", "text/html")
        elif path == "/icon.svg":
            self.serve_static("icon.svg", "image/svg+xml")
        elif path == "/api/videos":
            self.send_json(scan_videos(VIDEO_DIR))
        elif path == "/api/queue":
            self.send_json(load_queue())
        elif path.startswith("/stream/"):
            self.stream_video(path[len("/stream/"):])
        else:
            self.send_error(404)

    def do_POST(self):
        parsed = urlparse(self.path)
        length = int(self.headers.get("Content-Length", 0))
        try:
            body = json.loads(self.rfile.read(length)) if length else {}
        except Exception:
            body = {}

        if parsed.path == "/api/queue":
            # Replace entire queue
            queue = body.get("queue", [])
            save_queue(queue)
            self.send_json({"status": "ok"})
        elif parsed.path == "/api/queue/append":
            rel = body.get("path", "")
            queue = load_queue()
            if rel and rel not in queue:
                queue.append(rel)
                save_queue(queue)
            self.send_json({"status": "ok", "queue": queue})
        elif parsed.path == "/api/queue/remove":
            rel = body.get("path", "")
            queue = [p for p in load_queue() if p != rel]
            save_queue(queue)
            self.send_json({"status": "ok", "queue": queue})
        elif parsed.path == "/api/queue/clear":
            save_queue([])
            self.send_json({"status": "ok"})
        else:
            self.send_error(404)

    def stream_video(self, rel):
        """Stream a video file with HTTP byte-range support."""
        full = resolve_safe(VIDEO_DIR, rel)
        if not full:
            self.send_error(403)
            return
        if not os.path.isfile(full):
            self.send_error(404)
            return

        ext = os.path.splitext(full)[1].lower()
        mime = MIME_OVERRIDES.get(ext) or mimetypes.guess_type(full)[0] or "application/octet-stream"

        file_size = os.path.getsize(full)
        range_header = self.headers.get("Range")

        if range_header:
            # Parse "bytes=start-end"
            try:
                byte_range = range_header.replace("bytes=", "").strip()
                parts = byte_range.split("-")
                start = int(parts[0]) if parts[0] else 0
                end = int(parts[1]) if len(parts) > 1 and parts[1] else file_size - 1
            except (ValueError, IndexError):
                start, end = 0, file_size - 1
            end = min(end, file_size - 1)
            length = end - start + 1

            self.send_response(206)
            self.send_header("Content-Type", mime)
            self.send_header("Content-Length", str(length))
            self.send_header("Content-Range", f"bytes {start}-{end}/{file_size}")
            self.send_header("Accept-Ranges", "bytes")
            self.send_header("Cache-Control", "no-cache")
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
            self.send_header("Content-Type", mime)
            self.send_header("Content-Length", str(file_size))
            self.send_header("Accept-Ranges", "bytes")
            self.send_header("Cache-Control", "no-cache")
            self.end_headers()
            with open(full, "rb") as f:
                while chunk := f.read(65536):
                    self.wfile.write(chunk)

    def serve_static(self, filename, content_type):
        filepath = os.path.join(APP_DIR, filename)
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

    def log_message(self, format, *args):
        pass


print(f"[video] Video Player on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), VideoHandler).serve_forever()
