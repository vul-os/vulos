// contacts_handler.go — VCF import/export and dedup API endpoints.
//
// Routes (all protected):
//
//	POST /api/contacts/import          — import .vcf file (multipart form "file")
//	GET  /api/contacts/export          — export all contacts as .vcf
//	GET  /api/contacts/duplicates      — find potential duplicates
//	POST /api/contacts/merge           — merge two contacts
//
// Contact CRUD (create/read/update/delete) is handled by the existing CardDAV
// layer; these endpoints are additive and focus on import/export and
// server-side dedup.
package handlers

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"vulos-office/backend/services/contacts_vcf"
)

// ─── in-memory contact store (supplements CardDAV) ────────────────────────────

type contactStore struct {
	mu       sync.RWMutex
	contacts map[string]*contacts_vcf.Contact
}

var ctStore = &contactStore{contacts: map[string]*contacts_vcf.Contact{}}

func (s *contactStore) list() []*contacts_vcf.Contact {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*contacts_vcf.Contact, 0, len(s.contacts))
	for _, c := range s.contacts {
		out = append(out, c)
	}
	return out
}

func (s *contactStore) put(c *contacts_vcf.Contact) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contacts[c.UID] = c
}

func (s *contactStore) delete(uid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.contacts, uid)
}

// ─── handler ──────────────────────────────────────────────────────────────────

// ContactsVCFHandler handles VCF import/export and dedup.
type ContactsVCFHandler struct{}

func NewContactsVCFHandler() *ContactsVCFHandler { return &ContactsVCFHandler{} }

// ImportVCF POST /api/contacts/import  multipart "file" field.
func (h *ContactsVCFHandler) ImportVCF(c *gin.Context) {
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing file field: " + err.Error()})
		return
	}
	defer file.Close()

	contacts, err := contacts_vcf.Import(file)
	if err != nil && len(contacts) == 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}

	now := time.Now().UTC()
	var imported []contacts_vcf.Contact
	for _, contact := range contacts {
		if contact.UID == "" {
			contact.UID = uuid.NewString()
		}
		contact.CreatedAt = now
		contact.UpdatedAt = now
		ctStore.put(&contact)
		imported = append(imported, contact)
	}

	c.JSON(http.StatusOK, gin.H{
		"imported": len(imported),
		"contacts": imported,
		"warnings": func() string {
			if err != nil {
				return err.Error()
			}
			return ""
		}(),
	})
}

// ExportVCF GET /api/contacts/export?version=4.0
func (h *ContactsVCFHandler) ExportVCF(c *gin.Context) {
	version := c.DefaultQuery("version", "4.0")
	list := ctStore.list()
	contacts := make([]contacts_vcf.Contact, len(list))
	for i, c := range list {
		contacts[i] = *c
	}

	data, err := contacts_vcf.Export(contacts, version)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.Header("Content-Disposition", `attachment; filename="contacts.vcf"`)
	c.Data(http.StatusOK, "text/vcard; charset=utf-8", data)
}

// DuplicateCandidate is a pair of potentially duplicate contacts.
type DuplicateCandidate struct {
	A      contacts_vcf.Contact `json:"a"`
	B      contacts_vcf.Contact `json:"b"`
	Reason string               `json:"reason"` // "email" or "phone"
}

// FindDuplicates GET /api/contacts/duplicates
func (h *ContactsVCFHandler) FindDuplicates(c *gin.Context) {
	list := ctStore.list()
	var candidates []DuplicateCandidate

	// Index by email
	emailIndex := map[string][]string{} // email → []UID
	for _, ct := range list {
		for _, e := range ct.Emails {
			addr := strings.ToLower(strings.TrimSpace(e.Address))
			if addr == "" {
				continue
			}
			emailIndex[addr] = append(emailIndex[addr], ct.UID)
		}
	}

	// Index by phone (normalised: digits only)
	phoneIndex := map[string][]string{} // digits → []UID
	for _, ct := range list {
		for _, p := range ct.Phones {
			digits := normalisePhone(p.Number)
			if digits == "" {
				continue
			}
			phoneIndex[digits] = append(phoneIndex[digits], ct.UID)
		}
	}

	seen := map[string]bool{}
	pairKey := func(a, b string) string {
		if a > b {
			a, b = b, a
		}
		return a + ":" + b
	}

	addPairs := func(uids []string, reason string) {
		for i := 0; i < len(uids); i++ {
			for j := i + 1; j < len(uids); j++ {
				k := pairKey(uids[i], uids[j])
				if seen[k] {
					continue
				}
				seen[k] = true

				ctStore.mu.RLock()
				a, aok := ctStore.contacts[uids[i]]
				b, bok := ctStore.contacts[uids[j]]
				ctStore.mu.RUnlock()

				if aok && bok {
					candidates = append(candidates, DuplicateCandidate{A: *a, B: *b, Reason: reason})
				}
			}
		}
	}

	for _, uids := range emailIndex {
		if len(uids) > 1 {
			addPairs(uids, "email")
		}
	}
	for _, uids := range phoneIndex {
		if len(uids) > 1 {
			addPairs(uids, "phone")
		}
	}

	c.JSON(http.StatusOK, gin.H{"candidates": candidates})
}

// MergeRequest is the body for POST /api/contacts/merge.
type MergeRequest struct {
	KeepUID   string `json:"keep_uid" binding:"required"`
	DeleteUID string `json:"delete_uid" binding:"required"`
}

// MergeContacts POST /api/contacts/merge
func (h *ContactsVCFHandler) MergeContacts(c *gin.Context) {
	var req MergeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctStore.mu.Lock()
	defer ctStore.mu.Unlock()

	keep, ok1 := ctStore.contacts[req.KeepUID]
	del, ok2 := ctStore.contacts[req.DeleteUID]
	if !ok1 || !ok2 {
		c.JSON(http.StatusNotFound, gin.H{"error": "one or both contacts not found"})
		return
	}

	// Merge: append missing emails/phones from del into keep.
	emailSet := map[string]bool{}
	for _, e := range keep.Emails {
		emailSet[strings.ToLower(e.Address)] = true
	}
	for _, e := range del.Emails {
		if !emailSet[strings.ToLower(e.Address)] {
			keep.Emails = append(keep.Emails, e)
		}
	}

	phoneSet := map[string]bool{}
	for _, p := range keep.Phones {
		phoneSet[normalisePhone(p.Number)] = true
	}
	for _, p := range del.Phones {
		if !phoneSet[normalisePhone(p.Number)] {
			keep.Phones = append(keep.Phones, p)
		}
	}

	if keep.Notes == "" && del.Notes != "" {
		keep.Notes = del.Notes
	}

	keep.UpdatedAt = time.Now().UTC()
	delete(ctStore.contacts, req.DeleteUID)

	c.JSON(http.StatusOK, keep)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func normalisePhone(s string) string {
	var buf bytes.Buffer
	for _, ch := range s {
		if ch >= '0' && ch <= '9' {
			buf.WriteRune(ch)
		}
	}
	return buf.String()
}

// ImportVCFFromBytes is a helper used in tests to import directly from bytes.
func ImportVCFFromBytes(data []byte) ([]contacts_vcf.Contact, error) {
	return contacts_vcf.Import(io.NopCloser(bytes.NewReader(data)))
}
