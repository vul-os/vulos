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
)

// ─── RegisterMigrationHandlers ───────────────────────────────────────────────

// RegisterMigrationHandlers wires the 2 migration endpoints into mux.
// The orchestrator calls this alongside the existing RegisterHandlers so that
// handlers.go is never modified.
func RegisterMigrationHandlers(mux *http.ServeMux, h *Handler) {
	mux.HandleFunc("POST /api/auth/totp/import", h.handleImport)
	mux.HandleFunc("POST /api/auth/totp/export", h.handleExport)
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
func (p *protoReader) readBytes() ([]byte, error) {
	n, err := p.readVarint()
	if err != nil {
		return nil, err
	}
	if p.pos+int(n) > len(p.buf) {
		return nil, fmt.Errorf("authvault/proto: LEN field truncated (need %d, have %d)", n, len(p.buf)-p.pos)
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
type ExportBlob struct {
	// Nonce is the 12-byte GCM nonce, base64-encoded.
	Nonce string `json:"nonce"`
	// Ciphertext is the AES-256-GCM encrypted JSON of []exportEntry, base64-encoded.
	Ciphertext string `json:"ciphertext"`
	// Version is always 1 for this format.
	Version int `json:"version"`
	// Count is the number of accounts in the blob.
	Count int `json:"count"`
}

// buildExportBlob serialises all accounts (including their secrets) into an
// AES-256-GCM encrypted blob using the store's keychain key.
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

// importExportBlob decrypts an ExportBlob and adds all entries into a store.
func importExportBlob(store *Store, blob *ExportBlob) ([]*Account, error) {
	nonce, err := base64.StdEncoding.DecodeString(blob.Nonce)
	if err != nil {
		return nil, fmt.Errorf("authvault/import-blob: nonce decode: %w", err)
	}
	ct, err := base64.StdEncoding.DecodeString(blob.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("authvault/import-blob: ciphertext decode: %w", err)
	}

	block, err := aes.NewCipher(store.keychainKey)
	if err != nil {
		return nil, fmt.Errorf("authvault/import-blob: AES: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("authvault/import-blob: GCM: %w", err)
	}
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("authvault/import-blob: decrypt: %w", err)
	}

	var entries []exportEntry
	if err := json.Unmarshal(plaintext, &entries); err != nil {
		return nil, fmt.Errorf("authvault/import-blob: unmarshal: %w", err)
	}

	var imported []*Account
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
			return imported, fmt.Errorf("authvault/import-blob: account %q: %w", e.Service, err)
		}
		imported = append(imported, a)
	}
	return imported, nil
}

// ─── HTTP handlers ────────────────────────────────────────────────────────────

// handleImport handles POST /api/auth/totp/import.
// Accepts either:
//
//	{ "uri": "otpauth-migration://offline?data=..." }  — Google Authenticator export
//	{ "blob": { "nonce":..., "ciphertext":..., "version":1 } } — encrypted Vula blob
func (h *Handler) handleImport(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeJSONErr(w, 401, "unauthorized")
		return
	}

	var req struct {
		URI  string      `json:"uri"`
		Blob *ExportBlob `json:"blob"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONErr(w, 400, "invalid request body")
		return
	}
	if req.URI == "" && req.Blob == nil {
		writeJSONErr(w, 400, "uri or blob required")
		return
	}

	store, err := h.storeFor(userID)
	if err != nil {
		writeJSONErr(w, 500, err.Error())
		return
	}

	if req.Blob != nil {
		// Encrypted blob import (Vula → Vula).
		accounts, err := importExportBlob(store, req.Blob)
		if err != nil {
			writeJSONErr(w, 400, err.Error())
			return
		}
		writeJSONOK(w, map[string]any{
			"imported": len(accounts),
			"accounts": accounts,
		})
		return
	}

	// Google Authenticator otpauth-migration:// import.
	mp, err := ParseMigrationURI(req.URI)
	if err != nil {
		writeJSONErr(w, 400, err.Error())
		return
	}

	accounts, errs := importIntoStore(store, mp.OtpParameters)
	var errStrings []string
	for _, e := range errs {
		errStrings = append(errStrings, e.Error())
	}

	writeJSONOK(w, map[string]any{
		"imported": len(accounts),
		"accounts": accounts,
		"warnings": errStrings,
	})
}

// handleExport handles POST /api/auth/totp/export.
// Returns an encrypted blob that can be re-imported via handleImport.
func (h *Handler) handleExport(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		writeJSONErr(w, 401, "unauthorized")
		return
	}

	store, err := h.storeFor(userID)
	if err != nil {
		writeJSONErr(w, 500, err.Error())
		return
	}

	blob, err := buildExportBlob(store)
	if err != nil {
		writeJSONErr(w, 500, err.Error())
		return
	}

	writeJSONOK(w, blob)
}
