package cluster

import (
	"os"
	"strings"
	"testing"
)

// The cluster subsystem encrypts with SSE-C, which means the encryption key is
// NOT used on this box — it goes to the endpoint in a header on every PUT and
// GET. Over plain HTTP to an off-box endpoint, the key and the object body are
// both in the clear, and "encrypted at rest" is worth nothing to anyone who
// watched the key go past.
//
// VULOS_S3_USE_SSL defaults to "false", which is right for the default endpoint
// (localhost:9000) and wrong the moment VULOS_S3_ENDPOINT names another host.

func withProd(t *testing.T, on bool) {
	t.Helper()
	prev, had := os.LookupEnv("VULOS_ENV")
	if on {
		os.Setenv("VULOS_ENV", "prod")
	} else {
		os.Unsetenv("VULOS_ENV")
	}
	t.Cleanup(func() {
		if had {
			os.Setenv("VULOS_ENV", prev)
		} else {
			os.Unsetenv("VULOS_ENV")
		}
	})
}

func TestLoopbackEndpointsAreRecognised(t *testing.T) {
	// A bare host, a host:port, an IPv6 literal in brackets and a pasted URL all
	// have to resolve the same way. Splitting on ":" instead of using
	// SplitHostPort mangles "[::1]:9000" into something that is not loopback,
	// which would make the gate refuse a perfectly local MinIO in prod.
	local := []string{
		"localhost", "localhost:9000", "LOCALHOST:9000",
		"127.0.0.1", "127.0.0.1:9000", "127.53.0.1:9000",
		"[::1]", "[::1]:9000", "http://localhost:9000", "http://127.0.0.1:9000/",
	}
	for _, e := range local {
		if !isLoopbackEndpoint(e) {
			t.Errorf("isLoopbackEndpoint(%q) = false, want true", e)
		}
	}
	remote := []string{
		"s3.amazonaws.com", "s3.amazonaws.com:443", "minio.example.org:9000",
		"192.168.1.42:9000", "10.0.0.5", "[2001:db8::1]:9000", "",
		// A host that merely CONTAINS the word is not loopback.
		"localhost.evil.example.com:9000",
	}
	for _, e := range remote {
		if isLoopbackEndpoint(e) {
			t.Errorf("isLoopbackEndpoint(%q) = true, want false", e)
		}
	}
}

func TestProdRefusesPlaintextToRemoteEndpoint(t *testing.T) {
	withProd(t, true)
	err := guardTransport(S3Config{Endpoint: "minio.example.org:9000", UseSSL: false})
	if err == nil {
		t.Fatal("prod accepted an off-box endpoint over plain HTTP — the SSE-C key would " +
			"cross the network in the clear")
	}
	if !strings.Contains(err.Error(), "VULOS_S3_USE_SSL") {
		t.Errorf("the refusal must name the setting that fixes it, got: %v", err)
	}
}

func TestProdAllowsTLSAndLoopback(t *testing.T) {
	withProd(t, true)
	// TLS to a remote endpoint is the arrangement this is steering people to.
	if err := guardTransport(S3Config{Endpoint: "minio.example.org:9000", UseSSL: true}); err != nil {
		t.Errorf("prod refused a TLS endpoint: %v", err)
	}
	// Plain HTTP to a MinIO on this machine is the shipped default and must keep
	// working — a gate that breaks the default configuration gets switched off.
	if err := guardTransport(S3Config{Endpoint: "localhost:9000", UseSSL: false}); err != nil {
		t.Errorf("prod refused the default on-box endpoint: %v", err)
	}
}

func TestNonProdWarnsButProceeds(t *testing.T) {
	withProd(t, false)
	if err := guardTransport(S3Config{Endpoint: "minio.example.org:9000", UseSSL: false}); err != nil {
		t.Errorf("development refused instead of warning: %v", err)
	}
}

// Both constructors are separate doors into the same transport, so both are
// checked. NewClient reaches the network before it could return this error, so
// the constructor-level assertion is made on NewClientWithKey, which does not.
func TestNewClientWithKeyRefusesPlaintextRemoteInProd(t *testing.T) {
	withProd(t, true)
	key := make([]byte, 32)
	_, err := NewClientWithKey(S3Config{
		Endpoint: "minio.example.org:9000", Bucket: "b",
		AccessKey: "a", SecretKey: "s", UseSSL: false,
	}, key)
	if err == nil {
		t.Fatal("NewClientWithKey built a client that sends SSE-C keys in the clear in prod")
	}
	// And the same call is fine once TLS is on.
	if _, err := NewClientWithKey(S3Config{
		Endpoint: "minio.example.org:9000", Bucket: "b",
		AccessKey: "a", SecretKey: "s", UseSSL: true,
	}, key); err != nil {
		t.Errorf("NewClientWithKey refused a TLS endpoint: %v", err)
	}
}
