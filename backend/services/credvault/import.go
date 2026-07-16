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
	"sync"
	"time"

	"github.com/google/uuid"
)

// ---- Hostile-input bounds ------------------------------------------------
//
// Import parses files the user got from SOMEWHERE ELSE (Bitwarden, 1Password,
// KeePass, Chrome — or an attacker who talked them into "restoring a backup").
// Every one of these limits exists because the unbounded version is a way to
// wedge or OOM the box:
//
//   - maxImportBody:    json.Decoder on a raw r.Body will happily read a 10 GB
//     upload into RAM. Cap the request itself.
//   - maxImportFile:    the body carries base64; the decoded file is ~3/4 of it,
//     but a nested/again-base64'd payload can still balloon.
//     Cap the DECODED bytes too.
//   - maxImportEntries: a 6 MiB CSV can hold ~200k rows. We refuse (413) rather
//     than truncate — a silently-truncated password import is
//     worse than a rejected one.
//   - maxFieldLen:      one row must not be able to carry a 6 MiB "username".
const (
	maxImportBody    = 12 << 20 // 12 MiB request body (base64 inflates ~4/3)
	maxImportFile    = 8 << 20  // 8 MiB decoded file
	maxImportEntries = 20000    // entries per import
	maxFieldLen      = 16 << 10 // 16 KiB per field (URL/username/password/notes)

	// minExportPassword is the floor for the passphrase that encrypts an export.
	// The export blob is every password the user has; a 3-character passphrase
	// makes the AES-256-GCM in front of it decorative.
	minExportPassword = 8
)

// ---- Import result -------------------------------------------------------

// ImportResult summarises the outcome of an import operation.
//
// Parsed/Imported/Skipped/Errors always reconcile: Parsed == Imported + Skipped
// + Errors. Callers (and the UI) must show all four — a partial import that
// reports only "imported: 12" while silently dropping 40 rows is the exact bug
// class this struct exists to prevent.
type ImportResult struct {
	Parsed   int      `json:"parsed"` // entries found in the file
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"` // duplicates of entries already in the vault
	Errors   int      `json:"errors"`  // entries the vault refused
	Warnings []string `json:"warnings,omitempty"`
}

// ---- Step-up gate --------------------------------------------------------

// stepUpGate throttles failed master-password step-up attempts.
//
// Two reasons this is not optional:
//
//  1. Brute force. /export re-checks the master password. Without a throttle
//     that endpoint is an online password oracle for the whole vault.
//  2. Memory DoS. Each check runs Argon2id at 64 MiB. An attacker with a stolen
//     session who fires 100 concurrent wrong-password exports allocates 6.4 GiB
//     — the verification itself is the weapon.
//
// Fails CLOSED: once a user is over the limit every step-up is refused until the
// window expires, whether or not the password offered is correct.
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

// locked reports whether userID is currently locked out of step-up.
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

// fail records a failed step-up for userID.
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

// success clears the failure counter for userID.
func (g *stepUpGate) success(userID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.fails, userID)
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

// truncField clamps a single imported field to maxFieldLen.
//
// The ID under which an entry is stored is a server-generated UUID (below), and
// nothing from the file ever reaches a filename — so there is no path-traversal
// surface here even for a field of "../../etc/passwd". The cap exists to stop a
// single hostile row from carrying megabytes into the vault's re-encrypted blob.
func truncField(s string) (string, bool) {
	if len(s) <= maxFieldLen {
		return s, false
	}
	return s[:maxFieldLen], true
}

// ImportEntries adds the given ParsedEntries into vault v, skipping duplicates.
// The vault must be unlocked.  Returns a summary of what happened.
//
// Entries are persisted in ONE write via Vault.AddEntries — see the note there:
// the naive per-entry AddEntry loop is quadratic and hangs the box on a large
// (or hostile) file.
func ImportEntries(v *Vault, parsed []ParsedEntry) (ImportResult, error) {
	var result ImportResult
	result.Parsed = len(parsed)

	if len(parsed) > maxImportEntries {
		return result, fmt.Errorf("import: file contains %d entries, limit is %d", len(parsed), maxImportEntries)
	}

	existing, err := v.ListEntries()
	if err != nil {
		return result, fmt.Errorf("import: list existing entries: %w", err)
	}
	dupes := buildDedupeSet(existing)

	batch := make([]Entry, 0, len(parsed))
	truncated := 0
	for _, p := range parsed {
		k := dedupeKey{
			url:      canonicalURL(p.URL),
			username: strings.ToLower(strings.TrimSpace(p.Username)),
		}
		if _, isDupe := dupes[k]; isDupe {
			result.Skipped++
			continue
		}

		var cut bool
		e := Entry{ID: uuid.New().String()}
		if e.URL, cut = truncField(p.URL); cut {
			truncated++
		}
		if e.Username, cut = truncField(p.Username); cut {
			truncated++
		}
		if e.Password, cut = truncField(p.Password); cut {
			truncated++
		}
		if e.Notes, cut = truncField(p.Notes); cut {
			truncated++
		}

		batch = append(batch, e)
		// Add to dedupe set so within-batch duplicates are also caught.
		dupes[k] = struct{}{}
	}

	added, err := v.AddEntries(batch)
	if err != nil {
		// Nothing was persisted (AddEntries rolls the batch back) — say so.
		result.Errors = len(batch)
		return result, fmt.Errorf("import: save vault: %w", err)
	}
	result.Imported = added
	result.Errors = len(batch) - added

	if truncated > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("%d oversized field(s) were truncated to %d bytes", truncated, maxFieldLen))
	}
	if result.Errors > 0 {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("%d entr(ies) could not be stored", result.Errors))
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
	if len(blob.Entries) > maxImportEntries {
		return ImportResult{}, fmt.Errorf("import: export contains %d entries, limit is %d", len(blob.Entries), maxImportEntries)
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
//
//	POST /api/auth/vault/import  — import credentials from an external format
//	POST /api/auth/vault/export  — encrypted export
//
// AUTHORISATION. Both routes are mounted on the SAME mux as the rest of the
// vault surface, so they sit behind auth.Handler.Middleware, which (a) deletes
// any client-supplied X-User-ID before routing and re-sets it from the validated
// session, (b) 401s every non-public path with no session, and (c) enforces the
// CSRF content-type/Origin check on cookie-authed POSTs. The user identity these
// handlers act on is therefore SERVER-DERIVED; there is no account id in the
// request body and a forged header cannot reach another user's vault.
//
// /export additionally requires a master-password STEP-UP (see handleExport):
// a session cookie alone must never be able to dump every password the user has.
func RegisterImportHandlers(mux *http.ServeMux, store *Handler) {
	mux.HandleFunc("POST /api/auth/vault/import", store.handleImport)
	mux.HandleFunc("POST /api/auth/vault/export", store.handleExport)
}

// noStore marks a response as never-cacheable. An export body is the plaintext-
// equivalent of the whole vault; it must not land in a disk cache, a proxy, or
// the browser's back/forward cache.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
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
	// Identity is SERVER-DERIVED: the auth middleware stripped any attacker-
	// supplied X-User-ID and re-set it from the validated session. The body
	// carries no account id, so there is nothing here to confuse.
	uid := requireUserID(w, r)
	if uid == "" {
		return
	}
	noStore(w)

	var req struct {
		Format   string `json:"format"`
		Data     string `json:"data"`     // base64-encoded raw file
		Password string `json:"password"` // only for encrypted format
	}
	// Bound the request BEFORE decoding: an unbounded json.Decoder on r.Body is
	// an OOM primitive.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxImportBody)).Decode(&req); err != nil {
		if strings.Contains(err.Error(), "too large") {
			writeErrCV(w, 413, fmt.Sprintf("import file too large (limit %d MiB)", maxImportBody>>20))
			return
		}
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
	if len(raw) > maxImportFile {
		writeErrCV(w, 413, fmt.Sprintf("import file too large (limit %d MiB)", maxImportFile>>20))
		return
	}

	v, err := h.vaultFor(uid)
	if err != nil {
		writeErrCV(w, 500, "vault unavailable")
		return
	}
	if !v.IsUnlocked() {
		writeErrCV(w, 423, "vault is locked — unlock first")
		return
	}

	// Parse. Parser errors are reported as a bounded, generic message: the error
	// from a JSON/CSV decoder can quote the offending bytes, and those bytes are
	// credentials.
	var (
		parsed []ParsedEntry
		result ImportResult
		perr   error
	)
	switch req.Format {
	case "bitwarden":
		parsed, perr = ParseBitwardenJSON(raw)
	case "1password-1pif":
		parsed, perr = Parse1PIF(raw)
	case "1password-csv":
		parsed, perr = Parse1PasswordCSV(raw)
	case "keepass-csv":
		parsed, perr = ParseKeePassCSV(raw)
	case "chrome-csv":
		parsed, perr = ParseChromeCSV(raw)
	case "encrypted":
		if req.Password == "" {
			writeErrCV(w, 400, "password is required for encrypted format")
			return
		}
		result, err = ImportEncrypted(v, raw, req.Password)
		if err != nil {
			writeErrCV(w, 400, "could not read export file — wrong password or corrupted data")
			return
		}
		writeJSONCV(w, result)
		return
	default:
		writeErrCV(w, 400, "unknown format")
		return
	}

	if perr != nil {
		writeErrCV(w, 400, fmt.Sprintf("could not parse %s file — is it the right format?", req.Format))
		return
	}
	// Refuse, never truncate: a password import that silently drops rows leaves
	// the user believing they migrated when they did not.
	if len(parsed) > maxImportEntries {
		writeErrCV(w, 413, fmt.Sprintf("file contains %d entries, limit is %d", len(parsed), maxImportEntries))
		return
	}

	result, err = ImportEntries(v, parsed)
	if err != nil {
		writeErrCV(w, 500, "import failed — no entries were saved")
		return
	}

	writeJSONCV(w, result)
}

// handleExport encrypts the vault contents and returns the ciphertext as a
// base64 string.
//
// Request body (JSON):
//
//	{
//	  "master_password": "<the vault master password — STEP-UP>",
//	  "password":        "<passphrase that will encrypt the export file>"
//	}
//
// Response body (JSON):
//
//	{ "data": "<base64 ciphertext>" }
//
// SECURITY. This endpoint returns every credential the user has. Session auth
// alone is not enough for that: an XSS payload or a CSRF'd request rides the
// session cookie, and the vault is very likely already unlocked when the user is
// using it. So export demands the MASTER PASSWORD again (master_password), which
// neither an injected script nor a cross-site form can supply. Failed step-ups
// are throttled (see stepUpGate) because the check itself is a 64 MiB Argon2id
// computation and would otherwise be both a password oracle and a memory DoS.
//
// `password` is a SEPARATE secret: it encrypts the resulting file so the backup
// can be stored off-box. It is not authentication and is never accepted in place
// of the step-up.
func (h *Handler) handleExport(w http.ResponseWriter, r *http.Request) {
	uid := requireUserID(w, r)
	if uid == "" {
		return
	}
	noStore(w)

	var req struct {
		MasterPassword string `json:"master_password"`
		Password       string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErrCV(w, 400, "invalid request body")
		return
	}
	if req.Password == "" {
		writeErrCV(w, 400, "password is required")
		return
	}
	if len(req.Password) < minExportPassword {
		writeErrCV(w, 400, fmt.Sprintf("export password must be at least %d characters", minExportPassword))
		return
	}
	if req.MasterPassword == "" {
		writeErrCV(w, 401, "master password is required to export the vault")
		return
	}

	v, err := h.vaultFor(uid)
	if err != nil {
		writeErrCV(w, 500, "vault unavailable")
		return
	}
	if !v.IsUnlocked() {
		writeErrCV(w, 423, "vault is locked — unlock first")
		return
	}

	// Step-up gate. Throttle first, then verify — a locked-out user must not be
	// able to keep spending Argon2id memory.
	if locked, retry := h.stepUp().locked(uid); locked {
		writeErrCV(w, 429, fmt.Sprintf("too many failed attempts — try again in %d minute(s)", int(retry.Minutes())+1))
		return
	}
	if !v.VerifyMasterPassword(req.MasterPassword) {
		h.stepUp().fail(uid)
		writeErrCV(w, 401, "wrong master password")
		return
	}
	h.stepUp().success(uid)

	ct, err := ExportEncrypted(v, req.Password)
	if err != nil {
		writeErrCV(w, 500, "export failed")
		return
	}

	writeJSONCV(w, map[string]string{
		"data": base64.StdEncoding.EncodeToString(ct),
	})
}
