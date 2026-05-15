"""Vula OS - PDF Viewer
PDF.js-powered local PDF reader. All assets (pdf.mjs, pdf.worker.mjs) are
bundled in the app directory and served locally — no external network requests.
"""
import http.server
import os

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
APP_DIR = os.path.dirname(os.path.abspath(__file__))

# Map request path → (relative filesystem path, MIME type)
# Only explicitly listed paths are served; everything else gets a 404.
ROUTES = {
    "/":                         ("index.html",               "text/html; charset=utf-8"),
    "/index.html":               ("index.html",               "text/html; charset=utf-8"),
    "/icon.svg":                 ("icon.svg",                 "image/svg+xml"),
    "/pdfjs/pdf.mjs":            ("pdfjs/pdf.mjs",            "text/javascript; charset=utf-8"),
    "/pdfjs/pdf.worker.mjs":     ("pdfjs/pdf.worker.mjs",     "text/javascript; charset=utf-8"),
}


class PDFViewerHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        route = ROUTES.get(self.path)
        if route is None:
            self.send_error(404)
            return
        rel_path, content_type = route
        self.serve_file(os.path.join(APP_DIR, rel_path), content_type)

    def serve_file(self, filepath, content_type):
        try:
            with open(filepath, "rb") as f:
                data = f.read()
            self.send_response(200)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(data)))
            # Allow cross-origin requests for the worker module
            self.send_header("Cross-Origin-Opener-Policy", "same-origin")
            self.send_header("Cross-Origin-Embedder-Policy", "require-corp")
            self.end_headers()
            self.wfile.write(data)
        except FileNotFoundError:
            self.send_error(404)

    def log_message(self, format, *args):
        pass


print(f"[pdf-viewer] PDF Viewer on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), PDFViewerHandler).serve_forever()
