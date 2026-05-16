"""Vula OS — Text Editor
Code & plain text editor with syntax highlighting and live collaboration (PEER-33).
Yjs-based co-editing over /api/peering/collab WS + remote cursors + Share button.
"""
import http.server
import json
import os
import time
import urllib.error
import urllib.parse
import urllib.request

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
VULOS_API = os.environ.get("VULOS_API", "http://localhost:8080")
DATA_DIR = os.environ.get("EDITOR_DIR", os.path.expanduser("~/.vulos/data/text-editor"))
APP_DIR = os.path.dirname(os.path.abspath(__file__))
os.makedirs(DATA_DIR, exist_ok=True)


# ---------------------------------------------------------------------------
# File helpers
# ---------------------------------------------------------------------------

def list_files():
    files = []
    for name in sorted(os.listdir(DATA_DIR)):
        path = os.path.join(DATA_DIR, name)
        if os.path.isfile(path):
            files.append({
                "name": name,
                "modified": os.path.getmtime(path),
                "size": os.path.getsize(path),
            })
    return files


def read_file(name):
    path = os.path.join(DATA_DIR, name)
    if not os.path.exists(path):
        return None
    with open(path, "r", errors="replace") as f:
        return f.read()


def write_file(name, content):
    # Sanitise: no path traversal
    name = os.path.basename(name)
    if not name:
        name = "untitled-" + str(int(time.time() * 1000)) + ".txt"
    path = os.path.join(DATA_DIR, name)
    with open(path, "w") as f:
        f.write(content)
    return name


def delete_file(name):
    path = os.path.join(DATA_DIR, os.path.basename(name))
    if os.path.exists(path):
        os.remove(path)


# ---------------------------------------------------------------------------
# Peering collab proxy helpers
# ---------------------------------------------------------------------------

def collab_share(payload_bytes):
    """Proxy POST /api/peering/collab/share to the Vula backend."""
    url = VULOS_API + "/api/peering/collab/share"
    req = urllib.request.Request(
        url,
        data=payload_bytes,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()
    except Exception as e:
        return 502, json.dumps({"error": str(e)}).encode()


# ---------------------------------------------------------------------------
# HTTP handler
# ---------------------------------------------------------------------------

class TextEditorHandler(http.server.BaseHTTPRequestHandler):

    # ------------------------------------------------------------------
    # GET
    # ------------------------------------------------------------------
    def do_GET(self):
        if self.path in ("/", ""):
            self.serve_file(os.path.join(APP_DIR, "index.html"), "text/html")

        elif self.path == "/api/files":
            self.send_json(list_files())

        elif self.path.startswith("/api/files/"):
            name = urllib.parse.unquote(self.path[len("/api/files/"):])
            content = read_file(name)
            if content is None:
                self.send_error(404)
            else:
                data = content.encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "text/plain; charset=utf-8")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        else:
            self.send_error(404)

    # ------------------------------------------------------------------
    # POST
    # ------------------------------------------------------------------
    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length) if length else b""

        if self.path == "/api/files":
            # Create a new untitled file
            name = write_file("", body.decode("utf-8", errors="replace"))
            self.send_json({"name": name})

        elif self.path.startswith("/api/files/"):
            name = urllib.parse.unquote(self.path[len("/api/files/"):])
            name = write_file(name, body.decode("utf-8", errors="replace"))
            self.send_json({"name": name})

        elif self.path == "/api/peering/collab/share":
            # Proxy to Vula backend peering API
            status, resp_body = collab_share(body)
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Access-Control-Allow-Origin", "*")
            self.end_headers()
            self.wfile.write(resp_body)

        else:
            self.send_error(404)

    # ------------------------------------------------------------------
    # DELETE
    # ------------------------------------------------------------------
    def do_DELETE(self):
        if self.path.startswith("/api/files/"):
            name = urllib.parse.unquote(self.path[len("/api/files/"):])
            delete_file(name)
            self.send_json({"status": "deleted"})
        else:
            self.send_error(404)

    # ------------------------------------------------------------------
    # OPTIONS (CORS pre-flight)
    # ------------------------------------------------------------------
    def do_OPTIONS(self):
        self.send_response(204)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Content-Type")
        self.end_headers()

    # ------------------------------------------------------------------
    # Helpers
    # ------------------------------------------------------------------
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
        encoded = json.dumps(data).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
        self.wfile.write(encoded)

    def log_message(self, format, *args):
        pass  # silence per-request logs


print(f"[text-editor] Text Editor on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), TextEditorHandler).serve_forever()
