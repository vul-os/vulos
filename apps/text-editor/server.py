"""Vula OS - Text Editor
Tabbed localStorage-backed plain text and code editor.
"""
import http.server
import os

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
APP_DIR = os.path.dirname(os.path.abspath(__file__))

CONTENT_TYPES = {
    ".html": "text/html; charset=utf-8",
    ".svg": "image/svg+xml",
}


class TextEditorHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        path = self.path.split("?", 1)[0]
        if path == "/":
            self.serve_file("index.html")
        elif path == "/icon.svg":
            self.serve_file("icon.svg")
        else:
            self.send_error(404)

    def serve_file(self, name):
        filepath = os.path.join(APP_DIR, name)
        try:
            with open(filepath, "rb") as f:
                data = f.read()
            _, ext = os.path.splitext(filepath)
            self.send_response(200)
            self.send_header("Content-Type", CONTENT_TYPES.get(ext, "application/octet-stream"))
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        except FileNotFoundError:
            self.send_error(404)

    def log_message(self, format, *args):
        pass


print(f"[text-editor] Text Editor on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), TextEditorHandler).serve_forever()
