package handlers

import (
	"net/http"
	"time"

	"vulos-office/backend/models"
	"vulos-office/backend/storage"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// EnvelopeHandler implements CRUD for signing envelopes (OFFICE-41).
type EnvelopeHandler struct {
	store storage.Storage
}

func NewEnvelopeHandler(store storage.Storage) *EnvelopeHandler {
	return &EnvelopeHandler{store: store}
}

// List — GET /api/envelopes
func (h *EnvelopeHandler) List(c *gin.Context) {
	envs, err := h.store.ListEnvelopes()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if envs == nil {
		envs = []*models.Envelope{}
	}
	c.JSON(http.StatusOK, envs)
}

// Get — GET /api/envelopes/:id
func (h *EnvelopeHandler) Get(c *gin.Context) {
	env, err := h.store.GetEnvelope(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "envelope not found"})
		return
	}
	c.JSON(http.StatusOK, env)
}

// Create — POST /api/envelopes
// Accepts a full Envelope payload (including fields + signers) from the
// frontend SigningSetup component.
func (h *EnvelopeHandler) Create(c *gin.Context) {
	var env models.Envelope
	if err := c.ShouldBindJSON(&env); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	now := time.Now().UTC()
	if env.ID == "" {
		env.ID = uuid.New().String()
	}
	env.CreatedAt = now
	env.UpdatedAt = now
	if env.Status == "" {
		env.Status = models.EnvelopeStatusDraft
	}
	if env.OrderMode == "" {
		env.OrderMode = models.SigningOrderSequential
	}

	// Assign IDs to fields and signers if not already set.
	for _, f := range env.Fields {
		if f.ID == "" {
			f.ID = uuid.New().String()
		}
	}
	for _, s := range env.Signers {
		if s.ID == "" {
			s.ID = uuid.New().String()
		}
		if s.EnvelopeID == "" {
			s.EnvelopeID = env.ID
		}
		s.CreatedAt = now
		s.UpdatedAt = now
		if s.Status == "" {
			s.Status = models.SignerStatusPending
		}
	}

	if err := h.store.CreateEnvelope(&env); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Emit a "created" audit event.
	audit := &models.AuditEvent{
		ID:         uuid.New().String(),
		EnvelopeID: env.ID,
		Action:     models.AuditActionCreated,
		Timestamp:  now,
	}
	_ = h.store.AppendAuditEvent(audit) // best-effort

	c.JSON(http.StatusCreated, env)
}

// Update — PUT /api/envelopes/:id
// Replaces the full envelope (fields + signers included).
func (h *EnvelopeHandler) Update(c *gin.Context) {
	id := c.Param("id")
	existing, err := h.store.GetEnvelope(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "envelope not found"})
		return
	}

	var env models.Envelope
	if err := c.ShouldBindJSON(&env); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	env.ID = id
	env.CreatedAt = existing.CreatedAt
	env.UpdatedAt = time.Now().UTC()
	if env.Status == "" {
		env.Status = existing.Status
	}
	if env.OrderMode == "" {
		env.OrderMode = existing.OrderMode
	}

	now := env.UpdatedAt
	for _, f := range env.Fields {
		if f.ID == "" {
			f.ID = uuid.New().String()
		}
	}
	for _, s := range env.Signers {
		if s.ID == "" {
			s.ID = uuid.New().String()
		}
		if s.EnvelopeID == "" {
			s.EnvelopeID = id
		}
		if s.CreatedAt.IsZero() {
			s.CreatedAt = now
		}
		s.UpdatedAt = now
		if s.Status == "" {
			s.Status = models.SignerStatusPending
		}
	}

	if err := h.store.UpdateEnvelope(&env); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, env)
}

// Delete — DELETE /api/envelopes/:id
func (h *EnvelopeHandler) Delete(c *gin.Context) {
	if err := h.store.DeleteEnvelope(c.Param("id")); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "envelope not found"})
		return
	}
	c.JSON(http.StatusNoContent, nil)
}
