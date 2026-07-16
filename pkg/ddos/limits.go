package ddos

import (
	"net/http"
	"strings"
)

// BodySizeConfig maps route prefixes to max body sizes in bytes.
type BodySizeConfig struct {
	Routes []BodySizeRoute
}

// BodySizeRoute maps a path prefix to a max body size.
type BodySizeRoute struct {
	Prefix  string
	MaxSize int64 // bytes
}

const (
	bodySizeAuth    = 8 * 1024         // 8 KB — auth routes
	bodySizeDefault = 1 * 1024 * 1024  // 1 MB — general API
	bodySizeJMAP    = 10 * 1024 * 1024 // 10 MB — JMAP
	bodySizeMail    = 50 * 1024 * 1024 // 50 MB — mail submission
)

// DefaultBodySizeConfig is the built-in per-route body size policy.
// More-specific prefixes must appear before less-specific ones.
var DefaultBodySizeConfig = BodySizeConfig{
	Routes: []BodySizeRoute{
		{Prefix: "/api/auth/", MaxSize: bodySizeAuth},
		{Prefix: "/jmap/", MaxSize: bodySizeJMAP},
		{Prefix: "/api/mail/submit", MaxSize: bodySizeMail},
		{Prefix: "/", MaxSize: bodySizeDefault},
	},
}

// resolveBodyLimit returns the appropriate max body size for the given request path.
func resolveBodyLimit(cfg BodySizeConfig, reqPath string) int64 {
	for _, route := range cfg.Routes {
		if strings.HasPrefix(reqPath, route.Prefix) {
			return route.MaxSize
		}
	}
	return bodySizeDefault
}

// BodySizeLimiter returns middleware that applies http.MaxBytesReader to all
// POST, PUT, and PATCH requests according to the given config. Requests
// exceeding the limit will receive a 413 from the downstream handler (when it
// reads the body).
//
// Server-level timeouts (ReadHeaderTimeout, ReadTimeout, WriteTimeout,
// IdleTimeout) must be set on the http.Server itself. Recommended values:
//
//	ReadHeaderTimeout: 5s
//	ReadTimeout:       30s
//	WriteTimeout:      30s
//	IdleTimeout:       120s
//
// These cannot be enforced in middleware — they are properties of the listener.
func BodySizeLimiter(cfg BodySizeConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch:
				limit := resolveBodyLimit(cfg, r.URL.Path)
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}
