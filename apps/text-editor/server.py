"""Vula OS - Text Editor
Tabbed localStorage-backed code editor powered by CodeMirror 6.
All assets are served locally — no external network requests.
"""
import http.server
import os
import posixpath

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
APP_DIR = os.path.dirname(os.path.abspath(__file__))

CONTENT_TYPES = {
    ".html": "text/html; charset=utf-8",
    ".js":   "application/javascript; charset=utf-8",
    ".css":  "text/css; charset=utf-8",
    ".svg":  "image/svg+xml",
    ".ico":  "image/x-icon",
    ".woff2":"font/woff2",
    ".woff": "font/woff",
    ".ttf":  "font/ttf",
}

# Allowed sub-paths (whitelist to prevent directory traversal)
ALLOWED_DIRS = {"vendor"}


class TextEditorHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        path = self.path.split("?", 1)[0].split("#", 1)[0]

        if path == "/" or path == "/index.html":
            self.serve_file("index.html")
            return

        if path == "/icon.svg":
            self.serve_file("icon.svg")
            return

        # Serve files under /vendor/ and any other whitelisted sub-directory
        parts = [p for p in path.lstrip("/").split("/") if p and p != ".."]
        if parts and parts[0] in ALLOWED_DIRS:
            rel = os.path.join(*parts)
            # Prevent path traversal
            full = os.path.realpath(os.path.join(APP_DIR, rel))
            if full.startswith(os.path.realpath(APP_DIR) + os.sep):
                self.serve_file(rel)
                return

        self.send_error(404)

    def serve_file(self, rel_path):
        filepath = os.path.join(APP_DIR, rel_path)
        try:
            with open(filepath, "rb") as f:
                data = f.read()
            _, ext = os.path.splitext(filepath)
            content_type = CONTENT_TYPES.get(ext.lower(), "application/octet-stream")
            self.send_response(200)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(data)))
            # Cache vendor assets aggressively; HTML never cached
            if rel_path.startswith("vendor"):
                self.send_header("Cache-Control", "public, max-age=31536000, immutable")
            else:
                self.send_header("Cache-Control", "no-cache")
            self.end_headers()
            self.wfile.write(data)
        except FileNotFoundError:
            self.send_error(404)

    def log_message(self, fmt, *args):  # silence access log
        pass


print(f"[text-editor] serving on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), TextEditorHandler).serve_forever()
