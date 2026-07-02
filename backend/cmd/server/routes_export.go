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
//
// NOT covered (and the manifest says so, rather than faking completeness):
//   - Talk/Meet history, Board/Docs documents, app data, and anything held only
//     on a peer instance (content-blind shares we cannot decrypt server-side).
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

// registerExportRoutes wires GET /api/export/data. filesSvc may be nil (Files
// service failed to init); in that case the files/ section is omitted and the
// manifest records why. mailBaseURL is the LilMail base (VULOS_MAIL_URL).
func registerExportRoutes(mux *http.ServeMux, filesSvc *files.Service, mailBaseURL string, brokerHeaders map[string]string) {
	mux.HandleFunc("GET /api/export/data", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeErr(w, 401, "unauthorized")
			return
		}

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
		if mailBaseURL != "" {
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
	b.WriteString("  - Talk / Meet call history, Board & Docs documents, and per-app data\r\n")
	b.WriteString("    are not yet exportable through this endpoint.\r\n")
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
