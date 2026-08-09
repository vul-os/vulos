package llmuxclient

// embedded.go — llmux running IN THE OS PROCESS as a Go library.
//
// llmux 0.1.x split its gateway (core/gateway) from its HTTP shell
// (core/server), so the routing, sovereignty gate, BYOK resolution, retries,
// failover, cost attribution and metering are all reachable without a socket.
// This file is the OS's composition of that gateway: it is a faithful port of
// what cmd/llmux's runServe wires up, MINUS the HTTP server and MINUS the
// background work.
//
// Two properties are deliberate and load-bearing:
//
//  1. NOTHING STARTS BY ITSELF. gateway.New opens no sockets and starts no
//     goroutines, and this package does not call Start unless the operator
//     asked for it by name. An embedded gateway quietly GETting openrouter.ai
//     every six hours from inside the OS process is exactly the surprise
//     embedding is supposed to remove — so the price-feed URLs config.Default()
//     ships are also cleared when background work is off, making "no surprise
//     egress" structural rather than a promise about a call we happen not to
//     make.
//
//  2. NO INHERITED DATABASE. config.Load resolves cfg.Postgres from
//     DATABASE_URL / VULOS_DATABASE_URL, and gateway.New connects AND MIGRATES
//     eagerly when it is set. In a Vulos deployment those variables name the
//     OS's OWN database, so embedding llmux would silently migrate an llmux
//     schema into it at boot. The DSN is dropped unless the operator opts in.
//
// The metering seam is unchanged: usage still goes llmux → CP, wired here from
// the same cfg.CP fields the sidecar reads, so switching a box from remote to
// embedded does not move where billing is decided.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/vul-os/llmux/core/byok"
	"github.com/vul-os/llmux/core/config"
	"github.com/vul-os/llmux/core/gateway"
	"github.com/vul-os/llmux/core/openai"
	"github.com/vul-os/llmux/integration/cp"
)

// EmbeddedOptions configures the in-process gateway. The zero value is valid:
// it loads llmux's defaults plus llmux's own environment variables, starts
// nothing, and opens nothing.
type EmbeddedOptions struct {
	// ConfigPath is the llmux JSON config file (the same file the sidecar takes
	// via -config). Empty means defaults + environment only; a missing file at a
	// named path is not an error, matching config.Load.
	ConfigPath string

	// Token is the account token presented to Gateway.Authorize on every
	// request. It is the in-process equivalent of the "Authorization: Bearer"
	// header the remote backend sends, and it is what selects BYOK vs the
	// central provider keys: Authorize resolves it to a Principal, and dispatch
	// looks up that account's own provider key. Empty is fine when no credential
	// wall is configured (no static keys, no cp) — Authorize is then a no-op.
	Token string

	// Background opts into Gateway.Start: the price-catalog syncer (which GETs
	// the configured pricing sources every sync_interval_minutes) and the file
	// key store's spend flusher. OFF by default. When it is off the pricing
	// sources are cleared too, so the syncer has nothing to fetch even if some
	// future caller starts it.
	Background bool

	// AllowPostgres permits an inherited DATABASE_URL / VULOS_DATABASE_URL to
	// reach gateway.New, which will connect to it and run llmux's key/spend
	// migrations immediately. Off by default: see the file comment.
	AllowPostgres bool

	// Logf receives startup lines. nil discards them.
	Logf func(format string, args ...any)
}

// Embedded is an in-process llmux gateway behind the Backend seam.
type Embedded struct {
	gw      *gateway.Gateway
	cfg     *config.Config
	token   string
	started bool

	// closers run on Close, in reverse order of registration.
	closers []func()
}

// NewEmbedded builds the in-process gateway. It performs no network I/O and
// starts no goroutines unless opts.Background is set (or a cp URL is
// configured, whose usage logger owns its own reconcile loop — the same
// goroutine the sidecar runs).
func NewEmbedded(ctx context.Context, opts EmbeddedOptions) (*Embedded, error) {
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("llmuxclient: embedded config: %w", err)
	}

	if cfg.Postgres != "" && !opts.AllowPostgres {
		logf("[llmux/embedded] ignoring an inherited Postgres DSN (DATABASE_URL/VULOS_DATABASE_URL): " +
			"gateway.New would connect and run llmux migrations against the OS's own database at boot. " +
			"Set VULOS_AI_EMBEDDED_POSTGRES=1 if that is really what you want; key spend stays in memory otherwise")
		cfg.Postgres = ""
		cfg.PostgresSchema = ""
	}
	if !opts.Background {
		// config.Default() ships two price-feed URLs. We never call Start, but
		// clearing the sources makes that a property of the configuration rather
		// than of one call site.
		cfg.Pricing.Sources = nil
		cfg.Pricing.Azure = false
	}

	e := &Embedded{cfg: cfg, token: opts.Token}

	gw, err := gateway.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("llmuxclient: embedded gateway: %w", err)
	}
	e.gw = gw
	e.closers = append(e.closers, func() { _ = gw.Close() })

	if err := e.wireBYOK(logf); err != nil {
		_ = e.Close()
		return nil, err
	}
	if err := e.wireUsage(logf); err != nil {
		_ = e.Close()
		return nil, err
	}

	if opts.Background {
		if err := gw.Start(ctx); err != nil {
			_ = e.Close()
			return nil, fmt.Errorf("llmuxclient: embedded gateway start: %w", err)
		}
		e.started = true
		logf("[llmux/embedded] background work STARTED on request (VULOS_AI_EMBEDDED_BACKGROUND): "+
			"price-catalog sync every %dm from %v, plus the key-spend flusher",
			cfg.Pricing.SyncIntervalMinutes, cfg.Pricing.Sources)
	} else {
		logf("[llmux/embedded] background work off: no price-catalog sync, no outbound price-feed GETs. " +
			"Requests are served unpriced unless pricing.catalog_path or pricing.overrides is configured; " +
			"set VULOS_AI_EMBEDDED_BACKGROUND=1 to run the syncer (and the file key-spend flusher) in-process")
		if cfg.KeyStorePath != "" {
			logf("[llmux/embedded] key_store_path is set but the spend flusher is part of the background work; " +
				"per-key spend will not be persisted until VULOS_AI_EMBEDDED_BACKGROUND=1")
		}
	}

	logf("[llmux/embedded] in-process llmux ready: %s", cfg)
	return e, nil
}

// wireBYOK enables per-account provider keys when a KEK is configured, exactly
// as cmd/llmux does. Without it every request uses the central provider keys.
//
// Note for operators: the sidecar also exposes /admin/byok/* to register those
// keys; the OS does not proxy that surface, so an embedded gateway reads BYOK
// credentials from an existing encrypted store file (byok.store_path).
func (e *Embedded) wireBYOK(logf func(string, ...any)) error {
	kekStr := e.cfg.BYOK.ResolveKEK()
	if kekStr == "" {
		return nil
	}
	kek, err := byok.ParseKEK(kekStr)
	if err != nil {
		return fmt.Errorf("llmuxclient: embedded byok KEK: %w", err)
	}
	crypter, err := byok.NewCrypter(kek)
	if err != nil {
		return fmt.Errorf("llmuxclient: embedded byok crypter: %w", err)
	}
	if path := e.cfg.BYOK.StorePath; path != "" {
		store, err := byok.NewFileStore(crypter, path)
		if err != nil {
			return fmt.Errorf("llmuxclient: embedded byok store: %w", err)
		}
		e.gw.SetBYOKStore(store)
		logf("[llmux/embedded] BYOK enabled (encrypted store -> %s)", path)
		return nil
	}
	e.gw.SetBYOKStore(byok.NewMemStore(crypter))
	logf("[llmux/embedded] BYOK enabled (in-memory store; nothing survives a restart)")
	return nil
}

// wireUsage composes the usage sinks the sidecar composes: the optional JSONL
// file, and the optional control plane. This is the metering seam — usage goes
// llmux → CP, and embedding llmux must not move that decision into the OS.
func (e *Embedded) wireUsage(logf func(string, ...any)) error {
	var loggers []gateway.UsageLogger

	if path := e.cfg.UsageLogPath; path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("llmuxclient: embedded usage log %s: %w", path, err)
		}
		e.closers = append(e.closers, func() { _ = f.Close() })
		loggers = append(loggers, gateway.NewJSONLUsageLogger(f))
		logf("[llmux/embedded] usage log -> %s", path)
	}

	cpCfg := cp.New(e.cfg.CP.URL, e.cfg.CP.SharedSecret).
		WithRPM(e.cfg.CP.RPM).
		WithDegradedFailOpen(e.cfg.CP.DegradedFailOpen).
		WithDegradedRPM(e.cfg.CP.DegradedRPM).
		WithUsageSpoolPath(e.cfg.CP.UsageSpoolPath)
	if secs := e.cfg.CP.EntitlementTTLSeconds; secs > 0 {
		cpCfg = cpCfg.WithEntitlementTTL(time.Duration(secs) * time.Second)
	}
	if cpCfg.Enabled() {
		e.gw.SetIdentity(cp.NewIdentity(cpCfg))
		e.gw.SetBudgetGate(cp.NewBudgetGate(cpCfg))
		usage := cp.NewUsageLogger(cpCfg)
		e.closers = append(e.closers, usage.Close)
		loggers = append(loggers, usage)
		logf("[llmux/embedded] control plane integration on (%s) — metering still goes llmux -> CP", cpCfg.BaseURL)
	}

	switch len(loggers) {
	case 0: // keep the gateway's NopUsageLogger
	case 1:
		e.gw.SetUsageLogger(loggers[0])
	default:
		e.gw.SetUsageLogger(cp.NewMultiUsageLogger(loggers...))
	}
	return nil
}

// Gateway exposes the underlying llmux gateway for tests and for callers that
// need something the Backend seam does not surface.
func (e *Embedded) Gateway() *gateway.Gateway { return e.gw }

// Mode implements Backend.
func (e *Embedded) Mode() Mode { return ModeEmbedded }

// Status implements Backend. It names the providers the gateway can reach so an
// operator can tell an embedded gateway that is wired up from one that loaded
// cleanly and has nowhere to send a request.
func (e *Embedded) Status() map[string]any {
	providers := make([]string, 0, len(e.cfg.Providers))
	for _, p := range e.cfg.Providers {
		providers = append(providers, p.Name)
	}
	return map[string]any{
		"status":     "ok",
		"mode":       string(ModeEmbedded),
		"gateway":    "in-process",
		"providers":  providers,
		"models":     len(e.gw.Models()),
		"background": e.started,
	}
}

// Close implements Backend.
func (e *Embedded) Close() error {
	for i := len(e.closers) - 1; i >= 0; i-- {
		e.closers[i]()
	}
	e.closers = nil
	return nil
}

// Chat implements Backend. It runs the request through the in-process gateway
// and frames the response itself.
//
// A streaming request is framed by sseSink, byte-identically to llmux's own
// HTTP shell (see sse.go). A non-streaming request writes the gateway's
// already-serialized body — the same bytes the proxy path copied through, under
// the same headers, so the response does not change shape with the backend.
func (e *Embedded) Chat(ctx context.Context, w http.ResponseWriter, body []byte) (bool, error) {
	var req openai.ChatCompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return false, fmt.Errorf("llmuxclient: invalid chat request: %w", err)
	}

	ctx, release, err := e.gw.Authorize(ctx, e.token)
	// release is never nil, even on error, and MUST be called or an in-flight
	// budget hold leaks for the life of the process.
	defer release()
	if err != nil {
		return false, err
	}

	res, err := e.gw.Prepare(ctx, req.Model)
	if err != nil {
		return false, err
	}

	if !req.Stream {
		out, err := e.gw.ChatRoute(ctx, &req, body, res)
		if err != nil {
			return false, err
		}
		writeStreamHeaders(w)
		_, werr := w.Write(out.Body)
		return true, werr
	}

	sink := &sseSink{w: w}
	err = e.gw.ChatStreamSink(ctx, &req, body, res, sink)
	return sink.committed(), err
}

// Embed implements Backend.
func (e *Embedded) Embed(ctx context.Context, model, input string) ([]float32, string, error) {
	if model == "" {
		model = defaultEmbedModel
	}
	ctx, release, err := e.gw.Authorize(ctx, e.token)
	defer release()
	if err != nil {
		return nil, model, err
	}

	raw, err := json.Marshal(input)
	if err != nil {
		return nil, model, err
	}
	resp, err := e.gw.Embed(ctx, &openai.EmbeddingRequest{Model: model, Input: raw})
	if err != nil {
		return nil, model, err
	}
	resolved := resp.Model
	if resolved == "" {
		resolved = model
	}
	if len(resp.Data) == 0 || len(resp.Data[0].Embedding) == 0 {
		return nil, resolved, errors.New("llmuxclient: empty embedding in response")
	}
	vec := make([]float32, len(resp.Data[0].Embedding))
	for i, v := range resp.Data[0].Embedding {
		vec[i] = float32(v)
	}
	return vec, resolved, nil
}

// Models implements Backend, returning the same OpenAI-shaped listing the
// sidecar serves at GET /v1/models.
func (e *Embedded) Models(context.Context) (json.RawMessage, error) {
	data, err := json.Marshal(e.gw.ModelList())
	if err != nil {
		return nil, err
	}
	return json.RawMessage(data), nil
}

// ---------------------------------------------------------------------------
// SSE sink
// ---------------------------------------------------------------------------

// sseSink adapts the OS's SSE writer to gateway.StreamSink. It mirrors llmux's
// core/server sseSink one method at a time, because any divergence here is a
// wire-format change that only shows up in a client half way through a stream.
type sseSink struct {
	w   http.ResponseWriter
	sse *sseWriter
}

// Open commits the response before the first chunk. Failing here (an
// unflushable ResponseWriter, so no streaming is possible) aborts the stream
// with nothing written, and the caller answers with an ordinary JSON error.
func (s *sseSink) Open() error {
	sse, ok := newSSEWriter(s.w)
	if !ok {
		return errors.New("llmuxclient: response writer cannot stream")
	}
	s.sse = sse
	return nil
}

// Chunk writes one chat completion chunk as an SSE data event.
func (s *sseSink) Chunk(c *openai.ChatCompletionChunk) error { return s.sse.event(c) }

// Failed relays a failure that happened AFTER the stream started, as one more
// data event carrying an OpenAI error object. The status line is long gone.
func (s *sseSink) Failed(er *openai.ErrorResponse) {
	if s.sse != nil {
		_ = s.sse.event(er)
	}
}

// Done writes the terminal sentinel, opening the stream first if no chunk ever
// arrived so the client still sees a valid terminal stream.
func (s *sseSink) Done() {
	if s.sse == nil {
		if err := s.Open(); err != nil {
			return
		}
	}
	s.sse.done()
}

// committed reports whether the response was committed (headers written).
func (s *sseSink) committed() bool { return s.sse != nil }
