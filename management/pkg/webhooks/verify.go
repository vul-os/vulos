// verify.go — HMAC-SHA256 signature helper for Vulos webhook subscribers.
//
// Vulos signs each webhook delivery with:
//
//	X-Vulos-Signature: t=<unix-ts>,v1=<hex-hmac-sha256>
//
// The signed payload is: "<unix-ts>.<json-body>"
//
// This file is intentionally standalone so subscribers can copy it directly.
package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SignatureMaxAge is the maximum age of a webhook signature before it is
// rejected as a potential replay. Callers may override this for testing.
var SignatureMaxAge = 5 * time.Minute

// ErrSignatureInvalid is returned when the HMAC signature does not match.
var ErrSignatureInvalid = errors.New("webhooks: invalid signature")

// ErrSignatureExpired is returned when the webhook timestamp is too old.
var ErrSignatureExpired = errors.New("webhooks: signature timestamp expired")

// BuildSignature constructs the X-Vulos-Signature header value for a given
// secret, unix timestamp, and body. Used internally by the dispatcher.
func BuildSignature(secret []byte, ts int64, body []byte) string {
	mac := computeHMAC(secret, ts, body)
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(mac))
}

// VerifySignature verifies an X-Vulos-Signature header.
// Returns nil if the signature is valid and not expired.
//
// Example (subscriber code):
//
//	err := webhooks.VerifySignature(rawBody, r.Header.Get("X-Vulos-Signature"), []byte(mySecret))
func VerifySignature(body []byte, sigHeader string, secret []byte) error {
	ts, v1, err := parseSigHeader(sigHeader)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSignatureInvalid, err)
	}

	// Replay protection: reject timestamps older than SignatureMaxAge.
	age := time.Since(time.Unix(ts, 0))
	if age > SignatureMaxAge || age < -SignatureMaxAge {
		return ErrSignatureExpired
	}

	expected := computeHMAC(secret, ts, body)
	got, err := hex.DecodeString(v1)
	if err != nil {
		return ErrSignatureInvalid
	}
	if !hmac.Equal(expected, got) {
		return ErrSignatureInvalid
	}
	return nil
}

// computeHMAC returns HMAC-SHA256 of "<ts>.<body>" using secret.
func computeHMAC(secret []byte, ts int64, body []byte) []byte {
	payload := fmt.Sprintf("%d.", ts)
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(payload))
	h.Write(body)
	return h.Sum(nil)
}

// parseSigHeader parses "t=<ts>,v1=<hex>" into (ts, hex, nil).
func parseSigHeader(header string) (int64, string, error) {
	var ts int64
	var v1 string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			var err error
			ts, err = strconv.ParseInt(kv[1], 10, 64)
			if err != nil {
				return 0, "", fmt.Errorf("invalid timestamp: %w", err)
			}
		case "v1":
			v1 = kv[1]
		}
	}
	if ts == 0 || v1 == "" {
		return 0, "", errors.New("missing t or v1 fields")
	}
	return ts, v1, nil
}
