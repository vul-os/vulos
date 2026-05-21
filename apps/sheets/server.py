"""Vula OS — Sheets
Collaborative spreadsheet server.  Serves index.html and provides a minimal
REST API for sheet metadata (list, create, delete).  Cell data is stored as
Yjs CRDT state via the main Vula backend's peering/collab endpoints.
"""
import http.server
import json
import os
import time
import urllib.parse

PORT  = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
DATA_DIR = os.environ.get("SHEETS_DIR", os.path.expanduser("~/.vulos/data/sheets"))
APP_DIR  = os.path.dirname(os.path.abspath(__file__))
os.makedirs(DATA_DIR, exist_ok=True)

_DATA_DIR_REAL = os.path.realpath(DATA_DIR)

CSP = (
    "default-src 'self'; "
    "script-src 'self' 'unsafe-inline' https://esm.sh; "
    "style-src 'self' 'unsafe-inline'; "
    "connect-src 'self' wss: ws:; "
    "object-src 'none'; "
    "frame-ancestors 'none'; "
    "base-uri 'none'"
)


def _safe_sheet_id(sheet_id):
    """Validate and return a safe sheet ID (no path traversal)."""
    if not sheet_id:
        raise ValueError("empty sheet_id")
    if "/" in sheet_id or "\\" in sheet_id or ".." in sheet_id:
        raise ValueError("invalid sheet_id")
    safe = os.path.basename(sheet_id)
    if not safe or safe != sheet_id:
        raise ValueError("invalid sheet_id")
    candidate = os.path.realpath(os.path.join(DATA_DIR, safe + ".json"))
    if not candidate.startswith(_DATA_DIR_REAL + os.sep):
        raise ValueError("path escapes DATA_DIR")
    return safe


def list_sheets():
    out = []
    for f in sorted(os.listdir(DATA_DIR), reverse=True):
        if f.endswith(".json"):
            path = os.path.join(DATA_DIR, f)
            try:
                with open(path) as fh:
                    meta = json.load(fh)
                out.append(meta)
            except Exception:
                pass
    return out


def get_sheet_meta(sheet_id):
    sheet_id = _safe_sheet_id(sheet_id)
    path = os.path.join(DATA_DIR, sheet_id + ".json")
    if not os.path.exists(path):
        return None
    with open(path) as fh:
        return json.load(fh)


def create_sheet(title="Untitled Sheet"):
    sheet_id = str(int(time.time() * 1000))
    meta = {
        "id": sheet_id,
        "title": title,
        "created_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "updated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    path = os.path.join(DATA_DIR, sheet_id + ".json")
    with open(path, "w") as fh:
        json.dump(meta, fh)
    return meta


def update_sheet_meta(sheet_id, title):
    sheet_id = _safe_sheet_id(sheet_id)
    path = os.path.join(DATA_DIR, sheet_id + ".json")
    if not os.path.exists(path):
        return None
    with open(path) as fh:
        meta = json.load(fh)
    meta["title"] = title
    meta["updated_at"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    with open(path, "w") as fh:
        json.dump(meta, fh)
    return meta


def delete_sheet(sheet_id):
    sheet_id = _safe_sheet_id(sheet_id)
    path = os.path.join(DATA_DIR, sheet_id + ".json")
    if os.path.exists(path):
        os.remove(path)


class SheetsHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path in ("/", "/index.html"):
            self._serve_file(os.path.join(APP_DIR, "index.html"), "text/html")
        elif self.path == "/icon.svg":
            self._serve_file(os.path.join(APP_DIR, "icon.svg"), "image/svg+xml")
        elif self.path == "/api/sheets":
            self._send_json(list_sheets())
        elif self.path.startswith("/api/sheets/"):
            sheet_id = urllib.parse.unquote(self.path.split("/api/sheets/")[1].split("?")[0])
            try:
                meta = get_sheet_meta(sheet_id)
            except ValueError:
                self.send_error(400, "Invalid sheet id")
                return
            if meta is None:
                self.send_error(404)
            else:
                self._send_json(meta)
        else:
            self.send_error(404)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length).decode() if length else "{}"
        if self.path == "/api/sheets":
            try:
                data = json.loads(body) if body else {}
            except Exception:
                data = {}
            title = data.get("title", "Untitled Sheet")
            meta = create_sheet(title)
            self._send_json(meta, status=201)
        elif self.path.startswith("/api/sheets/"):
            sheet_id = urllib.parse.unquote(self.path.split("/api/sheets/")[1].split("?")[0])
            try:
                data = json.loads(body) if body else {}
                title = data.get("title")
                if title is None:
                    self.send_error(400, "title required")
                    return
                meta = update_sheet_meta(sheet_id, title)
            except ValueError:
                self.send_error(400, "Invalid sheet id")
                return
            if meta is None:
                self.send_error(404)
            else:
                self._send_json(meta)
        else:
            self.send_error(404)

    def do_DELETE(self):
        if self.path.startswith("/api/sheets/"):
            sheet_id = urllib.parse.unquote(self.path.split("/api/sheets/")[1].split("?")[0])
            try:
                delete_sheet(sheet_id)
            except ValueError:
                self.send_error(400, "Invalid sheet id")
                return
            self._send_json({"status": "deleted"})
        else:
            self.send_error(404)

    def _serve_file(self, filepath, content_type):
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

    def _send_json(self, data, status=200):
        body = json.dumps(data).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Content-Security-Policy", CSP)
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format, *args):
        pass


print(f"[sheets] Vula OS Sheets on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), SheetsHandler).serve_forever()
