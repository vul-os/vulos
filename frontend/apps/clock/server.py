"""Vulos OS - Clock
Static stdlib server for the Clock web app.
"""
import http.server
import os
from urllib.parse import unquote, urlparse

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
APP_DIR = os.path.dirname(os.path.abspath(__file__))


class ClockHandler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=APP_DIR, **kwargs)

    def end_headers(self):
        self.send_header("Cache-Control", "no-store")
        super().end_headers()

    def do_GET(self):
        path = unquote(urlparse(self.path).path)
        if path == "/":
            self.path = "/index.html"
        return super().do_GET()


if __name__ == "__main__":
    with http.server.ThreadingHTTPServer(("0.0.0.0", PORT), ClockHandler) as httpd:
        print(f"Clock app serving on http://localhost:{PORT}")
        httpd.serve_forever()
