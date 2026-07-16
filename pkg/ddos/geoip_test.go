package ddos

import (
	"net"
	"net/http/httptest"
	"os"
	"testing"
)

// netIPStubDB satisfies geoIPDB using net.IP.
type netIPStubDB struct {
	ipToCountry map[string]string
}

func (s *netIPStubDB) LookupCountry(ip net.IP) (string, error) {
	return s.ipToCountry[ip.String()], nil
}

func (s *netIPStubDB) Close() error { return nil }

func TestGeoIPFilter_BlockedByHeader(t *testing.T) {
	os.Unsetenv(envTrustHeader)
	f := &GeoIPFilter{blockedCountries: map[string]bool{"KP": true}}

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("CF-IPCountry", "KP")
	r.RemoteAddr = "1.2.3.4:80"

	if !f.IsBlocked(r) {
		t.Fatal("expected KP to be blocked")
	}
}

func TestGeoIPFilter_AllowedCountry(t *testing.T) {
	f := &GeoIPFilter{blockedCountries: map[string]bool{"KP": true}}

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("CF-IPCountry", "ZA")
	r.RemoteAddr = "1.2.3.4:80"

	if f.IsBlocked(r) {
		t.Fatal("ZA should not be blocked")
	}
}

func TestGeoIPFilter_SetBlockedCountries(t *testing.T) {
	f := &GeoIPFilter{blockedCountries: make(map[string]bool)}
	f.SetBlockedCountries([]string{"RU", "CN"})

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("CF-IPCountry", "RU")
	r.RemoteAddr = "1.2.3.4:80"

	if !f.IsBlocked(r) {
		t.Fatal("RU should be blocked after SetBlockedCountries")
	}
}
