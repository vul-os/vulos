package llmuxclient

// mode.go — which of the three backends this box runs, resolved from the
// environment.
//
// CONFIG SURFACE
//
//	VULOS_AI_MODE                    embedded | remote | off. Unset = auto.
//	                                 Auto picks remote when LLMUX_URL (alias
//	                                 VULOS_LLMUX_URL) is set, embedded when an
//	                                 llmux config file is named, otherwise
//	                                 unconfigured — so a box that worked before
//	                                 embedding existed keeps working unchanged.
//
//	remote:
//	  LLMUX_URL / VULOS_LLMUX_URL    the gateway base URL
//	  LLMUX_KEY / VULOS_LLMUX_KEY    the bearer token sent to it
//
//	embedded:
//	  VULOS_LLMUX_CONFIG /           llmux's own JSON config file (the same file
//	  LLMUX_CONFIG                   the sidecar takes via -config). Optional:
//	                                 without it, llmux's defaults plus its own
//	                                 environment (OLLAMA_HOST,
//	                                 LLMUX_LOCAL_BASE_URL, OPENAI_API_KEY, ...)
//	                                 configure the providers.
//	  LLMUX_KEY / VULOS_LLMUX_KEY    the account token presented to
//	                                 Gateway.Authorize — the same value the
//	                                 remote backend sends as a bearer token, and
//	                                 what selects BYOK vs central provider keys.
//	  VULOS_AI_EMBEDDED_BACKGROUND   1/true to start llmux's background work:
//	                                 the price-catalog syncer (outbound GETs to
//	                                 the configured price feeds every
//	                                 sync_interval_minutes) and the file key
//	                                 store's spend flusher. OFF by default.
//	  VULOS_AI_EMBEDDED_POSTGRES     1/true to let an inherited DATABASE_URL /
//	                                 VULOS_DATABASE_URL reach the gateway, which
//	                                 connects and migrates on construction. OFF
//	                                 by default. See embedded.go.
//
// llmux reads plenty of its own environment on top of this (LLMUX_BYOK_KEK,
// LLMUX_CP_URL, LLMUX_USAGE_LOG, ...); those keep their meaning in embedded
// mode because the config is loaded by llmux's own config.Load.

import (
	"context"
	"strings"

	"vulos/backend/services/env"
)

// The environment variable names are constants so the documentation above and
// the code below cannot drift apart.
const (
	modeEnv       = "VULOS_AI_MODE"
	backgroundEnv = "VULOS_AI_EMBEDDED_BACKGROUND"
	postgresEnv   = "VULOS_AI_EMBEDDED_POSTGRES"
)

// embeddedConfigPath returns the llmux JSON config file for embedded mode, or
// "" when none is named.
func embeddedConfigPath() string {
	return env.FirstNonEmptyEnv("VULOS_LLMUX_CONFIG", "LLMUX_CONFIG")
}

// accountToken returns the token the OS presents to the gateway. It is the same
// value on both backends: a bearer header remotely, Gateway.Authorize's argument
// in-process.
func accountToken() string {
	return env.FirstNonEmptyEnv("LLMUX_KEY", "VULOS_LLMUX_KEY")
}

// envFlag reports whether an environment variable is set to a truthy value.
func envFlag(name string) bool {
	switch strings.ToLower(env.FirstNonEmptyEnv(name)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// ResolveMode reports which backend the environment asks for. An explicit
// VULOS_AI_MODE always wins; otherwise the choice is inferred, and inference
// prefers remote so that an existing LLMUX_URL deployment is never silently
// moved in-process.
func ResolveMode() Mode {
	switch strings.ToLower(env.FirstNonEmptyEnv(modeEnv)) {
	case string(ModeEmbedded):
		return ModeEmbedded
	case string(ModeRemote):
		return ModeRemote
	case "off", "none", "disabled":
		return ModeUnconfigured
	}
	if _, ok := FromEnv(); ok {
		return ModeRemote
	}
	if embeddedConfigPath() != "" {
		return ModeEmbedded
	}
	return ModeUnconfigured
}

// OpenFromEnv builds the backend the environment asks for. It never returns
// nil: a misconfigured or failed backend degrades to Unconfigured (503 on every
// /api/ai/* route) rather than taking the OS down, because an unreachable LLM
// is not a reason for a box not to boot.
//
// ctx bounds any background work the embedded gateway is asked to start; it
// should be the server's shutdown context.
func OpenFromEnv(ctx context.Context, logf func(format string, args ...any)) Backend {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	switch ResolveMode() {
	case ModeRemote:
		cfg, ok := FromEnv()
		if !ok {
			logf("[llmuxclient] %s=remote but LLMUX_URL (alias VULOS_LLMUX_URL) is unset — "+
				"/api/ai/* will return 503", modeEnv)
			return Unconfigured()
		}
		logf("[llmuxclient] remote llmux gateway at %s", cfg.BaseURL)
		return RemoteBackend(New(cfg))

	case ModeEmbedded:
		e, err := NewEmbedded(ctx, EmbeddedOptions{
			ConfigPath:    embeddedConfigPath(),
			Token:         accountToken(),
			Background:    envFlag(backgroundEnv),
			AllowPostgres: envFlag(postgresEnv),
			Logf:          logf,
		})
		if err != nil {
			logf("[llmuxclient] embedded llmux failed to initialise: %v — /api/ai/* will return 503", err)
			return Unconfigured()
		}
		return e

	default:
		logf("[llmuxclient] no AI gateway configured — /api/ai/* will return 503. "+
			"Set LLMUX_URL for a remote llmux, or %s=embedded to run it in this process", modeEnv)
		return Unconfigured()
	}
}
