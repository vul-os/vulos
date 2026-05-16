package credvault

// import.go — credential vault import / export for AUTH-08.
//
// Supported import formats:
//   - Bitwarden JSON export (personal vault)
//   - 1Password 1PIF export (.1pif)
//   - 1Password CSV export
//   - KeePass CSV export
//   - Chrome / Chromium CSV export
//
// Export format: AES-256-GCM encrypted JSON blob (same key derivation as the
// vault itself).  The blob is base64-encoded so it can be safely stored or
// transmitted as a string.
//
// Deduplication: an import entry is considered a duplicate of an existing
// vault entry when both the canonicalised URL and the username match.  The
// existing entry is kept and the incoming one is silently skipped.

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ---- Import result -------------------------------------------------------

// ImportResult summarises the outcome of an import operation.
type ImportResult struct {
	Imported int `json:"imported"`
	Skipped  int `json:"skipped"` // duplicates
	Errors   int `json:"errors"`
}

// ---- Format parsers ------------------------------------------------------

// ParsedEntry is an intermediary representation parsed from external formats
// before being converted to a vault Entry.
type ParsedEntry struct {
	URL      string
	Username string
	Password string
	Notes    string
}

// ParseBitwardenJSON parses a Bitwarden personal vault JSON export.
//
// Bitwarden export structure (relevant subset):
//
//	{
//	  "items": [
//	    {
//	      "type": 1,
//	      "name": "...",
//	      "notes": "...",
//	      "login": {
//	        "username": "...",
//	        "password": "...",
//	        "uris": [{"uri": "https://..."}]
//	      }
//	    }
//	  ]
//	}
func ParseBitwardenJSON(data []byte) ([]ParsedEntry, error) {
	var bw struct {
		Items []struct {
			Type  int    `json:"type"` // 1 = login
			Name  string `json:"name"`
			Notes string `json:"notes"`
			Login struct {
				Username string `json:"username"`
				Password string `json:"password"`
				URIs     []struct {
					URI string `json:"uri"`
				} `json:"uris"`
			} `json:"login"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &bw); err != nil {
		return nil, fmt.Errorf("import: bitwarden json: %w", err)
	}

	var out []ParsedEntry
	for _, item := range bw.Items {
		if item.Type != 1 { // only login items
			continue
		}
		uri := ""
		if len(item.Login.URIs) > 0 {
			uri = item.Login.URIs[0].URI
		}
		out = append(out, ParsedEntry{
			URL:      uri,
			Username: item.Login.Username,
			Password: item.Login.Password,
			Notes:    item.Notes,
		})
	}
	return out, nil
}

// onepifRecord is the JSON structure of a single .1pif record.
type onepifRecord struct {
	TypeName       string `json:"typeName"`
	Location       string `json:"location"`
	SecureContents struct {
		Fields []struct {
			Name        string `json:"name"`
			Designation string `json:"designation"`
			Value       string `json:"value"`
		} `json:"fields"`
		Password string `json:"password"`
		URL      string `json:"URL"`
	} `json:"secureContents"`
}

// Parse1PIF parses a 1Password .1pif export file.
//
// 1PIF is a newline-delimited stream of JSON objects.  Non-JSON separator
// lines ("**...**") are skipped.  Each record has a typeName field; we only
// process "webforms.WebForm" entries.
func Parse1PIF(data []byte) ([]ParsedEntry, error) {
	lines := strings.Split(string(data), "\n")
	var out []ParsedEntry

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "**") {
			continue
		}
		var rec onepifRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // skip non-JSON lines
		}
		if rec.TypeName != "webforms.WebForm" {
			continue
		}

		var username, password string
		for _, f := range rec.SecureContents.Fields {
			switch f.Designation {
			case "username":
				username = f.Value
			case "password":
				password = f.Value
			}
		}
		if password == "" {
			password = rec.SecureContents.Password
		}
		uri := rec.SecureContents.URL
		if uri == "" {
			uri = rec.Location
		}

		out = append(out, ParsedEntry{
			URL:      uri,
			Username: username,
			Password: password,
		})
	}
	return out, nil
}

// Parse1PasswordCSV parses a 1Password CSV export.
//
// Expected columns (header row required):
//
//	Title, Username, Password, OTPAuth, URL, Notes
//
// Column order may vary; the parser matches by header name.
func Parse1PasswordCSV(data []byte) ([]ParsedEntry, error) {
	return parseCSVGeneric(data, map[string]string{
		"url":      "URL",
		"username": "Username",
		"password": "Password",
		"notes":    "Notes",
	})
}

// ParseKeePassCSV parses a KeePass CSV export.
//
// Expected columns (header row required):
//
//	"Account","Login Name","Password","Web Site","Comments"
func ParseKeePassCSV(data []byte) ([]ParsedEntry, error) {
	return parseCSVGeneric(data, map[string]string{
		"url":      "Web Site",
		"username": "Login Name",
		"password": "Password",
		"notes":    "Comments",
	})
}

// ParseChromeCSV parses a Chrome / Chromium password export CSV.
//
// Expected columns (header row required):
//
//	name,url,username,password
func ParseChromeCSV(data []byte) ([]ParsedEntry, error) {
	return parseCSVGeneric(data, map[string]string{
		"url":      "url",
		"username": "username",
		"password": "password",
		"notes":    "", // Chrome export has no notes column
	})
}

// ---- Generic CSV parser --------------------------------------------------

// parseCSVGeneric parses a CSV byte slice using a column-name mapping.
// fieldMap maps logical field names ("url", "username", "password", "notes")
// to the actual column header strings in the CSV.
func parseCSVGeneric(data []byte, fieldMap map[string]string) ([]ParsedEntry, error) {
	rows, err := parseCSVBytes(data)
	if err != nil {
		return nil, err
	}
	if len(rows) < 2 {
		return nil, nil // empty file (header only or blank)
	}

	// Build column-index map from the header row.
	header := rows[0]
	colIdx := make(map[string]int, len(header))
	for i, h := range header {
		colIdx[strings.TrimSpace(h)] = i
	}

	resolve := func(logical string) int {
		col, ok := fieldMap[logical]
		if !ok || col == "" {
			return -1
		}
		idx, ok := colIdx[col]
		if !ok {
			return -1
		}
		return idx
	}

	urlIdx := resolve("url")
	userIdx := resolve("username")
	passIdx := resolve("password")
	noteIdx := resolve("notes")

	var out []ParsedEntry
	for _, row := range rows[1:] {
		cell := func(i int) string {
			if i < 0 || i >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[i])
		}
		out = append(out, ParsedEntry{
			URL:      cell(urlIdx),
			Username: cell(userIdx),
			Password: cell(passIdx),
			Notes:    cell(noteIdx),
		})
	}
	return out, nil
}

// parseCSVBytes is a minimal RFC-4180 CSV parser that handles quoted fields
// (including embedded commas and newlines).  It does not depend on encoding/csv
// so that the import logic stays self-contained.
func parseCSVBytes(data []byte) ([][]string, error) {
	s := string(data)
	var rows [][]string
	var row []string
	var field strings.Builder
	inQuote := false

	flush := func() {
		row = append(row, field.String())
		field.Reset()
	}

	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case inQuote && ch == '"':
			// Peek for escaped double-quote ("").
			if i+1 < len(s) && s[i+1] == '"' {
				field.WriteByte('"')
				i++
			} else {
				inQuote = false
			}
		case !inQuote && ch == '"':
			inQuote = true
		case !inQuote && ch == ',':
			flush()
		case !inQuote && ch == '\n':
			flush()
			rows = append(rows, row)
			row = nil
		case !inQuote && ch == '\r':
			// skip \r (handle \r\n)
		default:
			field.WriteByte(ch)
		}
	}
	// Final field / row.
	if field.Len() > 0 || len(row) > 0 {
		flush()
		rows = append(rows, row)
	}
	return rows, nil
}

// ---- Deduplication -------------------------------------------------------

// canonicalURL normalises a URL to a stable key for deduplication.
// It lower-cases the scheme and host and strips the path, query, and fragment.
// Falls back to the raw string (lower-cased) if the URL has no scheme/host.
func canonicalURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.ToLower(raw)
	}
	return strings.ToLower(u.Scheme + "://" + u.Host)
}

// buildDedupeSet returns a map[dedupeKey]struct{} from existing vault entries.
type dedupeKey struct {
	url      string
	username string
}

func buildDedupeSet(entries []Entry) map[dedupeKey]struct{} {
	m := make(map[dedupeKey]struct{}, len(entries))
	for _, e := range entries {
		k := dedupeKey{
			url:      canonicalURL(e.URL),
			username: strings.ToLower(strings.TrimSpace(e.Username)),
		}
		m[k] = struct{}{}
	}
	return m
}

// ---- Import into vault ---------------------------------------------------

// ImportEntries adds the given ParsedEntries into vault v, skipping duplicates.
// The vault must be unlocked.  Returns a summary of what happened.
func ImportEntries(v *Vault, parsed []ParsedEntry) (ImportResult, error) {
	existing, err := v.ListEntries()
	if err != nil {
		return ImportResult{}, fmt.Errorf("import: list existing entries: %w", err)
	}
	dupes := buildDedupeSet(existing)

	var result ImportResult
	for _, p := range parsed {
		k := dedupeKey{
			url:      canonicalURL(p.URL),
			username: strings.ToLower(strings.TrimSpace(p.Username)),
		}
		if _, isDupe := dupes[k]; isDupe {
			result.Skipped++
			continue
		}
		entry := Entry{
			ID:        uuid.New().String(),
			URL:       p.URL,
			Username:  p.Username,
			Password:  p.Password,
			Notes:     p.Notes,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		if err := v.AddEntry(entry); err != nil {
			result.Errors++
			continue
		}
		// Add to dedupe set so within-batch duplicates are also caught.
		dupes[k] = struct{}{}
		result.Imported++
	}
	return result, nil
}

// ---- Encrypted export / import -------------------------------------------

// exportBlob is the JSON structure inside an encrypted export.
type exportBlob struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

const exportVersion = 1

// ExportEncrypted serialises all vault entries and encrypts them using the
// provided password (independent of the vault's own master password, so the
// export can be shared or stored separately).
//
// Returns the encrypted bytes; the caller may base64-encode or write to a file.
func ExportEncrypted(v *Vault, exportPassword string) ([]byte, error) {
	entries, err := v.ListEntries()
	if err != nil {
		return nil, fmt.Errorf("export: list entries: %w", err)
	}

	blob := exportBlob{
		Version: exportVersion,
		Entries: entries,
	}
	plain, err := json.Marshal(blob)
	if err != nil {
		return nil, fmt.Errorf("export: marshal: %w", err)
	}

	// Generate a random salt for the export key.
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("export: generate salt: %w", err)
	}
	key := deriveKey(exportPassword, salt)

	ct, err := aesGCMEncrypt(key, plain)
	if err != nil {
		return nil, fmt.Errorf("export: encrypt: %w", err)
	}

	// Prepend salt so the importer can re-derive the key.
	out := make([]byte, saltLen+len(ct))
	copy(out[:saltLen], salt)
	copy(out[saltLen:], ct)
	return out, nil
}

// ImportEncrypted decrypts an export blob and inserts the entries into vault v,
// applying the same deduplication logic as ImportEntries.
func ImportEncrypted(v *Vault, data []byte, exportPassword string) (ImportResult, error) {
	if len(data) <= saltLen {
		return ImportResult{}, errors.New("import: export blob too short")
	}
	salt := data[:saltLen]
	ct := data[saltLen:]
	key := deriveKey(exportPassword, salt)

	plain, err := aesGCMDecrypt(key, ct)
	if err != nil {
		return ImportResult{}, fmt.Errorf("import: decrypt: wrong password or corrupted data")
	}

	var blob exportBlob
	if err := json.Unmarshal(plain, &blob); err != nil {
		return ImportResult{}, fmt.Errorf("import: parse export blob: %w", err)
	}

	var parsed []ParsedEntry
	for _, e := range blob.Entries {
		parsed = append(parsed, ParsedEntry{
			URL:      e.URL,
			Username: e.Username,
			Password: e.Password,
			Notes:    e.Notes,
		})
	}
	return ImportEntries(v, parsed)
}

// ---- HTTP handlers -------------------------------------------------------

// RegisterImportHandlers wires the import/export HTTP endpoints into mux.
// This is the entry point called by the orchestrator — main.go should call
// this function (do NOT edit main.go directly; the orchestrator will wire it).
//
//	POST /api/auth/vault/import  — import credentials from an external format
//	POST /api/auth/vault/export  — encrypted export
func RegisterImportHandlers(mux *http.ServeMux, store *Handler) {
	mux.HandleFunc("POST /api/auth/vault/import", store.handleImport)
	mux.HandleFunc("POST /api/auth/vault/export", store.handleExport)
}

// handleImport accepts a multipart or JSON request and imports credentials.
//
// Request body (JSON):
//
//	{
//	  "format":   "bitwarden" | "1password-1pif" | "1password-csv" | "keepass-csv" | "chrome-csv" | "encrypted",
//	  "data":     "<base64-encoded file contents>",
//	  "password": "<export password, required only for format=encrypted>"
//	}
func (h *Handler) handleImport(w http.ResponseWriter, r *http.Request) {
	uid := requireUserID(w, r)
	if uid == "" {
		return
	}

	var req struct {
		Format   string `json:"format"`
		Data     string `json:"data"`     // base64-encoded raw file
		Password string `json:"password"` // only for encrypted format
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrCV(w, 400, "invalid request body")
		return
	}
	if req.Format == "" {
		writeErrCV(w, 400, "format is required")
		return
	}
	if req.Data == "" {
		writeErrCV(w, 400, "data is required")
		return
	}

	raw, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		// Try raw URL encoding as fallback.
		raw, err = base64.URLEncoding.DecodeString(req.Data)
		if err != nil {
			writeErrCV(w, 400, "data must be base64-encoded")
			return
		}
	}

	v, err := h.vaultFor(uid)
	if err != nil {
		writeErrCV(w, 500, fmt.Sprintf("vault init: %v", err))
		return
	}
	if !v.IsUnlocked() {
		writeErrCV(w, 423, "vault is locked — unlock first")
		return
	}

	var result ImportResult

	switch req.Format {
	case "bitwarden":
		parsed, perr := ParseBitwardenJSON(raw)
		if perr != nil {
			writeErrCV(w, 400, fmt.Sprintf("parse bitwarden: %v", perr))
			return
		}
		result, err = ImportEntries(v, parsed)

	case "1password-1pif":
		parsed, perr := Parse1PIF(raw)
		if perr != nil {
			writeErrCV(w, 400, fmt.Sprintf("parse 1pif: %v", perr))
			return
		}
		result, err = ImportEntries(v, parsed)

	case "1password-csv":
		parsed, perr := Parse1PasswordCSV(raw)
		if perr != nil {
			writeErrCV(w, 400, fmt.Sprintf("parse 1password csv: %v", perr))
			return
		}
		result, err = ImportEntries(v, parsed)

	case "keepass-csv":
		parsed, perr := ParseKeePassCSV(raw)
		if perr != nil {
			writeErrCV(w, 400, fmt.Sprintf("parse keepass csv: %v", perr))
			return
		}
		result, err = ImportEntries(v, parsed)

	case "chrome-csv":
		parsed, perr := ParseChromeCSV(raw)
		if perr != nil {
			writeErrCV(w, 400, fmt.Sprintf("parse chrome csv: %v", perr))
			return
		}
		result, err = ImportEntries(v, parsed)

	case "encrypted":
		if req.Password == "" {
			writeErrCV(w, 400, "password is required for encrypted format")
			return
		}
		result, err = ImportEncrypted(v, raw, req.Password)

	default:
		writeErrCV(w, 400, fmt.Sprintf("unknown format %q", req.Format))
		return
	}

	if err != nil {
		writeErrCV(w, 500, fmt.Sprintf("import: %v", err))
		return
	}

	writeJSONCV(w, result)
}

// handleExport encrypts the vault contents and returns the ciphertext as a
// base64 string.
//
// Request body (JSON):
//
//	{ "password": "<export password>" }
//
// Response body (JSON):
//
//	{ "data": "<base64 ciphertext>" }
func (h *Handler) handleExport(w http.ResponseWriter, r *http.Request) {
	uid := requireUserID(w, r)
	if uid == "" {
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Password == "" {
		writeErrCV(w, 400, "password is required")
		return
	}

	v, err := h.vaultFor(uid)
	if err != nil {
		writeErrCV(w, 500, fmt.Sprintf("vault init: %v", err))
		return
	}
	if !v.IsUnlocked() {
		writeErrCV(w, 423, "vault is locked — unlock first")
		return
	}

	ct, err := ExportEncrypted(v, req.Password)
	if err != nil {
		writeErrCV(w, 500, fmt.Sprintf("export: %v", err))
		return
	}

	writeJSONCV(w, map[string]string{
		"data": base64.StdEncoding.EncodeToString(ct),
	})
}
