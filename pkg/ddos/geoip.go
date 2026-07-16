package ddos

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
)

// GeoIPFilter enforces country-based filtering.
// It honours the CF-IPCountry header when present (provided by Cloudflare for free),
// and falls back to MaxMind GeoLite2 lookup when a DB path is configured.
//
// Env vars:
//
//	VULOS_BLOCK_COUNTRIES — comma-separated ISO-3166-1 alpha-2 codes (e.g. "KP,CU")
//	VULOS_GEOIP_DB_PATH   — path to MaxMind GeoLite2-Country.mmdb
type GeoIPFilter struct {
	blockedCountries map[string]bool
	db               geoIPDB // nil = CF-header only
}

// geoIPDB is an interface so we can swap MaxMind vs stub in tests.
type geoIPDB interface {
	LookupCountry(ip net.IP) (string, error)
	Close() error
}

// OpenGeoIPFilter creates a GeoIPFilter from environment config.
// If VULOS_GEOIP_DB_PATH is set and the file exists, it opens the MaxMind DB.
// If CF-IPCountry is provided by the edge, the DB lookup is skipped.
func OpenGeoIPFilter() (*GeoIPFilter, error) {
	f := &GeoIPFilter{
		blockedCountries: make(map[string]bool),
	}

	raw := os.Getenv("VULOS_BLOCK_COUNTRIES")
	for _, code := range strings.Split(raw, ",") {
		code = strings.TrimSpace(strings.ToUpper(code))
		if code != "" {
			f.blockedCountries[code] = true
		}
	}

	dbPath := os.Getenv("VULOS_GEOIP_DB_PATH")
	if dbPath != "" {
		db, err := openMaxMindDB(dbPath)
		if err != nil {
			log.Printf("[ddos/geoip] WARNING: could not open MaxMind DB at %s: %v — CF-header fallback only", dbPath, err)
		} else {
			f.db = db
			log.Printf("[ddos/geoip] MaxMind GeoLite2 DB opened: %s", dbPath)
		}
	} else {
		log.Println("[ddos/geoip] VULOS_GEOIP_DB_PATH not set — using CF-IPCountry header only")
	}

	return f, nil
}

// SetBlockedCountries replaces the current blocked-country set (for super-admin toggle).
func (f *GeoIPFilter) SetBlockedCountries(codes []string) {
	m := make(map[string]bool, len(codes))
	for _, c := range codes {
		c = strings.TrimSpace(strings.ToUpper(c))
		if c != "" {
			m[c] = true
		}
	}
	f.blockedCountries = m
}

// BlockedCountries returns the current blocked-country list.
func (f *GeoIPFilter) BlockedCountries() []string {
	out := make([]string, 0, len(f.blockedCountries))
	for c := range f.blockedCountries {
		out = append(out, c)
	}
	return out
}

// CountryForRequest returns the ISO country code for the request.
// Precedence: CF-IPCountry header → MaxMind DB → "" (unknown).
func (f *GeoIPFilter) CountryForRequest(r *http.Request) string {
	// Cloudflare provides CF-IPCountry for free, no DB needed.
	if cf := r.Header.Get("CF-IPCountry"); cf != "" && cf != "XX" && cf != "T1" {
		return strings.ToUpper(strings.TrimSpace(cf))
	}

	if f.db != nil {
		ip := net.ParseIP(RealClientIP(r))
		if ip != nil {
			country, err := f.db.LookupCountry(ip)
			if err == nil && country != "" {
				return strings.ToUpper(country)
			}
		}
	}
	return ""
}

// IsBlocked reports whether the request's country is in the blocked list.
func (f *GeoIPFilter) IsBlocked(r *http.Request) bool {
	if len(f.blockedCountries) == 0 {
		return false
	}
	country := f.CountryForRequest(r)
	if country == "" {
		return false
	}
	return f.blockedCountries[country]
}

// GeoIPMiddleware returns an http.Handler middleware that blocks requests from
// configured countries with 403.
func GeoIPMiddleware(filter *GeoIPFilter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if filter.IsBlocked(r) {
				country := filter.CountryForRequest(r)
				log.Printf("[ddos/geoip] 403 ip=%s country=%s path=%s",
					RealClientIP(r), country, r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "access not available in your region",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ── MaxMind DB adapter ────────────────────────────────────────────────────────

// maxmindDB wraps the maxminddb reader. We use a minimal interface to avoid
// hard-coupling the import at the top of this file — the actual import is in
// geoip_mmdb.go which is only compiled when the tag is present, or just
// uses maxminddb-golang directly.
type maxmindDBImpl struct {
	reader maxminddbReader
}

// maxminddbReader is satisfied by *maxminddb.Reader from oschwald/maxminddb-golang.
type maxminddbReader interface {
	Lookup(ip net.IP, result interface{}) error
	Close() error
}

func openMaxMindDB(path string) (geoIPDB, error) {
	r, err := openMaxmindReader(path)
	if err != nil {
		return nil, err
	}
	return &maxmindDBImpl{reader: r}, nil
}

func (m *maxmindDBImpl) LookupCountry(ip net.IP) (string, error) {
	var record struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	if err := m.reader.Lookup(ip, &record); err != nil {
		return "", err
	}
	return record.Country.ISOCode, nil
}

func (m *maxmindDBImpl) Close() error { return m.reader.Close() }
