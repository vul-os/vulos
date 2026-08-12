"""Vulos OS — Phone
Static file server for the Phone app (Messages + Dialer UI).
The telephony backend is the Vulos Go service at /api/telephony/*.
"""
import http.server
import os

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
APP_DIR = os.path.dirname(os.path.abspath(__file__))

MIME = {
    ".html": "text/html; charset=utf-8",
    ".js":   "application/javascript",
    ".css":  "text/css",
    ".svg":  "image/svg+xml",
    ".ico":  "image/x-icon",
    ".json": "application/json",
    ".png":  "image/png",
    ".woff2": "font/woff2",
}


class PhoneHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        path = self.path.split("?")[0]
        if path == "/" or path == "":
            path = "/index.html"
        if path == "/vulos-tokens.css":
            # Shared design tokens live one level up in apps/_shared/, outside
            # this app's own sandboxed APP_DIR — serve it explicitly rather
            # than through the path-traversal-guarded resolver below.
            self.serve_shared_tokens()
            return
        filepath = os.path.join(APP_DIR, path.lstrip("/"))
        # Only serve files directly inside APP_DIR (no path traversal)
        if not os.path.realpath(filepath).startswith(os.path.realpath(APP_DIR) + os.sep):
            self.send_error(403)
            return
        if os.path.isfile(filepath):
            ext = os.path.splitext(filepath)[1].lower()
            ctype = MIME.get(ext, "application/octet-stream")
            try:
                with open(filepath, "rb") as f:
                    data = f.read()
                self.send_response(200)
                self.send_header("Content-Type", ctype)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)
            except OSError:
                self.send_error(500)
        else:
            # SPA fallback — serve index.html for any unknown path
            self.serve_index()

    def serve_shared_tokens(self):
        filepath = os.path.join(APP_DIR, "..", "_shared", "vulos-tokens.css")
        try:
            with open(filepath, "rb") as f:
                data = f.read()
            self.send_response(200)
            self.send_header("Content-Type", "text/css")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        except FileNotFoundError:
            self.send_error(404)

    def serve_index(self):
        filepath = os.path.join(APP_DIR, "index.html")
        try:
            with open(filepath, "rb") as f:
                data = f.read()
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        except FileNotFoundError:
            self.send_error(404)

    def log_message(self, format, *args):
        pass


print(f"[phone] Phone app on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), PhoneHandler).serve_forever()
