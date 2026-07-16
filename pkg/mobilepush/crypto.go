package mobilepush

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
)

// randReader returns a crypto/rand reader.  Wrapped so tests can replace it.
var randReaderOverride io.Reader

func randReader() io.Reader {
	if randReaderOverride != nil {
		return randReaderOverride
	}
	return rand.Reader
}

// base64URLEncode encodes b using base64url (no padding), as required for JWT.
func base64URLEncode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// sha256Sum computes a SHA-256 digest.
func sha256Sum(data []byte) [32]byte {
	return sha256.Sum256(data)
}
