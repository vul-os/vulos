"""Vula OS — Universal Memory (Notes)
Every thought indexed by Recall. Markdown editor with instant search.
Real-time collaboration via the Peering layer (PEER-32).
"""
import http.server
import json
import os
import time
import urllib.request
import urllib.error

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
VULOS_API = os.environ.get("VULOS_API", "http://localhost:8080")
DATA_DIR = os.environ.get("NOTES_DIR", os.path.expanduser("~/.vulos/data/notes"))
APP_DIR = os.path.dirname(os.path.abspath(__file__))
os.makedirs(DATA_DIR, exist_ok=True)

# ── Note helpers ────────────────────────────────────────────────────────────

def list_notes():
    notes = []
    for f in sorted(os.listdir(DATA_DIR), reverse=True):
        if f.endswith(".md"):
            path = os.path.join(DATA_DIR, f)
            with open(path) as fh:
                content = fh.read()
            title = content.split("\n")[0].lstrip("# ").strip() or f
            notes.append({
                "id": f[:-3],
                "title": title,
                "preview": content[:200],
                "modified": os.path.getmtime(path),
            })
    return notes

def get_note(note_id):
    path = os.path.join(DATA_DIR, note_id + ".md")
    if not os.path.exists(path):
        return None
    with open(path) as f:
        return f.read()

def save_note(note_id, content):
    if not note_id:
        note_id = str(int(time.time() * 1000))
    path = os.path.join(DATA_DIR, note_id + ".md")
    with open(path, "w") as f:
        f.write(content)
    # Trigger Recall re-index (best-effort)
    try:
        urllib.request.urlopen(
            urllib.request.Request(VULOS_API + "/api/recall/index", method="POST"),
            timeout=2,
        )
    except Exception:
        pass
    return note_id

def delete_note(note_id):
    path = os.path.join(DATA_DIR, note_id + ".md")
    if os.path.exists(path):
        os.remove(path)

# ── Collab helpers ──────────────────────────────────────────────────────────

def proxy_post_json(path, payload):
    """Forward a JSON POST to the Vula gateway API. Returns (status, body_bytes)."""
    data = json.dumps(payload).encode()
    req = urllib.request.Request(
        VULOS_API + path,
        data=data,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()
    except Exception as ex:
        return 503, str(ex).encode()

def proxy_get_json(path):
    """Forward a GET to the Vula gateway API. Returns (status, body_bytes)."""
    req = urllib.request.Request(VULOS_API + path, method="GET")
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()
    except Exception as ex:
        return 503, str(ex).encode()

# ── HTTP handler ────────────────────────────────────────────────────────────

class NotesHandler(http.server.BaseHTTPRequestHandler):

    # ── GET ──────────────────────────────────────────────────────────────────
    def do_GET(self):
        if self.path == "/":
            self.serve_file(os.path.join(APP_DIR, "index.html"), "text/html")
        elif self.path == "/collab.js":
            self.serve_file(os.path.join(APP_DIR, "collab.js"), "application/javascript")
        elif self.path == "/api/notes":
            self.send_json(list_notes())
        elif self.path.startswith("/api/notes/"):
            note_id = self.path.split("/api/notes/")[1]
            content = get_note(note_id)
            if content is None:
                self.send_error(404)
            else:
                self.send_response(200)
                self.send_header("Content-Type", "text/plain")
                self.end_headers()
                self.wfile.write(content.encode())
        # ── Collab document list (proxied) ──────────────────────────────────
        elif self.path == "/api/peering/collab/documents":
            status, body = proxy_get_json("/api/peering/collab/documents")
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Access-Control-Allow-Origin", "*")
            self.end_headers()
            self.wfile.write(body)
        # ── Collab profile (for display name) ──────────────────────────────
        elif self.path == "/api/peering/profile":
            status, body = proxy_get_json("/api/peering/profile")
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Access-Control-Allow-Origin", "*")
            self.end_headers()
            self.wfile.write(body)
        else:
            self.send_error(404)

    # ── POST ─────────────────────────────────────────────────────────────────
    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length).decode() if length else ""

        if self.path == "/api/notes":
            note_id = save_note(None, body)
            self.send_json({"id": note_id})
        elif self.path.startswith("/api/notes/"):
            note_id = self.path.split("/api/notes/")[1]
            save_note(note_id, body)
            self.send_json({"id": note_id})
        # ── Share a note via Peering layer (PEER-31/32) ────────────────────
        elif self.path == "/api/peering/collab/share":
            try:
                payload = json.loads(body) if body else {}
            except json.JSONDecodeError:
                self.send_error(400, "Invalid JSON")
                return
            status, resp_body = proxy_post_json("/api/peering/collab/share", payload)
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Access-Control-Allow-Origin", "*")
            self.end_headers()
            self.wfile.write(resp_body)
        else:
            self.send_error(404)

    # ── DELETE ───────────────────────────────────────────────────────────────
    def do_DELETE(self):
        if self.path.startswith("/api/notes/"):
            note_id = self.path.split("/api/notes/")[1]
            delete_note(note_id)
            self.send_json({"status": "deleted"})
        else:
            self.send_error(404)

    # ── OPTIONS (CORS preflight) ──────────────────────────────────────────────
    def do_OPTIONS(self):
        self.send_response(204)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Content-Type")
        self.end_headers()

    # ── Helpers ───────────────────────────────────────────────────────────────
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

    def log_message(self, format, *args):
        pass


print(f"[notes] Universal Memory on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), NotesHandler).serve_forever()
