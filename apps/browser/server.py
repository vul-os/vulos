"""Vula OS — Smart Browser
Ad-stripping web viewer with AI summarization.
Proxies pages through the server, strips ads/trackers, optionally summarizes.
"""
import http.server
import ipaddress
import json
import os
import re
import socket
import urllib.parse
import urllib.request
import urllib.error
from html.parser import HTMLParser

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
VULOS_API = os.environ.get("VULOS_API", "http://localhost:8080")
APP_DIR = os.path.dirname(os.path.abspath(__file__))

CSP = (
    "default-src 'self'; "
    "script-src 'self' 'unsafe-inline'; "
    "style-src 'self' 'unsafe-inline'; "
    "object-src 'none'; "
    "frame-ancestors 'none'; "
    "base-uri 'none'"
)

# Ad/tracker domain blocklist — loaded from file if available, else defaults
_DEFAULT_AD_DOMAINS = {
    "doubleclick.net", "googlesyndication.com", "googleadservices.com",
    "facebook.net", "fbcdn.net", "analytics.google.com",
    "amazon-adsystem.com", "adnxs.com", "adsrvr.org",
    "criteo.com", "outbrain.com", "taboola.com",
    "scorecardresearch.com", "quantserve.com", "bluekai.com",
    "moatads.com", "2mdn.net", "serving-sys.com",
    "smartadserver.com", "pubmatic.com", "rubiconproject.com",
    "openx.net", "casalemedia.com", "lijit.com",
    "mathtag.com", "turn.com", "nexac.com",
    "demdex.net", "krxd.net", "exelator.com",
    "agkn.com", "rlcdn.com", "bidswitch.net",
    "contextweb.com", "spotxchange.com", "yieldmanager.com",
    "googletagmanager.com", "googletagservices.com",
    "googlesyndication.com", "google-analytics.com",
    "hotjar.com", "fullstory.com", "mouseflow.com",
    "clarity.ms", "newrelic.com", "nr-data.net",
}

def load_blocklist():
    """Load blocklist from EasyList-format file if available."""
    domains = set(_DEFAULT_AD_DOMAINS)
    blocklist_path = os.path.join(APP_DIR, "blocklist.txt")
    if os.path.exists(blocklist_path):
        with open(blocklist_path) as f:
            for line in f:
                line = line.strip()
                if not line or line.startswith("!") or line.startswith("["):
                    continue
                if line.startswith("||") and line.endswith("^"):
                    domains.add(line[2:-1])
                elif "." in line and " " not in line and "/" not in line:
                    domains.add(line)
    return domains

AD_DOMAINS = load_blocklist()

# ---------------------------------------------------------------------------
# H2: SSRF guard — allow only http/https to public, non-private addresses
# ---------------------------------------------------------------------------

# Private/reserved network ranges (IPv4 and IPv6)
_PRIVATE_NETS_V4 = [
    ipaddress.IPv4Network("127.0.0.0/8"),      # loopback
    ipaddress.IPv4Network("10.0.0.0/8"),        # RFC 1918
    ipaddress.IPv4Network("172.16.0.0/12"),     # RFC 1918
    ipaddress.IPv4Network("192.168.0.0/16"),    # RFC 1918
    ipaddress.IPv4Network("169.254.0.0/16"),    # link-local
    ipaddress.IPv4Network("100.64.0.0/10"),     # shared address (CGNAT)
    ipaddress.IPv4Network("192.0.0.0/24"),      # IETF Protocol Assignments
    ipaddress.IPv4Network("192.0.2.0/24"),      # TEST-NET-1
    ipaddress.IPv4Network("198.51.100.0/24"),   # TEST-NET-2
    ipaddress.IPv4Network("203.0.113.0/24"),    # TEST-NET-3
    ipaddress.IPv4Network("224.0.0.0/4"),       # multicast
    ipaddress.IPv4Network("240.0.0.0/4"),       # reserved
    ipaddress.IPv4Network("0.0.0.0/8"),         # "this" network
]

_PRIVATE_NETS_V6 = [
    ipaddress.IPv6Network("::1/128"),           # loopback
    ipaddress.IPv6Network("fc00::/7"),          # unique local
    ipaddress.IPv6Network("fe80::/10"),         # link-local
    ipaddress.IPv6Network("ff00::/8"),          # multicast
    ipaddress.IPv6Network("::/128"),            # unspecified
]


def _is_private_address(addr_str):
    """Return True if addr_str is a private/reserved IP address."""
    try:
        addr = ipaddress.ip_address(addr_str)
        if isinstance(addr, ipaddress.IPv4Address):
            return any(addr in net for net in _PRIVATE_NETS_V4)
        else:
            # Also handle IPv4-mapped IPv6 (::ffff:192.168.x.x)
            if addr.ipv4_mapped is not None:
                mapped = addr.ipv4_mapped
                return any(mapped in net for net in _PRIVATE_NETS_V4)
            return any(addr in net for net in _PRIVATE_NETS_V6)
    except ValueError:
        return True  # treat unparseable addresses as private/unsafe


def _validate_url(url):
    """Validate that url is http/https and resolves to a public address.

    Raises ValueError with a descriptive message on rejection.
    Returns the validated url string unchanged.
    """
    parsed = urllib.parse.urlparse(url)
    if parsed.scheme not in ("http", "https"):
        raise ValueError(f"Scheme '{parsed.scheme}' not allowed; only http/https are permitted")

    host = parsed.hostname
    if not host:
        raise ValueError("No host in URL")

    # Resolve all addresses for the host and check every one
    try:
        results = socket.getaddrinfo(host, None)
    except socket.gaierror as exc:
        raise ValueError(f"DNS resolution failed for '{host}': {exc}")

    if not results:
        raise ValueError(f"No addresses resolved for '{host}'")

    for family, _type, _proto, _canonname, sockaddr in results:
        ip_str = sockaddr[0]
        if _is_private_address(ip_str):
            raise ValueError(
                f"Host '{host}' resolves to private/reserved address '{ip_str}' — request blocked"
            )

    return url


class _NoRedirectHandler(urllib.request.HTTPRedirectHandler):
    """Redirect handler that re-validates the destination before following."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        try:
            _validate_url(newurl)
        except ValueError as exc:
            raise urllib.error.URLError(f"Redirect to blocked URL: {exc}")
        return super().redirect_request(req, fp, code, msg, headers, newurl)


_OPENER = urllib.request.build_opener(_NoRedirectHandler)

MAX_RESPONSE_BYTES = 5 * 1024 * 1024  # 5 MB cap


def _safe_fetch(url, timeout=10):
    """Validate url then fetch it, returning response bytes (capped at MAX_RESPONSE_BYTES)."""
    _validate_url(url)
    req = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0 (compatible; VulaOS)"})
    resp = _OPENER.open(req, timeout=timeout)
    return resp.read(MAX_RESPONSE_BYTES).decode("utf-8", errors="replace")


class AdStripper(HTMLParser):
    """Strips ad-related elements from HTML — scripts, iframes, images, divs with ad classes."""
    AD_CLASSES = {"ad", "ads", "advert", "advertisement", "banner-ad", "ad-container", "ad-wrapper",
                  "google-ad", "sponsored", "promoted", "dfp-ad", "ad-slot", "ad-unit"}

    def __init__(self):
        super().__init__()
        self.output = []
        self.skip = False
        self.skip_depth = 0

    def _is_ad(self, tag, attrs):
        attrs_dict = dict(attrs)
        for attr in ("src", "href", "data-src"):
            val = attrs_dict.get(attr, "")
            if val and any(ad in val for ad in AD_DOMAINS):
                return True
        classes = attrs_dict.get("class", "").lower().split()
        if any(c in self.AD_CLASSES for c in classes):
            return True
        elem_id = attrs_dict.get("id", "").lower()
        if any(ad in elem_id for ad in ("ad-", "ads-", "advert", "banner-ad", "google_ads")):
            return True
        return False

    def handle_starttag(self, tag, attrs):
        if self.skip:
            self.skip_depth += 1
            return
        if tag in ("script", "iframe", "img", "div", "aside", "section") and self._is_ad(tag, attrs):
            self.skip = True
            self.skip_depth = 1
            return
        attr_str = " ".join(f'{k}="{v}"' for k, v in attrs)
        self.output.append(f"<{tag} {attr_str}>" if attr_str else f"<{tag}>")

    def handle_endtag(self, tag):
        if self.skip:
            self.skip_depth -= 1
            if self.skip_depth <= 0:
                self.skip = False
                self.skip_depth = 0
            return
        if not self.skip:
            self.output.append(f"</{tag}>")

    def handle_data(self, data):
        if not self.skip:
            self.output.append(data)

    def get_output(self):
        return "".join(self.output)


class BrowserHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/" or self.path == "":
            self.serve_file(os.path.join(APP_DIR, "index.html"), "text/html")
        elif self.path.startswith("/browse?url="):
            self.handle_browse()
        else:
            self.send_error(404)

    def do_POST(self):
        # Proxy /api/ai/* to the Vulos OS backend (AIROT-05).
        if self.path.startswith("/api/ai/"):
            self.handle_ai_proxy()
        else:
            self.send_error(404)

    def handle_browse(self):
        url = urllib.parse.unquote(self.path.split("url=", 1)[1])
        try:
            content = _safe_fetch(url, timeout=10)
            stripper = AdStripper()
            stripper.feed(content)
            clean = stripper.get_output()
            self.send_html(clean)
        except ValueError as e:
            self.send_html(f"<html><body style='background:#0a0a0a;color:#e5e5e5;padding:20px'><h2>Blocked</h2><p>{e}</p></body></html>")
        except Exception as e:
            self.send_html(f"<html><body style='background:#0a0a0a;color:#e5e5e5;padding:20px'><h2>Error</h2><p>{e}</p></body></html>")

    def handle_ai_proxy(self):
        """Proxy /api/ai/* to the Vulos OS backend and stream the SSE response.

        AIROT-05: The browser frontend POSTs directly to /api/ai/chat on this
        server; we forward to VULOS_API (the OS backend) and pipe the SSE
        stream back so the client receives live chunks.
        """
        content_length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(content_length) if content_length > 0 else b""

        target_url = VULOS_API + self.path
        try:
            req = urllib.request.Request(
                target_url,
                data=body if body else None,
                headers={"Content-Type": "application/json"},
                method="POST",
            )
            resp = urllib.request.urlopen(req, timeout=120)

            self.send_response(resp.status)
            # Forward relevant headers from the backend response.
            for key in ("Content-Type", "Cache-Control", "X-Accel-Buffering"):
                val = resp.headers.get(key)
                if val:
                    self.send_header(key, val)
            self.send_header("Content-Security-Policy", CSP)
            self.end_headers()

            # Stream response body in 4 KiB chunks.
            chunk_size = 4096
            while True:
                chunk = resp.read(chunk_size)
                if not chunk:
                    break
                self.wfile.write(chunk)
                self.wfile.flush()

        except urllib.error.HTTPError as e:
            body_err = e.read(4096)
            self.send_response(e.code)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Security-Policy", CSP)
            self.end_headers()
            self.wfile.write(body_err)
        except Exception as e:
            self.send_response(502)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Security-Policy", CSP)
            self.end_headers()
            self.wfile.write(json.dumps({"error": str(e)}).encode())

    def serve_file(self, filepath, content_type):
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

    def send_html(self, html):
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Security-Policy", CSP)
        self.end_headers()
        self.wfile.write(html.encode())

    def log_message(self, format, *args):
        pass

print(f"[browser] Smart Browser on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), BrowserHandler).serve_forever()
