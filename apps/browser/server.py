"""Vula OS — Smart Browser
Ad-stripping web viewer with AI summarization.
Proxies pages through the server, strips ads/trackers, optionally summarizes.
"""
import http.server
import json
import os
import re
import urllib.request
import urllib.error
from html.parser import HTMLParser

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
VULOS_API = os.environ.get("VULOS_API", "http://localhost:8080")
APP_DIR = os.path.dirname(os.path.abspath(__file__))

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
        elif self.path.startswith("/summarize?url="):
            self.handle_summarize()
        else:
            self.send_error(404)

    def handle_browse(self):
        url = self.path.split("url=", 1)[1]
        url = urllib.request.unquote(url)
        try:
            req = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0 (compatible; VulaOS)"})
            resp = urllib.request.urlopen(req, timeout=10)
            content = resp.read().decode("utf-8", errors="replace")
            stripper = AdStripper()
            stripper.feed(content)
            clean = stripper.get_output()
            self.send_html(clean)
        except Exception as e:
            self.send_html(f"<html><body style='background:#0a0a0a;color:#e5e5e5;padding:20px'><h2>Error</h2><p>{e}</p></body></html>")

    def handle_summarize(self):
        url = self.path.split("url=", 1)[1]
        url = urllib.request.unquote(url)
        try:
            req = urllib.request.Request(url, headers={"User-Agent": "Mozilla/5.0 (compatible; VulaOS)"})
            resp = urllib.request.urlopen(req, timeout=10)
            html = resp.read().decode("utf-8", errors="replace")
            text = re.sub(r"<[^>]+>", " ", html)
            text = re.sub(r"\s+", " ", text).strip()[:3000]

            ai_req = urllib.request.Request(
                VULOS_API + "/api/ai/chat",
                data=json.dumps({"messages": [{"role": "user", "content": f"Summarize this web page in 3 bullet points:\n\n{text}"}], "stream": False}).encode(),
                headers={"Content-Type": "application/json"},
            )
            ai_resp = urllib.request.urlopen(ai_req, timeout=30)
            ai_data = json.loads(ai_resp.read())
            summary = ai_data.get("content", "No summary available.")

            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Access-Control-Allow-Origin", "*")
            self.end_headers()
            self.wfile.write(json.dumps({"summary": summary}).encode())
        except Exception as e:
            self.send_response(500)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(json.dumps({"error": str(e)}).encode())

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

    def send_html(self, html):
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.end_headers()
        self.wfile.write(html.encode())

    def log_message(self, format, *args):
        pass

print(f"[browser] Smart Browser on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), BrowserHandler).serve_forever()
