"""Vula OS — Media Gallery
Traditional photo/video manager with AI features.
Timeline view, albums from folders, favorites, EXIF info, AI describe & search.
"""
import http.server
import json
import mimetypes
import os
import struct
import time
import urllib.request
from datetime import datetime
from urllib.parse import urlparse, parse_qs, unquote

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
VULOS_API = os.environ.get("VULOS_API", "http://localhost:8080")
MEDIA_DIR = os.environ.get("MEDIA_DIR", os.path.expanduser("~/.vulos/data"))
APP_DIR = os.path.dirname(os.path.abspath(__file__))
STATE_DIR = os.path.expanduser("~/.vulos/data/.gallery")
os.makedirs(STATE_DIR, exist_ok=True)

IMAGE_EXTS = {".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".bmp", ".svg", ".tiff"}
VIDEO_EXTS = {".mp4", ".mov", ".avi", ".mkv", ".webm", ".m4v"}
MEDIA_EXTS = IMAGE_EXTS | VIDEO_EXTS

# --- Favorites store (simple JSON file) ---
FAVS_PATH = os.path.join(STATE_DIR, "favorites.json")

def load_favorites():
    try:
        with open(FAVS_PATH) as f: return set(json.load(f))
    except: return set()

def save_favorites(favs):
    with open(FAVS_PATH, "w") as f: json.dump(list(favs), f)

# --- Media scanning ---
def scan_all(root, limit=2000):
    """Scan all media files, return sorted by modification time (newest first)."""
    media = []
    favs = load_favorites()
    for dirpath, _, filenames in os.walk(root):
        for fname in filenames:
            ext = os.path.splitext(fname)[1].lower()
            if ext not in MEDIA_EXTS:
                continue
            path = os.path.join(dirpath, fname)
            try:
                stat = os.stat(path)
            except OSError:
                continue
            rel = os.path.relpath(path, root)
            media.append({
                "name": fname,
                "path": rel,
                "folder": os.path.relpath(dirpath, root) if dirpath != root else "",
                "size": stat.st_size,
                "modified": stat.st_mtime,
                "date": datetime.fromtimestamp(stat.st_mtime).strftime("%Y-%m-%d"),
                "type": "video" if ext in VIDEO_EXTS else "image",
                "favorite": rel in favs,
            })
            if len(media) >= limit:
                break
        if len(media) >= limit:
            break
    media.sort(key=lambda m: m["modified"], reverse=True)
    return media

def list_albums(root):
    """Return folders that contain media as albums."""
    albums = {}
    for dirpath, _, filenames in os.walk(root):
        media_files = [f for f in filenames if os.path.splitext(f)[1].lower() in MEDIA_EXTS]
        if not media_files:
            continue
        rel = os.path.relpath(dirpath, root) if dirpath != root else ""
        name = os.path.basename(dirpath) if dirpath != root else "Root"
        # Use first image as cover
        cover = None
        for f in media_files:
            if os.path.splitext(f)[1].lower() in IMAGE_EXTS:
                cover = os.path.relpath(os.path.join(dirpath, f), root)
                break
        if not cover and media_files:
            cover = os.path.relpath(os.path.join(dirpath, media_files[0]), root)
        albums[rel] = {
            "path": rel,
            "name": name,
            "count": len(media_files),
            "cover": cover,
        }
    # Sort by name
    return sorted(albums.values(), key=lambda a: a["name"].lower())

def get_file_info(root, rel_path):
    """Get detailed file info including basic EXIF for JPEGs."""
    path = os.path.realpath(os.path.join(root, rel_path))
    if not path.startswith(os.path.realpath(root)):
        return None
    if not os.path.isfile(path):
        return None
    stat = os.stat(path)
    ext = os.path.splitext(path)[1].lower()
    info = {
        "name": os.path.basename(path),
        "path": rel_path,
        "size": stat.st_size,
        "size_human": fmt_size(stat.st_size),
        "modified": datetime.fromtimestamp(stat.st_mtime).isoformat(),
        "type": "video" if ext in VIDEO_EXTS else "image",
        "ext": ext,
        "favorite": rel_path in load_favorites(),
    }
    # Try to read basic EXIF from JPEG
    if ext in (".jpg", ".jpeg"):
        exif = read_basic_exif(path)
        if exif:
            info["exif"] = exif
    # Image dimensions (for common formats)
    dims = read_image_size(path, ext)
    if dims:
        info["width"], info["height"] = dims
    return info

def fmt_size(b):
    for unit in ("B", "KB", "MB", "GB"):
        if b < 1024: return f"{b:.1f} {unit}"
        b /= 1024
    return f"{b:.1f} TB"

def read_basic_exif(path):
    """Read basic EXIF tags from JPEG without external libraries."""
    try:
        with open(path, "rb") as f:
            data = f.read(64 * 1024)  # Read first 64KB
        if data[:2] != b'\xff\xd8':
            return None
        exif = {}
        pos = 2
        while pos < len(data) - 4:
            if data[pos] != 0xFF:
                break
            marker = data[pos + 1]
            if marker == 0xE1:  # APP1 (EXIF)
                length = struct.unpack(">H", data[pos + 2:pos + 4])[0]
                segment = data[pos + 4:pos + 2 + length]
                if segment[:6] == b'Exif\x00\x00':
                    tiff = segment[6:]
                    if len(tiff) < 8:
                        break
                    byte_order = tiff[:2]
                    if byte_order == b'II':
                        endian = "<"
                    elif byte_order == b'MM':
                        endian = ">"
                    else:
                        break
                    ifd_offset = struct.unpack(endian + "I", tiff[4:8])[0]
                    exif = parse_ifd(tiff, ifd_offset, endian)
                break
            elif marker in (0xD9, 0xDA):
                break
            else:
                length = struct.unpack(">H", data[pos + 2:pos + 4])[0]
                pos += 2 + length
                continue
            pos += 2 + struct.unpack(">H", data[pos + 2:pos + 4])[0]
        return exif if exif else None
    except:
        return None

EXIF_TAGS = {
    0x010F: "make", 0x0110: "model", 0x0112: "orientation",
    0x829A: "exposure_time", 0x829D: "f_number",
    0x8827: "iso", 0x9003: "date_taken",
    0x920A: "focal_length", 0xA405: "focal_length_35mm",
    0xA002: "width", 0xA003: "height",
}

def parse_ifd(tiff, offset, endian):
    result = {}
    try:
        count = struct.unpack(endian + "H", tiff[offset:offset + 2])[0]
        for i in range(count):
            entry_off = offset + 2 + i * 12
            if entry_off + 12 > len(tiff):
                break
            tag, typ, cnt, val_off = struct.unpack(
                endian + "HHI I", tiff[entry_off:entry_off + 12]
            )
            name = EXIF_TAGS.get(tag)
            if not name:
                continue
            # ASCII string
            if typ == 2:
                if cnt <= 4:
                    val = tiff[entry_off + 8:entry_off + 8 + cnt]
                else:
                    val = tiff[val_off:val_off + cnt]
                result[name] = val.decode("ascii", errors="replace").rstrip("\x00").strip()
            # Short
            elif typ == 3:
                result[name] = struct.unpack(endian + "H", tiff[entry_off + 8:entry_off + 10])[0]
            # Long
            elif typ == 4:
                result[name] = val_off
            # Rational
            elif typ == 5:
                if val_off + 8 <= len(tiff):
                    num, den = struct.unpack(endian + "II", tiff[val_off:val_off + 8])
                    result[name] = f"{num}/{den}" if den else str(num)
    except:
        pass
    return result

def read_image_size(path, ext):
    """Read image dimensions from file header."""
    try:
        with open(path, "rb") as f:
            head = f.read(32)
        if ext == ".png" and head[:8] == b'\x89PNG\r\n\x1a\n':
            w, h = struct.unpack(">II", head[16:24])
            return w, h
        if ext in (".jpg", ".jpeg"):
            with open(path, "rb") as f:
                data = f.read(64 * 1024)
            pos = 2
            while pos < len(data) - 9:
                if data[pos] != 0xFF: break
                marker = data[pos + 1]
                if marker in (0xC0, 0xC2):
                    h, w = struct.unpack(">HH", data[pos + 5:pos + 9])
                    return w, h
                if marker in (0xD9, 0xDA): break
                length = struct.unpack(">H", data[pos + 2:pos + 4])[0]
                pos += 2 + length
        if ext == ".gif" and head[:6] in (b'GIF87a', b'GIF89a'):
            w, h = struct.unpack("<HH", head[6:10])
            return w, h
    except:
        pass
    return None


class GalleryHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        parsed = urlparse(self.path)
        path = parsed.path
        params = parse_qs(parsed.query)

        if path == "/":
            self.serve_file(os.path.join(APP_DIR, "index.html"), "text/html")
        elif path == "/api/media":
            folder = params.get("folder", [None])[0]
            ftype = params.get("type", [None])[0]  # "image" or "video"
            favs_only = params.get("favorites", [""])[0] == "1"
            media = scan_all(MEDIA_DIR)
            if folder is not None:
                media = [m for m in media if m["folder"] == folder]
            if ftype:
                media = [m for m in media if m["type"] == ftype]
            if favs_only:
                media = [m for m in media if m["favorite"]]
            self.send_json(media)
        elif path == "/api/albums":
            self.send_json(list_albums(MEDIA_DIR))
        elif path == "/api/info":
            p = params.get("path", [None])[0]
            if not p:
                self.send_error(400); return
            info = get_file_info(MEDIA_DIR, unquote(p))
            if info is None:
                self.send_error(404); return
            self.send_json(info)
        elif path == "/api/search":
            q = params.get("q", [""])[0]
            self.handle_search(unquote(q))
        elif path == "/api/describe":
            p = params.get("path", [None])[0]
            if not p:
                self.send_error(400); return
            self.handle_describe(unquote(p))
        elif path.startswith("/media/"):
            self.serve_media()
        else:
            self.send_error(404)

    def do_POST(self):
        parsed = urlparse(self.path)
        length = int(self.headers.get("Content-Length", 0))
        body = json.loads(self.rfile.read(length)) if length else {}

        if parsed.path == "/api/favorite":
            path = body.get("path", "")
            add = body.get("add", True)
            favs = load_favorites()
            if add:
                favs.add(path)
            else:
                favs.discard(path)
            save_favorites(favs)
            self.send_json({"status": "ok", "favorite": add})
        elif parsed.path == "/api/delete":
            rel = body.get("path", "")
            full = os.path.realpath(os.path.join(MEDIA_DIR, rel))
            if not full.startswith(os.path.realpath(MEDIA_DIR)):
                self.send_error(403); return
            if os.path.isfile(full):
                os.remove(full)
                favs = load_favorites()
                favs.discard(rel)
                save_favorites(favs)
            self.send_json({"status": "deleted"})
        else:
            self.send_error(404)

    def handle_search(self, query):
        try:
            req = urllib.request.Request(
                VULOS_API + "/api/recall/search",
                data=json.dumps({"query": query, "top_k": 50}).encode(),
                headers={"Content-Type": "application/json"},
            )
            resp = urllib.request.urlopen(req, timeout=5)
            results = json.loads(resp.read())
            favs = load_favorites()
            media = []
            for r in results:
                path = r.get("metadata", {}).get("path", "")
                ext = os.path.splitext(path)[1].lower()
                if ext in MEDIA_EXTS:
                    media.append({
                        "name": os.path.basename(path),
                        "path": path,
                        "type": "video" if ext in VIDEO_EXTS else "image",
                        "score": r.get("score", 0),
                        "favorite": path in favs,
                    })
            self.send_json(media)
        except Exception:
            self.send_json([])

    def handle_describe(self, rel_path):
        """Ask AI to describe an image."""
        full = os.path.realpath(os.path.join(MEDIA_DIR, rel_path))
        if not full.startswith(os.path.realpath(MEDIA_DIR)):
            self.send_error(403); return
        try:
            ai_req = urllib.request.Request(
                VULOS_API + "/api/ai/chat",
                data=json.dumps({
                    "messages": [{
                        "role": "user",
                        "content": f"Describe the image at this path briefly (2-3 sentences, focus on subject and mood): {os.path.basename(full)}"
                    }],
                    "stream": False,
                }).encode(),
                headers={"Content-Type": "application/json"},
            )
            resp = urllib.request.urlopen(ai_req, timeout=30)
            data = json.loads(resp.read())
            self.send_json({"description": data.get("content", "No description available.")})
        except Exception as e:
            self.send_json({"description": f"Could not describe: {e}"})

    def serve_media(self):
        rel = self.path[len("/media/"):]
        path = os.path.join(MEDIA_DIR, unquote(rel))
        path = os.path.realpath(path)
        if not path.startswith(os.path.realpath(MEDIA_DIR)):
            self.send_error(403); return
        if not os.path.isfile(path):
            self.send_error(404); return
        mime, _ = mimetypes.guess_type(path)
        self.send_response(200)
        self.send_header("Content-Type", mime or "application/octet-stream")
        self.send_header("Content-Length", str(os.path.getsize(path)))
        self.send_header("Cache-Control", "public, max-age=86400")
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
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
        self.wfile.write(json.dumps(data).encode())

    def log_message(self, format, *args): pass

print(f"[gallery] Media Gallery on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), GalleryHandler).serve_forever()
