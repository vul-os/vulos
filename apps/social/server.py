"""Vula OS — Fediverse Social Client
ActivityPub / Mastodon-compatible client with OAuth2 login.

FED-01: read-only public timeline proxy (CORS bypass).
FED-02: dynamic client registration, OAuth2 authorization-code flow,
        token storage in ~/.vulos/data/social/, authenticated home
        timeline, verify-credentials, logout.
FED-03: feed interactions — compose (POST /api/v1/statuses), boost,
        favourite, reply (in-reply-to), bookmark, thread/context view.
FED-04: Photos tab — paginated media-only timeline proxy (image/gifv
        attachments); Video tab — paginated video-attachment timeline
        proxy + thread/context reuse for comments; hls.js vendored
        under apps/social/hls.js for HLS playback with native fallback.
FED-05: Forums tab — Lemmy communities browser.  Separate Lemmy JWT
        login (username+password POST to /api/v3/user/login), stored in
        ~/.vulos/data/social/lemmy_token.json.  Proxy endpoints:
          GET  /api/lemmy/communities   — list communities (sort, page)
          GET  /api/lemmy/posts         — posts for a community (sort, page)
          GET  /api/lemmy/comments      — comment tree for a post
          POST /api/lemmy/login         — Lemmy JWT login
          POST /api/lemmy/logout        — clear stored JWT
          POST /api/lemmy/vote          — upvote/downvote post or comment
          POST /api/lemmy/subscribe     — subscribe/unsubscribe community
          POST /api/lemmy/comment       — create a comment
        All Lemmy calls are proxied through the local server to bypass CORS.
        Read-only mode (no JWT) is fully supported for browsing.
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

TOKEN_FILE   = os.path.join(DATA_DIR, "token.json")        # { host, access_token, account }
APP_FILE     = os.path.join(DATA_DIR, "apps.json")         # { host: { client_id, client_secret } }
STATE_FILE   = os.path.join(DATA_DIR, "oauth_state")       # ephemeral CSRF state ("rand|host")
LEMMY_FILE   = os.path.join(DATA_DIR, "lemmy_token.json")  # { host, jwt, username }


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


def mastodon_post_empty(host, path, token):
    """POST with no body (used for favourite/boost/bookmark toggles)."""
    url = f"https://{host}{path}"
    headers = {
        "User-Agent": "VulaOS-Social/0.2 (+https://vulos.io)",
        "Accept": "application/json",
        "Content-Length": "0",
        "Authorization": f"Bearer {token}",
    }
    req = urllib.request.Request(url, data=b"", headers=headers, method="POST")
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

# ── FED-04: media-type filters ────────────────────────────────

MEDIA_IMAGE_TYPES = {"image", "gifv"}
MEDIA_VIDEO_TYPES = {"video", "audio"}  # audio omitted in UI but keep broad


def is_allowed_public_path(path):
    for allowed in ALLOWED_PUBLIC_PATHS:
        if path == allowed or path.startswith(allowed):
            return True
    return False


# ── FED-05: Lemmy token helpers ───────────────────────────────

def load_lemmy_token():
    """Return stored Lemmy JWT dict or {}."""
    try:
        with open(LEMMY_FILE) as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return {}


def save_lemmy_token(data):
    with open(LEMMY_FILE, "w") as f:
        json.dump(data, f)


def clear_lemmy_token():
    if os.path.exists(LEMMY_FILE):
        os.remove(LEMMY_FILE)


# ── FED-05: Low-level Lemmy API helpers ───────────────────────

def lemmy_get(host, path, params=None, jwt=None):
    """GET from a Lemmy instance. Returns (http_status, body_bytes)."""
    url = f"https://{host}{path}"
    if params:
        url += "?" + urllib.parse.urlencode(params)
    headers = {
        "User-Agent": "VulaOS-Social/0.3 (+https://vulos.io)",
        "Accept": "application/json",
    }
    if jwt:
        headers["Authorization"] = f"Bearer {jwt}"
    req = urllib.request.Request(url, headers=headers)
    try:
        resp = urllib.request.urlopen(req, timeout=15, context=_ssl_ctx())
        return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read() or b"{}"
    except Exception as e:
        return 502, json.dumps({"error": str(e)}).encode()


def lemmy_post_json(host, path, payload, jwt=None):
    """POST JSON to a Lemmy instance. Returns (http_status, body_bytes)."""
    url = f"https://{host}{path}"
    body = json.dumps(payload).encode()
    headers = {
        "User-Agent": "VulaOS-Social/0.3 (+https://vulos.io)",
        "Content-Type": "application/json",
        "Accept": "application/json",
    }
    if jwt:
        headers["Authorization"] = f"Bearer {jwt}"
    req = urllib.request.Request(url, data=body, headers=headers, method="POST")
    try:
        resp = urllib.request.urlopen(req, timeout=15, context=_ssl_ctx())
        return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read() or b"{}"
    except Exception as e:
        return 502, json.dumps({"error": str(e)}).encode()


def lemmy_put_json(host, path, payload, jwt=None):
    """PUT JSON to a Lemmy instance. Returns (http_status, body_bytes)."""
    url = f"https://{host}{path}"
    body = json.dumps(payload).encode()
    headers = {
        "User-Agent": "VulaOS-Social/0.3 (+https://vulos.io)",
        "Content-Type": "application/json",
        "Accept": "application/json",
    }
    if jwt:
        headers["Authorization"] = f"Bearer {jwt}"
    req = urllib.request.Request(url, data=body, headers=headers, method="PUT")
    try:
        resp = urllib.request.urlopen(req, timeout=15, context=_ssl_ctx())
        return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read() or b"{}"
    except Exception as e:
        return 502, json.dumps({"error": str(e)}).encode()


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

        # FED-03: thread/conversation view
        elif path.startswith("/api/v1/statuses/") and path.endswith("/context"):
            self._handle_thread_context(path)

        # FED-04: Photos — media-only (images) paginated timeline
        elif path == "/api/media/photos":
            self._handle_photos_timeline(params)

        # FED-04: Video — video-only paginated timeline
        elif path == "/api/media/videos":
            self._handle_videos_timeline(params)

        # FED-04: vendor hls.js (locally cached copy)
        elif path == "/hls.js":
            self.serve_file(os.path.join(APP_DIR, "hls.js"), "application/javascript")

        # FED-05: Lemmy — list communities
        elif path == "/api/lemmy/communities":
            self._handle_lemmy_communities(params)

        # FED-05: Lemmy — posts for a community (or front page)
        elif path == "/api/lemmy/posts":
            self._handle_lemmy_posts(params)

        # FED-05: Lemmy — comment tree for a post
        elif path == "/api/lemmy/comments":
            self._handle_lemmy_comments(params)

        # FED-05: Lemmy — auth status
        elif path == "/api/lemmy/auth/status":
            self._handle_lemmy_auth_status()

        else:
            self.send_error(404)

    def do_POST(self):
        parsed = urllib.parse.urlparse(self.path)
        path   = parsed.path

        if path == "/api/auth/logout":
            self._handle_logout()

        # FED-03: compose a new post
        elif path == "/api/v1/statuses":
            self._handle_post_status()

        # FED-03: favourite / unfavourite / boost / unboost / bookmark / unbookmark
        elif path.startswith("/api/v1/statuses/") and any(
            path.endswith(s) for s in (
                "/favourite", "/unfavourite",
                "/reblog",    "/unreblog",
                "/bookmark",  "/unbookmark",
            )
        ):
            self._handle_status_action(path)

        # FED-05: Lemmy — login (username + password → JWT)
        elif path == "/api/lemmy/login":
            self._handle_lemmy_login()

        # FED-05: Lemmy — logout (clear stored JWT)
        elif path == "/api/lemmy/logout":
            self._handle_lemmy_logout()

        # FED-05: Lemmy — upvote/downvote a post or comment
        elif path == "/api/lemmy/vote":
            self._handle_lemmy_vote()

        # FED-05: Lemmy — subscribe / unsubscribe to a community
        elif path == "/api/lemmy/subscribe":
            self._handle_lemmy_subscribe()

        # FED-05: Lemmy — create a comment on a post
        elif path == "/api/lemmy/comment":
            self._handle_lemmy_comment()

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

    # ── FED-03: compose a new status ─────────────────────────

    def _handle_post_status(self):
        tok = load_token()
        if not tok.get("access_token"):
            self.send_json(401, {"error": "not authenticated"})
            return

        length = int(self.headers.get("Content-Length", 0))
        raw = self.rfile.read(length) if length else b""

        content_type = self.headers.get("Content-Type", "")
        if "application/json" in content_type:
            try:
                payload = json.loads(raw)
            except Exception:
                self.send_json(400, {"error": "invalid JSON body"})
                return
        else:
            # fall back to form-encoded
            payload = dict(urllib.parse.parse_qsl(raw.decode(errors="replace")))

        status_text = (payload.get("status") or "").strip()
        if not status_text:
            self.send_json(400, {"error": "status text is required"})
            return
        if len(status_text) > 500:
            self.send_json(400, {"error": "status exceeds 500 characters"})
            return

        post_data = {"status": status_text}
        if payload.get("spoiler_text"):
            post_data["spoiler_text"] = payload["spoiler_text"]
        if payload.get("in_reply_to_id"):
            post_data["in_reply_to_id"] = payload["in_reply_to_id"]
        if payload.get("visibility"):
            post_data["visibility"] = payload["visibility"]

        status, body = mastodon_post_form(
            tok["host"],
            "/api/v1/statuses",
            post_data,
            token=tok["access_token"],
        )
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    # ── FED-03: single-status action (fav/boost/bookmark) ────

    def _handle_status_action(self, path):
        """Proxy a POST to /api/v1/statuses/:id/<action> authenticated."""
        tok = load_token()
        if not tok.get("access_token"):
            self.send_json(401, {"error": "not authenticated"})
            return

        # Consume any body sent by the client (not forwarded)
        length = int(self.headers.get("Content-Length", 0))
        if length:
            self.rfile.read(length)

        status, body = mastodon_post_empty(
            tok["host"], path, tok["access_token"]
        )
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    # ── FED-03: thread/context view ───────────────────────────

    def _handle_thread_context(self, path):
        """
        Proxy GET /api/v1/statuses/:id/context — works for both
        authenticated (home) and unauthenticated (public) requests.
        path format: /api/v1/statuses/<id>/context
        """
        # Extract the status id to also fetch the root status itself
        parts = path.split("/")
        # path = ['', 'api', 'v1', 'statuses', '<id>', 'context']
        status_id = parts[4] if len(parts) >= 6 else ""

        tok = load_token()
        token = tok.get("access_token") if tok else None
        host  = tok.get("host") if tok else None

        # For unauthenticated thread view we need a host from the query string
        parsed = urllib.parse.urlparse(self.path)
        qs     = urllib.parse.parse_qs(parsed.query)
        if not host:
            host = _sanitise_host(qs.get("host", [""])[0])
        if not host or "." not in host:
            self.send_json(400, {"error": "host required for unauthenticated thread view"})
            return

        # Fetch context (ancestors + descendants)
        ctx_status, ctx_body = mastodon_get(host, path, token=token)
        # Fetch the root status itself so the UI can show it at the top
        root_status_code, root_body = mastodon_get(
            host, f"/api/v1/statuses/{status_id}", token=token
        )

        try:
            ctx  = json.loads(ctx_body)
        except Exception:
            ctx  = {"ancestors": [], "descendants": []}
        try:
            root = json.loads(root_body) if root_status_code == 200 else {}
        except Exception:
            root = {}

        result = {
            "root":        root,
            "ancestors":   ctx.get("ancestors", []),
            "descendants": ctx.get("descendants", []),
        }
        body = json.dumps(result).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    # ── FED-04: Photos timeline ───────────────────────────────

    def _handle_photos_timeline(self, params):
        """
        Return a list of statuses that contain at least one image/gifv
        attachment.  Works authenticated (home timeline) or unauthenticated
        (public timeline for a given host).

        Query params accepted:
          host      — required when not authenticated
          max_id    — pagination cursor (passed through to upstream)
          limit     — how many results to return (default 40, max 80)
          local     — 'true' for local-only on public timeline
        """
        def p(key, default=""):
            return params.get(key, [default])[0]

        tok   = load_token()
        token = tok.get("access_token") if tok else None
        host  = tok.get("host") if token else _sanitise_host(p("host"))

        if not host or "." not in host:
            self.send_json(400, {"error": "host required"})
            return

        want = min(int(p("limit", "40")), 80)
        results = []
        max_id  = p("max_id") or None
        fetched = 0

        # We over-fetch because not every status has image attachments.
        # Stop after 5 pages or when we have enough.
        for _ in range(5):
            fwd = {"limit": "40"}
            if max_id:
                fwd["max_id"] = max_id
            if p("local") == "true":
                fwd["local"] = "true"
            if token:
                fwd["only_media"] = "true"

            api_path = "/api/v1/timelines/home" if token else "/api/v1/timelines/public"
            if not token:
                fwd["only_media"] = "true"

            status, body = mastodon_get(host, api_path, token=token, params=fwd)
            if status != 200:
                break
            try:
                page = json.loads(body)
            except Exception:
                break
            if not page:
                break

            for s in page:
                actual = s.get("reblog") or s
                imgs = [m for m in (actual.get("media_attachments") or [])
                        if m.get("type") in MEDIA_IMAGE_TYPES]
                if imgs:
                    results.append(s)
                    if len(results) >= want:
                        break

            fetched += len(page)
            max_id = page[-1]["id"] if page else None

            if len(results) >= want or not max_id:
                break

        out = json.dumps({"statuses": results[:want], "next_max_id": max_id}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    # ── FED-04: Videos timeline ───────────────────────────────

    def _handle_videos_timeline(self, params):
        """
        Return statuses with at least one video attachment.
        Same host/auth/pagination logic as _handle_photos_timeline.
        """
        def p(key, default=""):
            return params.get(key, [default])[0]

        tok   = load_token()
        token = tok.get("access_token") if tok else None
        host  = tok.get("host") if token else _sanitise_host(p("host"))

        if not host or "." not in host:
            self.send_json(400, {"error": "host required"})
            return

        want   = min(int(p("limit", "20")), 60)
        results = []
        max_id  = p("max_id") or None

        for _ in range(8):
            fwd = {"limit": "40"}
            if max_id:
                fwd["max_id"] = max_id
            if p("local") == "true":
                fwd["local"] = "true"
            fwd["only_media"] = "true"

            api_path = "/api/v1/timelines/home" if token else "/api/v1/timelines/public"
            status, body = mastodon_get(host, api_path, token=token, params=fwd)
            if status != 200:
                break
            try:
                page = json.loads(body)
            except Exception:
                break
            if not page:
                break

            for s in page:
                actual = s.get("reblog") or s
                vids = [m for m in (actual.get("media_attachments") or [])
                        if m.get("type") in MEDIA_VIDEO_TYPES]
                if vids:
                    results.append(s)
                    if len(results) >= want:
                        break

            max_id = page[-1]["id"] if page else None
            if len(results) >= want or not max_id:
                break

        out = json.dumps({"statuses": results[:want], "next_max_id": max_id}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    # ══════════════════════════════════════════════════════════
    # FED-05: Lemmy forum proxy handlers
    # ══════════════════════════════════════════════════════════

    def _read_json_body(self):
        """Read and parse JSON request body. Returns (payload_dict, error_str)."""
        length = int(self.headers.get("Content-Length", 0))
        raw = self.rfile.read(length) if length else b""
        content_type = self.headers.get("Content-Type", "")
        if "application/json" in content_type:
            try:
                return json.loads(raw), None
            except Exception:
                return None, "invalid JSON body"
        else:
            return dict(urllib.parse.parse_qsl(raw.decode(errors="replace"))), None

    def _handle_lemmy_auth_status(self):
        """Return current Lemmy login status."""
        tok = load_lemmy_token()
        if tok.get("jwt"):
            self.send_json(200, {
                "authenticated": True,
                "host":     tok.get("host", ""),
                "username": tok.get("username", ""),
            })
        else:
            self.send_json(200, {"authenticated": False})

    def _handle_lemmy_login(self):
        """
        POST /api/lemmy/login
        Body JSON: { host, username, password }
        Calls POST https://<host>/api/v3/user/login and stores the JWT.
        """
        payload, err = self._read_json_body()
        if err:
            self.send_json(400, {"error": err})
            return

        host     = _sanitise_host(payload.get("host", ""))
        username = (payload.get("username") or "").strip()
        password = (payload.get("password") or "")

        if not host or "." not in host:
            self.send_json(400, {"error": "invalid Lemmy instance"})
            return
        if not username or not password:
            self.send_json(400, {"error": "username and password required"})
            return

        status, body = lemmy_post_json(host, "/api/v3/user/login", {
            "username_or_email": username,
            "password": password,
        })
        try:
            data = json.loads(body)
        except Exception:
            self.send_json(502, {"error": "invalid response from instance"})
            return

        if status not in (200, 201):
            self.send_json(status, {"error": data.get("error", f"HTTP {status}")})
            return

        jwt = data.get("jwt", "")
        if not jwt:
            self.send_json(502, {"error": "No JWT in login response"})
            return

        save_lemmy_token({"host": host, "jwt": jwt, "username": username})
        self.send_json(200, {"ok": True, "username": username, "host": host})

    def _handle_lemmy_logout(self):
        """POST /api/lemmy/logout — clear stored Lemmy JWT."""
        # Consume body
        length = int(self.headers.get("Content-Length", 0))
        if length:
            self.rfile.read(length)
        clear_lemmy_token()
        self.send_json(200, {"status": "logged_out"})

    def _handle_lemmy_communities(self, params):
        """
        GET /api/lemmy/communities
        Query params: host, sort (Active/Hot/New/TopAll/…), page, limit
        Proxies GET https://<host>/api/v3/community/list
        Works read-only (no JWT required).
        """
        def p(key, default=""):
            return params.get(key, [default])[0]

        tok  = load_lemmy_token()
        jwt  = tok.get("jwt") if tok else None
        host = tok.get("host") if jwt else _sanitise_host(p("host"))

        if not host or "." not in host:
            self.send_json(400, {"error": "host required"})
            return

        fwd = {
            "type_":  "All",
            "sort":   p("sort", "Active"),
            "page":   p("page", "1"),
            "limit":  p("limit", "40"),
        }

        status, body = lemmy_get(host, "/api/v3/community/list", params=fwd, jwt=jwt)
        try:
            data = json.loads(body)
        except Exception:
            data = {}

        # Wrap with auth context
        out = json.dumps({
            "communities": data.get("communities", []),
            "lemmy_host":  host,
            "authenticated": bool(jwt),
        }).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def _handle_lemmy_posts(self, params):
        """
        GET /api/lemmy/posts
        Query params: host, community_id, community_name, sort, page, limit
        Proxies GET https://<host>/api/v3/post/list
        Works read-only.
        """
        def p(key, default=""):
            return params.get(key, [default])[0]

        tok  = load_lemmy_token()
        jwt  = tok.get("jwt") if tok else None
        host = tok.get("host") if jwt else _sanitise_host(p("host"))

        if not host or "." not in host:
            self.send_json(400, {"error": "host required"})
            return

        fwd = {
            "type_": "All",
            "sort":  p("sort", "Active"),
            "page":  p("page", "1"),
            "limit": p("limit", "20"),
        }
        if p("community_id"):
            fwd["community_id"] = p("community_id")
        if p("community_name"):
            fwd["community_name"] = p("community_name")

        status, body = lemmy_get(host, "/api/v3/post/list", params=fwd, jwt=jwt)
        try:
            data = json.loads(body)
        except Exception:
            data = {}

        out = json.dumps({
            "posts":         data.get("posts", []),
            "lemmy_host":    host,
            "authenticated": bool(jwt),
        }).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def _handle_lemmy_comments(self, params):
        """
        GET /api/lemmy/comments
        Query params: host, post_id, max_depth, page, limit, sort
        Proxies GET https://<host>/api/v3/comment/list
        Works read-only.
        """
        def p(key, default=""):
            return params.get(key, [default])[0]

        tok  = load_lemmy_token()
        jwt  = tok.get("jwt") if tok else None
        host = tok.get("host") if jwt else _sanitise_host(p("host"))

        if not host or "." not in host:
            self.send_json(400, {"error": "host required"})
            return

        post_id = p("post_id")
        if not post_id:
            self.send_json(400, {"error": "post_id required"})
            return

        fwd = {
            "post_id":   post_id,
            "sort":      p("sort", "Hot"),
            "max_depth": p("max_depth", "6"),
            "page":      p("page", "1"),
            "limit":     p("limit", "50"),
            "type_":     "All",
        }

        status, body = lemmy_get(host, "/api/v3/comment/list", params=fwd, jwt=jwt)
        try:
            data = json.loads(body)
        except Exception:
            data = {}

        out = json.dumps({
            "comments":      data.get("comments", []),
            "lemmy_host":    host,
            "authenticated": bool(jwt),
        }).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def _handle_lemmy_vote(self):
        """
        POST /api/lemmy/vote
        Body JSON: { type: "post"|"comment", id: <int>, score: 1|0|-1 }
        Requires Lemmy login.
        Calls POST https://<host>/api/v3/post/like  or  /api/v3/comment/like
        """
        payload, err = self._read_json_body()
        if err:
            self.send_json(400, {"error": err})
            return

        tok = load_lemmy_token()
        jwt = tok.get("jwt")
        if not jwt:
            self.send_json(401, {"error": "Lemmy login required to vote"})
            return

        host  = tok["host"]
        kind  = payload.get("type", "post")
        item_id = payload.get("id")
        score   = payload.get("score", 1)

        if item_id is None:
            self.send_json(400, {"error": "id required"})
            return
        if score not in (1, 0, -1):
            self.send_json(400, {"error": "score must be 1, 0, or -1"})
            return

        if kind == "comment":
            api_path = "/api/v3/comment/like"
            body_key = "comment_id"
        else:
            api_path = "/api/v3/post/like"
            body_key = "post_id"

        status, body = lemmy_post_json(
            host, api_path,
            {body_key: int(item_id), "score": int(score)},
            jwt=jwt,
        )
        try:
            data = json.loads(body)
        except Exception:
            data = {}

        self.send_response(status if status in (200, 201) else 200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        out = json.dumps({"ok": status in (200, 201), "data": data}).encode()
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def _handle_lemmy_subscribe(self):
        """
        POST /api/lemmy/subscribe
        Body JSON: { community_id: <int>, subscribe: true|false }
        Calls POST https://<host>/api/v3/community/follow
        Requires Lemmy login.
        """
        payload, err = self._read_json_body()
        if err:
            self.send_json(400, {"error": err})
            return

        tok = load_lemmy_token()
        jwt = tok.get("jwt")
        if not jwt:
            self.send_json(401, {"error": "Lemmy login required to subscribe"})
            return

        host         = tok["host"]
        community_id = payload.get("community_id")
        subscribe    = bool(payload.get("subscribe", True))

        if community_id is None:
            self.send_json(400, {"error": "community_id required"})
            return

        status, body = lemmy_post_json(
            host, "/api/v3/community/follow",
            {"community_id": int(community_id), "follow": subscribe},
            jwt=jwt,
        )
        try:
            data = json.loads(body)
        except Exception:
            data = {}

        out = json.dumps({"ok": status in (200, 201), "data": data}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def _handle_lemmy_comment(self):
        """
        POST /api/lemmy/comment
        Body JSON: { post_id: <int>, content: <str>, parent_id: <int|null> }
        Calls POST https://<host>/api/v3/comment
        Requires Lemmy login.
        """
        payload, err = self._read_json_body()
        if err:
            self.send_json(400, {"error": err})
            return

        tok = load_lemmy_token()
        jwt = tok.get("jwt")
        if not jwt:
            self.send_json(401, {"error": "Lemmy login required to comment"})
            return

        host    = tok["host"]
        post_id = payload.get("post_id")
        content = (payload.get("content") or "").strip()

        if not post_id:
            self.send_json(400, {"error": "post_id required"})
            return
        if not content:
            self.send_json(400, {"error": "content required"})
            return
        if len(content) > 10000:
            self.send_json(400, {"error": "comment too long (max 10 000 chars)"})
            return

        body_data = {"post_id": int(post_id), "content": content}
        if payload.get("parent_id"):
            body_data["parent_id"] = int(payload["parent_id"])

        status, body = lemmy_post_json(host, "/api/v3/comment", body_data, jwt=jwt)
        try:
            data = json.loads(body)
        except Exception:
            data = {}

        if status not in (200, 201):
            self.send_json(status, {"error": data.get("error", f"HTTP {status}")})
            return

        out = json.dumps({"ok": True, "comment_view": data.get("comment_view", {})}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

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
