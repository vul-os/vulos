"""Vula OS - Weather
Static weather dashboard. Browser fetches geocoding and forecast data from Open-Meteo.
"""
import http.server
import os

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
APP_DIR = os.path.dirname(os.path.abspath(__file__))

CONTENT_TYPES = {
    ".html": "text/html; charset=utf-8",
    ".svg": "image/svg+xml",
}


class WeatherHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path in ("", "/"):
            self.serve_file("index.html")
        elif self.path == "/icon.svg":
            self.serve_file("icon.svg")
        else:
            self.send_error(404)

    def serve_file(self, filename):
        filepath = os.path.join(APP_DIR, filename)
        try:
            with open(filepath, "rb") as f:
                data = f.read()
            ext = os.path.splitext(filename)[1]
            self.send_response(200)
            self.send_header("Content-Type", CONTENT_TYPES.get(ext, "application/octet-stream"))
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
        except FileNotFoundError:
            self.send_error(404)

    def log_message(self, format, *args):
        pass


print(f"[weather] Weather on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), WeatherHandler).serve_forever()
