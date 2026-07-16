package appsplatform

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Outbound-event signing (Slack-compatible v0 scheme, so receivers/verifiers
// written against the widely-understood Slack signature model port unchanged).
//
// For each outbound event POST the platform sends:
//
//	X-Vulos-Request-Timestamp: <unix seconds>
//	X-Vulos-Signature:         v0=<hex hmac-sha256>
//
// The signed string is exactly:
//
//	"<timestamp>" + "." + <raw request body bytes>
//
// signed with the app's signing secret using HMAC-SHA256. Receivers reproduce
// the signature over the timestamp + raw body they received and compare in
// constant time (see Verify). They SHOULD also reject stale timestamps to blunt
// replay (the example app uses a 5-minute window).

// SigHeaderTimestamp is the request header carrying the unix-seconds timestamp.
const SigHeaderTimestamp = "X-Vulos-Request-Timestamp"

// SigHeaderSignature is the request header carrying the "v0=" signature.
const SigHeaderSignature = "X-Vulos-Signature"

// sigBasestring builds the exact bytes that are HMAC'd: timestamp + "." + body.
func sigBasestring(timestamp string, body []byte) []byte {
	base := make([]byte, 0, len(timestamp)+1+len(body))
	base = append(base, timestamp...)
	base = append(base, '.')
	base = append(base, body...)
	return base
}

// Sign returns the "v0=<hex>" signature for (timestamp, body) under secret.
func Sign(timestamp string, body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(sigBasestring(timestamp, body))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

// Verify reports whether sig is a valid signature for (timestamp, body) under
// secret. The comparison is constant-time.
func Verify(timestamp string, body []byte, secret, sig string) bool {
	expected := Sign(timestamp, body, secret)
	return hmac.Equal([]byte(expected), []byte(sig))
}

// VerifyWithSkew is Verify plus a freshness check, giving receivers replay
// defense out of the box: it returns true only when sig is a valid signature for
// (timestamp, body) under secret AND timestamp is within maxAge of now (in
// either direction, to tolerate clock skew). The HMAC comparison is constant
// time. A non-numeric timestamp fails. A maxAge <= 0 disables the freshness
// check (equivalent to Verify). Callers typically pass 5 * time.Minute.
func VerifyWithSkew(timestamp string, body []byte, secret, sig string, maxAge time.Duration) bool {
	if !Verify(timestamp, body, secret, sig) {
		return false
	}
	if maxAge <= 0 {
		return true
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return false
	}
	delta := time.Now().Unix() - ts
	if delta < 0 {
		delta = -delta
	}
	return time.Duration(delta)*time.Second <= maxAge
}

// NowTimestamp returns the current unix-seconds timestamp as a string, suitable
// for the X-Vulos-Request-Timestamp header.
func NowTimestamp() string {
	return strconv.FormatInt(time.Now().Unix(), 10)
}
