// bot.go — bot detection via JA3/JA4 TLS fingerprinting + UA heuristics.
//
// JA3/JA4 computation:
//   - JA3 is captured via tls.Config.GetConfigForClient which has access to
//     the ClientHelloInfo. The hello is stored in a per-connection context via
//     a sync.Map keyed on the remote addr. Handlers call BotScore(r) to read it.
//   - JA4 follows the public spec (t<version><SNI_flag><N_ext>_<cipher_hex>_<ext_hex>).
//
// Known-good JA3 hashes are loaded from the embedded data/legit_ja3.txt file.
// Known-bad hashes are hard-coded (scanner tools).
package security

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // JA3 spec requires MD5
	"crypto/tls"
	_ "embed"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed data/legit_ja3.txt
var legitJA3Data []byte

// known JA3 hashes for common scanning/bot tools.
var knownBadJA3 = map[string]string{
	"e7d705a3286e19ea42f587b344ee6865": "sqlmap",
	"6bea3f4cc11f2fc7c1e2e90041e6f7a6": "nikto",
	"a0e9f5d64349fb13191bc781f81f42e1": "curl-scanner",
	"de9f2c7fd25e1b3afad3e85a0226210c": "masscan",
	"05af1f5ca1b87cc9cc9b25185115607d": "zgrab",
}

var (
	legitJA3Set  map[string]bool
	legitJA3Once sync.Once
)

func loadLegitJA3() map[string]bool {
	legitJA3Once.Do(func() {
		m := make(map[string]bool)
		scanner := bufio.NewScanner(bytes.NewReader(legitJA3Data))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			// Take only the hash (before any whitespace / comment).
			hash := strings.Fields(line)[0]
			m[hash] = true
		}
		legitJA3Set = m
	})
	return legitJA3Set
}

// helloStore maps remoteAddr → clientHelloSummary for in-flight connections.
// Populated by GetTLSConfigForClient (GetConfigForClient callback path).
var helloStore sync.Map // string → *clientHelloSummary

// globalJA3Store is the package-level raw-bytes JA3 store, populated by
// JA3Conn on the JA3Listener path. Set via SetJA3Store before serving.
var globalJA3Store *JA3Store

// SetJA3Store registers the raw-bytes store used by BotScore. Call once during
// server startup after constructing the JA3Listener.
func SetJA3Store(s *JA3Store) { globalJA3Store = s }

type clientHelloSummary struct {
	JA3 string
	JA4 string
	SNI string
}

// GetTLSConfigForClient is wired into tls.Config.GetConfigForClient.
// It captures the ClientHelloInfo and stores it in helloStore, keyed by
// remote address. Call this when constructing the TLS listener.
func GetTLSConfigForClient(base *tls.Config) func(*tls.ClientHelloInfo) (*tls.Config, error) {
	return func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		summary := &clientHelloSummary{
			SNI: hello.ServerName,
			JA3: computeJA3(hello),
			JA4: computeJA4(hello),
		}
		if hello.Conn != nil {
			helloStore.Store(hello.Conn.RemoteAddr().String(), summary)
		}
		if base != nil {
			return base, nil
		}
		return nil, nil
	}
}

// computeJA3 computes the JA3 fingerprint from a TLS ClientHello.
// Format: TLSVersion,Ciphers,Extensions,EllipticCurves,EllipticCurvePointFormats
// MD5-hashed.
func computeJA3(hello *tls.ClientHelloInfo) string {
	// TLS version: use max supported.
	var version uint16
	if len(hello.SupportedVersions) > 0 {
		version = hello.SupportedVersions[0]
	} else {
		version = tls.VersionTLS12
	}

	// Ciphers (filter out GREASE values 0xXAXA).
	var ciphers []string
	for _, c := range hello.CipherSuites {
		if !isGREASE(c) {
			ciphers = append(ciphers, strconv.FormatUint(uint64(c), 10))
		}
	}

	// Extensions — derived from SupportedVersions, SupportedCurves, etc.
	// We use a proxy: count of advertised fields as extension type IDs.
	// A full implementation would need raw bytes; this is a best-effort proxy.
	var extIDs []string
	if len(hello.SupportedVersions) > 0 {
		extIDs = append(extIDs, "43") // supported_versions
	}
	if hello.ServerName != "" {
		extIDs = append(extIDs, "0") // SNI
	}
	if len(hello.SupportedProtos) > 0 {
		extIDs = append(extIDs, "16") // ALPN
	}
	if len(hello.SignatureSchemes) > 0 {
		extIDs = append(extIDs, "13") // signature_algorithms
	}
	if len(hello.SupportedCurves) > 0 {
		extIDs = append(extIDs, "10") // supported_groups
	}

	// Curves.
	var curves []string
	for _, c := range hello.SupportedCurves {
		if !isGREASECurve(uint16(c)) {
			curves = append(curves, strconv.FormatUint(uint64(c), 10))
		}
	}

	// Point formats — default to "0" (uncompressed).
	pointFormats := "0"

	raw := fmt.Sprintf("%d,%s,%s,%s,%s",
		version,
		strings.Join(ciphers, "-"),
		strings.Join(extIDs, "-"),
		strings.Join(curves, "-"),
		pointFormats,
	)
	//nolint:gosec // JA3 spec requires MD5
	sum := md5.Sum([]byte(raw))
	return fmt.Sprintf("%x", sum)
}

// computeJA4 computes a JA4 fingerprint following the public spec draft.
// Format: t<proto><2-digit-version><sni-flag><ext-count>_<sorted-ciphers-hex>_<sorted-exts-hex>
func computeJA4(hello *tls.ClientHelloInfo) string {
	// Protocol indicator.
	proto := "t" // TCP TLS

	// Version.
	var verStr string
	if len(hello.SupportedVersions) > 0 {
		switch hello.SupportedVersions[0] {
		case tls.VersionTLS13:
			verStr = "13"
		case tls.VersionTLS12:
			verStr = "12"
		default:
			verStr = "10"
		}
	} else {
		verStr = "12"
	}

	// SNI flag.
	sniFl := "i" // no SNI
	if hello.ServerName != "" {
		sniFl = "d"
	}

	// Number of extensions (proxy via field presence).
	extCount := 0
	if hello.ServerName != "" {
		extCount++
	}
	if len(hello.SupportedProtos) > 0 {
		extCount++
	}
	if len(hello.SignatureSchemes) > 0 {
		extCount++
	}
	if len(hello.SupportedCurves) > 0 {
		extCount++
	}
	if len(hello.SupportedVersions) > 0 {
		extCount++
	}

	// Ciphers — sorted hex values.
	var cipherHexes []string
	for _, c := range hello.CipherSuites {
		if !isGREASE(c) {
			cipherHexes = append(cipherHexes, fmt.Sprintf("%04x", c))
		}
	}
	sort.Strings(cipherHexes)

	// Extension type IDs (sorted hex).
	extMap := map[string]bool{}
	if hello.ServerName != "" {
		extMap["0000"] = true
	}
	if len(hello.SupportedVersions) > 0 {
		extMap["002b"] = true
	}
	if len(hello.SignatureSchemes) > 0 {
		extMap["000d"] = true
	}
	if len(hello.SupportedCurves) > 0 {
		extMap["000a"] = true
	}
	if len(hello.SupportedProtos) > 0 {
		extMap["0010"] = true
	}
	var extHexes []string
	for h := range extMap {
		extHexes = append(extHexes, h)
	}
	sort.Strings(extHexes)

	return fmt.Sprintf("%s%s%s%02d_%s_%s",
		proto, verStr, sniFl, extCount,
		strings.Join(cipherHexes, ","),
		strings.Join(extHexes, ","),
	)
}

func isGREASE(v uint16) bool      { return v&0x0f0f == 0x0a0a }
func isGREASECurve(v uint16) bool { return v&0x0f0f == 0x0a0a }

// ─── Bot score ────────────────────────────────────────────────────────────────

// BotScore computes a 0..1 bot probability for the request.
// 0 = clearly human; 1 = definitely bot.
func BotScore(r *http.Request, store *Store) float64 {
	score := 0.0
	reasons := []string{}

	ua := r.UserAgent()
	lower := strings.ToLower(ua)

	// Check for empty UA.
	if ua == "" {
		score += 0.5
		reasons = append(reasons, "empty-ua")
	}

	// Check for headless browser markers in UA.
	headlessMarkers := []string{
		"headlesschrome", "phantomjs", "selenium", "webdriver", "puppeteer",
		"playwright", "zombie", "htmlunit", "mechanize",
	}
	for _, m := range headlessMarkers {
		if strings.Contains(lower, m) {
			score += 0.8
			reasons = append(reasons, "headless:"+m)
			break
		}
	}

	// Check for known scanning tools in UA.
	scannerMarkers := []string{
		"sqlmap", "nikto", "nmap", "masscan", "burpsuite", "acunetix",
		"nessus", "openvas", "w3af", "dirbuster", "gobuster", "ffuf", "nuclei",
	}
	for _, m := range scannerMarkers {
		if strings.Contains(lower, m) {
			score += 1.0
			reasons = append(reasons, "scanner:"+m)
			break
		}
	}

	// Check webdriver header (set by some headless browsers).
	if r.Header.Get("Sec-WebDriver") != "" || r.Header.Get("X-WebDriver") != "" {
		score += 0.7
		reasons = append(reasons, "webdriver-header")
	}

	// JA3/JA4 fingerprint check.
	// Primary source: raw-bytes store populated by JA3Conn (JA3Listener path).
	// Fallback: GetConfigForClient-derived helloStore (when listener not wrapped).
	var sum *clientHelloSummary
	if globalJA3Store != nil {
		if s, ok := globalJA3Store.Load(r.RemoteAddr); ok {
			sum = s
		}
	}
	if sum == nil {
		if v, ok := helloStore.Load(r.RemoteAddr); ok {
			sum = v.(*clientHelloSummary)
		}
	}
	if sum != nil {
		legit := loadLegitJA3()

		if reason, bad := knownBadJA3[sum.JA3]; bad {
			score += 0.9
			reasons = append(reasons, "bad-ja3:"+reason)
		} else if !legit[sum.JA3] && sum.JA3 != "" {
			// Unknown fingerprint — not necessarily bad, but mildly suspicious.
			score += 0.15
			reasons = append(reasons, "unknown-ja3:"+sum.JA3[:8])
		}

		// UA vs JA3 mismatch heuristic:
		// Chrome UA should have TLS 1.3 support; if JA4 shows TLS 1.2 only, flag.
		if strings.Contains(lower, "chrome") && strings.HasPrefix(sum.JA4, "tt12") {
			score += 0.3
			reasons = append(reasons, "ua-ja4-mismatch")
		}
	}

	// Cap at 1.0.
	if score > 1.0 {
		score = 1.0
	}

	// Persist to DB if above threshold.
	if score >= 0.5 && store != nil {
		ip := r.RemoteAddr
		if idx := strings.LastIndex(ip, ":"); idx != -1 {
			ip = ip[:idx]
		}
		reason := strings.Join(reasons, ";")
		if err := store.RecordBotEvent(context.Background(), ip, score, reason, ua); err != nil {
			log.Printf("[security/bot] record event: %v", err)
		}
	}

	return score
}

// BotMiddleware returns a middleware that checks bot score and blocks if >= blockThreshold.
// Routes that are APIs expecting programmatic access should pass a higher threshold.
func BotMiddleware(store *Store, blockThreshold float64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			score := BotScore(r, store)
			if score >= blockThreshold {
				ip := r.RemoteAddr
				log.Printf("[security/bot] blocking bot score=%.2f ip=%s ua=%q", score, ip, r.UserAgent())
				http.Error(w, `{"error":"automated traffic blocked"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CleanHelloStore removes the stored hello for an address after the connection closes.
// Cleans both the GetConfigForClient store and the raw-bytes JA3Store.
func CleanHelloStore(remoteAddr string) {
	helloStore.Delete(remoteAddr)
	if globalJA3Store != nil {
		globalJA3Store.Delete(remoteAddr)
	}
}
