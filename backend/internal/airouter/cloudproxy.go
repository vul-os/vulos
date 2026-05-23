package airouter

// cloudproxy.go — AIROT-07: Cloud AI proxy transport (OS-side client).
//
// Implements the `cloud` mode transport used by Router.routeCloud and
// Embedder.callEmbedCloud.  Responsibilities:
//
//  1. Device-cert acquisition / refresh via CertProvider.
//  2. POST to VULOS_AI_PROXY_URL with Authorization: VulosDevice <cert>.
//  3. Exponential back-off retry on HTTP 429 / 503 (up to maxRetries).
//  4. Best-effort usage reporting after a successful response (fire-and-forget).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// CertProvider supplies the OS device certificate used to authenticate with
// the Vulos cloud AI gateway.  Implementations may refresh the cert (e.g. from
// disk, OS keychain, or a cloud-login session) when the current cert is expired
// or missing.
type CertProvider interface {
	// DeviceCert returns the current PEM-encoded device certificate.
	// If the cert needs to be refreshed the implementation should do so
	// transparently and return the fresh cert.  Returns an error if no valid
	// cert is available.
	DeviceCert(ctx context.Context) ([]byte, error)
}

// staticCertProvider is a trivial CertProvider backed by a static byte slice.
// Used when a caller sets Router.DeviceCert directly (backwards-compatible
// path).
type staticCertProvider struct {
	cert []byte
}

func (s *staticCertProvider) DeviceCert(_ context.Context) ([]byte, error) {
	if len(s.cert) == 0 {
		return nil, fmt.Errorf("airouter: no device cert configured")
	}
	return s.cert, nil
}

// UsageReporter receives usage metadata after each successful cloud proxy
// response.  Implementations may POST usage to the cloud control plane or
// write it to a local SQLite usage log.  Errors are logged but never returned
// to the caller.
type UsageReporter interface {
	ReportUsage(ctx context.Context, u UsageRecord)
}

// UsageRecord carries the metadata emitted after each proxied request.
type UsageRecord struct {
	// Endpoint is "chat" or "embed".
	Endpoint string
	// Model is the model slug that was requested.
	Model string
	// RequestedAt is when the request was initiated.
	RequestedAt time.Time
	// DurationMs is the round-trip time in milliseconds (excluding streaming time).
	DurationMs int64
	// StatusCode is the HTTP status returned by the cloud proxy.
	StatusCode int
}

// noopUsageReporter discards all usage records.  Used when no reporter is wired.
type noopUsageReporter struct{}

func (noopUsageReporter) ReportUsage(_ context.Context, _ UsageRecord) {}

// cloudProxyConfig holds tunable parameters for the CloudProxy.
type cloudProxyConfig struct {
	// MaxRetries is the maximum number of retry attempts on 429/503.
	MaxRetries int
	// BaseDelay is the initial back-off delay.
	BaseDelay time.Duration
	// MaxDelay caps the exponential back-off.
	MaxDelay time.Duration
}

var defaultCloudProxyConfig = cloudProxyConfig{
	MaxRetries: 4,
	BaseDelay:  250 * time.Millisecond,
	MaxDelay:   16 * time.Second,
}

// CloudProxy is the low-level HTTP transport for cloud-mode requests.
// It is created once per Router and reused across requests.
type CloudProxy struct {
	httpClient *http.Client
	certProv   CertProvider
	reporter   UsageReporter
	cfg        cloudProxyConfig
}

// newCloudProxy creates a CloudProxy.  If reporter is nil a no-op reporter
// is used.
func newCloudProxy(client *http.Client, certProv CertProvider, reporter UsageReporter) *CloudProxy {
	if reporter == nil {
		reporter = noopUsageReporter{}
	}
	return &CloudProxy{
		httpClient: client,
		certProv:   certProv,
		reporter:   reporter,
		cfg:        defaultCloudProxyConfig,
	}
}

// ProxyChat posts a chat request to the cloud proxy and returns the raw SSE
// stream.  It retries on 429/503 with exponential back-off.
func (p *CloudProxy) ProxyChat(ctx context.Context, req ChatRequest) (io.ReadCloser, error) {
	data, _ := json.Marshal(req)
	return p.doWithRetry(ctx, cloudProxyURL(), data, req.Model, "chat")
}

// ProxyEmbed posts an embed request to the cloud proxy and returns the raw
// response body.
func (p *CloudProxy) ProxyEmbed(ctx context.Context, model, input string) (io.ReadCloser, error) {
	data, _ := json.Marshal(map[string]any{"model": model, "input": input})
	proxyURL := getenv("VULOS_AI_EMBED_URL", "https://api.vulos.org/ai/embed")
	return p.doWithRetry(ctx, proxyURL, data, model, "embed")
}

// doWithRetry performs a POST to url with payload, retrying on 429/503.
// Returns the response body on success.
func (p *CloudProxy) doWithRetry(ctx context.Context, url string, payload []byte, model, endpoint string) (io.ReadCloser, error) {
	cert, err := p.certProv.DeviceCert(ctx)
	if err != nil {
		return nil, fmt.Errorf("airouter cloud: acquire device cert: %w", err)
	}

	delay := p.cfg.BaseDelay
	var lastErr error

	for attempt := 0; attempt <= p.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			// Back-off: wait, then refresh cert in case it expired.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-after(delay):
			}
			delay = minDuration(delay*2, p.cfg.MaxDelay)

			// Attempt cert refresh on retry (the provider may have invalidated
			// the previous cert during the back-off window).
			if fresh, ferr := p.certProv.DeviceCert(ctx); ferr == nil {
				cert = fresh
			}
		}

		started := time.Now()
		rc, status, err := p.doOnce(ctx, url, payload, cert)
		elapsed := time.Since(started).Milliseconds()

		if err != nil {
			lastErr = err
			// Network errors are not retried — only explicit rate-limit / unavailable responses.
			return nil, fmt.Errorf("airouter cloud: %w", lastErr)
		}

		if status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
			if rc != nil {
				rc.Close()
			}
			lastErr = fmt.Errorf("airouter cloud: proxy returned %d (attempt %d/%d)", status, attempt+1, p.cfg.MaxRetries+1)
			log.Printf("[airouter] cloud proxy %d on attempt %d — retrying after %s", status, attempt+1, delay)
			continue
		}

		if status < 200 || status >= 300 {
			body, _ := io.ReadAll(rc)
			rc.Close()
			return nil, fmt.Errorf("airouter cloud: proxy returned %d: %s", status, body)
		}

		// Success — report usage asynchronously so the stream is returned immediately.
		rec := UsageRecord{
			Endpoint:    endpoint,
			Model:       model,
			RequestedAt: started,
			DurationMs:  elapsed,
			StatusCode:  status,
		}
		go p.reporter.ReportUsage(context.Background(), rec)

		return rc, nil
	}

	return nil, fmt.Errorf("airouter cloud: max retries exceeded: %w", lastErr)
}

// doOnce performs a single HTTP POST and returns (body, statusCode, error).
func (p *CloudProxy) doOnce(ctx context.Context, url string, payload []byte, cert []byte) (io.ReadCloser, int, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if len(cert) > 0 {
		httpReq.Header.Set("Authorization", "VulosDevice "+encodeDeviceCert(cert))
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, 0, err
	}
	return resp.Body, resp.StatusCode, nil
}

// after returns a channel that fires after d.  Extracted for testability.
// In production this is time.After; tests can replace it via the cloudProxy
// config's BaseDelay set to 0.
func after(d time.Duration) <-chan time.Time {
	if d <= 0 {
		ch := make(chan time.Time, 1)
		ch <- time.Now()
		return ch
	}
	return time.After(d)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
