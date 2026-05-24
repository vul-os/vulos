package handlers

// signing.go — OFFICE-42 + OFFICE-44: signing-link generation + scoped signer view +
// cryptographic token + tamper-evident audit trail.
//
// Routes (registered in main.go):
//   POST /api/sign/:envelopeId/send    → issue one opaque token per signer
//   GET  /api/sign/:token              → resolve token, check order, log viewed, return scoped view
//   POST /api/sign/:token/complete     → accept field values, compute hashes, issue Ed25519 token, chain audit

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"vulos-office/backend/models"
	"vulos-office/backend/signing"
	"vulos-office/backend/storage"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// TokenTTL is how long a signing link remains valid.
const TokenTTL = 7 * 24 * time.Hour

type SigningHandler struct {
	store storage.Storage
}

func NewSigningHandler(store storage.Storage) *SigningHandler {
	return &SigningHandler{store: store}
}

// Send issues one scoped token per signer for the given envelope.
//
//	POST /api/sign/:envelopeId/send
//
// AC: send issues one scoped token per signer.
func (h *SigningHandler) Send(c *gin.Context) {
	envelopeID := c.Param("envelopeId")

	env, err := h.store.GetEnvelope(envelopeID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "envelope not found"})
		return
	}

	if len(env.Signers) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no signers on this envelope"})
		return
	}

	tokens := make(map[string]string) // signerID → token
	links := make(map[string]string)  // signerID → /sign/<token>

	for _, sg := range env.Signers {
		// Mint a new opaque UUID token for this signer.
		token := uuid.New().String()
		expiry := time.Now().Add(TokenTTL)

		sg.Token = token
		sg.TokenExpiry = &expiry
		sg.Status = models.SignerStatusSent
		sg.UpdatedAt = time.Now()

		// Persist the signer with the new token.
		if err := h.store.UpsertSigner(sg); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": fmt.Sprintf("persist token for signer %s: %v", sg.ID, err),
			})
			return
		}

		// Persist the token → {envelopeID, signerID} index for O(1) lookup.
		if err := h.store.StoreSignerToken(token, envelopeID, sg.ID); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": fmt.Sprintf("store token for signer %s: %v", sg.ID, err),
			})
			return
		}

		tokens[sg.ID] = token
		links[sg.ID] = "/sign/" + token
	}

	// Mark the envelope as sent.
	env.Status = models.EnvelopeStatusSent
	env.UpdatedAt = time.Now()
	if err := h.store.UpdateEnvelope(env); err != nil {
		// Non-fatal — tokens are already issued.
	}

	// Emit a "sent" audit event for the envelope.
	sentEvent := &models.AuditEvent{
		ID:         uuid.New().String(),
		EnvelopeID: envelopeID,
		Action:     models.AuditActionSent,
		Timestamp:  time.Now(),
		IP:         c.ClientIP(),
		Identity:   identityFromContext(c),
	}
	_ = h.store.AppendAuditEvent(sentEvent)

	c.JSON(http.StatusOK, gin.H{
		"envelope_id": envelopeID,
		"tokens":      tokens,
		"links":       links,
	})
}

// GetSignerView resolves a token, enforces signing order, logs "viewed", and
// returns only the fields assigned to this signer.
//
//	GET /api/sign/:token
//
// AC:
//   - /sign/:token shows only that signer's fields
//   - open logs a "viewed" audit event
//   - out-of-order signer link → 403/locked
func (h *SigningHandler) GetSignerView(c *gin.Context) {
	token := c.Param("token")

	// Resolve token to envelope + signer.
	envelopeID, signerID, err := h.store.ResolveToken(token)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "signing link not found or expired"})
		return
	}

	env, err := h.store.GetEnvelope(envelopeID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "envelope not found"})
		return
	}

	// Find the signer.
	var signer *models.Signer
	for _, sg := range env.Signers {
		if sg.ID == signerID {
			signer = sg
			break
		}
	}
	if signer == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "signer not found on envelope"})
		return
	}

	// Validate token hasn't expired.
	if signer.TokenExpiry != nil && time.Now().After(*signer.TokenExpiry) {
		c.JSON(http.StatusGone, gin.H{"error": "signing link has expired"})
		return
	}

	// Enforce signing order: in sequential mode, a signer may only proceed
	// once all signers with a lower order value have status "signed".
	locked := false
	if env.OrderMode == models.SigningOrderSequential {
		for _, sg := range env.Signers {
			if sg.Order < signer.Order && sg.Status != models.SignerStatusSigned {
				locked = true
				break
			}
		}
	}
	if locked {
		// Return 403 with a machine-readable locked flag so the UI can show
		// a friendly "waiting for prior signers" message.
		c.JSON(http.StatusForbidden, gin.H{
			"error":  "waiting for prior signers to complete",
			"locked": true,
		})
		return
	}

	// Filter fields to only this signer's assignments.
	var myFields []*models.SigningField
	for _, f := range env.Fields {
		if f.SignerID == signerID {
			myFields = append(myFields, f)
		}
	}

	// Log "viewed" (idempotent — repeated opens produce additional events;
	// the handler for OFFICE-44 will deduplicate by status if needed).
	if signer.Status == models.SignerStatusSent || signer.Status == models.SignerStatusPending {
		signer.Status = models.SignerStatusViewed
		signer.UpdatedAt = time.Now()
		_ = h.store.UpsertSigner(signer)

		viewedEvent := &models.AuditEvent{
			ID:         uuid.New().String(),
			EnvelopeID: envelopeID,
			SignerID:   signerID,
			Action:     models.AuditActionViewed,
			Timestamp:  time.Now(),
			IP:         c.ClientIP(),
			Identity:   fmt.Sprintf("link:%s", token[:8]),
		}
		_ = h.store.AppendAuditEvent(viewedEvent)
	}

	c.JSON(http.StatusOK, gin.H{
		"envelope_id": envelopeID,
		"signer_id":   signerID,
		"signer_name": signer.Name,
		"source_file": env.SourceFileID,
		"fields":      myFields,
		"locked":      false,
		"order_mode":  env.OrderMode,
	})
}

// Complete accepts a signer's filled field values, records doc hashes, issues an
// Ed25519 crypto token, and appends a hash-chained "signed" audit event.
//
//	POST /api/sign/:token/complete
//
// AC (OFFICE-44):
//   - each signature yields a verifiable Ed25519 token bound to the doc hash
//   - audit log is hash-chained + append-only
//   - before/after hashes recorded per signer
//   - identity captured (Vulos account or link)
func (h *SigningHandler) Complete(c *gin.Context) {
	token := c.Param("token")

	// Resolve token to envelope + signer.
	envelopeID, signerID, err := h.store.ResolveToken(token)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "signing link not found or expired"})
		return
	}

	env, err := h.store.GetEnvelope(envelopeID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "envelope not found"})
		return
	}

	// Find the signer.
	var signer *models.Signer
	for _, sg := range env.Signers {
		if sg.ID == signerID {
			signer = sg
			break
		}
	}
	if signer == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "signer not found on envelope"})
		return
	}

	// Token expiry check.
	if signer.TokenExpiry != nil && time.Now().After(*signer.TokenExpiry) {
		c.JSON(http.StatusGone, gin.H{"error": "signing link has expired"})
		return
	}

	// Signer must not have already completed.
	if signer.Status == models.SignerStatusSigned || signer.Status == models.SignerStatusDeclined {
		c.JSON(http.StatusConflict, gin.H{"error": "signer has already completed"})
		return
	}

	// Parse body: map of fieldID → value.
	var fieldValues map[string]string
	if err := c.ShouldBindJSON(&fieldValues); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: expected {field_id: value, ...}"})
		return
	}

	// --- Compute doc_hash_before: hash of the current envelope state ---
	docBefore, err := json.Marshal(env)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "marshal envelope for hash"})
		return
	}
	docHashBefore := signing.HashDocument(docBefore)

	// Apply field values to the envelope.
	for _, f := range env.Fields {
		if f.SignerID == signerID {
			if val, ok := fieldValues[f.ID]; ok {
				f.Value = val
			}
		}
	}

	// --- Compute doc_hash_after: hash of the updated envelope ---
	docAfter, err := json.Marshal(env)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "marshal envelope after fill"})
		return
	}
	docHashAfter := signing.HashDocument(docAfter)

	// Determine identity: prefer authenticated account, fall back to link identity.
	identity := identityFromContext(c)
	if identity == "" {
		identity = signer.Email
		if identity == "" {
			identity = fmt.Sprintf("link:%s", token[:8])
		}
	}

	// --- Generate Ed25519 crypto token ---
	cryptoToken, err := signing.GenerateToken(signing.TokenPayload{
		EnvelopeID:    envelopeID,
		SignerID:      signerID,
		DocHashBefore: docHashBefore,
		DocHashAfter:  docHashAfter,
		Timestamp:     time.Now().Unix(),
		Identity:      identity,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("generate crypto token: %v", err)})
		return
	}

	// --- Build hash-chain: load prior events, compute prevHash ---
	priorEvents, err := h.store.ListAuditEvents(envelopeID)
	if err != nil {
		// Non-fatal: chain will start fresh if list fails.
		priorEvents = nil
	}
	chainInputs := auditEventsToChainInputs(priorEvents)
	prevHash, err := signing.LatestEventHash(chainInputs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("compute prev event hash: %v", err)})
		return
	}

	// --- Persist "signed" audit event (immutable, hash-chained) ---
	signedEvent := &models.AuditEvent{
		ID:            uuid.New().String(),
		EnvelopeID:    envelopeID,
		SignerID:      signerID,
		Action:        models.AuditActionSigned,
		Timestamp:     time.Now().UTC(),
		IP:            c.ClientIP(),
		Identity:      identity,
		DocHashBefore: docHashBefore,
		DocHashAfter:  docHashAfter,
		Token:         cryptoToken,
		PrevEventHash: prevHash,
	}
	if err := h.store.AppendAuditEvent(signedEvent); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("append audit event: %v", err)})
		return
	}

	// --- Update signer status + persist updated envelope ---
	signer.Status = models.SignerStatusSigned
	signer.UpdatedAt = time.Now()
	_ = h.store.UpsertSigner(signer)

	env.UpdatedAt = time.Now()
	// Check if all signers are now signed; if so, mark envelope completed.
	allSigned := true
	for _, sg := range env.Signers {
		if sg.ID == signerID {
			continue // just updated in-memory
		}
		if sg.Status != models.SignerStatusSigned {
			allSigned = false
			break
		}
	}
	if allSigned {
		env.Status = models.EnvelopeStatusCompleted
	}
	_ = h.store.UpdateEnvelope(env)

	// OFFICE-45: advance the sequential orchestration state machine.
	// For sequential envelopes, unlock the next signer(s) now that this one is done.
	AdvanceOrchestration(h.store, env)

	c.JSON(http.StatusOK, gin.H{
		"envelope_id":     envelopeID,
		"signer_id":       signerID,
		"doc_hash_before": docHashBefore,
		"doc_hash_after":  docHashAfter,
		"crypto_token":    cryptoToken,
		"audit_event_id":  signedEvent.ID,
		"prev_event_hash": prevHash,
	})
}

// auditEventsToChainInputs converts stored AuditEvent records to the stable
// chain-input projection used for hashing.
func auditEventsToChainInputs(events []*models.AuditEvent) []signing.AuditChainInput {
	inputs := make([]signing.AuditChainInput, 0, len(events))
	for _, ev := range events {
		inputs = append(inputs, signing.AuditChainInput{
			ID:            ev.ID,
			EnvelopeID:    ev.EnvelopeID,
			SignerID:       ev.SignerID,
			Action:        string(ev.Action),
			Timestamp:     ev.Timestamp,
			IP:            ev.IP,
			Identity:      ev.Identity,
			DocHashBefore: ev.DocHashBefore,
			DocHashAfter:  ev.DocHashAfter,
			Token:         ev.Token,
			PrevEventHash: ev.PrevEventHash,
		})
	}
	return inputs
}

// identityFromContext extracts a Vulos account identity from the request if
// auth is enabled, else returns an empty string.
func identityFromContext(c *gin.Context) string {
	if v, ok := c.Get("account_id"); ok {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}
