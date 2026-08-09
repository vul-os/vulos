package main

// routes_export.go — "It's yours": the anti-lock-in / data-permanence proof.
//
// GET /api/export/data streams a single .zip of the signed-in user's data in
// STANDARD, portable formats — no Vulos-proprietary container, nothing that
// requires Vulos to read back. This is the concrete, verifiable half of the
// legible-trust surface: sovereignty you can not only SEE (the shell badge +
// transparency panel) but WALK AWAY WITH.
//
// Coverage (honest — the archive itself carries a MANIFEST.txt saying the same):
//
//   mail/       — messages from the local LilMail /v1 API, one .eml per message
//                 (RFC-822-ish) plus a messages.json index, per folder. Bodies
//                 are the representation the /v1 list endpoint returns (full body
//                 where the mail service includes it, else the preview — the
//                 manifest is explicit about this).
//   files/      — the user's Drive tree (services/files), real bytes, original
//                 paths and names. ACL-gated by the same service the Files app
//                 uses; only the caller's own nodes are walked.
//   calendar.ics— iCalendar export IF the mail service exposes /v1/calendar/events.
//   contacts.vcf— vCard export IF the mail service exposes /v1/contacts.
//   settings.json— the caller's own OS profile/preferences, secrets SCRUBBED.
//                 Only a fixed allowlist of non-sensitive fields is serialized;
//                 API keys, PIN hashes and password hashes are NEVER included.
//
// NOT covered (and the manifest says so, rather than faking completeness):
//   - Diwan documents (docs/sheets/slides/PDF/whiteboards), app data, and
//     anything held only on a peer instance (content-blind shares we cannot
//     decrypt server-side). Chat/video history from third-party comms apps
//     (Cinny/Element, Jitsi Meet/Element Call) lives with those services, not
//     Vulos, so there is nothing to export here by design.
//
// Session-authed: identity is ALWAYS the X-User-ID header injected by the auth
// middleware, never the request body — same rule as routes_files.go.

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"vulos/backend/services/assistant"
	"vulos/backend/services/auth"
	"vulos/backend/services/files"
)

// exportMailFolders is the set of LilMail folders the export walks. Kept small
// and standard so the export is predictable; missing folders are skipped
// silently (a fresh mailbox has no Archive, etc.).
var exportMailFolders = []string{"INBOX", "Sent", "Drafts", "Archive"}

// exportMailPerFolder caps how many messages are pulled per folder so a huge
// mailbox can't turn one download into an unbounded stream. Generous but finite.
const exportMailPerFolder = 2000

// exportMaxFileBytes caps a single Drive object copied into the zip. Objects
// larger than this are listed in the manifest as skipped-too-large rather than
// silently dropped, so the export never lies about completeness.
const exportMaxFileBytes = 256 << 20 // 256 MiB

// exportMaxFiles bounds the total number of Drive nodes walked, a belt-and-braces
// guard against a pathological tree.
const exportMaxFiles = 20000

// settingsProvider returns the caller's own OS preferences as an already-safe,
// secret-scrubbed map ready to serialize. It is the ONLY settings source the
// export trusts: the implementation (see safeProfileExport in main.go) copies a
// fixed allowlist of non-sensitive fields, so no API key / PIN hash / password
// hash can reach the archive even if the profile struct grows new secret fields.
// A nil provider (or a false second return) simply omits settings.json.
type settingsProvider func(userID string) (map[string]any, bool)

// registerExportRoutes wires GET /api/export/data. filesSvc may be nil (Files
// service failed to init); in that case the files/ section is omitted and the
// manifest records why. mailBaseURL is the LilMail base (VULOS_MAIL_URL).
// settings may be nil; when present it supplies the secret-scrubbed profile.
// ownerID resolves the box owner: when the box runs in brokered-mail mode, the
// mail/calendar/contacts sections are the OWNER's data (see mailbroker_owner.go)
// and are omitted for every other account (nil ⇒ omitted for everyone).
func registerExportRoutes(mux *http.ServeMux, filesSvc *files.Service, mailBaseURL string, brokerHeaders map[string]string, settings settingsProvider, ownerID func() string) {
	mux.HandleFunc("GET /api/export/data", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, 401, "unauthorized")
			return
		}
		// IDOR-EXPORT-MAIL-01: "your data" must mean the CALLER's data. In broker
		// mode the mail credential is the box owner's single account, so a
		// non-owner's export would otherwise ship the owner's inbox (.eml),
		// calendar (.ics) and address book (.vcf) in a zip. Omit those sections
		// for anyone but the owner — and say so in the manifest rather than
		// pretending the mailbox was empty. Files and settings stay per-caller.
		mailAllowed := brokeredMailAllowed(brokerHeaders, ownerID, userID)

		auth := assistant.Auth{
			Cookie: r.Header.Get("Cookie"),
			Broker: brokerHeaders,
			UserID: userID,
		}

		ts := time.Now().UTC().Format("20060102-150405")
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "vulos-export-"+ts+".zip"))
		w.Header().Set("Cache-Control", "no-store")

		zw := zip.NewWriter(w)
		defer zw.Close()

		// notes accumulates the honest coverage report written last as MANIFEST.txt.
		var notes []string
		add := func(f string, a ...any) { notes = append(notes, fmt.Sprintf(f, a...)) }

		// --- mail --------------------------------------------------------------
		if mailBaseURL == "" {
			add("mail: SKIPPED — no mail service configured (VULOS_MAIL_URL unset)")
		} else if !mailAllowed {
			add("mail: SKIPPED — this box uses a single box-wide mail account belonging to")
			add("      the owner, and you are not the owner. Exporting it here would hand")
			add("      you another account's inbox, so it is deliberately omitted.")
		} else {
			src := assistant.NewLilmailSource(mailBaseURL)
			total, reached := 0, false
			for _, folder := range exportMailFolders {
				msgs, err := src.Recent(r.Context(), auth, folder, exportMailPerFolder)
				if err != nil {
					// One unreachable/absent folder shouldn't abort the export.
					if !reached {
						add("mail/%s: skipped — %s", folder, oneLine(err.Error()))
					}
					continue
				}
				reached = true
				if len(msgs) == 0 {
					continue
				}
				// Per-folder JSON index (machine-readable, full fidelity of what
				// the /v1 API returned).
				if err := writeZipJSON(zw, path.Join("mail", folder, "messages.json"), msgs); err != nil {
					return
				}
				for i, m := range msgs {
					name := path.Join("mail", folder, fmt.Sprintf("%04d-%s.eml", i+1, slug(m.Subject)))
					if err := writeZipBytes(zw, name, messageToEML(m)); err != nil {
						return
					}
				}
				total += len(msgs)
				add("mail/%s: %d messages (.eml + messages.json)", folder, len(msgs))
			}
			switch {
			case !reached:
				add("mail: SKIPPED — mail service unreachable or not authenticated")
			case total == 0:
				add("mail: reached, but no messages found")
			}
			add("mail note: bodies are the representation the /v1 list API returns; where it")
			add("           returns only a preview, the .eml body is that preview.")
		}

		// --- files -------------------------------------------------------------
		if filesSvc == nil {
			add("files: SKIPPED — Files service unavailable on this instance")
		} else {
			fc := &fileExportCounters{}
			exportDriveTree(r.Context(), zw, filesSvc, userID, "", "files", 0, fc, add)
			add("files: %d file(s) exported, %d folder(s)", fc.files, fc.folders)
			if fc.skippedLarge > 0 {
				add("files: %d file(s) skipped — larger than %d MiB", fc.skippedLarge, exportMaxFileBytes>>20)
			}
			if fc.errors > 0 {
				add("files: %d file(s) could not be read and were skipped", fc.errors)
			}
		}

		// --- calendar (optional, iff the mail service exposes it) --------------
		// Same owner gate as mail above: in broker mode these come back from the
		// box-wide credential, so for a non-owner they are the OWNER's calendar
		// and address book, not the caller's.
		if mailBaseURL != "" && !mailAllowed {
			add("calendar: SKIPPED — box-wide mail account belongs to the owner (see mail note)")
			add("contacts: SKIPPED — box-wide mail account belongs to the owner (see mail note)")
		} else if mailBaseURL != "" {
			if ics, ok := fetchCalendarICS(r.Context(), mailBaseURL, auth); ok {
				if err := writeZipBytes(zw, "calendar.ics", ics); err != nil {
					return
				}
				add("calendar.ics: exported from /v1/calendar/events")
			} else {
				add("calendar: SKIPPED — mail service exposes no readable /v1/calendar/events")
			}
			if vcf, ok := fetchContactsVCF(r.Context(), mailBaseURL, auth); ok {
				if err := writeZipBytes(zw, "contacts.vcf", vcf); err != nil {
					return
				}
				add("contacts.vcf: exported from /v1/contacts")
			} else {
				add("contacts: SKIPPED — mail service exposes no readable /v1/contacts")
			}
		}

		// --- settings (the caller's own, secret-scrubbed) ----------------------
		if settings == nil {
			add("settings: SKIPPED — profile store unavailable on this instance")
		} else if prefs, ok := settings(userID); ok {
			if err := writeZipJSON(zw, "settings.json", prefs); err != nil {
				return
			}
			add("settings.json: your OS preferences (secrets scrubbed — no API keys, PINs or passwords)")
		} else {
			add("settings: SKIPPED — no profile found for this account")
		}

		// --- manifest (written last, so it reflects everything above) ----------
		_ = writeZipBytes(zw, "MANIFEST.txt", []byte(buildManifest(userID, ts, notes)))
	})
}

// fileExportCounters tallies the Drive walk for the manifest.
type fileExportCounters struct {
	files, folders, skippedLarge, errors int
}

// exportDriveTree recursively copies userID's Drive subtree rooted at parentID
// into the zip under zipPrefix. It is best-effort: an unreadable node is counted
// and skipped, never fatal, so one bad object can't sink the whole export.
func exportDriveTree(ctx context.Context, zw *zip.Writer, svc *files.Service, userID, parentID, zipPrefix string, depth int, fc *fileExportCounters, add func(string, ...any)) {
	if depth > 64 || fc.files+fc.folders > exportMaxFiles {
		return
	}
	nodes, err := svc.List(userID, parentID)
	if err != nil {
		if parentID == "" {
			add("files: could not list Drive root — %s", oneLine(err.Error()))
		}
		return
	}
	for _, n := range nodes {
		child := path.Join(zipPrefix, safeSegment(n.Name))
		if n.IsDir {
			fc.folders++
			exportDriveTree(ctx, zw, svc, userID, n.ID, child, depth+1, fc, add)
			continue
		}
		if n.Size > exportMaxFileBytes {
			fc.skippedLarge++
			continue
		}
		rc, _, _, err := svc.GetContent(ctx, userID, n.ID)
		if err != nil {
			fc.errors++
			continue
		}
		werr := writeZipReader(zw, child, rc)
		rc.Close()
		if werr != nil {
			fc.errors++
			continue
		}
		fc.files++
	}
}

// --- mail formatting ---------------------------------------------------------

// messageToEML renders a normalized mail Message as a minimal but valid RFC-822
// .eml. Any mail client (Thunderbird, Apple Mail, mutt) can open these — the
// point is a STANDARD, non-proprietary format.
func messageToEML(m assistant.Message) []byte {
	var b strings.Builder
	hdr := func(k, v string) {
		if strings.TrimSpace(v) != "" {
			fmt.Fprintf(&b, "%s: %s\r\n", k, headerSafe(v))
		}
	}
	hdr("From", m.Sender())
	hdr("To", m.To)
	hdr("Subject", m.Subject)
	if !m.Date.IsZero() {
		hdr("Date", m.Date.UTC().Format(time.RFC1123Z))
	}
	hdr("Message-ID", m.MessageID)
	hdr("In-Reply-To", m.InReplyTo)
	if m.Folder != "" {
		hdr("X-Vulos-Folder", m.Folder)
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	body := m.Body
	if strings.TrimSpace(body) == "" {
		body = m.Preview
	}
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(b.String())
}

// --- optional calendar / contacts --------------------------------------------

// fetchCalendarICS asks the mail service for events and renders an iCalendar
// file. Returns (nil,false) if the endpoint is absent/unreadable — the export
// simply omits the file and the manifest records the gap.
func fetchCalendarICS(ctx context.Context, base string, auth assistant.Auth) ([]byte, bool) {
	var out struct {
		Events []struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Start    string `json:"start"`
			End      string `json:"end"`
			Location string `json:"location"`
			Notes    string `json:"notes"`
		} `json:"events"`
	}
	if !getMailJSON(ctx, base, "/v1/calendar/events", auth, &out) || len(out.Events) == 0 {
		return nil, false
	}
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Vulos//Export//EN\r\n")
	for _, e := range out.Events {
		b.WriteString("BEGIN:VEVENT\r\n")
		fmt.Fprintf(&b, "UID:%s\r\n", icalText(firstNonEmpty(e.ID, e.Title)))
		fmt.Fprintf(&b, "SUMMARY:%s\r\n", icalText(e.Title))
		if dt := icalTime(e.Start); dt != "" {
			fmt.Fprintf(&b, "DTSTART:%s\r\n", dt)
		}
		if dt := icalTime(e.End); dt != "" {
			fmt.Fprintf(&b, "DTEND:%s\r\n", dt)
		}
		if e.Location != "" {
			fmt.Fprintf(&b, "LOCATION:%s\r\n", icalText(e.Location))
		}
		if e.Notes != "" {
			fmt.Fprintf(&b, "DESCRIPTION:%s\r\n", icalText(e.Notes))
		}
		b.WriteString("END:VEVENT\r\n")
	}
	b.WriteString("END:VCALENDAR\r\n")
	return []byte(b.String()), true
}

// fetchContactsVCF asks the mail service for contacts and renders vCard 3.0.
func fetchContactsVCF(ctx context.Context, base string, auth assistant.Auth) ([]byte, bool) {
	var out struct {
		Contacts []struct {
			Name  string `json:"name"`
			Email string `json:"email"`
			Phone string `json:"phone"`
			Notes string `json:"notes"`
		} `json:"contacts"`
	}
	if !getMailJSON(ctx, base, "/v1/contacts", auth, &out) || len(out.Contacts) == 0 {
		return nil, false
	}
	var b strings.Builder
	for _, c := range out.Contacts {
		b.WriteString("BEGIN:VCARD\r\nVERSION:3.0\r\n")
		fmt.Fprintf(&b, "FN:%s\r\n", vcardText(firstNonEmpty(c.Name, c.Email)))
		if c.Email != "" {
			fmt.Fprintf(&b, "EMAIL:%s\r\n", vcardText(c.Email))
		}
		if c.Phone != "" {
			fmt.Fprintf(&b, "TEL:%s\r\n", vcardText(c.Phone))
		}
		if c.Notes != "" {
			fmt.Fprintf(&b, "NOTE:%s\r\n", vcardText(c.Notes))
		}
		b.WriteString("END:VCARD\r\n")
	}
	return []byte(b.String()), true
}

// getMailJSON performs an auth-forwarded GET against the mail service and decodes
// JSON. Returns false on any non-2xx or transport error (caller treats absence
// as "not exportable", not as a fatal error).
func getMailJSON(ctx context.Context, base, apiPath string, auth assistant.Auth, v any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+apiPath, nil)
	if err != nil {
		return false
	}
	if auth.Cookie != "" {
		req.Header.Set("Cookie", auth.Cookie)
	}
	for k, val := range auth.Broker {
		if val != "" {
			req.Header.Set(k, val)
		}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(v) == nil
}

// --- manifest ----------------------------------------------------------------

func buildManifest(userID, ts string, notes []string) string {
	var b strings.Builder
	b.WriteString("VULOS DATA EXPORT\r\n")
	b.WriteString("=================\r\n\r\n")
	fmt.Fprintf(&b, "Account:  %s\r\n", userID)
	fmt.Fprintf(&b, "Created:  %s UTC\r\n\r\n", ts)
	b.WriteString("This archive is YOUR data in standard, portable formats. Nothing here\r\n")
	b.WriteString("needs Vulos to read it back: .eml opens in any mail client, files keep\r\n")
	b.WriteString("their original names/paths, .ics/.vcf are standard calendar/contact files.\r\n\r\n")
	b.WriteString("CONTENTS\r\n--------\r\n")
	for _, n := range notes {
		b.WriteString("  - ")
		b.WriteString(n)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\nNOT INCLUDED (honest boundaries)\r\n--------------------------------\r\n")
	b.WriteString("  - Diwan documents and per-app data are not yet exportable\r\n")
	b.WriteString("    through this endpoint.\r\n")
	b.WriteString("  - Chat/video call history from third-party comms apps (Cinny/Element,\r\n")
	b.WriteString("    Jitsi Meet/Element Call) lives with those services, not Vulos, so\r\n")
	b.WriteString("    there is nothing to export here by design.\r\n")
	b.WriteString("  - Content held only on a PEER instance via a content-blind (end-to-end)\r\n")
	b.WriteString("    share cannot be decrypted server-side, so it is not in this archive —\r\n")
	b.WriteString("    that is the privacy guarantee working as intended, not a gap in your\r\n")
	b.WriteString("    access. Export it from the instance that holds the keys.\r\n")
	return b.String()
}

// --- zip helpers -------------------------------------------------------------

func writeZipBytes(zw *zip.Writer, name string, data []byte) error {
	f, err := zipCreate(zw, name)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return err
}

func writeZipReader(zw *zip.Writer, name string, r io.Reader) error {
	f, err := zipCreate(zw, name)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	return err
}

func writeZipJSON(zw *zip.Writer, name string, v any) error {
	f, err := zipCreate(zw, name)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func zipCreate(zw *zip.Writer, name string) (io.Writer, error) {
	return zw.CreateHeader(&zip.FileHeader{
		Name:     name,
		Method:   zip.Deflate,
		Modified: time.Now(),
	})
}

// --- small string helpers ----------------------------------------------------

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return strings.TrimSpace(s)
}

// slug makes a short, filesystem-safe token from a subject line for .eml names.
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
		if b.Len() >= 40 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "message"
	}
	return out
}

// safeSegment sanitizes a single path segment for a Drive name so it cannot
// escape its folder (no separators, no traversal) inside the zip.
func safeSegment(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.TrimLeft(name, ".")
	// Drop control chars.
	var b strings.Builder
	for _, r := range name {
		if r >= 0x20 {
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		return "unnamed"
	}
	return out
}

func headerSafe(v string) string {
	v = strings.ReplaceAll(v, "\r", " ")
	v = strings.ReplaceAll(v, "\n", " ")
	return strings.TrimSpace(v)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// icalText / vcardText escape per RFC 5545 / 6350: commas, semicolons,
// backslashes and newlines.
func icalText(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ";", "\\;")
	s = strings.ReplaceAll(s, ",", "\\,")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

func vcardText(s string) string { return icalText(s) }

// icalTime converts an ISO/RFC3339 timestamp to iCalendar UTC form
// (YYYYMMDDTHHMMSSZ). Unparseable input yields "" so the field is omitted.
func icalTime(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format("20060102T150405Z")
		}
	}
	return ""
}

// --- settings export ---------------------------------------------------------

// profileStore is the tiny slice of the auth store the export needs: look up a
// user's own profile. *auth.Store satisfies it.
type profileStore interface {
	GetProfile(userID string) (*auth.Profile, bool)
}

// safeProfileExport builds a settingsProvider that serializes ONLY a fixed
// allowlist of non-sensitive preference fields. This is an ALLOWLIST by design:
// new fields on auth.Profile (which may be secrets) are invisible to the export
// until someone deliberately adds them here, so an accidentally-added secret can
// never silently leak into a user's download. It explicitly never touches
// AIAPIKey, PinHash, or the User.PasswordHash, and it drops any Settings-map
// entry whose key looks sensitive (token/key/secret/password/pin/hash).
//
// The provider is owner-scoped by construction: it is only ever called with the
// session-derived userID from the export handler, so it can only ever return the
// caller's own preferences.
func safeProfileExport(store profileStore) settingsProvider {
	if store == nil {
		return nil
	}
	return func(userID string) (map[string]any, bool) {
		p, ok := store.GetProfile(userID)
		if !ok || p == nil {
			return nil, false
		}
		out := map[string]any{
			"user_id":      p.UserID,
			"display_name": p.DisplayName,
			"role":         string(p.Role),
			"theme":        p.Theme,
			"locale":       p.Locale,
			"timezone":     p.Timezone,
			"ai_provider":  p.AIProvider,
			"ai_model":     p.AIModel,
			"initiative":   p.Initiative,
			"created_at":   p.CreatedAt.UTC().Format(time.RFC3339),
			"updated_at":   p.UpdatedAt.UTC().Format(time.RFC3339),
		}
		// The free-form Settings map is user-controlled; copy it, but drop any
		// key that looks like a credential so a stray secret stored there never
		// rides along in the export.
		if len(p.Settings) > 0 {
			safe := map[string]string{}
			for k, v := range p.Settings {
				if looksSensitiveKey(k) {
					continue
				}
				safe[k] = v
			}
			if len(safe) > 0 {
				out["settings"] = safe
			}
		}
		return out, true
	}
}

// looksSensitiveKey reports whether a settings-map key name suggests it holds a
// credential and must be kept out of the export.
//
// The free-form Settings map is user-controlled, so this filter is the ONLY
// gate on it. It is therefore deliberately BROAD and FAIL-CLOSED: it errs
// toward dropping a benign preference rather than letting a secret ride along.
// Any substring match on a credential-ish token drops the entry. In particular
// a bare "key" (catching every *_key: session_key, signing_key, master_key,
// encryption_key, …), "bearer"/"auth"/"oauth"/"jwt"/"cookie" (session
// material), "refresh"/"access"/"session" (token names that don't contain the
// word "token"), and "seed"/"salt"/"otp"/"mnemonic"/"phrase"/"recovery"
// (2FA/recovery material) are all excluded. Missing any of these previously
// leaked e.g. `oauth_refresh`, `session_key`, or a recovery phrase into the
// user's download.
func looksSensitiveKey(k string) bool {
	k = strings.ToLower(k)
	for _, needle := range []string{
		"token", "secret", "password", "passwd", "apikey", "api_key",
		"pin", "hash", "private", "credential",
		// broadened, fail-closed additions:
		"key",       // catches every *_key (signing_key, session_key, master_key, …)
		"bearer",    // Authorization: Bearer …
		"auth",      // auth / authorization / oauth
		"oauth",     // oauth_refresh, oauth_access, …
		"jwt",       // raw JSON Web Tokens
		"cookie",    // session cookies
		"csrf",      // CSRF tokens
		"session",   // session_id / session_key
		"refresh",   // refresh tokens by any name
		"access",    // access_key / access_token
		"cert",      // certificates / cert material
		"signature", // request signatures
		"salt",      // KDF salts
		"seed",      // TOTP / wallet seeds
		"otp",       // otp / totp secrets
		"mnemonic",  // recovery mnemonics
		"phrase",    // recovery phrases
		"recovery",  // recovery material
		"webhook",   // webhook URLs commonly embed a token
	} {
		if strings.Contains(k, needle) {
			return true
		}
	}
	return false
}

// mailBaseURLFromEnv resolves the LilMail base the export reads, matching
// registerMailRoutes' default.
func mailBaseURLFromEnv() string {
	u := os.Getenv("VULOS_MAIL_URL")
	if u == "" {
		u = "http://localhost:3000"
	}
	// Validate; a malformed value disables the mail section rather than erroring.
	if _, err := url.Parse(u); err != nil {
		return ""
	}
	return u
}
