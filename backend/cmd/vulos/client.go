package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// defaultBoxURL is the repo's default server bind address (PORT defaults to
// 8080 in backend/internal/config/config.go). A locally-running box is reached
// on loopback; remote boxes are configured through VULOS_BOX_URL.
const defaultBoxURL = "http://localhost:8080"

// client is a thin HTTP client bound to a single configured box. It attaches the
// caller's bearer token to every request — and, by construction, only ever
// issues requests to baseURL, so the token is never leaked to some other host.
type client struct {
	baseURL string
	token   string
	http    *http.Client
}

// newClient resolves the box URL (VULOS_BOX_URL, default http://localhost:8080)
// and the auth token (VULOS_TOKEN). The token is sent as an
// `Authorization: Bearer` header; the box's auth middleware accepts a session
// token, an admin token (vulos-admin-…), or a CP API key (vk_…) in that slot.
func newClient() (*client, error) {
	base := strings.TrimSpace(os.Getenv("VULOS_BOX_URL"))
	if base == "" {
		base = defaultBoxURL
	}
	base = strings.TrimRight(base, "/")

	token := strings.TrimSpace(os.Getenv("VULOS_TOKEN"))
	if token == "" {
		return nil, fmt.Errorf("VULOS_TOKEN is not set — export your box session token first")
	}

	return &client{
		baseURL: base,
		token:   token,
		http:    &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// newRequest builds a request against the configured box and stamps auth. The
// path must be a box-relative path beginning with "/"; callers never pass a full
// URL, which is what guarantees credentials only ever reach baseURL.
func (c *client) newRequest(method, path string, body io.Reader) (*http.Request, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	return req, nil
}

// doJSON issues a request and decodes a JSON response into out (which may be
// nil). A non-2xx status is turned into an error carrying the server's message.
func (c *client) doJSON(method, path string, body io.Reader, contentType string, out any) error {
	req, err := c.newRequest(method, path, body)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("contacting box %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpError(resp.StatusCode, data)
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decoding box response: %w", err)
	}
	return nil
}

// getJSON / deleteJSON / postJSON are convenience wrappers over doJSON.
func (c *client) getJSON(path string, out any) error {
	return c.doJSON(http.MethodGet, path, nil, "", out)
}

func (c *client) deleteJSON(path string, out any) error {
	return c.doJSON(http.MethodDelete, path, nil, "", out)
}

func (c *client) postJSON(path string, in, out any) error {
	buf, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.doJSON(http.MethodPost, path, bytes.NewReader(buf), "application/json", out)
}

// httpError renders a box error response, preferring a JSON {"error":"…"} body.
func httpError(status int, body []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return fmt.Errorf("box returned %d: %s", status, e.Error)
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Errorf("box returned %d: %s", status, msg)
}
