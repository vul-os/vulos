// Package turn mints short-lived HMAC TURN credentials for use with an
// external coturn server (TURN-REST-API format).
//
// Credential format (compatible with coturn --use-auth-secret):
//
//	username = "<expiry_unix>:<ulid>"
//	password = base64(HMAC-SHA256(secret, username))
//
// Reference: https://github.com/coturn/coturn/wiki/turnserver#turn-rest-api
package turn

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"
)

const defaultTTL = 86400 * time.Second // 24 hours

// MintCredentials returns a coturn TURN-REST-API credential pair for the
// given ULID, signed with secret and valid for ttl.
//
//   - username = "<expiry_unix>:<ulid>"   (expiry = now + ttl, Unix seconds)
//   - password = base64(HMAC-SHA256(secret, username))
//   - ttlSeconds = number of seconds the credential is valid
//
// ttl == 0 defaults to 86400 s (24 h).
func MintCredentials(secret []byte, ulid string, ttl time.Duration) (username, password string, ttlSeconds int64) {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	expiry := time.Now().Unix() + int64(ttl.Seconds())
	username = fmt.Sprintf("%d:%s", expiry, ulid)

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(username))
	password = base64.StdEncoding.EncodeToString(mac.Sum(nil))

	ttlSeconds = int64(ttl.Seconds())
	return username, password, ttlSeconds
}
