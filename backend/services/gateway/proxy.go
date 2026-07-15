package gateway

// proxy.go — hot-path reverse proxy for authenticated app traffic.
//
// The :8080 gateway fronts EVERY in-shell app request, so the proxy itself is on
// the hot path. This replaces the previous hand-rolled "copy headers → client.Do
// → io.Copy / ReadAll" block with a single shared *httputil.ReverseProxy that:
//
//   - reuses ONE pooled *http.Transport (keep-alive, bounded idle pool) instead
//     of dialing a fresh connection per request;
//   - STREAMS request and response bodies (FlushInterval flushes writes as they
//     arrive, so SSE / chunked responses are not stalled) using a sync.Pool of
//     copy buffers — no full-body buffering except where the ORIGIN-01 HTML
//     bridge injection inherently requires the whole document;
//   - applies EVERY security transform unchanged, in Director (request:
//     X-Vulos-* strip + trusted identity/integration/storage inject, Host
//     rewrite, Cookie strip) and ModifyResponse (response: X-Vulos-* strip,
//     Set-Cookie / X-Powered-By / X-Frame-Options drop, security headers,
//     per-app frame-ancestors CSP, HTML bridge injection).
//
// The per-request data the transforms need (session, appID, resolved namespace,
// origin context) travels in the request context via proxyCtx — set by Handler
// right before it delegates here — so nothing is re-parsed from the request.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"time"

	"vulos/backend/services/appnet"
	"vulos/backend/services/auth"
)

// proxyCtxKey keys the per-request proxy context in the request context.
type proxyCtxKey struct{}

// proxyCtx carries everything Director/ModifyResponse need for one request, so
// neither has to re-derive it from the raw request.
type proxyCtx struct {
	session      *auth.Session
	appID        string
	ns           *appnet.Namespace
	appPath      string
	onAppOrigin  bool
	isPathPrefix bool
	scheme       string
	reqHost      string
	baseDomain   string
	reqPort      string
}

func proxyCtxFrom(ctx context.Context) *proxyCtx {
	pc, _ := ctx.Value(proxyCtxKey{}).(*proxyCtx)
	return pc
}

// bufferPool is an httputil.BufferPool backed by a sync.Pool so the reverse
// proxy's streaming copy reuses 32 KiB buffers instead of allocating per call.
type bufferPool struct{ pool sync.Pool }

func newBufferPool() *bufferPool {
	return &bufferPool{pool: sync.Pool{New: func() any { b := make([]byte, 32*1024); return &b }}}
}

func (p *bufferPool) Get() []byte  { return *(p.pool.Get().(*[]byte)) }
func (p *bufferPool) Put(b []byte) { p.pool.Put(&b) }

// newSharedTransport builds the single pooled transport reused by the reverse
// proxy AND the gateway's plain client (health checks, public path). Keep-alive
// + a generous idle pool keeps upstream connections warm across requests.
func newSharedTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil, // never route app traffic through an external proxy
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     false, // app upstreams are plain HTTP/1.1 on loopback
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}
}

// retryRoundTripper retries a request a few times on connection-level errors so a
// just-launched app (whose listener isn't up for the first moment) still serves
// the initial page load — the "app may still be starting" behaviour the old
// hand-rolled loop provided. It only retries when the body can be safely re-sent
// (no body, or a rewindable GetBody), so a streamed request body is never
// double-consumed.
type retryRoundTripper struct {
	base     http.RoundTripper
	attempts int
	delay    time.Duration
}

func (rt *retryRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error
	for attempt := 0; attempt < rt.attempts; attempt++ {
		if attempt > 0 {
			// Only retry when we can reproduce the body.
			if req.Body != nil && req.Body != http.NoBody {
				if req.GetBody == nil {
					break
				}
				b, gbErr := req.GetBody()
				if gbErr != nil {
					break
				}
				req.Body = b
			}
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(rt.delay):
			}
		}
		resp, err = rt.base.RoundTrip(req)
		if err == nil {
			return resp, nil
		}
	}
	return resp, err
}

// newReverseProxy constructs the shared reverse proxy. All per-request state is
// read from proxyCtx in the request context.
func (g *Gateway) newReverseProxy(transport http.RoundTripper) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Transport:  transport,
		BufferPool: newBufferPool(),
		// Flush writes as they arrive so SSE / long-poll / chunked upstreams stream
		// to the browser instead of being buffered until the handler returns.
		FlushInterval:  -1,
		Director:       g.proxyDirector,
		ModifyResponse: g.proxyModifyResponse,
		ErrorHandler:   g.proxyErrorHandler,
	}
}

// proxyDirector rewrites the outbound request to the resolved namespace and
// applies the request-side security transforms (identical to the old
// applyVulosHeaders): strip ALL inbound X-Vulos-*, inject trusted identity /
// integration / storage headers, drop the Cookie, and rewrite Host.
func (g *Gateway) proxyDirector(req *http.Request) {
	pc := proxyCtxFrom(req.Context())
	if pc == nil {
		return
	}
	req.URL.Scheme = "http"
	req.URL.Host = fmt.Sprintf("%s:%d", pc.ns.NSIP, pc.ns.AppPort)
	req.URL.Path = pc.appPath
	// RawQuery is preserved from the inbound clone.

	g.applyTrustedHeaders(req.Context(), req, pc.session, pc.appID)
	req.Header.Del("Host")
	req.Host = fmt.Sprintf("localhost:%d", pc.ns.AppPort)
}

// proxyModifyResponse applies the response-side security transforms (identical
// to the old inline block): strip ALL X-Vulos-* from the app's response, drop
// Set-Cookie / X-Powered-By / X-Frame-Options, set the hardening headers and the
// per-app frame-ancestors CSP, and inject the ORIGIN-01 bridge (and the
// path-prefix <base>) into HTML responses.
func (g *Gateway) proxyModifyResponse(resp *http.Response) error {
	pc := proxyCtxFrom(resp.Request.Context())
	if pc == nil {
		return nil
	}

	// M2: strip ALL X-Vulos-* from the app's response so an app can never leak or
	// forge seam headers back to the browser.
	stripInboundVulosHeaders(resp.Header)
	resp.Header.Del("Set-Cookie")
	resp.Header.Del("X-Powered-By")
	resp.Header.Del("X-Frame-Options")
	resp.Header.Set("X-Vulos-App", pc.appID)
	resp.Header.Set("X-Content-Type-Options", "nosniff")
	resp.Header.Set("Referrer-Policy", "no-referrer")
	resp.Header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), clipboard-read=(), clipboard-write=()")

	// ORIGIN-01 (part 2): pin who may frame this app.
	shellOrigin := pc.scheme + "://" + pc.reqHost
	if pc.onAppOrigin {
		if so, ok := appnet.ShellOrigin(pc.scheme, pc.baseDomain, pc.reqPort); ok {
			shellOrigin = so
			resp.Header.Set("Content-Security-Policy", "frame-ancestors "+so)
		} else {
			shellOrigin = ""
			resp.Header.Set("Content-Security-Policy", "frame-ancestors 'none'")
		}
	} else {
		resp.Header.Set("Content-Security-Policy", "frame-ancestors 'self'")
	}

	// HTML responses get the bridge client (+ path-prefix <base>) injected. This
	// inherently needs the whole document, so it is the ONE place we buffer — and
	// only for HTML that is not content-encoded (injecting into a compressed body
	// would corrupt it; such responses are streamed unmodified).
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/html") && resp.Header.Get("Content-Encoding") == "" {
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		var head string
		if pc.isPathPrefix && !pc.onAppOrigin {
			head = fmt.Sprintf(`<base href="/app/%s/">`, pc.appID)
		}
		head += bridgeScript(shellOrigin)
		html := []byte(injectIntoHead(string(body), head))
		resp.Body = io.NopCloser(bytes.NewReader(html))
		resp.ContentLength = int64(len(html))
		resp.Header.Set("Content-Length", strconv.Itoa(len(html)))
	}
	return nil
}

// proxyErrorHandler maps an upstream failure to a 502, preserving the old log
// line and JSON body.
func (g *Gateway) proxyErrorHandler(w http.ResponseWriter, r *http.Request, err error) {
	appID := ""
	if pc := proxyCtxFrom(r.Context()); pc != nil {
		appID = pc.appID
	}
	log.Printf("[gateway] proxy to %s failed: %v", appID, err)
	http.Error(w, `{"error":"app unreachable"}`, http.StatusBadGateway)
}
