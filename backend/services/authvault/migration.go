// Package authvault — migration.go
//
// Implements Google Authenticator import/export via the otpauth-migration://
// URI scheme.  The payload is a base64-encoded protobuf without any external
// dependency: we hand-decode the wire format (varint + length-delimited fields)
// according to the documented MigrationPayload schema.
//
// Routes (registered via RegisterMigrationHandlers — does NOT touch handlers.go):
//
//	POST /api/auth/totp/import   – accept otpauth-migration:// URI, add all accounts
//	POST /api/auth/totp/export   – produce an encrypted export blob
package authvault

//lint:file-ignore ST1005 These errors end in a URI template
// (otpauth-migration://offline?data=...), not a sentence period -- the trailing
// ellipsis is part of the example the operator has to type.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// ─── Hostile-input bounds ────────────────────────────────────────────────────
//
// The import side parses an attacker-supplyable protobuf (the user pastes a QR
// payload they scanned from *somewhere*). Every limit below closes a way to
// exhaust the box:
//
//   - maxTOTPBody:          unbounded json.Decoder on r.Body is an OOM primitive.
//   - maxMigrationAccounts: repeated field 1 lets one URI declare millions of
//     accounts; each one is a keychain re-encrypt.
//   - minExportPassphrase:  the export blob is every 2FA seed the user has.
const (
	maxTOTPBody          = 4 << 20 // 4 MiB request body
	maxMigrationAccounts = 1000    // accounts per otpauth-migration:// payload
	minExportPassphrase  = 8
)

// ─── RegisterMigrationHandlers ───────────────────────────────────────────────

// RegisterMigrationHandlers wires the 2 migration endpoints into mux.
//
// AUTHORISATION. Mounted on the SAME mux as the rest of the TOTP surface, so
// both routes sit behind auth.Handler.Middleware: it deletes any client-supplied
// X-User-ID and re-sets it from the validated session, 401s any non-public path
// without one, and applies the CSRF content-type/Origin check to cookie-authed
// POSTs. The store these handlers open is keyed by that SERVER-DERIVED user id —
// no account id is read from the request, so no caller can address another
// user's seeds.
//
// /export additionally requires an account-password STEP-UP (see handleExport).
func RegisterMigrationHandlers(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("POST /api/auth/totp/import", h.handleImport)
	mux.HandleFunc("POST /api/auth/totp/export", h.handleExport)
}

// noStoreAV marks a response as never-cacheable. A TOTP export body is every 2FA
// seed the user has; it must not be written to any cache.
func noStoreAV(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}

// ─── Step-up gate ────────────────────────────────────────────────────────────

// Reauthenticator verifies a user's PRIMARY account password (the OS login
// password), given the server-derived user id. Wired in main.go from the auth
// store; see Handler.SetReauthenticator.
type Reauthenticator func(userID, password string) bool

// stepUpGate throttles failed step-up attempts — both to stop an online
// brute-force of the account password and to bound the bcrypt work one caller
// can force. Fails closed.
type stepUpGate struct {
	mu    sync.Mutex
	fails map[string]*stepUpState
}

type stepUpState struct {
	count int
	until time.Time
}

const (
	stepUpMaxFailures = 5
	stepUpLockout     = 15 * time.Minute
)

func (g *stepUpGate) locked(userID string) (bool, time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.fails[userID]
	if !ok {
		return false, 0
	}
	if time.Now().After(st.until) {
		delete(g.fails, userID)
		return false, 0
	}
	if st.count >= stepUpMaxFailures {
		return true, time.Until(st.until)
	}
	return false, 0
}

func (g *stepUpGate) fail(userID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fails == nil {
		g.fails = make(map[string]*stepUpState)
	}
	st, ok := g.fails[userID]
	if !ok || time.Now().After(st.until) {
		st = &stepUpState{}
		g.fails[userID] = st
	}
	st.count++
	st.until = time.Now().Add(stepUpLockout)
}

func (g *stepUpGate) success(userID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.fails, userID)
}

// ─── Proto hand-decoder ──────────────────────────────────────────────────────

// protoReader is a minimal wire-format decoder for the subset of protobuf
// used by MigrationPayload / OtpParameters.
type protoReader struct {
	buf []byte
	pos int
}

func newProtoReader(data []byte) *protoReader { return &protoReader{buf: data} }

func (p *protoReader) done() bool { return p.pos >= len(p.buf) }

// readVarint decodes a base-128 varint and returns its uint64 value.
func (p *protoReader) readVarint() (uint64, error) {
	var v uint64
	var shift uint
	for {
		if p.pos >= len(p.buf) {
			return 0, fmt.Errorf("authvault/proto: varint truncated")
		}
		b := p.buf[p.pos]
		p.pos++
		v |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return v, nil
		}
		shift += 7
		if shift > 63 {
			return 0, fmt.Errorf("authvault/proto: varint overflow")
		}
	}
}

// readBytes reads a length-delimited bytes field (wire type 2).
//
// SECURITY: the length is an attacker-controlled varint, i.e. any uint64. The
// bounds check MUST be done in uint64 against the bytes actually remaining.
// Doing it as `p.pos + int(n) > len(p.buf)` is a classic integer-overflow hole:
// for n = 2^63 the int conversion goes NEGATIVE, the comparison passes, and the
// following make([]byte, n) either panics or tries to allocate exabytes. A
// 40-byte otpauth-migration:// URI is then enough to take the box down.
func (p *protoReader) readBytes() ([]byte, error) {
	n, err := p.readVarint()
	if err != nil {
		return nil, err
	}
	remaining := uint64(len(p.buf) - p.pos)
	if n > remaining {
		return nil, fmt.Errorf("authvault/proto: LEN field truncated (need %d, have %d)", n, remaining)
	}
	out := make([]byte, n)
	copy(out, p.buf[p.pos:p.pos+int(n)])
	p.pos += int(n)
	return out, nil
}

// nextTag reads the next (fieldNumber, wireType) pair from the stream.
// wireType: 0=varint, 1=64-bit, 2=length-delimited, 5=32-bit.
func (p *protoReader) nextTag() (field uint64, wireType uint64, err error) {
	tag, err := p.readVarint()
	if err != nil {
		return 0, 0, err
	}
	return tag >> 3, tag & 0x7, nil
}

// skipField discards one field given its wire type.
func (p *protoReader) skipField(wireType uint64) error {
	switch wireType {
	case 0: // varint
		_, err := p.readVarint()
		return err
	case 1: // 64-bit
		if p.pos+8 > len(p.buf) {
			return fmt.Errorf("authvault/proto: 64-bit field truncated")
		}
		p.pos += 8
	case 2: // length-delimited
		_, err := p.readBytes()
		return err
	case 5: // 32-bit
		if p.pos+4 > len(p.buf) {
			return fmt.Errorf("authvault/proto: 32-bit field truncated")
		}
		p.pos += 4
	default:
		return fmt.Errorf("authvault/proto: unknown wire type %d", wireType)
	}
	return nil
}

// ─── Migration protobuf structs ──────────────────────────────────────────────

// Algorithm matches the MigrationPayload.Algorithm enum.
type pbAlgorithm int32

const (
	pbAlgorithmUnspecified pbAlgorithm = 0
	pbAlgorithmSHA1        pbAlgorithm = 1
	pbAlgorithmSHA256      pbAlgorithm = 2
	pbAlgorithmSHA512      pbAlgorithm = 3
	pbAlgorithmMD5         pbAlgorithm = 4
)

// DigitCount matches the MigrationPayload.DigitCount enum.
type pbDigits int32

const (
	pbDigitsUnspecified pbDigits = 0
	pbDigitsSix         pbDigits = 1
	pbDigitsEight       pbDigits = 2
)

// OtpType matches the MigrationPayload.OtpType enum.
type pbOtpType int32

const (
	pbOtpTypeUnspecified pbOtpType = 0
	pbOtpTypeHOTP        pbOtpType = 1
	pbOtpTypeTOTP        pbOtpType = 2
)

// otpParameters mirrors MigrationPayload.OtpParameters (field 1).
//
//	field 1 → secret   (bytes)
//	field 2 → name     (string)
//	field 3 → issuer   (string)
//	field 4 → algorithm (enum/varint)
//	field 5 → digits    (enum/varint)
//	field 6 → type      (enum/varint)
//	field 7 → counter   (int64/varint)  — HOTP only
//	field 8 → account_id (string)       — unused in GA but present in spec
type otpParameters struct {
	Secret    []byte
	Name      string
	Issuer    string
	Algorithm pbAlgorithm
	Digits    pbDigits
	Type      pbOtpType
}

// migrationPayload mirrors the top-level MigrationPayload message.
//
//	field 1 → otp_parameters (repeated, length-delimited)
//	field 2 → version        (int32/varint)
//	field 3 → batch_size     (int32/varint)
//	field 4 → batch_index    (int32/varint)
//	field 5 → batch_id       (int32/varint)
type migrationPayload struct {
	OtpParameters []otpParameters
	Version       int32
	BatchSize     int32
	BatchIndex    int32
	BatchID       int32
}

// ─── Decode helpers ──────────────────────────────────────────────────────────

// decodeOtpParameters parses one OtpParameters submessage from raw bytes.
func decodeOtpParameters(data []byte) (otpParameters, error) {
	r := newProtoReader(data)
	var p otpParameters
	for !r.done() {
		field, wt, err := r.nextTag()
		if err != nil {
			return p, err
		}
		switch field {
		case 1: // secret (bytes)
			if wt != 2 {
				return p, fmt.Errorf("authvault/proto: field 1 wrong wire type %d", wt)
			}
			p.Secret, err = r.readBytes()
		case 2: // name (string)
			if wt != 2 {
				return p, fmt.Errorf("authvault/proto: field 2 wrong wire type %d", wt)
			}
			var b []byte
			b, err = r.readBytes()
			p.Name = string(b)
		case 3: // issuer (string)
			if wt != 2 {
				return p, fmt.Errorf("authvault/proto: field 3 wrong wire type %d", wt)
			}
			var b []byte
			b, err = r.readBytes()
			p.Issuer = string(b)
		case 4: // algorithm (enum)
			if wt != 0 {
				return p, fmt.Errorf("authvault/proto: field 4 wrong wire type %d", wt)
			}
			var v uint64
			v, err = r.readVarint()
			p.Algorithm = pbAlgorithm(v)
		case 5: // digits (enum)
			if wt != 0 {
				return p, fmt.Errorf("authvault/proto: field 5 wrong wire type %d", wt)
			}
			var v uint64
			v, err = r.readVarint()
			p.Digits = pbDigits(v)
		case 6: // type (enum)
			if wt != 0 {
				return p, fmt.Errorf("authvault/proto: field 6 wrong wire type %d", wt)
			}
			var v uint64
			v, err = r.readVarint()
			p.Type = pbOtpType(v)
		default:
			err = r.skipField(wt)
		}
		if err != nil {
			return p, err
		}
	}
	return p, nil
}

// decodeMigrationPayload parses a raw MigrationPayload wire-format buffer.
func decodeMigrationPayload(data []byte) (*migrationPayload, error) {
	r := newProtoReader(data)
	var mp migrationPayload
	for !r.done() {
		field, wt, err := r.nextTag()
		if err != nil {
			return nil, err
		}
		switch field {
		case 1: // otp_parameters (repeated, length-delimited)
			if wt != 2 {
				return nil, fmt.Errorf("authvault/proto: field 1 wrong wire type %d", wt)
			}
			// Bound the repeat count. Each account costs a keychain re-encrypt on
			// import; an unbounded repeated field is a cheap way to make one
			// request do unbounded work.
			if len(mp.OtpParameters) >= maxMigrationAccounts {
				return nil, fmt.Errorf("authvault/proto: too many accounts (limit %d)", maxMigrationAccounts)
			}
			raw, err := r.readBytes()
			if err != nil {
				return nil, err
			}
			otp, err := decodeOtpParameters(raw)
			if err != nil {
				return nil, fmt.Errorf("authvault/proto: otp_parameters: %w", err)
			}
			mp.OtpParameters = append(mp.OtpParameters, otp)
		case 2: // version
			if wt != 0 {
				return nil, fmt.Errorf("authvault/proto: field 2 wrong wire type %d", wt)
			}
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			mp.Version = int32(v)
		case 3: // batch_size
			if wt != 0 {
				return nil, fmt.Errorf("authvault/proto: field 3 wrong wire type %d", wt)
			}
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			mp.BatchSize = int32(v)
		case 4: // batch_index
			if wt != 0 {
				return nil, fmt.Errorf("authvault/proto: field 4 wrong wire type %d", wt)
			}
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			mp.BatchIndex = int32(v)
		case 5: // batch_id
			if wt != 0 {
				return nil, fmt.Errorf("authvault/proto: field 5 wrong wire type %d", wt)
			}
			v, err := r.readVarint()
			if err != nil {
				return nil, err
			}
			mp.BatchID = int32(v)
		default:
			if err := r.skipField(wt); err != nil {
				return nil, err
			}
		}
	}
	return &mp, nil
}

// ─── URI parser ──────────────────────────────────────────────────────────────

// ParseMigrationURI decodes an otpauth-migration:// URI and returns all
// contained OTP parameters.
func ParseMigrationURI(migrationURI string) (*migrationPayload, error) {
	u, err := url.Parse(migrationURI)
	if err != nil {
		return nil, fmt.Errorf("authvault/migration: invalid URI: %w", err)
	}
	if u.Scheme != "otpauth-migration" || u.Host != "offline" {
		return nil, fmt.Errorf("authvault/migration: URI must be otpauth-migration://offline?data=...")
	}
	dataParam := u.Query().Get("data")
	if dataParam == "" {
		return nil, fmt.Errorf("authvault/migration: missing 'data' query parameter")
	}
	raw, err := base64.StdEncoding.DecodeString(dataParam)
	if err != nil {
		// Google sometimes uses standard base64 with '+' and '/' URL-encoded.
		// Try URL-safe as fallback.
		raw, err = base64.URLEncoding.DecodeString(dataParam)
		if err != nil {
			return nil, fmt.Errorf("authvault/migration: base64 decode failed: %w", err)
		}
	}
	mp, err := decodeMigrationPayload(raw)
	if err != nil {
		return nil, fmt.Errorf("authvault/migration: protobuf decode failed: %w", err)
	}
	return mp, nil
}

// ─── Conversion helpers ──────────────────────────────────────────────────────

// pbAlgorithmString converts a protobuf algorithm enum to the string used by Store.
func pbAlgorithmString(a pbAlgorithm) string {
	switch a {
	case pbAlgorithmSHA256:
		return "SHA256"
	case pbAlgorithmSHA512:
		return "SHA512"
	default:
		return "SHA1"
	}
}

// pbDigitsInt converts a protobuf digits enum to an integer.
func pbDigitsInt(d pbDigits) int {
	if d == pbDigitsEight {
		return 8
	}
	return 6
}

// importIntoStore converts decoded OtpParameters into Store entries.
// Returns the list of imported accounts and any per-entry errors.
func importIntoStore(store *Store, params []otpParameters) ([]*Account, []error) {
	var accounts []*Account
	var errs []error
	for _, p := range params {
		// Only TOTP is supported; skip HOTP silently.
		if p.Type == pbOtpTypeHOTP {
			errs = append(errs, fmt.Errorf("authvault/migration: skipping HOTP account %q (not supported)", p.Name))
			continue
		}
		// Secret is raw bytes; encode as base32 for the store.
		secret := base64.StdEncoding.EncodeToString(p.Secret)
		// Re-encode as base32 (the store expects base32, not base64).
		secretBytes, err := base64.StdEncoding.DecodeString(secret)
		if err != nil {
			errs = append(errs, fmt.Errorf("authvault/migration: account %q: bad secret: %w", p.Name, err))
			continue
		}
		secret32 := base32Encode(secretBytes)

		service := p.Issuer
		if service == "" {
			service = p.Name
		}
		issuer := p.Issuer
		accountID := p.Name
		// If name has "issuer:account" format, split it.
		if parts := strings.SplitN(p.Name, ":", 2); len(parts) == 2 {
			if issuer == "" {
				issuer = strings.TrimSpace(parts[0])
			}
			accountID = strings.TrimSpace(parts[1])
			if service == "" || service == p.Name {
				service = issuer
			}
		}

		acc := &Account{
			ID:        generateID(),
			Issuer:    issuer,
			Service:   service,
			AccountID: accountID,
			Algorithm: pbAlgorithmString(p.Algorithm),
			Digits:    pbDigitsInt(p.Digits),
			Period:    30,
		}
		imported, err := store.addAccount(acc, secret32)
		if err != nil {
			errs = append(errs, fmt.Errorf("authvault/migration: account %q: %w", p.Name, err))
			continue
		}
		accounts = append(accounts, imported)
	}
	return accounts, errs
}

// base32Encode encodes raw bytes as an unpadded base32 string (uppercase).
// We implement this directly to avoid pulling in a new import; the standard
// library encoding/base32 is already used in totp.go.
func base32Encode(data []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	var sb strings.Builder
	for i := 0; i < len(data); i += 5 {
		// Collect up to 5 bytes.
		chunk := make([]byte, 5)
		n := copy(chunk, data[i:])
		// Expand into 8 base32 characters.
		chars := [8]byte{
			alphabet[chunk[0]>>3],
			alphabet[(chunk[0]&0x07)<<2|(chunk[1]>>6)],
			alphabet[(chunk[1]&0x3f)>>1],
			alphabet[(chunk[1]&0x01)<<4|(chunk[2]>>4)],
			alphabet[(chunk[2]&0x0f)<<1|(chunk[3]>>7)],
			alphabet[(chunk[3]&0x7f)>>2],
			alphabet[(chunk[3]&0x03)<<3|(chunk[4]>>5)],
			alphabet[chunk[4]&0x1f],
		}
		// Only write as many characters as actual data (unpadded).
		switch n {
		case 1:
			sb.WriteByte(chars[0])
			sb.WriteByte(chars[1])
		case 2:
			sb.WriteByte(chars[0])
			sb.WriteByte(chars[1])
			sb.WriteByte(chars[2])
			sb.WriteByte(chars[3])
		case 3:
			sb.WriteByte(chars[0])
			sb.WriteByte(chars[1])
			sb.WriteByte(chars[2])
			sb.WriteByte(chars[3])
			sb.WriteByte(chars[4])
		case 4:
			for j := 0; j < 7; j++ {
				sb.WriteByte(chars[j])
			}
		case 5:
			for _, c := range chars {
				sb.WriteByte(c)
			}
		}
	}
	return sb.String()
}

// ─── Export blob ─────────────────────────────────────────────────────────────

// exportEntry is one entry in the export blob.
type exportEntry struct {
	ID        string `json:"id"`
	Service   string `json:"service"`
	Issuer    string `json:"issuer"`
	AccountID string `json:"account"`
	Algorithm string `json:"algorithm"`
	Digits    int    `json:"digits"`
	Period    int    `json:"period"`
	Secret    string `json:"secret"` // base32, included in the encrypted blob
}

// ExportBlob is the encrypted export payload returned by POST /api/auth/totp/export.
//
// TWO VERSIONS, and the difference is the whole point of having an export:
//
//	v1 — encrypted with the store's keychainKey, which is derived from
//	     ~/.vulos/auth/totp/<user>/keyfile. That key never leaves this box, so a
//	     v1 blob is ONLY decryptable ON THIS BOX. As a backup it is worthless:
//	     the scenario an export exists for is "the box is gone", and in that
//	     scenario the key is gone too. v1 is still accepted on IMPORT (a
//	     same-box restore is legitimate) but is no longer produced.
//
//	v2 — encrypted with a key derived (Argon2id) from a passphrase the USER
//	     supplies. Survives the box. This is what /export now emits.
type ExportBlob struct {
	// Nonce is the 12-byte GCM nonce, base64-encoded.
	Nonce string `json:"nonce"`
	// Ciphertext is the AES-256-GCM encrypted JSON of []exportEntry, base64-encoded.
	Ciphertext string `json:"ciphertext"`
	// Version is 1 (box keychain key) or 2 (user passphrase).
	Version int `json:"version"`
	// Count is the number of accounts in the blob.
	Count int `json:"count"`
	// Salt is the base64 Argon2id salt. v2 only.
	Salt string `json:"salt,omitempty"`
}

// Export blob versions.
const (
	exportVersionBoxKey     = 1 // legacy: AES key = store keychain key (box-bound)
	exportVersionPassphrase = 2 // portable: AES key = Argon2id(passphrase, salt)
)

// Argon2id parameters for passphrase-derived export keys. Matched to credvault's
// vault KDF (OWASP minimums): 64 MiB, t=3, p=4, 32-byte key.
const (
	expArgonMemory      uint32 = 64 * 1024
	expArgonTime        uint32 = 3
	expArgonParallelism uint8  = 4
	expArgonKeyLen      uint32 = 32
	expSaltLen                 = 32
)

func deriveExportKey(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, expArgonTime, expArgonMemory, expArgonParallelism, expArgonKeyLen)
}

// collectExportEntries snapshots every account + secret in the store.
func collectExportEntries(store *Store) []exportEntry {
	store.mu.RLock()
	defer store.mu.RUnlock()
	entries := make([]exportEntry, 0, len(store.accounts))
	for id, acc := range store.accounts {
		secret, ok := store.secrets[id]
		if !ok {
			continue
		}
		entries = append(entries, exportEntry{
			ID:        id,
			Service:   acc.Service,
			Issuer:    acc.Issuer,
			AccountID: acc.AccountID,
			Algorithm: acc.Algorithm,
			Digits:    acc.Digits,
			Period:    acc.Period,
			Secret:    secret,
		})
	}
	return entries
}

// buildPortableExportBlob encrypts every TOTP seed under a key derived from the
// user's passphrase, producing a blob that can be restored on a DIFFERENT box.
func buildPortableExportBlob(store *Store, passphrase string) (*ExportBlob, error) {
	if len(passphrase) < minExportPassphrase {
		return nil, fmt.Errorf("authvault/export: passphrase must be at least %d characters", minExportPassphrase)
	}
	entries := collectExportEntries(store)

	plaintext, err := json.Marshal(entries)
	if err != nil {
		return nil, fmt.Errorf("authvault/export: marshal: %w", err)
	}

	salt := make([]byte, expSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("authvault/export: salt: %w", err)
	}
	key := deriveExportKey(passphrase, salt)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("authvault/export: AES: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("authvault/export: GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("authvault/export: nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	return &ExportBlob{
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
		Version:    exportVersionPassphrase,
		Count:      len(entries),
		Salt:       base64.StdEncoding.EncodeToString(salt),
	}, nil
}

// buildExportBlob serialises all accounts (including their secrets) into an
// AES-256-GCM encrypted blob using the store's keychain key.
//
// Deprecated for the HTTP surface: this produces a v1 (box-bound) blob — see
// ExportBlob. /api/auth/totp/export emits v2 via buildPortableExportBlob.
// Retained for same-box snapshot/restore.
func buildExportBlob(store *Store) (*ExportBlob, error) {
	store.mu.RLock()
	entries := make([]exportEntry, 0, len(store.accounts))
	for id, acc := range store.accounts {
		secret, ok := store.secrets[id]
		if !ok {
			continue
		}
		entries = append(entries, exportEntry{
			ID:        id,
			Service:   acc.Service,
			Issuer:    acc.Issuer,
			AccountID: acc.AccountID,
			Algorithm: acc.Algorithm,
			Digits:    acc.Digits,
			Period:    acc.Period,
			Secret:    secret,
		})
	}
	store.mu.RUnlock()

	plaintext, err := json.Marshal(entries)
	if err != nil {
		return nil, fmt.Errorf("authvault/export: marshal: %w", err)
	}

	block, err := aes.NewCipher(store.keychainKey)
	if err != nil {
		return nil, fmt.Errorf("authvault/export: AES: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("authvault/export: GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("authvault/export: nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	return &ExportBlob{
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
		Version:    1,
		Count:      len(entries),
	}, nil
}

// importExportBlob decrypts a v1 (box-key) ExportBlob and adds all entries into
// a store. Same-box restore only — see ExportBlob.
func importExportBlob(store *Store, blob *ExportBlob) ([]*Account, error) {
	accounts, _, err := importBlob(store, blob, "")
	return accounts, err
}

// importBlob decrypts an ExportBlob of either version and adds its entries.
//
// v2 needs the passphrase the user chose at export time; v1 is unwrapped with
// this box's keychain key. Returns the accounts that landed AND the per-account
// failures — a blob whose 5th seed is malformed must not report "error" while
// silently having stored the first four with no word about it.
func importBlob(store *Store, blob *ExportBlob, passphrase string) ([]*Account, []string, error) {
	nonce, err := base64.StdEncoding.DecodeString(blob.Nonce)
	if err != nil {
		return nil, nil, fmt.Errorf("authvault/import-blob: nonce decode: %w", err)
	}
	ct, err := base64.StdEncoding.DecodeString(blob.Ciphertext)
	if err != nil {
		return nil, nil, fmt.Errorf("authvault/import-blob: ciphertext decode: %w", err)
	}

	var key []byte
	switch blob.Version {
	case exportVersionPassphrase:
		if passphrase == "" {
			return nil, nil, fmt.Errorf("authvault/import-blob: passphrase required for this export")
		}
		salt, err := base64.StdEncoding.DecodeString(blob.Salt)
		if err != nil || len(salt) != expSaltLen {
			return nil, nil, fmt.Errorf("authvault/import-blob: malformed salt")
		}
		key = deriveExportKey(passphrase, salt)
	case exportVersionBoxKey, 0:
		// Legacy same-box blob: unwrap with this box's keychain key.
		key = store.keychainKey
	default:
		return nil, nil, fmt.Errorf("authvault/import-blob: unsupported export version %d", blob.Version)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("authvault/import-blob: AES: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("authvault/import-blob: GCM: %w", err)
	}
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		// AEAD failure — wrong passphrase, wrong box, or tampered blob. Do not
		// distinguish: that difference is an oracle.
		return nil, nil, fmt.Errorf("authvault/import-blob: decrypt: wrong passphrase or corrupted export")
	}

	var entries []exportEntry
	if err := json.Unmarshal(plaintext, &entries); err != nil {
		return nil, nil, fmt.Errorf("authvault/import-blob: unmarshal: %w", err)
	}
	if len(entries) > maxMigrationAccounts {
		return nil, nil, fmt.Errorf("authvault/import-blob: export contains %d accounts, limit is %d", len(entries), maxMigrationAccounts)
	}

	var imported []*Account
	var failures []string
	for _, e := range entries {
		acc := &Account{
			ID:        generateID(),
			Service:   e.Service,
			Issuer:    e.Issuer,
			AccountID: e.AccountID,
			Algorithm: e.Algorithm,
			Digits:    e.Digits,
			Period:    e.Period,
		}
		a, err := store.addAccount(acc, e.Secret)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: could not be stored", e.Service))
			continue
		}
		imported = append(imported, a)
	}
	return imported, failures, nil
}

// ─── HTTP handlers ────────────────────────────────────────────────────────────

// handleImport handles POST /api/auth/totp/import.
// Accepts either:
//
//	{ "uri": "otpauth-migration://offline?data=..." }  — Google Authenticator export
//	{ "blob": { "nonce":..., "ciphertext":..., "version":1 } } — encrypted Vulos blob
func (h *Handler) handleImport(w http.ResponseWriter, r *http.Request) {
	// SERVER-DERIVED identity: the auth middleware stripped any attacker-supplied
	// X-User-ID and re-set it from the session. No account id is read from the
	// body, so a caller cannot write into someone else's TOTP store.
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeJSONErr(w, 401, "unauthorized")
		return
	}
	noStoreAV(w)

	var req struct {
		URI        string      `json:"uri"`
		Blob       *ExportBlob `json:"blob"`
		Passphrase string      `json:"passphrase"` // for a v2 (portable) blob
	}
	// Bound the body before decoding — an unbounded decoder is an OOM primitive.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTOTPBody)).Decode(&req); err != nil {
		if strings.Contains(err.Error(), "too large") {
			writeJSONErr(w, 413, fmt.Sprintf("import too large (limit %d MiB)", maxTOTPBody>>20))
			return
		}
		writeJSONErr(w, 400, "invalid request body")
		return
	}
	if req.URI == "" && req.Blob == nil {
		writeJSONErr(w, 400, "uri or blob required")
		return
	}

	store, err := h.storeFor(userID)
	if err != nil {
		writeJSONErr(w, 500, "totp store unavailable")
		return
	}

	if req.Blob != nil {
		// Encrypted blob import (Vulos → Vulos), v1 box-key or v2 passphrase.
		accounts, failures, err := importBlob(store, req.Blob, req.Passphrase)
		if err != nil {
			writeJSONErr(w, 400, err.Error())
			return
		}
		writeJSONOK(w, map[string]any{
			"imported": len(accounts),
			"failed":   len(failures),
			"accounts": accounts,
			"warnings": failures,
		})
		return
	}

	// Google Authenticator otpauth-migration:// import.
	mp, err := ParseMigrationURI(req.URI)
	if err != nil {
		// Generic: a decoder error can quote payload bytes, and those bytes are
		// 2FA seeds.
		writeJSONErr(w, 400, "could not read migration QR payload — expected otpauth-migration://offline?data=…")
		return
	}

	accounts, errs := importIntoStore(store, mp.OtpParameters)
	// Report EVERYTHING: parsed, imported, skipped/failed. A migration that
	// silently drops 3 of 12 seeds locks the user out of 3 accounts and they
	// find out months later.
	errStrings := make([]string, 0, len(errs))
	for _, e := range errs {
		errStrings = append(errStrings, e.Error())
	}

	writeJSONOK(w, map[string]any{
		"parsed":   len(mp.OtpParameters),
		"imported": len(accounts),
		"failed":   len(errStrings),
		"accounts": accounts,
		"warnings": errStrings,
	})
}

// handleExport handles POST /api/auth/totp/export.
//
// Request body (JSON):
//
//	{
//	  "account_password": "<the user's OS login password — STEP-UP>",
//	  "passphrase":       "<passphrase that will encrypt the export file>"
//	}
//
// SECURITY. The response is every TOTP seed the user owns — a full 2FA
// compromise for every account they protect. A session cookie is not sufficient
// authorisation for that: XSS and CSRF both ride the cookie. So export demands
// the account password again, via the Reauthenticator wired in main.go, and
// FAILS CLOSED (503) if no reauthenticator was wired — an unwired gate must
// never degrade into an open one. Failed attempts are throttled.
//
// The blob is encrypted with a key derived from `passphrase`, NOT with the box's
// keychain key, so the backup is still readable after the box is gone. That is
// the entire reason an export exists.
func (h *Handler) handleExport(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeJSONErr(w, 401, "unauthorized")
		return
	}
	noStoreAV(w)

	var req struct {
		AccountPassword string `json:"account_password"`
		Passphrase      string `json:"passphrase"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSONErr(w, 400, "invalid request body")
		return
	}
	if len(req.Passphrase) < minExportPassphrase {
		writeJSONErr(w, 400, fmt.Sprintf("export passphrase must be at least %d characters", minExportPassphrase))
		return
	}

	// Step-up. Fail CLOSED when unwired.
	reauth := h.reauthenticator()
	if reauth == nil {
		writeJSONErr(w, 503, "export unavailable: re-authentication is not configured")
		return
	}
	if req.AccountPassword == "" {
		writeJSONErr(w, 401, "account password is required to export your 2FA seeds")
		return
	}
	if locked, retry := h.stepUp().locked(userID); locked {
		writeJSONErr(w, 429, fmt.Sprintf("too many failed attempts — try again in %d minute(s)", int(retry.Minutes())+1))
		return
	}
	if !reauth(userID, req.AccountPassword) {
		h.stepUp().fail(userID)
		writeJSONErr(w, 401, "wrong account password")
		return
	}
	h.stepUp().success(userID)

	store, err := h.storeFor(userID)
	if err != nil {
		writeJSONErr(w, 500, "totp store unavailable")
		return
	}

	blob, err := buildPortableExportBlob(store, req.Passphrase)
	if err != nil {
		writeJSONErr(w, 500, "export failed")
		return
	}

	writeJSONOK(w, blob)
}
