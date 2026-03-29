"""Vula OS — Universal Memory (Notes)
Every thought indexed by Recall. Markdown editor with instant search.
"""
import http.server
import json
import os
import time
import urllib.request

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
VULOS_API = os.environ.get("VULOS_API", "http://localhost:8080")
DATA_DIR = os.environ.get("NOTES_DIR", os.path.expanduser("~/.vulos/data/notes"))
APP_DIR = os.path.dirname(os.path.abspath(__file__))
os.makedirs(DATA_DIR, exist_ok=True)

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
    path = os.path.join(DATA_DIR, note_id + ".md")
    if not os.path.exists(path): return None
    with open(path) as f: return f.read()

def save_note(note_id, content):
    if not note_id:
        note_id = str(int(time.time() * 1000))
    path = os.path.join(DATA_DIR, note_id + ".md")
    with open(path, "w") as f: f.write(content)
    # Trigger Recall re-index
    try: urllib.request.urlopen(urllib.request.Request(VULOS_API + "/api/recall/index", method="POST"), timeout=2)
    except: pass
    return note_id

def delete_note(note_id):
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
            content = get_note(note_id)
            if content is None:
                self.send_error(404)
            else:
                self.send_response(200)
                self.send_header("Content-Type", "text/plain")
                self.end_headers()
                self.wfile.write(content.encode())
        else:
            self.send_error(404)

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

    def do_DELETE(self):
        if self.path.startswith("/api/notes/"):
            note_id = self.path.split("/api/notes/")[1]
            delete_note(note_id)
            self.send_json({"status": "deleted"})

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

print(f"[notes] Universal Memory on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), NotesHandler).serve_forever()
