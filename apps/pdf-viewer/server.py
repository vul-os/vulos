"""Vula OS - PDF Viewer
Static local PDF reader shell. PDF files are opened client-side via file input.
"""
import http.server
import os

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
APP_DIR = os.path.dirname(os.path.abspath(__file__))


class PDFViewerHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path in ("", "/"):
            self.serve_file(os.path.join(APP_DIR, "index.html"), "text/html; charset=utf-8")
        elif self.path == "/icon.svg":
            self.serve_file(os.path.join(APP_DIR, "icon.svg"), "image/svg+xml")
        else:
            self.send_error(404)

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

    def log_message(self, format, *args):
        pass


print(f"[pdf-viewer] PDF Viewer on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), PDFViewerHandler).serve_forever()
