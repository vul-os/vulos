package security

import (
	"encoding/binary"
	"net/http/httptest"
	"testing"
)

// TestBotScoreHeadlessBrowser verifies a headless chrome UA scores high.
func TestBotScoreHeadlessBrowser(t *testing.T) {
	store := openTestStore(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 HeadlessChrome/120.0.0.0")

	score := BotScore(req, store)
	if score < 0.5 {
		t.Errorf("want score>=0.5 for headless UA, got %.2f", score)
	}
}

// TestBotScoreScannerUA verifies a known scanner UA scores at maximum.
func TestBotScoreScannerUA(t *testing.T) {
	store := openTestStore(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("User-Agent", "sqlmap/1.6.0#stable (https://sqlmap.org)")

	score := BotScore(req, store)
	if score < 0.9 {
		t.Errorf("want score>=0.9 for sqlmap UA, got %.2f", score)
	}
}

// TestBotScoreLegitBrowser verifies a normal browser UA scores low.
func TestBotScoreLegitBrowser(t *testing.T) {
	store := openTestStore(t)
	req := httptest.NewRequest("GET", "/dashboard", nil)
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "+
			"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	score := BotScore(req, store)
	if score >= 0.5 {
		t.Errorf("want score<0.5 for legit browser, got %.2f", score)
	}
}

// TestBotScoreEmptyUA verifies an empty UA scores suspicious.
func TestBotScoreEmptyUA(t *testing.T) {
	store := openTestStore(t)
	req := httptest.NewRequest("GET", "/", nil)
	// No User-Agent set.

	score := BotScore(req, store)
	if score < 0.3 {
		t.Errorf("want score>=0.3 for empty UA, got %.2f", score)
	}
}

// TestBotScoreWebdriverHeader verifies X-WebDriver header triggers bot flag.
func TestBotScoreWebdriverHeader(t *testing.T) {
	store := openTestStore(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/120.0.0.0")
	req.Header.Set("Sec-WebDriver", "1")

	score := BotScore(req, store)
	if score < 0.5 {
		t.Errorf("want score>=0.5 for webdriver header, got %.2f", score)
	}
}

// ─── JA3 raw-bytes tests ──────────────────────────────────────────────────────

// buildSyntheticClientHello constructs a minimal TLS ClientHello record suitable
// for parseClientHello. The record includes:
//   - 1 cipher suite: TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 (0xC02F)
//   - SNI extension for "test.vulos.org"
//   - supported_groups extension with secp256r1 (0x0017)
func buildSyntheticClientHello(t *testing.T) []byte {
	t.Helper()

	// ClientHello body (before record framing):
	// version (2) + random (32) + session_id_len (1) + cipher_suites_len (2) +
	// cipher_suite (2) + compression_methods_len (1) + compression_method (1) +
	// extensions_len (2) + SNI ext + supported_groups ext

	sni := "test.vulos.org"
	// SNI extension data: list_len(2) + type(1=host_name) + name_len(2) + name
	sniName := []byte(sni)
	sniExt := make([]byte, 2+1+2+len(sniName))
	binary.BigEndian.PutUint16(sniExt[0:2], uint16(1+2+len(sniName))) // list len
	sniExt[2] = 0                                                     // host_name type
	binary.BigEndian.PutUint16(sniExt[3:5], uint16(len(sniName)))
	copy(sniExt[5:], sniName)

	// supported_groups extension: list_len(2) + group (0x0017 = secp256r1)
	sgExt := []byte{0x00, 0x02, 0x00, 0x17}

	// Build extension list.
	//   ext type 0x0000 (SNI) + len(2) + data
	//   ext type 0x000a (supported_groups) + len(2) + data
	extBuf := make([]byte, 0, 4+len(sniExt)+4+len(sgExt))
	extBuf = append(extBuf, 0x00, 0x00) // SNI type
	extBuf = appendUint16(extBuf, uint16(len(sniExt)))
	extBuf = append(extBuf, sniExt...)
	extBuf = append(extBuf, 0x00, 0x0a) // supported_groups type
	extBuf = appendUint16(extBuf, uint16(len(sgExt)))
	extBuf = append(extBuf, sgExt...)

	// ClientHello body.
	hello := make([]byte, 0, 64)
	hello = append(hello, 0x03, 0x03)          // client_version = TLS 1.2
	hello = append(hello, make([]byte, 32)...) // random
	hello = append(hello, 0x00)                // session_id_len = 0
	hello = append(hello, 0x00, 0x02)          // cipher_suites_len = 2
	hello = append(hello, 0xC0, 0x2F)          // TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
	hello = append(hello, 0x01, 0x00)          // compression_methods: 1 byte, null
	// extensions_len
	hello = appendUint16(hello, uint16(len(extBuf)))
	hello = append(hello, extBuf...)

	// Handshake header: type(1) + length(3)
	hs := make([]byte, 0, 4+len(hello))
	hs = append(hs, 0x01) // ClientHello
	hs = append(hs, byte(len(hello)>>16), byte(len(hello)>>8), byte(len(hello)))
	hs = append(hs, hello...)

	// TLS record header: content_type(1) + version(2) + length(2)
	rec := make([]byte, 0, 5+len(hs))
	rec = append(rec, 0x16)       // Handshake
	rec = append(rec, 0x03, 0x01) // TLS 1.0 (legacy record version)
	rec = appendUint16(rec, uint16(len(hs)))
	rec = append(rec, hs...)
	return rec
}

func appendUint16(b []byte, v uint16) []byte {
	return append(b, byte(v>>8), byte(v))
}

// TestJA3RawBytesCapture verifies that parseClientHello extracts the cipher
// suite, SNI, and supported group correctly from a synthetic ClientHello, and
// that computeJA3Raw produces a non-empty hash.
func TestJA3RawBytesCapture(t *testing.T) {
	data := buildSyntheticClientHello(t)

	ch, ok := parseClientHello(data)
	if !ok {
		t.Fatal("parseClientHello: want ok=true")
	}
	if ch.sni != "test.vulos.org" {
		t.Errorf("want SNI=%q, got %q", "test.vulos.org", ch.sni)
	}
	if len(ch.cipherSuites) == 0 {
		t.Error("want at least one cipher suite")
	}
	found := false
	for _, cs := range ch.cipherSuites {
		if cs == 0xC02F {
			found = true
		}
	}
	if !found {
		t.Errorf("want cipher 0xC02F in list %v", ch.cipherSuites)
	}
	if len(ch.supportedGroups) == 0 {
		t.Error("want at least one supported group")
	}

	hash := computeJA3Raw(ch)
	if len(hash) != 32 {
		t.Errorf("want 32-char MD5 hex hash, got len=%d: %q", len(hash), hash)
	}
}

// TestJA4RawComputation verifies that computeJA4Raw produces a non-empty string
// in the expected format: t<ver><sni><count>_<ciphers>_<exts>.
func TestJA4RawComputation(t *testing.T) {
	data := buildSyntheticClientHello(t)
	ch, ok := parseClientHello(data)
	if !ok {
		t.Fatal("parseClientHello: want ok=true")
	}

	ja4 := computeJA4Raw(ch)
	if ja4 == "" {
		t.Fatal("want non-empty JA4 string")
	}
	// Must start with "t" (TCP TLS).
	if ja4[0] != 't' {
		t.Errorf("want JA4 starting with 't', got %q", ja4)
	}
	// Must contain two '_' separators.
	parts := 0
	for _, c := range ja4 {
		if c == '_' {
			parts++
		}
	}
	if parts != 2 {
		t.Errorf("want 2 '_' separators in JA4, got %d: %q", parts, ja4)
	}
}

// TestJA3StoreViaBotScore verifies that BotScore reads from the global JA3Store
// when populated, producing a fingerprint-influenced score.
func TestJA3StoreViaBotScore(t *testing.T) {
	// Temporarily set the global JA3Store.
	prev := globalJA3Store
	testStore := NewJA3Store()
	globalJA3Store = testStore
	t.Cleanup(func() { globalJA3Store = prev })

	// Plant a known-bad JA3 hash.
	remoteAddr := "10.0.0.1:9999"
	testStore.store(remoteAddr, &clientHelloSummary{
		JA3: "e7d705a3286e19ea42f587b344ee6865", // sqlmap in knownBadJA3
		JA4: "t12i05_c02f_0000",
		SNI: "",
	})

	dbStore := openTestStore(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = remoteAddr
	req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/120.0.0.0")

	score := BotScore(req, dbStore)
	if score < 0.5 {
		t.Errorf("want score>=0.5 for known-bad JA3 via raw store, got %.2f", score)
	}
}
