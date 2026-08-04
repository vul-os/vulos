"""Vula OS — Music Player
Serves audio files from ~/.vulos/music/, manages playlists, extracts ID3 album art.
Formats: mp3, flac, ogg, wav, m4a — played via browser <audio> element.
"""
import http.server
import json
import mimetypes
import os
import struct
import time
from urllib.parse import urlparse, parse_qs, unquote

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
APP_DIR = os.path.dirname(os.path.abspath(__file__))
MUSIC_DIR = os.environ.get("MUSIC_DIR", os.path.expanduser("~/.vulos/music"))
DATA_DIR = os.path.expanduser("~/.vulos/data/.music")
os.makedirs(MUSIC_DIR, exist_ok=True)
os.makedirs(DATA_DIR, exist_ok=True)

AUDIO_EXTS = {".mp3", ".flac", ".ogg", ".wav", ".m4a", ".aac", ".opus", ".weba"}
PLAYLISTS_FILE = os.path.join(DATA_DIR, "playlists.json")

# ---------- Playlist persistence ----------

def load_playlists():
    try:
        with open(PLAYLISTS_FILE) as f:
            return json.load(f)
    except Exception:
        return {}

def save_playlists(playlists):
    with open(PLAYLISTS_FILE, "w") as f:
        json.dump(playlists, f, indent=2)

# ---------- Library scanning ----------

def scan_library(root, limit=5000):
    """Walk music dir, return list of track dicts grouped by artist/album."""
    tracks = []
    for dirpath, _, filenames in os.walk(root):
        for fname in sorted(filenames):
            ext = os.path.splitext(fname)[1].lower()
            if ext not in AUDIO_EXTS:
                continue
            full = os.path.join(dirpath, fname)
            try:
                stat = os.stat(full)
            except OSError:
                continue
            rel = os.path.relpath(full, root)
            folder = os.path.relpath(dirpath, root) if dirpath != root else ""
            # Derive artist/album from folder structure: Artist/Album/track or flat
            parts = folder.split(os.sep) if folder else []
            artist = parts[0] if len(parts) >= 1 else "Unknown Artist"
            album = parts[1] if len(parts) >= 2 else (parts[0] if len(parts) == 1 else "Unknown Album")
            title = os.path.splitext(fname)[0]
            tracks.append({
                "path": rel,
                "name": fname,
                "title": title,
                "artist": artist,
                "album": album,
                "folder": folder,
                "size": stat.st_size,
                "modified": stat.st_mtime,
                "ext": ext,
                "has_art": False,  # will be checked lazily
            })
            if len(tracks) >= limit:
                return tracks
    return tracks

# ---------- ID3 album art extraction ----------

def extract_id3_art(full_path):
    """Extract embedded album art from ID3v2 tags (MP3). Returns bytes or None."""
    try:
        with open(full_path, "rb") as f:
            header = f.read(10)
        if header[:3] != b"ID3":
            return None
        # ID3v2 size is 4 bytes syncsafe integer
        raw_size = header[6:10]
        size = (raw_size[0] << 21) | (raw_size[1] << 14) | (raw_size[2] << 7) | raw_size[3]
        with open(full_path, "rb") as f:
            f.seek(10)
            tag_data = f.read(size)
        pos = 0
        version = header[3]  # 3 or 4
        while pos < len(tag_data) - 10:
            if version == 3:
                frame_id = tag_data[pos:pos + 4].decode("latin-1", errors="replace")
                frame_size = struct.unpack(">I", tag_data[pos + 4:pos + 8])[0]
                frame_flags = tag_data[pos + 8:pos + 10]
                frame_data = tag_data[pos + 10:pos + 10 + frame_size]
                pos += 10 + frame_size
            else:
                frame_id = tag_data[pos:pos + 4].decode("latin-1", errors="replace")
                raw = tag_data[pos + 4:pos + 8]
                frame_size = (raw[0] << 21) | (raw[1] << 14) | (raw[2] << 7) | raw[3]
                frame_flags = tag_data[pos + 8:pos + 10]
                frame_data = tag_data[pos + 10:pos + 10 + frame_size]
                pos += 10 + frame_size
            if frame_id.startswith("\x00") or frame_size == 0:
                break
            if frame_id == "APIC":
                # APIC: text_encoding, mime_type, picture_type, description, image_data
                try:
                    enc = frame_data[0]
                    null = b"\x00\x00" if enc in (1, 2) else b"\x00"
                    mime_end = frame_data.index(b"\x00", 1)
                    mime = frame_data[1:mime_end].decode("latin-1")
                    idx = mime_end + 1 + 1  # skip picture type byte
                    # skip description (null-terminated)
                    while idx < len(frame_data) - 1:
                        if enc in (1, 2):
                            if frame_data[idx:idx + 2] == b"\x00\x00":
                                idx += 2
                                break
                        else:
                            if frame_data[idx] == 0:
                                idx += 1
                                break
                        idx += 1
                    return frame_data[idx:]
                except Exception:
                    pass
    except Exception:
        pass
    return None

def get_art_mime(data):
    """Detect MIME type from image magic bytes."""
    if data[:8] == b"\x89PNG\r\n\x1a\n":
        return "image/png"
    if data[:3] == b"\xff\xd8\xff":
        return "image/jpeg"
    if data[:4] in (b"GIF8", b"GIF9"):
        return "image/gif"
    return "image/jpeg"

# ---------- HTTP Handler ----------

class MusicHandler(http.server.BaseHTTPRequestHandler):

    def do_GET(self):
        parsed = urlparse(self.path)
        path = parsed.path
        params = parse_qs(parsed.query)

        if path == "/" or path == "":
            self.serve_file(os.path.join(APP_DIR, "index.html"), "text/html")
        elif path == "/api/library":
            tracks = scan_library(MUSIC_DIR)
            self.send_json(tracks)
        elif path == "/api/playlists":
            self.send_json(load_playlists())
        elif path == "/api/art":
            rel = unquote(params.get("path", [""])[0])
            self.serve_art(rel)
        elif path.startswith("/audio/"):
            self.serve_audio(path[7:])
        elif path == "/icon.svg":
            self.serve_file(os.path.join(APP_DIR, "icon.svg"), "image/svg+xml")
        else:
            self.send_error(404)

    def do_POST(self):
        parsed = urlparse(self.path)
        length = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(length)) if length else {}

        if parsed.path == "/api/playlists/save":
            name = body.get("name", "").strip()
            tracks = body.get("tracks", [])
            if not name:
                self.send_error(400)
                return
            playlists = load_playlists()
            playlists[name] = tracks
            save_playlists(playlists)
            self.send_json({"status": "ok", "name": name})

        elif parsed.path == "/api/playlists/delete":
            name = body.get("name", "").strip()
            playlists = load_playlists()
            playlists.pop(name, None)
            save_playlists(playlists)
            self.send_json({"status": "ok"})

        elif parsed.path == "/api/playlists/reorder":
            name = body.get("name", "").strip()
            tracks = body.get("tracks", [])
            playlists = load_playlists()
            if name in playlists:
                playlists[name] = tracks
                save_playlists(playlists)
            self.send_json({"status": "ok"})

        else:
            self.send_error(404)

    def serve_art(self, rel):
        """Serve embedded album art from an MP3, or 404."""
        if not rel:
            self.send_error(400)
            return
        full = os.path.realpath(os.path.join(MUSIC_DIR, rel))
        _mdir_real = os.path.realpath(MUSIC_DIR)
        if not (full == _mdir_real or full.startswith(_mdir_real + os.sep)):
            self.send_error(403)
            return
        if not os.path.isfile(full):
            self.send_error(404)
            return
        art = extract_id3_art(full)
        if not art:
            self.send_error(404)
            return
        mime = get_art_mime(art)
        self.send_response(200)
        self.send_header("Content-Type", mime)
        self.send_header("Content-Length", str(len(art)))
        self.send_header("Cache-Control", "public, max-age=86400")
        self.end_headers()
        self.wfile.write(art)

    def serve_audio(self, rel):
        """Serve audio file with range support for seeking."""
        rel = unquote(rel)
        full = os.path.realpath(os.path.join(MUSIC_DIR, rel))
        _mdir_real = os.path.realpath(MUSIC_DIR)
        if not (full == _mdir_real or full.startswith(_mdir_real + os.sep)):
            self.send_error(403)
            return
        if not os.path.isfile(full):
            self.send_error(404)
            return
        mime, _ = mimetypes.guess_type(full)
        if not mime:
            mime = "application/octet-stream"
        file_size = os.path.getsize(full)
        range_header = self.headers.get("Range")
        if range_header:
            try:
                byte_range = range_header.replace("bytes=", "").split("-")
                start = int(byte_range[0]) if byte_range[0] else 0
                end = int(byte_range[1]) if byte_range[1] else file_size - 1
                end = min(end, file_size - 1)
                length = end - start + 1
                self.send_response(206)
                self.send_header("Content-Type", mime)
                self.send_header("Content-Range", f"bytes {start}-{end}/{file_size}")
                self.send_header("Content-Length", str(length))
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
            except Exception:
                self.send_error(416)
        else:
            self.send_response(200)
            self.send_header("Content-Type", mime)
            self.send_header("Content-Length", str(file_size))
            self.send_header("Accept-Ranges", "bytes")
            self.end_headers()
            with open(full, "rb") as f:
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

    def log_message(self, fmt, *args):
        pass


print(f"[music] Music Player on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), MusicHandler).serve_forever()
