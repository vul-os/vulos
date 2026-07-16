package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters — updated to 2026 OWASP guidance (t=2, m=64MiB, p=4).
// 2026 OWASP recommendation: argon2id with t≥2, m≥64MiB.
// Previous value was t=1 (met the 2023 floor but not the 2026 recommendation).
// NOTE: changing these parameters invalidates no existing hashes — VerifyPassword
// uses the parameters from the stored PHC string, not these constants, so all
// existing passwords remain verifiable. New hashes will use the updated cost.
const (
	argonTime    = 2
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// argonTestTime / argonTestMemory / argonTestThreads are intentionally weak
// parameters used ONLY when ARGON2_TEST_FAST=1 is set in the environment.
// They MUST never appear in production code paths: HashPassword guards them
// with an env check that is evaluated once per process startup.
//
// Production security is NOT affected: these constants are only consulted
// when the env flag is set, and VerifyPassword always reads parameters from
// the stored PHC string rather than these constants.
const (
	argonTestTime    uint32 = 1
	argonTestMemory  uint32 = 64 // 64 KiB — just enough to exercise the code path
	argonTestThreads uint8  = 1
)

// ArgonProductionParams returns the production Argon2id cost parameters as
// named values.  Use this in security/compliance tests to verify that the
// production constants meet policy minimums WITHOUT relying on HashPassword
// (which may return cheaper test params inside a test binary).
type ArgonProductionParams struct {
	Time    uint32
	Memory  uint32 // KiB
	Threads uint8
	KeyLen  uint32
}

// ProductionArgonParams returns the compile-time production Argon2id constants.
// These values never change at runtime regardless of environment variables or
// whether the binary is a test binary.
func ProductionArgonParams() ArgonProductionParams {
	return ArgonProductionParams{
		Time:    argonTime,
		Memory:  argonMemory,
		Threads: argonThreads,
		KeyLen:  argonKeyLen,
	}
}

// argonCost returns the cost parameters to use for a new hash.
//
// SECURITY: the cheap test parameters are ONLY returned when BOTH conditions
// are true:
//  1. The process is a test binary (testing.Testing() == true, Go 1.21+).
//  2. ARGON2_TEST_FAST=1 is set in the environment.
//
// A production server binary always satisfies testing.Testing() == false, so
// this function ignores ARGON2_TEST_FAST entirely in production — even if an
// operator accidentally sets it.  The test-speedup remains available for test
// binaries that set the env flag.
//
// VerifyPassword always reads cost from the stored PHC string, so hashes
// produced with either parameter set remain verifiable.
func argonCost() (time, memory uint32, threads uint8) {
	if testing.Testing() && os.Getenv("ARGON2_TEST_FAST") == "1" {
		return argonTestTime, argonTestMemory, argonTestThreads
	}
	return argonTime, argonMemory, argonThreads
}

// ErrHashMismatch is returned by VerifyPassword when the password does not
// match the stored hash.
var ErrHashMismatch = errors.New("auth: password mismatch")

// ErrInvalidHash is returned when the stored hash string is malformed.
var ErrInvalidHash = errors.New("auth: invalid argon2id hash format")

// HashPassword hashes password using argon2id with a fresh random salt and
// returns the PHC-like encoded string:
//
//	$argon2id$v=19$m=65536,t=2,p=4$<salt-b64>$<hash-b64>
//
// Production parameters (t=2, m=64MiB, p=4) are used unless ARGON2_TEST_FAST=1
// is set, in which case deliberately cheap test parameters are used to prevent
// CPU-heavy Argon2 calls from starving parallel test goroutines.
// ARGON2_TEST_FAST must never be set in production or staging environments.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generate salt: %w", err)
	}

	time, memory, threads := argonCost()
	hash := argon2.IDKey([]byte(password), salt, time, memory, threads, argonKeyLen)

	saltB64 := base64.RawStdEncoding.EncodeToString(salt)
	hashB64 := base64.RawStdEncoding.EncodeToString(hash)

	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memory, time, threads, saltB64, hashB64)
	return encoded, nil
}

// argon2Params holds the parameters extracted from a stored PHC string.
type argon2Params struct {
	time    uint32
	memory  uint32
	threads uint8
	keyLen  uint32
}

// VerifyPassword checks password against the PHC-encoded hash.
// Uses constant-time comparison to prevent timing attacks.
// Uses the parameters from the stored hash string so that hashes produced with
// old cost parameters (e.g. t=1) remain verifiable after a cost upgrade.
// Returns ErrHashMismatch on mismatch, ErrInvalidHash on parse errors.
func VerifyPassword(encoded, password string) error {
	salt, expectedHash, params, err := parseArgon2Encoded(encoded)
	if err != nil {
		return err
	}

	actualHash := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, params.keyLen)

	// constant-time compare — never reveal which byte differed.
	if subtle.ConstantTimeCompare(actualHash, expectedHash) != 1 {
		return ErrHashMismatch
	}
	return nil
}

// parseArgon2Encoded decodes an encoded argon2id hash string and returns the
// salt, expected hash bytes, and the cost parameters embedded in the string.
// Format: $argon2id$v=19$m=65536,t=2,p=4$<salt-b64>$<hash-b64>
func parseArgon2Encoded(encoded string) (salt, hash []byte, params argon2Params, err error) {
	// Expect exactly 6 $ delimited segments (leading $ gives empty first).
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 {
		return nil, nil, argon2Params{}, ErrInvalidHash
	}
	// parts[0] == "" (leading $), parts[1] == "argon2id"
	if parts[1] != "argon2id" {
		return nil, nil, argon2Params{}, ErrInvalidHash
	}

	// Parse parameters from parts[3]: "m=65536,t=2,p=4"
	p, perr := parseArgon2ParamString(parts[3])
	if perr != nil {
		return nil, nil, argon2Params{}, ErrInvalidHash
	}

	salt, err = base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, nil, argon2Params{}, ErrInvalidHash
	}
	hash, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return nil, nil, argon2Params{}, ErrInvalidHash
	}
	return salt, hash, p, nil
}

// parseArgon2ParamString parses "m=65536,t=2,p=4" into argon2Params.
// Unknown fields are silently ignored; missing fields default to the
// current module constants so legacy hashes that omit a field still work.
func parseArgon2ParamString(s string) (argon2Params, error) {
	p := argon2Params{
		time:    argonTime,
		memory:  argonMemory,
		threads: argonThreads,
		keyLen:  argonKeyLen,
	}
	for _, field := range strings.Split(s, ",") {
		kv := strings.SplitN(field, "=", 2)
		if len(kv) != 2 {
			continue
		}
		var v uint64
		if _, err := fmt.Sscanf(kv[1], "%d", &v); err != nil {
			return argon2Params{}, ErrInvalidHash
		}
		switch kv[0] {
		case "m":
			p.memory = uint32(v)
		case "t":
			p.time = uint32(v)
		case "p":
			p.threads = uint8(v)
		case "k":
			p.keyLen = uint32(v)
		}
	}
	return p, nil
}
