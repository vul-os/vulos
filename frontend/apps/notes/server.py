"""Vulos OS — Universal Memory (Notes)
Every thought indexed by Recall. Markdown editor with instant search.
"""
import http.server
import json
import os
import time
import urllib.parse
import urllib.request

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
VULOS_API = os.environ.get("VULOS_API", "http://localhost:8080")
DATA_DIR = os.environ.get("NOTES_DIR", os.path.expanduser("~/.vulos/data/notes"))
APP_DIR = os.path.dirname(os.path.abspath(__file__))
os.makedirs(DATA_DIR, exist_ok=True)

# Realpath of DATA_DIR used for containment checks (resolved once at startup)
_DATA_DIR_REAL = os.path.realpath(DATA_DIR)
# Realpath of the app source dir — static assets (index.html, collab.js, vendor/,
# src/) are served from here for the offline/collab code to load (OFFLINE-DATA-01).
_APP_DIR_REAL = os.path.realpath(APP_DIR)

# Static asset types served from the app dir. Extension allow-list — nothing else
# is servable, so there is no way to read arbitrary files even before the
# containment check below.
STATIC_TYPES = {
    ".html": "text/html; charset=utf-8",
    ".js":   "text/javascript; charset=utf-8",
    ".mjs":  "text/javascript; charset=utf-8",
    ".css":  "text/css; charset=utf-8",
    ".json": "application/json; charset=utf-8",
    ".svg":  "image/svg+xml",
    ".map":  "application/json; charset=utf-8",
    ".woff2": "font/woff2",
}

CSP = (
    "default-src 'self'; "
    "script-src 'self' 'unsafe-inline'; "
    "style-src 'self' 'unsafe-inline'; "
    "object-src 'none'; "
    "frame-ancestors 'none'; "
    "base-uri 'none'"
)

# ---------------------------------------------------------------------------
# H1: safe note-id validation + realpath containment
# ---------------------------------------------------------------------------

def _sanitize_note_id(note_id):
    """Return a safe basename note_id, or raise ValueError.

    Rules:
    - Must not be empty.
    - Must not contain path separators or '..' components.
    - After os.path.join with DATA_DIR, the realpath must remain inside DATA_DIR.
    """
    if not note_id:
        raise ValueError("empty note_id")
    # Reject any path separator characters or traversal sequences
    if "/" in note_id or "\\" in note_id or ".." in note_id:
        raise ValueError("invalid note_id")
    safe_id = os.path.basename(note_id)
    if not safe_id or safe_id != note_id:
        raise ValueError("invalid note_id")
    # Realpath containment check
    candidate = os.path.realpath(os.path.join(DATA_DIR, safe_id + ".md"))
    if not candidate.startswith(_DATA_DIR_REAL + os.sep):
        raise ValueError("path escapes DATA_DIR")
    return safe_id


def list_notes():
    notes = []
    for f in sorted(os.listdir(DATA_DIR), reverse=True):
        if f.endswith(".md"):
            path = os.path.join(DATA_DIR, f)
            with open(path) as fh:
                content = fh.read()
            title = content.split("\n")[0].lstrip("# ").strip() or f
            notes.append({"id": f[:-3], "title": title, "preview": content[:200], "modified": os.path.getmtime(path)})
    return notes

def get_note(note_id):
    note_id = _sanitize_note_id(note_id)
    path = os.path.join(DATA_DIR, note_id + ".md")
    if not os.path.exists(path): return None
    with open(path) as f: return f.read()

def save_note(note_id, content):
    if not note_id:
        note_id = str(int(time.time() * 1000))
    note_id = _sanitize_note_id(note_id)
    path = os.path.join(DATA_DIR, note_id + ".md")
    with open(path, "w") as f: f.write(content)
    # Trigger Recall re-index
    try: urllib.request.urlopen(urllib.request.Request(VULOS_API + "/api/recall/index", method="POST"), timeout=2)
    except: pass
    return note_id

def delete_note(note_id):
    note_id = _sanitize_note_id(note_id)
    path = os.path.join(DATA_DIR, note_id + ".md")
    if os.path.exists(path): os.remove(path)


class NotesHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/":
            self.serve_file(os.path.join(APP_DIR, "index.html"), "text/html")
        elif self.path == "/api/notes":
            self.send_json(list_notes())
        elif self.path.startswith("/api/notes/"):
            note_id = self.path.split("/api/notes/")[1]
            try:
                content = get_note(note_id)
            except ValueError:
                self.send_error(400, "Invalid note id")
                return
            if content is None:
                self.send_error(404)
            else:
                self.send_response(200)
                self.send_header("Content-Type", "text/plain")
                self.send_header("Content-Security-Policy", CSP)
                self.end_headers()
                self.wfile.write(content.encode())
        else:
            self.serve_static()

    def serve_static(self):
        """Serve an app static asset (collab.js, vendor/*, src/*) from APP_DIR.

        Two guards, both required: an extension allow-list (STATIC_TYPES) so only
        web assets are servable at all, and a realpath containment check so a
        crafted path (``..``/encoded/symlink) can never escape the app directory.
        """
        raw = urllib.parse.unquote(self.path.split("?", 1)[0].split("#", 1)[0])
        rel = raw.lstrip("/")
        ext = os.path.splitext(rel)[1].lower()
        if not rel or ext not in STATIC_TYPES:
            self.send_error(404)
            return
        target = os.path.realpath(os.path.join(APP_DIR, rel))
        # Must stay strictly within the app dir (blocks traversal + symlink escape).
        if target != _APP_DIR_REAL and not target.startswith(_APP_DIR_REAL + os.sep):
            self.send_error(404)
            return
        if not os.path.isfile(target):
            self.send_error(404)
            return
        self.serve_file(target, STATIC_TYPES[ext])

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length).decode() if length else ""
        if self.path == "/api/notes":
            note_id = save_note(None, body)
            self.send_json({"id": note_id})
        elif self.path.startswith("/api/notes/"):
            note_id = self.path.split("/api/notes/")[1]
            try:
                note_id = save_note(note_id, body)
            except ValueError:
                self.send_error(400, "Invalid note id")
                return
            self.send_json({"id": note_id})

    def do_DELETE(self):
        if self.path.startswith("/api/notes/"):
            note_id = self.path.split("/api/notes/")[1]
            try:
                delete_note(note_id)
            except ValueError:
                self.send_error(400, "Invalid note id")
                return
            self.send_json({"status": "deleted"})

    def serve_file(self, filepath, content_type):
        try:
            with open(filepath, "rb") as f:
                data = f.read()
            self.send_response(200)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(data)))
            self.send_header("Content-Security-Policy", CSP)
            self.end_headers()
            self.wfile.write(data)
        except FileNotFoundError:
            self.send_error(404)

    def send_json(self, data):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Security-Policy", CSP)
        self.end_headers()
        self.wfile.write(json.dumps(data).encode())

    def log_message(self, format, *args): pass

print(f"[notes] Universal Memory on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), NotesHandler).serve_forever()
