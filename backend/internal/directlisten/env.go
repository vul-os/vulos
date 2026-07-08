package directlisten

// env.go — config-gating for the direct public listener. OFF BY DEFAULT: a box
// only opts in when it actually has a reachable public IP/hostname (most boxes are
// NAT'd/CGNAT and must stay purely on the relay path). Wiring in cmd/server reads
// FromEnv and only starts the listener when Enabled() is true.
//
// Env surface:
//
//	VULOS_DIRECT_ENABLE=1                 opt in (required; unset => relay only)
//	VULOS_DIRECT_HOSTNAME=box1.example.net public DNS name (required for ACME +
//	                                       to build the advertised endpoint)
//	VULOS_DIRECT_ADDR=:443                listen addr (default :443)
//	VULOS_DIRECT_CERT_MODE=acme|provided  cert source (default acme)
//	VULOS_DIRECT_CERT_FILE / _KEY_FILE    provided-cert paths (provided mode)
//	VULOS_DIRECT_ACME_CACHE=/path         autocert cache dir (default under state)
//	VULOS_DIRECT_ACME_EMAIL=you@ex.com    ACME account contact (optional)
//	VULOS_DIRECT_ENDPOINT=https://...     override advertised endpoint (optional)

import (
	"net/http"
	"os"
	"strings"
)

// EnvConfig is the parsed opt-in configuration.
type EnvConfig struct {
	Enabled           bool
	Hostname          string
	Addr              string
	CertMode          CertMode
	CertFile, KeyFile string
	ACMECacheDir      string
	ACMEEmail         string
	AdvertiseEndpoint string
}

// FromEnv reads the direct-listener configuration. defaultACMECache is used when
// VULOS_DIRECT_ACME_CACHE is unset (callers pass a path under the box state dir).
func FromEnv(defaultACMECache string) EnvConfig {
	c := EnvConfig{
		Enabled:           os.Getenv("VULOS_DIRECT_ENABLE") == "1",
		Hostname:          strings.TrimSpace(os.Getenv("VULOS_DIRECT_HOSTNAME")),
		Addr:              strings.TrimSpace(os.Getenv("VULOS_DIRECT_ADDR")),
		CertFile:          strings.TrimSpace(os.Getenv("VULOS_DIRECT_CERT_FILE")),
		KeyFile:           strings.TrimSpace(os.Getenv("VULOS_DIRECT_KEY_FILE")),
		ACMECacheDir:      strings.TrimSpace(os.Getenv("VULOS_DIRECT_ACME_CACHE")),
		ACMEEmail:         strings.TrimSpace(os.Getenv("VULOS_DIRECT_ACME_EMAIL")),
		AdvertiseEndpoint: strings.TrimSpace(os.Getenv("VULOS_DIRECT_ENDPOINT")),
	}
	if c.Addr == "" {
		c.Addr = ":443"
	}
	if c.ACMECacheDir == "" {
		c.ACMECacheDir = defaultACMECache
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("VULOS_DIRECT_CERT_MODE"))) {
	case "provided":
		c.CertMode = CertModeProvided
	default: // "acme" or unset
		c.CertMode = CertModeACME
	}
	return c
}

// BuildConfig assembles a Service Config from the env config + the box's real OS
// http.Handler (the SAME handler the relay-fronted path serves — auth parity).
func (c EnvConfig) BuildConfig(handler http.Handler) Config {
	return Config{
		Handler:           handler,
		Hostname:          c.Hostname,
		AdvertiseEndpoint: c.AdvertiseEndpoint,
		Addr:              c.Addr,
		CertMode:          c.CertMode,
		CertFile:          c.CertFile,
		KeyFile:           c.KeyFile,
		ACMECacheDir:      c.ACMECacheDir,
		ACMEEmail:         c.ACMEEmail,
	}
}
