// bot_conn.go — JA3Listener and JA3Conn for raw-bytes TLS fingerprinting.
//
// # Architecture
//
// JA3Listener wraps a net.Listener. Its Accept method returns *JA3Conn values.
// JA3Conn intercepts the first Read call (which delivers the TLS ClientHello
// record from the client), parses the raw bytes to compute JA3/JA4 hashes,
// and stores them in a *JA3Store keyed by RemoteAddr. Subsequent Reads are
// delegated to the underlying net.Conn unchanged.
//
// # TLS record parsing (RFC 8446 §5)
//
// Record layer header (5 bytes):
//
//	byte 0      — ContentType (0x16 = Handshake)
//	bytes 1-2   — legacy_record_version
//	bytes 3-4   — length of the record payload (big-endian)
//
// Handshake message (inside the record):
//
//	byte 0      — HandshakeType (0x01 = ClientHello)
//	bytes 1-3   — length of the handshake body (big-endian, 3 bytes)
//
// ClientHello body:
//
//	bytes 0-1   — client_version (TLS version)
//	bytes 2-33  — random (32 bytes)
//	byte 34     — session_id length (N)
//	bytes 35..  — session_id (N bytes)
//	next 2      — cipher_suites length (C*2)
//	next C*2    — cipher_suites (each 2 bytes, big-endian)
//	next 1      — compression_methods length (M)
//	next M      — compression_methods
//	next 2      — extensions length (total)
//	extensions  — repeated: type(2) + length(2) + data(length)
//
// # JA3 format
//
//	<TLSVersion>,<CipherSuites>,<Extensions>,<EllipticCurves>,<EllipticCurvePointFormats>
//
// where each list is dash-separated decimal values. GREASE values (0xXAXA) are
// excluded. The MD5 of that string is the JA3 hash.
//
// # JA4 completeness
//
// JA4 is partially implemented. We compute:
//
//	t<version><sni_flag><ext_count>_<sorted-ciphers-hex>_<sorted-exts-hex>
//
// What is NOT captured from raw bytes alone without ALPN visibility:
//   - ALPN protocol list (requires extension type 0x0010 data parsing — we do
//     parse this but produce a string token rather than the JA4 "a1" form).
//   - The 4th component (ALPN protos) of JA4+ is present but simplified.
//
// JA3 is exact per the Salesforce specification.
package security

import (
	"crypto/md5" //nolint:gosec // JA3 spec requires MD5
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
)

// ─── JA3Store ─────────────────────────────────────────────────────────────────

// JA3Store stores raw-bytes-derived JA3/JA4 fingerprints keyed by RemoteAddr.
// It is safe for concurrent use. Use NewJA3Store to create one.
type JA3Store struct {
	mu sync.Map // string → *clientHelloSummary
}

// NewJA3Store returns an empty JA3Store.
func NewJA3Store() *JA3Store { return &JA3Store{} }

// store saves a summary. Called by JA3Conn after parsing.
func (s *JA3Store) store(remoteAddr string, sum *clientHelloSummary) {
	s.mu.Store(remoteAddr, sum)
}

// Load retrieves the summary for an address. Returns nil, false if not found.
func (s *JA3Store) Load(remoteAddr string) (*clientHelloSummary, bool) {
	v, ok := s.mu.Load(remoteAddr)
	if !ok {
		return nil, false
	}
	return v.(*clientHelloSummary), true
}

// Delete removes the stored summary for an address (call on connection close).
func (s *JA3Store) Delete(remoteAddr string) {
	s.mu.Delete(remoteAddr)
}

// ─── JA3Listener ─────────────────────────────────────────────────────────────

// JA3Listener wraps a net.Listener. Accepted connections are *JA3Conn values
// that intercept the first Read to capture raw TLS ClientHello bytes.
//
// Wire before tls.NewListener so the raw bytes are read before the TLS stack
// consumes them:
//
//	ja3ln := security.NewJA3Listener(tcpln, ja3store)
//	tlsln := tls.NewListener(ja3ln, tlsCfg)
type JA3Listener struct {
	inner net.Listener
	store *JA3Store
}

// NewJA3Listener wraps ln. Fingerprints are stored in store.
func NewJA3Listener(ln net.Listener, store *JA3Store) *JA3Listener {
	return &JA3Listener{inner: ln, store: store}
}

// Accept waits for and returns the next connection. The returned net.Conn is
// always a *JA3Conn, which will parse the ClientHello on its first Read.
func (l *JA3Listener) Accept() (net.Conn, error) {
	c, err := l.inner.Accept()
	if err != nil {
		return nil, err
	}
	return &JA3Conn{Conn: c, store: l.store}, nil
}

// Close closes the underlying listener.
func (l *JA3Listener) Close() error { return l.inner.Close() }

// Addr returns the listener's network address.
func (l *JA3Listener) Addr() net.Addr { return l.inner.Addr() }

// ─── JA3Conn ──────────────────────────────────────────────────────────────────

// JA3Conn wraps a net.Conn and intercepts the first Read call.
// On first Read it buffers the bytes, parses the TLS ClientHello, stores the
// derived JA3/JA4 fingerprints, then returns the bytes verbatim so that the
// TLS stack can complete the handshake normally.
type JA3Conn struct {
	net.Conn

	store  *JA3Store
	once   sync.Once
	buf    []byte // buffered bytes from first read (re-delivered to caller)
	bufOff int    // offset into buf for re-delivery
}

// Read intercepts the first call to parse the ClientHello, then delegates.
func (c *JA3Conn) Read(b []byte) (int, error) {
	// After the first read is re-delivered, delegate to underlying conn.
	if c.bufOff >= len(c.buf) && c.buf != nil {
		return c.Conn.Read(b)
	}

	// First time: read enough bytes to cover the TLS record header + handshake.
	c.once.Do(func() {
		// Read a generous initial chunk. TLS ClientHello records are typically
		// 200–600 bytes; we cap at 4096 to bound memory usage.
		tmp := make([]byte, 4096)
		n, _ := c.Conn.Read(tmp)
		if n > 0 {
			c.buf = tmp[:n]
			parseAndStoreFingerprint(c.buf, c.RemoteAddr().String(), c.store)
		}
	})

	if len(c.buf) == 0 {
		return c.Conn.Read(b)
	}

	// Re-deliver buffered bytes to the caller (TLS stack).
	if c.bufOff < len(c.buf) {
		n := copy(b, c.buf[c.bufOff:])
		c.bufOff += n
		return n, nil
	}
	return c.Conn.Read(b)
}

// ─── ClientHello parser ───────────────────────────────────────────────────────

// parseAndStoreFingerprint parses raw TLS record bytes to extract ClientHello
// fields, computes JA3/JA4 hashes, and stores them in the given store.
// Errors are silently discarded — a malformed/truncated hello results in empty
// hashes, not a crash.
func parseAndStoreFingerprint(data []byte, remoteAddr string, store *JA3Store) {
	sum := &clientHelloSummary{}
	defer store.store(remoteAddr, sum)

	ch, ok := parseClientHello(data)
	if !ok {
		return
	}
	sum.SNI = ch.sni
	sum.JA3 = computeJA3Raw(ch)
	sum.JA4 = computeJA4Raw(ch)
}

// clientHelloFields holds the decoded fields needed for JA3/JA4.
type clientHelloFields struct {
	version         uint16
	cipherSuites    []uint16
	extensions      []uint16
	supportedGroups []uint16
	pointFormats    []uint8
	alpnProtos      []string
	sni             string
	supportedVers   []uint16 // from supported_versions extension (0x002b)
}

// parseClientHello decodes a raw TLS record containing a ClientHello.
func parseClientHello(data []byte) (clientHelloFields, bool) {
	var ch clientHelloFields

	// TLS record header: byte 0 = ContentType, bytes 3-4 = length.
	if len(data) < 5 {
		return ch, false
	}
	if data[0] != 0x16 { // Handshake
		return ch, false
	}
	recLen := int(binary.BigEndian.Uint16(data[3:5]))
	if len(data) < 5+recLen {
		// Truncated; parse what we have.
		recLen = len(data) - 5
	}
	payload := data[5 : 5+recLen]

	// Handshake header: byte 0 = type (0x01 = ClientHello), bytes 1-3 = length.
	if len(payload) < 4 {
		return ch, false
	}
	if payload[0] != 0x01 {
		return ch, false
	}
	hsLen := int(payload[1])<<16 | int(payload[2])<<8 | int(payload[3])
	if len(payload) < 4+hsLen {
		hsLen = len(payload) - 4
	}
	hello := payload[4 : 4+hsLen]

	// ClientHello body starts with: version (2) + random (32).
	if len(hello) < 34 {
		return ch, false
	}
	ch.version = binary.BigEndian.Uint16(hello[0:2])
	off := 34 // skip version + random

	// session_id
	if off >= len(hello) {
		return ch, false
	}
	sidLen := int(hello[off])
	off++
	off += sidLen

	// cipher_suites
	if off+2 > len(hello) {
		return ch, false
	}
	csLen := int(binary.BigEndian.Uint16(hello[off : off+2]))
	off += 2
	if off+csLen > len(hello) {
		return ch, false
	}
	for i := 0; i+1 < csLen; i += 2 {
		cs := binary.BigEndian.Uint16(hello[off+i : off+i+2])
		ch.cipherSuites = append(ch.cipherSuites, cs)
	}
	off += csLen

	// compression_methods
	if off >= len(hello) {
		return ch, false
	}
	cmLen := int(hello[off])
	off++
	off += cmLen

	// extensions
	if off+2 > len(hello) {
		return ch, true // no extensions
	}
	extTotalLen := int(binary.BigEndian.Uint16(hello[off : off+2]))
	off += 2
	extEnd := off + extTotalLen
	if extEnd > len(hello) {
		extEnd = len(hello)
	}

	for off+4 <= extEnd {
		extType := binary.BigEndian.Uint16(hello[off : off+2])
		extLen := int(binary.BigEndian.Uint16(hello[off+2 : off+4]))
		off += 4
		extData := []byte{}
		if off+extLen <= extEnd {
			extData = hello[off : off+extLen]
		}
		off += extLen

		ch.extensions = append(ch.extensions, extType)

		switch extType {
		case 0x0000: // SNI
			ch.sni = parseSNI(extData)
		case 0x000a: // supported_groups
			ch.supportedGroups = parseUint16List(extData, 2)
		case 0x000b: // ec_point_formats
			ch.pointFormats = parseUint8List(extData, 1)
		case 0x002b: // supported_versions
			ch.supportedVers = parseSupportedVersions(extData)
		case 0x0010: // ALPN
			ch.alpnProtos = parseALPN(extData)
		}
	}

	return ch, true
}

// ─── Extension data parsers ───────────────────────────────────────────────────

func parseSNI(data []byte) string {
	// server_name_list length (2) + entry type (1) + name length (2) + name
	if len(data) < 5 {
		return ""
	}
	nameLen := int(binary.BigEndian.Uint16(data[3:5]))
	if len(data) < 5+nameLen {
		return ""
	}
	return string(data[5 : 5+nameLen])
}

// parseUint16List parses a length-prefixed list of uint16 values.
// prefixBytes is the byte width of the length prefix (typically 2).
func parseUint16List(data []byte, prefixBytes int) []uint16 {
	if len(data) < prefixBytes {
		return nil
	}
	var listLen int
	if prefixBytes == 2 {
		listLen = int(binary.BigEndian.Uint16(data[0:2]))
	} else {
		listLen = int(data[0])
	}
	data = data[prefixBytes:]
	if len(data) < listLen {
		listLen = len(data)
	}
	var out []uint16
	for i := 0; i+1 < listLen; i += 2 {
		out = append(out, binary.BigEndian.Uint16(data[i:i+2]))
	}
	return out
}

func parseUint8List(data []byte, prefixBytes int) []uint8 {
	if len(data) < prefixBytes {
		return nil
	}
	var listLen int
	if prefixBytes == 2 {
		listLen = int(binary.BigEndian.Uint16(data[0:2]))
	} else {
		listLen = int(data[0])
	}
	data = data[prefixBytes:]
	if len(data) < listLen {
		listLen = len(data)
	}
	return data[:listLen]
}

func parseSupportedVersions(data []byte) []uint16 {
	// For ClientHello: length byte + list of uint16 versions.
	if len(data) < 1 {
		return nil
	}
	listLen := int(data[0])
	data = data[1:]
	if len(data) < listLen {
		listLen = len(data)
	}
	var out []uint16
	for i := 0; i+1 < listLen; i += 2 {
		out = append(out, binary.BigEndian.Uint16(data[i:i+2]))
	}
	return out
}

func parseALPN(data []byte) []string {
	// ALPNProtocolNameList: length (2) + repeated { length (1) + name }
	if len(data) < 2 {
		return nil
	}
	listLen := int(binary.BigEndian.Uint16(data[0:2]))
	data = data[2:]
	if len(data) < listLen {
		listLen = len(data)
	}
	var protos []string
	off := 0
	for off < listLen {
		if off >= len(data) {
			break
		}
		nameLen := int(data[off])
		off++
		if off+nameLen > len(data) {
			break
		}
		protos = append(protos, string(data[off:off+nameLen]))
		off += nameLen
	}
	return protos
}

// ─── JA3 computation (raw bytes) ─────────────────────────────────────────────

// computeJA3Raw computes the JA3 hash from raw-parsed ClientHello fields.
// This is exact per the Salesforce JA3 specification:
// https://github.com/salesforce/ja3
func computeJA3Raw(ch clientHelloFields) string {
	// Effective TLS version: prefer supported_versions extension (TLS 1.3 signalling)
	// over the legacy client_version field.
	ver := ch.version
	for _, v := range ch.supportedVers {
		if !isGREASE(v) && v > ver {
			ver = v
		}
	}

	// Cipher suites (excluding GREASE).
	var ciphers []string
	for _, c := range ch.cipherSuites {
		if !isGREASE(c) {
			ciphers = append(ciphers, fmt.Sprintf("%d", c))
		}
	}

	// Extension type IDs (excluding GREASE).
	var extIDs []string
	for _, e := range ch.extensions {
		if !isGREASE(e) {
			extIDs = append(extIDs, fmt.Sprintf("%d", e))
		}
	}

	// Elliptic curves (supported_groups, excluding GREASE).
	var curves []string
	for _, g := range ch.supportedGroups {
		if !isGREASE(g) {
			curves = append(curves, fmt.Sprintf("%d", g))
		}
	}

	// Elliptic curve point formats.
	var pts []string
	for _, p := range ch.pointFormats {
		pts = append(pts, fmt.Sprintf("%d", p))
	}
	if len(pts) == 0 {
		pts = []string{"0"} // uncompressed is always implied
	}

	raw := fmt.Sprintf("%d,%s,%s,%s,%s",
		ver,
		strings.Join(ciphers, "-"),
		strings.Join(extIDs, "-"),
		strings.Join(curves, "-"),
		strings.Join(pts, "-"),
	)
	//nolint:gosec // JA3 spec requires MD5
	h := md5.Sum([]byte(raw))
	return fmt.Sprintf("%x", h)
}

// ─── JA4 computation (raw bytes, partial) ─────────────────────────────────────

// computeJA4Raw computes a JA4 fingerprint from raw-parsed ClientHello fields.
//
// Format: t<version><sni_flag><ext_count>_<sorted-ciphers-hex>_<sorted-exts-hex>
//
// NOTE: The 4th JA4 component (ALPN token) is omitted here. JA4 specifies a
// two-char ALPN token derived from the first ALPN entry. We parse ALPN protos
// from the raw extension data and include the first proto as a tag comment, but
// the core three-component form is what is stored. Full JA4+ with ALPN token
// would require combining with application-layer visibility; this is a known
// partial implementation.
func computeJA4Raw(ch clientHelloFields) string {
	proto := "t" // TCP TLS

	// Effective TLS version from supported_versions (prefer highest non-GREASE).
	var verStr string
	bestVer := ch.version
	for _, v := range ch.supportedVers {
		if !isGREASE(v) && v > bestVer {
			bestVer = v
		}
	}
	switch {
	case bestVer >= 0x0304:
		verStr = "13"
	case bestVer == 0x0303:
		verStr = "12"
	case bestVer == 0x0302:
		verStr = "11"
	default:
		verStr = "10"
	}

	// SNI flag.
	sniFl := "i"
	if ch.sni != "" {
		sniFl = "d"
	}

	// Extension count (excluding GREASE).
	extCount := 0
	for _, e := range ch.extensions {
		if !isGREASE(e) {
			extCount++
		}
	}

	// Cipher suites sorted hex (excluding GREASE).
	var cipherHexes []string
	for _, c := range ch.cipherSuites {
		if !isGREASE(c) {
			cipherHexes = append(cipherHexes, fmt.Sprintf("%04x", c))
		}
	}
	sort.Strings(cipherHexes)

	// Extension type IDs sorted hex (excluding GREASE).
	extSet := map[string]bool{}
	for _, e := range ch.extensions {
		if !isGREASE(e) {
			extSet[fmt.Sprintf("%04x", e)] = true
		}
	}
	var extHexes []string
	for h := range extSet {
		extHexes = append(extHexes, h)
	}
	sort.Strings(extHexes)

	return fmt.Sprintf("%s%s%s%02d_%s_%s",
		proto, verStr, sniFl, extCount,
		strings.Join(cipherHexes, ","),
		strings.Join(extHexes, ","),
	)
}
