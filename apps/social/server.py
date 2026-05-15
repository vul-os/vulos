"""Vula OS — Fediverse Social Client
Read-only ActivityPub / Mastodon-compatible public timeline viewer.
Proxies requests to any Mastodon-compatible instance to avoid CORS constraints.
"""
import http.server
import json
import os
import ssl
import urllib.request
import urllib.error
import urllib.parse

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
APP_DIR = os.path.dirname(os.path.abspath(__file__))

# Mastodon API endpoints that we allow proxying (read-only whitelist)
ALLOWED_PATHS = {
    "/api/v1/timelines/public",
    "/api/v1/timelines/tag/",
    "/api/v1/trends/statuses",
    "/api/v1/trends/tags",
    "/api/v1/instance",
}


def is_allowed_path(path):
    for allowed in ALLOWED_PATHS:
        if path == allowed or path.startswith(allowed):
            return True
    return False


def proxy_mastodon(host, api_path, query_string=""):
    """Fetch from a Mastodon-compatible instance and return (status, headers, body)."""
    # Sanitise host — strip scheme, path, and credentials
    host = host.strip()
    host = host.removeprefix("https://").removeprefix("http://")
    host = host.split("/")[0]  # strip any path suffix
    host = host.split("@")[-1]  # strip user@ if present

    if not host or "." not in host:
        return 400, {}, b'{"error":"invalid host"}'

    if not is_allowed_path(api_path):
        return 403, {}, b'{"error":"path not permitted"}'

    url = f"https://{host}{api_path}"
    if query_string:
        url += "?" + query_string

    req = urllib.request.Request(
        url,
        headers={
            "User-Agent": "VulaOS-Social/0.1 (+https://vulos.io)",
            "Accept": "application/json",
        },
    )
    try:
        # Create SSL context — don't verify self-signed certs on private instances
        ctx = ssl.create_default_context()
        resp = urllib.request.urlopen(req, timeout=10, context=ctx)
        body = resp.read()
        return 200, {"Content-Type": "application/json"}, body
    except urllib.error.HTTPError as e:
        body = e.read() or b'{"error":"upstream error"}'
        return e.code, {"Content-Type": "application/json"}, body
    except urllib.error.URLError as e:
        msg = json.dumps({"error": f"Cannot reach {host}: {e.reason}"}).encode()
        return 502, {"Content-Type": "application/json"}, msg
    except Exception as e:
        msg = json.dumps({"error": str(e)}).encode()
        return 500, {"Content-Type": "application/json"}, msg


class SocialHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        path = parsed.path
        qs = parsed.query
        params = urllib.parse.parse_qs(qs)

        # Serve the single-page app
        if path in ("/", ""):
            self.serve_file(os.path.join(APP_DIR, "index.html"), "text/html")
            return

        # Proxy endpoint: /proxy?host=mastodon.social&path=/api/v1/timelines/public&limit=20
        if path == "/proxy":
            host = params.get("host", [""])[0]
            api_path = params.get("path", ["/api/v1/timelines/public"])[0]
            # Forward limit and other pagination params
            forward_params = {}
            for k in ("limit", "max_id", "since_id", "min_id", "local", "only_media"):
                if k in params:
                    forward_params[k] = params[k][0]
            forward_qs = urllib.parse.urlencode(forward_params) if forward_params else ""

            status, headers, body = proxy_mastodon(host, api_path, forward_qs)
            self.send_response(status)
            for k, v in headers.items():
                self.send_header(k, v)
            self.send_header("Access-Control-Allow-Origin", "*")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return

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
        pass  # suppress per-request noise


print(f"[social] Fediverse on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), SocialHandler).serve_forever()
