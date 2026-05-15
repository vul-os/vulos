"""Vula OS — Fediverse Social Client
ActivityPub / Mastodon-compatible client with OAuth2 login.

FED-01: read-only public timeline proxy (CORS bypass).
FED-02: dynamic client registration, OAuth2 authorization-code flow,
        token storage in ~/.vulos/data/social/, authenticated home
        timeline, verify-credentials, logout.
"""
import http.server
import json
import os
import secrets
import ssl
import urllib.error
import urllib.parse
import urllib.request

PORT = int(os.environ.get("PORT", os.environ.get("VULOS_PORT", 8080)))
APP_DIR = os.path.dirname(os.path.abspath(__file__))

# Persistent token / app-registration storage
DATA_DIR = os.environ.get("SOCIAL_DATA_DIR",
                           os.path.expanduser("~/.vulos/data/social"))
os.makedirs(DATA_DIR, exist_ok=True)

TOKEN_FILE = os.path.join(DATA_DIR, "token.json")   # { host, access_token, account }
APP_FILE   = os.path.join(DATA_DIR, "apps.json")    # { host: { client_id, client_secret } }
STATE_FILE = os.path.join(DATA_DIR, "oauth_state")  # ephemeral CSRF state ("rand|host")


# ── Token / app-credential helpers ───────────────────────────

def load_token():
    """Return stored token dict or {}."""
    try:
        with open(TOKEN_FILE) as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return {}


def save_token(data):
    with open(TOKEN_FILE, "w") as f:
        json.dump(data, f)


def clear_token():
    if os.path.exists(TOKEN_FILE):
        os.remove(TOKEN_FILE)


def load_apps():
    try:
        with open(APP_FILE) as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return {}


def save_apps(apps):
    with open(APP_FILE, "w") as f:
        json.dump(apps, f)


def save_state(state):
    with open(STATE_FILE, "w") as f:
        f.write(state)


def load_state():
    try:
        with open(STATE_FILE) as f:
            return f.read().strip()
    except FileNotFoundError:
        return ""


def clear_state():
    if os.path.exists(STATE_FILE):
        os.remove(STATE_FILE)


# ── SSL context ───────────────────────────────────────────────

def _ssl_ctx():
    return ssl.create_default_context()


# ── Low-level Mastodon API helpers ────────────────────────────

def _sanitise_host(host):
    """Strip scheme, credentials, and path from a host string."""
    host = host.strip()
    host = host.removeprefix("https://").removeprefix("http://")
    host = host.split("/")[0]
    host = host.split("@")[-1]
    return host


def mastodon_get(host, path, token=None, params=None):
    """GET from a Mastodon-compatible instance. Returns (http_status, body_bytes)."""
    url = f"https://{host}{path}"
    if params:
        url += "?" + urllib.parse.urlencode(params)
    headers = {
        "User-Agent": "VulaOS-Social/0.2 (+https://vulos.io)",
        "Accept": "application/json",
    }
    if token:
        headers["Authorization"] = f"Bearer {token}"
    req = urllib.request.Request(url, headers=headers)
    try:
        resp = urllib.request.urlopen(req, timeout=12, context=_ssl_ctx())
        return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read() or b"{}"
    except Exception as e:
        return 502, json.dumps({"error": str(e)}).encode()


def mastodon_post_form(host, path, data_dict, token=None):
    """POST form-encoded data to a Mastodon-compatible instance."""
    url = f"https://{host}{path}"
    body = urllib.parse.urlencode(data_dict).encode()
    headers = {
        "User-Agent": "VulaOS-Social/0.2 (+https://vulos.io)",
        "Content-Type": "application/x-www-form-urlencoded",
        "Accept": "application/json",
    }
    if token:
        headers["Authorization"] = f"Bearer {token}"
    req = urllib.request.Request(url, data=body, headers=headers, method="POST")
    try:
        resp = urllib.request.urlopen(req, timeout=12, context=_ssl_ctx())
        return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read() or b"{}"
    except Exception as e:
        return 502, json.dumps({"error": str(e)}).encode()


# ── Dynamic client registration ───────────────────────────────

def ensure_app_registered(host):
    """
    Register this client with the instance using POST /api/v1/apps if we
    don't already have credentials cached for this host.
    Returns (client_id, client_secret).
    Raises RuntimeError on failure.
    """
    apps = load_apps()
    if host in apps:
        return apps[host]["client_id"], apps[host]["client_secret"]

    redirect_uri = f"http://localhost:{PORT}/oauth/callback"
    status, body = mastodon_post_form(host, "/api/v1/apps", {
        "client_name":   "Vula OS Social",
        "redirect_uris": redirect_uri,
        "scopes":        "read write follow",
        "website":       "https://vulos.io",
    })
    if status not in (200, 201):
        raise RuntimeError(
            f"Client registration failed ({status}): {body.decode(errors='replace')}"
        )
    data = json.loads(body)
    client_id     = data.get("client_id", "")
    client_secret = data.get("client_secret", "")
    if not client_id or not client_secret:
        raise RuntimeError("Instance returned no client credentials.")

    apps[host] = {"client_id": client_id, "client_secret": client_secret}
    save_apps(apps)
    return client_id, client_secret


# ── FED-01 path allowlist (read-only public proxy) ────────────

ALLOWED_PUBLIC_PATHS = {
    "/api/v1/timelines/public",
    "/api/v1/timelines/tag/",
    "/api/v1/trends/statuses",
    "/api/v1/trends/tags",
    "/api/v1/instance",
}


def is_allowed_public_path(path):
    for allowed in ALLOWED_PUBLIC_PATHS:
        if path == allowed or path.startswith(allowed):
            return True
    return False


# ── HTTP request handler ──────────────────────────────────────

class SocialHandler(http.server.BaseHTTPRequestHandler):

    # ── routing ───────────────────────────────────────────────

    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        path   = parsed.path
        params = urllib.parse.parse_qs(parsed.query, keep_blank_values=True)

        def p(key, default=""):
            return params.get(key, [default])[0]

        if path in ("/", ""):
            self.serve_file(os.path.join(APP_DIR, "index.html"), "text/html")

        # FED-01: public timeline proxy
        elif path == "/proxy":
            self._handle_public_proxy(p("host"),
                                      p("path", "/api/v1/timelines/public"),
                                      params)

        # FED-02: OAuth2 authorization-code flow
        elif path == "/oauth/start":
            self._handle_oauth_start(p("host"))

        elif path == "/oauth/callback":
            self._handle_oauth_callback(p("code"), p("state"), p("error"))

        # FED-02: session / timeline API (called by the SPA)
        elif path == "/api/auth/status":
            self._handle_auth_status()

        elif path == "/api/auth/logout":
            self._handle_logout()

        elif path == "/api/timeline/home":
            self._handle_home_timeline(params)

        elif path == "/api/account/verify":
            self._handle_verify_credentials()

        else:
            self.send_error(404)

    def do_POST(self):
        parsed = urllib.parse.urlparse(self.path)
        if parsed.path == "/api/auth/logout":
            self._handle_logout()
        else:
            self.send_error(404)

    def do_OPTIONS(self):
        self.send_response(204)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Content-Type")
        self.end_headers()

    # ── FED-01: public proxy ──────────────────────────────────

    def _handle_public_proxy(self, host, api_path, params):
        host = _sanitise_host(host)
        if not host or "." not in host:
            self.send_json(400, {"error": "invalid host"})
            return
        if not is_allowed_public_path(api_path):
            self.send_json(403, {"error": "path not permitted"})
            return

        forward = {}
        for k in ("limit", "max_id", "since_id", "min_id", "local", "only_media"):
            if k in params:
                forward[k] = params[k][0]

        status, body = mastodon_get(host, api_path, params=forward or None)
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    # ── FED-02: OAuth start ───────────────────────────────────

    def _handle_oauth_start(self, host):
        host = _sanitise_host(host)
        if not host or "." not in host:
            self._redirect_to_ui("?oauth_error=" +
                                  urllib.parse.quote("Invalid instance name."))
            return
        try:
            client_id, _ = ensure_app_registered(host)
        except RuntimeError as e:
            self._redirect_to_ui("?oauth_error=" + urllib.parse.quote(str(e)))
            return

        # State = "<random>|<host>" so the callback can recover the host
        # without a separate server-side session store.
        rand_part = secrets.token_urlsafe(24)
        state     = f"{rand_part}|{host}"
        save_state(state)  # persist for CSRF validation

        redirect_uri = f"http://localhost:{PORT}/oauth/callback"
        auth_url = (
            f"https://{host}/oauth/authorize"
            f"?client_id={urllib.parse.quote(client_id)}"
            f"&redirect_uri={urllib.parse.quote(redirect_uri)}"
            f"&response_type=code"
            f"&scope={urllib.parse.quote('read write follow')}"
            f"&state={urllib.parse.quote(state)}"
        )
        self.send_response(302)
        self.send_header("Location", auth_url)
        self.end_headers()

    # ── FED-02: OAuth callback ────────────────────────────────

    def _handle_oauth_callback(self, code, state, error):
        if error:
            clear_state()
            self._redirect_to_ui("?oauth_error=" + urllib.parse.quote(
                f"Authorization denied: {error}"))
            return

        saved_state = load_state()
        clear_state()

        # Validate the full state string (CSRF check)
        if not saved_state or state != saved_state:
            self._redirect_to_ui("?oauth_error=" +
                                  urllib.parse.quote("State mismatch — possible CSRF."))
            return

        # Recover host from state
        parts = state.split("|", 1)
        if len(parts) != 2 or not parts[1]:
            self._redirect_to_ui("?oauth_error=" +
                                  urllib.parse.quote("Malformed state parameter."))
            return
        host = parts[1]

        if not code:
            self._redirect_to_ui("?oauth_error=" +
                                  urllib.parse.quote("No authorization code received."))
            return

        apps = load_apps()
        if host not in apps:
            self._redirect_to_ui("?oauth_error=" +
                                  urllib.parse.quote("No registered app for this instance."))
            return

        client_id     = apps[host]["client_id"]
        client_secret = apps[host]["client_secret"]
        redirect_uri  = f"http://localhost:{PORT}/oauth/callback"

        # Exchange authorization code for access token
        status, body = mastodon_post_form(host, "/oauth/token", {
            "grant_type":    "authorization_code",
            "code":          code,
            "client_id":     client_id,
            "client_secret": client_secret,
            "redirect_uri":  redirect_uri,
            "scope":         "read write follow",
        })
        if status not in (200, 201):
            self._redirect_to_ui("?oauth_error=" + urllib.parse.quote(
                f"Token exchange failed (HTTP {status})."))
            return

        try:
            token_data = json.loads(body)
        except Exception:
            self._redirect_to_ui("?oauth_error=" +
                                  urllib.parse.quote("Invalid token response from instance."))
            return

        access_token = token_data.get("access_token", "")
        if not access_token:
            self._redirect_to_ui("?oauth_error=" +
                                  urllib.parse.quote("No access token in response."))
            return

        # Fetch and cache account info
        vstatus, vbody = mastodon_get(host, "/api/v1/accounts/verify_credentials",
                                      token=access_token)
        account = {}
        if vstatus == 200:
            try:
                account = json.loads(vbody)
            except Exception:
                pass

        save_token({
            "host":         host,
            "access_token": access_token,
            "account":      account,
        })
        self._redirect_to_ui("?oauth_success=1")

    # ── FED-02: auth status ───────────────────────────────────

    def _handle_auth_status(self):
        tok = load_token()
        if tok.get("access_token"):
            self.send_json(200, {
                "authenticated": True,
                "host":          tok.get("host", ""),
                "account":       tok.get("account", {}),
            })
        else:
            self.send_json(200, {"authenticated": False})

    # ── FED-02: logout ────────────────────────────────────────

    def _handle_logout(self):
        tok = load_token()
        host = tok.get("host", "")
        if host and tok.get("access_token"):
            apps = load_apps()
            if host in apps:
                # Best-effort revocation — not all instances support it
                try:
                    mastodon_post_form(host, "/oauth/revoke", {
                        "client_id":     apps[host]["client_id"],
                        "client_secret": apps[host]["client_secret"],
                        "token":         tok["access_token"],
                    })
                except Exception:
                    pass
        clear_token()
        self.send_json(200, {"status": "logged_out"})

    # ── FED-02: home timeline (authenticated) ─────────────────

    def _handle_home_timeline(self, params):
        tok = load_token()
        if not tok.get("access_token"):
            self.send_json(401, {"error": "not authenticated"})
            return

        forward = {"limit": "20"}
        for k in ("limit", "max_id", "since_id", "min_id"):
            if k in params:
                forward[k] = params[k][0]

        status, body = mastodon_get(
            tok["host"],
            "/api/v1/timelines/home",
            token=tok["access_token"],
            params=forward,
        )
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    # ── FED-02: verify credentials ────────────────────────────

    def _handle_verify_credentials(self):
        tok = load_token()
        if not tok.get("access_token"):
            self.send_json(401, {"error": "not authenticated"})
            return
        status, body = mastodon_get(
            tok["host"],
            "/api/v1/accounts/verify_credentials",
            token=tok["access_token"],
        )
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    # ── Shared utilities ──────────────────────────────────────

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

    def send_json(self, status, data):
        body = json.dumps(data).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _redirect_to_ui(self, query=""):
        self.send_response(302)
        self.send_header("Location", "/" + query)
        self.end_headers()

    def log_message(self, format, *args):
        pass  # suppress per-request noise


print(f"[social] Fediverse on port {PORT}")
http.server.HTTPServer(("0.0.0.0", PORT), SocialHandler).serve_forever()
