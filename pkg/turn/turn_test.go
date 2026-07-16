package turn_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/turn"
)

func TestMintCredentials_Format(t *testing.T) {
	secret := []byte("super-secret-turn-key")
	ulid := "01HXYZ0000000000000000TEST"
	ttl := 3600 * time.Second

	before := time.Now().Unix()
	username, password, ttlSeconds := turn.MintCredentials(secret, ulid, ttl)
	after := time.Now().Unix()

	// username must be "<expiry>:<ulid>"
	parts := strings.SplitN(username, ":", 2)
	if len(parts) != 2 {
		t.Fatalf("username %q: expected <ts>:<ulid> format", username)
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		t.Fatalf("username expiry not an integer: %v", err)
	}
	if parts[1] != ulid {
		t.Errorf("username ulid = %q, want %q", parts[1], ulid)
	}

	// expiry should be roughly now + ttl
	wantExpiryMin := before + int64(ttl.Seconds())
	wantExpiryMax := after + int64(ttl.Seconds())
	if expiry < wantExpiryMin || expiry > wantExpiryMax {
		t.Errorf("expiry %d out of expected range [%d, %d]", expiry, wantExpiryMin, wantExpiryMax)
	}

	// password must be base64(HMAC-SHA256(secret, username))
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(username))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if password != want {
		t.Errorf("password = %q, want %q", password, want)
	}

	// ttlSeconds must match requested ttl
	if ttlSeconds != int64(ttl.Seconds()) {
		t.Errorf("ttlSeconds = %d, want %d", ttlSeconds, int64(ttl.Seconds()))
	}
}

func TestMintCredentials_DefaultTTL(t *testing.T) {
	secret := []byte("key")
	_, _, ttlSeconds := turn.MintCredentials(secret, "ULID001", 0)
	if ttlSeconds != 86400 {
		t.Errorf("default ttlSeconds = %d, want 86400", ttlSeconds)
	}
}

func TestMintCredentials_DifferentSecrets(t *testing.T) {
	ulid := "TESTULID00000000000000001"
	ttl := 3600 * time.Second
	_, pw1, _ := turn.MintCredentials([]byte("secret-a"), ulid, ttl)
	_, pw2, _ := turn.MintCredentials([]byte("secret-b"), ulid, ttl)
	if pw1 == pw2 {
		t.Error("different secrets should produce different passwords")
	}
}

func TestMintCredentials_CoturnFormat(t *testing.T) {
	// Verify the username format is exactly "<expiry_unix>:<ulid>" with no extra colons
	// in the timestamp portion (coturn splits on first colon).
	secret := []byte("coturn-secret")
	ulid := "01ABCDEFGHJKMNPQRSTVWXYZ01"
	username, _, _ := turn.MintCredentials(secret, ulid, 86400*time.Second)

	colonIdx := strings.Index(username, ":")
	if colonIdx < 1 {
		t.Fatalf("username %q missing colon separator", username)
	}
	ts := username[:colonIdx]
	tail := username[colonIdx+1:]

	// ts must be a valid positive integer (Unix timestamp)
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || n <= 0 {
		t.Errorf("timestamp portion %q is not a positive integer", ts)
	}
	// tail must exactly equal the ulid
	if tail != ulid {
		t.Errorf("tail = %q, want %q", tail, ulid)
	}
	// Ensure the username prints in the format coturn expects
	expected := fmt.Sprintf("%d:%s", n, ulid)
	if username != expected {
		t.Errorf("username = %q, want %q", username, expected)
	}
}
