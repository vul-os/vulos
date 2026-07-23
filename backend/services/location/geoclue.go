package location

// OPTIONAL box-side feed-OUT: renders a Position as standard NMEA 0183
// sentences (GPRMC + GGA) so a LOCAL gpsd instance can ingest them and, in
// turn, feed GeoClue — letting native Linux apps (which query GeoClue, not an
// HTTP API) see the browser-reported / modem-reported position.
//
// WIRING IS AN OPS STEP, NOT DONE HERE: this file only knows how to FORMAT
// sentences and best-effort APPEND them to a path. Making a native app
// actually see them requires, outside this code:
//   1. A fifo or pty that gpsd watches, e.g. `gpsd /path/to/fifo` (or a real
//      pty pair created with `socat PTY,link=/path,raw` and gpsd pointed at
//      the PTY).
//   2. gpsd running with its D-Bus/geoclue export enabled so GeoClue's
//      "gpsd source" picks it up (distro-dependent; some ship a dedicated
//      geoclue-2.0 gpsd config).
// None of that is started or supervised here — WriteNMEA merely appends to
// whatever path it is given, and is non-fatal (returns an error, never
// panics) so a box with no gpsd/fifo configured just gets a no-op logged by
// the caller.
import (
	"fmt"
	"math"
	"os"
	"time"
)

// FormatNMEA renders pos as a GPRMC sentence followed by a GGA sentence
// (CRLF-terminated, as NMEA requires), each with a correct XOR checksum. Pure
// function — no I/O — so it's fully unit-testable without gpsd/hardware.
func FormatNMEA(pos Position) string {
	ts := pos.TS
	if ts == 0 {
		ts = time.Now().Unix()
	}
	t := time.Unix(ts, 0).UTC()
	hhmmss := t.Format("150405") + ".00"
	ddmmyy := t.Format("020106")

	latVal, latHemi := degToNMEA(pos.Lat, 2)
	lngVal, lngHemi := degToNMEA(pos.Lng, 3)

	speedKnots := pos.Speed * 1.9438444924 // Geolocation API Speed is m/s
	heading := pos.Heading

	rmc := fmt.Sprintf("GPRMC,%s,A,%s,%s,%s,%s,%.1f,%.1f,%s,,,A",
		hhmmss, latVal, string(latHemi), lngVal, string(lngHemi), speedKnots, heading, ddmmyy)

	gga := fmt.Sprintf("GPGGA,%s,%s,%s,%s,%s,1,08,1.0,%.1f,M,0.0,M,,",
		hhmmss, latVal, string(latHemi), lngVal, string(lngHemi), pos.Altitude)

	return sentenceWithChecksum(rmc) + sentenceWithChecksum(gga)
}

// degToNMEA converts decimal degrees to NMEA's "ddmm.mmmm" (or "dddmm.mmmm"
// for longitude, degWidth=3) format plus its hemisphere letter. positive is
// N/E, negative is S/W.
func degToNMEA(deg float64, degWidth int) (string, byte) {
	hemi := byte('N')
	if degWidth == 3 {
		hemi = 'E'
	}
	if deg < 0 {
		if degWidth == 3 {
			hemi = 'W'
		} else {
			hemi = 'S'
		}
		deg = -deg
	}
	whole := math.Floor(deg)
	minutes := (deg - whole) * 60
	// Float rounding can push minutes to exactly 60.0000 for a degree value
	// whose fractional part sits a hair below the next whole degree; NMEA
	// requires minutes < 60, so carry into the degrees field.
	if minutes >= 60 {
		minutes -= 60
		whole++
	}
	format := fmt.Sprintf("%%0%dd%%07.4f", degWidth)
	return fmt.Sprintf(format, int(whole), minutes), hemi
}

// sentenceWithChecksum wraps body in "$...*CS\r\n", CS = 2-hex-digit XOR
// checksum of every byte in body (NMEA's standard checksum algorithm).
func sentenceWithChecksum(body string) string {
	var cs byte
	for i := 0; i < len(body); i++ {
		cs ^= body[i]
	}
	return fmt.Sprintf("$%s*%02X\r\n", body, cs)
}

// WriteNMEA best-effort appends FormatNMEA(pos) to path (intended to be a
// fifo or pty that a local gpsd instance is watching — see the package doc
// comment above). Non-fatal: returns an error rather than panicking, and
// callers should treat a failure (path doesn't exist, no gpsd listening on a
// fifo with no reader, permission denied, etc.) as a no-op, not a request
// failure — feeding GeoClue is a best-effort side channel, never a
// requirement for GET/POST /api/location to work.
func WriteNMEA(path string, pos Position) error {
	if path == "" {
		return fmt.Errorf("location: no NMEA output path configured")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("location: open NMEA sink: %w", err)
	}
	defer f.Close()
	_, err = f.WriteString(FormatNMEA(pos))
	if err != nil {
		return fmt.Errorf("location: write NMEA sink: %w", err)
	}
	return nil
}
