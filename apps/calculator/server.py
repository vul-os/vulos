"""Vula OS - Calculator
Standard and scientific calculator with local browser history.
"""
import http.server
import mimetypes
import os

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
APP_DIR = os.path.dirname(os.path.abspath(__file__))


class CalculatorHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        path = self.path.split("?", 1)[0]
        if path == "/":
            path = "/index.html"
        filepath = os.path.realpath(os.path.join(APP_DIR, path.lstrip("/")))
        if not filepath.startswith(APP_DIR) or not os.path.isfile(filepath):
            self.send_error(404)
            return
        self.serve_file(filepath)

    def serve_file(self, filepath):
        content_type = mimetypes.guess_type(filepath)[0] or "application/octet-stream"
        with open(filepath, "rb") as f:
            data = f.read()
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, format, *args):
        pass


print(f"[calculator] Calculator on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), CalculatorHandler).serve_forever()
