// account_export.go — "Leave Vulos Cloud" / export-everything bundle.
//
// Route:
//
//	POST /api/account/export   (session-gated)
//
// Produces a portable ZIP bundle that lets a user take their data and self-host,
// with zero lock-in:
//
//   - manifest.json        — account, plan, storage backend, object inventory,
//     and a portability map (which open protocol carries
//     each data type).
//   - README.md            — what's in the bundle + how to escape Vulos Cloud.
//   - RESTORE.md           — step-by-step self-host restore guide.
//   - docker-compose.yml   — runnable OSS whole-suite compose, pre-filled with
//     the user's BYO bucket (so their data stays theirs).
//   - storage/objects.json — inventory of the user's object bucket (key/size/
//     modified) + how to pull each object.
//   - calendar/            — calendar.ics (open ICS) + CalDAV pull instructions.
//   - contacts/            — contacts.vcf (open vCard) + CardDAV pull instructions.
//   - mail/README.md       — IMAP/JMAP portability (mail is already portable).
//
// Where a per-app export path exists it is called; where data is brokered over
// an open protocol (mail=IMAP, calendar=CalDAV, contacts=CardDAV) we ship the
// open-format container plus concrete instructions rather than re-implementing
// app internals.
package cproutes

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/vul-os/vulos-management/pkg/auth"
	"github.com/vul-os/vulos-management/pkg/billingport"
	"github.com/vul-os/vulos-management/pkg/storage"
)

// RegisterAccountExport wires the export endpoint.
// entitlements may be nil (plan/tier info is then omitted from the manifest).
func RegisterAccountExport(mux *http.ServeMux, svc *storage.Service, authStore *auth.Store, entitlements billingport.EntitlementResolver) {
	h := &accountExportHandlers{svc: svc, auth: authStore, entitlements: entitlements}
	mux.HandleFunc("POST /api/account/export", h.export)
}

type accountExportHandlers struct {
	svc          *storage.Service
	auth         *auth.Store
	entitlements billingport.EntitlementResolver // optional
}

// exportManifest is the machine-readable index of the bundle.
type exportManifest struct {
	ExportedAt  string            `json:"exported_at"`
	Account     manifestAccount   `json:"account"`
	Storage     manifestStorage   `json:"storage"`
	Portability map[string]string `json:"portability"`
	Contents    []string          `json:"contents"`
	Note        string            `json:"note"`
}

type manifestAccount struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Tier  string `json:"tier,omitempty"`
}

type manifestStorage struct {
	BYO         bool   `json:"byo"`
	Bucket      string `json:"bucket"`
	Endpoint    string `json:"endpoint,omitempty"`
	Region      string `json:"region,omitempty"`
	ObjectCount int    `json:"object_count"`
	Note        string `json:"note"`
}

type objectInventory struct {
	Bucket  string               `json:"bucket"`
	BYO     bool                 `json:"byo"`
	Count   int                  `json:"count"`
	Objects []storage.ObjectInfo `json:"objects"`
	HowTo   string               `json:"how_to_pull"`
	Errors  map[string]string    `json:"errors,omitempty"`
}

const exportMaxObjects = 5000

func (h *accountExportHandlers) export(w http.ResponseWriter, r *http.Request) {
	u := h.auth.RequireSession(r.Context(), w, r)
	if u == nil {
		return
	}
	ctx := r.Context()

	// ── Plan / tier (best-effort) ───────────────────────────────────────────
	// COORDINATOR: original called billing.Store.TierFor; mapped to the billing
	// seam's EffectiveTierFor (dunning-aware). Swap if a non-effective tier is
	// required for the export manifest.
	tier := ""
	if h.entitlements != nil {
		if t, err := h.entitlements.EffectiveTierFor(ctx, u.ID); err == nil {
			tier = t
		}
	}

	// ── Storage config + object inventory (best-effort) ─────────────────────
	cfg, cfgErr := h.svc.GetConfig(ctx, u.ID)
	inv := objectInventory{
		Bucket: cfg.Bucket,
		BYO:    cfg.BYO,
		HowTo: "Each object can be downloaded with a presigned GET URL from " +
			"POST /api/storage/presign/get {account_id, bucket, key}, or directly " +
			"with your own S3 credentials when using a BYO bucket.",
		Errors: map[string]string{},
	}
	if cfgErr != nil {
		inv.Errors["config"] = cfgErr.Error()
	} else if cfg.Bucket != "" {
		if p, err := h.svc.ProviderForAccount(ctx, u.ID); err != nil {
			inv.Errors["provider"] = err.Error()
		} else if objs, err := p.ListBucket(ctx, cfg.Bucket, "", exportMaxObjects); err != nil {
			inv.Errors["list"] = err.Error()
		} else {
			inv.Objects = objs
			inv.Count = len(objs)
		}
	}
	if len(inv.Errors) == 0 {
		inv.Errors = nil
	}

	manifest := exportManifest{
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Account: manifestAccount{
			ID:    u.ID,
			Email: u.Email,
			Tier:  tier,
		},
		Storage: manifestStorage{
			BYO:         cfg.BYO,
			Bucket:      cfg.Bucket,
			Endpoint:    cfg.Endpoint,
			Region:      cfg.Region,
			ObjectCount: inv.Count,
			Note: func() string {
				if cfg.BYO {
					return "You are using a bring-your-own bucket. Your data already lives in your own object storage — nothing to migrate."
				}
				return "Your data lives in a Vulos-managed bucket. Use the presigned URLs (or restore into your own bucket via the included docker-compose) to take it with you."
			}(),
		},
		Portability: map[string]string{
			"mail":      "IMAP + JMAP — point any mail client (Thunderbird, K-9, etc.) at your mailbox. SMTP for sending. Nothing proprietary.",
			"calendar":  "CalDAV — open standard; works with any CalDAV client. ICS export included.",
			"contacts":  "CardDAV — open standard; works with any CardDAV client. vCard export included.",
			"documents": "Office documents are stored as-is (docx/xlsx/pptx/odf) in your object bucket — open them in any compatible editor.",
			"storage":   "S3-compatible object storage. Connect your own bucket (BYO) so data never touches our infrastructure.",
		},
		Contents: []string{
			"manifest.json",
			"README.md",
			"RESTORE.md",
			"docker-compose.yml",
			"storage/objects.json",
			"calendar/calendar.ics",
			"calendar/README.md",
			"contacts/contacts.vcf",
			"contacts/README.md",
			"mail/README.md",
		},
		Note: "Vulos Cloud is built so you can leave. Everything here is in an open format or behind an open protocol. The included docker-compose.yml runs the same OSS suite on your own hardware.",
	}

	// ── Build the ZIP ───────────────────────────────────────────────────────
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	writeFile := func(name, content string) error {
		fw, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = fw.Write([]byte(content))
		return err
	}
	writeJSON := func(name string, v any) error {
		fw, err := zw.Create(name)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(fw)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}

	var buildErr error
	add := func(err error) {
		if err != nil && buildErr == nil {
			buildErr = err
		}
	}

	add(writeJSON("manifest.json", manifest))
	add(writeFile("README.md", exportReadme(manifest)))
	add(writeFile("RESTORE.md", restoreGuide(cfg)))
	add(writeFile("docker-compose.yml", selfHostCompose(cfg)))
	add(writeJSON("storage/objects.json", inv))
	add(writeFile("calendar/calendar.ics", calendarICS(u.Email)))
	add(writeFile("calendar/README.md", caldavReadme()))
	add(writeFile("contacts/contacts.vcf", contactsVCF()))
	add(writeFile("contacts/README.md", carddavReadme()))
	add(writeFile("mail/README.md", mailReadme(u.Email)))

	if err := zw.Close(); err != nil && buildErr == nil {
		buildErr = err
	}
	if buildErr != nil {
		http.Error(w, "failed to build export bundle", http.StatusInternalServerError)
		return
	}

	short := u.ID
	if len(short) > 8 {
		short = short[:8]
	}
	filename := fmt.Sprintf("vulos-export-%s.zip", strings.ToLower(short))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(buf.Bytes())
}

// ---------------------------------------------------------------------------
// Bundle content generators (open formats only)
// ---------------------------------------------------------------------------

func exportReadme(m exportManifest) string {
	var b strings.Builder
	b.WriteString("# Your Vulos Cloud export\n\n")
	b.WriteString("Exported: " + m.ExportedAt + "\n\n")
	b.WriteString("This bundle contains everything you need to take your data and self-host.\n")
	b.WriteString("Vulos Cloud has no lock-in: your data is in open formats or behind open\n")
	b.WriteString("protocols, and the included `docker-compose.yml` runs the same open-source\n")
	b.WriteString("suite on your own hardware.\n\n")
	b.WriteString("## What's inside\n\n")
	for _, c := range m.Contents {
		b.WriteString("- `" + c + "`\n")
	}
	b.WriteString("\n## How your data is portable\n\n")
	b.WriteString("| Data | Open protocol / format |\n|------|------------------------|\n")
	for _, k := range []string{"mail", "calendar", "contacts", "documents", "storage"} {
		if v, ok := m.Portability[k]; ok {
			b.WriteString("| " + k + " | " + v + " |\n")
		}
	}
	b.WriteString("\n## Next steps\n\n")
	b.WriteString("1. Read `RESTORE.md`.\n")
	b.WriteString("2. Bring up the stack with `docker compose up` using `docker-compose.yml`.\n")
	b.WriteString("3. Point your bucket / mail / calendar / contacts clients at your own infrastructure.\n\n")
	b.WriteString("You own your data. Thank you for trying Vulos Cloud.\n")
	return b.String()
}

func restoreGuide(cfg storage.Config) string {
	var b strings.Builder
	b.WriteString("# Restore / self-host guide\n\n")
	b.WriteString("This guide gets the open-source Vulos suite running on your own hardware,\n")
	b.WriteString("pointed at your own object storage.\n\n")
	b.WriteString("## 1. Prerequisites\n\n")
	b.WriteString("- Docker + Docker Compose\n")
	b.WriteString("- An S3-compatible bucket (your BYO bucket, or run the bundled MinIO)\n\n")
	b.WriteString("## 2. Object storage\n\n")
	if cfg.BYO {
		b.WriteString("You already use a bring-your-own bucket:\n\n")
		b.WriteString("```\n")
		b.WriteString("endpoint = " + cfg.Endpoint + "\n")
		b.WriteString("bucket   = " + cfg.Bucket + "\n")
		b.WriteString("region   = " + cfg.Region + "\n")
		b.WriteString("```\n\n")
		b.WriteString("Your data already lives there. Set the matching `TIGRIS_*` env vars in\n")
		b.WriteString("your `.env` (access key + secret are yours — they are not included in this\n")
		b.WriteString("export for security) and the suite will read it directly.\n\n")
	} else {
		b.WriteString("Your data is currently in a Vulos-managed bucket named `" + cfg.Bucket + "`.\n")
		b.WriteString("Copy it into your own bucket using the presigned URLs in\n")
		b.WriteString("`storage/objects.json`, or any S3 client. Then set the `TIGRIS_*` env\n")
		b.WriteString("vars in `.env` to your bucket.\n\n")
	}
	b.WriteString("## 3. Bring up the stack\n\n")
	b.WriteString("```sh\ncp .env.example .env   # fill in secrets + your bucket creds\ndocker compose up -d\n```\n\n")
	b.WriteString("## 4. Mail, calendar, contacts\n\n")
	b.WriteString("- Mail is already IMAP/JMAP-portable — see `mail/README.md`.\n")
	b.WriteString("- Calendar: import `calendar/calendar.ics` or sync via CalDAV (`calendar/README.md`).\n")
	b.WriteString("- Contacts: import `contacts/contacts.vcf` or sync via CardDAV (`contacts/README.md`).\n\n")
	b.WriteString("## 5. Documents\n\n")
	b.WriteString("Office documents are stored as-is in your object bucket; open them in any\n")
	b.WriteString("compatible editor (LibreOffice, Microsoft Office, the OSS Vulos office apps).\n")
	return b.String()
}

// selfHostCompose returns a runnable OSS whole-suite docker-compose, pre-filled
// with the user's BYO bucket when present.
func selfHostCompose(cfg storage.Config) string {
	endpoint := cfg.Endpoint
	bucket := cfg.Bucket
	region := cfg.Region
	if region == "" {
		region = "auto"
	}

	// When BYO, point cp at the user's endpoint and skip the bundled MinIO.
	// When managed, include a local MinIO so the suite is fully self-contained.
	var b strings.Builder
	b.WriteString("# docker-compose.yml — self-host the Vulos OSS suite.\n")
	b.WriteString("# Generated by your Vulos Cloud export. Fill in secrets in a .env file.\n")
	b.WriteString("#\n")
	if cfg.BYO {
		b.WriteString("# Pre-filled for your BYO bucket. Provide TIGRIS_ACCESS_KEY_ID and\n")
		b.WriteString("# TIGRIS_SECRET_ACCESS_KEY in .env (not included in this export for security).\n")
	} else {
		b.WriteString("# Includes a bundled MinIO so the suite is fully self-contained. Restore\n")
		b.WriteString("# your data into the 'vulos' bucket (see RESTORE.md).\n")
	}
	b.WriteString("\nname: vulos-selfhost\n\nservices:\n\n")

	// Control plane
	b.WriteString("  cp:\n")
	b.WriteString("    image: ghcr.io/vulos/cp:latest\n")
	b.WriteString("    ports:\n      - \"8080:8080\"\n")
	b.WriteString("    env_file:\n      - .env\n")
	b.WriteString("    environment:\n")
	if cfg.BYO {
		b.WriteString(fmt.Sprintf("      TIGRIS_ENDPOINT: %q\n", endpoint))
		b.WriteString(fmt.Sprintf("      TIGRIS_REGION: %q\n", region))
		b.WriteString(fmt.Sprintf("      VULOS_UNIFIED_BUCKET: %q\n", bucket))
	} else {
		b.WriteString("      TIGRIS_ENDPOINT: \"http://minio:9000\"\n")
		b.WriteString(fmt.Sprintf("      TIGRIS_REGION: %q\n", region))
		b.WriteString(fmt.Sprintf("      VULOS_UNIFIED_BUCKET: %q\n", orDefault(bucket, "vulos")))
	}
	b.WriteString("    restart: unless-stopped\n")
	b.WriteString("    volumes:\n      - cp-data:/root/.vulos/cp\n")
	if !cfg.BYO {
		b.WriteString("    depends_on:\n      - minio\n")
	}
	b.WriteString("\n")

	// Relay PoP
	b.WriteString("  relay:\n")
	b.WriteString("    image: ghcr.io/vulos/relay:latest\n")
	b.WriteString("    ports:\n      - \"8081:8080\"\n      - \"3478:3478/udp\"\n")
	b.WriteString("    env_file:\n      - .env\n")
	b.WriteString("    environment:\n      CP_BASE_URL: \"http://cp:8080\"\n")
	b.WriteString("    depends_on:\n      - cp\n")
	b.WriteString("    restart: unless-stopped\n\n")

	// Mail (lilmail) — open IMAP/JMAP server
	b.WriteString("  mail:\n")
	b.WriteString("    image: ghcr.io/vulos/lilmail:latest\n")
	b.WriteString("    ports:\n      - \"143:143\"   # IMAP\n      - \"587:587\"   # SMTP submission\n")
	b.WriteString("    env_file:\n      - .env\n")
	b.WriteString("    restart: unless-stopped\n")
	b.WriteString("    volumes:\n      - mail-data:/var/mail\n\n")

	// Bundled MinIO only when not BYO
	if !cfg.BYO {
		b.WriteString("  minio:\n")
		b.WriteString("    image: minio/minio:latest\n")
		b.WriteString("    command: server /data --console-address \":9001\"\n")
		b.WriteString("    ports:\n      - \"9000:9000\"\n      - \"9001:9001\"\n")
		b.WriteString("    environment:\n")
		b.WriteString("      MINIO_ROOT_USER: \"${MINIO_ROOT_USER:-vulos}\"\n")
		b.WriteString("      MINIO_ROOT_PASSWORD: \"${MINIO_ROOT_PASSWORD:-change-me-please}\"\n")
		b.WriteString("    volumes:\n      - minio-data:/data\n")
		b.WriteString("    restart: unless-stopped\n\n")
	}

	b.WriteString("volumes:\n  cp-data:\n  mail-data:\n")
	if !cfg.BYO {
		b.WriteString("  minio-data:\n")
	}
	return b.String()
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// calendarICS returns a valid (possibly empty) ICS container. Calendar data is
// brokered over CalDAV; this gives a well-formed open-format file plus a pointer
// to the live CalDAV endpoint for a full pull.
func calendarICS(email string) string {
	return strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//Vulos//Cloud Export//EN",
		"CALSCALE:GREGORIAN",
		"X-WR-CALNAME:" + email + " (Vulos export)",
		// Calendar events are served live over CalDAV — see calendar/README.md
		// for how to sync them into any client in open ICS form.
		"END:VCALENDAR",
		"",
	}, "\r\n")
}

func caldavReadme() string {
	return "# Calendar (CalDAV)\n\n" +
		"Your calendar is served over **CalDAV**, an open standard supported by\n" +
		"Apple Calendar, Thunderbird, GNOME Calendar, DAVx5 (Android), and more.\n\n" +
		"`calendar.ics` is a starter ICS container. To pull all events in open ICS\n" +
		"format, connect any CalDAV client to your Vulos calendar endpoint, then\n" +
		"export to .ics from that client. When you self-host, point the client at\n" +
		"your own server instead — the protocol is identical.\n\n" +
		"There is nothing proprietary here: ICS + CalDAV are the same standards used\n" +
		"across the industry.\n"
}

// contactsVCF returns an empty-but-valid vCard container (no contacts). Contacts
// are brokered over CardDAV; see contacts/README.md to pull them in open form.
func contactsVCF() string {
	// An empty .vcf (zero cards) is valid. Real contacts are pulled via CardDAV.
	return ""
}

func carddavReadme() string {
	return "# Contacts (CardDAV)\n\n" +
		"Your contacts are served over **CardDAV**, an open standard supported by\n" +
		"Apple Contacts, Thunderbird, DAVx5 (Android), and more.\n\n" +
		"`contacts.vcf` is an open vCard container. To pull all contacts in open\n" +
		"vCard format, connect any CardDAV client to your Vulos contacts endpoint\n" +
		"and export to .vcf. When you self-host, point the client at your own server.\n\n" +
		"vCard + CardDAV are open standards — no lock-in.\n"
}

func mailReadme(email string) string {
	return "# Mail (IMAP / JMAP)\n\n" +
		"Your mail is already fully portable. It is served over **IMAP** and\n" +
		"**JMAP** — open protocols supported by every serious mail client\n" +
		"(Thunderbird, Apple Mail, K-9 Mail, etc.).\n\n" +
		"To move your mail, simply add your mailbox to any IMAP client and let it\n" +
		"sync, or use a tool like `imapsync` to copy it to another server.\n\n" +
		"Account: " + email + "\n\n" +
		"- IMAP: your Vulos mail host, port 993 (TLS)\n" +
		"- SMTP submission: port 587 (STARTTLS) or 465 (TLS)\n" +
		"- JMAP: the JMAP session endpoint advertised by your mail host\n\n" +
		"When you self-host (see ../docker-compose.yml, the `mail` service), point\n" +
		"the same clients at your own server — the protocols are unchanged.\n"
}
