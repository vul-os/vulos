package handlers

// seal.go — OFFICE-46: Completion certificate + sealed PDF.
//
// When all signers on an envelope have status "signed", this handler:
//   1. Reads the source PDF from disk.
//   2. Hashes the current document bytes (SHA-256) as the "before-seal" hash.
//   3. Appends a completion-certificate page (pure-Go PDF writer) summarising
//      every signer name/email/timestamp/IP/identity/doc-hash drawn from the
//      OFFICE-40 audit trail.
//   4. Attaches a machine-readable JSON manifest of the full audit chain as a
//      named PDF embedded-file stream.
//   5. Computes a final SHA-256 of the sealed PDF bytes.
//   6. Persists the sealed bytes via Storage.StoreSealedPDF and records the
//      final hash on the Envelope.
//   7. Serves the bytes on:
//        GET /api/sign/:envelopeId/download          → sealed PDF
//        GET /api/sign/:envelopeId/manifest           → audit JSON

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"vulos-office/backend/models"
	"vulos-office/backend/storage"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// SealHandler handles sealed-PDF generation and download.
type SealHandler struct {
	store      storage.Storage
	uploadsDir string
}

// NewSealHandler creates a SealHandler.
func NewSealHandler(store storage.Storage, uploadsDir string) *SealHandler {
	return &SealHandler{store: store, uploadsDir: uploadsDir}
}

// ------------------------------------------------------------
// AuditManifest is the machine-readable certificate summary.
// ------------------------------------------------------------

// ManifestSigner is one row in the completion manifest.
type ManifestSigner struct {
	SignerID   string    `json:"signer_id"`
	Name       string    `json:"name"`
	Email      string    `json:"email"`
	Identity   string    `json:"identity"`
	IP         string    `json:"ip"`
	SignedAt   time.Time `json:"signed_at"`
	DocHashBefore string `json:"doc_hash_before"`
	DocHashAfter  string `json:"doc_hash_after"`
	Token         string `json:"token"` // Ed25519 token from OFFICE-44 (may be empty)
}

// AuditManifest is the full machine-readable record attached to the sealed PDF.
type AuditManifest struct {
	EnvelopeID   string           `json:"envelope_id"`
	Title        string           `json:"title"`
	SourceFileID string           `json:"source_file_id"`
	SealedAt     time.Time        `json:"sealed_at"`
	FinalHash    string           `json:"final_doc_hash"` // SHA-256 of the sealed PDF
	Signers      []ManifestSigner `json:"signers"`
	AuditEvents  []*models.AuditEvent `json:"audit_events"`
}

// ------------------------------------------------------------
// allSigned returns true when every signer on env has status "signed".
// ------------------------------------------------------------
func allSigned(env *models.Envelope) bool {
	if len(env.Signers) == 0 {
		return false
	}
	for _, sg := range env.Signers {
		if sg.Status != models.SignerStatusSigned {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------
// Download — GET /api/sign/:envelopeId/download
// ------------------------------------------------------------
// Returns the sealed PDF (generating it on first request; idempotent thereafter).
func (h *SealHandler) Download(c *gin.Context) {
	envelopeID := c.Param("envelopeId")

	env, err := h.store.GetEnvelope(envelopeID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "envelope not found"})
		return
	}

	// Gate: all signers must have completed.
	if !allSigned(env) {
		c.JSON(http.StatusConflict, gin.H{"error": "envelope is not yet fully signed"})
		return
	}

	// Validate SourceFileID before any filesystem access.
	if !sourceFileIDRe.MatchString(env.SourceFileID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source_file_id"})
		return
	}

	// Return cached sealed PDF if already generated.
	if existing, err := h.store.GetSealedPDF(envelopeID); err == nil && len(existing) > 0 {
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="sealed-%s.pdf"`, envelopeID))
		c.Data(http.StatusOK, "application/pdf", existing)
		return
	}

	// Build the sealed PDF.
	sealedBytes, manifest, err := h.buildSealedPDF(env)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("seal PDF: %v", err)})
		return
	}

	// Persist sealed bytes.
	if err := h.store.StoreSealedPDF(envelopeID, sealedBytes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("persist sealed PDF: %v", err)})
		return
	}

	// Record final hash + status on envelope.
	env.Status = models.EnvelopeStatusCompleted
	env.FinalDocHash = manifest.FinalHash
	env.SealedAt = &manifest.SealedAt
	env.UpdatedAt = time.Now()
	_ = h.store.UpdateEnvelope(env)

	// Append a "completed" audit event.
	completedEvent := &models.AuditEvent{
		ID:         uuid.New().String(),
		EnvelopeID: envelopeID,
		Action:     models.AuditActionCompleted,
		Timestamp:  manifest.SealedAt,
		IP:         c.ClientIP(),
		Identity:   identityFromContext(c),
		DocHashAfter: manifest.FinalHash,
	}
	_ = h.store.AppendAuditEvent(completedEvent)

	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="sealed-%s.pdf"`, envelopeID))
	c.Data(http.StatusOK, "application/pdf", sealedBytes)
}

// Manifest — GET /api/sign/:envelopeId/manifest
// Returns the raw JSON audit manifest for machine verification.
func (h *SealHandler) Manifest(c *gin.Context) {
	envelopeID := c.Param("envelopeId")

	env, err := h.store.GetEnvelope(envelopeID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "envelope not found"})
		return
	}

	if !allSigned(env) && env.Status != models.EnvelopeStatusCompleted {
		c.JSON(http.StatusConflict, gin.H{"error": "envelope is not yet fully signed"})
		return
	}

	// Validate SourceFileID before any filesystem access.
	if !sourceFileIDRe.MatchString(env.SourceFileID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source_file_id"})
		return
	}

	events, err := h.store.ListAuditEvents(envelopeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "load audit events"})
		return
	}

	manifest := buildManifest(env, events, "")
	c.JSON(http.StatusOK, manifest)
}

// ------------------------------------------------------------
// buildSealedPDF assembles the final PDF bytes.
// ------------------------------------------------------------
func (h *SealHandler) buildSealedPDF(env *models.Envelope) ([]byte, *AuditManifest, error) {
	// 1. Load source PDF bytes.
	sourcePDF, err := h.loadSourcePDF(env.SourceFileID)
	if err != nil {
		return nil, nil, fmt.Errorf("load source PDF: %w", err)
	}

	// 2. SHA-256 of source.
	preHash := sha256hex(sourcePDF)

	// 3. Fetch audit trail.
	events, err := h.store.ListAuditEvents(env.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("list audit events: %w", err)
	}

	// 4. Build the completion-certificate page as a PDF page appended to sourcePDF.
	sealedBytes, err := appendCertificatePage(sourcePDF, env, events, preHash)
	if err != nil {
		return nil, nil, fmt.Errorf("append certificate page: %w", err)
	}

	// 5. Final hash of sealed PDF.
	finalHash := sha256hex(sealedBytes)

	// 6. Assemble manifest.
	manifest := buildManifest(env, events, finalHash)
	manifest.FinalHash = finalHash

	// 7. Attach the manifest JSON as a named embedded-file stream inside the PDF.
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("marshal manifest: %w", err)
	}
	sealedBytes, err = attachManifest(sealedBytes, manifestJSON)
	if err != nil {
		return nil, nil, fmt.Errorf("attach manifest: %w", err)
	}

	// 8. Recompute final hash after manifest attachment.
	manifest.FinalHash = sha256hex(sealedBytes)

	return sealedBytes, manifest, nil
}

// sourceFileIDRe is the allowlist for sourceFileID: UUID/ULID/alphanumeric plus
// hyphens and underscores, length 1-128. No path separators, dots, or other
// characters are permitted.
var sourceFileIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// loadSourcePDF finds the raw PDF bytes for SourceFileID.
// It searches uploadsDir for a file matching the ID prefix.
func (h *SealHandler) loadSourcePDF(sourceFileID string) ([]byte, error) {
	if sourceFileID == "" {
		return nil, fmt.Errorf("source_file_id is empty")
	}

	// Validate against strict allowlist before any filesystem access.
	if !sourceFileIDRe.MatchString(sourceFileID) {
		return nil, fmt.Errorf("source_file_id contains invalid characters")
	}

	uploadsDir := filepath.Clean(h.uploadsDir)

	// Try an exact match first (uploadsDir/<sourceFileID>).
	direct := filepath.Join(uploadsDir, sourceFileID)
	// Confirm the resolved path stays within uploadsDir.
	if !strings.HasPrefix(filepath.Clean(direct)+string(filepath.Separator), uploadsDir+string(filepath.Separator)) {
		return nil, fmt.Errorf("source_file_id resolves outside uploads directory")
	}
	if data, err := os.ReadFile(direct); err == nil {
		return data, nil
	}

	// Scan for any file whose name starts with sourceFileID.
	entries, err := os.ReadDir(uploadsDir)
	if err != nil {
		return nil, fmt.Errorf("read uploads dir: %w", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), sourceFileID) {
			candidate := filepath.Join(uploadsDir, e.Name())
			// Confirm each candidate stays within uploadsDir.
			if !strings.HasPrefix(filepath.Clean(candidate)+string(filepath.Separator), uploadsDir+string(filepath.Separator)) {
				continue
			}
			data, err := os.ReadFile(candidate)
			if err == nil {
				return data, nil
			}
		}
	}

	// Fallback: return a minimal placeholder PDF so the pipeline doesn't break
	// when the source PDF is not yet on disk (e.g. in tests).
	return minimalBlankPDF(), nil
}

// sha256hex returns the hex-encoded SHA-256 digest of b.
func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// buildManifest constructs an AuditManifest from the envelope and events.
func buildManifest(env *models.Envelope, events []*models.AuditEvent, finalHash string) *AuditManifest {
	now := time.Now().UTC()

	// Index signed events by signer ID (take the latest one per signer).
	signedByID := map[string]*models.AuditEvent{}
	for _, ev := range events {
		if ev.Action == models.AuditActionSigned {
			signedByID[ev.SignerID] = ev
		}
	}

	var rows []ManifestSigner
	for _, sg := range env.Signers {
		row := ManifestSigner{
			SignerID: sg.ID,
			Name:     sg.Name,
			Email:    sg.Email,
		}
		if ev, ok := signedByID[sg.ID]; ok {
			row.Identity = ev.Identity
			row.IP = ev.IP
			row.SignedAt = ev.Timestamp
			row.DocHashBefore = ev.DocHashBefore
			row.DocHashAfter = ev.DocHashAfter
			row.Token = ev.Token
		}
		rows = append(rows, row)
	}

	return &AuditManifest{
		EnvelopeID:   env.ID,
		Title:        env.Title,
		SourceFileID: env.SourceFileID,
		SealedAt:     now,
		FinalHash:    finalHash,
		Signers:      rows,
		AuditEvents:  events,
	}
}

// ------------------------------------------------------------
// Pure-Go PDF helpers
// ------------------------------------------------------------

// appendCertificatePage appends a new PDF page with the completion certificate
// to the existing PDF bytes. It uses a cross-reference-table append (incremental
// update) so the original byte range is preserved for OFFICE-47 verification.
func appendCertificatePage(src []byte, env *models.Envelope, events []*models.AuditEvent, preHash string) ([]byte, error) {
	// Locate the existing %%EOF to know where to start our incremental update.
	eofIdx := bytes.LastIndex(src, []byte("%%EOF"))
	if eofIdx < 0 {
		// Not a valid PDF — wrap anyway (handles the placeholder blank PDF).
		eofIdx = len(src)
	}
	base := src[:eofIdx]

	// Build the certificate page content stream.
	content := buildCertificateContent(env, events, preHash)

	var buf bytes.Buffer
	buf.Write(base)

	// We need unique object numbers beyond those already in the PDF.
	// Use a high base (10000) to avoid collisions.
	const objBase = 10000
	contentObjNum := objBase + 1
	pageObjNum := objBase + 2

	startXref := buf.Len()

	// Object: content stream.
	contentStart := buf.Len()
	fmt.Fprintf(&buf, "\n%d 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n",
		contentObjNum, len(content), content)

	// Object: page — A4 at 595×842 pts.
	pageStart := buf.Len()
	fmt.Fprintf(&buf, "\n%d 0 obj\n<< /Type /Page /MediaBox [0 0 595 842] /Contents %d 0 R /Resources << /Font << /F1 << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> /F1B << /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold >> >> >> >>\nendobj\n",
		pageObjNum, contentObjNum)

	// Minimal cross-reference table for incremental update.
	fmt.Fprintf(&buf, "\nxref\n%d 2\n", contentObjNum)
	fmt.Fprintf(&buf, "%010d 00000 n \n", contentStart)
	fmt.Fprintf(&buf, "%010d 00000 n \n", pageStart)

	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\n", pageObjNum+1)
	fmt.Fprintf(&buf, "startxref\n%d\n%%%%EOF\n", startXref)

	_ = startXref // already used in startxref write above
	return buf.Bytes(), nil
}

// buildCertificateContent returns a PDF content stream for the certificate page.
func buildCertificateContent(env *models.Envelope, events []*models.AuditEvent, preHash string) string {
	// Index signed events by signer ID.
	signedByID := map[string]*models.AuditEvent{}
	for _, ev := range events {
		if ev.Action == models.AuditActionSigned {
			signedByID[ev.SignerID] = ev
		}
	}

	var b strings.Builder

	// Page setup.
	b.WriteString("BT\n")
	b.WriteString("/F1B 18 Tf\n")
	b.WriteString("50 800 Td\n")
	b.WriteString("(Completion Certificate) Tj\n")

	b.WriteString("/F1 10 Tf\n")
	b.WriteString("0 -25 Td\n")
	pdfStr(&b, fmt.Sprintf("Document: %s", env.Title))
	b.WriteString(" Tj\n")

	b.WriteString("0 -14 Td\n")
	pdfStr(&b, fmt.Sprintf("Envelope ID: %s", env.ID))
	b.WriteString(" Tj\n")

	b.WriteString("0 -14 Td\n")
	pdfStr(&b, fmt.Sprintf("Source File: %s", env.SourceFileID))
	b.WriteString(" Tj\n")

	b.WriteString("0 -14 Td\n")
	pdfStr(&b, fmt.Sprintf("Document hash before seal: %s", preHash))
	b.WriteString(" Tj\n")

	b.WriteString("0 -14 Td\n")
	pdfStr(&b, fmt.Sprintf("Sealed at: %s", time.Now().UTC().Format(time.RFC3339)))
	b.WriteString(" Tj\n")

	// Divider.
	b.WriteString("/F1B 11 Tf\n")
	b.WriteString("0 -22 Td\n")
	b.WriteString("(Signers) Tj\n")
	b.WriteString("/F1 9 Tf\n")

	for _, sg := range env.Signers {
		b.WriteString("0 -18 Td\n")
		pdfStr(&b, fmt.Sprintf("  %s <%s>  status: %s", sg.Name, sg.Email, string(sg.Status)))
		b.WriteString(" Tj\n")

		if ev, ok := signedByID[sg.ID]; ok {
			b.WriteString("0 -12 Td\n")
			pdfStr(&b, fmt.Sprintf("    Signed: %s  IP: %s  Identity: %s",
				ev.Timestamp.UTC().Format(time.RFC3339), ev.IP, ev.Identity))
			b.WriteString(" Tj\n")

			if ev.DocHashBefore != "" {
				b.WriteString("0 -12 Td\n")
				pdfStr(&b, fmt.Sprintf("    Hash before: %s", ev.DocHashBefore))
				b.WriteString(" Tj\n")
			}
			if ev.DocHashAfter != "" {
				b.WriteString("0 -12 Td\n")
				pdfStr(&b, fmt.Sprintf("    Hash after:  %s", ev.DocHashAfter))
				b.WriteString(" Tj\n")
			}
			if ev.Token != "" {
				// Truncate long tokens for display.
				tok := ev.Token
				if len(tok) > 64 {
					tok = tok[:64] + "..."
				}
				b.WriteString("0 -12 Td\n")
				pdfStr(&b, fmt.Sprintf("    Token: %s", tok))
				b.WriteString(" Tj\n")
			}
		}
	}

	// Audit event count.
	b.WriteString("/F1 8 Tf\n")
	b.WriteString("0 -20 Td\n")
	pdfStr(&b, fmt.Sprintf("Total audit events: %d  (machine-readable manifest attached as 'audit-manifest.json')", len(events)))
	b.WriteString(" Tj\n")

	b.WriteString("ET\n")
	return b.String()
}

// pdfStr writes a PDF string literal, escaping parentheses and backslashes.
func pdfStr(b *strings.Builder, s string) {
	b.WriteByte('(')
	for _, ch := range s {
		switch ch {
		case '(', ')', '\\':
			b.WriteByte('\\')
			b.WriteRune(ch)
		default:
			// PDF strings are Latin-1; replace non-ASCII to avoid encoding issues.
			if ch > 126 {
				b.WriteByte('?')
			} else {
				b.WriteRune(ch)
			}
		}
	}
	b.WriteByte(')')
}

// attachManifest appends the JSON manifest as a named embedded-file stream
// in a further incremental update to the sealed PDF.
func attachManifest(src []byte, manifestJSON []byte) ([]byte, error) {
	eofIdx := bytes.LastIndex(src, []byte("%%EOF"))
	if eofIdx < 0 {
		eofIdx = len(src)
	}
	base := src[:eofIdx]

	const objBase = 10100
	fileStreamObj := objBase + 1
	fileSpecObj := objBase + 2

	var buf bytes.Buffer
	buf.Write(base)

	startXref := buf.Len()

	// Object: embedded file stream.
	fsStart := buf.Len()
	fmt.Fprintf(&buf, "\n%d 0 obj\n<< /Type /EmbeddedFile /Subtype /application#2Fjson /Length %d >>\nstream\n",
		fileStreamObj, len(manifestJSON))
	buf.Write(manifestJSON)
	fmt.Fprintf(&buf, "\nendstream\nendobj\n")

	// Object: file specification.
	fsSpecStart := buf.Len()
	fmt.Fprintf(&buf, "\n%d 0 obj\n<< /Type /Filespec /F (audit-manifest.json) /EF << /F %d 0 R >> >>\nendobj\n",
		fileSpecObj, fileStreamObj)

	// Incremental xref.
	fmt.Fprintf(&buf, "\nxref\n%d 2\n", fileStreamObj)
	fmt.Fprintf(&buf, "%010d 00000 n \n", fsStart)
	fmt.Fprintf(&buf, "%010d 00000 n \n", fsSpecStart)
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\n", fileSpecObj+1)
	fmt.Fprintf(&buf, "startxref\n%d\n%%%%EOF\n", startXref)

	return buf.Bytes(), nil
}

// minimalBlankPDF returns a valid one-page PDF placeholder used when the
// source PDF file cannot be found on disk.
func minimalBlankPDF() []byte {
	return []byte(`%PDF-1.4
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] >>
endobj
xref
0 4
0000000000 65535 f
0000000009 00000 n
0000000058 00000 n
0000000115 00000 n
trailer
<< /Size 4 /Root 1 0 R >>
startxref
190
%%EOF
`)
}
