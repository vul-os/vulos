// Package location is the box-side per-user LOCATION service (LOCATION-01).
//
// The box itself has no GPS. Instead, the client's browser (Geolocation API)
// reports the device's position to the box, which caches the latest fix PER
// USER (X-User-ID) and exposes it to other apps running on the box (maps,
// weather, etc.) so they don't each need their own browser permission prompt.
//
// A modem-GPS fallback (modem_location.go) is used only when the client fix is
// stale or absent, for boxes with a GNSS-capable USB LTE modem attached.
//
// Scoping mirrors services/telephony: requests carry X-User-ID (stamped by the
// auth middleware, which strips any client-supplied copy before it reaches the
// app layer — see services/auth/handlers.go), and data is kept in a per-user
// map so user A can never read user B's position.
package location

import (
	"math"
	"sync"
	"time"
)

// staleAfter is how old a cached position may be before GetPosition marks it
// stale (and, if configured, falls back to modem GPS).
const staleAfter = 60 * time.Second

// Position is one location fix. Lat/Lng are required; the rest are optional
// (zero value = "not reported" for Accuracy/Altitude/Heading/Speed, since a
// browser Geolocation fix may omit any of them).
type Position struct {
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	Accuracy float64 `json:"accuracy,omitempty"`
	Altitude float64 `json:"altitude,omitempty"`
	Heading  float64 `json:"heading,omitempty"`
	Speed    float64 `json:"speed,omitempty"`
	TS       int64   `json:"ts"`     // unix seconds the fix was recorded
	Source   string  `json:"source"` // "client" | "modem"
}

// Reading is a Position enriched with server-computed staleness metadata, as
// returned by GetPosition / GET /api/location.
type Reading struct {
	Position
	AgeSeconds int64 `json:"age_seconds"`
	Stale      bool  `json:"stale"`
	// Found is false when there is no cached position at all for the user (and
	// no usable modem fallback). Not serialized directly; handlers use it to
	// decide between 200 {found:false} and a populated reading.
	Found bool `json:"-"`
}

// ModemFallback is the seam modem_location.go implements. Kept as an interface
// (rather than a concrete *modemSource field) so tests can stub it and so a box
// with no modem support compiled in can pass nil.
type ModemFallback interface {
	// ModemPosition returns the modem's current GNSS fix and true, or
	// (Position{}, false) when unavailable (no mmcli, no modem, no GNSS fix).
	ModemPosition() (Position, bool)
}

// Service is the box location service. Safe for concurrent use.
type Service struct {
	mu     sync.Mutex
	byUser map[string]Position
	modem  ModemFallback // optional; nil disables the modem-GPS fallback
}

// New creates the service. modem may be nil to disable the modem-GPS fallback
// entirely (e.g. builds/boxes with no telephony hardware support compiled in).
func New(modem ModemFallback) *Service {
	return &Service{
		byUser: map[string]Position{},
		modem:  modem,
	}
}

// SetPosition validates and records the latest position reported by userID's
// client. Returns an error (and stores nothing) when the fix is invalid.
func (s *Service) SetPosition(userID string, p Position) error {
	if userID == "" {
		return errUnauthenticated
	}
	if err := validatePosition(p); err != nil {
		return err
	}
	if p.TS == 0 {
		p.TS = time.Now().Unix()
	}
	if p.Source == "" {
		p.Source = "client"
	}
	s.mu.Lock()
	s.byUser[userID] = p
	s.mu.Unlock()
	return nil
}

// GetPosition returns userID's latest known position: the cached client fix if
// present, else — only when that fix is absent or stale — a modem-GPS fallback
// (if configured and available). Found is false when neither source has one.
func (s *Service) GetPosition(userID string) Reading {
	if userID == "" {
		return Reading{Found: false}
	}
	s.mu.Lock()
	p, ok := s.byUser[userID]
	s.mu.Unlock()

	now := time.Now().Unix()
	if ok {
		age := now - p.TS
		if age < 0 {
			age = 0
		}
		stale := age >= int64(staleAfter/time.Second)
		if !stale {
			return Reading{Position: p, AgeSeconds: age, Stale: false, Found: true}
		}
		// Client fix is stale — try the modem before giving up on freshness.
		if s.modem != nil {
			if mp, mok := s.modem.ModemPosition(); mok {
				if mp.TS == 0 {
					mp.TS = now
				}
				mAge := now - mp.TS
				if mAge < 0 {
					mAge = 0
				}
				return Reading{Position: mp, AgeSeconds: mAge, Stale: mAge >= int64(staleAfter/time.Second), Found: true}
			}
		}
		// No modem fallback available — return the stale client fix; callers
		// can decide whether stale-but-present is good enough.
		return Reading{Position: p, AgeSeconds: age, Stale: true, Found: true}
	}

	// No client fix at all — modem is the only possible source.
	if s.modem != nil {
		if mp, mok := s.modem.ModemPosition(); mok {
			if mp.TS == 0 {
				mp.TS = now
			}
			mAge := now - mp.TS
			if mAge < 0 {
				mAge = 0
			}
			return Reading{Position: mp, AgeSeconds: mAge, Stale: mAge >= int64(staleAfter/time.Second), Found: true}
		}
	}
	return Reading{Found: false}
}

var errUnauthenticated = positionError("location: unauthenticated")
var errInvalidPosition = positionError("location: invalid position")

// positionError is a plain string error type so callers can compare/inspect
// without importing "errors" for a trivial sentinel.
type positionError string

func (e positionError) Error() string { return string(e) }

// validatePosition rejects NaN/Inf and out-of-range coordinates. Optional
// fields (Accuracy/Altitude/Heading/Speed) are range-checked only enough to
// reject NaN/Inf — a browser can legitimately report 0 or omit them, and
// altitude/speed have no universal bound worth enforcing here.
func validatePosition(p Position) error {
	if math.IsNaN(p.Lat) || math.IsInf(p.Lat, 0) {
		return errInvalidPosition
	}
	if math.IsNaN(p.Lng) || math.IsInf(p.Lng, 0) {
		return errInvalidPosition
	}
	if p.Lat < -90 || p.Lat > 90 {
		return errInvalidPosition
	}
	if p.Lng < -180 || p.Lng > 180 {
		return errInvalidPosition
	}
	for _, v := range []float64{p.Accuracy, p.Altitude, p.Heading, p.Speed} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errInvalidPosition
		}
	}
	return nil
}
